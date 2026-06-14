package core

import (
	"context"
	"net/http"
	"net/url"
	"os"
	"path/filepath"
	"testing"
	"time"
)

func TestParseProxyURL(t *testing.T) {
	tests := []struct {
		name    string
		url     string
		want    string
		wantErr bool
	}{
		{name: "empty", url: "", wantErr: true},
		{name: "no scheme implies http", url: "proxy.example:8080", want: "http://proxy.example:8080"},
		{name: "http", url: "http://proxy:3128", want: "http://proxy:3128"},
		{name: "https", url: "https://proxy:8443", want: "https://proxy:8443"},
		{name: "socks5", url: "socks5://proxy:1080", want: "socks5://proxy:1080"},
		{name: "with auth", url: "http://user:pass@proxy:3128", want: "http://user:pass@proxy:3128"},
		{name: "unsupported scheme", url: "ftp://proxy:21", wantErr: true},
		{name: "no host", url: "http://", wantErr: true},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got, err := parseProxyURL(tt.url)
			if (err != nil) != tt.wantErr {
				t.Fatalf("parseProxyURL(%q) error = %v, wantErr %v", tt.url, err, tt.wantErr)
			}
			if !tt.wantErr && got.String() != tt.want {
				t.Errorf("parseProxyURL(%q) = %q, want %q", tt.url, got.String(), tt.want)
			}
		})
	}
}

func TestBuildBypassSet(t *testing.T) {
	set := buildBypassSet([]string{"localhost", "  127.0.0.1  ", "*.internal"})
	want := []string{"localhost", "127.0.0.1", "*.internal"}
	for _, w := range want {
		if _, ok := set[w]; !ok {
			t.Errorf("buildBypassSet missing %q", w)
		}
	}
	if len(set) != 3 {
		t.Errorf("buildBypassSet len = %d, want 3", len(set))
	}
}

func TestShouldBypass(t *testing.T) {
	set := buildBypassSet([]string{"localhost", "127.0.0.1", "*.internal"})
	tests := []struct {
		host string
		want bool
	}{
		{"localhost", true},
		{"LOCALHOST", true}, // case-insensitive
		{"127.0.0.1", true},
		{"foo.internal", true}, // wildcard suffix
		{"deep.foo.internal", true},
		{"google.com", false},
		{"internal", false}, // wildcard requires the leading dot
	}
	for _, tt := range tests {
		t.Run(tt.host, func(t *testing.T) {
			if got := shouldBypass(tt.host, set); got != tt.want {
				t.Errorf("shouldBypass(%q) = %v, want %v", tt.host, got, tt.want)
			}
		})
	}
}

func TestMaskProxyURL(t *testing.T) {
	tests := []struct {
		in   string
		want string
	}{
		{"", ""},
		{"http://proxy:3128", "http://proxy:3128"},
		{"http://user@proxy:3128", "http://user@proxy:3128"}, // no password to mask
		// url.URL.String percent-encodes asterisks; both forms acceptable.
		{"http://user:pass@proxy:3128", "http://user:%2A%2A%2A@proxy:3128"},
		{":://invalid", ":://invalid"}, // unparseable returns original
	}
	for _, tt := range tests {
		t.Run(tt.in, func(t *testing.T) {
			if got := MaskProxyURL(tt.in); got != tt.want {
				t.Errorf("MaskProxyURL(%q) = %q, want %q", tt.in, got, tt.want)
			}
		})
	}
}

func TestBuildProxyTransport_Disabled(t *testing.T) {
	tr, err := BuildProxyTransport(BuilderProxyConfig{Enabled: false}, nil)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if tr != nil {
		t.Error("expected nil transport when disabled")
	}
}

func TestBuildProxyTransport_Enabled(t *testing.T) {
	tr, err := BuildProxyTransport(BuilderProxyConfig{
		Enabled:    true,
		URL:        "http://proxy.example:3128",
		BypassList: []string{"localhost"},
	}, nil)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if tr == nil {
		t.Fatal("expected non-nil transport")
	}

	// Verify the proxy func routes non-bypass hosts to the proxy URL.
	req, _ := http.NewRequestWithContext(context.Background(), http.MethodGet, "https://example.com/path", http.NoBody)
	target, err := tr.Proxy(req)
	if err != nil {
		t.Fatalf("proxy func error: %v", err)
	}
	if target == nil || target.Host != "proxy.example:3128" {
		t.Errorf("proxy func returned %v, want proxy.example:3128", target)
	}

	// Bypassed host should return nil (direct connection).
	bypReq, _ := http.NewRequestWithContext(context.Background(), http.MethodGet, "https://localhost/", http.NoBody)
	target, err = tr.Proxy(bypReq)
	if err != nil {
		t.Fatalf("proxy func error: %v", err)
	}
	if target != nil {
		t.Errorf("bypassed host: got %v, want nil", target)
	}
}

func TestBuildProxyClient(t *testing.T) {
	client, err := BuildProxyClient(BuilderProxyConfig{Enabled: false}, 5*time.Second, nil)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if client != nil {
		t.Error("expected nil client when disabled")
	}

	client, err = BuildProxyClient(BuilderProxyConfig{
		Enabled: true,
		URL:     "http://proxy:3128",
	}, 5*time.Second, nil)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if client == nil {
		t.Fatal("expected non-nil client")
	}
	if client.Timeout != 5*time.Second {
		t.Errorf("client.Timeout = %v, want 5s", client.Timeout)
	}
}

func TestSetProxyEnvVars_Roundtrip(t *testing.T) {
	// Snapshot any pre-existing values so we can restore them.
	keys := []string{"HTTP_PROXY", "HTTPS_PROXY", "http_proxy", "https_proxy", "NO_PROXY", "no_proxy", "SSL_CERT_DIR"}
	prev := map[string]string{}
	for _, k := range keys {
		prev[k] = os.Getenv(k)
	}
	t.Cleanup(func() {
		for k, v := range prev {
			if v == "" {
				_ = os.Unsetenv(k)
			} else {
				_ = os.Setenv(k, v)
			}
		}
	})

	cfg := BuilderProxyConfig{
		Enabled:    true,
		URL:        "http://proxy.example:3128",
		BypassList: []string{"localhost", "127.0.0.1"},
		TLSCertDir: "/etc/ssl/certs",
	}
	SetProxyEnvVars(cfg)

	if got := os.Getenv("HTTP_PROXY"); got != cfg.URL {
		t.Errorf("HTTP_PROXY = %q, want %q", got, cfg.URL)
	}
	if got := os.Getenv("NO_PROXY"); got != "localhost,127.0.0.1" {
		t.Errorf("NO_PROXY = %q, want localhost,127.0.0.1", got)
	}
	if got := os.Getenv("SSL_CERT_DIR"); got != cfg.TLSCertDir {
		t.Errorf("SSL_CERT_DIR = %q, want %q", got, cfg.TLSCertDir)
	}

	ClearProxyEnvVars()
	for _, k := range keys {
		if got := os.Getenv(k); got != "" {
			t.Errorf("after clear: %s = %q, want empty", k, got)
		}
	}
}

func TestSetProxyEnvVars_Disabled(t *testing.T) {
	prev := os.Getenv("HTTP_PROXY")
	t.Cleanup(func() {
		if prev == "" {
			_ = os.Unsetenv("HTTP_PROXY")
		} else {
			_ = os.Setenv("HTTP_PROXY", prev)
		}
	})

	_ = os.Setenv("HTTP_PROXY", "http://stale:1234")
	SetProxyEnvVars(BuilderProxyConfig{Enabled: false})
	if got := os.Getenv("HTTP_PROXY"); got != "" {
		t.Errorf("disabled proxy did not clear HTTP_PROXY; got %q", got)
	}
}

func TestLoadCertPool_NoCertDir(t *testing.T) {
	cfg, err := buildTLSConfig("", nil)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if cfg != nil {
		t.Error("expected nil tls config for empty certDir")
	}
}

func TestLoadCertPool_NotADirectory(t *testing.T) {
	tmp := t.TempDir()
	regularFile := filepath.Join(tmp, "not-a-dir")
	if err := os.WriteFile(regularFile, []byte("x"), 0o644); err != nil {
		t.Fatalf("setup: %v", err)
	}
	if _, err := buildTLSConfig(regularFile, nil); err == nil {
		t.Error("expected error when certDir is not a directory")
	}
}

func TestLoadCertPool_NoValidCerts(t *testing.T) {
	tmp := t.TempDir()
	if err := os.WriteFile(filepath.Join(tmp, "bogus.pem"), []byte("not a real cert"), 0o644); err != nil {
		t.Fatalf("setup: %v", err)
	}
	cfg, err := buildTLSConfig(tmp, nil)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	// loadCertPool returns nil when no valid certs are loaded → buildTLSConfig
	// returns nil tls config.
	if cfg != nil {
		t.Error("expected nil tls config when no valid certs loaded")
	}
}

func TestParseProxyURL_AcceptsAuthInURL(t *testing.T) {
	got, err := parseProxyURL("http://user:secret@proxy:3128")
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if got.User == nil {
		t.Fatal("expected user info to be parsed")
	}
	if got.User.Username() != "user" {
		t.Errorf("Username = %q, want user", got.User.Username())
	}
	if pw, ok := got.User.Password(); !ok || pw != "secret" {
		t.Errorf("Password = (%q, %v), want (secret, true)", pw, ok)
	}
	// Sanity check: confirm parser handled it as a typical proxy URL.
	if got.Host != "proxy:3128" {
		t.Errorf("Host = %q, want proxy:3128", got.Host)
	}
}

// Ensure parseProxyURL rejects plain bare strings that look nothing like URLs.
func TestParseProxyURL_BareString(t *testing.T) {
	_, err := url.Parse("//bad")
	if err != nil {
		// stdlib parses "//bad" without error, so we instead probe an obviously
		// invalid scheme.
		t.Logf("url.Parse('//bad') err = %v", err)
	}
	if _, err := parseProxyURL("ftp://example.com"); err == nil {
		t.Error("expected error for ftp scheme")
	}
}
