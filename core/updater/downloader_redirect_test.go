package updater

// Redirect-policy tests: the HTTPS-only transport invariant must hold for
// every redirect hop, not just the initial URLs.

import (
	"context"
	"crypto/tls"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync/atomic"
	"testing"
	"time"
)

func TestSecureHTTPClient_NonLoopbackHTTPHopRefused(t *testing.T) {
	origin := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		http.Redirect(w, r, "http://example.com/steal", http.StatusFound)
	}))
	defer origin.Close()

	d := NewDownloader(nil, nil)
	req, err := http.NewRequestWithContext(context.Background(), http.MethodGet, origin.URL, http.NoBody)
	if err != nil {
		t.Fatalf("http.NewRequestWithContext = %v, want nil", err)
	}
	resp, err := d.Client.Do(req)
	if err == nil {
		_ = resp.Body.Close()
		t.Fatal("redirect to a non-loopback http origin = nil error, want refusal")
	}
	if !strings.Contains(err.Error(), "must use HTTPS") {
		t.Errorf("error = %v, want it to contain %q", err, "must use HTTPS")
	}
}

func TestSecureHTTPClient_HTTPSOriginToPlainHostRefused(t *testing.T) {
	origin := httptest.NewTLSServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		http.Redirect(w, r, "http://example.com/steal", http.StatusFound)
	}))
	defer origin.Close()

	// A caller-supplied (proxy-aware) client: the policy must land on a copy.
	caller := &http.Client{
		Timeout:   5 * time.Second,
		Transport: &http.Transport{TLSClientConfig: &tls.Config{InsecureSkipVerify: true}}, // test CA
	}
	d := NewDownloader(caller, nil)
	req, err := http.NewRequestWithContext(context.Background(), http.MethodGet, origin.URL, http.NoBody)
	if err != nil {
		t.Fatalf("http.NewRequestWithContext = %v, want nil", err)
	}
	resp, err := d.Client.Do(req)
	if err == nil {
		_ = resp.Body.Close()
		t.Fatal("https origin redirecting to a plaintext host = nil error, want refusal")
	}
	if !strings.Contains(err.Error(), "must use HTTPS") {
		t.Errorf("error = %v, want it to contain %q", err, "must use HTTPS")
	}
	if caller.CheckRedirect != nil {
		t.Error("caller client was mutated; CheckRedirect must remain nil on the original")
	}
}

func TestSecureHTTPClient_LoopbackRedirectFollowed(t *testing.T) {
	var targetHits atomic.Int64
	target := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		targetHits.Add(1)
		w.WriteHeader(http.StatusOK)
	}))
	defer target.Close()

	origin := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		http.Redirect(w, r, target.URL, http.StatusFound)
	}))
	defer origin.Close()

	d := NewDownloader(nil, nil)
	req, err := http.NewRequestWithContext(context.Background(), http.MethodGet, origin.URL, http.NoBody)
	if err != nil {
		t.Fatalf("http.NewRequestWithContext = %v, want nil", err)
	}
	resp, err := d.Client.Do(req)
	if err != nil {
		t.Fatalf("loopback→loopback redirect = %v, want nil (the loopback exception keeps tests working)", err)
	}
	defer func() { _ = resp.Body.Close() }()
	if targetHits.Load() != 1 {
		t.Errorf("redirect target hits = %d, want 1", targetHits.Load())
	}
}

func TestSecureHTTPClient_RedirectCap(t *testing.T) {
	var originURL string
	origin := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		http.Redirect(w, r, originURL+"/next", http.StatusFound)
	}))
	defer origin.Close()
	originURL = origin.URL

	d := NewDownloader(nil, nil)
	req, err := http.NewRequestWithContext(context.Background(), http.MethodGet, origin.URL, http.NoBody)
	if err != nil {
		t.Fatalf("http.NewRequestWithContext = %v, want nil", err)
	}
	resp, err := d.Client.Do(req)
	if err == nil {
		_ = resp.Body.Close()
		t.Fatal("self-redirecting endpoint = nil error, want the redirect cap")
	}
	if !strings.Contains(err.Error(), "stopped after 10 redirects") {
		t.Errorf("error = %v, want it to contain %q", err, "stopped after 10 redirects")
	}
}

func TestNewChecker_RedirectPolicyInstalled(t *testing.T) {
	caller := &http.Client{Timeout: time.Second}
	c := NewChecker(Config{}, caller, nil)
	if c.client == caller {
		t.Error("checker aliases the caller's client; the policy must land on a copy")
	}
	if c.client.CheckRedirect == nil {
		t.Error("checker client is missing the redirect policy")
	}
	if caller.CheckRedirect != nil {
		t.Error("caller client was mutated; CheckRedirect must remain nil on the original")
	}
}
