package backend

// Router-closure semantics (ADR-080): the shared VectorSearchFunc /
// VectorSearchWaitFunc pair built by Application.buildVectorRouter preserves
// the singleton-era dispatch contract — one bounded deadline covering the
// embedder wait and the per-root Service.WaitReady, fail-fast dispatch on the
// search closure (NoWait), and the explicit search_wait_timeout_ms: 0
// sentinel — while resolving the manager per workspace root from the executor
// context (or the Git-panel focus when the context carries none).

import (
	"context"
	"strings"
	"testing"
	"time"

	"github.com/v0lka/c0wrk/core/vectorindex"
	"github.com/v0lka/sp4rk/tools/builtins"
)

// newRouterStuckManager returns a Manager whose service is never marked ready
// (no SetProject/SetReady call), simulating a full index that never finishes.
func newRouterStuckManager(t *testing.T) *vectorindex.Manager {
	t.Helper()
	mgr, err := vectorindex.NewManager(vectorindex.ManagerConfig{
		EmbeddingFunc: func(ctx context.Context, text string) ([]float32, error) {
			return make([]float32, 4), nil
		},
	})
	if err != nil {
		t.Fatalf("NewManager: %v", err)
	}
	t.Cleanup(func() { mgr.Shutdown() })
	return mgr
}

// newRouterHarness wires an Application whose router resolves a fresh
// per-root registry. factory/ready reproduce the background ONNX init states
// (nil factory + open ready = still loading; settled ready + nil factory =
// embedder known unavailable); focusMgr, when non-nil, is injected as the
// focused root's live manager so routing never needs the factory. A focus
// root is ALWAYS set (production: a CODE project is active) — without it the
// router's no-target error would pre-empt the readiness surfaces under test.
func newRouterHarness(t *testing.T, factory func() (*vectorindex.Manager, error), ready chan struct{}, focusMgr *vectorindex.Manager) (*Application, *VectorRoots) {
	t.Helper()
	f := &FrontendAPI{appCtx: context.Background}
	vr := newVectorRoots(f)
	root := canonicalRoot(t.TempDir())
	vr.mu.Lock()
	vr.focus = root
	if focusMgr != nil {
		vr.roots[root] = &vectorRootEntry{mgr: focusMgr, projectID: "p-test", lastUsed: time.Now()}
	}
	vr.mu.Unlock()
	if factory != nil || ready != nil {
		vr.SetFactory(factory, ready, nil)
	}
	app := &Application{}
	app.SetVectorRoots(vr)
	t.Cleanup(func() { vr.ShutdownAll() })
	return app, vr
}

func TestBuildVectorRouter_CtxCanceledBeforeReady(t *testing.T) {
	// No factory wired and the init has not settled: the still-loading
	// surface, bounded by the caller's ctx.
	app, _ := newRouterHarness(t, nil, nil, nil)
	searchFunc, waitFunc := app.buildVectorRouter(vectorindex.DefaultSearchWaitTimeout)

	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Millisecond)
	defer cancel()

	_, err := searchFunc(ctx, builtins.VectorSearchOptions{Query: "x"})
	if err == nil {
		t.Fatal("expected error when ctx expires before vector ready")
	}
	if !strings.Contains(err.Error(), "retry") {
		t.Errorf("expected actionable not-ready error suggesting retry, got %v", err)
	}

	if err := waitFunc(ctx); err == nil {
		t.Fatal("expected wait error when ctx expires before vector ready")
	}
}

func TestBuildVectorRouter_ReadyButUnavailable(t *testing.T) {
	// The init settled without a factory: the embedder never came up, so
	// both closures report the unavailable surface instead of waiting.
	app, _ := newRouterHarness(t, nil, closedChannel(), nil)
	searchFunc, waitFunc := app.buildVectorRouter(vectorindex.DefaultSearchWaitTimeout)

	_, err := searchFunc(context.Background(), builtins.VectorSearchOptions{Query: "x"})
	if err == nil {
		t.Fatal("expected 'unavailable' error when the embedder init failed")
	}
	if !strings.Contains(err.Error(), "unavailable") {
		t.Errorf("expected the unavailable surface, got %v", err)
	}

	if err := waitFunc(context.Background()); err == nil {
		t.Fatal("expected 'unavailable' error from waitFunc when the embedder init failed")
	}
}

// TestBuildVectorRouter_WaitFuncBoundedWhenIndexStuck pins the core
// guarantee of vector_index.search_wait_timeout_ms: while a full index is
// stuck (never ready), waitFunc returns within the bound with an actionable
// error carrying the retry suggestion — never blocking indefinitely.
func TestBuildVectorRouter_WaitFuncBoundedWhenIndexStuck(t *testing.T) {
	ready := make(chan struct{})
	close(ready) // embedder loaded; per-root index never becomes ready
	app, _ := newRouterHarness(t, func() (*vectorindex.Manager, error) {
		return nil, context.Canceled
	}, ready, newRouterStuckManager(t))

	const timeout = 80 * time.Millisecond
	_, waitFunc := app.buildVectorRouter(timeout)

	start := time.Now()
	err := waitFunc(context.Background())
	elapsed := time.Since(start)

	if err == nil {
		t.Fatal("expected error when index never becomes ready")
	}
	if elapsed > timeout+500*time.Millisecond {
		t.Errorf("waitFunc blocked for %v; want bounded by %v", elapsed, timeout)
	}
	if !strings.Contains(err.Error(), "index not yet ready") || !strings.Contains(err.Error(), "retry") {
		t.Errorf("expected actionable not-ready error with retry suggestion, got %v", err)
	}
}

// TestBuildVectorRouter_WaitFuncBoundedWhenEmbedderLoading covers the first
// bounded stage: the wait for the embedder (init not settled, factory nil)
// must respect the same timeout even though a manager will never appear.
func TestBuildVectorRouter_WaitFuncBoundedWhenEmbedderLoading(t *testing.T) {
	ready := make(chan struct{}) // never closed: embedder still loading
	app, _ := newRouterHarness(t, nil, ready, nil)

	const timeout = 80 * time.Millisecond
	_, waitFunc := app.buildVectorRouter(timeout)

	start := time.Now()
	err := waitFunc(context.Background())
	elapsed := time.Since(start)

	if err == nil {
		t.Fatal("expected error when embedder never loads")
	}
	if elapsed > timeout+500*time.Millisecond {
		t.Errorf("waitFunc blocked for %v; want bounded by %v", elapsed, timeout)
	}
	if !strings.Contains(err.Error(), "retry") {
		t.Errorf("expected error suggesting retry, got %v", err)
	}
}

// TestBuildVectorRouter_SearchFuncBoundedWhenIndexStuck covers the
// never-ready index on the search closure: searchFunc dispatches to
// HybridSearchNoWait, so it fails fast with the actionable not-ready error
// instead of blocking on WaitReady until the index becomes ready (or
// forever). The bound holds even when an incremental pass starts between
// the waitFunc gate and this call.
func TestBuildVectorRouter_SearchFuncBoundedWhenIndexStuck(t *testing.T) {
	ready := make(chan struct{})
	close(ready)
	app, _ := newRouterHarness(t, func() (*vectorindex.Manager, error) {
		return nil, context.Canceled
	}, ready, newRouterStuckManager(t))

	const timeout = 80 * time.Millisecond
	searchFunc, _ := app.buildVectorRouter(timeout)

	start := time.Now()
	_, err := searchFunc(context.Background(), builtins.VectorSearchOptions{Query: "x"})
	elapsed := time.Since(start)

	if err == nil {
		t.Fatal("expected error when index never becomes ready")
	}
	// NoWait must return essentially immediately — not merely within the
	// timeout: it never enters a readiness wait at all.
	if elapsed > 100*time.Millisecond {
		t.Errorf("searchFunc took %v on a stuck index; want fail-fast (NoWait), bound %v", elapsed, timeout)
	}
	if !strings.Contains(err.Error(), "index not yet ready") || !strings.Contains(err.Error(), "retry") {
		t.Errorf("expected actionable not-ready error with retry suggestion, got %v", err)
	}
}

// TestBuildVectorRouter_SearchFuncFailsFastWhenReadinessFlipsAfterGate pins
// the TOCTOU fix: readiness may flip (an incremental pass calls MarkNotReady)
// between a successful waitFunc gate and the search call; searchFunc must
// fail fast with the actionable not-ready error instead of blocking.
func TestBuildVectorRouter_SearchFuncFailsFastWhenReadinessFlipsAfterGate(t *testing.T) {
	ready := make(chan struct{})
	close(ready)
	mgr := newRouterStuckManager(t)
	mgr.Service().SetReady(true)
	app, _ := newRouterHarness(t, func() (*vectorindex.Manager, error) {
		return nil, context.Canceled
	}, ready, mgr)

	searchFunc, waitFunc := app.buildVectorRouter(vectorindex.DefaultSearchWaitTimeout)

	if err := waitFunc(context.Background()); err != nil {
		t.Fatalf("waitFunc with ready index: %v", err)
	}

	// An incremental pass starts right after the gate.
	mgr.Service().MarkNotReady()

	start := time.Now()
	_, err := searchFunc(context.Background(), builtins.VectorSearchOptions{Query: "x"})
	if err == nil || !strings.Contains(err.Error(), "index not yet ready") || !strings.Contains(err.Error(), "retry") {
		t.Fatalf("expected immediate not-ready error after readiness flip, got %v", err)
	}
	if d := time.Since(start); d > 100*time.Millisecond {
		t.Errorf("searchFunc blocked %v after readiness flip; want fail-fast (NoWait)", d)
	}
}

// TestBuildVectorRouter_FailFastZeroTimeout pins the explicit
// search_wait_timeout_ms: 0 sentinel: readiness is checked without waiting —
// both when the embedder is loading and when the index is not ready — with
// zero waiting (no timeout-sized delay before the error).
func TestBuildVectorRouter_FailFastZeroTimeout(t *testing.T) {
	// Embedder still loading: immediate still-loading error.
	app, _ := newRouterHarness(t, nil, nil, nil)
	_, waitFunc := app.buildVectorRouter(0)

	start := time.Now()
	if err := waitFunc(context.Background()); err == nil || !strings.Contains(err.Error(), "retry") {
		t.Fatalf("expected immediate not-ready error, got %v", err)
	}
	if d := time.Since(start); d > 50*time.Millisecond {
		t.Errorf("fail-fast waitFunc took %v while embedder loading; want ~0", d)
	}

	// Embedder init settled but the focused index is never ready: immediate
	// actionable error from both closures.
	ready := make(chan struct{})
	close(ready)
	app2, _ := newRouterHarness(t, func() (*vectorindex.Manager, error) {
		return nil, context.Canceled
	}, ready, newRouterStuckManager(t))
	searchFunc2, waitFunc2 := app2.buildVectorRouter(0)

	start = time.Now()
	if err := waitFunc2(context.Background()); err == nil || !strings.Contains(err.Error(), "index not yet ready") {
		t.Fatalf("expected immediate not-ready error, got %v", err)
	}
	if d := time.Since(start); d > 50*time.Millisecond {
		t.Errorf("fail-fast waitFunc took %v with index not ready; want ~0", d)
	}

	start = time.Now()
	_, err := searchFunc2(context.Background(), builtins.VectorSearchOptions{Query: "x"})
	if err == nil || !strings.Contains(err.Error(), "index not yet ready") {
		t.Fatalf("expected immediate not-ready error from searchFunc, got %v", err)
	}
	if d := time.Since(start); d > 50*time.Millisecond {
		t.Errorf("fail-fast searchFunc took %v with index not ready; want ~0", d)
	}
}

// TestBuildVectorRouter_ReadyIndexProceedsUnchanged pins the ready path: with
// the index ready, waitFunc returns nil and searchFunc delegates to
// HybridSearch unchanged — the bound must not fail a ready index.
func TestBuildVectorRouter_ReadyIndexProceedsUnchanged(t *testing.T) {
	ready := make(chan struct{})
	close(ready)
	mgr := newRouterStuckManager(t)
	mgr.Service().SetReady(true)
	app, _ := newRouterHarness(t, func() (*vectorindex.Manager, error) {
		return nil, context.Canceled
	}, ready, mgr)

	searchFunc, waitFunc := app.buildVectorRouter(vectorindex.DefaultSearchWaitTimeout)

	start := time.Now()
	if err := waitFunc(context.Background()); err != nil {
		t.Fatalf("waitFunc with ready index: %v", err)
	}
	if d := time.Since(start); d > time.Second {
		t.Errorf("ready waitFunc took %v; want immediate", d)
	}

	// No collection is configured on this service, so HybridSearch returns
	// its ordinary no-collection error quickly — NOT a not-ready/timeout
	// error. That is exactly the long-standing behaviour for a ready index.
	_, err := searchFunc(context.Background(), builtins.VectorSearchOptions{Query: "x"})
	if err == nil {
		t.Fatal("expected no-collection error from HybridSearch (no SetProject called)")
	}
	if strings.Contains(err.Error(), "retry") || strings.Contains(err.Error(), "not ready") {
		t.Errorf("ready index must not produce a not-ready error, got %v", err)
	}
}

func closedChannel() chan struct{} {
	ch := make(chan struct{})
	close(ch)
	return ch
}
