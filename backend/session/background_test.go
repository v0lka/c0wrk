package session

import (
	"io"
	"log/slog"
	"sync/atomic"
	"testing"
	"time"
)

// newDiscardLogger returns a logger that swallows every record, for tests that
// only need the nil-safe logging path to be wired.
func newDiscardLogger() *slog.Logger {
	return slog.New(slog.NewTextHandler(io.Discard, nil))
}

// TestBackgroundTracker_CloseAndWaitJoinsTrackedGoroutines pins the core
// contract Shutdown relies on: closeAndWait does not report success while a
// tracked goroutine is still running, and only returns true once the last one
// has drained.
func TestBackgroundTracker_CloseAndWaitJoinsTrackedGoroutines(t *testing.T) {
	tr := newBackgroundTracker()

	started := make(chan struct{})
	release := make(chan struct{})
	var finished atomic.Bool
	if !tr.spawn(func() {
		close(started)
		<-release
		finished.Store(true)
	}) {
		t.Fatal("spawn() = false before close; want true")
	}
	<-started

	// While the goroutine is still blocked, the wait must time out rather than
	// pretend everything finished (a bounded budget only makes this more
	// certain on a slow machine).
	if tr.closeAndWait(20 * time.Millisecond) {
		t.Fatal("closeAndWait() = true while a tracked goroutine was still running; want false")
	}
	if finished.Load() {
		t.Fatal("tracked goroutine finished while it was still blocked on release")
	}

	close(release)
	if !tr.closeAndWait(5 * time.Second) {
		t.Fatal("closeAndWait() = false after the tracked goroutine finished; want true")
	}
	if !finished.Load() {
		t.Fatal("closeAndWait() reported success before the tracked goroutine finished")
	}

	// Once closed, the tracker refuses new work instead of leaking it.
	if tr.spawn(func() {}) {
		t.Fatal("spawn() after close = true; want false")
	}
	// A second close on a drained tracker is a no-op that returns immediately.
	if !tr.closeAndWait(time.Second) {
		t.Fatal("second closeAndWait() = false on a drained tracker; want true")
	}
}

// TestBackgroundTracker_JoinsAllConcurrentGoroutines verifies that a close
// waits for every concurrently spawned goroutine, not merely the first one.
func TestBackgroundTracker_JoinsAllConcurrentGoroutines(t *testing.T) {
	tr := newBackgroundTracker()

	const n = 50
	var completed atomic.Int64
	for i := 0; i < n; i++ {
		if !tr.spawn(func() { completed.Add(1) }) {
			t.Fatalf("spawn() #%d = false before close; want true", i)
		}
	}
	if !tr.closeAndWait(10 * time.Second) {
		t.Fatal("closeAndWait() = false; want true")
	}
	if got := completed.Load(); got != n {
		t.Fatalf("completed goroutines = %d; want %d", got, n)
	}
}

// TestManager_Shutdown_JoinsTrackedBackgroundGoroutines is the regression test
// for the ENOTEMPTY teardown flake: a manager-owned background goroutine must
// be joined by Shutdown, so it cannot touch the filesystem after the backend
// (and a test's temp dirs) have been torn down.
func TestManager_Shutdown_JoinsTrackedBackgroundGoroutines(t *testing.T) {
	m := NewManager(nil, func(Event) {}, runtimeTempDir(t))
	m.stopTimeout = 5 * time.Second

	started := make(chan struct{})
	release := make(chan struct{})
	var finished atomic.Bool
	if !m.spawnBackground(func() {
		close(started)
		<-release
		finished.Store(true)
	}) {
		t.Fatal("spawnBackground() = false before Shutdown; want true")
	}
	<-started

	shutdownReturned := make(chan struct{})
	go func() {
		m.Shutdown()
		close(shutdownReturned)
	}()

	// Shutdown must block on the tracked goroutine rather than return while it
	// is still running.
	select {
	case <-shutdownReturned:
		t.Fatal("Shutdown returned while a tracked goroutine was still running")
	case <-time.After(200 * time.Millisecond):
	}

	close(release)
	select {
	case <-shutdownReturned:
	case <-time.After(5 * time.Second):
		t.Fatal("Shutdown did not return after the tracked goroutine finished")
	}
	if !finished.Load() {
		t.Fatal("Shutdown returned before the tracked goroutine finished")
	}
	if m.spawnBackground(func() {}) {
		t.Fatal("spawnBackground() after Shutdown = true; want false")
	}
}

// TestManager_Shutdown_StopsTrackedBlackboardWorker verifies that Shutdown
// tears down the persistence worker of every blackboard the manager built —
// including one whose task never reached a terminal finalizer (the paused or
// abandoned case, which would otherwise leak the worker goroutine).
func TestManager_Shutdown_StopsTrackedBlackboardWorker(t *testing.T) {
	m := NewManager(nil, func(Event) {}, runtimeTempDir(t))
	m.stopTimeout = 5 * time.Second

	pb := NewPersistentBlackboard("task-bg", "sess-bg", NewTaskStoreAdapter(newInMemoryTaskStore()), newDiscardLogger())
	m.trackBlackboard(pb)

	var ran atomic.Int32
	if err := pb.persistSafe("probe", func() error { ran.Add(1); return nil }); err != nil {
		t.Fatalf("persistSafe(probe) error = %v", err)
	}
	if got := ran.Load(); got != 1 {
		t.Fatalf("persistence worker ran %d ops; want 1", got)
	}

	m.Shutdown()

	select {
	case <-pb.persistDone:
	default:
		t.Fatal("Shutdown left the tracked blackboard's persistence worker running")
	}
}

// TestPersistentBlackboard_Shutdown_DrainsAndIsIdempotent pins the contract
// the manager's join depends on: Shutdown returns only once the worker has
// drained and exited, and a repeat call is harmless.
func TestPersistentBlackboard_Shutdown_DrainsAndIsIdempotent(t *testing.T) {
	pb := NewPersistentBlackboard("task-drain", "sess-drain", NewTaskStoreAdapter(newInMemoryTaskStore()), newDiscardLogger())

	var drained atomic.Int32
	if err := pb.persistSafe("queued", func() error { drained.Add(1); return nil }); err != nil {
		t.Fatalf("persistSafe(queued) error = %v", err)
	}

	pb.Shutdown(time.Second)

	select {
	case <-pb.persistDone:
	default:
		t.Fatal("Shutdown returned while the persistence worker was still running")
	}
	if got := drained.Load(); got != 1 {
		t.Fatalf("drained ops = %d; want 1", got)
	}

	// Idempotent: a second call must not block or panic.
	pb.Shutdown(time.Second)
}
