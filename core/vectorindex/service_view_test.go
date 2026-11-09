package vectorindex

import (
	"context"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	chromem "github.com/philippgille/chromem-go"
)

// TestCommitEmbeddedDocuments_NilCollectionReturnsError pins the crash
// barrier behind the 2026-11-05 silent app exit: when the working store is
// torn down underneath a pass, commitEmbeddedDocuments must return the same
// honest error as Service.AddDocuments — a nil collection handed to chromem
// AddDocuments dereferences its receiver only inside spawned goroutines
// (chromem-go collection.go), so without this guard the pass takes the whole
// process down with an unrecoverable SIGSEGV instead of failing the pass.
// The document mirrors the incident's shape: pre-embedded (a batch-embedder
// window) and content-stripped.
func TestCommitEmbeddedDocuments_NilCollectionReturnsError(t *testing.T) {
	svc, err := NewService(ServiceConfig{EmbeddingFunc: fakeEmbeddingFunc()})
	if err != nil {
		t.Fatalf("NewService: %v", err)
	}
	t.Cleanup(func() { _ = svc.Close() })
	if err := svc.SetProject("proj", t.TempDir()); err != nil {
		t.Fatalf("SetProject: %v", err)
	}
	// No SwitchBranch: the working store stays unopened, collection nil.

	svc.AcquireWriteLock()
	defer svc.ReleaseWriteLock()
	doc := chromem.Document{
		ID:        "file.go:0",
		Embedding: []float32{0.1, 0.2, 0.3},
		Metadata:  map[string]string{"file_path": "file.go"},
	}
	err = svc.commitEmbeddedDocuments(context.Background(), []chromem.Document{doc}, nil)
	if err == nil {
		t.Fatal("committing against a nil collection returned nil; want the no-collection error")
	}
	if !strings.Contains(err.Error(), "no collection available") {
		t.Fatalf("commit error = %q; want it to name the missing collection", err)
	}
}

// TestCloseWorkingStoreIfCurrent_DisplacedBuilderLeavesDisplacerIntact pins
// the ownership gate on the corpus build callback's cleanup, replaying the
// exact displacement sequence of the 2026-11-05 crash: builder A binds the
// working store to its snapshot, a concurrent attach of a DIFFERENT
// snapshot displaces the slot (EnsureCorpus is single-flight per KEY, not
// globally), A's publish honestly fails — and A's cleanup must NOT close
// the displacer's freshly opened store out from under the displacer's pass.
func TestCloseWorkingStoreIfCurrent_DisplacedBuilderLeavesDisplacerIntact(t *testing.T) {
	svc, err := NewService(ServiceConfig{EmbeddingFunc: fakeEmbeddingFunc()})
	if err != nil {
		t.Fatalf("NewService: %v", err)
	}
	t.Cleanup(func() { _ = svc.Close() })
	if err := svc.SetProject("proj", t.TempDir()); err != nil {
		t.Fatalf("SetProject: %v", err)
	}
	ctx := context.Background()

	// Builder A binds the working store to its corpus build store.
	if err := svc.SwitchBranch(ctx, "snap_aaa"); err != nil {
		t.Fatalf("binding builder A's store: %v", err)
	}
	// A concurrent different-snapshot attach displaces the slot.
	if err := svc.SwitchBranch(ctx, "snap_bbb"); err != nil {
		t.Fatalf("binding the displacer's store: %v", err)
	}
	displacerCol := svc.GetCollection()
	if displacerCol == nil {
		t.Fatal("the displacer's store did not open")
	}

	// A's publish fails exactly as in the incident — the slot is no longer
	// A's corpus build store.
	pubErr := svc.PublishCorpus(context.Background(), "snap_aaa")
	if pubErr == nil || !strings.Contains(pubErr.Error(), "not the corpus build store") {
		t.Fatalf("PublishCorpus(displaced) error = %v; want the not-the-build-store failure", pubErr)
	}

	// A's ownership-gated cleanup must skip: the slot belongs to the
	// displacer now.
	if svc.CloseWorkingStoreIfCurrent("snap_aaa") {
		t.Fatal("a displaced builder's cleanup closed the working store; it must leave the displacer's slot alone")
	}
	if got := svc.CurrentBranchName(); got != "snap_bbb" {
		t.Fatalf("after the displaced builder's cleanup, branch = %q; want the displacer's snap_bbb", got)
	}
	if col := svc.GetCollection(); col == nil {
		t.Fatal("the displaced builder's cleanup tore down the displacer's collection — the mid-pass nil that killed the app")
	} else if col != displacerCol {
		t.Fatal("the displacer's collection was replaced by the displaced builder's cleanup")
	}

	// The owning builder's own cleanup still closes its store.
	if !svc.CloseWorkingStoreIfCurrent("snap_bbb") {
		t.Fatal("the owning builder's cleanup skipped its own store")
	}
	if col := svc.GetCollection(); col != nil {
		t.Error("the owning builder's cleanup left a collection behind")
	}
	if got := svc.CurrentBranchName(); got != "" {
		t.Errorf("after the owning builder's cleanup, branch = %q; want empty", got)
	}
}

// TestCommitEmbeddedDocuments_ClosedWorkingStoreReturnsError is the
// closed-store twin of the nil-collection crash barrier above: it opens a
// real working store, tears it down through the ACTUAL close path
// (CloseWorkingStore → releaseBranchResidentsLocked, which nils the live
// collection mid-service), and pins that the next commit window comes back
// as the same honest error instead of handing chromem a nil receiver. The
// never-opened variant (collection nil from the start) cannot see a
// residents-teardown regression; this one can.
func TestCommitEmbeddedDocuments_ClosedWorkingStoreReturnsError(t *testing.T) {
	svc, err := NewService(ServiceConfig{EmbeddingFunc: fakeEmbeddingFunc()})
	if err != nil {
		t.Fatalf("NewService: %v", err)
	}
	t.Cleanup(func() { _ = svc.Close() })
	if err := svc.SetProject("proj", t.TempDir()); err != nil {
		t.Fatalf("SetProject: %v", err)
	}
	ctx := context.Background()

	// Open the working store for real, then close it through the same
	// teardown the displaced-builder cleanup once used unconditionally.
	if err := svc.SwitchBranch(ctx, "snap_ccc"); err != nil {
		t.Fatalf("opening the working store: %v", err)
	}
	if svc.GetCollection() == nil {
		t.Fatal("fixture: the opened working store has no collection")
	}
	svc.CloseWorkingStore()

	svc.AcquireWriteLock()
	defer svc.ReleaseWriteLock()
	doc := chromem.Document{
		ID:        "file.go:0",
		Embedding: []float32{0.1, 0.2, 0.3},
		Metadata:  map[string]string{"file_path": "file.go"},
	}
	err = svc.commitEmbeddedDocuments(ctx, []chromem.Document{doc}, nil)
	if err == nil {
		t.Fatal("committing against a closed working store returned nil; want the no-collection error")
	}
	if !strings.Contains(err.Error(), "no collection available") {
		t.Fatalf("commit error = %q; want it to name the missing collection", err)
	}
}

// raceRootFiles returns the fixture file set for one race root: distinct
// content per root so corpus-served documents are attributable in searches.
func writeRaceRoot(t *testing.T, files map[string]string) string {
	t.Helper()
	root := t.TempDir()
	for name, content := range files {
		path := filepath.Join(root, filepath.FromSlash(name))
		if err := os.WriteFile(path, []byte(content), 0o644); err != nil {
			t.Fatalf("writing race fixture %s: %v", name, err)
		}
	}
	return root
}

// TestAttachSnapshot_ConcurrentDisplacement_DisplacedCleanupKeepsDisplacerAlive
// reproduces the 2026-11-05 silent-exit race end-to-end through the REAL
// AttachSnapshot pipeline — two concurrent attaches of DIFFERENT snapshots
// on one shared service — with the incident's interleaving forced
// deterministically (no sleeps, no scheduler luck):
//
//   - builder A parks at its first full-pass progress event, which fires
//     AFTER the walk and BEFORE IndexFull takes the service write lock —
//     the exact displacement window the git monitor's per-HEAD goroutines
//     raced through in production (EnsureCorpus leases are single-flight
//     per snapshot KEY, so a different-key attach does not wait);
//   - builder B attaches, DISPLACES A's working-store slot with its own
//     SwitchBranch, and parks at the same lock-free point inside its own
//     build callback;
//   - A is released first: its pass runs, its publish honestly fails (the
//     slot is B's now), and its cleanup must NOT close B's freshly opened
//     store — pre-fix, this unconditional CloseWorkingStore nil'd B's
//     collection mid-attach;
//   - B is released only after A fully returned: its pass then commits a
//     document window — pre-fix, chromem's AddDocuments dereferenced the
//     nil receiver inside a goroutine and killed the whole process with an
//     unrecoverable SIGSEGV; with the crash barrier alone (no ownership
//     gate) B's attach would fail with the no-collection error instead.
//
// The fixed world must show: A fails honestly, B's store survives A's
// cleanup, B's attach succeeds, B's documents are searchable, and A replays
// cleanly on its next attach.
func TestAttachSnapshot_ConcurrentDisplacement_DisplacedCleanupKeepsDisplacerAlive(t *testing.T) {
	storage := filepath.Join(t.TempDir(), "vector_index")
	registry := NewCorpusRegistry(nil)
	t.Cleanup(func() { registry.Close() })
	svc, err := NewService(ServiceConfig{
		EmbeddingFunc:             fakeEmbeddingFunc(),
		EmbeddingCacheFingerprint: "fp-race",
		// Production wires sp4rk's embedder, so full passes commit through
		// the accumulator (commitEmbeddedDocuments) — the incident's exact
		// path; without a BatchEmbedder the final flush would take the
		// legacy AddDocuments route and never exercise the window commit.
		BatchEmbedder:      &fakeBatchEmbedder{},
		EmbeddingBatchSize: 32,
		Corpus:             registry,
	})
	if err != nil {
		t.Fatalf("NewService: %v", err)
	}
	t.Cleanup(func() { _ = svc.Close() })
	if err := svc.SetProject("race", storage); err != nil {
		t.Fatalf("SetProject: %v", err)
	}
	ctx := context.Background()

	// Two distinct non-git roots: each attaches under its own synthetic
	// ngit-<root fingerprint> snapshot key, so both builds hold their own
	// lease concurrently — the precondition of the displacement.
	rootA := writeRaceRoot(t, map[string]string{
		"a_one.txt": "alpha displaced builder marker one\n",
		"a_two.txt": "alpha displaced builder marker two\n",
	})
	rootB := writeRaceRoot(t, map[string]string{
		"b_one.txt": "bravo displacer survivor marker one\n",
		"b_two.txt": "bravo displacer survivor marker two\n",
	})
	keyA := snapshotKey("ngit-" + rootFingerprint(rootA))
	keyB := snapshotKey("ngit-" + rootFingerprint(rootB))

	// parkHook parks the builder's FIRST full-pass Indexing progress event
	// until released. The event fires after the walk and before the service
	// write lock is taken, so a parked builder holds NO service lock — the
	// displacer's SwitchBranch can freely rebind the slot underneath it.
	// `fired` is written only by the builder's own attach goroutine and
	// read again by the same-indexer replay after the test observed the
	// first attach's return (channel happens-before), so it needs no lock.
	parkHook := func(parked, release chan struct{}) ProgressCallback {
		var fired bool
		return func(p IndexPhase, st IndexState, _, _ int, _ string) {
			if p == PhaseBoth && st == IndexStateIndexing && !fired {
				fired = true
				close(parked)
				<-release
			}
		}
	}
	aParked := make(chan struct{})
	bParked := make(chan struct{})
	releaseA := make(chan struct{})
	releaseB := make(chan struct{})
	// Unpark both builders no matter where the test aborts, so a failed
	// assertion cannot leak goroutines parked inside their build callback
	// past the service close (all closes happen on the test goroutine, so
	// the closed-check cannot race a second close).
	t.Cleanup(func() {
		for _, ch := range []chan struct{}{releaseA, releaseB} {
			select {
			case <-ch: // already closed
			default:
				close(ch)
			}
		}
	})

	idxA := NewIndexer(IndexerConfig{
		Service:    svc,
		ChunkFn:    fakeChunkFunc,
		HashFn:     fakeHashFunc,
		OnProgress: parkHook(aParked, releaseA),
	})
	idxB := NewIndexer(IndexerConfig{
		Service:    svc,
		ChunkFn:    fakeChunkFunc,
		HashFn:     fakeHashFunc,
		OnProgress: parkHook(bParked, releaseB),
	})

	waitSignal := func(ch <-chan struct{}, what string) {
		t.Helper()
		select {
		case <-ch:
		case <-time.After(60 * time.Second):
			t.Fatalf("timed out waiting for %s", what)
		}
	}
	waitErr := func(ch <-chan error, what string) error {
		t.Helper()
		select {
		case err := <-ch:
			return err
		case <-time.After(60 * time.Second):
			t.Fatalf("timed out waiting for %s", what)
			return nil
		}
	}

	// (1) A attaches and parks inside its build callback, before its pass.
	errA := make(chan error, 1)
	go func() { errA <- idxA.AttachSnapshot(ctx, rootA, "") }()
	waitSignal(aParked, "builder A to park before its full pass")
	// Ground truth: A owns the working-store slot while it is parked.
	if got := svc.CurrentBranchName(); got != keyA {
		t.Fatalf("builder A parked with working store %q; want its own %q", got, keyA)
	}

	// (2) B attaches while A is parked: its SwitchBranch displaces A's
	// slot, then B parks inside its own build callback.
	errB := make(chan error, 1)
	go func() { errB <- idxB.AttachSnapshot(ctx, rootB, "") }()
	waitSignal(bParked, "the displacer to park before its full pass")

	// Mid-race ground truth: the slot is the displacer's and its store is
	// open — the state A's failing cleanup is about to be tempted to close.
	if got := svc.CurrentBranchName(); got != keyB {
		t.Fatalf("mid-race working store = %q; want the displacer's %q", got, keyB)
	}
	displacerCol := svc.GetCollection()
	if displacerCol == nil {
		t.Fatal("mid-race: the displacer's working store did not open")
	}

	// (3) Release A. Its pass runs (into the displacer's store), its
	// publish honestly fails, and its cleanup runs to completion BEFORE B
	// is released — the gate under test sits exactly here.
	close(releaseA)
	aErr := waitErr(errA, "the displaced builder's attach to return")
	if aErr == nil {
		t.Fatal("the displaced builder's attach succeeded; want the honest not-the-build-store failure")
	}
	if !strings.Contains(aErr.Error(), "not the corpus build store") {
		t.Fatalf("displaced attach error = %v; want the not-the-build-store failure", aErr)
	}
	// The ownership gate: A's completed cleanup left B's store alive. With
	// the pre-fix unconditional CloseWorkingStore this is where the
	// displacer's collection became nil — and B's next commit window took
	// the process down.
	if col := svc.GetCollection(); col == nil {
		t.Fatal("the displaced builder's cleanup tore down the displacer's working store — the 2026-11-05 silent-exit sequence")
	} else if col != displacerCol {
		t.Fatal("the displaced builder's cleanup replaced the displacer's collection")
	}

	// (4) Release B: its pass commits document windows against a LIVING
	// collection, publishes its corpus, and attaches its view. Pre-fix
	// this release ended in an unrecoverable SIGSEGV inside chromem's
	// commit goroutines (nil receiver); a barrier-only fix would end here
	// with the no-collection error instead.
	close(releaseB)
	if bErr := waitErr(errB, "the displacer's attach to return"); bErr != nil {
		t.Fatalf("the displacer's attach failed after the displaced builder's cleanup: %v", bErr)
	}

	// (5) The displacer's committed documents survived and are served:
	// reconcile its overlay over the published corpus and search.
	svc.SetWorkspaceRoot(rootB)
	if err := idxB.IndexIncremental(ctx, rootB); err != nil {
		t.Fatalf("displacer reconciliation: %v", err)
	}
	res, err := svc.HybridSearch(ctx, SearchOptions{
		Query:     "bravo displacer survivor",
		TopK:      5,
		Mode:      ModeHybrid,
		MustMatch: []string{"bravo displacer survivor"},
	})
	if err != nil {
		t.Fatalf("searching the displacer's corpus: %v", err)
	}
	if len(res) == 0 {
		t.Fatal("the displacer's documents are not served after the race; its corpus content was lost")
	}

	// (6) The displaced builder replays cleanly: a fresh attach rebuilds
	// its own corpus with no residue of the failed attempt.
	if err := idxA.AttachSnapshot(ctx, rootA, ""); err != nil {
		t.Fatalf("the displaced builder's replay attach failed: %v", err)
	}
}
