package vectorindex

// Regression tests for the review findings on initProject's publication
// atomicity, branch-step cancellation guards, DeleteProjectData's state
// reset, and the embedding-cache accounting seed — see the c1 review report.

import (
	"context"
	"log/slog"
	"os"
	"path/filepath"
	"runtime"
	"sync/atomic"
	"testing"
	"time"

	"github.com/v0lka/sp4rk/embedding"
)

// newTestManager builds the minimal Manager literal the concurrency tests
// use: a real service with a fake embedder and the default chunk/hash
// functions.
func newTestManager(t *testing.T) (*Manager, *Service) {
	t.Helper()
	svc, err := NewService(ServiceConfig{EmbeddingFunc: fakeEmbeddingFunc()})
	if err != nil {
		t.Fatalf("NewService: %v", err)
	}
	mgr := &Manager{
		service: svc,
		logger:  slog.New(slog.DiscardHandler),
		chunkFn: defaultChunkFn,
		hashFn:  embedding.ComputeFileHash,
	}
	return mgr, svc
}

// TestManagerPublishInitState_CancelledContextDoesNotPublish pins the
// publishInitState contract directly: a cancelled init context must abort
// the publication (and release the background-indexing context it created)
// instead of installing the indexer, workspace, and indexCancel over the
// newer project's torn-down state.
func TestManagerPublishInitState_CancelledContextDoesNotPublish(t *testing.T) {
	mgr, svc := newTestManager(t)
	t.Cleanup(func() {
		mgr.Shutdown()
		_ = svc.Close()
	})

	idx := NewIndexer(IndexerConfig{
		Service: svc,
		ChunkFn: fakeChunkFunc,
		HashFn:  fakeHashFunc,
	})

	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	indexCtx, indexCancel, published := mgr.publishInitState(ctx, "p-cancelled", "/ws", idx)
	if published {
		t.Fatal("publishInitState published for a cancelled context")
	}
	if indexCtx != nil || indexCancel != nil {
		t.Errorf("publishInitState returned (%v, %v) on abort, want (nil, nil)", indexCtx != nil, indexCancel != nil)
	}

	mgr.mu.RLock()
	staleIndexer := mgr.indexer
	staleWorkspace := mgr.workspacePath
	staleCancel := mgr.indexCancel
	staleCtx := mgr.indexCtx
	mgr.mu.RUnlock()
	if staleIndexer != nil || staleWorkspace != "" || staleCancel != nil || staleCtx != nil {
		t.Errorf("cancelled publish installed stale state: indexer=%v workspace=%q indexCancel=%v indexCtx=%v",
			staleIndexer != nil, staleWorkspace, staleCancel != nil, staleCtx != nil)
	}

	// A live context still publishes (and hands back the cancel pair).
	liveCtx, liveCancel := context.WithCancel(context.Background())
	defer liveCancel()
	gotCtx, gotCancel, ok := mgr.publishInitState(liveCtx, "p-live", "/ws", idx)
	if !ok {
		t.Fatal("publishInitState refused to publish for a live context")
	}
	defer gotCancel()
	if gotCtx == nil {
		t.Fatal("publishInitState returned a nil index context on success")
	}
	mgr.mu.RLock()
	if mgr.indexer != idx || mgr.workspacePath != "/ws" {
		t.Errorf("live publish did not install the given state: indexer=%v workspace=%q", mgr.indexer != idx, mgr.workspacePath)
	}
	mgr.mu.RUnlock()
}

// TestManagerInitProject_CancelBetweenCheckAndPublishAborts exercises the
// check-to-publish TOCTOU window end to end: the init goroutine has passed
// its unlocked pre-publish ctx check and is parked on m.mu (the lock
// SwitchProject cancels initCancel under) when the cancellation lands. The
// publication must abort — without the under-lock re-check the orphaned init
// would install its indexer/workspace/indexCancel after the newer project's
// teardown already nulled them.
func TestManagerInitProject_CancelBetweenCheckAndPublishAborts(t *testing.T) {
	persistDir := t.TempDir()
	mgr, svc := newTestManager(t)
	t.Cleanup(func() {
		mgr.Shutdown()
		_ = svc.Close()
	})

	ws := t.TempDir()
	viPath := filepath.Join(persistDir, "project-a")

	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()

	// Hold m.mu so the init goroutine — after passing its unlocked
	// pre-publish ctx check — parks exactly on the publish lock, the point
	// where a rapid SwitchProject's cancel (issued under the same lock)
	// lands.
	mgr.mu.Lock()

	mgr.initWG.Add(1)
	go mgr.initProject(ctx, "project-a", ws, viPath, "", ProjectCallbacks{})

	// Marker: SwitchBranch completed ⇒ the goroutine is between the
	// pre-publish check and the publish lock. (setStatus uses statusMu and
	// SetProject/CurrentBranch/SwitchBranch use the service lock, so nothing
	// before the publish needs m.mu.)
	deadline := time.Now().Add(5 * time.Second)
	for time.Now().Before(deadline) {
		if svc.CurrentBranchName() != "" {
			break
		}
		time.Sleep(10 * time.Millisecond)
	}
	if svc.CurrentBranchName() == "" {
		mgr.mu.Unlock()
		t.Fatal("init never completed its branch switch")
	}
	// Let the goroutine reach the (blocked) publish lock.
	time.Sleep(100 * time.Millisecond)

	// The cancellation a rapid follow-up SwitchProject would issue — while
	// the init goroutine holds no lock and has already passed its check.
	cancel()
	mgr.mu.Unlock()

	done := make(chan struct{})
	go func() {
		mgr.initWG.Wait()
		close(done)
	}()
	select {
	case <-done:
	case <-time.After(10 * time.Second):
		t.Fatal("init goroutine did not drain after cancellation")
	}

	mgr.mu.RLock()
	idx := mgr.indexer
	wsPath := mgr.workspacePath
	indexCancel := mgr.indexCancel
	indexCtx := mgr.indexCtx
	gitMon := mgr.gitMonitor
	mgr.mu.RUnlock()
	if idx != nil || wsPath != "" || indexCancel != nil || indexCtx != nil || gitMon != nil {
		t.Errorf("cancelled init published stale state: indexer=%v workspace=%q indexCancel=%v indexCtx=%v gitMonitor=%v",
			idx != nil, wsPath, indexCancel != nil, indexCtx != nil, gitMon != nil)
	}
}

// TestManagerInstallGitMonitor_CancelledContextStopsAndSkips pins
// installGitMonitor: a monitor created after the init was cancelled must be
// stopped (its fsnotify watcher and done channel released) and never
// installed, while a live context installs it.
func TestManagerInstallGitMonitor_CancelledContextStopsAndSkips(t *testing.T) {
	mgr, svc := newTestManager(t)
	t.Cleanup(func() {
		mgr.Shutdown()
		_ = svc.Close()
	})

	gitMon, err := NewGitMonitor(t.TempDir(), func(string) {}, slog.New(slog.DiscardHandler))
	if err != nil {
		t.Fatalf("NewGitMonitor: %v", err)
	}

	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	if installed := mgr.installGitMonitor(ctx, "p", gitMon); installed {
		t.Fatal("installGitMonitor installed a monitor for a cancelled context")
	}
	mgr.mu.RLock()
	installedMon := mgr.gitMonitor
	mgr.mu.RUnlock()
	if installedMon != nil {
		t.Error("cancelled git monitor was published")
	}
	select {
	case <-gitMon.done:
		// stopped as required
	default:
		t.Error("stale git monitor was not stopped (watcher/event loop leak)")
	}

	liveMon, err := NewGitMonitor(t.TempDir(), func(string) {}, slog.New(slog.DiscardHandler))
	if err != nil {
		t.Fatalf("NewGitMonitor (live): %v", err)
	}
	t.Cleanup(func() { _ = liveMon.Stop() })
	liveCtx, liveCancel := context.WithCancel(context.Background())
	defer liveCancel()
	if installed := mgr.installGitMonitor(liveCtx, "p", liveMon); !installed {
		t.Fatal("installGitMonitor refused a monitor for a live context")
	}
	mgr.mu.RLock()
	installedMon = mgr.gitMonitor
	mgr.mu.RUnlock()
	if installedMon != liveMon {
		t.Error("live git monitor was not installed")
	}
}

// TestManagerInitProject_BranchDetectFailureDuringCancelIsIgnored covers the
// branch-step cancellation guard: when the cancellation lands while
// CurrentBranch's git subprocess runs, the resulting failure must be
// ignored (no notifyInitFailure, no SetReady(true), no "unavailable"
// status) — it belongs to an orphaned init whose newer project already owns
// readiness. Determinism comes from a PATH-shimmed git that touches a marker
// and sleeps: the marker proves the init is inside branch detection with a
// live ctx when the test cancels it.
func TestManagerInitProject_BranchDetectFailureDuringCancelIsIgnored(t *testing.T) {
	if runtime.GOOS == "windows" {
		t.Skip("POSIX shell git shim")
	}
	shimDir := t.TempDir()
	marker := filepath.Join(shimDir, "shim-started")
	script := "#!/bin/sh\ntouch " + marker + "\nexec sleep 30\n"
	if err := os.WriteFile(filepath.Join(shimDir, "git"), []byte(script), 0o755); err != nil {
		t.Fatalf("writing git shim: %v", err)
	}
	t.Setenv("PATH", shimDir+string(os.PathListSeparator)+os.Getenv("PATH"))

	mgr, svc := newTestManager(t)
	t.Cleanup(func() {
		mgr.Shutdown()
		_ = svc.Close()
	})

	// A .git marker makes CurrentBranch shell out to git (the shim) instead
	// of returning DefaultBranch, so branch detection blocks there.
	ws := t.TempDir()
	if err := os.MkdirAll(filepath.Join(ws, ".git"), 0o755); err != nil {
		t.Fatalf("creating .git marker: %v", err)
	}

	var onFailure atomic.Bool
	if err := mgr.SwitchProject("p-shim", ws, filepath.Join(t.TempDir(), "vi"),
		ProjectCallbacks{OnFailure: func(error) { onFailure.Store(true) }}); err != nil {
		t.Fatalf("SwitchProject: %v", err)
	}

	// Wait until the shim is actually running: proof the init goroutine is
	// inside CurrentBranch with a still-live ctx (it passed the check after
	// SetProject).
	deadline := time.Now().Add(5 * time.Second)
	for time.Now().Before(deadline) {
		if _, err := os.Stat(marker); err == nil {
			break
		}
		time.Sleep(10 * time.Millisecond)
	}
	if _, err := os.Stat(marker); err != nil {
		t.Fatal("git shim never started; init did not reach branch detection")
	}

	// Cancel the in-flight init — what a rapid follow-up SwitchProject does.
	mgr.mu.RLock()
	initCancel := mgr.initCancel
	mgr.mu.RUnlock()
	if initCancel == nil {
		t.Fatal("no init cancel registered")
	}
	initCancel()

	// The ctx-aware cmd kills the shim; runGit then fails and the
	// branch-detect failure path must IGNORE the failure (orphaned init).
	done := make(chan struct{})
	go func() {
		mgr.initWG.Wait()
		close(done)
	}()
	select {
	case <-done:
	case <-time.After(15 * time.Second):
		t.Fatal("init goroutine did not drain after cancellation")
	}

	if onFailure.Load() {
		t.Error("orphaned init surfaced its branch-detect failure to the caller (notifyInitFailure)")
	}
	if svc.IsReady() {
		t.Error("orphaned init flipped service readiness (SetReady(true))")
	}
	mgr.statusMu.RLock()
	state := mgr.currentState
	mgr.statusMu.RUnlock()
	if state == IndexStateUnavailable {
		t.Error("orphaned init marked the manager status unavailable")
	}
}

// TestServiceDeleteProjectData_ResetsCurrentState pins the DeleteProjectData
// reset: after deleting the CURRENT project's data, the closed state must be
// replaced by an empty placeholder (projectID/projectPath cleared) so the
// next SetProject never parks the dead, db-less state into the LRU — where
// it would both waste a park slot and, for a future deterministic id+path
// reuse, restore a collection-less state.
func TestServiceDeleteProjectData_ResetsCurrentState(t *testing.T) {
	base := t.TempDir()
	svc, err := NewService(ServiceConfig{EmbeddingFunc: fakeEmbeddingFunc(), ParkCapacity: 3})
	if err != nil {
		t.Fatalf("NewService: %v", err)
	}
	t.Cleanup(func() { _ = svc.Close() })

	p1 := filepath.Join(base, "p1")
	if err := svc.SetProject("p1", p1); err != nil {
		t.Fatalf("SetProject p1: %v", err)
	}
	if err := svc.SwitchBranch(context.Background(), "main"); err != nil {
		t.Fatalf("SwitchBranch p1: %v", err)
	}

	if err := svc.DeleteProjectData(p1); err != nil {
		t.Fatalf("DeleteProjectData: %v", err)
	}

	svc.mu.RLock()
	cur := svc.current
	svc.mu.RUnlock()
	if cur == nil {
		t.Fatal("current state is nil after DeleteProjectData")
	}
	if cur.projectID != "" || cur.projectPath != "" {
		t.Errorf("current state not reset after delete: projectID=%q projectPath=%q", cur.projectID, cur.projectPath)
	}
	if cur.db != nil || cur.collection != nil {
		t.Errorf("current state still carries handles after delete: db=%v collection=%v", cur.db != nil, cur.collection != nil)
	}

	// Switching to two other projects must park only live states: the dead
	// p1 state must never enter the LRU.
	p2 := filepath.Join(base, "p2")
	p3 := filepath.Join(base, "p3")
	if err := svc.SetProject("p2", p2); err != nil {
		t.Fatalf("SetProject p2: %v", err)
	}
	if err := svc.SwitchBranch(context.Background(), "main"); err != nil {
		t.Fatalf("SwitchBranch p2: %v", err)
	}
	if err := svc.SetProject("p3", p3); err != nil {
		t.Fatalf("SetProject p3: %v", err)
	}

	svc.mu.RLock()
	parked := append([]*projectState(nil), svc.parked...)
	svc.mu.RUnlock()
	for _, ps := range parked {
		if filepath.Clean(ps.projectPath) == filepath.Clean(p1) {
			t.Error("the closed state of the deleted project was parked into the LRU")
		}
		if ps.db == nil {
			t.Errorf("a db-less (closed) state entered the park LRU: %+v", ps)
		}
	}
	if len(parked) != 1 {
		t.Errorf("parked = %d states, want exactly p2 (1)", len(parked))
	}
}

// TestEmbeddingCache_PruneDefersToInFlightSeed pins the deferred fast path:
// while the background seed walk is in flight, a synchronous prune (called
// from the embed path under the service write lock) must not walk — and once
// the flag clears, the normal seeding/retry semantics resume.
func TestEmbeddingCache_PruneDefersToInFlightSeed(t *testing.T) {
	root := t.TempDir()
	entrySize := int64(len(encodeEmbeddingCacheEntry(make([]float32, 8))))
	cache := newEmbeddingCache(root, "fp-defer", 8, entrySize*100, nil)

	cache.seedInFlight.Store(true) // simulate a seed walk in flight
	cache.prune()
	if cache.pruneWalks != 0 {
		t.Errorf("prune walked while a seed was in flight: %d walks", cache.pruneWalks)
	}

	// prune must never block on c.mu: the embed path calls it while holding the
	// service write lock, and c.mu is held both by get/put mutations and by the
	// seed walk's short reconcile step (the tree scan itself is lock-free).
	// Holding the lock here reproduces a concurrent holder; a plain Lock (or
	// checking the flag only inside pruneLocked) would deadlock this call until
	// the 2s guard fires.
	cache.mu.Lock()
	done := make(chan struct{})
	go func() {
		cache.prune()
		close(done)
	}()
	select {
	case <-done:
	case <-time.After(2 * time.Second):
		cache.mu.Unlock()
		t.Fatal("prune blocked behind the in-flight seed walk holding the mutex")
	}
	cache.mu.Unlock()

	cache.seedInFlight.Store(false)
	cache.prune()
	if cache.pruneWalks != 1 || !cache.accountingSeeded {
		t.Errorf("after the seed flag cleared: walks=%d seeded=%t, want 1/true", cache.pruneWalks, cache.accountingSeeded)
	}
}

// TestEmbeddingCache_SeedAccountingAsyncSeedsEventually pins the async seed:
// a fresh cache over a pre-warmed tree seeds its byte accounting from the
// walked total without the caller blocking on the walk.
func TestEmbeddingCache_SeedAccountingAsyncSeedsEventually(t *testing.T) {
	root := t.TempDir()
	dim := 8
	cache := newEmbeddingCache(root, "fp-async-seed", dim, 1<<30, nil)

	vec := make([]float32, dim)
	entrySize := int64(len(encodeEmbeddingCacheEntry(vec)))
	for _, text := range []string{"a", "b", "c"} {
		path := cache.path(cache.key(text))
		if err := os.MkdirAll(filepath.Dir(path), 0o750); err != nil {
			t.Fatalf("MkdirAll: %v", err)
		}
		if err := os.WriteFile(path, encodeEmbeddingCacheEntry(vec), 0o600); err != nil {
			t.Fatalf("writing entry: %v", err)
		}
	}

	cache.seedAccountingAsync()
	deadline := time.Now().Add(5 * time.Second)
	var seeded bool
	var tracked int64
	for time.Now().Before(deadline) {
		cache.mu.Lock()
		seeded = cache.accountingSeeded
		tracked = cache.trackedBytes
		cache.mu.Unlock()
		if seeded {
			break
		}
		time.Sleep(5 * time.Millisecond)
	}
	if !seeded {
		t.Fatal("accounting was never seeded by the background walk")
	}
	if want := 3 * entrySize; tracked != want {
		t.Errorf("trackedBytes = %d after seed, want %d", tracked, want)
	}
}

// TestServiceSetProject_SeedsCacheAccountingAsynchronously pins the service
// wiring: SetProject seeds a warm cache's accounting on a background
// goroutine (never under the service write lock), and the seed lands within
// a generous deadline.
func TestServiceSetProject_SeedsCacheAccountingAsynchronously(t *testing.T) {
	base := t.TempDir()
	cachePath := filepath.Join(base, "embedding_cache")
	const fingerprint = "fp-svc-async"
	const dim = 8

	// Pre-warm the cache directory with one valid entry the fresh cache has
	// never walked (same fingerprint so the keys match).
	scratch := newEmbeddingCache(cachePath, fingerprint, dim, 1<<30, nil)
	entry := encodeEmbeddingCacheEntry(make([]float32, dim))
	warmPath := scratch.path(scratch.key("warm"))
	if err := os.MkdirAll(filepath.Dir(warmPath), 0o750); err != nil {
		t.Fatalf("MkdirAll: %v", err)
	}
	if err := os.WriteFile(warmPath, entry, 0o600); err != nil {
		t.Fatalf("writing warm entry: %v", err)
	}

	svc, err := NewService(ServiceConfig{
		EmbeddingFunc:             fakeEmbeddingFunc(),
		BatchEmbedder:             &fakeBatchEmbedder{},
		EmbeddingBatchSize:        2,
		EmbeddingCacheFingerprint: fingerprint,
		EmbeddingDimension:        dim,
		EmbeddingCacheMaxBytes:    1 << 30,
		Telemetry:                 &Telemetry{},
	})
	if err != nil {
		t.Fatalf("NewService: %v", err)
	}
	t.Cleanup(func() { _ = svc.Close() })

	if err := svc.SetProject("async-seed", base, cachePath); err != nil {
		t.Fatalf("SetProject: %v", err)
	}

	deadline := time.Now().Add(5 * time.Second)
	var seeded bool
	var tracked int64
	for time.Now().Before(deadline) {
		svc.mu.RLock()
		cache := svc.current.embeddingCache
		svc.mu.RUnlock()
		if cache == nil {
			t.Fatal("SetProject did not construct an embedding cache")
		}
		cache.mu.Lock()
		seeded = cache.accountingSeeded
		tracked = cache.trackedBytes
		cache.mu.Unlock()
		if seeded {
			break
		}
		time.Sleep(5 * time.Millisecond)
	}
	if !seeded {
		t.Fatal("SetProject never seeded the cache accounting")
	}
	if want := int64(len(entry)); tracked != want {
		t.Errorf("trackedBytes = %d after async seed, want %d", tracked, want)
	}
}
