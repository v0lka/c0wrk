package llmtls

import (
	"bytes"
	"context"
	"crypto/sha256"
	"encoding/base64"
	"errors"
	"log/slog"
	"net"
	"net/http"
	"net/http/httptest"
	"net/url"
	"strings"
	"sync/atomic"
	"testing"
	"time"
)

// serverPin returns the expected SPKI pin for the test server's leaf
// certificate via the exported helper.
func serverPin(t *testing.T, srv *httptest.Server) string {
	t.Helper()
	return SPKIFingerprint(srv.Certificate())
}

// manualPin computes the pin WITHOUT SPKIFingerprint so the test is an
// independent oracle: base64(sha256(SPKI DER)).
func manualPin(t *testing.T, srv *httptest.Server) string {
	t.Helper()
	sum := sha256.Sum256(srv.Certificate().RawSubjectPublicKeyInfo)
	return base64.StdEncoding.EncodeToString(sum[:])
}

// doGet performs a context-bound GET and closes the body; lint-clean wrapper
// for the many small assertions below.
func doGet(t *testing.T, httpClient *http.Client, target string) error {
	t.Helper()
	req, err := http.NewRequestWithContext(t.Context(), http.MethodGet, target, http.NoBody)
	if err != nil {
		t.Fatalf("building request: %v", err)
	}
	resp, err := httpClient.Do(req)
	if err != nil {
		return err
	}
	defer func() { _ = resp.Body.Close() }()
	return nil
}

func TestClient_EmptyFingerprintReturnsBaseUnchanged(t *testing.T) {
	base := &http.Client{}
	if got := Client(base, "", nil); got != base {
		t.Fatal("expected base pointer back when fingerprint is empty")
	}
}

func TestClient_NilBaseStillWorks(t *testing.T) {
	got := Client(nil, "a-nonempty-pin-makes-nil-base-derive-a-fresh-client", nil)
	if got == nil {
		t.Fatal("expected non-nil client")
	}
	if got.Transport == nil {
		t.Fatal("expected a transport on the derived client")
	}
}

func TestClient_CorrectPinSucceedsWrongPinFails(t *testing.T) {
	srv := httptest.NewTLSServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		_, _ = w.Write([]byte("ok"))
	}))
	t.Cleanup(srv.Close)

	pin := serverPin(t, srv)
	if want := manualPin(t, srv); pin != want {
		t.Fatalf("SPKIFingerprint diverges from manual sha256: %q vs %q", pin, want)
	}

	// Correct pin → success.
	okClient := Client(http.DefaultClient, pin, nil)
	if err := doGet(t, okClient, srv.URL); err != nil {
		t.Fatalf("expected pinned client to succeed, got: %v", err)
	}

	// Wrong pin → failure carrying ErrPinMismatch and NOT echoing the
	// actual certificate fingerprint (bare-error decision, ADR-050).
	wrong := strings.Repeat("A", len(pin))
	badClient := Client(http.DefaultClient, wrong, nil)
	if err := doGet(t, badClient, srv.URL); err == nil {
		t.Fatal("expected wrong pin to fail the request")
	} else {
		if !errors.Is(err, ErrPinMismatch) {
			t.Fatalf("expected error chain to contain ErrPinMismatch, got: %v", err)
		}
		if strings.Contains(err.Error(), pin) {
			t.Fatalf("error must not echo the actual pin, got: %v", err)
		}
	}
}

func TestClient_PinWhitespaceTolerant(t *testing.T) {
	srv := httptest.NewTLSServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		_, _ = w.Write([]byte("ok"))
	}))
	t.Cleanup(srv.Close)

	pin := serverPin(t, srv)
	spaced := pin[:4] + " \n" + pin[4:]

	client := Client(http.DefaultClient, spaced, nil)
	if err := doGet(t, client, srv.URL); err != nil {
		t.Fatalf("expected whitespace-padded pin to be accepted, got: %v", err)
	}
}

func TestClient_PreservesBaseProxyAndTimeout(t *testing.T) {
	// A base transport with a marker proxy function: the derived transport
	// must preserve it (proxy settings survive the TLS wrap).
	var proxyCalled bool
	base := &http.Client{
		Timeout: 42 * time.Second,
		Transport: &http.Transport{
			Proxy: func(*http.Request) (*url.URL, error) {
				proxyCalled = true
				return nil, nil
			},
		},
	}

	// A syntactically valid (but arbitrary) pin: no handshake happens in
	// this test — only proxy/timeout preservation is asserted.
	derived := Client(base, strings.Repeat("A", 43)+"=", nil)
	if derived.Timeout != base.Timeout {
		t.Fatalf("timeout not preserved: %v vs %v", derived.Timeout, base.Timeout)
	}
	pt, ok := derived.Transport.(*http.Transport)
	if !ok {
		t.Fatalf("expected *http.Transport, got %T", derived.Transport)
	}
	if pt.Proxy == nil {
		t.Fatal("base proxy transport not preserved in the pinned transport")
	}
	if _, _ = pt.Proxy(nil); !proxyCalled {
		t.Fatal("preserved proxy function is not the base one")
	}
}

// TestClient_TransportReusedAcrossRequests is the keep-alive regression
// guard: the pinned *http.Transport is cloned ONCE at construction and
// shared by every RoundTrip, so its connection pool persists and two
// sequential requests reuse one TCP connection (cloning inside RoundTrip
// dropped every idle connection and forced a fresh TCP+TLS handshake per
// request).
func TestClient_TransportReusedAcrossRequests(t *testing.T) {
	lc := net.ListenConfig{}
	ln, err := lc.Listen(t.Context(), "tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatalf("listen: %v", err)
	}
	var conns atomic.Int32
	srv := httptest.NewUnstartedServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		_, _ = w.Write([]byte("ok"))
	}))
	srv.Listener = &countingListener{Listener: ln, conns: &conns}
	srv.StartTLS()
	t.Cleanup(srv.Close)

	client := Client(http.DefaultClient, serverPin(t, srv), nil)
	pt, ok := client.Transport.(*http.Transport)
	if !ok {
		t.Fatalf("expected *http.Transport, got %T", client.Transport)
	}
	first := pt
	for i := range 2 {
		if err := doGet(t, client, srv.URL); err != nil {
			t.Fatalf("request %d failed: %v", i+1, err)
		}
	}
	if got := conns.Load(); got != 1 {
		t.Fatalf("expected exactly 1 TCP connection (keep-alive reuse), got %d", got)
	}
	if pt != first {
		t.Fatal("pinned transport must be built once and reused, not rebuilt per request")
	}
}

// countingListener counts accepted connections so tests can assert
// keep-alive reuse at the TCP level.
type countingListener struct {
	net.Listener
	conns *atomic.Int32
}

func (l *countingListener) Accept() (net.Conn, error) {
	c, err := l.Listener.Accept()
	if err == nil {
		l.conns.Add(1)
	}
	return c, err
}

// customRT is an http.RoundTripper that is NOT an *http.Transport — it
// stands in for retry/observability wrappers in the fallback test.
type customRT struct{}

func (customRT) RoundTrip(*http.Request) (*http.Response, error) {
	return nil, errors.New("custom RoundTripper must not be used after substitution")
}

// TestClient_CustomRoundTripperFallsBackWithWarn verifies the m3 behavior:
// a base transport that is NOT an *http.Transport (a retry/observability
// wrapper) cannot carry a tls.Config; it is replaced by a default-transport
// clone and the substitution is surfaced as a Warn, never silent.
func TestClient_CustomRoundTripperFallsBackWithWarn(t *testing.T) {
	srv := httptest.NewTLSServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		_, _ = w.Write([]byte("ok"))
	}))
	t.Cleanup(srv.Close)

	base := &http.Client{Transport: customRT{}}

	var buf bytes.Buffer
	logger := slog.New(slog.NewTextHandler(&buf, &slog.HandlerOptions{Level: slog.LevelWarn}))

	derived := Client(base, serverPin(t, srv), logger)
	if _, isTransport := any(derived.Transport).(*http.Transport); !isTransport {
		t.Fatalf("expected an *http.Transport fallback, got %T", derived.Transport)
	}
	if err := doGet(t, derived, srv.URL); err != nil {
		t.Fatalf("fallback transport must still serve requests, got: %v", err)
	}
	out := buf.String()
	if !strings.Contains(out, "custom RoundTripper") {
		t.Fatalf("expected a Warn naming the custom RoundTripper substitution, got log: %q", out)
	}
	if !strings.Contains(out, "customRT") {
		t.Fatalf("expected the Warn to carry the wrapper's type, got log: %q", out)
	}
}

// TestClient_CloseIdleConnectionsReached is the closeIdler regression guard:
// the derived client's transport must be a concrete *http.Transport, so
// http.Client.CloseIdleConnections (which checks the closeIdler interface)
// actually reaches the connection pool — closing idle keep-alive connections
// instead of leaking them until IdleConnTimeout after a config rebuild
// swaps the client.
func TestClient_CloseIdleConnectionsReached(t *testing.T) {
	lc := net.ListenConfig{}
	ln, err := lc.Listen(t.Context(), "tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatalf("listen: %v", err)
	}
	var conns atomic.Int32
	srv := httptest.NewUnstartedServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		_, _ = w.Write([]byte("ok"))
	}))
	srv.Listener = &countingListener{Listener: ln, conns: &conns}
	srv.StartTLS()
	t.Cleanup(srv.Close)

	client := Client(http.DefaultClient, serverPin(t, srv), nil)
	if _, ok := client.Transport.(interface{ CloseIdleConnections() }); !ok {
		t.Fatalf("transport %T must satisfy closeIdler so CloseIdleConnections is not a silent no-op", client.Transport)
	}
	if err := doGet(t, client, srv.URL); err != nil {
		t.Fatalf("first request failed: %v", err)
	}
	client.CloseIdleConnections()
	// The idle connection was closed, so the next request must dial a new
	// one (2 total) — proof the close reached the wrapped transport's pool.
	if err := doGet(t, client, srv.URL); err != nil {
		t.Fatalf("second request failed: %v", err)
	}
	if got := conns.Load(); got != 2 {
		t.Fatalf("expected 2 TCP connections after CloseIdleConnections dropped the idle one, got %d", got)
	}
}

// TestClient_InvalidPinFormatWarns pins the diagnostics contract: a
// non-empty pin that is not base64(SHA-256) can never match any certificate;
// it must surface as a Warn (typo'd paste vs. genuine key rotation) while
// still flowing through to the handshake, which fails with ErrPinMismatch.
func TestClient_InvalidPinFormatWarns(t *testing.T) {
	newLogger := func() (*bytes.Buffer, *slog.Logger) {
		var buf bytes.Buffer
		return &buf, slog.New(slog.NewTextHandler(&buf, &slog.HandlerOptions{Level: slog.LevelWarn}))
	}

	// Hex-encoded digest (not base64) → Warn.
	buf, logger := newLogger()
	Client(http.DefaultClient, strings.Repeat("ab", 32), logger)
	if !strings.Contains(buf.String(), "not base64") {
		t.Fatalf("expected a Warn for a hex-encoded pin, got log: %q", buf.String())
	}

	// Valid pin missing its "=" padding → base64 decode fails → Warn.
	buf, logger = newLogger()
	srv := httptest.NewTLSServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		_, _ = w.Write([]byte("ok"))
	}))
	t.Cleanup(srv.Close)
	Client(http.DefaultClient, strings.TrimRight(serverPin(t, srv), "="), logger)
	if !strings.Contains(buf.String(), "not base64") {
		t.Fatalf("expected a Warn for a pin missing its padding, got log: %q", buf.String())
	}

	// A well-formed pin → no Warn.
	buf, logger = newLogger()
	Client(http.DefaultClient, serverPin(t, srv), logger)
	if out := buf.String(); out != "" {
		t.Fatalf("expected no Warn for a well-formed pin, got log: %q", out)
	}

	// Empty pin (= no override) → base returned unchanged → no Warn.
	buf, logger = newLogger()
	Client(http.DefaultClient, "", logger)
	if out := buf.String(); out != "" {
		t.Fatalf("expected no Warn for an empty pin, got log: %q", out)
	}

	// Malformed pin + nil logger → no panic.
	Client(http.DefaultClient, "!!!not-base64!!!", nil)
}

// TestPinVerifier_DefensiveBranches covers the pinVerifier callback's
// defensive paths directly: no certificates presented, unparseable DER
// garbage, the match case, and the mismatch case.
func TestPinVerifier_DefensiveBranches(t *testing.T) {
	srv := httptest.NewTLSServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		_, _ = w.Write([]byte("ok"))
	}))
	t.Cleanup(srv.Close)
	certDER := srv.Certificate().Raw

	t.Run("no certificates", func(t *testing.T) {
		verifier := pinVerifier(serverPin(t, srv))
		err := verifier(nil, nil)
		if err == nil {
			t.Fatal("expected an error when the server presents no certificate")
		}
		if !errors.Is(err, ErrPinMismatch) {
			t.Fatalf("expected ErrPinMismatch in the chain, got: %v", err)
		}
	})

	t.Run("unparseable DER", func(t *testing.T) {
		verifier := pinVerifier(serverPin(t, srv))
		err := verifier([][]byte{{0xde, 0xad, 0xbe, 0xef}}, nil)
		if err == nil {
			t.Fatal("expected an error for garbage certificate bytes")
		}
		if errors.Is(err, ErrPinMismatch) {
			t.Fatalf("garbage DER is a parse failure, not a pin mismatch: %v", err)
		}
	})

	t.Run("matching pin", func(t *testing.T) {
		verifier := pinVerifier(serverPin(t, srv))
		if err := verifier([][]byte{certDER}, nil); err != nil {
			t.Fatalf("expected the server's own pin to verify, got: %v", err)
		}
	})

	t.Run("mismatching pin", func(t *testing.T) {
		verifier := pinVerifier(strings.Repeat("B", len(serverPin(t, srv))))
		err := verifier([][]byte{certDER}, nil)
		if !errors.Is(err, ErrPinMismatch) {
			t.Fatalf("expected bare ErrPinMismatch, got: %v", err)
		}
	})
}

func TestFetchFingerprint_ReturnsServerPin(t *testing.T) {
	srv := httptest.NewTLSServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		_, _ = w.Write([]byte("handler is never reached — handshake only"))
	}))
	t.Cleanup(srv.Close)

	got, err := FetchFingerprint(context.Background(), srv.URL, nil)
	if err != nil {
		t.Fatalf("FetchFingerprint: %v", err)
	}
	if want := manualPin(t, srv); got != want {
		t.Fatalf("fingerprint mismatch: got %q, want %q", got, want)
	}
}

func TestFetchFingerprint_ErrorsOnUnreachableHost(t *testing.T) {
	// Port 1 reliably refuses connections.
	if _, err := FetchFingerprint(context.Background(), "https://127.0.0.1:1", nil); err == nil {
		t.Fatal("expected an error for an unreachable host")
	}
}

func TestFetchFingerprint_RejectsMalformedURLs(t *testing.T) {
	if _, err := FetchFingerprint(context.Background(), "://", nil); err == nil {
		t.Fatal("expected an error for a malformed URL")
	}
	if _, err := FetchFingerprint(context.Background(), "http://", nil); err == nil {
		t.Fatal("expected an error for a URL without a host")
	}
}

// --- ResolveProviderClient (proxy-wins rule, ADR-051) ----------------------

func TestResolveProviderClient_ProxyWinsOverPin(t *testing.T) {
	// A configured proxy client is a global network policy: the pin is
	// ignored and the proxy client is returned VERBATIM (same pointer —
	// no clone, no derived pinned transport).
	proxyClient := &http.Client{}
	got := ResolveProviderClient(proxyClient, "some-nonempty-pin", nil)
	if got != proxyClient {
		t.Fatal("expected the proxy client back verbatim when a proxy is configured")
	}
}

func TestResolveProviderClient_NoProxyNoPinReturnsNil(t *testing.T) {
	if got := ResolveProviderClient(nil, "", nil); got != nil {
		t.Fatalf("expected nil (direct client, system verification), got %v", got)
	}
}

func TestResolveProviderClient_NoProxyWithPinReturnsPinnedClient(t *testing.T) {
	srv := httptest.NewTLSServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.WriteHeader(http.StatusOK)
	}))
	t.Cleanup(srv.Close)

	pin := serverPin(t, srv)
	got := ResolveProviderClient(nil, pin, nil)
	if got == nil {
		t.Fatal("expected a pinned client for a non-empty pin without proxy")
	}
	// The derived client must actually reach the self-signed server...
	if err := doGet(t, got, srv.URL); err != nil {
		t.Fatalf("pinned client failed against self-signed server: %v", err)
	}
	// ...while a wrong pin still fails closed.
	wrong := ResolveProviderClient(nil, "not-the-server-pin-value", nil)
	if err := doGet(t, wrong, srv.URL); err == nil {
		t.Fatal("expected wrong-pin client to fail against self-signed server")
	}
}
