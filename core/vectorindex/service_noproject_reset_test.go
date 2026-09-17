package vectorindex

import (
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
	go func() { openErr <- svc.SetProject("proj-a", t.TempDir()) }()

	select {
	case <-openStarted:
	case <-time.After(10 * time.Second):
		close(releaseOpen)
		t.Fatal("SetProject never reached the persistent DB open")
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
		t.Fatalf("SetProject: %v", err)
	}

	// The open applied the queued reset as its final act (received from the
	// goroutine above, so every write below happens-before this read).
	svc.mu.RLock()
	currentID := svc.current.projectID
	svc.mu.RUnlock()
	if currentID != noProjectIDForTest {
		t.Fatalf("current project = %q after the queued reset, want %q", currentID, noProjectIDForTest)
	}
	if svc.pendingNoProjectReset.Load() != nil {
		t.Fatal("the queued reset must be consumed by the in-flight open")
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
