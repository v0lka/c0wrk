// Package llmtls provides per-provider TLS verification overrides for
// self-signed LLM endpoints: the pin is the switch (empty pin = system
// verification; no accept-any state); a proxy wins except on bypassed
// hosts. See specs/decisions/054-per-provider-tls-pinning.md.
package llmtls

import (
	"context"
	"crypto/sha256"
	"crypto/tls"
	"crypto/x509"
	"encoding/base64"
	"errors"
	"fmt"
	"log/slog"
	"net"
	"net/http"
	"net/url"
	"strings"
	"time"
)

// FetchFingerprintTimeout bounds the TLS handshake performed by
// FetchFingerprint.
const FetchFingerprintTimeout = 10 * time.Second

// ErrPinMismatch is returned (wrapped) on a pin mismatch; it carries no
// certificate material (specs/decisions/054-per-provider-tls-pinning.md).
var ErrPinMismatch = errors.New("fingerprint mismatch")

// DialPolicy answers whether a dial goes through the operator's proxy or
// straight to the endpoint — the gate for whether a pin applies (specs/
// decisions/054-per-provider-tls-pinning.md). ProxyActive is the
// effective-proxy rule; a bypassed target dials direct, so its pin re-arms.
type DialPolicy struct {
	ProxyActive    bool
	TargetBypassed bool
}

// ZeroDialPolicy is the no-proxy dial policy.
var ZeroDialPolicy = DialPolicy{}

// Client returns an HTTP client for one provider endpoint: empty pin → base
// unchanged (same pointer), non-empty pin → a clone whose transport accepts
// only the certificate whose SPKI SHA-256 (base64) equals the pin. base is
// never mutated (nil → fresh default-timeout client); the clone keeps
// base's Timeout. See specs/decisions/054-per-provider-tls-pinning.md.
func Client(base *http.Client, fingerprint string, logger *slog.Logger) *http.Client {
	pin := normalizePin(fingerprint)
	if pin == "" {
		return base
	}

	warnInvalidPin(pin, logger)

	start := base
	if start == nil {
		start = &http.Client{Timeout: 10 * time.Minute}
	}
	clone := *start
	clone.Transport = pinnedTransport(start.Transport, pin, logger)
	return &clone
}

// RouterEntryClient resolves the per-provider override for an
// llm.ProviderEntry — the chat/inference dial path: nil while the proxy
// dials or no pin is set (the router-level fallback carries the long LLM
// timeout; the proxy client would cap every inference request), else a
// pinned clone with the proxy routing stripped (specs/decisions/054-per-provider-tls-pinning.md).
func RouterEntryClient(policy DialPolicy, base *http.Client, fingerprint string, logger *slog.Logger) *http.Client {
	if policy.ProxyActive && !policy.TargetBypassed {
		return nil
	}
	pin := normalizePin(fingerprint)
	if pin == "" {
		return nil
	}
	client := Client(base, pin, logger)
	if policy.ProxyActive {
		// The proxy is active but this host bypasses it: the dial is direct,
		// so the pinned transport must not inherit the proxy routing.
		client.Transport = stripProxy(client.Transport)
	}
	return client
}

// DirectDialClient resolves dial paths with no router-level fallback
// (Fetch Models, the context-window probe): the proxy client verbatim while
// the proxy dials — nil would mean "dial directly", bypassing the
// operator's routing — a direct derived client for a bypassed host, else
// the pinned or nil SDK client.
func DirectDialClient(proxyClient *http.Client, policy DialPolicy, fingerprint string, logger *slog.Logger) *http.Client {
	if policy.HostThroughProxy() {
		return proxyClient
	}
	if !policy.ProxyActive {
		// No proxy: proxyClient is nil by construction here.
		if pin := normalizePin(fingerprint); pin != "" {
			return Client(nil, pin, logger)
		}
		return nil
	}
	// Bypassed while the proxy is active: dial direct (operator CA
	// overrides kept), pin layered on top when set.
	pin := normalizePin(fingerprint)
	if pin == "" {
		clone := *proxyClient
		clone.Transport = stripProxy(proxyClient.Transport)
		return &clone
	}
	client := Client(nil, pin, logger)
	client.Timeout = proxyClient.Timeout
	return client
}

// HostThroughProxy reports whether the dial behind policy still goes through
// the proxy transport (active and NOT bypassed).
func (p DialPolicy) HostThroughProxy() bool {
	return p.ProxyActive && !p.TargetBypassed
}

// stripProxy returns a transport with the proxy routing removed for direct
// bypassed dials; the *http.Transport is cloned (never mutated — it may be
// shared), keeping the operator's dialer and CA overrides.
func stripProxy(base http.RoundTripper) http.RoundTripper {
	if ht, ok := base.(*http.Transport); ok {
		ht = ht.Clone()
		ht.Proxy = nil
		return ht
	}
	return base
}

// TargetHost returns the hostname (no port, no scheme) of rawURL, or "" when
// the URL is empty or unparseable — the bypass-matching key every dial path
// derives from a provider base URL.
func TargetHost(rawURL string) string {
	parsed, err := url.Parse(rawURL)
	if err != nil {
		return ""
	}
	return parsed.Hostname()
}

// warnInvalidPin logs a Warn for a pin that cannot be base64(SHA-256) —
// diagnostic only (ADR-054): the handshake still fails with ErrPinMismatch,
// but a typo'd paste is distinguishable from a genuine key rotation.
func warnInvalidPin(pin string, logger *slog.Logger) {
	if pin == "" || logger == nil {
		return
	}
	raw, err := base64.StdEncoding.DecodeString(pin)
	if err != nil || len(raw) != sha256.Size {
		logger.Warn("llmtls: configured TLS pin is not base64(SHA-256) — it can never match any certificate; check for a copy/paste error (hex encoding, missing padding, or truncation)",
			"pin_length", len(pin))
	}
}

// pinnedTransport derives the pin-only *http.Transport from base (nil →
// default). The result is a concrete *http.Transport — not a wrapper — so
// http.Client.CloseIdleConnections reaches the derived client's pool
// (specs/decisions/054-per-provider-tls-pinning.md).
func pinnedTransport(base http.RoundTripper, pin string, logger *slog.Logger) *http.Transport {
	tlsCfg := &tls.Config{
		// Deliberate, user-opted-in bypass (gosec G402): system verification
		// is replaced by the SPKI check below.
		InsecureSkipVerify:    true, // #nosec G402 -- user-opted per-provider override
		MinVersion:            tls.VersionTLS12,
		VerifyPeerCertificate: pinVerifier(pin),
	}

	return transportWithTLS(base, tlsCfg, logger)
}

// transportWithTLS clones base into a private *http.Transport whose
// TLSClientConfig is tlsCfg — once per derived client, never per request
// (Clone drops the idle pool; a custom RoundTripper is replaced by a
// default-transport clone with a Warn).
//
// Clone's side effect: it runs the base's lazy HTTP/2 setup, populating
// base.TLSClientConfig with the default h2 list when nil; the config is
// deep-copied, so the pinned tlsCfg lands only on the clone.
func transportWithTLS(base http.RoundTripper, tlsCfg *tls.Config, logger *slog.Logger) *http.Transport {
	if ht, ok := base.(*http.Transport); ok {
		ht = ht.Clone()
		ht.TLSClientConfig = tlsCfg
		return ht
	}
	if base != nil && logger != nil {
		logger.Warn("llmtls: base transport is a custom RoundTripper that cannot carry a TLS config; falling back to a default-transport clone",
			"type", fmt.Sprintf("%T", base))
	}
	if dt, ok := http.DefaultTransport.(*http.Transport); ok {
		ht := dt.Clone()
		ht.TLSClientConfig = tlsCfg
		return ht
	}
	// Unreachable with the stdlib default transport; kept fail-safe.
	return &http.Transport{TLSClientConfig: tlsCfg}
}

// pinVerifier returns a VerifyPeerCertificate callback that accepts the leaf
// certificate only when base64(SHA-256(SPKI DER)) equals pin.
func pinVerifier(pin string) func(rawCerts [][]byte, _ [][]*x509.Certificate) error {
	return func(rawCerts [][]byte, _ [][]*x509.Certificate) error {
		if len(rawCerts) == 0 {
			return fmt.Errorf("%w: server presented no certificate", ErrPinMismatch)
		}
		cert, err := x509.ParseCertificate(rawCerts[0])
		if err != nil {
			return fmt.Errorf("parsing peer certificate: %w", err)
		}
		if SPKIFingerprint(cert) != pin {
			return ErrPinMismatch
		}
		return nil
	}
}

// SPKIFingerprint returns base64(StdEncoding, SHA-256(cert.RawSubjectPublicKeyInfo)).
func SPKIFingerprint(cert *x509.Certificate) string {
	sum := sha256.Sum256(cert.RawSubjectPublicKeyInfo)
	return base64.StdEncoding.EncodeToString(sum[:])
}

// normalizePin strips whitespace so values pasted with line wraps still
// match (ADR-054); comparison stays case-sensitive.
func normalizePin(pin string) string {
	return strings.Map(func(r rune) rune {
		switch r {
		case ' ', '\t', '\n', '\r':
			return -1
		default:
			return r
		}
	}, pin)
}

// FetchFingerprint performs an unverified TLS handshake against rawURL and
// returns the leaf certificate's SPKI fingerprint — the settings UI "Get"
// button. Unconditional w.r.t. any configured pin (takes and reads none),
// no HTTP request, no API key; non-https schemes rejected up front (specs/
// decisions/054-per-provider-tls-pinning.md).
func FetchFingerprint(ctx context.Context, rawURL string, logger *slog.Logger) (string, error) {
	parsed, err := url.Parse(rawURL)
	if err != nil {
		return "", fmt.Errorf("parsing URL: %w", err)
	}
	if parsed.Scheme != "https" {
		return "", fmt.Errorf("URL %q is not https — only https endpoints present a certificate to pin", rawURL)
	}
	host := parsed.Hostname()
	port := parsed.Port()
	if host == "" {
		return "", fmt.Errorf("URL %q has no host", rawURL)
	}
	if port == "" {
		port = "443"
	}
	addr := net.JoinHostPort(host, port)

	ctx, cancel := context.WithTimeout(ctx, FetchFingerprintTimeout)
	defer cancel()

	dialer := &tls.Dialer{
		// The whole point is to READ the untrusted certificate; verification
		// is the user's next step (pinning it). #nosec G402.
		Config: &tls.Config{
			InsecureSkipVerify: true, // #nosec G402 -- deliberate: fetching the pin
			MinVersion:         tls.VersionTLS12,
		},
	}
	conn, err := dialer.DialContext(ctx, "tcp", addr)
	if err != nil {
		return "", fmt.Errorf("TLS dial %s: %w", addr, err)
	}
	defer func() {
		if cerr := conn.Close(); cerr != nil && logger != nil {
			logger.Debug("closing fingerprint probe connection", "error", cerr)
		}
	}()

	tlsConn, ok := conn.(*tls.Conn)
	if !ok {
		return "", errors.New("fingerprint probe returned a non-TLS connection")
	}
	certs := tlsConn.ConnectionState().PeerCertificates
	if len(certs) == 0 {
		return "", errors.New("server presented no certificate")
	}
	return SPKIFingerprint(certs[0]), nil
}
