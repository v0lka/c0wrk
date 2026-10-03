package vectorindex

import (
	"context"
	"errors"
	"log/slog"
	"os"
	"path/filepath"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/v0lka/c0wrk/core"
	"github.com/v0lka/sp4rk/embedding"
)

// TestManagerNotifyFileChangeSerializesOverlappingPasses keeps real filesystem
// and index storage coverage, but controls debounce dispatch rather than waiting
// for a quiet window. Both passes must embed and finish; the trailing callback
// must re-arm while A is in flight, without invoking the embedder concurrently.
func TestManagerNotifyFileChangeSerializesOverlappingPasses(t *testing.T) {
	persistDir, ws := t.TempDir(), t.TempDir()
	var gated atomic.Bool
	var inFlight, peak, passEmbeds atomic.Int32
	entered := make(chan struct{}, 1)
	release := make(chan struct{})
	var releaseOnce sync.Once
	unblock := func() { releaseOnce.Do(func() { close(release) }) }
	embed := func(ctx context.Context, _ string) ([]float32, error) {
		cur := inFlight.Add(1)
		defer inFlight.Add(-1)
		for old := peak.Load(); cur > old; old = peak.Load() {
			if peak.CompareAndSwap(old, cur) {
				break
			}
		}
		if gated.Load() {
			passEmbeds.Add(1)
			entered <- struct{}{}
			select {
			case <-release:
			case <-ctx.Done():
				return nil, ctx.Err()
			}
		}
		return []float32{0.01, 0.02}, nil
	}
	mgr, err := NewManager(ManagerConfig{EmbeddingFunc: embed, Debounce: 150 * time.Millisecond})
	if err != nil {
		t.Fatalf("NewManager: %v", err)
	}
	// One notification/re-arm is consumed at a time, so capacity one cannot
	// fill during dispatch. The callback itself is joined by the test.
	armed := make(chan func(), 1)
	mgr.afterFunc = func(d time.Duration, f func()) *time.Timer {
		if d != 150*time.Millisecond {
			t.Errorf("debounce = %v, want 150ms", d)
		}
		armed <- f
		return nil
	}
	var workers []<-chan struct{}
	t.Cleanup(func() {
		unblock()
		for _, done := range workers {
			awaitVectorSignal(t, done, "incremental callback cleanup")
		}
		mgr.Shutdown()
	})
	write := func(name string) {
		t.Helper()
		if err := os.WriteFile(filepath.Join(ws, name), []byte("package main\nfunc "+name[:len(name)-3]+"() {}\n"), 0o644); err != nil {
			t.Fatalf("write %s: %v", name, err)
		}
	}
	write("main.go")
	if err := mgr.SwitchProject("project-x", ws, filepath.Join(persistDir, "project-x"), ProjectCallbacks{}); err != nil {
		t.Fatalf("SwitchProject: %v", err)
	}
	waitForBackgroundPasses(t, mgr, mgr.Service())
	gated.Store(true)
	write("added.go")
	mgr.NotifyFileChange()
	launch := func(f func()) <-chan struct{} {
		done := make(chan struct{})
		workers = append(workers, done)
		go func() { defer close(done); f() }()
		return done
	}
	doneA := launch(awaitVectorCallback(t, armed))
	awaitVectorSignal(t, entered, "pass A embed entry")

	// A's file snapshot has already reached embedding. This file must be
	// picked up by B, not silently folded into A before the overlap begins.
	write("trailing.go")
	mgr.NotifyFileChange()
	guardDone := launch(awaitVectorCallback(t, armed))
	trailing := awaitVectorCallback(t, armed)
	awaitVectorSignal(t, guardDone, "overlap re-arm completion")
	if got := passEmbeds.Load(); got != 1 {
		t.Fatalf("embeds while A blocked = %d, want 1", got)
	}
	unblock()
	awaitVectorSignal(t, doneA, "pass A completion")
	doneB := launch(trailing)
	awaitVectorSignal(t, entered, "pass B embed entry")
	awaitVectorSignal(t, doneB, "pass B completion")
	if got := passEmbeds.Load(); got != 2 {
		t.Errorf("incremental embed calls = %d, want 2", got)
	}
	if got := peak.Load(); got != 1 {
		t.Errorf("peak embed concurrency = %d, want 1", got)
	}
	if mgr.indexing.Load() {
		t.Error("indexing remains true after both callbacks completed")
	}
	files, err := mgr.Service().GetCollectionFiles()
	if err != nil {
		t.Fatalf("GetCollectionFiles: %v", err)
	}
	for _, name := range []string{"main.go", "added.go", "trailing.go"} {
		if _, ok := files[filepath.Join(ws, name)]; !ok {
			t.Errorf("completed passes did not index %s", name)
		}
	}
}

// awaitVectorSignal bounds hangs only; channels establish all ordering.
func awaitVectorSignal(t *testing.T, ch <-chan struct{}, what string) {
	t.Helper()
	select {
	case <-ch:
	case <-time.After(10 * time.Second):
		t.Fatalf("timed out waiting for %s", what)
	}
}

func awaitVectorCallback(t *testing.T, ch <-chan func()) func() {
	t.Helper()
	select {
	case f := <-ch:
		return f
	case <-time.After(10 * time.Second):
		t.Fatal("debounce callback was not armed")
		return nil
	}
}

// TestManagerSwitchProject_RapidDoubleSwitchCancelsInFlight exercises the
// single-flight contract (initCancel + initWG + indexCancel) when a second
// SwitchProject fires while the first project's background indexing is still
// in flight and blocked on the embedder — which holds the service write lock
// mid-AddDocuments.
//
// The second switch must: cancel the first's index context (unblocking the
// embedder via chromem's ctx propagation and so releasing the write lock),
// wait out the first's init goroutine, then initialize cleanly so the second
// project's collection wins — no deadlock, no leaked indexing goroutine, and
// readiness restored.
//
// Determinism: the embedder is ctx-aware, so SwitchProject B's indexCancel
// unblocks A's blocked embedder (it returns ctx.Err()) rather than relying on
// wall-clock timing. We block on embedCalls > 0 to guarantee A is genuinely
// in flight before issuing the double-switch.
func TestManagerSwitchProject_RapidDoubleSwitchCancelsInFlight(t *testing.T) {
	persistDir := t.TempDir()

	var embedCalls atomic.Int32
	block := make(chan struct{})
	var gateOnce sync.Once
	release := func() { gateOnce.Do(func() { close(block) }) }

	// ctx-aware embedder: blocks on `block` until released OR the index
	// context is cancelled. The ctx path is what lets SwitchProject's
	// indexCancel deterministically unblock an in-flight pass.
	embed := func(ctx context.Context, _ string) ([]float32, error) {
		embedCalls.Add(1)
		select {
		case <-block:
		case <-ctx.Done():
			return nil, ctx.Err()
		}
		return []float32{0.01, 0.02}, nil
	}

	svc, err := NewService(ServiceConfig{EmbeddingFunc: embed})
	if err != nil {
		t.Fatalf("NewService: %v", err)
	}
	t.Cleanup(func() { _ = svc.Close() })

	mgr := &Manager{
		service: svc,
		logger:  slog.New(slog.DiscardHandler),
		chunkFn: defaultChunkFn,
		hashFn:  embedding.ComputeFileHash,
	}
	t.Cleanup(func() {
		release() // unblock any goroutine still waiting on the gate
		mgr.Shutdown()
	})

	// Two workspaces with distinct files so the winning project is unambiguous.
	wsA := t.TempDir()
	if err := os.WriteFile(filepath.Join(wsA, "a.go"), []byte("package a\n"), 0o644); err != nil {
		t.Fatalf("write a.go: %v", err)
	}
	wsB := t.TempDir()
	if err := os.WriteFile(filepath.Join(wsB, "b.go"), []byte("package b\n"), 0o644); err != nil {
		t.Fatalf("write b.go: %v", err)
	}
	viPathA := filepath.Join(persistDir, "project-a")
	viPathB := filepath.Join(persistDir, "project-b")

	// 1) SwitchProject A: its indexing goroutine reaches the embedder and
	//    blocks (gate closed), holding the service write lock mid-AddDocuments.
	if err := mgr.SwitchProject("project-a", wsA, viPathA, ProjectCallbacks{}); err != nil {
		t.Fatalf("SwitchProject A: %v", err)
	}
	deadline := time.Now().Add(5 * time.Second)
	for time.Now().Before(deadline) {
		if embedCalls.Load() > 0 {
			break
		}
		time.Sleep(10 * time.Millisecond)
	}
	if embedCalls.Load() == 0 {
		t.Fatal("project A indexing never reached the embedder before the double-switch")
	}

	// 2) Rapid double-switch to B while A's indexing is in flight. This must
	//    cancel A's index context (unblocking the embedder + releasing the
	//    write lock), wait out A's init goroutine, and not deadlock.
	if err := mgr.SwitchProject("project-b", wsB, viPathB, ProjectCallbacks{}); err != nil {
		t.Fatalf("SwitchProject B: %v", err)
	}

	// 3) Open the gate so B's indexing can proceed (A's embedder already
	//    unblocked via ctx cancellation in step 2).
	release()

	// 4) B must win: readiness restored and B's collection populated with
	//    b.go (not A's a.go). WaitReady succeeding also proves no deadlock —
	//    it requires B's SetProject to have acquired the write lock A held.
	readyCtx, readyCancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer readyCancel()
	if err := svc.WaitReady(readyCtx); err != nil {
		t.Fatalf("WaitReady B after rapid double-switch: %v", err)
	}
	col := svc.GetCollection()
	if col == nil || col.Count() == 0 {
		t.Fatal("expected project B collection to have documents after double-switch")
	}
	files, err := svc.GetCollectionFiles()
	if err != nil {
		t.Fatalf("GetCollectionFiles: %v", err)
	}
	for fp := range files {
		if filepath.Base(fp) != "b.go" {
			t.Errorf("project B collection contains unexpected file from A: %s", fp)
		}
	}

	// 5) No leaked indexing pass: the indexing flag settles to false (A's
	//    goroutine exited via ctx cancellation; B's completed normally).
	settleDeadline := time.Now().Add(5 * time.Second)
	for time.Now().Before(settleDeadline) {
		if !mgr.indexing.Load() {
			break
		}
		time.Sleep(25 * time.Millisecond)
	}
	if mgr.indexing.Load() {
		t.Error("expected indexing flag to settle to false after double-switch")
	}
}

// TestIndexIncrementalRestoresReadyOnCancellation verifies the core fix for
// "search blocked forever after indexing is cancelled": IndexIncremental
// (and IndexFull) must restore SetReady(true) on EVERY exit path, including
// ctx cancellation and batch-add errors. Before the fix, these paths
// returned without flipping readiness back, leaving WaitReady callers (all
// searches) blocked on the readyCh channel forever.
func TestIndexIncrementalRestoresReadyOnCancellation(t *testing.T) {
	svc, err := NewService(ServiceConfig{EmbeddingFunc: fakeEmbeddingFunc()})
	if err != nil {
		t.Fatalf("NewService: %v", err)
	}
	// Register the temp dir BEFORE the svc.Close cleanup: t.Cleanup is LIFO,
	// so svc.Close (registered later) runs first and releases the lexical
	// store (.zap/.bolt) handles before TempDir's RemoveAll. Windows refuses
	// to delete files that still have open handles (EBUSY); on Unix the same
	// ordering avoids the service holding stale, deleted inodes.
	projectDir := t.TempDir()
	t.Cleanup(func() { _ = svc.Close() })

	if err := svc.SetProject("ready-test", projectDir); err != nil {
		t.Fatalf("SetProject: %v", err)
	}
	if err := svc.SwitchBranch(context.Background(), "main"); err != nil {
		t.Fatalf("SwitchBranch: %v", err)
	}

	idx := NewIndexer(IndexerConfig{
		Service: svc,
		ChunkFn: fakeChunkFunc,
		HashFn:  fakeHashFunc,
	})

	ws := t.TempDir()
	if err := os.WriteFile(filepath.Join(ws, "a.go"), []byte("package main\n"), 0o644); err != nil {
		t.Fatalf("write a.go: %v", err)
	}

	// Populate the collection so the incremental pass has a baseline.
	if err := idx.IndexFull(context.Background(), ws); err != nil {
		t.Fatalf("IndexFull: %v", err)
	}
	if !svc.IsReady() {
		t.Fatal("expected ready after IndexFull")
	}

	// Add a new file so the incremental pass has real work (otherwise it
	// short-circuits on "no changes" before reaching any cancellation path).
	if err := os.WriteFile(filepath.Join(ws, "b.go"), []byte("package main\nfunc b() {}\n"), 0o644); err != nil {
		t.Fatalf("write b.go: %v", err)
	}

	// Pre-cancel the context. IndexIncremental should return a
	// context-related error AND restore readiness via the defer.
	ctx, cancel := context.WithCancel(context.Background())
	cancel()

	_ = idx.IndexIncremental(ctx, ws) //nolint:errcheck // error is expected

	if !svc.IsReady() {
		t.Fatal("expected service to be ready after cancelled IndexIncremental; " +
			"without the defer SetReady(true) fix, all subsequent searches would block forever on WaitReady")
	}
}

// TestShutdownReturnsWhenIndexingGoroutineIsStuck verifies the bounded-wait
// Shutdown fix: when a background indexing goroutine is stuck in
// non-interruptible work (simulating a hung ONNX inference that ignores
// ctx.Done()), Shutdown must return within the grace period instead of
// hanging forever on service.Close()'s write-lock acquisition.
//
// Before the fix, the initProject-launched indexing goroutine was untracked
// (initWG only tracks initProject itself, not the goroutine it launches) and
// held the service write lock during the entire indexing pass. Shutdown's
// service.Close() needs that lock → permanent deadlock when the goroutine
// can't exit.
func TestShutdownReturnsWhenIndexingGoroutineIsStuck(t *testing.T) {
	persistDir := t.TempDir()

	// releaseEmbed unblocks the stuck goroutine after the test so it doesn't
	// leak for the remainder of the test binary. The cleanup that closes it is
	// registered after `mgr` is built (below) so it can also wait for the
	// goroutine to exit and close the service — see the comment there.
	releaseEmbed := make(chan struct{})

	var embedCalls atomic.Int32

	// Non-ctx-aware embedder: blocks on releaseEmbed, simulating a hung ONNX
	// inference. The key property is that it does NOT select on ctx.Done(),
	// so Shutdown's indexCancel cannot unblock it.
	embed := func(_ context.Context, _ string) ([]float32, error) {
		embedCalls.Add(1)
		<-releaseEmbed
		return nil, errors.New("embedder released after shutdown skip")
	}

	svc, err := NewService(ServiceConfig{EmbeddingFunc: embed})
	if err != nil {
		t.Fatalf("NewService: %v", err)
	}

	mgr := &Manager{
		service:       svc,
		logger:        slog.New(slog.DiscardHandler),
		chunkFn:       defaultChunkFn,
		hashFn:        embedding.ComputeFileHash,
		shutdownGrace: 250 * time.Millisecond, // short for the test; prod default is 10 s
	}
	// Shutdown intentionally skips service.Close() when the indexing goroutine
	// is stuck (it can't acquire the write lock the goroutine holds), leaving
	// the on-disk lexical store (.zap/.bolt) and chromem DB handles open. On
	// Windows those open handles make TempDir's RemoveAll fail with EBUSY, so
	// we close them here: unblock the goroutine, wait for it to exit (which
	// releases the service write lock via the indexer's deferred
	// ReleaseWriteLock), then close the service. Registered after persistDir's
	// TempDir so LIFO runs this before the directory is removed.
	t.Cleanup(func() {
		close(releaseEmbed)
		mgr.indexingWG.Wait()
		_ = svc.Close()
	})

	ws := t.TempDir()
	if err := os.WriteFile(filepath.Join(ws, "a.go"), []byte("package main\n"), 0o644); err != nil {
		t.Fatalf("write a.go: %v", err)
	}
	viPath := filepath.Join(persistDir, "stuck-project")

	// SwitchProject launches initProject, which launches the background
	// indexing goroutine, which reaches the embedder and gets stuck.
	if err := mgr.SwitchProject("stuck-project", ws, viPath, ProjectCallbacks{}); err != nil {
		t.Fatalf("SwitchProject: %v", err)
	}

	// Wait for the indexing goroutine to reach the embedder (and thus hold
	// the service write lock), so Shutdown faces a genuinely stuck goroutine.
	deadline := time.Now().Add(5 * time.Second)
	for time.Now().Before(deadline) {
		if embedCalls.Load() > 0 {
			break
		}
		time.Sleep(10 * time.Millisecond)
	}
	if embedCalls.Load() == 0 {
		t.Fatal("indexing goroutine never reached the embedder before Shutdown")
	}

	// Shutdown must return within a bounded time despite the stuck goroutine.
	start := time.Now()
	mgr.Shutdown()
	elapsed := time.Since(start)

	// The grace period is 250 ms; allow a generous margin for scheduling
	// overhead and the non-stuck init goroutine wind-down.
	if elapsed > 5*time.Second {
		t.Fatalf("Shutdown took %v; expected it to return within the grace period when the indexing goroutine is stuck", elapsed)
	}
	t.Logf("Shutdown returned in %v with stuck indexing goroutine (grace=%v)", elapsed, mgr.shutdownGrace)
}

// TestShutdownReturnsWhenInitGoroutineIsStuck is the regression test for the
// previously unbounded m.initWG.Wait() in Shutdown. initProject's opening step
// (SetProject's gob-decode of every branch document) is synchronous and not
// ctx-interruptible; if it stalled on a large or corrupt DB, the old code
// waited forever — hanging app shutdown even though indexing had its own
// bounded grace. The fix bounds the init wait with the same grace period, so a
// stuck init goroutine cannot hold shutdown.
func TestShutdownReturnsWhenInitGoroutineIsStuck(t *testing.T) {
	svc, err := NewService(ServiceConfig{EmbeddingFunc: fakeEmbeddingFunc()})
	if err != nil {
		t.Fatalf("NewService: %v", err)
	}
	mgr := &Manager{
		service:       svc,
		logger:        slog.New(slog.DiscardHandler),
		shutdownGrace: 100 * time.Millisecond,
	}
	// Simulate a stuck init goroutine: it never calls Done(), so initWG never
	// drains — mirroring initProject wedged inside non-interruptible SetProject.
	mgr.initWG.Add(1)
	t.Cleanup(func() {
		mgr.initWG.Done() // unblock so the test binary doesn't leak the goroutine
		_ = svc.Close()
	})

	start := time.Now()
	mgr.Shutdown()
	elapsed := time.Since(start)

	if elapsed > 5*time.Second {
		t.Fatalf("Shutdown took %v; expected it to return within the grace period when the init goroutine is stuck", elapsed)
	}
	t.Logf("Shutdown returned in %v with stuck init goroutine (grace=%v)", elapsed, mgr.shutdownGrace)
}

// TestSwitchProjectDoesNotWaitForInitGoroutine is the regression test for the
// RPC-path change: SwitchProject no longer drains the previous project's init
// goroutine. It must return promptly even when a prior init has not finished
// (here: a stuck initWG that never drains, mirroring initProject wedged inside
// the non-interruptible chromem gob-decode) and even with a grace period set
// far above the tolerated latency — with the old bounded drain SwitchProject
// would have blocked for the full grace and failed the maxReturn bound.
//
// Serialization now comes from initCancel + initProject's per-step ctx checks
// (plus s.mu), and only Shutdown still waits on initWG (bounded). The CODE case
// additionally pins that readiness is reached through polling WaitReady (the
// async init), not through SwitchProject's return.
func TestSwitchProjectDoesNotWaitForInitGoroutine(t *testing.T) {
	// Far above the tolerated return latency: the removed bounded drain would
	// have blocked SwitchProject for this long, failing the maxReturn bound.
	const (
		grace     = 5 * time.Second
		maxReturn = 1 * time.Second
	)

	cases := []struct {
		name      string
		projectID string
	}{
		{name: "no-project", projectID: core.NoProjectID},
		{name: "code", projectID: "project-x"},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			// Storage roots before the Shutdown-cleanup registration
			// (t.Cleanup is LIFO): mgr.Shutdown must close the service and
			// release the lexical zap/bolt handles before the TempDir
			// RemoveAll runs — an open handle fails the unlink on Windows.
			ws, viPath := "", ""
			if tc.projectID != core.NoProjectID {
				ws = t.TempDir()
				if err := os.WriteFile(filepath.Join(ws, "a.go"), []byte("package a\n"), 0o644); err != nil {
					t.Fatalf("write a.go: %v", err)
				}
				viPath = filepath.Join(t.TempDir(), "vi")
			}

			svc, err := NewService(ServiceConfig{EmbeddingFunc: fakeEmbeddingFunc()})
			if err != nil {
				t.Fatalf("NewService: %v", err)
			}

			mgr := &Manager{
				service:       svc,
				logger:        slog.New(slog.DiscardHandler),
				chunkFn:       defaultChunkFn,
				hashFn:        embedding.ComputeFileHash,
				shutdownGrace: grace,
			}

			// Simulate a stuck prior init: it never calls Done(), so initWG
			// never drains — mirroring initProject wedged inside the
			// non-interruptible SetProject. Done first in cleanup so the later
			// bounded Shutdown does not itself wait out the grace.
			mgr.initWG.Add(1)
			t.Cleanup(func() {
				mgr.initWG.Done()
				mgr.Shutdown()
			})

			start := time.Now()
			if err := mgr.SwitchProject(tc.projectID, ws, viPath, ProjectCallbacks{}); err != nil {
				t.Fatalf("SwitchProject: unexpected error: %v", err)
			}
			elapsed := time.Since(start)
			if elapsed > maxReturn {
				t.Fatalf("SwitchProject took %v with a stuck prior init (grace=%v); it must not wait on initWG", elapsed, grace)
			}
			t.Logf("SwitchProject returned in %v with stuck prior init (grace=%v)", elapsed, grace)

			if tc.projectID == core.NoProjectID {
				// No Project disables the subsystem: no indexing goroutine runs,
				// so readiness is never restored.
				if svc.IsReady() {
					t.Fatal("expected service to be not-ready for No Project")
				}
				return
			}

			// CODE: readiness is reached asynchronously via polling WaitReady —
			// SwitchProject returned before the background init had finished.
			ctxReady, cancelReady := context.WithTimeout(context.Background(), 10*time.Second)
			defer cancelReady()
			if err := svc.WaitReady(ctxReady); err != nil {
				t.Fatalf("WaitReady after switch: %v", err)
			}
			col := svc.GetCollection()
			if col == nil || col.Count() == 0 {
				t.Fatal("expected the switched project's collection to be populated after polling WaitReady")
			}
		})
	}
}

// TestManagerParkDoubleSwitchConsistency hammers SwitchProject back and forth
// between two CODE projects with parking enabled (ParkCapacity > 0) and then
// waits for the final project's index to settle. It exercises the
// restore-vs-orphaned-init race: each switch cancels the previous async init
// while the next one restores a parked state. Run under -race, the park/restore
// bookkeeping must stay clean.
func TestManagerParkDoubleSwitchConsistency(t *testing.T) {
	persistDir := t.TempDir()

	svc, err := NewService(ServiceConfig{EmbeddingFunc: fakeEmbeddingFunc(), ParkCapacity: 2})
	if err != nil {
		t.Fatalf("NewService: %v", err)
	}
	mgr := &Manager{
		service: svc,
		logger:  slog.New(slog.DiscardHandler),
		chunkFn: defaultChunkFn,
		hashFn:  embedding.ComputeFileHash,
	}
	t.Cleanup(func() { mgr.Shutdown() })

	wsA := t.TempDir()
	if err := os.WriteFile(filepath.Join(wsA, "a.go"), []byte("package a\n"), 0o644); err != nil {
		t.Fatalf("write a.go: %v", err)
	}
	wsB := t.TempDir()
	if err := os.WriteFile(filepath.Join(wsB, "b.go"), []byte("package b\n"), 0o644); err != nil {
		t.Fatalf("write b.go: %v", err)
	}
	viA := filepath.Join(persistDir, "A")
	viB := filepath.Join(persistDir, "B")

	for round := 0; round < 4; round++ {
		if err := mgr.SwitchProject("A", wsA, viA, ProjectCallbacks{}); err != nil {
			t.Fatalf("SwitchProject A (round %d): %v", round, err)
		}
		if err := mgr.SwitchProject("B", wsB, viB, ProjectCallbacks{}); err != nil {
			t.Fatalf("SwitchProject B (round %d): %v", round, err)
		}
	}

	// Land on A and wait for its index to settle.
	if err := mgr.SwitchProject("A", wsA, viA, ProjectCallbacks{}); err != nil {
		t.Fatalf("SwitchProject A (final): %v", err)
	}
	ctx, cancel := context.WithTimeout(context.Background(), 20*time.Second)
	defer cancel()
	if err := svc.WaitReady(ctx); err != nil {
		t.Fatalf("WaitReady after double switches: %v", err)
	}
	if col := svc.GetCollection(); col == nil {
		t.Fatal("no collection after double switches")
	}
}

// TestManagerShutdownReleasesParked verifies that Shutdown closes the parked
// project states (not just the current one) and returns without hanging —
// i.e. no leaked handles or goroutines in the park LRU.
func TestManagerShutdownReleasesParked(t *testing.T) {
	persistDir := t.TempDir()

	svc, err := NewService(ServiceConfig{EmbeddingFunc: fakeEmbeddingFunc(), ParkCapacity: 3})
	if err != nil {
		t.Fatalf("NewService: %v", err)
	}
	mgr := &Manager{
		service: svc,
		logger:  slog.New(slog.DiscardHandler),
		chunkFn: defaultChunkFn,
		hashFn:  embedding.ComputeFileHash,
	}

	wsA := t.TempDir()
	if err := os.WriteFile(filepath.Join(wsA, "a.go"), []byte("package a\n"), 0o644); err != nil {
		t.Fatalf("write a.go: %v", err)
	}
	wsB := t.TempDir()
	if err := os.WriteFile(filepath.Join(wsB, "b.go"), []byte("package b\n"), 0o644); err != nil {
		t.Fatalf("write b.go: %v", err)
	}
	viA := filepath.Join(persistDir, "A")
	viB := filepath.Join(persistDir, "B")

	waitReady := func() {
		t.Helper()
		ctx, cancel := context.WithTimeout(context.Background(), 15*time.Second)
		defer cancel()
		if err := svc.WaitReady(ctx); err != nil {
			t.Fatalf("WaitReady: %v", err)
		}
	}

	if err := mgr.SwitchProject("A", wsA, viA, ProjectCallbacks{}); err != nil {
		t.Fatalf("SwitchProject A: %v", err)
	}
	waitReady()
	if err := mgr.SwitchProject("B", wsB, viB, ProjectCallbacks{}); err != nil {
		t.Fatalf("SwitchProject B: %v", err)
	}
	waitReady()

	svc.mu.RLock()
	parked := len(svc.parked)
	svc.mu.RUnlock()
	if parked != 1 {
		t.Fatalf("parked = %d, want 1 (A parked after switching to B)", parked)
	}

	done := make(chan struct{})
	go func() {
		mgr.Shutdown()
		close(done)
	}()
	select {
	case <-done:
	case <-time.After(25 * time.Second):
		t.Fatal("Shutdown did not return — possible deadlock/leak tearing down parked states")
	}

	svc.mu.RLock()
	defer svc.mu.RUnlock()
	if svc.parked != nil {
		t.Errorf("parked not cleared by Shutdown: %d states remain", len(svc.parked))
	}
}
