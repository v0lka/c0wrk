package vectorindex

import (
	"context"
	"testing"
	"time"

	chromem "github.com/philippgille/chromem-go"
)

const noProjectIDForTest = "__no_project__"

// TestResetForNoProject_DoesNotBlockOnInFlightOpen pins the fix for the
// "CHAT/CODE toggle does nothing for minutes" failure.
//
// A No Project switch resets the service, which needs the write lock —
// and SetProject holds that lock for the entire persistent DB open (chromem
// gob-decodes every document of every branch collection: minutes on a large
// index, ~6 GB / ~1M documents in the field). Blocking on it held the
// backend's project-switch lock for that whole window, so every toggle failed
// on its bounded acquire. The reset must therefore return AT ONCE and let the
// in-flight open apply it as its last act.
func TestResetForNoProject_DoesNotBlockOnInFlightOpen(t *testing.T) {
	origNewPersistentDB := newPersistentDB
	defer func() { newPersistentDB = origNewPersistentDB }()

	openStarted := make(chan struct{})
	releaseOpen := make(chan struct{})
	newPersistentDB = func(path string, compress bool) (*chromem.DB, error) {
		close(openStarted)
		<-releaseOpen
		return origNewPersistentDB(path, compress)
	}

	svc, err := NewService(ServiceConfig{EmbeddingFunc: fakeEmbeddingFunc()})
	if err != nil {
		t.Fatalf("NewService: %v", err)
	}
	t.Cleanup(func() { _ = svc.Close() })

	openErr := make(chan error, 1)
	// ADR-064: the (heavy) persistent DB open now happens in SwitchBranch,
	// which holds the write lock for its whole duration; SetProject only
	// prepares the layout.
	go func() {
		if err := svc.SetProject("proj-a", t.TempDir()); err != nil {
			openErr <- err
			return
		}
		openErr <- svc.SwitchBranch(context.Background(), "main")
	}()

	select {
	case <-openStarted:
	case <-time.After(10 * time.Second):
		close(releaseOpen)
		t.Fatal("the open never reached chromem.NewPersistentDB")
	}

	// Contended path: the reset must not wait for the open to finish.
	start := time.Now()
	svc.ResetForNoProject(noProjectIDForTest)
	elapsed := time.Since(start)
	if elapsed > time.Second {
		close(releaseOpen)
		t.Fatalf("ResetForNoProject blocked for %v while an open held the lock; want an immediate return", elapsed)
	}
	if svc.pendingNoProjectReset.Load() == nil {
		close(releaseOpen)
		t.Fatal("expected the No Project reset to be queued while the open holds the lock")
	}

	close(releaseOpen)
	if err := <-openErr; err != nil {
		t.Fatalf("open: %v", err)
	}

	// The reset is delivered by the background applier (kickNoProjectResetApplier)
	// once the lock holder releases — no longer "by the open as its last act",
	// because ADR-064 moved the open out of SetProject. Poll for it so the
	// assertion is not racy.
	deadline := time.Now().Add(3 * time.Second)
	var currentID string
	for {
		svc.mu.RLock()
		currentID = svc.current.projectID
		svc.mu.RUnlock()
		if currentID == noProjectIDForTest {
			break
		}
		if time.Now().After(deadline) {
			t.Fatalf("current project = %q after the queued reset, want %q", currentID, noProjectIDForTest)
		}
		time.Sleep(5 * time.Millisecond)
	}
	if svc.pendingNoProjectReset.Load() != nil {
		t.Fatal("the queued reset must be consumed")
	}
	if svc.GetCollection() != nil {
		t.Fatal("the No Project state must carry no collection")
	}
	if svc.IsReady() {
		t.Fatal("the No Project state must not be ready")
	}
}

// TestResetForNoProject_ImmediateWhenIdle pins the uncontended path: with no
// open in flight the reset applies synchronously and installs the empty
// in-memory No Project state (readiness dropped, no collection).
func TestResetForNoProject_ImmediateWhenIdle(t *testing.T) {
	svc, err := NewService(ServiceConfig{EmbeddingFunc: fakeEmbeddingFunc()})
	if err != nil {
		t.Fatalf("NewService: %v", err)
	}
	t.Cleanup(func() { _ = svc.Close() })

	if err := svc.SetProject("proj-a", t.TempDir()); err != nil {
		t.Fatalf("SetProject: %v", err)
	}
	svc.SetReady(true)

	svc.ResetForNoProject(noProjectIDForTest)

	if svc.pendingNoProjectReset.Load() != nil {
		t.Fatal("nothing should be queued when no open is in flight")
	}
	if got := svc.current.projectID; got != noProjectIDForTest {
		t.Fatalf("current project = %q, want %q", got, noProjectIDForTest)
	}
	if svc.GetCollection() != nil {
		t.Fatal("the No Project state must carry no collection")
	}
	if svc.IsReady() {
		t.Fatal("the No Project state must not be ready")
	}
}

// TestResetForNoProject_QueuedBehindNonOpenHolderEventuallyLands pins the
// second failure mode of the queued reset: a request recorded while the lock
// is held by something OTHER than a SetProject open (an indexing pass's
// AddDocuments window, ValidateCollection, a Browse) must still LAND once
// that holder releases — previously nothing ever drained the queue, so the
// outgoing project's collection stayed live forever (and the next unrelated
// SetProject applied the stale reset, wiping itself).
func TestResetForNoProject_QueuedBehindNonOpenHolderEventuallyLands(t *testing.T) {
	svc, err := NewService(ServiceConfig{EmbeddingFunc: fakeEmbeddingFunc()})
	if err != nil {
		t.Fatalf("NewService: %v", err)
	}
	t.Cleanup(func() { _ = svc.Close() })

	if err := svc.SetProject("proj-a", t.TempDir()); err != nil {
		t.Fatalf("SetProject: %v", err)
	}

	// Simulate a non-SetProject write-lock holder (an indexing pass).
	svc.AcquireWriteLock()
	svc.ResetForNoProject(noProjectIDForTest)
	if got := svc.current.projectID; got != "proj-a" {
		svc.ReleaseWriteLock()
		t.Fatalf("current project = %q while the holder still owns the lock, want proj-a (the reset must be deferred)", got)
	}
	svc.ReleaseWriteLock()

	// The background applier must land the reset shortly after the release.
	deadline := time.Now().Add(10 * time.Second)
	for {
		svc.mu.RLock()
		currentID := svc.current.projectID
		svc.mu.RUnlock()
		if currentID == noProjectIDForTest {
			break
		}
		if time.Now().After(deadline) {
			t.Fatal("queued No Project reset never landed after the non-open holder released the lock")
		}
		time.Sleep(2 * time.Millisecond)
	}
	if svc.pendingNoProjectReset.Load() != nil {
		t.Fatal("the queued reset must be consumed once it lands")
	}
}

// TestResetForNoProject_QueuedBehindReadHolderEventuallyLands is the read-lock
// variant of the same scenario: TryLock also fails while a Browse or
// ValidateCollection holds s.mu with an RLock, so the reset queues there too
// and must land when the reader releases.
func TestResetForNoProject_QueuedBehindReadHolderEventuallyLands(t *testing.T) {
	svc, err := NewService(ServiceConfig{EmbeddingFunc: fakeEmbeddingFunc()})
	if err != nil {
		t.Fatalf("NewService: %v", err)
	}
	t.Cleanup(func() { _ = svc.Close() })

	if err := svc.SetProject("proj-a", t.TempDir()); err != nil {
		t.Fatalf("SetProject: %v", err)
	}

	svc.mu.RLock()
	svc.ResetForNoProject(noProjectIDForTest)
	svc.mu.RUnlock()

	deadline := time.Now().Add(10 * time.Second)
	for {
		svc.mu.RLock()
		currentID := svc.current.projectID
		svc.mu.RUnlock()
		if currentID == noProjectIDForTest {
			return
		}
		if time.Now().After(deadline) {
			t.Fatal("queued No Project reset never landed after the read holder released the lock")
		}
		time.Sleep(2 * time.Millisecond)
	}
}

// TestResetForNoProject_QueuedResetDoesNotWipeSubsequentlyOpenedProject pins
// the primary failure mode: a reset queued behind a non-open holder must
// NEVER wipe a project opened after the reset was requested. The newer
// SetProject supersedes the queued request (queue voided at the start of its
// critical section; generation guard discards any late lock-free store), so
// project B survives both appliers — the background goroutine and
// SetProject's own deferred apply.
func TestResetForNoProject_QueuedResetDoesNotWipeSubsequentlyOpenedProject(t *testing.T) {
	svc, err := NewService(ServiceConfig{EmbeddingFunc: fakeEmbeddingFunc()})
	if err != nil {
		t.Fatalf("NewService: %v", err)
	}
	t.Cleanup(func() { _ = svc.Close() })

	if err := svc.SetProject("proj-a", t.TempDir()); err != nil {
		t.Fatalf("SetProject: %v", err)
	}

	// Queue a reset behind a NON-SetProject write holder, then release.
	svc.AcquireWriteLock()
	svc.ResetForNoProject(noProjectIDForTest)
	if svc.pendingNoProjectReset.Load() == nil {
		svc.ReleaseWriteLock()
		t.Fatal("expected the reset to be queued while a non-open holder owns the lock")
	}
	svc.ReleaseWriteLock()

	// The user then opens project B: the newer open supersedes the reset.
	staleGen := svc.openGen.Load()
	if err := svc.SetProject("proj-b", t.TempDir()); err != nil {
		t.Fatalf("SetProject(proj-b): %v", err)
	}

	// Give the background applier every opportunity to (incorrectly) apply
	// the superseded request before asserting.
	deadline := time.Now().Add(10 * time.Second)
	for svc.pendingNoProjectReset.Load() != nil && time.Now().Before(deadline) {
		time.Sleep(2 * time.Millisecond)
	}

	svc.mu.RLock()
	currentID := svc.current.projectID
	currentPath := svc.current.projectPath
	svc.mu.RUnlock()
	if currentID != "proj-b" || currentPath == "" {
		t.Fatalf("current project = %q (path %q) after opening proj-b; a queued reset predating the open must never wipe it", currentID, currentPath)
	}

	// Deterministic check of the generation guard itself: forge a request
	// stamped with the pre-open generation — exactly what a lock-free store
	// racing past SetProject's queue-clear would leave behind — and drain it.
	// It must be discarded, not applied.
	svc.pendingNoProjectReset.Store(&pendingNoProjectResetRequest{
		projectID: noProjectIDForTest,
		openGen:   staleGen,
	})
	svc.mu.Lock()
	svc.applyPendingNoProjectResetLocked()
	svc.mu.Unlock()

	svc.mu.RLock()
	currentID = svc.current.projectID
	svc.mu.RUnlock()
	if currentID != "proj-b" {
		t.Fatalf("current project = %q after draining a stale-generation request, want proj-b", currentID)
	}
	if svc.pendingNoProjectReset.Load() != nil {
		t.Fatal("the stale request must be consumed (discarded) by the applier")
	}
}

// TestResetForNoProject_QueuedDuringOpenWindowLands pins the residual race
// found in review: a No Project reset requested in the window AFTER a
// SetProject stamped its generation but BEFORE that open acquired the lock
// carries the open's OWN generation, so the open must neither drop it at the
// start of its critical section nor let the background applier apply it
// before the open installs — in both lock-acquisition orderings the reset
// wins and the service ends in the No Project state (the user's CHAT toggle
// was the last action).
func TestResetForNoProject_QueuedDuringOpenWindowLands(t *testing.T) {
	svc, err := NewService(ServiceConfig{EmbeddingFunc: fakeEmbeddingFunc()})
	if err != nil {
		t.Fatalf("NewService: %v", err)
	}
	t.Cleanup(func() { _ = svc.Close() })

	if err := svc.SetProject("proj-a", t.TempDir()); err != nil {
		t.Fatalf("SetProject(proj-a): %v", err)
	}

	// A non-open write holder (e.g. an indexing pass) owns the lock;
	// project B's open stamps its generation and then blocks on the lock.
	svc.AcquireWriteLock()
	genBefore := svc.openGen.Load()
	openErr := make(chan error, 1)
	go func() { openErr <- svc.SetProject("proj-b", t.TempDir()) }()
	deadline := time.Now().Add(10 * time.Second)
	for svc.openGen.Load() == genBefore {
		if time.Now().After(deadline) {
			svc.ReleaseWriteLock()
			t.Fatal("SetProject(proj-b) never stamped its generation")
		}
		time.Sleep(time.Millisecond)
	}

	// The user toggles CHAT while B's open is still waiting for the lock:
	// the queued request is stamped with B's own generation.
	svc.ResetForNoProject(noProjectIDForTest)
	if svc.pendingNoProjectReset.Load() == nil {
		svc.ReleaseWriteLock()
		t.Fatal("expected the reset to be queued while the holder owns the lock")
	}

	// Whichever of the background applier and B's open wins the lock, the
	// reset must land: the applier defers to the in-flight open
	// (openPending > 0), and the open applies the same-generation request as
	// its last act — AFTER installing B.
	svc.ReleaseWriteLock()
	if err := <-openErr; err != nil {
		t.Fatalf("SetProject(proj-b): %v", err)
	}

	deadline = time.Now().Add(10 * time.Second)
	for {
		svc.mu.RLock()
		currentID := svc.current.projectID
		svc.mu.RUnlock()
		if currentID == noProjectIDForTest {
			break
		}
		if time.Now().After(deadline) {
			t.Fatalf("current project = %q after a reset queued during proj-b's open window; want %q (the last user action was the CHAT toggle)", currentID, noProjectIDForTest)
		}
		time.Sleep(2 * time.Millisecond)
	}
	if svc.pendingNoProjectReset.Load() != nil {
		t.Fatal("the queued reset must be consumed once it lands")
	}
}
