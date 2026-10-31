package securehttp

import (
	"context"
	"errors"
	"net/http"
	"strings"
	"testing"
)

// TestRequireHTTPS pins the full URL policy matrix: only https passes, the
// loopback exception keeps consumers testable, and everything else —
// including private non-loopback IPs and non-HTTP schemes — is refused.
func TestRequireHTTPS(t *testing.T) {
	t.Parallel()
	cases := []struct {
		name    string
		url     string
		wantErr bool
	}{
		{"https production", "https://github.com/asset.zip", false},
		{"loopback ipv4", "http://127.0.0.1:8080/asset.zip", false},
		{"loopback localhost", "http://localhost:8080/asset.zip", false},
		{"loopback ipv6", "http://[::1]:8080/asset.zip", false},
		{"plain http non-loopback", "http://example.com/asset.zip", true},
		{"plain http private ip", "http://10.0.0.1/asset.zip", true},
		{"ftp", "ftp://github.com/asset.zip", true},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			err := RequireHTTPS(tc.url)
			if tc.wantErr && err == nil {
				t.Errorf("RequireHTTPS(%q) = nil, want error", tc.url)
			}
			if !tc.wantErr && err != nil {
				t.Errorf("RequireHTTPS(%q) = %v, want nil", tc.url, err)
			}
		})
	}
}

// TestRequireHTTPS_WrapsSentinel verifies the non-https refusals carry
// ErrNonHTTPS so callers can errors.Is against the shared sentinel.
func TestRequireHTTPS_WrapsSentinel(t *testing.T) {
	t.Parallel()
	for _, rawURL := range []string{
		"http://example.com/asset.zip",
		"ftp://github.com/asset.zip",
	} {
		if err := RequireHTTPS(rawURL); !errors.Is(err, ErrNonHTTPS) {
			t.Errorf("RequireHTTPS(%q) = %v, want it to wrap ErrNonHTTPS", rawURL, err)
		}
	}
}

// TestRequireHTTPS_ParseError verifies a malformed URL is refused, with an
// error that comes from url.Parse and deliberately does not claim
// ErrNonHTTPS (it is not a scheme violation).
func TestRequireHTTPS_ParseError(t *testing.T) {
	t.Parallel()
	err := RequireHTTPS("http://[::1")
	if err == nil {
		t.Fatal("RequireHTTPS of an unparseable URL = nil, want error")
	}
	if errors.Is(err, ErrNonHTTPS) {
		t.Errorf("parse error must not be misreported as ErrNonHTTPS: %v", err)
	}
}

// TestSecureRedirectCheck verifies the per-hop half of the policy: a
// plaintext hop is refused with the sentinel, and the chain cap reproduces
// net/http's default 10.
func TestSecureRedirectCheck(t *testing.T) {
	t.Parallel()

	httpsHop, err := http.NewRequestWithContext(context.Background(), http.MethodGet, "https://example.com/next", http.NoBody)
	if err != nil {
		t.Fatalf("http.NewRequest: %v", err)
	}
	httpHop, err := http.NewRequestWithContext(context.Background(), http.MethodGet, "http://example.com/next", http.NoBody)
	if err != nil {
		t.Fatalf("http.NewRequest: %v", err)
	}

	if err := SecureRedirectCheck(httpsHop, nil); err != nil {
		t.Errorf("SecureRedirectCheck(https hop) = %v, want nil", err)
	}
	if err := SecureRedirectCheck(httpHop, nil); !errors.Is(err, ErrNonHTTPS) {
		t.Errorf("SecureRedirectCheck(http hop) = %v, want it to wrap ErrNonHTTPS", err)
	}

	via := make([]*http.Request, MaxRedirects)
	if err := SecureRedirectCheck(httpsHop, via); err == nil {
		t.Error("SecureRedirectCheck at the cap = nil, want the redirect-cap error")
	} else if !strings.Contains(err.Error(), "stopped after 10 redirects") {
		t.Errorf("cap error = %v, want it to contain %q", err, "stopped after 10 redirects")
	}
	// One hop below the cap is still allowed.
	if err := SecureRedirectCheck(httpsHop, via[:MaxRedirects-1]); err != nil {
		t.Errorf("SecureRedirectCheck just below the cap = %v, want nil", err)
	}
}

// TestSecureHTTPClient_CopyNotAlias verifies the policy lands on a copy and
// the caller's client is never mutated.
func TestSecureHTTPClient_CopyNotAlias(t *testing.T) {
	t.Parallel()
	caller := &http.Client{Timeout: 1}
	secured := SecureHTTPClient(caller)
	if secured == caller {
		t.Fatal("SecureHTTPClient aliased the caller's client; the policy must land on a copy")
	}
	if secured.CheckRedirect == nil {
		t.Error("secured client is missing the redirect policy")
	}
	if caller.CheckRedirect != nil {
		t.Error("caller client was mutated; CheckRedirect must remain nil on the original")
	}
	if caller.Timeout != 1 {
		t.Errorf("caller client fields changed: Timeout = %v, want 1", caller.Timeout)
	}
}
