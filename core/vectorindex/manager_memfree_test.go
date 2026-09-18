package vectorindex

import (
	"context"
	"log/slog"
	"os"
	"path/filepath"
	"sync/atomic"
	"testing"
	"time"

	"github.com/v0lka/sp4rk/embedding"
)

// newMemFreeManager builds a Manager literal (the established test
// construction) whose freeOSMemory seam records calls into counter instead
// of running the real runtime/debug.FreeOSMemory, so the tests assert WHEN
// the seam fires without paying a forced GC cycle.
func newMemFreeManager(t *testing.T, counter *atomic.Int32) (*Manager, *Service) {
	t.Helper()

	svc, err := NewService(ServiceConfig{
		EmbeddingFunc: fakeEmbeddingFunc(),
	})
	if err != nil {
		t.Fatalf("NewService: %v", err)
	}
	t.Cleanup(func() {
		if err := svc.Close(); err != nil {
			t.Logf("Service.Close in cleanup: %v", err)
		}
	})

	// Swap the single package-level freeOSMemory seam (the same one the park
	// eviction and content-less migration use) so the tests assert WHEN the
	// seam fires without paying a forced GC cycle.
	orig := freeOSMemory
	freeOSMemory = func() { counter.Add(1) }
	t.Cleanup(func() { freeOSMemory = orig })

	mgr := &Manager{
		service: svc,
		logger:  slog.New(slog.DiscardHandler),
		chunkFn: defaultChunkFn,
		hashFn:  embedding.ComputeFileHash,
	}
	return mgr, svc
}

// waitForBackgroundPasses waits for the init goroutine and every background
// indexing goroutine to finish. The caller must first observe a signal that
// proves Add(1) already ran (WaitReady after a pass, or Reindex having
// returned), so the WaitGroup wait itself is race-free.
func waitForBackgroundPasses(t *testing.T, mgr *Manager, svc *Service) {
	t.Helper()
	waitCtx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()
	if err := svc.WaitReady(waitCtx); err != nil {
		t.Fatalf("WaitReady: %v", err)
	}
	mgr.indexingWG.Wait()
	mgr.initWG.Wait()
}

// TestInitProject_FullIndexFreesOSMemory verifies the freeOSMemory seam fires
// after the initProject background goroutine completes a FULL indexing pass
// (empty collection → IndexFull): the transient spike of embedding the whole
// corpus must be handed back to the OS immediately.
func TestInitProject_FullIndexFreesOSMemory(t *testing.T) {
	var calls atomic.Int32
	// Register the index temp dir BEFORE newMemFreeManager's svc.Close
	// cleanup: t.Cleanup is LIFO, so svc.Close runs first and releases the
	// bleve lexical store (.bolt/.zap) handles before TempDir's RemoveAll.
	// Windows refuses to delete files that still have open handles (EBUSY).
	viDir := filepath.Join(t.TempDir(), "vi")
	ws := t.TempDir()
	mgr, svc := newMemFreeManager(t, &calls)

	if err := os.WriteFile(filepath.Join(ws, "a.go"), []byte("package a\n"), 0o644); err != nil {
		t.Fatalf("write a.go: %v", err)
	}

	if err := mgr.SwitchProject("project-a", ws, viDir, ProjectCallbacks{}); err != nil {
		t.Fatalf("SwitchProject: %v", err)
	}
	waitForBackgroundPasses(t, mgr, svc)

	if calls.Load() == 0 {
		t.Error("expected the freeOSMemory seam to be called after a full indexing pass")
	}
	if col := svc.GetCollection(); col == nil || col.Count() == 0 {
		t.Error("expected the full pass to index the workspace file")
	}
}

// TestReindex_IncrementalDoesNotFreeOSMemory verifies the seam does NOT fire
// for a small incremental pass: after the initial full index, a manual
// Reindex against a non-empty collection reconciles via IndexIncremental,
// and a forced full GC per such pass would burn CPU for no memory win.
func TestReindex_IncrementalDoesNotFreeOSMemory(t *testing.T) {
	var calls atomic.Int32
	// Register the index temp dir BEFORE newMemFreeManager's svc.Close
	// cleanup (t.Cleanup is LIFO): svc.Close must release the bleve
	// .bolt/.zap handles before TempDir's RemoveAll on Windows.
	viDir := filepath.Join(t.TempDir(), "vi")
	ws := t.TempDir()
	mgr, svc := newMemFreeManager(t, &calls)

	if err := os.WriteFile(filepath.Join(ws, "a.go"), []byte("package a\n"), 0o644); err != nil {
		t.Fatalf("write a.go: %v", err)
	}

	// Initial full index (seam fires once for the full pass).
	if err := mgr.SwitchProject("project-a", ws, viDir, ProjectCallbacks{}); err != nil {
		t.Fatalf("SwitchProject: %v", err)
	}
	waitForBackgroundPasses(t, mgr, svc)
	if calls.Load() == 0 {
		t.Fatal("precondition failed: the initial full pass must trigger the seam")
	}
	calls.Store(0)

	// A tiny file change, then a manual reindex: the collection is
	// non-empty, so the pass is incremental.
	if err := os.WriteFile(filepath.Join(ws, "b.go"), []byte("package b\n"), 0o644); err != nil {
		t.Fatalf("write b.go: %v", err)
	}
	if err := mgr.Reindex(context.Background()); err != nil {
		t.Fatalf("Reindex: %v", err)
	}
	mgr.indexingWG.Wait()

	if got := calls.Load(); got != 0 {
		t.Errorf("incremental reindex must not call the freeOSMemory seam, got %d calls", got)
	}
}

// TestReindex_EmptyCollectionFullPassFreesOSMemory verifies the Reindex
// goroutine's own IndexFull path (empty collection with a published indexer)
// also fires the seam — the manual "rebuild from scratch" flow allocates the
// corpus just like the initial pass.
func TestReindex_EmptyCollectionFullPassFreesOSMemory(t *testing.T) {
	var calls atomic.Int32
	// Register the index temp dir BEFORE newMemFreeManager's svc.Close
	// cleanup (t.Cleanup is LIFO): svc.Close must release the bleve
	// .bolt/.zap handles before TempDir's RemoveAll on Windows.
	viDir := filepath.Join(t.TempDir(), "vi")
	mgr, svc := newMemFreeManager(t, &calls)

	// Empty workspace: the collection stays empty after the init pass, so a
	// manual Reindex takes the IndexFull branch deterministically.
	ws := t.TempDir()
	if err := mgr.SwitchProject("project-a", ws, viDir, ProjectCallbacks{}); err != nil {
		t.Fatalf("SwitchProject: %v", err)
	}
	waitForBackgroundPasses(t, mgr, svc)
	calls.Store(0)

	if err := mgr.Reindex(context.Background()); err != nil {
		t.Fatalf("Reindex: %v", err)
	}
	mgr.indexingWG.Wait()

	if got := calls.Load(); got != 1 {
		t.Errorf("full reindex on an empty collection must call the seam exactly once, got %d", got)
	}
}

// TestBranchSwitch_NoOpSwitchDoesNotFreeOSMemory pins the gate on the git
// branch-switch path: a switch that ends in an incremental (or outright
// no-op) reconciliation must NOT force a stop-the-world GC. The monitor
// callback fired freeOSMemory unconditionally, so hopping between
// already-indexed branches — where HandleBranchSwitch only swaps collections
// and the incremental pass finds no changes — paid a full FreeOSMemory cycle
// (freezing the whole app, in-flight LLM streaming included) per hop for
// zero memory benefit.
func TestBranchSwitch_NoOpSwitchDoesNotFreeOSMemory(t *testing.T) {
	var calls atomic.Int32
	// Register the index temp dir BEFORE newMemFreeManager's svc.Close
	// cleanup (t.Cleanup is LIFO): svc.Close must release the bleve
	// .bolt/.zap handles before TempDir's RemoveAll on Windows.
	viDir := filepath.Join(t.TempDir(), "vi")
	ws := createTestWorkspace(t)
	mgr, svc := newMemFreeManager(t, &calls)

	if err := svc.SetProject("project-a", viDir); err != nil {
		t.Fatalf("SetProject: %v", err)
	}
	if err := svc.SwitchBranch(context.Background(), "main"); err != nil {
		t.Fatalf("SwitchBranch: %v", err)
	}

	indexer := NewIndexer(IndexerConfig{
		Service: svc,
		ChunkFn: fakeChunkFunc,
		HashFn:  fakeHashFunc,
	})

	// Populate the main collection so a later same-branch switch takes the
	// incremental no-op path rather than a full pass.
	if err := indexer.IndexFull(context.Background(), ws); err != nil {
		t.Fatalf("IndexFull: %v", err)
	}
	if col := svc.GetCollection(); col == nil || col.Count() == 0 {
		t.Fatal("precondition: the main collection must be non-empty")
	}
	calls.Store(0)

	// Same-branch no-op: SwitchBranch early-returns, the collection is
	// non-empty, and the incremental pass finds no changes.
	mgr.handleBranchSwitch(context.Background(), indexer, ws, "main")

	if got := calls.Load(); got != 0 {
		t.Errorf("no-op branch switch must not call the freeOSMemory seam, got %d calls", got)
	}
}

// TestBranchSwitch_FullPassFreesOSMemory is the positive counterpart: a
// switch to a branch whose collection is empty runs a full pass, whose
// transient spike must be returned to the OS exactly once — matching the
// other full-pass call sites (initProject's empty-collection branch and
// Reindex).
func TestBranchSwitch_FullPassFreesOSMemory(t *testing.T) {
	var calls atomic.Int32
	// Register the index temp dir BEFORE newMemFreeManager's svc.Close
	// cleanup (t.Cleanup is LIFO): svc.Close must release the bleve
	// .bolt/.zap handles before TempDir's RemoveAll on Windows.
	viDir := filepath.Join(t.TempDir(), "vi")
	ws := createTestWorkspace(t)
	mgr, svc := newMemFreeManager(t, &calls)

	if err := svc.SetProject("project-a", viDir); err != nil {
		t.Fatalf("SetProject: %v", err)
	}
	if err := svc.SwitchBranch(context.Background(), "main"); err != nil {
		t.Fatalf("SwitchBranch: %v", err)
	}

	indexer := NewIndexer(IndexerConfig{
		Service: svc,
		ChunkFn: fakeChunkFunc,
		HashFn:  fakeHashFunc,
	})
	if err := indexer.IndexFull(context.Background(), ws); err != nil {
		t.Fatalf("IndexFull: %v", err)
	}
	calls.Store(0)

	// feature/new has no collection yet → the switch runs a full pass.
	mgr.handleBranchSwitch(context.Background(), indexer, ws, "feature/new")

	if got := calls.Load(); got != 1 {
		t.Errorf("full-pass branch switch must call the seam exactly once, got %d calls", got)
	}
	if col := svc.GetCollection(); col == nil || col.Count() == 0 {
		t.Error("expected the target branch collection to be indexed by the full pass")
	}
}
