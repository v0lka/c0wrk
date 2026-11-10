package vectorindex

// Follow-up regressions for the code review of the ADR-120 change set:
// the findings fixed on top of the self-healing ladder.
//
//   - Finding 1: a FAILED repair must publish a terminal unavailable status
//     (with its cause and kind), never leave the last busy state standing.
//   - Finding 2: the no-quarantine suffix names the class honestly — only a
//     genuinely transient fault is labelled "transient fault".
//   - Finding 4: an expired wait for a concurrent corpus build is
//     CONGESTION (transient, nothing quarantined, the late publication
//     survives and is served), never a proven store defect.
//   - Finding 6: the pre-attach repair branches (legacy no-op, failed branch
//     detection) publish their terminal outcome too; a cancelled pass
//     publishes nothing.

import (
	"context"
	"errors"
	"fmt"
	"io/fs"
	"log/slog"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"
)

// withCorpusBuildWait swaps the attach drain window for the duration of one
// test and restores it afterwards. Sequential tests only: the package's
// t.Parallel tests resume after the sequential batch, so the window is never
// observed shrunk by an unrelated test.
func withCorpusBuildWait(t *testing.T, d time.Duration) {
	t.Helper()
	old := corpusBuildWait
	corpusBuildWait = d
	t.Cleanup(func() { corpusBuildWait = old })
}

// TestWaitBuildOutcome_TellsTimeoutFromSettled pins the drain primitive's
// contract the quarantine decision reads: a closed channel settles (and
// reports the disk publication verdict), an open channel expires with the
// bound. Confusing the two is what let a congestion timeout read as a store
// defect.
func TestWaitBuildOutcome_TellsTimeoutFromSettled(t *testing.T) {
	reg := NewCorpusRegistry(nil)
	t.Cleanup(reg.Close)
	storage := t.TempDir()
	const key = "snap_outcome00000001"
	const fp = "fp-outcome"
	cs := reg.storeFor(storage)

	if err := publishFixtureCorpus(storage, key, fp); err != nil {
		t.Fatalf("publishFixtureCorpus: %v", err)
	}
	settled := make(chan struct{})
	close(settled)

	if s, p := cs.waitBuildOutcome(settled, key, fp, 8, time.Minute); !s || !p {
		t.Errorf("settled+published = (%v, %v); want (true, true)", s, p)
	}
	if s, p := cs.waitBuildOutcome(settled, "snap_outcome00000002", fp, 8, time.Minute); !s || p {
		t.Errorf("settled+unpublished = (%v, %v); want (true, false)", s, p)
	}
	if s, p := cs.waitBuildOutcome(make(chan struct{}), key, fp, 8, 20*time.Millisecond); s || p {
		t.Errorf("expired = (%v, %v); want (false, false)", s, p)
	}
}

// TestCorpusBuildOvertime_Classification pins the taxonomy of the wait
// expiry through the wrapping the attach path applies: transient (retry when
// the machine is less loaded), never repairable, no quarantine scope — a
// congestion timeout proves nothing about the store's content.
func TestCorpusBuildOvertime_Classification(t *testing.T) {
	err := fmt.Errorf("ensuring corpus snap_x: %w",
		fmt.Errorf("%w: concurrent corpus build for snap_x did not settle within %s", errCorpusBuildOvertime, corpusBuildWait))
	if !Transient(err) {
		t.Error("an expired build wait is not transient")
	}
	if Repairable(err) {
		t.Error("an expired build wait must never classify as a proven store defect")
	}
	if got := QuarantineScopeOf(err); got != QuarantineNoStore {
		t.Errorf("QuarantineScopeOf = %s; want none (the publication stays)", got)
	}
	if got := ClassifyInitError(err); got != IndexErrorKindTransient {
		t.Errorf("ClassifyInitError = %q; want %q", got, IndexErrorKindTransient)
	}
}

// TestEnsureCorpus_WaitExpiryIsCongestionNotStoreDefect reproduces the
// congestion scenario end to end (finding 4): root A's corpus build is slow
// (gated inside its embedding pass), root B's attach of the SAME snapshot
// expires its drain window. B must fail closed with the congestion sentinel
// (transient, nothing quarantined — the review's "the publication stays"),
// and A's late publication must survive untouched and be SERVED by the next
// attach without re-embedding a single text.
func TestEnsureCorpus_WaitExpiryIsCongestionNotStoreDefect(t *testing.T) {
	withCorpusBuildWait(t, 250*time.Millisecond)
	files := map[string]string{"a.go": "package main\n\n// alpha marker\n"}
	h := newViewHarness(t, files)

	gate := &gatingEmbedder{release: make(chan struct{}), started: make(chan struct{})}
	svcA := h.newViewService(nil)
	svcA.embeddingFunc = gate.embed
	idxA := h.newIndexer(svcA, nil, nil)
	logs := &logCapture{}
	svcB := h.newViewService(nil)
	svcB.embeddingFunc = func(ctx context.Context, text string) ([]float32, error) {
		return fakeEmbeddingFunc()(ctx, text)
	}
	idxB := h.newIndexer(svcB, nil, nil)

	// A becomes the builder and parks inside its embedding pass.
	doneA := make(chan error, 1)
	go func() { doneA <- h.attach(svcA, idxA, h.rootA, "main") }()
	select {
	case <-gate.started:
	case <-time.After(20 * time.Second):
		t.Fatal("the builder never entered its embedding pass")
	}

	// B attaches the SAME snapshot (the linked worktree at the same commit)
	// and expires the shrunk drain window while A still holds the lease.
	doneB := make(chan error, 1)
	go func() { doneB <- h.attach(svcB, idxB, h.rootB, "topic") }()
	var errB error
	select {
	case errB = <-doneB:
	case <-time.After(20 * time.Second):
		t.Fatal("the congested attach never returned inside the shrunk drain window")
	}
	if errB == nil {
		t.Fatal("the congested attach succeeded inside the shrunk window")
	}
	if !errors.Is(errB, errCorpusBuildOvertime) {
		t.Fatalf("the wait expiry = %v; want the congestion sentinel", errB)
	}
	if got := QuarantineScopeOf(errB); got != QuarantineNoStore {
		t.Errorf("QuarantineScopeOf(wait expiry) = %s; want none — congestion is not a store verdict", got)
	}
	if n := logs.count("quarantin"); n != 0 {
		t.Errorf("the wait expiry quarantined a store (%d log records); want 0", n)
	}

	// A finishes AFTER B gave up: its publication is healthy by construction.
	close(gate.release)
	select {
	case errA := <-doneA:
		if errA != nil {
			t.Fatalf("the builder's attach failed: %v", errA)
		}
	case <-time.After(20 * time.Second):
		t.Fatal("the builder never settled after its release")
	}

	// The next attach of the same snapshot SERVES the late publication: not
	// one text re-embedded, nothing rebuilt, nothing destroyed.
	counterC := &embedCounter{}
	svcC := h.newViewService(counterC)
	if err := h.attach(svcC, h.newIndexer(svcC, nil, nil), h.rootB, "topic"); err != nil {
		t.Fatalf("attach after the congestion settled: %v", err)
	}
	if n := counterC.count(); n != 0 {
		t.Errorf("the late publication was not served: %d texts re-embedded", n)
	}
}

// TestAttachSnapshot_NoStoreSuffixNamesTheClass pins the suffix honesty
// (finding 2): a degenerate workspace (no indexable files) is a settled,
// non-transient no-store outcome, so its suffix is the class-neutral
// "nothing was quarantined" form — the word "transient" must stay reserved
// for faults the status renders beside error_kind: transient.
func TestAttachSnapshot_NoStoreSuffixNamesTheClass(t *testing.T) {
	storage := t.TempDir()
	reg := NewCorpusRegistry(nil)
	t.Cleanup(reg.Close)
	svc, err := NewService(ServiceConfig{
		EmbeddingFunc:      fakeEmbeddingFunc(),
		Corpus:             reg,
		EmbeddingDimension: 8,
	})
	if err != nil {
		t.Fatalf("NewService: %v", err)
	}
	t.Cleanup(func() { _ = svc.Close() })
	if err := svc.SetProject("suffix", storage); err != nil {
		t.Fatalf("SetProject: %v", err)
	}
	ws := t.TempDir() // a real, empty tree: clean walk, nothing indexable
	idx := NewIndexer(IndexerConfig{Service: svc, ChunkFn: fakeChunkFunc, HashFn: fakeHashFunc})

	attachErr := idx.AttachSnapshot(context.Background(), ws, "")
	if !errors.Is(attachErr, ErrCorpusNoIndexableFiles) {
		t.Fatalf("attach error = %v; want ErrCorpusNoIndexableFiles", attachErr)
	}
	if Transient(attachErr) {
		t.Error("a degenerate workspace is not a transient fault")
	}
	for _, want := range []string{
		"no indexable files",
		"(nothing was quarantined: the publication stays intact)",
	} {
		if !strings.Contains(attachErr.Error(), want) {
			t.Errorf("attach error %v does not carry %q", attachErr, want)
		}
	}
	if strings.Contains(attachErr.Error(), "transient fault") {
		t.Errorf("a settled no-store outcome is labelled transient: %v", attachErr)
	}
}

// TestReindex_RepairFailurePublishesTerminalUnavailable pins finding 1: a
// repair whose re-attach fails must leave the manager's status in the
// terminal unavailable state with its cause and kind, and surface the
// failure through the project callbacks — the last busy progress state the
// attach emitted (loading / migration-rebuild) must not survive the failed
// repair, or the UI spins on it forever with the reindex action disabled.
func TestReindex_RepairFailurePublishesTerminalUnavailable(t *testing.T) {
	f := newRepairFixture(t)
	logs := &logCapture{}
	svc := f.newService(nil, logs)
	idx := f.newIndexer(svc, nil, logs)
	if err := f.attachView(svc, idx); err != nil {
		t.Fatalf("attachView: %v", err)
	}
	key := f.attachedKey(svc)

	var failures []error
	mgr := &Manager{
		service:       svc,
		logger:        slog.New(logs),
		chunkFn:       fakeChunkFunc,
		hashFn:        fakeHashFunc,
		workspacePath: f.root,
		indexer:       idx,
	}
	mgr.mu.Lock()
	mgr.projectCallbacks = ProjectCallbacks{OnFailure: func(err error) { failures = append(failures, err) }}
	mgr.mu.Unlock()
	t.Cleanup(mgr.Shutdown)

	// The incident state: no live collection, a corpus that reads
	// unpublished (its lexical leg is gone), and a rebuild that keeps hitting
	// a permission fault — transient, so nothing is quarantined and the
	// repair can only fail.
	f.detachCorpusView(svc)
	for _, leg := range corpusLexicalLegs(t, f.storage, key) {
		if err := os.RemoveAll(leg); err != nil {
			t.Fatalf("removing lexical leg %s: %v", leg, err)
		}
	}
	seam := installRepairDBSeam(t)
	corpusRoot := branchRootPath(f.storage, key)
	seam.setFail(func(path string) error {
		if path != filepath.Clean(corpusRoot) {
			return nil
		}
		return fmt.Errorf("opening the working store at %s: %w", path,
			&fs.PathError{Op: "open", Path: path, Err: fs.ErrPermission})
	})

	if err := mgr.Reindex(context.Background()); err != nil {
		t.Fatalf("Reindex: %v", err)
	}
	mgr.indexingWG.Wait()

	st := mgr.GetIndexStatus()
	if st.State != IndexStateUnavailable {
		t.Fatalf("status state after a failed repair = %q; want unavailable (the UI must not spin on the last busy state)", st.State)
	}
	if st.LastError == "" {
		t.Error("the terminal status carries no cause")
	}
	if st.ErrorKind != IndexErrorKindTransient {
		t.Errorf("status kind = %q; want %q", st.ErrorKind, IndexErrorKindTransient)
	}
	if len(failures) != 1 {
		t.Fatalf("OnFailure called %d time(s); want exactly 1", len(failures))
	}
	if !errors.Is(failures[0], fs.ErrPermission) {
		t.Errorf("OnFailure error = %v; want it to carry the permission fault", failures[0])
	}
}

// TestReindex_RepairBranchDetectFailurePublishesUnavailable pins finding 6's
// pre-attach branch: a workspace whose branch cannot be detected must
// publish the terminal unavailable status (cause + OnFailure), not return
// silently with the frontend latch stuck on "Repairing index…".
func TestReindex_RepairBranchDetectFailurePublishesUnavailable(t *testing.T) {
	f := newRepairFixture(t)
	logs := &logCapture{}
	svc := f.newService(nil, logs)
	idx := f.newIndexer(svc, nil, logs)

	notADir := filepath.Join(t.TempDir(), "workspace.txt")
	if err := os.WriteFile(notADir, []byte("a file, not a workspace"), 0o644); err != nil {
		t.Fatalf("writing the bogus workspace path: %v", err)
	}
	var failures int
	mgr := &Manager{
		service:       svc,
		logger:        slog.New(logs),
		chunkFn:       fakeChunkFunc,
		hashFn:        fakeHashFunc,
		workspacePath: notADir,
		indexer:       idx,
	}
	mgr.mu.Lock()
	mgr.projectCallbacks = ProjectCallbacks{OnFailure: func(error) { failures++ }}
	mgr.mu.Unlock()
	t.Cleanup(mgr.Shutdown)

	if err := mgr.Reindex(context.Background()); err != nil {
		t.Fatalf("Reindex: %v", err)
	}
	mgr.indexingWG.Wait()

	st := mgr.GetIndexStatus()
	if st.State != IndexStateUnavailable {
		t.Fatalf("status state = %q; want unavailable", st.State)
	}
	if !strings.Contains(st.LastError, "detecting the branch for the repair") {
		t.Errorf("status cause = %q; want it to name the failed branch detection", st.LastError)
	}
	if failures != 1 {
		t.Errorf("OnFailure called %d time(s); want 1", failures)
	}
}

// TestReindex_LegacyModeNoopPublishesUnavailable pins finding 6's legacy
// branch: in corpus-disabled (legacy) mode the repair genuinely cannot help,
// and the no-op must SAY so through the terminal status instead of leaving
// the request pending on the frontend.
func TestReindex_LegacyModeNoopPublishesUnavailable(t *testing.T) {
	f := newRepairFixture(t)
	logs := &logCapture{}
	svc, err := NewService(ServiceConfig{EmbeddingFunc: fakeEmbeddingFunc()})
	if err != nil {
		t.Fatalf("NewService: %v", err)
	}
	t.Cleanup(func() { _ = svc.Close() })
	if svc.CorpusEnabled() {
		t.Fatal("a service without a registry must run in legacy mode")
	}
	idx := NewIndexer(IndexerConfig{Service: svc, ChunkFn: fakeChunkFunc, HashFn: fakeHashFunc})

	var failures int
	mgr := &Manager{
		service:       svc,
		logger:        slog.New(logs),
		chunkFn:       fakeChunkFunc,
		hashFn:        fakeHashFunc,
		workspacePath: f.root,
		indexer:       idx,
	}
	mgr.mu.Lock()
	mgr.projectCallbacks = ProjectCallbacks{OnFailure: func(error) { failures++ }}
	mgr.mu.Unlock()
	t.Cleanup(mgr.Shutdown)

	if err := mgr.Reindex(context.Background()); err != nil {
		t.Fatalf("Reindex: %v", err)
	}
	mgr.indexingWG.Wait()

	st := mgr.GetIndexStatus()
	if st.State != IndexStateUnavailable {
		t.Fatalf("status state = %q; want unavailable", st.State)
	}
	if !strings.Contains(st.LastError, "legacy mode has no corpus to rebuild") {
		t.Errorf("status cause = %q; want the legacy no-op reason", st.LastError)
	}
	if st.ErrorKind != IndexErrorKindFatal {
		t.Errorf("status kind = %q; want %q (a rebuild promises nothing here)", st.ErrorKind, IndexErrorKindFatal)
	}
	if failures != 1 {
		t.Errorf("OnFailure called %d time(s); want 1", failures)
	}
}

// TestReindex_CancelledRepairPublishesNothing pins the silence half of the
// repair notification: a cancelled pass (a newer reindex, a project switch,
// a shutdown) is never a verdict, so it publishes nothing — the newer pass
// owns the status.
func TestReindex_CancelledRepairPublishesNothing(t *testing.T) {
	f := newRepairFixture(t)
	logs := &logCapture{}
	svc := f.newService(nil, logs)
	idx := f.newIndexer(svc, nil, logs)

	var failures int
	mgr := &Manager{
		service:       svc,
		logger:        slog.New(logs),
		chunkFn:       fakeChunkFunc,
		hashFn:        fakeHashFunc,
		workspacePath: f.root,
		indexer:       idx,
	}
	mgr.mu.Lock()
	mgr.projectCallbacks = ProjectCallbacks{OnFailure: func(error) { failures++ }}
	mgr.mu.Unlock()
	t.Cleanup(mgr.Shutdown)

	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	if err := mgr.Reindex(ctx); err != nil {
		t.Fatalf("Reindex: %v", err)
	}
	mgr.indexingWG.Wait()

	st := mgr.GetIndexStatus()
	if st.State == IndexStateUnavailable {
		t.Fatalf("a cancelled repair published a terminal %q status with cause %q", st.State, st.LastError)
	}
	if st.LastError != "" {
		t.Errorf("a cancelled repair recorded cause %q; want none", st.LastError)
	}
	if failures != 0 {
		t.Errorf("OnFailure called %d time(s) for a cancelled pass; want 0", failures)
	}
}

// TestQuarantineCorpusStore_ConcurrentPublicationAborts pins the FIRST abort
// path of the quarantine's concurrent-build branch (finding 4): a concurrent
// build that settles while the quarantine drains, publishing a compatible
// corpus, must ABORT the quarantine — that publication is a fresh full
// rebuild, healthy by construction, and the congestion that led here proved
// nothing about it. The quarantine owns no lease on this path (the builder
// settles its own), but the test still asserts the lease is releasable
// afterwards: every nil-returning exit must leave the snapshot buildable.
//
// The fixture closes the builder's done channel BY HAND while its map entry
// stays put, so the goroutine's first beginBuild deterministically takes the
// concurrent path no matter how it is scheduled against the test (a real
// endBuild races the goroutine's beginBuild: losing that race flips the
// quarantine onto the direct path, which legitimately removes the store).
// The hand-closed channel also means the entry cannot be settled through
// endBuild (double close) — it is dropped from the map by hand instead.
func TestQuarantineCorpusStore_ConcurrentPublicationAborts(t *testing.T) {
	reg := NewCorpusRegistry(nil)
	t.Cleanup(reg.Close)
	storage := t.TempDir()
	const key = "snap_qabort000000001"
	const fp = "fp-qabort"
	cs := reg.storeFor(storage)

	// The "builder": a full rebuild that publishes before settling. The
	// publication is on disk while the lease is held, so the quarantine's
	// drain observes it the moment the builder settles.
	if err := publishFixtureCorpus(storage, key, fp); err != nil {
		t.Fatalf("publishFixtureCorpus: %v", err)
	}
	started, _ := cs.beginBuild(key)
	if !started {
		t.Fatal("the fixture could not acquire the build lease")
	}
	cs.mu.Lock()
	buildLease := cs.builds[key]
	cs.mu.Unlock()
	close(buildLease.done) // the builder settles — its publication stays
	done := make(chan error, 1)
	go func() { done <- cs.quarantineCorpusStore(context.Background(), key, fp, 8) }()

	select {
	case err := <-done:
		if err != nil {
			t.Fatalf("quarantine = %v; want the concurrent publication to abort it", err)
		}
	case <-time.After(20 * time.Second):
		t.Fatal("the quarantine never settled after the concurrent publication")
	}
	if !cs.publishedCompatible(key, fp, 8) {
		t.Error("the aborted quarantine destroyed the concurrent publication")
	}
	// Drop the fixture's lease (by hand — see above) and prove the snapshot
	// stays buildable: no abort path may leave a lease behind.
	cs.mu.Lock()
	delete(cs.builds, key)
	cs.mu.Unlock()
	if again, _ := cs.beginBuild(key); !again {
		t.Error("the build lease leaked across the aborted quarantine")
	} else {
		cs.endBuild(key, nil)
	}
}

// TestQuarantineCorpusStore_ExpiredDrainStillHeldIsBusy pins the refusal
// path: a drain that expires while the concurrent build still HOLDS the
// lease must fail with errCorpusBuildBusy — exactly one writer per store,
// the quarantine never waits twice for the same builder — and must touch
// nothing on disk.
func TestQuarantineCorpusStore_ExpiredDrainStillHeldIsBusy(t *testing.T) {
	withCorpusBuildWait(t, 50*time.Millisecond)
	reg := NewCorpusRegistry(nil)
	t.Cleanup(reg.Close)
	storage := t.TempDir()
	const key = "snap_qbusy0000000001"
	const fp = "fp-qbusy"
	cs := reg.storeFor(storage)

	started, _ := cs.beginBuild(key)
	if !started {
		t.Fatal("the fixture could not acquire the build lease")
	}
	done := make(chan error, 1)
	go func() { done <- cs.quarantineCorpusStore(context.Background(), key, fp, 8) }()

	select {
	case err := <-done:
		if !errors.Is(err, errCorpusBuildBusy) {
			t.Fatalf("quarantine = %v; want errCorpusBuildBusy (the lease is still held)", err)
		}
	case <-time.After(20 * time.Second):
		t.Fatal("the quarantine never settled after the expired drain")
	}
	if cs.publishedCompatible(key, fp, 8) {
		t.Error("the refused quarantine published something")
	}
	// The holder settles; the snapshot must be buildable again (no leak).
	cs.endBuild(key, nil)
	if again, _ := cs.beginBuild(key); !again {
		t.Error("the build lease leaked across the refused quarantine")
	} else {
		cs.endBuild(key, nil)
	}
}

// TestQuarantineCorpusStore_GapPublicationSkipsAndReleasesLease pins the
// defense-in-depth skip and its lease discipline (the review's lease-leak
// finding on top of finding 4): a publication that lands in the gap BETWEEN
// the drain's read and the retried lease acquire aborts the quarantine (the
// corpus is a fresh full rebuild, healthy by construction), and the skip —
// which by then OWNS the reacquired lease — must settle it: a leaked lease
// wedges the snapshot for the process lifetime (every later attacher drains
// the full corpusBuildWait against a channel nobody will close, and the
// reachability sweep can never reclaim the corpus).
//
// The gap is microseconds of wall time, so the test drives it
// deterministically through the readCorpusManifest seam (the package's
// newPersistentDB pattern) plus a hand-closed done channel: the channel is
// closed with the lease's map entry still in place, so the goroutine's
// first beginBuild always takes the concurrent path (a real endBuild races
// that beginBuild); the drain's first read parks inside the seam, the test
// then drops the map entry (the builder's half of the settle — the channel
// half already happened) and lands the publication; the goroutine's retried
// beginBuild acquires a FRESH lease and its re-check sees the publication —
// the exact interleaving the branch exists for, without a race.
func TestQuarantineCorpusStore_GapPublicationSkipsAndReleasesLease(t *testing.T) {
	reg := NewCorpusRegistry(nil)
	t.Cleanup(reg.Close)
	storage := t.TempDir()
	const key = "snap_qgap0000000001"
	const fp = "fp-qgap"
	cs := reg.storeFor(storage)

	// The "builder" holds the lease over an unpublished corpus...
	started, _ := cs.beginBuild(key)
	if !started {
		t.Fatal("the fixture could not acquire the build lease")
	}

	snapRoot := branchRootPath(storage, key)
	orig := readCorpusManifest
	firstRead := make(chan struct{})
	unparked := make(chan struct{})
	var unparkOnce sync.Once
	unpark := func() { unparkOnce.Do(func() { close(unparked) }) }
	var reads atomic.Int32
	readCorpusManifest = func(branchRoot string) *CorpusManifest {
		if branchRoot != snapRoot {
			return orig(branchRoot)
		}
		// The FIRST read of this snapshot is the drain's: report the gap's
		// starting state (unpublished) and hold the goroutine there while
		// the test drops the lease and lands the publication.
		if reads.Add(1) == 1 {
			close(firstRead)
			<-unparked
			return nil
		}
		return orig(branchRoot)
	}
	// LIFO: the unpark runs before the restore, so a parked goroutine (a
	// failing watchdog path) never outlives the seam.
	t.Cleanup(func() { readCorpusManifest = orig })
	t.Cleanup(unpark)

	cs.mu.Lock()
	buildLease := cs.builds[key]
	cs.mu.Unlock()
	close(buildLease.done) // the builder settles — the publication has NOT landed yet
	done := make(chan error, 1)
	go func() { done <- cs.quarantineCorpusStore(context.Background(), key, fp, 8) }()

	// The drain parks inside its first publication read — proof the
	// goroutine is past its first beginBuild (the concurrent path) and
	// inside the gap.
	select {
	case <-firstRead:
	case <-time.After(20 * time.Second):
		t.Fatal("the quarantine never reached its first publication read")
	}

	// The builder's settle completes (the map half — the channel half is
	// already done) and the publication lands IN THE GAP, between the
	// drain's read and the retried lease's re-check; only then is the
	// goroutine let go.
	cs.mu.Lock()
	delete(cs.builds, key)
	cs.mu.Unlock()
	if err := publishFixtureCorpus(storage, key, fp); err != nil {
		t.Fatalf("publishFixtureCorpus: %v", err)
	}
	unpark()

	select {
	case err := <-done:
		if err != nil {
			t.Fatalf("quarantine = %v; want the gap publication to skip it", err)
		}
	case <-time.After(20 * time.Second):
		t.Fatal("the quarantine never settled after the gap publication")
	}
	if !cs.publishedCompatible(key, fp, 8) {
		t.Error("the skipped quarantine destroyed the gap publication")
	}
	// THE lease-leak regression guard: the skip reacquired the lease, so it
	// must have settled it — with the leak, this acquire would report the
	// stale lease and the snapshot would stay wedged for the process
	// lifetime.
	if again, _ := cs.beginBuild(key); !again {
		t.Error("the skip leaked the reacquired build lease — the snapshot is wedged for the process lifetime")
	} else {
		cs.endBuild(key, nil)
	}
}
