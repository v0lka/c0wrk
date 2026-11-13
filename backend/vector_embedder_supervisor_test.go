package backend

import (
	"context"
	"errors"
	"log/slog"
	"sync"
	"testing"
	"time"

	"github.com/v0lka/sp4rk/embedding"
)

// Virtual-time tests for the embedder supervisor (backend/vector_embedder_supervisor.go):
// the embed deadline never fires on a real timer — the supervisor's newTimer seam
// hands every bounded call a test-owned channel, and the tests fire it only after a
// channel barrier proves the detached embed call is parked inside its fake embedder.
// Probe and restart goroutines are joined through the supervisor's probeWG; the
// abandoned detached calls are released through a shared channel whose close is
// registered as cleanup BEFORE any fatal assertion. No sleeps, no real timers.

const (
	testProviderRequested = "cuda"
	testProviderCPU       = embedding.ExecutionProviderCPU
	testEmbedDim          = 4
)

// supLogEntry is one captured record: the assertions target exact
// (level, message) counts, never global warn totals.
type supLogEntry struct {
	level slog.Level
	msg   string
}

// supLogRecorder is the test-local slog handler: scoped capture, no global
// redirect. Cleanup needs no restore — the handler only lives on the
// supervisor's own logger.
type supLogRecorder struct {
	mu      sync.Mutex
	entries []supLogEntry
}

func (r *supLogRecorder) Enabled(_ context.Context, _ slog.Level) bool { return true }

func (r *supLogRecorder) Handle(_ context.Context, rec slog.Record) error {
	r.mu.Lock()
	defer r.mu.Unlock()
	r.entries = append(r.entries, supLogEntry{level: rec.Level, msg: rec.Message})
	return nil
}

func (r *supLogRecorder) WithAttrs(_ []slog.Attr) slog.Handler { return r }
func (r *supLogRecorder) WithGroup(_ string) slog.Handler      { return r }

func (r *supLogRecorder) count(level slog.Level, msg string) int {
	r.mu.Lock()
	defer r.mu.Unlock()
	n := 0
	for _, e := range r.entries {
		if e.level == level && e.msg == msg {
			n++
		}
	}
	return n
}

// fakeSupEmbedder is a liveEmbedder fake. Zero-value wedgedFn reports "not
// wedged" (the idle fresh instance a restart swaps in); wedgedFn is
// replaceable mid-test so one fake can start wedged while its successor
// stays clean.
type fakeSupEmbedder struct {
	mu         sync.Mutex
	provider   string
	wedgedFn   func(bound time.Duration) bool
	docsFn     func(ctx context.Context, texts []string) ([][]float32, error)
	queryFn    func(ctx context.Context, text string) ([]float32, error)
	closeFn    func() error
	docsCalls  int
	queryCalls int
	closeCalls int
}

func (f *fakeSupEmbedder) EmbedDocuments(ctx context.Context, texts []string) ([][]float32, error) {
	f.mu.Lock()
	f.docsCalls++
	fn := f.docsFn
	f.mu.Unlock()
	if fn != nil {
		return fn(ctx, texts)
	}
	return healthyVectors(len(texts)), nil
}

func (f *fakeSupEmbedder) EmbedQuery(ctx context.Context, text string) ([]float32, error) {
	f.mu.Lock()
	f.queryCalls++
	fn := f.queryFn
	f.mu.Unlock()
	if fn != nil {
		return fn(ctx, text)
	}
	return healthyVectors(1)[0], nil
}

func (f *fakeSupEmbedder) Wedged(bound time.Duration) bool {
	f.mu.Lock()
	fn := f.wedgedFn
	f.mu.Unlock()
	if fn == nil {
		return false
	}
	return fn(bound)
}

func (f *fakeSupEmbedder) setWedged(fn func(bound time.Duration) bool) {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.wedgedFn = fn
}

func (f *fakeSupEmbedder) ExecutionProvider() string { return f.provider }

func (f *fakeSupEmbedder) Close() error {
	f.mu.Lock()
	f.closeCalls++
	fn := f.closeFn
	f.mu.Unlock()
	if fn != nil {
		return fn()
	}
	return nil
}

func (f *fakeSupEmbedder) wasClosed() bool {
	f.mu.Lock()
	defer f.mu.Unlock()
	return f.closeCalls > 0
}

func healthyVectors(n int) [][]float32 {
	vecs := make([][]float32, n)
	for i := range vecs {
		vecs[i] = make([]float32, testEmbedDim)
	}
	return vecs
}

// supHarness wires a supervisor with recording seams and the virtual-time
// machinery: per-call timer channels (fired explicitly), a probe wrapper
// that records the consulted bounds and reads the LIVE instance exactly like
// production, and blocking-fake barriers (entered/left/release) with the
// release registered as cleanup before any assertion can run.
type supHarness struct {
	t   *testing.T
	sup *embedderSupervisor

	logs *supLogRecorder

	mu             sync.Mutex
	buildProviders []string
	buildFn        func(provider string) (liveEmbedder, error)
	probeBounds    []time.Duration
	swapProviders  []string
	// timerCreated carries every timer channel the supervisor's newTimer
	// seam hands out, in creation order. Consuming a push proves the OUTER
	// bounded call has built its timer and is at its select — the only
	// safe moment to fire it (the entered barrier alone proves the
	// DETACHED call parked, not that the outer reached the select).
	timerCreated chan chan time.Time

	releaseOnce sync.Once
	release     chan struct{}
	entered     chan struct{}
	left        chan struct{}
}

func newSupHarness(t *testing.T, shuttingDown func() bool) *supHarness {
	t.Helper()
	h := &supHarness{
		t:            t,
		logs:         &supLogRecorder{},
		release:      make(chan struct{}),
		entered:      make(chan struct{}, 16),
		left:         make(chan struct{}, 16),
		timerCreated: make(chan chan time.Time, 16),
	}
	h.sup = newEmbedderSupervisor(embedderSupervisorConfig{
		Log:               slog.New(h.logs),
		RequestedProvider: testProviderRequested,
		ShuttingDown:      shuttingDown,
	})
	// Build seam: records every provider request, then delegates (nil fn =
	// loud unexpected-build failure that still lands in the record).
	h.sup.newEmbedder = func(provider string) (liveEmbedder, error) {
		h.mu.Lock()
		h.buildProviders = append(h.buildProviders, provider)
		fn := h.buildFn
		h.mu.Unlock()
		if fn == nil {
			return nil, errors.New("unexpected embedder build in test")
		}
		return fn(provider)
	}
	// Probe seam: production shape (read the LIVE instance) plus bound
	// recording so tests can pin "the probe is consulted with the embed
	// deadline". The forward mirrors the production wiring — the
	// supervisor's bound-free wedged() is the Shutdown-drain surface and
	// applies its own deadline, not this internal seam.
	h.sup.wedgedProbe = func(bound time.Duration) bool {
		h.mu.Lock()
		h.probeBounds = append(h.probeBounds, bound)
		h.mu.Unlock()
		h.sup.mu.Lock()
		cur := h.sup.cur
		h.sup.mu.Unlock()
		if cur == nil {
			return false
		}
		return cur.Wedged(bound)
	}
	h.sup.onSwapped = func(provider string, _ liveEmbedder) {
		h.mu.Lock()
		defer h.mu.Unlock()
		h.swapProviders = append(h.swapProviders, provider)
	}
	// Virtual time: every bounded call gets a fresh test-owned channel,
	// published so the test can pair the fire with its owner.
	h.sup.newTimer = func(time.Duration) (<-chan time.Time, stopFunc) {
		ch := make(chan time.Time, 1)
		h.timerCreated <- ch
		return ch, func() {}
	}
	// Release the abandoned detached calls BEFORE asserts can fail (they
	// block on h.release; their completion tokens land in the buffered
	// h.left, so nothing leaks past the test function).
	t.Cleanup(h.releaseBlocking)
	return h
}

// --- instance helpers ---

// setInstance installs a supervised instance post-construction (the harness
// builds its first fake after the supervisor, so the barriers exist first).
func (h *supHarness) setInstance(f *fakeSupEmbedder) *fakeSupEmbedder {
	h.sup.mu.Lock()
	h.sup.cur = f
	h.sup.mu.Unlock()
	return f
}

// newBlockingEmbedder returns a fake whose EmbedDocuments parks on the
// harness barriers: signals entered (parked inside the fake), blocks on
// release, signals left (completion token) on the way out.
func (h *supHarness) newBlockingEmbedder(provider string) *fakeSupEmbedder {
	f := &fakeSupEmbedder{provider: provider}
	f.docsFn = func(_ context.Context, _ []string) ([][]float32, error) {
		h.entered <- struct{}{}
		<-h.release
		h.left <- struct{}{}
		return nil, nil
	}
	return f
}

func (h *supHarness) releaseBlocking() {
	h.releaseOnce.Do(func() { close(h.release) })
}

// --- virtual time ---

// fireThisCallTimer consumes the outer bounded call's timer-channel
// publication (proof it is at its select) and fires that exact channel.
func (h *supHarness) fireThisCallTimer() {
	ch := <-h.timerCreated
	ch <- time.Time{} // buffered 1: never blocks
}

// timedCall runs one bounded EmbedDocuments to its transient timeout. Two
// barriers make it deterministic: the detached call must be observed parked
// inside the blocking fake (entered — the result channel cannot win), and
// the outer call must be observed at its select (timer publication — the
// fire lands on the right channel).
func (h *supHarness) timedCall(texts ...string) error {
	h.t.Helper()
	errCh := make(chan error, 1)
	go func() {
		_, err := h.sup.EmbedDocuments(context.Background(), texts)
		errCh <- err
	}()
	<-h.entered
	h.fireThisCallTimer()
	return <-errCh
}

// joinProbes waits for every probe goroutine a timeout launched.
func (h *supHarness) joinProbes() {
	h.t.Helper()
	h.sup.probeWG.Wait()
}

// --- observations ---

func (h *supHarness) providersBuilt() []string {
	h.mu.Lock()
	defer h.mu.Unlock()
	return append([]string(nil), h.buildProviders...)
}

func (h *supHarness) probeBoundsSeen() []time.Duration {
	h.mu.Lock()
	defer h.mu.Unlock()
	return append([]time.Duration(nil), h.probeBounds...)
}

func (h *supHarness) swapsSeen() []string {
	h.mu.Lock()
	defer h.mu.Unlock()
	return append([]string(nil), h.swapProviders...)
}

func (h *supHarness) currentInstance() liveEmbedder {
	h.sup.mu.Lock()
	defer h.sup.mu.Unlock()
	return h.sup.cur
}

func (h *supHarness) restartFailures() int {
	h.sup.mu.Lock()
	defer h.sup.mu.Unlock()
	return h.sup.restartFailures
}

func (h *supHarness) setBuild(fn func(provider string) (liveEmbedder, error)) {
	h.mu.Lock()
	defer h.mu.Unlock()
	h.buildFn = fn
}

func closeOnce(ch chan struct{}) {
	select {
	case <-ch: // already closed
	default:
		close(ch)
	}
}

// TestEmbedderSupervisor_SuccessPassesThrough pins the fast path: a call that
// finishes inside the deadline delivers the embedder's result untouched, and
// neither a restart nor a timeout diagnostic happens.
func TestEmbedderSupervisor_SuccessPassesThrough(t *testing.T) {
	h := newSupHarness(t, nil)
	h.setInstance(&fakeSupEmbedder{provider: testProviderRequested})

	vecs, err := h.sup.EmbedDocuments(context.Background(), []string{"a", "b"})
	if err != nil {
		t.Fatalf("EmbedDocuments: %v", err)
	}
	if len(vecs) != 2 || len(vecs[0]) != testEmbedDim {
		t.Fatalf("vectors = %v, want 2x%d", vecs, testEmbedDim)
	}
	if got := h.providersBuilt(); len(got) != 0 {
		t.Errorf("healthy call must not build an embedder, built %v", got)
	}
	if n := h.logs.count(slog.LevelWarn, "batch embed call exceeded its deadline; returning a transient error"); n != 0 {
		t.Errorf("healthy call logged %d timeout warnings, want 0", n)
	}
}

// TestEmbedderSupervisor_TimeoutReturnsTransientError pins the AC "таймаут
// возвращается": the bounded call returns errEmbedCallTimeout while the
// detached call keeps flowing, and — the probe saying NOT wedged (a
// slow-but-alive batch) — no restart is attempted.
func TestEmbedderSupervisor_TimeoutReturnsTransientError(t *testing.T) {
	h := newSupHarness(t, nil)
	fake := h.setInstance(h.newBlockingEmbedder(testProviderRequested))
	fake.setWedged(func(time.Duration) bool { return false })

	err := h.timedCall("x")
	if !errors.Is(err, errEmbedCallTimeout) {
		t.Fatalf("err = %v, want errEmbedCallTimeout", err)
	}
	if errors.Is(err, context.DeadlineExceeded) {
		t.Errorf("timeout error must be the supervisor's transient sentinel, not ctx DeadlineExceeded: %v", err)
	}

	h.joinProbes()
	if got := h.providersBuilt(); len(got) != 0 {
		t.Errorf("not-wedged verdict must not restart, built %v", got)
	}
	// The probe is consulted with the embed deadline (the step_10 contract).
	if bounds := h.probeBoundsSeen(); len(bounds) != 1 || bounds[0] != h.sup.deadline {
		t.Errorf("probe bounds = %v, want exactly one consultation with the embed deadline %v", bounds, h.sup.deadline)
	}
	if n := h.logs.count(slog.LevelWarn, "batch embed call exceeded its deadline; returning a transient error"); n != 1 {
		t.Errorf("timeout warnings = %d, want exactly 1", n)
	}
	if n := h.logs.count(slog.LevelDebug, "embed call timed out but the ONNX thread is not wedged; no restart"); n != 1 {
		t.Errorf("not-wedged debug lines = %d, want exactly 1", n)
	}
}

// TestEmbedderSupervisor_NilProbeDegradesToTimeoutSemantics pins the AC
// "отсутствие Wedged-проба → деградация к таймаут-семантике": without the
// liveness probe the timeout returns the transient error and NOTHING else
// happens — no restart, no diagnosis.
func TestEmbedderSupervisor_NilProbeDegradesToTimeoutSemantics(t *testing.T) {
	h := newSupHarness(t, nil)
	h.setInstance(h.newBlockingEmbedder(testProviderRequested))
	h.sup.wedgedProbe = nil // the degradation shape: no probe wired

	err := h.timedCall("x")
	if !errors.Is(err, errEmbedCallTimeout) {
		t.Fatalf("err = %v, want errEmbedCallTimeout", err)
	}
	h.joinProbes()
	if got := h.providersBuilt(); len(got) != 0 {
		t.Errorf("nil probe must never restart, built %v", got)
	}
	if n := h.logs.count(slog.LevelDebug, "no embedder liveness probe wired; the embed timeout keeps plain timeout semantics (no restart)"); n != 1 {
		t.Errorf("nil-probe debug lines = %d, want exactly 1", n)
	}
}

// TestEmbedderSupervisor_RestartExactlyOncePerWedge pins the AC "рестарт по
// Wedged ровно один на клинч": two timed-out calls against the same wedged
// instance produce exactly ONE restart. The first trigger holds the
// in-flight guard while the (gated) build runs; a straggler trigger that
// lands after the swap consults the LIVE instance — the fresh idle fake —
// and its false verdict stops it. Both interleavings collapse to one build.
func TestEmbedderSupervisor_RestartExactlyOncePerWedge(t *testing.T) {
	h := newSupHarness(t, nil)
	wedged := h.setInstance(h.newBlockingEmbedder(testProviderRequested))
	wedged.setWedged(func(time.Duration) bool { return true }) // the ONNX thread is stuck

	fresh := &fakeSupEmbedder{provider: testProviderRequested} // idle: Wedged false
	gateEntered := make(chan struct{}, 4)
	gateRelease := make(chan struct{})
	h.setBuild(func(provider string) (liveEmbedder, error) {
		gateEntered <- struct{}{}
		<-gateRelease
		return fresh, nil
	})
	t.Cleanup(func() { closeOnce(gateRelease) })

	// Trigger 1: wedged verdict, restart starts and parks inside the gate.
	if err := h.timedCall("a"); !errors.Is(err, errEmbedCallTimeout) {
		t.Fatalf("first call err = %v", err)
	}
	<-gateEntered // restarting=true; the wedged instance is still live

	// Trigger 2 during the same wedge: collapse (or, if it lands after the
	// swap, the fresh instance's false verdict) — either way, no second build.
	if err := h.timedCall("b"); !errors.Is(err, errEmbedCallTimeout) {
		t.Fatalf("second call err = %v", err)
	}

	close(gateRelease)
	h.joinProbes()

	got := h.providersBuilt()
	if len(got) != 1 || got[0] != testProviderRequested {
		t.Fatalf("builds = %v, want exactly one restart with provider %q", got, testProviderRequested)
	}
	if cur := h.currentInstance(); cur != liveEmbedder(fresh) {
		t.Error("the fresh instance must be the live one after the restart")
	}
	if wedged.wasClosed() {
		t.Error("the wedged instance must NOT be closed — Close dispatches teardown through the wedged ONNX thread and would deadlock")
	}
	if got := h.swapsSeen(); len(got) != 1 || got[0] != testProviderRequested {
		t.Errorf("swaps = %v, want one %q", got, testProviderRequested)
	}
	if n := h.logs.count(slog.LevelInfo, "vector embedder restarted on a fresh instance"); n != 1 {
		t.Errorf("restart infos = %d, want exactly 1", n)
	}
	if n := h.logs.count(slog.LevelWarn, "batch embed call exceeded its deadline; returning a transient error"); n != 2 {
		t.Errorf("timeout warnings = %d, want exactly 2 (one per timed-out call)", n)
	}
}

// TestEmbedderSupervisor_CPUFallbackAfterNFailedRestarts pins the AC
// "CPU-фолбэк после N": two consecutive failed restarts on the same wedged
// instance make the third attempt use the CPU provider; that success swaps
// the CPU instance in and resets the failure counter.
func TestEmbedderSupervisor_CPUFallbackAfterNFailedRestarts(t *testing.T) {
	h := newSupHarness(t, nil)
	wedged := h.setInstance(h.newBlockingEmbedder(testProviderRequested))
	wedged.setWedged(func(time.Duration) bool { return true })

	cpu := &fakeSupEmbedder{provider: testProviderCPU}
	h.setBuild(func(provider string) (liveEmbedder, error) {
		if provider == testProviderRequested {
			return nil, errors.New("cuda runtime refuses to come up")
		}
		return cpu, nil
	})

	// Ladder rungs 1 and 2: requested provider, both fail.
	for i := 0; i < 2; i++ {
		if err := h.timedCall("x"); !errors.Is(err, errEmbedCallTimeout) {
			t.Fatalf("call %d err = %v", i+1, err)
		}
		h.joinProbes()
	}
	if got := h.providersBuilt(); len(got) != 2 || got[0] != testProviderRequested || got[1] != testProviderRequested {
		t.Fatalf("builds after two failures = %v, want [%s %s]", got, testProviderRequested, testProviderRequested)
	}
	if n := h.restartFailures(); n != 2 {
		t.Fatalf("failures = %d, want 2", n)
	}

	// Rung 3: the CPU fallback — success swaps the CPU instance in.
	if err := h.timedCall("x"); !errors.Is(err, errEmbedCallTimeout) {
		t.Fatalf("third call err = %v", err)
	}
	h.joinProbes()

	got := h.providersBuilt()
	if len(got) != 3 || got[2] != testProviderCPU {
		t.Fatalf("builds = %v, want third attempt on %q", got, testProviderCPU)
	}
	if n := h.restartFailures(); n != 0 {
		t.Errorf("failures after successful restart = %d, want 0 (counters reset)", n)
	}
	if cur := h.currentInstance(); cur != liveEmbedder(cpu) {
		t.Error("the CPU instance must be live after the fallback restart")
	}
	if got := h.swapsSeen(); len(got) != 1 || got[0] != testProviderCPU {
		t.Errorf("swaps = %v, want one %q", got, testProviderCPU)
	}
	if n := h.logs.count(slog.LevelWarn, "vector embedder restart falling back to the CPU execution provider"); n != 1 {
		t.Errorf("CPU-fallback warnings = %d, want exactly 1", n)
	}
	if n := h.logs.count(slog.LevelWarn, "vector embedder restart failed; the wedged instance stays live"); n != 2 {
		t.Errorf("restart-failure warnings = %d, want exactly 2", n)
	}
}

// TestEmbedderSupervisor_FailureCounterResetsOnSuccess pins the AC
// "счётчики сбрасываются": after a successful restart a NEW wedge on the
// fresh instance starts the ladder over at the requested provider instead of
// jumping straight to the CPU fallback.
func TestEmbedderSupervisor_FailureCounterResetsOnSuccess(t *testing.T) {
	h := newSupHarness(t, nil)
	initial := h.setInstance(h.newBlockingEmbedder(testProviderRequested))
	initial.setWedged(func(time.Duration) bool { return true })

	var builtMu sync.Mutex
	var built []*fakeSupEmbedder // index 0 = the wedge-1 restart, 1 = wedge-2
	h.setBuild(func(provider string) (liveEmbedder, error) {
		// Blocking fakes: a successor instance only becomes the live one
		// after its swap, and the NEXT wedge's timed call needs the entered
		// barrier against it. Idle by default (Wedged false), wedged
		// explicitly by the test.
		f := h.newBlockingEmbedder(provider)
		builtMu.Lock()
		built = append(built, f)
		builtMu.Unlock()
		return f, nil
	})

	// Wedge 1 on the initial instance: restart succeeds on the requested
	// provider.
	if err := h.timedCall("x"); !errors.Is(err, errEmbedCallTimeout) {
		t.Fatalf("first wedge call err = %v", err)
	}
	h.joinProbes()
	builtMu.Lock()
	restarted := built[0]
	builtMu.Unlock()
	if n := h.restartFailures(); n != 0 {
		t.Fatalf("failures after success = %d, want 0", n)
	}

	// Wedge 2 on the FRESH instance: the ladder starts over — requested
	// provider again, not the CPU fallback.
	restarted.setWedged(func(time.Duration) bool { return true })
	if err := h.timedCall("y"); !errors.Is(err, errEmbedCallTimeout) {
		t.Fatalf("second wedge call err = %v", err)
	}
	h.joinProbes()

	got := h.providersBuilt()
	if len(got) != 2 || got[0] != testProviderRequested || got[1] != testProviderRequested {
		t.Fatalf("builds = %v, want both attempts on %q (counter reset)", got, testProviderRequested)
	}
}

// TestEmbedderSupervisor_CallerCancelReturnsPromptly pins that caller
// cancellation keeps its pre-existing semantics: the bounded call returns
// the ctx error, the deadline never fires, and no restart machinery runs.
func TestEmbedderSupervisor_CallerCancelReturnsPromptly(t *testing.T) {
	h := newSupHarness(t, nil)
	h.setInstance(h.newBlockingEmbedder(testProviderRequested))
	ctx, cancel := context.WithCancel(context.Background())

	errCh := make(chan error, 1)
	go func() {
		_, err := h.sup.EmbedDocuments(ctx, []string{"x"})
		errCh <- err
	}()
	<-h.entered // parked inside the fake
	cancel()
	err := <-errCh

	if !errors.Is(err, context.Canceled) {
		t.Fatalf("err = %v, want context.Canceled", err)
	}
	if got := h.providersBuilt(); len(got) != 0 {
		t.Errorf("cancellation must not restart, built %v", got)
	}
	if n := h.logs.count(slog.LevelWarn, "batch embed call exceeded its deadline; returning a transient error"); n != 0 {
		t.Errorf("cancellation logged %d timeout warnings, want 0", n)
	}
}

// TestEmbedderSupervisor_QueryEmbedRacesCallerContext pins the query-side
// bound: a query embed parked inside the instance (sp4rk parks on the
// instance mutex and honors ctx only once the lock is held) returns at the
// caller's ctx bound instead of queuing forever, the fast path passes the
// instance's vector through untouched, and the query path never drives the
// batch restart machinery.
func TestEmbedderSupervisor_QueryEmbedRacesCallerContext(t *testing.T) {
	h := newSupHarness(t, nil)

	// Fast path: a query that completes delivers the instance's vector.
	h.setInstance(&fakeSupEmbedder{provider: testProviderRequested})
	vec, err := h.sup.queryEmbeddingFunc()(context.Background(), "q")
	if err != nil {
		t.Fatalf("fast-path query: %v", err)
	}
	if len(vec) != testEmbedDim {
		t.Fatalf("fast-path vector len = %d, want %d", len(vec), testEmbedDim)
	}

	// Parked path: the query embed parks inside the fake (entered), the
	// caller cancels, and the query func returns the ctx error promptly.
	// The abandoned goroutine is released by the harness's registered
	// cleanup, so nothing leaks past the test function.
	parked := &fakeSupEmbedder{provider: testProviderRequested}
	parked.queryFn = func(context.Context, string) ([]float32, error) {
		h.entered <- struct{}{}
		<-h.release
		h.left <- struct{}{}
		return nil, nil
	}
	h.setInstance(parked)

	ctx, cancel := context.WithCancel(context.Background())
	errCh := make(chan error, 1)
	go func() {
		_, err := h.sup.queryEmbeddingFunc()(ctx, "q")
		errCh <- err
	}()
	<-h.entered // parked inside the fake
	cancel()
	if err := <-errCh; !errors.Is(err, context.Canceled) {
		t.Fatalf("parked query err = %v, want context.Canceled", err)
	}

	// Bounded only: the query path must not run any restart machinery.
	if got := h.providersBuilt(); len(got) != 0 {
		t.Errorf("query cancellation built %v, want none", got)
	}
	if got := h.probeBoundsSeen(); len(got) != 0 {
		t.Errorf("query cancellation consulted the wedge probe %v, want none", got)
	}
	if n := h.logs.count(slog.LevelWarn, "batch embed call exceeded its deadline; returning a transient error"); n != 0 {
		t.Errorf("query cancellation logged %d timeout warnings, want 0", n)
	}
}

// TestEmbedderSupervisor_ShuttingDownSkipsRestart pins the shutdown guard: a
// wedged verdict during app teardown produces the transient error but no
// embedder build.
func TestEmbedderSupervisor_ShuttingDownSkipsRestart(t *testing.T) {
	h := newSupHarness(t, func() bool { return true })
	wedged := h.setInstance(h.newBlockingEmbedder(testProviderRequested))
	wedged.setWedged(func(time.Duration) bool { return true })

	if err := h.timedCall("x"); !errors.Is(err, errEmbedCallTimeout) {
		t.Fatalf("err = %v", err)
	}
	h.joinProbes()
	if got := h.providersBuilt(); len(got) != 0 {
		t.Errorf("shutting-down must skip the restart, built %v", got)
	}
	if n := h.logs.count(slog.LevelDebug, "embedder restart skipped: the app is shutting down"); n != 1 {
		t.Errorf("skip debug lines = %d, want exactly 1", n)
	}
}

// TestEmbedderSupervisor_CloseReleasesLiveInstanceAndStops pins the registry
// closeEmbedder seam on a HEALTHY live instance: close stops the supervisor
// (further restarts refused, live-instance reads degrade), closes the live
// instance exactly once, and later embed calls fail with errEmbedderStopped.
func TestEmbedderSupervisor_CloseReleasesLiveInstanceAndStops(t *testing.T) {
	h := newSupHarness(t, nil)
	fake := h.setInstance(&fakeSupEmbedder{provider: testProviderRequested})

	if err := h.sup.close(); err != nil {
		t.Fatalf("close: %v", err)
	}
	if !fake.wasClosed() {
		t.Error("close must release a healthy live instance")
	}
	if h.sup.wedged() {
		t.Error("a closed supervisor must never report wedged (no live instance)")
	}
	if _, err := h.sup.EmbedDocuments(context.Background(), []string{"x"}); !errors.Is(err, errEmbedderStopped) {
		t.Fatalf("post-close EmbedDocuments err = %v, want errEmbedderStopped", err)
	}
	if _, err := h.sup.queryEmbeddingFunc()(context.Background(), "x"); !errors.Is(err, errEmbedderStopped) {
		t.Fatalf("post-close query func err = %v, want errEmbedderStopped", err)
	}
	// A wedge verdict arriving after close must not build anything.
	h.sup.tryRestart()
	if got := h.providersBuilt(); len(got) != 0 {
		t.Errorf("post-close restart built %v, want nothing", got)
	}
}

// TestEmbedderSupervisor_CloseSkipsWedgedLiveInstance pins that a WEDGED live
// instance is never Closed: sp4rk's Close parks on the instance mutex the
// wedged inference holds forever, so closing it would freeze ShutdownAll and
// with it app quit — the same stranded-instance stance the restart ladder
// already takes. The supervisor is still stopped and the skip is logged.
func TestEmbedderSupervisor_CloseSkipsWedgedLiveInstance(t *testing.T) {
	h := newSupHarness(t, nil)
	wedged := h.setInstance(h.newBlockingEmbedder(testProviderRequested))
	wedged.setWedged(func(time.Duration) bool { return true })

	if err := h.sup.close(); err != nil {
		t.Fatalf("close: %v", err)
	}
	if wedged.wasClosed() {
		t.Error("a wedged live instance must NOT be closed — its Close would never return and would freeze ShutdownAll")
	}
	if n := h.logs.count(slog.LevelWarn, "vector embedder close skipped: the ONNX inference thread is wedged and its Close would never return; the thread dies with the process"); n != 1 {
		t.Errorf("wedged-skip warnings = %d, want exactly 1", n)
	}
	if _, err := h.sup.EmbedDocuments(context.Background(), []string{"x"}); !errors.Is(err, errEmbedderStopped) {
		t.Fatalf("post-close EmbedDocuments err = %v, want errEmbedderStopped (the supervisor is stopped regardless of the skipped Close)", err)
	}
}

// TestEmbedderSupervisor_CloseBoundedWhenCloseNeverReturns pins the race that
// backstops the wedged-skip: a NOT-wedged instance whose Close still never
// returns (a wedge younger than the verdict bound, or teardown stuck for any
// other reason) must not freeze the quit either — close returns at its
// deadline with a Warn, and the abandoned Close goroutine is released by
// cleanup exactly like the ladder's stranded work.
func TestEmbedderSupervisor_CloseBoundedWhenCloseNeverReturns(t *testing.T) {
	h := newSupHarness(t, nil)
	stuck := &fakeSupEmbedder{provider: testProviderRequested} // not wedged: passes the verdict gate
	closeEntered := make(chan struct{})
	closeRelease := make(chan struct{})
	t.Cleanup(func() { closeOnce(closeRelease) })
	stuck.closeFn = func() error {
		closeEntered <- struct{}{}
		<-closeRelease
		return nil
	}
	h.setInstance(stuck)

	errCh := make(chan error, 1)
	go func() {
		errCh <- h.sup.close()
	}()
	<-closeEntered        // the Close call is parked inside the fake
	h.fireThisCallTimer() // close is at its select; the deadline expired
	if err := <-errCh; err != nil {
		t.Fatalf("close after its deadline = %v, want nil (bounded, best-effort seam)", err)
	}
	if n := h.logs.count(slog.LevelWarn, "vector embedder close timed out; a wedged or long-running inference still holds the instance — the embedder dies with the process"); n != 1 {
		t.Errorf("close-timeout warnings = %d, want exactly 1", n)
	}
	if _, err := h.sup.EmbedDocuments(context.Background(), []string{"x"}); !errors.Is(err, errEmbedderStopped) {
		t.Fatalf("post-close EmbedDocuments err = %v, want errEmbedderStopped", err)
	}
}

// TestEmbedderSupervisor_WedgedAppliesDeadlineBound pins the drain-diagnosis
// surface's bound: wedged() consults the LIVE instance with the embed call
// deadline — the healthy-inference envelope — so the shutdown drain, whose
// grace is typically far shorter, can never fabricate a wedge verdict for a
// normal slow batch through this seam.
func TestEmbedderSupervisor_WedgedAppliesDeadlineBound(t *testing.T) {
	h := newSupHarness(t, nil)
	var gotBound time.Duration
	fake := h.setInstance(&fakeSupEmbedder{provider: testProviderRequested})
	fake.setWedged(func(bound time.Duration) bool {
		gotBound = bound
		return true
	})

	if !h.sup.wedged() {
		t.Fatal("wedged() must report the live instance's verdict")
	}
	if gotBound != h.sup.deadline {
		t.Errorf("wedged() probed the instance with bound %v, want the embed call deadline %v (the healthy-inference envelope, never a drain grace)", gotBound, h.sup.deadline)
	}
}

// TestEmbedderSupervisor_CloseRacingRestartReleasesFreshInstance pins the
// close/restart race: a restart whose build lands after close must NOT swap
// its fresh instance into the dead supervisor — the fresh instance is
// released instead, or ShutdownAll would leak it past the registry.
func TestEmbedderSupervisor_CloseRacingRestartReleasesFreshInstance(t *testing.T) {
	h := newSupHarness(t, nil)
	wedged := h.setInstance(h.newBlockingEmbedder(testProviderRequested))
	wedged.setWedged(func(time.Duration) bool { return true })

	fresh := &fakeSupEmbedder{provider: testProviderRequested}
	gateEntered := make(chan struct{}, 4)
	gateRelease := make(chan struct{})
	h.setBuild(func(provider string) (liveEmbedder, error) {
		gateEntered <- struct{}{}
		<-gateRelease
		return fresh, nil
	})
	t.Cleanup(func() { closeOnce(gateRelease) })

	done := make(chan struct{})
	go func() {
		h.sup.tryRestart()
		close(done)
	}()
	<-gateEntered // parked inside the build, restarting=true

	if err := h.sup.close(); err != nil {
		t.Fatalf("close: %v", err)
	}
	close(gateRelease)
	<-done // join the restart goroutine

	if !fresh.wasClosed() {
		t.Error("a fresh instance landing after close must be released, not swapped in")
	}
	if cur := h.currentInstance(); cur != nil {
		t.Errorf("live instance after close = %v, want nil", cur)
	}
	if got := h.swapsSeen(); len(got) != 0 {
		t.Errorf("swaps = %v, want none (the supervisor is closed)", got)
	}
}
