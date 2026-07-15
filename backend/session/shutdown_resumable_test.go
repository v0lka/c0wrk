package session

import (
	"context"
	"testing"
)

// countEvents drains the event channel and returns how many events of the
// given type it contained.
func countEvents(ch chan Event, eventType string) int {
	count := 0
	for {
		select {
		case e := <-ch:
			if e.Type == eventType {
				count++
			}
		default:
			return count
		}
	}
}

// launchFakeTaskGoroutine mimics the SendMessage/Resume task goroutine: it
// marks the session active with a cancellable context + done channel, blocks
// on the context, and on cancellation runs the SAME shutdown-aware
// cancellation handling the production goroutine uses. It returns a `started`
// channel that is closed once the goroutine is running (so the caller can
// safely trigger cancellation afterwards). The stand-in for
// orchestrator.HandleMessage is just `<-taskCtx.Done()`.
func launchFakeTaskGoroutine(t *testing.T, manager *Manager, session *Session) <-chan struct{} {
	t.Helper()

	taskCtx, cancel := context.WithCancel(context.Background())
	doneCh := make(chan struct{})

	session.mu.Lock()
	session.active = true
	session.cancel = cancel
	session.done = doneCh
	session.mu.Unlock()

	started := make(chan struct{})
	go func() {
		defer close(doneCh)
		defer func() {
			session.mu.Lock()
			session.active = false
			session.cancel = nil
			session.done = nil
			session.mu.Unlock()
		}()
		close(started)
		<-taskCtx.Done()
		// Mirror manager_execution.go's cancellation handling exactly.
		if taskCtx.Err() == context.Canceled {
			if manager.emitTaskCancelledUnlessShuttingDown(session.ID) {
				manager.persistCancellationIfUnfinished(session.ID)
			}
			return
		}
	}()

	return started
}

// TestShutdown_LeavesTaskInProgress verifies the core acceptance criterion:
// when the app shuts down while a task is running, the in-progress task is
// NOT marked cancelled — it stays resumable so GetUnfinishedTask finds it
// after restart.
func TestShutdown_LeavesTaskInProgress(t *testing.T) {
	manager, eventChan, _ := testManager(t)

	store := &recordingCancelTaskStore{
		mockTaskStoreForResumable: mockTaskStoreForResumable{
			unfinished: &TaskRecord{ID: "task-1", SessionID: "s1", Status: "in_progress"},
		},
	}

	info, err := manager.CreateSession(testProjectID, testWorkspacePath(t))
	if err != nil {
		t.Fatalf("CreateSession failed: %v", err)
	}
	// Set the task store AFTER CreateSession: the nil orchestrator from the
	// test factory would panic if a store were present at creation time.
	// persistCancellationIfUnfinished reads the store fresh at cancel time.
	manager.SetTaskStore(store)
	session, _ := manager.GetSession(info.ID)

	<-launchFakeTaskGoroutine(t, manager, session)

	// App shutdown: sets the shutting-down flag, cancels the active task, and
	// waits for the goroutine to finish.
	manager.Shutdown()

	store.mu.Lock()
	if store.cancelledCalls != 0 {
		t.Errorf("shutdown should leave the task in_progress, but CancelTask was called %d time(s)", store.cancelledCalls)
	}
	if store.completedCalls != 0 {
		t.Errorf("expected no CompleteTask calls on shutdown, got %d", store.completedCalls)
	}
	store.mu.Unlock()

	// No task_cancelled event should be emitted during shutdown.
	if n := countEvents(eventChan, "task_cancelled"); n != 0 {
		t.Errorf("expected no task_cancelled events on shutdown, got %d", n)
	}

	// The unfinished task should still be found after restart (resumable).
	adapter := NewTaskStoreAdapter(store)
	tid, err := adapter.GetUnfinishedTaskID(info.ID)
	if err != nil {
		t.Fatalf("GetUnfinishedTaskID error: %v", err)
	}
	if tid != "task-1" {
		t.Errorf("expected unfinished task task-1 to remain, got %q", tid)
	}

	// The shutting-down flag must be set after Shutdown returns.
	if !manager.shuttingDown.Load() {
		t.Error("expected shuttingDown flag to be set after Shutdown")
	}
}

// TestUserCancel_PersistsCancelled verifies the inverse: a user-initiated
// CancelTask does NOT set the shutting-down flag, so the task goroutine marks
// the task as cancelled (not resumable).
func TestUserCancel_PersistsCancelled(t *testing.T) {
	manager, eventChan, _ := testManager(t)

	store := &recordingCancelTaskStore{
		mockTaskStoreForResumable: mockTaskStoreForResumable{
			unfinished: &TaskRecord{ID: "task-1", SessionID: "s1", Status: "in_progress"},
		},
	}

	info, err := manager.CreateSession(testProjectID, testWorkspacePath(t))
	if err != nil {
		t.Fatalf("CreateSession failed: %v", err)
	}
	// Set the task store AFTER CreateSession (nil orchestrator would panic
	// otherwise). See TestShutdown_LeavesTaskInProgress for the rationale.
	manager.SetTaskStore(store)
	session, _ := manager.GetSession(info.ID)

	<-launchFakeTaskGoroutine(t, manager, session)

	// User-initiated cancel (NOT shutdown): the shutting-down flag stays false.
	if err := manager.CancelTask(info.ID); err != nil {
		t.Fatalf("CancelTask failed: %v", err)
	}

	store.mu.Lock()
	if store.cancelledCalls != 1 {
		t.Errorf("user cancel should mark the task cancelled once, got %d CancelTask call(s)", store.cancelledCalls)
	}
	if store.cancelledID != "task-1" {
		t.Errorf("expected task-1 to be cancelled, got %q", store.cancelledID)
	}
	store.mu.Unlock()

	if n := countEvents(eventChan, "task_cancelled"); n != 1 {
		t.Errorf("expected one task_cancelled event on user cancel, got %d", n)
	}

	if manager.shuttingDown.Load() {
		t.Error("shuttingDown flag must NOT be set for a user-initiated cancel")
	}
}

// TestEmitTaskCancelledUnlessShuttingDown exercises the decision seam directly:
// it is a no-op during shutdown and emits+returns true otherwise.
func TestEmitTaskCancelledUnlessShuttingDown(t *testing.T) {
	t.Run("shutdown skips emit and returns false", func(t *testing.T) {
		manager, eventChan, _ := testManager(t)
		manager.shuttingDown.Store(true)
		manager.SetTaskStore(&recordingCancelTaskStore{
			mockTaskStoreForResumable: mockTaskStoreForResumable{
				unfinished: &TaskRecord{ID: "task-1", SessionID: "s1", Status: "in_progress"},
			},
		})

		if manager.emitTaskCancelledUnlessShuttingDown("s1") {
			t.Error("expected false during shutdown")
		}
		if n := countEvents(eventChan, "task_cancelled"); n != 0 {
			t.Errorf("expected no task_cancelled event during shutdown, got %d", n)
		}
	})

	t.Run("user cancel emits and returns true", func(t *testing.T) {
		manager, eventChan, _ := testManager(t)
		// shuttingDown defaults to false.

		if !manager.emitTaskCancelledUnlessShuttingDown("s1") {
			t.Error("expected true when not shutting down")
		}
		if n := countEvents(eventChan, "task_cancelled"); n != 1 {
			t.Errorf("expected one task_cancelled event, got %d", n)
		}
	})
}
