package embeddedllm

import (
	"context"
	"crypto/tls"
	"crypto/x509"
	"errors"
	"io"
	"log"
	"net/http"
	"net/http/httptest"
	"strconv"
	"strings"
	"sync"
	"testing"
	"testing/synctest"
	"time"
)

// This file covers the ensure-loaded transport: the first request to an
// idle-unloaded model must transparently start it, concurrent cold requests
// must coalesce into ONE weight load, the derived client must preserve the
// shared LLM client's timeout (by MOVING it into the transport, so the cold-load
// wait is not charged to the request), activity must be stamped when the
// response COMPLETES, and a wait that runs out of budget must be an explicit
// error rather than a hang.
//
// The load path is exercised twice: against a loader double (to count and gate
// loads precisely) and against a real *Server with a fake spawner and a real
// loopback endpoint (to prove the seam is the supervisor itself, not a mock of
// it).

// errLoadFailed is what a failing loader double returns.
var errLoadFailed = errors.New("the weights are corrupt")

// ---------------------------------------------------------------------------
// doubles
// ---------------------------------------------------------------------------

// stubTransport stands in for the wrapped transport. It answers every request
// with a canned 200 and counts the calls, so a test can prove that a failed or
// abandoned load kept the request from ever being sent.
type stubTransport struct {
	mu         sync.Mutex
	calls      int
	idleCloses int
	body       string
	err        error
	// lastURL is the URL of the most recent request as the wrapped transport saw
	// it — i.e. AFTER the redirect the ensure-loaded transport applies. It is
	// what proves a moved port reaches the wire, not only the log.
	lastURL string
}

func (s *stubTransport) RoundTrip(req *http.Request) (*http.Response, error) {
	s.mu.Lock()
	s.calls++
	body, err := s.body, s.err
	if req != nil && req.URL != nil {
		s.lastURL = req.URL.String()
	}
	s.mu.Unlock()
	if err != nil {
		return nil, err
	}
	if body == "" {
		body = `{"choices":[]}`
	}
	return &http.Response{
		StatusCode: http.StatusOK,
		Header:     http.Header{"Content-Type": []string{"application/json"}},
		Body:       io.NopCloser(strings.NewReader(body)),
		Request:    req,
	}, nil
}

// CloseIdleConnections makes the stub satisfy the stdlib's closeIdler shape.
func (s *stubTransport) CloseIdleConnections() {
	s.mu.Lock()
	s.idleCloses++
	s.mu.Unlock()
}

func (s *stubTransport) callCount() int {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.calls
}

func (s *stubTransport) idleCloseCount() int {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.idleCloses
}

// lastSeenURL reports the URL the wrapped transport was actually handed.
func (s *stubTransport) lastSeenURL() string {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.lastURL
}

// fakeLoader is a Loader double. Load blocks on block (when set) or runs onLoad
// (when set), so a test controls exactly when a load finishes; every call is
// counted and its context captured.
type fakeLoader struct {
	mu       sync.Mutex
	loads    int
	marks    int
	begins   int
	ends     int
	block    chan struct{}
	err      error
	onLoad   func(ctx context.Context) error
	lastCtx  context.Context
	lastMark time.Time
}

func (l *fakeLoader) Load(ctx context.Context) error {
	l.mu.Lock()
	l.loads++
	l.lastCtx = ctx
	block, onLoad, err := l.block, l.onLoad, l.err
	l.mu.Unlock()

	if onLoad != nil {
		return onLoad(ctx)
	}
	if block != nil {
		select {
		case <-block:
		case <-ctx.Done():
			// Mirrors waitReady: a bounded wait reports the context error.
			return ctx.Err()
		}
	}
	return err
}

func (l *fakeLoader) MarkActivity() {
	l.mu.Lock()
	l.marks++
	l.lastMark = time.Now()
	l.mu.Unlock()
}

// BeginRequest and EndRequest make the double satisfy the optional
// RequestTracker capability, so the in-flight bracketing the transport performs
// around a response body is exercised by every test that uses it.
func (l *fakeLoader) BeginRequest() {
	l.mu.Lock()
	l.begins++
	l.mu.Unlock()
}

func (l *fakeLoader) EndRequest() {
	l.mu.Lock()
	l.ends++
	l.mu.Unlock()
}

func (l *fakeLoader) loadCount() int {
	l.mu.Lock()
	defer l.mu.Unlock()
	return l.loads
}

func (l *fakeLoader) markCount() int {
	l.mu.Lock()
	defer l.mu.Unlock()
	return l.marks
}

func (l *fakeLoader) beginCount() int {
	l.mu.Lock()
	defer l.mu.Unlock()
	return l.begins
}

func (l *fakeLoader) endCount() int {
	l.mu.Lock()
	defer l.mu.Unlock()
	return l.ends
}

// untrackedLoader satisfies Loader and nothing else: no PortSource, no
// RequestTracker. Both capabilities are optional, and this double is what keeps
// them that way — a transport that required either would fail here.
type untrackedLoader struct {
	mu    sync.Mutex
	marks int
}

var _ Loader = (*untrackedLoader)(nil)

func (l *untrackedLoader) Load(context.Context) error { return nil }

func (l *untrackedLoader) MarkActivity() {
	l.mu.Lock()
	l.marks++
	l.mu.Unlock()
}

func (l *untrackedLoader) markCount() int {
	l.mu.Lock()
	defer l.mu.Unlock()
	return l.marks
}

func (l *fakeLoader) capturedCtx() context.Context {
	l.mu.Lock()
	defer l.mu.Unlock()
	return l.lastCtx
}

// providerBaseURL is the URL the backend generates for the embedded provider
// entry (http://127.0.0.1:<persisted port>/v1). It comes from the manifest, not
// from Server.BaseURL: the router dials it BEFORE any load has run, and the
// supervisor only learns its port during one.
func providerBaseURL(fx *fixture) string {
	return "http://" + LoopbackHost + ":" + strconv.Itoa(fx.manifest.Port) + "/v1"
}

// doGet issues one GET through client and drains the response, returning the
// status and the body.
func doGet(t *testing.T, client *http.Client, url string) (status int, body string) {
	t.Helper()
	req, err := http.NewRequestWithContext(t.Context(), http.MethodGet, url, http.NoBody)
	if err != nil {
		t.Fatalf("building the request: %v", err)
	}
	resp, err := client.Do(req)
	if err != nil {
		t.Fatalf("the request must succeed: %v", err)
	}
	defer func() { _ = resp.Body.Close() }()
	raw, readErr := io.ReadAll(resp.Body)
	if readErr != nil {
		t.Fatalf("reading the response: %v", readErr)
	}
	return resp.StatusCode, string(raw)
}

// ---------------------------------------------------------------------------
// A cold request loads the model and then succeeds
// ---------------------------------------------------------------------------

// The user-visible requirement: the FIRST request to an unloaded model starts
// the server, waits for it, and completes — the caller never learns the model
// was cold. Driven against the real supervisor (fake spawner, real loopback
// endpoint) through a real *http.Client, so the whole seam is exercised.
func TestColdRequestLoadsTheModelAndSucceeds(t *testing.T) {
	fx := newFixture(t, fixtureOptions{notReady: 2})

	if got := fx.srv.State(); got != StateInstalled {
		t.Fatalf("state before the request = %q, want %q (the model must start cold)", got, StateInstalled)
	}

	client := EnsureLoadedClient(nil, &http.Client{Timeout: time.Minute}, fx.srv, 0, nil)
	status, body := doGet(t, client, providerBaseURL(fx)+"/models")

	if status != http.StatusOK {
		t.Errorf("status = %d, want 200", status)
	}
	if !strings.Contains(body, "Bonsai 2 27B") {
		t.Errorf("body = %q, want the model list the endpoint serves", body)
	}
	if got := fx.srv.State(); got != StateLoaded {
		t.Errorf("state after the request = %q, want %q", got, StateLoaded)
	}
	if got := fx.spawner.spawnCount(); got != 1 {
		t.Errorf("spawn count = %d, want exactly 1", got)
	}

	// A warm request must not start anything else.
	if status, _ := doGet(t, client, providerBaseURL(fx)+"/models"); status != http.StatusOK {
		t.Errorf("warm request status = %d, want 200", status)
	}
	if got := fx.spawner.spawnCount(); got != 1 {
		t.Errorf("spawn count after a warm request = %d, want 1 (a loaded model is not reloaded)", got)
	}
}

// ---------------------------------------------------------------------------
// Concurrent cold requests coalesce into ONE load
// ---------------------------------------------------------------------------

// N parallel cold requests must produce exactly one Load call: the transport
// coalesces them instead of queueing N loads on the supervisor's gate. The
// waiters counter is what makes this deterministic — the load is released only
// once every request has joined it.
func TestParallelColdRequestsCoalesceIntoOneLoad(t *testing.T) {
	const requests = 8

	release := make(chan struct{})
	loader := &fakeLoader{block: release}
	stub := &stubTransport{}
	tr := NewEnsureLoadedTransport(stub, loader, time.Minute, nil)

	var wg sync.WaitGroup
	results := make([]error, requests)
	started := make(chan struct{})
	for i := range requests {
		wg.Add(1)
		go func(i int) {
			defer wg.Done()
			<-started
			req, err := http.NewRequestWithContext(t.Context(), http.MethodGet, "http://127.0.0.1:9/v1/models", http.NoBody)
			if err != nil {
				results[i] = err
				return
			}
			resp, err := tr.RoundTrip(req)
			if err != nil {
				results[i] = err
				return
			}
			_, _ = io.Copy(io.Discard, resp.Body)
			_ = resp.Body.Close()
		}(i)
	}
	close(started)

	waitFor(t, 5*time.Second, func() bool { return tr.waiters.Load() == requests },
		"all 8 requests to join the single in-flight load")
	close(release)
	wg.Wait()

	for i, err := range results {
		if err != nil {
			t.Errorf("request %d failed: %v", i, err)
		}
	}
	if got := loader.loadCount(); got != 1 {
		t.Errorf("Load calls = %d, want exactly 1 for %d parallel cold requests", got, requests)
	}
	if got := stub.callCount(); got != requests {
		t.Errorf("requests reaching the wrapped transport = %d, want %d (every waiter must be served)", got, requests)
	}
}

// The same property against the real supervisor: N parallel cold requests
// spawn exactly one llama-server and emit exactly one loading transition.
func TestParallelColdRequestsSpawnOneServer(t *testing.T) {
	const requests = 8

	fx := newFixture(t, fixtureOptions{notReady: 3, readyDelay: 5 * time.Millisecond})
	tr := NewEnsureLoadedTransport(nil, fx.srv, time.Minute, nil)
	client := &http.Client{Transport: tr, Timeout: time.Minute}

	var wg sync.WaitGroup
	codes := make([]int, requests)
	errs := make([]error, requests)
	started := make(chan struct{})
	for i := range requests {
		wg.Add(1)
		go func(i int) {
			defer wg.Done()
			<-started
			req, err := http.NewRequestWithContext(context.Background(), http.MethodGet,
				providerBaseURL(fx)+"/models", http.NoBody)
			if err != nil {
				errs[i] = err
				return
			}
			resp, err := client.Do(req)
			if err != nil {
				errs[i] = err
				return
			}
			defer func() { _ = resp.Body.Close() }()
			_, _ = io.Copy(io.Discard, resp.Body)
			codes[i] = resp.StatusCode
		}(i)
	}
	close(started)
	wg.Wait()

	for i, err := range errs {
		if err != nil {
			t.Errorf("request %d failed: %v", i, err)
		}
	}
	for i, code := range codes {
		if code != http.StatusOK {
			t.Errorf("request %d status = %d, want 200", i, code)
		}
	}
	if got := fx.spawner.spawnCount(); got != 1 {
		t.Errorf("spawn count = %d, want exactly 1", got)
	}
	if got := fx.events.count(StateLoading); got != 1 {
		t.Errorf("loading transitions = %d, want exactly 1 (one weight load, not %d)", got, requests)
	}
	if got := fx.srv.State(); got != StateLoaded {
		t.Errorf("state = %q, want %q", got, StateLoaded)
	}
}

// ---------------------------------------------------------------------------
// The derived client preserves the shared LLM client's timeout — by MOVING it
// into the transport
// ---------------------------------------------------------------------------

// llm-providers.md invariant: a client handed to ProviderEntry.HTTPClient
// shadows RouterConfig.HTTPClient, so inference MUST be bounded by
// timeouts.llmRequestTimeout and never by the 30 s web-fetch proxy budget.
//
// The gate changes where that budget lives, not whether it exists.
// http.Client.Timeout covers the whole exchange — including the cold-load wait —
// so leaving it on the client would let a slow weight load eat the generation's
// budget (the default 10 min request budget is SHORTER than the supervisor's own
// 15 min ready allowance). The client's Timeout is therefore zeroed and handed
// to the transport, which arms it only once the model is resident.
func TestEnsureLoadedClientMovesTheSharedLLMTimeoutIntoTheTransport(t *testing.T) {
	const (
		llmTimeout   = 10 * time.Minute
		proxyTimeout = 30 * time.Second
	)
	sharedTransport := &stubTransport{}
	shared := &http.Client{Timeout: llmTimeout, Transport: sharedTransport}
	proxyClient := &http.Client{Timeout: proxyTimeout, Transport: &stubTransport{}}

	got := EnsureLoadedClient(nil, shared, &fakeLoader{}, 0, nil)

	if got == nil {
		t.Fatal("client = nil, want the ensure-loaded client")
	}
	if got == shared {
		t.Error("the shared LLM client must not be used in place — it has to be a clone carrying the wrapper")
	}
	if got.Timeout != 0 {
		t.Errorf("Timeout = %v, want 0: a client-level timeout would run during the "+
			"cold-load wait and starve the request it gates", got.Timeout)
	}
	if shared.Timeout != llmTimeout {
		t.Errorf("the shared client's Timeout was mutated to %v", shared.Timeout)
	}
	if shared.Transport != http.RoundTripper(sharedTransport) {
		t.Error("the shared client's transport was mutated")
	}
	wrapper, ok := got.Transport.(*EnsureLoadedTransport)
	if !ok {
		t.Fatalf("Transport = %T, want *EnsureLoadedTransport", got.Transport)
	}
	if wrapper.requestTimeout != llmTimeout {
		t.Errorf("requestTimeout = %v, want the shared LLM client's %v — the budget must "+
			"survive the move, not vanish with the zeroed Timeout", wrapper.requestTimeout, llmTimeout)
	}
	if wrapper.requestTimeout == proxyTimeout {
		t.Error("requestTimeout is the web-fetch proxy budget — inference would be capped at 30s")
	}
	if wrapper.base != http.RoundTripper(sharedTransport) {
		t.Errorf("wrapped transport = %T, want the shared client's transport (proxy routing must survive)", wrapper.base)
	}
	if wrapper.waitTimeout != DefaultLoadWaitTimeout {
		t.Errorf("waitTimeout = %v, want the %v default", wrapper.waitTimeout, DefaultLoadWaitTimeout)
	}
	// The proxy client is never a source for this client: it exists only to show
	// the shorter timeout that must NOT be inherited.
	if wrapper.requestTimeout == proxyClient.Timeout {
		t.Error("the transport inherited the proxy timeout")
	}
}

// When the pin resolver produced a client, the wrapper is layered on top of IT,
// so the pin (and the proxy-stripping a bypassed host needs) is preserved and
// the timeout still comes from the shared LLM client the pinned clone was built
// from.
func TestEnsureLoadedClientKeepsThePinnedTransport(t *testing.T) {
	pinned := &http.Transport{TLSClientConfig: &tls.Config{
		MinVersion:            tls.VersionTLS12,
		VerifyPeerCertificate: func([][]byte, [][]*x509.Certificate) error { return nil },
	}}
	base := &http.Client{Timeout: 7 * time.Minute, Transport: pinned}
	shared := &http.Client{Timeout: 10 * time.Minute, Transport: &stubTransport{}}

	got := EnsureLoadedClient(base, shared, &fakeLoader{}, 90*time.Second, nil)

	if got == base {
		t.Error("the pinned client must not be used in place")
	}
	if got.Timeout != 0 {
		t.Errorf("Timeout = %v, want 0 — the pinned clone's 7m0s must move into the "+
			"transport so the cold-load wait is not charged to it", got.Timeout)
	}
	if base.Timeout != 7*time.Minute {
		t.Errorf("the pinned client's Timeout was mutated to %v", base.Timeout)
	}
	wrapper, ok := got.Transport.(*EnsureLoadedTransport)
	if !ok {
		t.Fatalf("Transport = %T, want *EnsureLoadedTransport", got.Transport)
	}
	if wrapper.requestTimeout != 7*time.Minute {
		t.Errorf("requestTimeout = %v, want the pinned clone's 7m0s (base is the "+
			"timeout source when the pin resolver produced a client)", wrapper.requestTimeout)
	}
	if wrapper.base != http.RoundTripper(pinned) {
		t.Errorf("wrapped transport = %T, want the pinned transport (the pin must survive)", wrapper.base)
	}
	if wrapper.waitTimeout != 90*time.Second {
		t.Errorf("waitTimeout = %v, want the configured 90s", wrapper.waitTimeout)
	}
	if base.Transport != http.RoundTripper(pinned) {
		t.Error("the pinned client's transport was mutated")
	}
	if pinned.TLSClientConfig == nil || pinned.TLSClientConfig.VerifyPeerCertificate == nil {
		t.Error("the pin verifier was lost")
	}
}

// No loader means no override: the resolver's answer (including nil, the "let
// the SDK use the router-level client" answer) must pass through untouched, so
// the pin/proxy resolution order is preserved.
func TestEnsureLoadedClientWithoutALoaderChangesNothing(t *testing.T) {
	base := &http.Client{Timeout: time.Minute}
	if got := EnsureLoadedClient(base, &http.Client{}, nil, 0, nil); got != base {
		t.Errorf("client = %p, want the unchanged base %p", got, base)
	}
	if got := EnsureLoadedClient(nil, &http.Client{}, nil, 0, nil); got != nil {
		t.Errorf("client = %p, want nil (the SDK must fall back to the router-level client)", got)
	}
}

// ---------------------------------------------------------------------------
// Activity is stamped when the response COMPLETES
// ---------------------------------------------------------------------------

// A long generation must count as activity, not as idle time: the stamp lands
// when the response is finished, so the idle budget is whole again afterwards —
// and it is NOT taken when the response headers arrive.
func TestActivityIsMarkedWhenTheResponseCompletes(t *testing.T) {
	start := time.Date(2026, 9, 24, 12, 0, 0, 0, time.UTC)
	clock := newFakeClock(start)
	fx := newFixture(t, fixtureOptions{
		autoUnload: NewAutoUnload(true, 60),
		now:        clock.Now,
		// The weight load "takes" ten minutes, which the supervisor must not
		// charge to the idle budget.
		readyHook: func() { clock.advance(10 * time.Minute) },
	})

	client := EnsureLoadedClient(nil, &http.Client{Timeout: time.Minute}, fx.srv, 0, nil)
	req, err := http.NewRequestWithContext(t.Context(), http.MethodGet, providerBaseURL(fx)+"/models", http.NoBody)
	if err != nil {
		t.Fatalf("building the request: %v", err)
	}
	resp, err := client.Do(req)
	if err != nil {
		t.Fatalf("the cold request must succeed: %v", err)
	}
	if got := fx.srv.InFlightRequests(); got != 1 {
		t.Errorf("in-flight requests while the response is open = %d, want 1", got)
	}

	// The load stamped activity when it finished (start + the 10-minute load).
	wantLoadStamp := start.Add(10 * time.Minute)
	if got := fx.srv.LastActivity(); !got.Equal(wantLoadStamp) {
		t.Fatalf("LastActivity after the load = %v, want %v", got, wantLoadStamp)
	}

	// Twenty-five minutes of generation pass with the response still open. The
	// headers arriving must NOT have stamped anything: the stamp still sits at
	// the load.
	clock.advance(25 * time.Minute)
	if got := fx.srv.LastActivity(); !got.Equal(wantLoadStamp) {
		t.Errorf("LastActivity before the response completed = %v, want the load stamp %v "+
			"(activity is stamped on completion, not on the response headers)", got, wantLoadStamp)
	}
	if left, armed := fx.srv.IdleRemaining(); !armed || left != 35*time.Minute {
		t.Errorf("idle budget mid-response = %v (armed %v), want 35m0s left", left, armed)
	}
	if got := fx.srv.InFlightRequests(); got != 1 {
		t.Errorf("in-flight requests mid-response = %d, want 1", got)
	}

	body, err := io.ReadAll(resp.Body)
	if err != nil {
		t.Fatalf("reading the response: %v", err)
	}
	if err := resp.Body.Close(); err != nil {
		t.Fatalf("closing the response: %v", err)
	}
	if len(body) == 0 {
		t.Error("the response carried no body")
	}
	if got := fx.srv.InFlightRequests(); got != 0 {
		t.Errorf("in-flight requests after the response completed = %d, want 0", got)
	}

	// Completing the response restarts the budget from now.
	wantCompletionStamp := start.Add(35 * time.Minute)
	if got := fx.srv.LastActivity(); !got.Equal(wantCompletionStamp) {
		t.Errorf("LastActivity after the response = %v, want %v", got, wantCompletionStamp)
	}
	if left, armed := fx.srv.IdleRemaining(); !armed || left != time.Hour {
		t.Errorf("idle budget after the response = %v (armed %v), want the full 1h0m0s", left, armed)
	}
}

// Closing a body that was already read to EOF must not stamp twice, and a body
// that is closed without being read must still stamp — the caller may only ever
// do one of the two.
func TestActivityBodyMarksExactlyOnce(t *testing.T) {
	for _, tc := range []struct {
		name string
		use  func(t *testing.T, resp *http.Response)
	}{
		{"read then close", func(t *testing.T, resp *http.Response) {
			if _, err := io.ReadAll(resp.Body); err != nil {
				t.Fatalf("reading: %v", err)
			}
			if err := resp.Body.Close(); err != nil {
				t.Fatalf("closing: %v", err)
			}
		}},
		{"close without reading", func(t *testing.T, resp *http.Response) {
			if err := resp.Body.Close(); err != nil {
				t.Fatalf("closing: %v", err)
			}
		}},
	} {
		t.Run(tc.name, func(t *testing.T) {
			// Fresh doubles per case: the point is that ONE response stamps
			// activity exactly once, whichever way the caller finishes it.
			loader := &fakeLoader{}
			stub := &stubTransport{body: `{"ok":true}`}
			tr := NewEnsureLoadedTransport(stub, loader, time.Minute, nil)

			req, err := http.NewRequestWithContext(t.Context(), http.MethodPost, "http://127.0.0.1:9/v1/chat/completions", http.NoBody)
			if err != nil {
				t.Fatalf("building the request: %v", err)
			}
			// The case under test owns the body: closing it IS the assertion.
			resp, err := tr.RoundTrip(req) //nolint:bodyclose // closed by tc.use, which is what the case verifies
			if err != nil {
				t.Fatalf("RoundTrip: %v", err)
			}
			if got := loader.markCount(); got != 0 {
				t.Fatalf("marks before the response completed = %d, want 0", got)
			}
			// The in-flight count brackets the SAME window: it is what keeps a
			// generation longer than the idle budget from being killed mid-answer.
			if got := loader.beginCount(); got != 1 {
				t.Fatalf("BeginRequest calls = %d, want exactly 1 before the body is read", got)
			}
			if got := loader.endCount(); got != 0 {
				t.Fatalf("EndRequest calls = %d, want 0 while the body is still open", got)
			}
			tc.use(t, resp)
			if got := loader.markCount(); got != 1 {
				t.Errorf("marks after the response completed = %d, want exactly 1", got)
			}
			if got := loader.endCount(); got != 1 {
				t.Errorf("EndRequest calls after the response completed = %d, want exactly 1 — "+
					"a second release would take another request's slot and let the idle path "+
					"unload a model still in use", got)
			}
		})
	}
}

// A request that fails before it produces a body must not leave the in-flight
// count behind. A leaked count is not the permanent residency it once was —
// idle.go bounds one deferral episode by wall time — but it still holds the model
// resident for a FULL EXTRA IDLE BUDGET past the operator's own, and every
// request that leaks a count pays that again. And a loader that does not
// implement the optional capability must still work — the count is an addition,
// not a requirement.
func TestInFlightCountIsReleasedOnAFailedRoundTrip(t *testing.T) {
	t.Parallel()

	loader := &fakeLoader{}
	stub := &stubTransport{err: errors.New("the socket is gone")}
	tr := NewEnsureLoadedTransport(stub, loader, time.Minute, nil)

	req, err := http.NewRequestWithContext(t.Context(), http.MethodPost,
		"http://127.0.0.1:9/v1/chat/completions", http.NoBody)
	if err != nil {
		t.Fatalf("building the request: %v", err)
	}
	resp, err := tr.RoundTrip(req)
	if err == nil {
		_ = resp.Body.Close()
		t.Fatal("RoundTrip succeeded against a failing transport")
	}
	if got := loader.beginCount(); got != 1 {
		t.Errorf("BeginRequest calls = %d, want 1", got)
	}
	if got := loader.endCount(); got != 1 {
		t.Errorf("EndRequest calls after a failed request = %d, want 1", got)
	}
	if got := loader.markCount(); got != 0 {
		t.Errorf("marks after a failed request = %d, want 0 (nothing was served)", got)
	}
}

// A loader that only satisfies Loader (no request tracking) must be tolerated:
// the capability is optional, exactly like PortSource, so an existing double or
// an embedding that does not track requests keeps the pre-existing behaviour.
func TestLoaderWithoutRequestTrackingIsTolerated(t *testing.T) {
	t.Parallel()

	loader := &untrackedLoader{}
	stub := &stubTransport{body: `{"ok":true}`}
	tr := NewEnsureLoadedTransport(stub, loader, time.Minute, nil)

	req, err := http.NewRequestWithContext(t.Context(), http.MethodGet,
		"http://127.0.0.1:9/v1/models", http.NoBody)
	if err != nil {
		t.Fatalf("building the request: %v", err)
	}
	resp, err := tr.RoundTrip(req)
	if err != nil {
		t.Fatalf("RoundTrip: %v", err)
	}
	if _, err := io.ReadAll(resp.Body); err != nil {
		t.Fatalf("reading the response: %v", err)
	}
	if err := resp.Body.Close(); err != nil {
		t.Fatalf("closing the response: %v", err)
	}
	if got := loader.markCount(); got != 1 {
		t.Errorf("marks = %d, want 1 — activity is Loader's own contract, not the tracker's", got)
	}
}

// TestARequestThatOutlivesTheIdleBudgetIsNotKilled is the end-to-end pin of the
// mid-flight rule: the idle budget expires WHILE a response is still open, and
// the server must survive it. Stamping activity on completion cannot express
// this — the completion is still in the future when the timer fires — so the
// in-flight count is what defers the unload. Once the response does complete, the
// deferral ends with it and the model goes on the next expiry.
func TestARequestThatOutlivesTheIdleBudgetIsNotKilled(t *testing.T) {
	clock := newFakeClock(time.Date(2026, 1, 1, 0, 0, 0, 0, time.UTC))
	fx := newFixture(t, fixtureOptions{
		now:        clock.Now,
		autoUnload: AutoUnload{Enabled: true, Idle: time.Minute},
	})
	client := EnsureLoadedClient(nil, &http.Client{Timeout: 30 * time.Second}, fx.srv, 0, nil)

	req, err := http.NewRequestWithContext(context.Background(), http.MethodGet,
		providerBaseURL(fx)+"/models", http.NoBody)
	if err != nil {
		t.Fatalf("building the request: %v", err)
	}
	resp, err := client.Do(req)
	if err != nil {
		t.Fatalf("the request must succeed: %v", err)
	}
	t.Cleanup(func() {
		if err := resp.Body.Close(); err != nil {
			t.Errorf("cleanup response: %v", err)
		}
	})
	proc := fx.spawner.lastProcess()

	// Several times the whole budget, with the response still open — the shape a
	// streamed generation longer than an aggressive `auto_unload.minutes` has.
	clock.advance(2 * time.Minute)
	fx.srv.onIdleExpired() // The response is open when the expiry decision runs.
	if left, armed := fx.srv.IdleRemaining(); !armed || left != idleGracePeriod {
		t.Errorf("deferred idle = %v/%t, want grace/true", left, armed)
	}
	if !proc.alive() {
		t.Error("the model was stopped mid-generation, killing the answer it was serving")
	}
	if got := fx.srv.State(); got != StateLoaded {
		t.Errorf("state = %s, want %s while a request is in flight", got, StateLoaded)
	}
	if got := fx.srv.InFlightRequests(); got != 1 {
		t.Errorf("in-flight requests = %d, want 1 while the response is open", got)
	}

	// Completing the response releases the count and stamps a fresh budget; the
	// expiry after that has nothing left to defer for, so the model does go.
	if _, err := io.ReadAll(resp.Body); err != nil {
		t.Fatalf("reading the response: %v", err)
	}
	if err := resp.Body.Close(); err != nil {
		t.Fatalf("closing the response: %v", err)
	}
	if got := fx.srv.InFlightRequests(); got != 0 {
		t.Fatalf("requests after body close = %d, want 0", got)
	}
	clock.advance(time.Minute)
	fx.srv.onIdleExpired()
	if proc.alive() || fx.srv.State() != StateInstalled {
		t.Error("completed request's next idle expiry did not unload")
	}
	waitForEventState(t, fx.events, StateInstalled)
}

// ---------------------------------------------------------------------------
// A bounded wait, not a hang
// ---------------------------------------------------------------------------

// Exceeding the transport's own wait budget must be an explicit, typed error —
// the request must not be sent to a server that is not listening — and it must
// NOT touch the load: the budget bounds the waiting, so the in-flight load runs
// on to completion (bounded by the supervisor's own ready budget) and a following
// request joins it instead of paying for a second cold start.
func TestLoadWaitBudgetExpiryIsAnExplicitError(t *testing.T) {
	synctest.Test(t, func(t *testing.T) {
		// A load that never finishes on its own. It blocks on `block` and reports
		// through loadDone whether anything ever cancelled it, which is the property
		// under test: the transport's wait budget must not reach it.
		release := make(chan struct{})
		var releaseOnce sync.Once
		defer func() {
			releaseOnce.Do(func() { close(release) })
			synctest.Wait() // Release before joining the detached load and waiters.
		}()
		loadDone := make(chan error, 8)
		loader := &fakeLoader{onLoad: func(ctx context.Context) error {
			select {
			case <-release:
				loadDone <- nil
				return nil
			case <-ctx.Done():
				loadDone <- ctx.Err()
				return ctx.Err()
			}
		}}
		stub := &stubTransport{}
		tr := NewEnsureLoadedTransport(stub, loader, 25*time.Millisecond, nil)

		req, err := http.NewRequestWithContext(t.Context(), http.MethodGet, "http://127.0.0.1:9/v1/models", http.NoBody)
		if err != nil {
			t.Fatalf("building the request: %v", err)
		}

		startedAt := time.Now()
		resp, err := tr.RoundTrip(req)
		elapsed := time.Since(startedAt)

		if resp != nil {
			_ = resp.Body.Close()
			t.Error("a request whose load budget expired must not produce a response")
		}
		if err == nil {
			t.Fatal("err = nil, want the load wait budget to be reported")
		}
		if !errors.Is(err, ErrLoadWaitTimeout) {
			t.Errorf("err = %v, want it to match ErrLoadWaitTimeout", err)
		}
		if !errors.Is(err, context.DeadlineExceeded) {
			t.Errorf("err = %v, want it to also carry the deadline", err)
		}
		if got := stub.callCount(); got != 0 {
			t.Errorf("the wrapped transport was called %d times, want 0", got)
		}
		if elapsed > 5*time.Second {
			t.Errorf("the wait took %v — the budget must bound it, not hang", elapsed)
		}
		// The context the loader saw is the transport's own detached one, never the
		// caller's, and the wait budget must NOT have cancelled it: a cancelled Load
		// discards the half-loaded weights, so the next request would start from zero
		// and the model would never become resident.
		loadCtx := loader.capturedCtx()
		if loadCtx == nil {
			t.Fatal("the loader captured no context")
		}
		if ctxErr := loadCtx.Err(); ctxErr != nil {
			t.Errorf("the load context was cancelled (%v) — the wait budget must stop the WAIT, not the load", ctxErr)
		}

		// A SECOND request joins the still-running load rather than starting another,
		// and completes with it — the property the detachment exists for.
		second := make(chan error, 1)
		go func() {
			req2, reqErr := http.NewRequestWithContext(context.Background(), http.MethodGet,
				"http://127.0.0.1:9/v1/models", http.NoBody)
			if reqErr != nil {
				second <- reqErr
				return
			}
			resp2, rtErr := tr.RoundTrip(req2)
			if rtErr != nil {
				second <- rtErr
				return
			}
			_, _ = io.Copy(io.Discard, resp2.Body)
			_ = resp2.Body.Close()
			second <- nil
		}()
		synctest.Wait()
		if got := tr.waiters.Load(); got != 1 {
			t.Fatalf("joined waiters = %d, want 1", got)
		}
		releaseOnce.Do(func() { close(release) })

		if err := <-second; err != nil {
			t.Errorf("the request that joined the load failed: %v", err)
		}
		if err := <-loadDone; err != nil {
			t.Errorf("the load was cancelled after the wait budget expired: %v", err)
		}
		if got := loader.loadCount(); got != 1 {
			t.Errorf("Load calls = %d, want 1 (an expired wait budget must not waste the load)", got)
		}
		if got := stub.callCount(); got != 1 {
			t.Errorf("requests reaching the wrapped transport = %d, want 1", got)
		}
	})
}

// timeouts.llmRequestTimeout (default 10 min) is SHORTER than the supervisor's
// ready budget (15 min), so a request that runs out of patience must stop
// waiting without cancelling the load — otherwise the half-loaded weights are
// thrown away and the next request starts from zero, so the model would never
// become resident.
func TestRequestCancellationStopsWaitingButNotTheLoad(t *testing.T) {
	synctest.Test(t, func(t *testing.T) {
		release := make(chan struct{})
		var releaseOnce sync.Once
		defer func() {
			releaseOnce.Do(func() { close(release) })
			synctest.Wait() // Release before joining the detached load and waiters.
		}()
		// Buffered well past one: a coalescing regression would start a second load,
		// and that must fail the assertion rather than block on an unbuffered send.
		loadDone := make(chan error, 8)
		loader := &fakeLoader{onLoad: func(ctx context.Context) error {
			select {
			case <-release:
				loadDone <- nil
				return nil
			case <-ctx.Done():
				loadDone <- ctx.Err()
				return ctx.Err()
			}
		}}
		stub := &stubTransport{}
		tr := NewEnsureLoadedTransport(stub, loader, time.Minute, nil)

		ctx, cancel := context.WithTimeout(context.Background(), 25*time.Millisecond)
		defer cancel()
		req, err := http.NewRequestWithContext(ctx, http.MethodGet, "http://127.0.0.1:9/v1/models", http.NoBody)
		if err != nil {
			t.Fatalf("building the request: %v", err)
		}
		abandoned, err := tr.RoundTrip(req)
		if abandoned != nil {
			_ = abandoned.Body.Close()
			t.Error("a request that stopped waiting must not produce a response")
		}
		if !errors.Is(err, context.DeadlineExceeded) {
			t.Fatalf("err = %v, want the caller's own deadline", err)
		}
		if got := stub.callCount(); got != 0 {
			t.Errorf("the wrapped transport was called %d times, want 0", got)
		}

		// The load is still running, detached from the dead request.
		loadCtx := loader.capturedCtx()
		if loadCtx == nil {
			t.Fatal("the loader captured no context")
		}
		if err := loadCtx.Err(); err != nil {
			t.Fatalf("the load context was cancelled with the request (%v) — the load must be detached", err)
		}

		// A SECOND request joins the load the first one started instead of starting
		// another, which is the whole point of detaching it.
		synctest.Wait()
		if got := tr.waiters.Load(); got != 0 {
			t.Fatalf("waiters after cancellation = %d, want 0", got)
		}
		secondErr := make(chan error, 1)
		go func() {
			req2, err := http.NewRequestWithContext(t.Context(), http.MethodGet, "http://127.0.0.1:9/v1/models", http.NoBody)
			if err != nil {
				secondErr <- err
				return
			}
			resp, err := tr.RoundTrip(req2)
			if err != nil {
				secondErr <- err
				return
			}
			_, _ = io.Copy(io.Discard, resp.Body)
			_ = resp.Body.Close()
			secondErr <- nil
		}()
		synctest.Wait()
		if got := tr.waiters.Load(); got != 1 {
			t.Fatalf("joined waiters = %d, want 1", got)
		}
		releaseOnce.Do(func() { close(release) })

		if err := <-secondErr; err != nil {
			t.Errorf("the request that joined the detached load failed: %v", err)
		}
		if err := <-loadDone; err != nil {
			t.Errorf("the detached load failed: %v", err)
		}
		if got := loader.loadCount(); got != 1 {
			t.Errorf("Load calls = %d, want 1 (the cancelled request must not waste the load)", got)
		}
		if got := stub.callCount(); got != 1 {
			t.Errorf("requests reaching the wrapped transport = %d, want 1", got)
		}
	})
}

// A load that fails for its own reason reports that reason — not a timeout — and
// the next request tries again instead of being poisoned by the failure.
func TestLoadFailureIsReportedAndRetried(t *testing.T) {
	loader := &fakeLoader{err: errLoadFailed}
	stub := &stubTransport{}
	tr := NewEnsureLoadedTransport(stub, loader, time.Minute, nil)

	req, err := http.NewRequestWithContext(t.Context(), http.MethodGet, "http://127.0.0.1:9/v1/models", http.NoBody)
	if err != nil {
		t.Fatalf("building the request: %v", err)
	}
	failed, err := tr.RoundTrip(req)
	if failed != nil {
		_ = failed.Body.Close()
		t.Error("a failed load must not produce a response")
	}
	if !errors.Is(err, errLoadFailed) {
		t.Fatalf("err = %v, want the loader's own failure", err)
	}
	if errors.Is(err, ErrLoadWaitTimeout) {
		t.Error("a genuine load failure must not be reported as a wait-budget expiry")
	}
	if got := stub.callCount(); got != 0 {
		t.Errorf("the wrapped transport was called %d times, want 0", got)
	}

	loader.mu.Lock()
	loader.err = nil
	loader.mu.Unlock()

	resp, err := tr.RoundTrip(req)
	if err != nil {
		t.Fatalf("the retry must succeed: %v", err)
	}
	_, _ = io.Copy(io.Discard, resp.Body)
	_ = resp.Body.Close()
	if got := loader.loadCount(); got != 2 {
		t.Errorf("Load calls = %d, want 2 (a failed load must not stay cached)", got)
	}
	if got := stub.callCount(); got != 1 {
		t.Errorf("requests reaching the wrapped transport = %d, want 1", got)
	}
}

// ---------------------------------------------------------------------------
// Transport plumbing
// ---------------------------------------------------------------------------

// A nil loader makes the transport a pass-through rather than a panic: the
// request path is the wrong place to fail on a wiring mistake.
func TestNilLoaderPassesRequestsThrough(t *testing.T) {
	stub := &stubTransport{}
	tr := NewEnsureLoadedTransport(stub, nil, 0, nil)

	req, err := http.NewRequestWithContext(t.Context(), http.MethodGet, "http://127.0.0.1:9/v1/models", http.NoBody)
	if err != nil {
		t.Fatalf("building the request: %v", err)
	}
	resp, err := tr.RoundTrip(req)
	if err != nil {
		t.Fatalf("RoundTrip: %v", err)
	}
	_, _ = io.Copy(io.Discard, resp.Body)
	_ = resp.Body.Close()
	if got := stub.callCount(); got != 1 {
		t.Errorf("wrapped transport calls = %d, want 1", got)
	}
}

// A nil request is an error, not a nil dereference.
func TestNilRequestIsRejected(t *testing.T) {
	tr := NewEnsureLoadedTransport(&stubTransport{}, &fakeLoader{}, time.Second, nil)
	// The RoundTripper contract forbids it, but a caller may still do it.
	resp, err := tr.RoundTrip(nil) //nolint:staticcheck // deliberate nil argument
	if resp != nil {
		_ = resp.Body.Close()
	}
	if err == nil {
		t.Error("err = nil, want an explicit error for a nil request")
	}
}

// http.Client.CloseIdleConnections only reaches a transport that implements the
// method, so the wrapper must forward it — otherwise installing the ensure-loaded
// transport would silently strand the provider's connection pool.
func TestCloseIdleConnectionsReachesTheWrappedTransport(t *testing.T) {
	stub := &stubTransport{}
	tr := NewEnsureLoadedTransport(stub, &fakeLoader{}, time.Second, nil)

	client := &http.Client{Transport: tr}
	client.CloseIdleConnections()

	if got := stub.idleCloseCount(); got != 1 {
		t.Errorf("forwarded CloseIdleConnections = %d, want 1", got)
	}
}

// The wrapper is only correct if it leaves the request alone: RoundTrip must not
// rewrite the URL, method or body it was handed. The ONE deliberate exception is
// the loopback port, and only when the loader reports a live one (PortSource) —
// covered by the redirect tests below. This loader double reports no port, so
// the request must survive byte-identical.
func TestRequestReachesTheWrappedTransportUnchanged(t *testing.T) {
	var got *http.Request
	base := roundTripperFunc(func(req *http.Request) (*http.Response, error) {
		got = req
		return &http.Response{
			StatusCode: http.StatusOK,
			Header:     http.Header{},
			Body:       io.NopCloser(strings.NewReader("{}")),
			Request:    req,
		}, nil
	})
	tr := NewEnsureLoadedTransport(base, &fakeLoader{}, time.Second, nil)

	req, err := http.NewRequestWithContext(t.Context(), http.MethodPost, "http://127.0.0.1:9/v1/chat/completions", strings.NewReader(`{"model":"Bonsai 2 27B"}`))
	if err != nil {
		t.Fatalf("building the request: %v", err)
	}
	req.Header.Set("Authorization", "Bearer k")
	resp, err := tr.RoundTrip(req)
	if err != nil {
		t.Fatalf("RoundTrip: %v", err)
	}
	_ = resp.Body.Close()

	if got == nil {
		t.Fatal("the wrapped transport was never called")
	}
	if got.Method != http.MethodPost || got.URL.Path != "/v1/chat/completions" {
		t.Errorf("request = %s %s, want POST /v1/chat/completions", got.Method, got.URL.Path)
	}
	if got.Header.Get("Authorization") != "Bearer k" {
		t.Error("the Authorization header was lost")
	}
}

// roundTripperFunc adapts a function to http.RoundTripper.
type roundTripperFunc func(*http.Request) (*http.Response, error)

func (f roundTripperFunc) RoundTrip(req *http.Request) (*http.Response, error) { return f(req) }

// ---------------------------------------------------------------------------
// Port redirect
// ---------------------------------------------------------------------------

// portFakeLoader is a fakeLoader that also reports a live port, so it satisfies
// the optional PortSource capability the redirect reads.
type portFakeLoader struct {
	fakeLoader
	port int
}

func (l *portFakeLoader) Port() int { return l.port }

func newLoopbackRequest(t *testing.T, port int) *http.Request {
	t.Helper()
	url := "http://" + LoopbackHost + ":" + strconv.Itoa(port) + "/v1/chat/completions"
	req, err := http.NewRequestWithContext(t.Context(), http.MethodPost, url,
		strings.NewReader(`{"model":"Bonsai 2 27B"}`))
	if err != nil {
		t.Fatalf("building the request: %v", err)
	}
	return req
}

// The router builds the request URL from the provider base_url, which is derived
// from the PERSISTED port. When that port was taken, the load moved the server to
// the next free one — so the request in hand points at a socket this process does
// not own, and sending it there would hand the prompt to whatever unrelated local
// process holds the port. It must be redirected to the live one.
func TestRequestIsRedirectedToTheLivePortAfterAMove(t *testing.T) {
	const (
		persistedPort = 52341
		livePort      = 52401
	)
	stub := &stubTransport{}
	tr := NewEnsureLoadedTransport(stub, &portFakeLoader{port: livePort}, time.Second, nil)

	req := newLoopbackRequest(t, persistedPort)
	req.Header.Set("Authorization", "Bearer k")
	resp, err := tr.RoundTrip(req)
	if err != nil {
		t.Fatalf("RoundTrip: %v", err)
	}
	_ = resp.Body.Close()

	want := "http://" + LoopbackHost + ":" + strconv.Itoa(livePort) + "/v1/chat/completions"
	if got := stub.lastSeenURL(); got != want {
		t.Errorf("the wrapped transport saw %s, want %s — a moved port must reach the wire", got, want)
	}
	// Everything but the port survives the redirect.
	if got := resp.Request.Header.Get("Authorization"); got != "Bearer k" {
		t.Errorf("Authorization = %q, want the header the caller set", got)
	}
	// http.RoundTripper must not modify the request it was handed: the rewrite
	// happens on a clone.
	if req.URL.Host != LoopbackHost+":"+strconv.Itoa(persistedPort) {
		t.Errorf("the caller's request was mutated: URL.Host = %s", req.URL.Host)
	}
}

// An explicit Host field overrides URL.Host for the header the server sees, so
// the redirect has to carry it too or the request would be aimed at one port and
// labelled with another.
func TestRedirectRewritesAnExplicitHostHeader(t *testing.T) {
	const livePort = 52402
	stub := &stubTransport{}
	var seen *http.Request
	tr := NewEnsureLoadedTransport(roundTripperFunc(func(req *http.Request) (*http.Response, error) {
		seen = req
		return stub.RoundTrip(req)
	}), &portFakeLoader{port: livePort}, time.Second, nil)

	req := newLoopbackRequest(t, 52341)
	req.Host = LoopbackHost + ":52341"
	resp, err := tr.RoundTrip(req)
	if err != nil {
		t.Fatalf("RoundTrip: %v", err)
	}
	_ = resp.Body.Close()

	want := LoopbackHost + ":" + strconv.Itoa(livePort)
	if seen == nil {
		t.Fatal("the wrapped transport was never called")
	}
	if seen.Host != want {
		t.Errorf("Host = %q, want %q", seen.Host, want)
	}
	if seen.URL.Host != want {
		t.Errorf("URL.Host = %q, want %q", seen.URL.Host, want)
	}
}

// A loader that does not report a port (every double, and any Loader that is not
// the supervisor) must leave the request exactly as it was: the capability is
// optional, and inventing a port would be worse than not redirecting.
func TestLoaderWithoutAPortLeavesTheRequestAlone(t *testing.T) {
	stub := &stubTransport{}
	tr := NewEnsureLoadedTransport(stub, &fakeLoader{}, time.Second, nil)

	req := newLoopbackRequest(t, 52341)
	resp, err := tr.RoundTrip(req)
	if err != nil {
		t.Fatalf("RoundTrip: %v", err)
	}
	_ = resp.Body.Close()

	if got, want := stub.lastSeenURL(), "http://"+LoopbackHost+":52341/v1/chat/completions"; got != want {
		t.Errorf("the wrapped transport saw %s, want %s", got, want)
	}
}

// Port 0 means "no server has been started", which is not a port to aim at. A
// redirect to it would produce an unusable URL, so the request is left alone and
// the wrapped transport reports the real failure.
func TestZeroLivePortIsNotRedirected(t *testing.T) {
	stub := &stubTransport{}
	tr := NewEnsureLoadedTransport(stub, &portFakeLoader{port: 0}, time.Second, nil)

	req := newLoopbackRequest(t, 52341)
	resp, err := tr.RoundTrip(req)
	if err != nil {
		t.Fatalf("RoundTrip: %v", err)
	}
	_ = resp.Body.Close()

	if got, want := stub.lastSeenURL(), "http://"+LoopbackHost+":52341/v1/chat/completions"; got != want {
		t.Errorf("the wrapped transport saw %s, want %s", got, want)
	}
}

// The common case: the persisted port was free, the server bound it, and the
// request already points there. The redirect must be a no-op — no clone, no
// rewritten URL.
func TestMatchingPortIsNotRedirected(t *testing.T) {
	const port = 52341
	stub := &stubTransport{}
	loader := &portFakeLoader{port: port}
	tr := NewEnsureLoadedTransport(stub, loader, time.Second, nil)

	req := newLoopbackRequest(t, port)
	resp, err := tr.RoundTrip(req)
	if err != nil {
		t.Fatalf("RoundTrip: %v", err)
	}
	_ = resp.Body.Close()

	if got, want := stub.lastSeenURL(), "http://"+LoopbackHost+":"+strconv.Itoa(port)+"/v1/chat/completions"; got != want {
		t.Errorf("the wrapped transport saw %s, want %s", got, want)
	}
	if got := redirectToLivePort(req, loader, nil); got != req {
		t.Error("a matching port must return the SAME request, not a clone")
	}
}

// ---------------------------------------------------------------------------
// The cold-load wait is NOT charged to the request
// ---------------------------------------------------------------------------

// A slow weight load must not eat the request's own timeout. http.Client.Timeout
// covers the whole exchange, gate included, so EnsureLoadedClient zeroes it and
// hands the budget to the transport, which arms it only once the model can
// answer. This is the end-to-end proof: the load takes longer than the budget
// the client was configured with, and the request still succeeds with that
// budget whole.
func TestTheLoadIsNotChargedToTheRequestBudget(t *testing.T) {
	synctest.Test(t, func(t *testing.T) {
		const requestBudget = 150 * time.Millisecond
		loader := &fakeLoader{onLoad: func(context.Context) error { time.Sleep(400 * time.Millisecond); return nil }}
		base := roundTripperFunc(func(req *http.Request) (*http.Response, error) {
			deadline, ok := req.Context().Deadline()
			if !ok || req.Context().Err() != nil || time.Until(deadline) != requestBudget {
				t.Errorf("base entry deadline=%t remaining=%v err=%v, want full %v", ok, time.Until(deadline), req.Context().Err(), requestBudget)
			}
			return (&stubTransport{}).RoundTrip(req)
		})
		client := EnsureLoadedClient(nil, &http.Client{Timeout: requestBudget, Transport: base}, loader, time.Minute, nil)
		started := time.Now()
		resp, err := client.Do(newLoopbackRequest(t, 52341))
		if err != nil {
			t.Fatal(err)
		}
		if err := resp.Body.Close(); err != nil {
			t.Fatal(err)
		}
		if elapsed := time.Since(started); elapsed != 400*time.Millisecond {
			t.Errorf("virtual exchange=%v, want 400ms", elapsed)
		}
		if got := loader.loadCount(); got != 1 {
			t.Errorf("loads = %d, want 1", got)
		}
	})
}

func TestEnsureLoadedClientRealHTTPAfterGate(t *testing.T) {
	const (
		requestBudget = 30 * time.Second
	)
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte(`{"ok":true}`))
	}))
	t.Cleanup(srv.Close)

	entered, release := make(chan struct{}), make(chan struct{})
	var once sync.Once
	loader := &fakeLoader{onLoad: func(ctx context.Context) error {
		close(entered)
		select {
		case <-release:
			return nil
		case <-ctx.Done():
			return ctx.Err()
		}
	}}
	t.Cleanup(func() { once.Do(func() { close(release) }) })
	shared := &http.Client{Timeout: requestBudget}
	client := EnsureLoadedClient(nil, shared, loader, time.Minute, nil)

	req, err := http.NewRequestWithContext(t.Context(), http.MethodGet, srv.URL+"/v1/chat/completions", http.NoBody)
	if err != nil {
		t.Fatalf("building the request: %v", err)
	}
	if client.Timeout != 0 {
		t.Fatalf("derived client Timeout = %v, want 0", client.Timeout)
	}
	type outcome struct {
		body []byte
		err  error
	}
	result := make(chan outcome, 1)
	done := make(chan struct{})
	t.Cleanup(func() {
		once.Do(func() { close(release) })
		select {
		case <-done:
		case <-time.After(10 * time.Second):
			t.Error("HTTP request cleanup did not join")
		}
	})
	go func() {
		defer close(done)
		resp, err := client.Do(req)
		if err != nil {
			result <- outcome{err: err}
			return
		}
		body, readErr := io.ReadAll(resp.Body)
		closeErr := resp.Body.Close()
		result <- outcome{body: body, err: errors.Join(readErr, closeErr)}
	}()
	select {
	case <-entered:
	case <-time.After(10 * time.Second):
		t.Fatal("loader never entered")
	}
	once.Do(func() { close(release) })
	var got outcome
	select {
	case got = <-result:
	case <-time.After(10 * time.Second):
		t.Fatal("HTTP request never completed")
	}
	if got.err != nil {
		t.Fatalf("request after released gate failed: %v", got.err)
	}
	if !strings.Contains(string(got.body), "ok") {
		t.Errorf("body = %q, want the endpoint's reply", got.body)
	}
	if loader.loadCount() != 1 {
		t.Errorf("loads = %d, want 1", loader.loadCount())
	}
}

// The budget is armed AFTER the gate, not before it: the base transport must see
// a deadline of (almost) the full requestTimeout even though the load took a
// visible slice of time first.
func TestTheRequestBudgetIsArmedOnlyAfterTheGate(t *testing.T) {
	synctest.Test(t, func(t *testing.T) {
		const (
			requestBudget = time.Second
			loadTime      = 300 * time.Millisecond
		)
		var (
			remaining time.Duration
			hasDDL    bool
		)
		base := roundTripperFunc(func(req *http.Request) (*http.Response, error) {
			deadline, ok := req.Context().Deadline()
			hasDDL = ok
			remaining = time.Until(deadline)
			return &http.Response{
				StatusCode: http.StatusOK,
				Header:     http.Header{},
				Body:       io.NopCloser(strings.NewReader("{}")),
				Request:    req,
			}, nil
		})
		loader := &fakeLoader{onLoad: func(context.Context) error {
			time.Sleep(loadTime)
			return nil
		}}
		tr := newEnsureLoadedTransport(base, loader, time.Minute, requestBudget, nil)

		resp, err := tr.RoundTrip(newLoopbackRequest(t, 52341))
		if err != nil {
			t.Fatalf("RoundTrip: %v", err)
		}
		_ = resp.Body.Close()

		if !hasDDL {
			t.Fatal("the request reached the base transport with no deadline: the configured " +
				"budget was dropped instead of moved")
		}
		// Armed before the gate, the remaining budget would be requestBudget minus
		// the load; armed after it, the load is free.
		if remaining != requestBudget {
			t.Errorf("remaining budget = %v, want ≈%v — the %v load was charged to it",
				remaining, requestBudget, loadTime)
		}
	})
}

// The exported constructor keeps the historical contract: no request budget of
// its own, so the request runs on whatever deadline its caller gave it.
func TestNewEnsureLoadedTransportArmsNoRequestBudget(t *testing.T) {
	var hasDDL bool
	base := roundTripperFunc(func(req *http.Request) (*http.Response, error) {
		_, hasDDL = req.Context().Deadline()
		return &http.Response{
			StatusCode: http.StatusOK,
			Header:     http.Header{},
			Body:       io.NopCloser(strings.NewReader("{}")),
			Request:    req,
		}, nil
	})
	tr := NewEnsureLoadedTransport(base, &fakeLoader{}, time.Minute, nil)
	if tr.requestTimeout != 0 {
		t.Errorf("requestTimeout = %v, want 0", tr.requestTimeout)
	}

	resp, err := tr.RoundTrip(newLoopbackRequest(t, 52341))
	if err != nil {
		t.Fatalf("RoundTrip: %v", err)
	}
	_ = resp.Body.Close()
	if hasDDL {
		t.Error("the transport armed a deadline nobody asked for")
	}
}

// The deadline must outlive RoundTrip: a streamed generation is read long after
// the headers arrive, so cancelling on return would tear the connection down
// mid-body. Closing the body is what releases it.
func TestTheRequestDeadlineOutlivesRoundTripAndIsReleasedOnClose(t *testing.T) {
	var reqCtx context.Context
	base := roundTripperFunc(func(req *http.Request) (*http.Response, error) {
		reqCtx = req.Context()
		return &http.Response{
			StatusCode: http.StatusOK,
			Header:     http.Header{},
			Body:       io.NopCloser(strings.NewReader("streamed payload")),
			Request:    req,
		}, nil
	})
	tr := newEnsureLoadedTransport(base, &fakeLoader{}, time.Minute, 30*time.Second, nil)

	resp, err := tr.RoundTrip(newLoopbackRequest(t, 52341))
	if err != nil {
		t.Fatalf("RoundTrip: %v", err)
	}
	if reqCtx == nil {
		t.Fatal("the base transport saw no request context")
	}
	body, err := io.ReadAll(resp.Body)
	if err != nil {
		t.Fatalf("reading the body after RoundTrip returned: %v — the deadline was "+
			"released too early and tore the response down", err)
	}
	if string(body) != "streamed payload" {
		t.Errorf("body = %q", body)
	}
	if reqCtx.Err() != nil {
		t.Errorf("the request context was cancelled before the body was closed: %v", reqCtx.Err())
	}
	if err := resp.Body.Close(); err != nil {
		t.Fatalf("closing the body: %v", err)
	}
	if reqCtx.Err() == nil {
		t.Error("closing the body did not release the request deadline — the timer leaks")
	}
}

// The moved budget is still enforced: a server that outlives it fails the
// request rather than hanging the session forever.
func TestARequestThatOutlivesItsBudgetFails(t *testing.T) {
	release := make(chan struct{})
	t.Cleanup(func() { close(release) })
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		select {
		case <-release:
		case <-time.After(5 * time.Second):
		}
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte(`{"ok":true}`))
	}))
	t.Cleanup(srv.Close)
	srv.Config.ErrorLog = log.New(io.Discard, "", 0)

	client := EnsureLoadedClient(nil, &http.Client{Timeout: 120 * time.Millisecond}, &fakeLoader{}, time.Minute, nil)
	req, err := http.NewRequestWithContext(t.Context(), http.MethodGet, srv.URL+"/v1/chat/completions", http.NoBody)
	if err != nil {
		t.Fatalf("building the request: %v", err)
	}
	resp, err := client.Do(req)
	if resp != nil {
		_ = resp.Body.Close()
	}
	if err == nil {
		t.Fatal("a server that outlives the request budget must fail the call")
	}
	if !errors.Is(err, context.DeadlineExceeded) && !strings.Contains(err.Error(), "deadline") {
		t.Errorf("err = %v, want a deadline expiry", err)
	}
}

// A base that violates the RoundTripper contract must not panic the request path
// — and must not strand the armed deadline's timer either.
func TestANilResponseIsAnErrorNotAPanic(t *testing.T) {
	var reqCtx context.Context
	base := roundTripperFunc(func(req *http.Request) (*http.Response, error) {
		reqCtx = req.Context()
		return nil, nil
	})
	tr := newEnsureLoadedTransport(base, &fakeLoader{}, time.Minute, 30*time.Second, nil)

	resp, err := tr.RoundTrip(newLoopbackRequest(t, 52341))
	if resp != nil {
		if resp.Body != nil {
			_ = resp.Body.Close()
		}
		t.Errorf("response = %+v, want nil", resp)
	}
	if err == nil {
		t.Fatal("a (nil, nil) RoundTrip must be reported, not passed through")
	}
	if reqCtx == nil {
		t.Fatal("the base transport saw no request context")
	}
	if reqCtx.Err() == nil {
		t.Error("the armed request deadline was not released — its timer leaks")
	}
}
