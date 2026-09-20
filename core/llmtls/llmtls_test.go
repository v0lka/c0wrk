package llmtls

import (
	"bytes"
	"context"
	"crypto/tls"
	"encoding/base64"
	"errors"
	"io"
	"log"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"net/url"
	"strings"
	"testing"
	"time"
)

// testPin is a well-formed (base64 of 32 bytes) pin that matches no real
// certificate — used wherever only the shape of the pin matters.
const testPin = "k3J9vQ1Z0mF7hD2xS8pL4wR6tY5uI3oP1aE9cX0bN7g="

// newTLSServer starts an HTTPS test server and returns it plus the SPKI
// fingerprint of the certificate it presents.
func newTLSServer(t *testing.T, body string) (srv *httptest.Server, spkiPin string) {
	t.Helper()
	srv = httptest.NewTLSServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		_, _ = w.Write([]byte(body))
	}))
	t.Cleanup(srv.Close)
	// Mismatch tests deliberately provoke rejected handshakes; keep the
	// server's "tls: bad certificate" noise out of the test output.
	srv.Config.ErrorLog = log.New(io.Discard, "", 0)
	cert := srv.Certificate()
	if cert == nil {
		t.Fatal("test server presented no certificate")
	}
	return srv, SPKIFingerprint(cert)
}

// get issues a GET through client with a request context (the noctx linter
// forbids the convenience http.Client.Get).
func get(t *testing.T, client *http.Client, rawURL string) (*http.Response, error) {
	t.Helper()
	req, err := http.NewRequestWithContext(context.Background(), http.MethodGet, rawURL, http.NoBody)
	if err != nil {
		t.Fatalf("building request for %s: %v", rawURL, err)
	}
	return client.Do(req)
}

// captureLogger returns a logger writing into buf so Warn emission can be
// asserted.
func captureLogger(buf *bytes.Buffer) *slog.Logger {
	return slog.New(slog.NewTextHandler(buf, &slog.HandlerOptions{Level: slog.LevelDebug}))
}

func TestClient_EmptyPinReturnsBaseUnchanged(t *testing.T) {
	base := &http.Client{Timeout: 42 * time.Second}
	got := Client(base, "", nil)
	if got != base {
		t.Errorf("empty pin must return the SAME pointer as base; got %p want %p", got, base)
	}
	// Whitespace-only is equivalent to empty after normalization.
	if got := Client(base, "  \n\t ", nil); got != base {
		t.Error("whitespace-only pin must be treated as no pin")
	}
}

func TestClient_DoesNotMutateBaseAndInheritsTimeout(t *testing.T) {
	baseTransport := &http.Transport{MaxIdleConns: 7}
	base := &http.Client{Timeout: 11 * time.Minute, Transport: baseTransport}

	derived := Client(base, testPin, nil)

	if derived == base {
		t.Fatal("a non-empty pin must return a distinct client")
	}
	if derived.Timeout != base.Timeout {
		t.Errorf("derived Timeout = %v, want %v (must inherit base)", derived.Timeout, base.Timeout)
	}
	if base.Transport != baseTransport {
		t.Error("base.Transport pointer was replaced")
	}
	// http.Transport.Clone runs the base's lazy HTTP/2 setup, which may
	// populate base.TLSClientConfig with the default h2 protocol list. That
	// is policy-free. What must never leak into the base is OUR pin.
	if bc := baseTransport.TLSClientConfig; bc != nil {
		if bc.InsecureSkipVerify {
			t.Error("base transport inherited InsecureSkipVerify from the pinned clone")
		}
		if bc.VerifyPeerCertificate != nil {
			t.Error("base transport inherited the pin verifier from the clone")
		}
	}
	dt, ok := derived.Transport.(*http.Transport)
	if !ok {
		t.Fatalf("derived transport is %T, want *http.Transport (needed for CloseIdleConnections)", derived.Transport)
	}
	if dt == baseTransport {
		t.Error("derived transport must be a clone, not the base transport itself")
	}
	if dt.MaxIdleConns != 7 {
		t.Errorf("derived transport lost base settings: MaxIdleConns = %d, want 7", dt.MaxIdleConns)
	}
	if dt.TLSClientConfig == nil || dt.TLSClientConfig.VerifyPeerCertificate == nil {
		t.Fatal("derived transport carries no pin verifier")
	}
	if dt.TLSClientConfig.MinVersion != tls.VersionTLS12 {
		t.Errorf("MinVersion = %x, want TLS 1.2", dt.TLSClientConfig.MinVersion)
	}
}

func TestClient_TransportDerivedOncePerClient(t *testing.T) {
	srv, pin := newTLSServer(t, "ok")
	client := Client(nil, pin, nil)

	first, ok := client.Transport.(*http.Transport)
	if !ok {
		t.Fatalf("transport is %T, want *http.Transport", client.Transport)
	}
	for i := range 3 {
		resp, err := get(t, client, srv.URL)
		if err != nil {
			t.Fatalf("request %d failed: %v", i, err)
		}
		_ = resp.Body.Close()
	}
	// A per-request clone would replace the transport (and drop the idle
	// connection pool) between calls.
	if got, _ := client.Transport.(*http.Transport); got != first {
		t.Error("transport pointer changed across requests; the clone must happen once at construction")
	}
}

func TestClient_CorrectPinConnects(t *testing.T) {
	srv, pin := newTLSServer(t, "hello")
	client := Client(nil, pin, nil)

	resp, err := get(t, client, srv.URL)
	if err != nil {
		t.Fatalf("request with the correct pin must succeed: %v", err)
	}
	defer func() { _ = resp.Body.Close() }()
	if resp.StatusCode != http.StatusOK {
		t.Errorf("status = %d, want 200", resp.StatusCode)
	}
}

func TestClient_WrongPinFailsClosed(t *testing.T) {
	srv, _ := newTLSServer(t, "hello")
	client := Client(nil, testPin, nil)

	resp, err := get(t, client, srv.URL)
	if err == nil {
		_ = resp.Body.Close()
		t.Fatal("request with a mismatching pin must fail")
	}
	if !errors.Is(err, ErrPinMismatch) {
		t.Errorf("errors.Is(err, ErrPinMismatch) = false; err = %v (%T)", err, err)
	}
}

// The mismatch error must report the FACT of a mismatch and nothing about
// the certificate — no PEM, no base64 of the key, no subject/DNS names
// (RFC 7469 style; see ADR-054).
func TestPinVerifier_ErrorCarriesNoCertificateMaterial(t *testing.T) {
	srv, serverPin := newTLSServer(t, "hello")
	cert := srv.Certificate()

	verify := pinVerifier(testPin)
	err := verify([][]byte{cert.Raw}, nil)
	if err == nil {
		t.Fatal("expected a mismatch error")
	}
	msg := err.Error()

	rawB64 := base64.StdEncoding.EncodeToString(cert.Raw)
	forbidden := map[string]string{
		"raw certificate (base64)": rawB64,
		"server SPKI pin":          serverPin,
		"configured pin":           testPin,
		"PEM header":               "BEGIN CERTIFICATE",
		"subject common name":      cert.Subject.CommonName,
	}
	for label, needle := range forbidden {
		if needle == "" {
			continue
		}
		if strings.Contains(msg, needle) {
			t.Errorf("mismatch error leaks %s: %q", label, msg)
		}
	}
	for _, dns := range cert.DNSNames {
		if dns != "" && strings.Contains(msg, dns) {
			t.Errorf("mismatch error leaks a certificate DNS name %q: %q", dns, msg)
		}
	}
	if msg != ErrPinMismatch.Error() {
		t.Errorf("mismatch error = %q, want the bare sentinel %q", msg, ErrPinMismatch.Error())
	}
}

func TestPinVerifier_NoCertificatePresented(t *testing.T) {
	err := pinVerifier(testPin)(nil, nil)
	if !errors.Is(err, ErrPinMismatch) {
		t.Errorf("an empty certificate chain must fail closed with ErrPinMismatch; got %v", err)
	}
}

func TestPinVerifier_UnparseableCertificate(t *testing.T) {
	err := pinVerifier(testPin)([][]byte{[]byte("not a certificate")}, nil)
	if err == nil {
		t.Fatal("expected a parse error")
	}
	if errors.Is(err, ErrPinMismatch) {
		t.Error("a parse failure must be distinguishable from a pin mismatch")
	}
}

func TestClient_WhitespaceTolerantPin(t *testing.T) {
	srv, pin := newTLSServer(t, "ok")
	wrapped := pin[:10] + "\n  " + pin[10:]

	resp, err := get(t, Client(nil, wrapped, nil), srv.URL)
	if err != nil {
		t.Fatalf("a pin pasted with line wraps must still match: %v", err)
	}
	_ = resp.Body.Close()
}

type stubRoundTripper struct{}

func (stubRoundTripper) RoundTrip(*http.Request) (*http.Response, error) {
	return nil, errors.New("stub")
}

func TestClient_CustomRoundTripperWarnsAndFallsBack(t *testing.T) {
	var buf bytes.Buffer
	base := &http.Client{Timeout: time.Minute, Transport: stubRoundTripper{}}

	derived := Client(base, testPin, captureLogger(&buf))

	if _, ok := derived.Transport.(*http.Transport); !ok {
		t.Fatalf("derived transport is %T, want a *http.Transport fallback clone", derived.Transport)
	}
	if !strings.Contains(buf.String(), "custom RoundTripper") {
		t.Errorf("replacing a custom RoundTripper must Warn; log = %q", buf.String())
	}
}

func TestWarnInvalidPin(t *testing.T) {
	tests := []struct {
		name     string
		pin      string
		wantWarn bool
	}{
		{"well-formed base64 sha256", testPin, false},
		{"hex paste", "a1b2c3d4e5f60718293a4b5c6d7e8f90a1b2c3d4e5f60718293a4b5c6d7e8f90", true},
		{"missing padding", strings.TrimSuffix(testPin, "="), true},
		{"truncated digest", base64.StdEncoding.EncodeToString([]byte("short")), true},
		{"not base64 at all", "!!! not base64 !!!", true},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			var buf bytes.Buffer
			warnInvalidPin(normalizePin(tc.pin), captureLogger(&buf))
			gotWarn := strings.Contains(buf.String(), "not base64(SHA-256)")
			if gotWarn != tc.wantWarn {
				t.Errorf("warn emitted = %v, want %v; log = %q", gotWarn, tc.wantWarn, buf.String())
			}
		})
	}
	// A nil logger must never panic.
	warnInvalidPin("garbage", nil)
}

// RouterEntryClient must return nil while a proxy is active so the SDK falls
// back to the router-level client, which carries both the proxy transport and
// the long LLM request timeout. Returning the proxy client here would shadow
// it and cap inference at the much shorter proxy timeout.
func TestRouterEntryClient(t *testing.T) {
	base := &http.Client{Timeout: 10 * time.Minute}

	tests := []struct {
		name    string
		policy  DialPolicy
		pin     string
		wantNil bool
	}{
		{"proxy dials, pin set", DialPolicy{ProxyActive: true}, testPin, true},
		{"proxy dials, no pin", DialPolicy{ProxyActive: true}, "", true},
		{"no proxy, no pin", ZeroDialPolicy, "", true},
		{"no proxy, whitespace pin", ZeroDialPolicy, "  \n ", true},
		{"no proxy, pin set", ZeroDialPolicy, testPin, false},
		{"proxy bypassed, pin set", DialPolicy{ProxyActive: true, TargetBypassed: true}, testPin, false},
		{"proxy bypassed, no pin", DialPolicy{ProxyActive: true, TargetBypassed: true}, "", true},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			got := RouterEntryClient(tc.policy, base, tc.pin, nil)
			if tc.wantNil {
				if got != nil {
					t.Fatalf("got %p, want nil", got)
				}
				return
			}
			if got == nil {
				t.Fatal("got nil, want a pinned client")
			}
			if got == base {
				t.Error("the pinned client must be a distinct clone of base")
			}
			if got.Timeout != base.Timeout {
				t.Errorf("Timeout = %v, want %v (the LLM request timeout must be inherited)", got.Timeout, base.Timeout)
			}
		})
	}
}

// On the bypassed branch the pinned entry client is still cloned from the
// shared LLM client (long timeout preserved), but its transport must carry
// NO proxy routing — a bypassed host dials directly, and a MITM proxy on the
// way would present its own certificate and guarantee a pin mismatch.
func TestRouterEntryClient_BypassedStripsProxyRouting(t *testing.T) {
	base := &http.Client{
		Timeout: 10 * time.Minute,
		Transport: &http.Transport{
			Proxy: func(*http.Request) (*url.URL, error) { return url.Parse("http://proxy.lan:3128") },
		},
	}

	got := RouterEntryClient(DialPolicy{ProxyActive: true, TargetBypassed: true}, base, testPin, nil)
	if got == nil {
		t.Fatal("got nil, want a pinned client")
	}
	if got.Timeout != base.Timeout {
		t.Errorf("Timeout = %v, want %v (the LLM timeout must survive the bypass)", got.Timeout, base.Timeout)
	}
	ht, ok := got.Transport.(*http.Transport)
	if !ok {
		t.Fatalf("transport = %T, want *http.Transport", got.Transport)
	}
	if ht.Proxy != nil {
		t.Error("the bypassed pinned transport must have no Proxy func")
	}
	if baseHT, ok := base.Transport.(*http.Transport); !ok || baseHT.Proxy == nil {
		t.Error("stripProxy must clone, not mutate the shared base transport")
	}
}

func TestDirectDialClient(t *testing.T) {
	proxyClient := &http.Client{
		Timeout: 30 * time.Second,
		Transport: &http.Transport{
			Proxy: func(*http.Request) (*url.URL, error) { return url.Parse("http://proxy.lan:3128") },
		},
	}
	noProxy := &http.Client{Transport: &http.Transport{}}

	t.Run("proxy dials wins verbatim", func(t *testing.T) {
		for _, pin := range []string{"", testPin} {
			got := DirectDialClient(proxyClient, DialPolicy{ProxyActive: true}, pin, nil)
			if got != proxyClient {
				t.Errorf("pin=%q: got %p, want the proxy client %p unchanged", pin, got, proxyClient)
			}
		}
	})

	t.Run("bypassed host dials direct", func(t *testing.T) {
		bypassed := DialPolicy{ProxyActive: true, TargetBypassed: true}

		// No pin: the direct clone of the proxy client keeps the operator's
		// CA overrides but loses the proxy routing.
		got := DirectDialClient(proxyClient, bypassed, "", nil)
		if got == nil || got == proxyClient {
			t.Fatalf("got %p, want a distinct direct clone of the proxy client", got)
		}
		if got.Timeout != proxyClient.Timeout {
			t.Errorf("Timeout = %v, want %v", got.Timeout, proxyClient.Timeout)
		}
		if ht, ok := got.Transport.(*http.Transport); !ok || ht.Proxy != nil {
			t.Errorf("transport = %T, want an *http.Transport with the Proxy func stripped", got.Transport)
		}
		if ht, ok := proxyClient.Transport.(*http.Transport); !ok || ht.Proxy == nil {
			t.Error("stripProxy must clone, not mutate the shared proxy transport")
		}

		// With a pin: a fresh pinned direct client that reaches a
		// self-signed endpoint.
		srv, pin := newTLSServer(t, "ok")
		pinned := DirectDialClient(proxyClient, bypassed, pin, nil)
		resp, err := get(t, pinned, srv.URL)
		if err != nil {
			t.Fatalf("the bypassed pinned client must reach the self-signed server: %v", err)
		}
		_ = resp.Body.Close()
	})

	t.Run("no proxy no pin yields nil", func(t *testing.T) {
		if got := DirectDialClient(nil, ZeroDialPolicy, "", nil); got != nil {
			t.Errorf("got %p, want nil (default transport)", got)
		}
		if got := DirectDialClient(nil, ZeroDialPolicy, " \t\n", nil); got != nil {
			t.Errorf("whitespace pin: got %p, want nil", got)
		}
	})

	t.Run("no proxy with pin dials pinned", func(t *testing.T) {
		srv, pin := newTLSServer(t, "ok")
		client := DirectDialClient(nil, ZeroDialPolicy, pin, nil)
		if client == nil {
			t.Fatal("got nil, want a pinned client")
		}
		resp, err := get(t, client, srv.URL)
		if err != nil {
			t.Fatalf("pinned direct client must reach the server: %v", err)
		}
		_ = resp.Body.Close()
	})

	// Defense in depth: a caller that hands a proxy client to the no-proxy
	// branch must not have its routing silently kept or mutated.
	t.Run("no-proxy policy never mutates a handed-in client", func(t *testing.T) {
		if got := DirectDialClient(noProxy, ZeroDialPolicy, "", nil); got != nil {
			t.Fatalf("got %p, want nil (SDK default transport)", got)
		}
	})
}

func TestFetchFingerprint_MatchesServerSPKI(t *testing.T) {
	srv, want := newTLSServer(t, "ok")

	got, err := FetchFingerprint(context.Background(), srv.URL, nil)
	if err != nil {
		t.Fatalf("FetchFingerprint: %v", err)
	}
	if got != want {
		t.Errorf("fingerprint = %q, want %q", got, want)
	}
}

// The "Get" button is unconditional with respect to the configured pin
// (ADR-054): FetchFingerprint takes no fingerprint argument, so its result
// cannot depend on whether the provider already has one. Calling it twice
// against the same server yields the same value both times.
func TestFetchFingerprint_IndependentOfAnyConfiguredPin(t *testing.T) {
	srv, want := newTLSServer(t, "ok")

	first, err := FetchFingerprint(context.Background(), srv.URL, nil)
	if err != nil {
		t.Fatalf("first fetch: %v", err)
	}
	second, err := FetchFingerprint(context.Background(), srv.URL, nil)
	if err != nil {
		t.Fatalf("second fetch: %v", err)
	}
	if first != want || second != want {
		t.Errorf("fetches = (%q, %q), want both %q", first, second, want)
	}

	// Compile-time guard: the signature must stay pin-free.
	if fn := fetchFingerprintSignatureGuard(); fn == nil {
		t.Fatal("FetchFingerprint is nil")
	}
}

// fetchFingerprintSignatureGuard pins FetchFingerprint's signature at
// compile time: the "Get" button must stay unconditional with respect to the
// configured pin (ADR-054), so the function takes no fingerprint argument.
// Adding one stops this from compiling.
func fetchFingerprintSignatureGuard() func(context.Context, string, *slog.Logger) (string, error) {
	return FetchFingerprint
}

func TestFetchFingerprint_PathAndPortHandling(t *testing.T) {
	srv, want := newTLSServer(t, "ok")

	// A trailing API path (the shape of a real base_url) is ignored.
	got, err := FetchFingerprint(context.Background(), srv.URL+"/v1/chat/completions", nil)
	if err != nil {
		t.Fatalf("FetchFingerprint with a path: %v", err)
	}
	if got != want {
		t.Errorf("fingerprint = %q, want %q", got, want)
	}
}

func TestFetchFingerprint_Errors(t *testing.T) {
	tests := []struct {
		name string
		url  string
	}{
		{"no host", "https:///v1"},
		{"bare path", "/just/a/path"},
		{"empty", ""},
		{"unparseable", "https://exa mple.com:99999/"},
		{"unreachable", "https://127.0.0.1:1/"},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			ctx, cancel := context.WithTimeout(context.Background(), 3*time.Second)
			defer cancel()
			got, err := FetchFingerprint(ctx, tc.url, nil)
			if err == nil {
				t.Fatalf("expected an error, got fingerprint %q", got)
			}
			if got != "" {
				t.Errorf("fingerprint must be empty on error, got %q", got)
			}
		})
	}
}

func TestFetchFingerprint_PlainHTTPServerFails(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(http.ResponseWriter, *http.Request) {}))
	defer srv.Close()

	ctx, cancel := context.WithTimeout(context.Background(), 3*time.Second)
	defer cancel()
	if _, err := FetchFingerprint(ctx, srv.URL, nil); err == nil {
		t.Error("a non-TLS endpoint must produce an error, not an empty pin")
	}
}

// A plain-http base URL is rejected up front with an explicit error — not a
// TLS dial to port 443 with an obscure connection error (review on ADR-054).
// The rejection happens before any network access.
func TestFetchFingerprint_NonHTTPSSchemeRejected(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(http.ResponseWriter, *http.Request) {}))
	defer srv.Close()

	for _, raw := range []string{srv.URL, "http://llm.lan:1234/v1", "ftp://llm.lan"} {
		ctx, cancel := context.WithTimeout(context.Background(), 3*time.Second)
		_, err := FetchFingerprint(ctx, raw, nil)
		cancel()
		if err == nil {
			t.Errorf("FetchFingerprint(%q) must reject a non-https URL", raw)
			continue
		}
		if !strings.Contains(err.Error(), "https") {
			t.Errorf("FetchFingerprint(%q) error %q should name the https requirement", raw, err)
		}
	}
}

func TestSPKIFingerprint_StableAndBase64SHA256(t *testing.T) {
	srv, _ := newTLSServer(t, "ok")
	cert := srv.Certificate()

	got := SPKIFingerprint(cert)
	if got != SPKIFingerprint(cert) {
		t.Error("SPKIFingerprint must be deterministic")
	}
	raw, err := base64.StdEncoding.DecodeString(got)
	if err != nil {
		t.Fatalf("fingerprint is not standard base64: %v", err)
	}
	if len(raw) != 32 {
		t.Errorf("decoded fingerprint is %d bytes, want 32 (SHA-256)", len(raw))
	}
}

func TestNormalizePin(t *testing.T) {
	tests := []struct{ in, want string }{
		{"", ""},
		{"  ", ""},
		{"abc", "abc"},
		{" a b\tc\r\nd ", "abcd"},
		{testPin, testPin},
	}
	for _, tc := range tests {
		if got := normalizePin(tc.in); got != tc.want {
			t.Errorf("normalizePin(%q) = %q, want %q", tc.in, got, tc.want)
		}
	}
}
