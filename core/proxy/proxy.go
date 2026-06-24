// Package proxy provides HTTP proxy infrastructure for c0wrk LLM API clients.
package proxy

import (
	"crypto/tls"
	"crypto/x509"
	"errors"
	"fmt"
	"log/slog"
	"net"
	"net/http"
	"net/url"
	"os"
	"path/filepath"
	"strings"
	"time"
)

// Config holds HTTP/HTTPS proxy settings.
type Config struct {
	Enabled    bool
	URL        string   // scheme://user:password@host:port
	BypassList []string // hostnames/IPs to skip proxy
	TLSCertDir string   // directory with .pem/.crt CA certs

	// SetGlobalEnv, when true, mutates HTTP_PROXY/HTTPS_PROXY/NO_PROXY/SSL_CERT_DIR
	// in the process environment so subprocesses inherit the proxy settings.
	// Default false — most callers should use the explicitly threaded *http.Client
	// returned by BuildClient instead. Mutating global env affects every child
	// process and other Go libraries that read these vars.
	SetGlobalEnv bool
}

// BuildTransport creates an *http.Transport configured with the given proxy settings.
// It sets up proxy routing, bypass list, and custom TLS CA certificates.
// Returns nil transport (no error) if proxy is disabled.
func BuildTransport(cfg Config, logger *slog.Logger) (*http.Transport, error) {
	if !cfg.Enabled || cfg.URL == "" {
		return nil, nil
	}

	proxyURL, err := parseProxyURL(cfg.URL)
	if err != nil {
		return nil, fmt.Errorf("invalid proxy URL: %w", err)
	}

	bypassSet := buildBypassSet(cfg.BypassList)

	proxyFunc := func(req *http.Request) (*url.URL, error) {
		host := req.URL.Hostname()
		if shouldBypass(host, bypassSet) {
			return nil, nil // direct connection
		}
		return proxyURL, nil
	}

	tlsCfg, err := buildTLSConfig(cfg.TLSCertDir, logger)
	if err != nil {
		return nil, fmt.Errorf("failed to build TLS config: %w", err)
	}

	transport := &http.Transport{
		Proxy: proxyFunc,
		DialContext: (&net.Dialer{
			Timeout:   10 * time.Second,
			KeepAlive: 30 * time.Second,
		}).DialContext,
		TLSClientConfig:       tlsCfg,
		ForceAttemptHTTP2:     true,
		MaxIdleConns:          100,
		IdleConnTimeout:       90 * time.Second,
		TLSHandshakeTimeout:   10 * time.Second,
		ExpectContinueTimeout: 1 * time.Second,
	}

	return transport, nil
}

// BuildClient creates an *http.Client configured with proxy settings and
// the given timeout. Returns nil (no error) if proxy is disabled.
func BuildClient(cfg Config, timeout time.Duration, logger *slog.Logger) (*http.Client, error) {
	transport, err := BuildTransport(cfg, logger)
	if err != nil {
		return nil, err
	}
	if transport == nil {
		return nil, nil
	}
	return &http.Client{
		Transport: transport,
		Timeout:   timeout,
	}, nil
}

// SetEnvVars sets HTTP_PROXY, HTTPS_PROXY, NO_PROXY, and SSL_CERT_DIR
// environment variables based on the proxy configuration.
// These are inherited by child processes (e.g. bash_exec).
func SetEnvVars(cfg Config) {
	if !cfg.Enabled || cfg.URL == "" {
		ClearEnvVars()
		return
	}
	_ = os.Setenv("HTTP_PROXY", cfg.URL)
	_ = os.Setenv("HTTPS_PROXY", cfg.URL)
	_ = os.Setenv("http_proxy", cfg.URL)
	_ = os.Setenv("https_proxy", cfg.URL)

	if len(cfg.BypassList) > 0 {
		noProxy := strings.Join(cfg.BypassList, ",")
		_ = os.Setenv("NO_PROXY", noProxy)
		_ = os.Setenv("no_proxy", noProxy)
	}

	if cfg.TLSCertDir != "" {
		_ = os.Setenv("SSL_CERT_DIR", cfg.TLSCertDir)
	}
}

// ClearEnvVars removes proxy-related environment variables.
func ClearEnvVars() {
	_ = os.Unsetenv("HTTP_PROXY")
	_ = os.Unsetenv("HTTPS_PROXY")
	_ = os.Unsetenv("http_proxy")
	_ = os.Unsetenv("https_proxy")
	_ = os.Unsetenv("NO_PROXY")
	_ = os.Unsetenv("no_proxy")
	_ = os.Unsetenv("SSL_CERT_DIR")
}

// MaskURL replaces the password in a proxy URL with "***" for safe display.
// Returns the original string if parsing fails.
func MaskURL(rawURL string) string {
	if rawURL == "" {
		return ""
	}
	parsed, err := url.Parse(rawURL)
	if err != nil {
		return rawURL
	}
	if parsed.User == nil {
		return rawURL
	}
	if _, hasPass := parsed.User.Password(); hasPass {
		parsed.User = url.UserPassword(parsed.User.Username(), "***")
	}
	return parsed.String()
}

// parseProxyURL parses and validates a proxy URL string.
func parseProxyURL(rawURL string) (*url.URL, error) {
	if rawURL == "" {
		return nil, errors.New("empty proxy URL")
	}

	// Add scheme if missing
	if !strings.Contains(rawURL, "://") {
		rawURL = "http://" + rawURL
	}

	parsed, err := url.Parse(rawURL)
	if err != nil {
		return nil, err
	}

	switch parsed.Scheme {
	case "http", "https", "socks5":
		// valid
	default:
		return nil, fmt.Errorf("unsupported proxy scheme %q (use http, https, or socks5)", parsed.Scheme)
	}

	if parsed.Host == "" {
		return nil, errors.New("proxy URL has no host")
	}

	return parsed, nil
}

// buildBypassSet creates a set of lowercase hostnames/IPs for fast lookup.
func buildBypassSet(bypassList []string) map[string]struct{} {
	set := make(map[string]struct{}, len(bypassList))
	for _, entry := range bypassList {
		set[strings.ToLower(strings.TrimSpace(entry))] = struct{}{}
	}
	return set
}

// shouldBypass checks whether a host should bypass the proxy.
func shouldBypass(host string, bypassSet map[string]struct{}) bool {
	host = strings.ToLower(host)
	if _, ok := bypassSet[host]; ok {
		return true
	}
	// Check for wildcard domain matches (e.g., *.example.com)
	for entry := range bypassSet {
		if strings.HasPrefix(entry, "*.") {
			suffix := entry[1:] // ".example.com"
			if strings.HasSuffix(host, suffix) {
				return true
			}
		}
	}
	return false
}

// buildTLSConfig creates a *tls.Config with custom CA certificates loaded from certDir.
// If certDir is empty, returns nil (default system pool will be used).
func buildTLSConfig(certDir string, logger *slog.Logger) (*tls.Config, error) {
	if certDir == "" {
		return nil, nil
	}

	pool, err := loadCertPool(certDir, logger)
	if err != nil {
		return nil, err
	}
	if pool == nil {
		return nil, nil
	}

	return &tls.Config{
		RootCAs:    pool,
		MinVersion: tls.VersionTLS12,
	}, nil
}

// loadCertPool reads all .pem and .crt files from certDir and appends them
// to the system CA pool. Returns nil pool if no valid certs were found.
func loadCertPool(certDir string, logger *slog.Logger) (*x509.CertPool, error) {
	info, err := os.Stat(certDir)
	if err != nil {
		return nil, fmt.Errorf("cert directory %q: %w", certDir, err)
	}
	if !info.IsDir() {
		return nil, fmt.Errorf("cert path %q is not a directory", certDir)
	}

	pool, err := x509.SystemCertPool()
	if err != nil {
		pool = x509.NewCertPool()
	}

	entries, err := os.ReadDir(certDir)
	if err != nil {
		return nil, fmt.Errorf("reading cert directory: %w", err)
	}

	loaded := 0
	for _, entry := range entries {
		if entry.IsDir() {
			continue
		}
		ext := strings.ToLower(filepath.Ext(entry.Name()))
		if ext != ".pem" && ext != ".crt" {
			continue
		}

		certPath := filepath.Join(certDir, entry.Name())
		data, err := os.ReadFile(certPath)
		if err != nil {
			if logger != nil {
				logger.Warn("skipping unreadable cert file", "path", certPath, "error", err)
			}
			continue
		}

		if pool.AppendCertsFromPEM(data) {
			loaded++
		} else if logger != nil {
			logger.Warn("no valid PEM certificates in file", "path", certPath)
		}
	}

	if loaded == 0 {
		if logger != nil {
			logger.Warn("no valid certificates found in cert directory", "dir", certDir)
		}
		return nil, nil
	}

	return pool, nil
}
