package session

import (
	"io"
	"sync/atomic"
	"testing"
	"time"
)

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

// TestBackgroundTracker_EarlyDrainDoesNotCloseZero pins the fix for the
// early-drain bug: a tracked goroutine finishing while the tracker is still
// open must not close the zero channel, because spawn never reopens it — a
// stale closed zero would let a later closeAndWait return true immediately
// while a freshly spawned goroutine is still running. This sequence matches
// the real manager: StartEnvInfoCollection's goroutine finishes in
// milliseconds at startup, long before Shutdown joins a long-lived goroutine
// (title generation, deferred temp-dir removal).
func TestBackgroundTracker_EarlyDrainDoesNotCloseZero(t *testing.T) {
	tr := newBackgroundTracker()

	// Phase 1: a short-lived goroutine drains the counter to zero while the
	// tracker is still open.
	shortDone := make(chan struct{})
	if !tr.spawn(func() { close(shortDone) }) {
		t.Fatal("spawn() = false before close; want true")
	}
	<-shortDone

	// Phase 2: a goroutine that is still running at close time. closeAndWait
	// must NOT report success while it runs — the drained-to-zero event from
	// phase 1 must not satisfy the wait.
	blocked := make(chan struct{})
	release := make(chan struct{})
	if !tr.spawn(func() {
		close(blocked)
		<-release
	}) {
		t.Fatal("spawn() = false before close; want true")
	}
	<-blocked

	if tr.closeAndWait(20 * time.Millisecond) {
		t.Fatal("closeAndWait() = true while a goroutine spawned after an early drain was still running; want false")
	}

	// Phase 3: after the blocked goroutine finishes, the (already closed)
	// tracker reports success.
	close(release)
	if !tr.closeAndWait(5 * time.Second) {
		t.Fatal("closeAndWait() = false after every tracked goroutine finished; want true")
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

	pb := NewPersistentBlackboard("task-bg", "sess-bg", NewTaskStoreAdapter(newInMemoryTaskStore()), testLogger(io.Discard))
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
	pb := NewPersistentBlackboard("task-drain", "sess-drain", NewTaskStoreAdapter(newInMemoryTaskStore()), testLogger(io.Discard))

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

// TestManager_RestoreBlackboardTracked_StopsWorkerOnShutdown pins the
// invariant every manager-side restore path relies on: a blackboard returned
// by restoreBlackboardTracked has its persistence worker registered with the
// manager, so Shutdown stops it even though the restored task never reaches a
// terminal finalizer (the paused/abandoned resume case).
func TestManager_RestoreBlackboardTracked_StopsWorkerOnShutdown(t *testing.T) {
	m := NewManager(nil, func(Event) {}, runtimeTempDir(t))
	m.stopTimeout = 5 * time.Second
	t.Cleanup(m.Shutdown)

	adapter := NewTaskStoreAdapter(newInMemoryTaskStore())
	if err := adapter.PersistNewTask("task-restore", "sess-restore", "interrupted work"); err != nil {
		t.Fatalf("PersistNewTask error = %v", err)
	}

	pb, err := m.restoreBlackboardTracked("task-restore", "sess-restore", adapter, testLogger(io.Discard))
	if err != nil {
		t.Fatalf("restoreBlackboardTracked error = %v", err)
	}
	if pb == nil {
		t.Fatal("restoreBlackboardTracked returned nil blackboard for a persisted task")
	}

	m.Shutdown()

	select {
	case <-pb.persistDone:
	default:
		t.Fatal("Shutdown left the restored blackboard's persistence worker running")
	}
}

// TestManager_TrackBlackboard_PrunesFinishedWorkers pins the bounded-growth
// contract of the blackboard registry: tracking a new blackboard drops
// entries whose worker already exited through a terminal finalizer, while
// live workers (e.g. a paused task's) stay registered for Shutdown to stop.
func TestManager_TrackBlackboard_PrunesFinishedWorkers(t *testing.T) {
	m := NewManager(nil, func(Event) {}, runtimeTempDir(t))
	m.stopTimeout = 5 * time.Second
	t.Cleanup(m.Shutdown)

	adapter := NewTaskStoreAdapter(newInMemoryTaskStore())

	finished := NewPersistentBlackboard("task-finished", "sess-prune", adapter, testLogger(io.Discard))
	finished.CompleteTask(1) // terminal finalizer stops the worker
	select {
	case <-finished.persistDone:
	case <-time.After(5 * time.Second):
		t.Fatal("persistence worker did not exit after CompleteTask")
	}

	live := NewPersistentBlackboard("task-live", "sess-prune", adapter, testLogger(io.Discard))

	m.trackBlackboard(finished)
	m.trackBlackboard(live)

	m.mu.RLock()
	tracked := make([]*PersistentBlackboard, len(m.blackboards))
	copy(tracked, m.blackboards)
	m.mu.RUnlock()

	if len(tracked) != 1 || tracked[0] != live {
		t.Fatalf("tracked blackboards after pruning = %v; want only the live one", tracked)
	}
}

// TestManager_StopBackground_SharesOneBudgetAcrossBlackboards pins the
// shared-budget contract: N stuck persistence workers add up to at most one
// stopTimeout in total, not N × stopTimeout. Two workers whose op blocks
// forever would cost 2×stopTimeout under a per-blackboard budget; the margins
// below separate the two behaviors comfortably.
func TestManager_StopBackground_SharesOneBudgetAcrossBlackboards(t *testing.T) {
	const budget = 400 * time.Millisecond
	m := NewManager(nil, func(Event) {}, runtimeTempDir(t))
	m.stopTimeout = budget

	block := make(chan struct{})
	defer close(block) // let the stuck workers drain once the test is done

	adapter := NewTaskStoreAdapter(newInMemoryTaskStore())
	for _, taskID := range []string{"task-stuck-1", "task-stuck-2"} {
		pb := NewPersistentBlackboard(taskID, "sess-budget", adapter, testLogger(io.Discard))
		// Enqueue directly into the worker channel (buffered) so the worker
		// picks the op up and blocks in fn without any caller waiting on it.
		pb.persistCh <- persistOp{
			operation: "block",
			fn:        func() error { <-block; return nil },
			done:      make(chan error, 1),
		}
		m.trackBlackboard(pb)
	}

	start := time.Now()
	m.stopBackground()
	elapsed := time.Since(start)

	// Shared budget: both stuck workers together must stay inside one budget
	// plus scheduling slack (a per-blackboard budget would need 2×budget).
	if limit := 2 * budget; elapsed >= limit {
		t.Fatalf("stopBackground took %v with two stuck workers; want < %v (one shared budget)", elapsed, limit)
	}
}
