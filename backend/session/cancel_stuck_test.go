package session

import (
	"io"
	"log/slog"
	"testing"
	"time"
)

// silenceManagerLogs routes the manager's own logging (INFO shutdown records
// and the like) to a discard sink so it does not clutter the test output. The
// manager is test-local, so the sink is left installed: the t.Cleanup Shutdown
// it triggers is silent too. Expected WARN diagnostics are still asserted
// explicitly where they occur (via captureManagerDiagnostics), which is the
// pattern this suite uses for deliberately exercised failure paths.
func silenceManagerLogs(m *Manager) {
	m.SetLogger(slog.New(slog.NewTextHandler(io.Discard, nil)))
}

// TestCancelTask_TimeoutForcesTerminalState verifies the escape hatch of
// CancelTask's bounded wait: when the task goroutine ignores its cancellation
// for the whole stopTimeout (e.g. a tool call that never returns), the stop
// must still take effect — the persisted task is flipped to cancelled and the
// terminal task_cancelled event is emitted, so the UI leaves
// "Generating response…" instead of showing a forever-running task.
func TestCancelTask_TimeoutForcesTerminalState(t *testing.T) {
	manager, eventChan, _ := testManager(t)
	manager.stopTimeout = 25 * time.Millisecond
	// Silence the routine INFO teardown records, then assert the forced-cancel
	// WARN (the behavior under test) by severity, message and payload so it is
	// intercepted rather than leaked to stderr.
	silenceManagerLogs(manager)
	captureManagerDiagnostics(t, manager, warningDiagnostic(
		"timed out waiting for task goroutine to stop on cancel; forcing terminal state",
		map[string]string{"session_id": "sess-stuck"},
	))

	store := &recordingCancelTaskStore{
		mockTaskStoreForResumable: mockTaskStoreForResumable{
			unfinished: &TaskRecord{ID: "task-stuck", SessionID: "sess-stuck", Status: "in_progress"},
		},
	}
	manager.SetTaskStore(store)

	// An active session whose goroutine never observes cancellation: `done` is
	// never closed and the cancel func is a no-op — the stuck-tool shape.
	sess := &Session{
		ID:     "sess-stuck",
		Name:   "Stuck",
		active: true,
		done:   make(chan struct{}),
		cancel: func() {},
	}
	manager.mu.Lock()
	manager.sessions["sess-stuck"] = sess
	manager.mu.Unlock()

	drainEvents(eventChan)

	if err := manager.CancelTask("sess-stuck"); err != nil {
		t.Fatalf("CancelTask returned error: %v", err)
	}

	// The persisted task must be flipped to cancelled so no stale unfinished
	// row keeps has_unfinished_task alive after restart.
	store.mu.Lock()
	cancelledCalls := store.cancelledCalls
	cancelledID := store.cancelledID
	store.mu.Unlock()
	if cancelledCalls != 1 || cancelledID != "task-stuck" {
		t.Errorf("expected one persisted cancellation of task-stuck, got calls=%d id=%q", cancelledCalls, cancelledID)
	}

	// The terminal event must be emitted even though the goroutine never
	// finished, so the UI leaves the running state.
	if n := countEvents(eventChan, "task_cancelled"); n != 1 {
		t.Errorf("expected one task_cancelled event on stuck cancel, got %d", n)
	}
}

// TestCancelTask_FastStopDoesNotForceTerminalState verifies the normal path is
// unchanged: when the goroutine settles within the timeout, CancelTask returns
// without emitting a terminal event on its own (the goroutine owns that).
func TestCancelTask_FastStopDoesNotForceTerminalState(t *testing.T) {
	manager, eventChan, _ := testManager(t)
	manager.stopTimeout = time.Second
	silenceManagerLogs(manager)

	store := &recordingCancelTaskStore{
		mockTaskStoreForResumable: mockTaskStoreForResumable{
			unfinished: &TaskRecord{ID: "task-ok", SessionID: "sess-ok", Status: "in_progress"},
		},
	}
	manager.SetTaskStore(store)

	done := make(chan struct{})
	close(done) // the task goroutine already settled
	sess := &Session{
		ID:     "sess-ok",
		Name:   "Done",
		active: true,
		done:   done,
		cancel: func() {},
	}
	manager.mu.Lock()
	manager.sessions["sess-ok"] = sess
	manager.mu.Unlock()

	drainEvents(eventChan)

	if err := manager.CancelTask("sess-ok"); err != nil {
		t.Fatalf("CancelTask returned error: %v", err)
	}

	if n := countEvents(eventChan, "task_cancelled"); n != 0 {
		t.Errorf("expected no forced task_cancelled event when the goroutine settled, got %d", n)
	}
	store.mu.Lock()
	defer store.mu.Unlock()
	if store.cancelledCalls != 0 {
		t.Errorf("expected no forced persisted cancellation, got %d", store.cancelledCalls)
	}
}

// TestActiveSessions_FlagsHungSessions verifies the close-guard payload flags a
// session whose stop/pause request has gone unanswered past the threshold,
// while a session with no pending request (or a fresh one) is not flagged.
func TestActiveSessions_FlagsHungSessions(t *testing.T) {
	manager, _, _ := testManager(t)
	silenceManagerLogs(manager)

	manager.mu.Lock()
	manager.sessions["hung"] = &Session{
		ID: "hung", Name: "Hung", active: true,
		stopRequestedAt: time.Now().Add(-hungSessionThreshold - time.Second),
	}
	manager.sessions["running"] = &Session{ID: "running", Name: "Running", active: true}
	manager.sessions["fresh"] = &Session{
		ID: "fresh", Name: "Fresh", active: true,
		stopRequestedAt: time.Now(),
	}
	manager.mu.Unlock()

	byID := map[string]ActiveSessionInfo{}
	for _, s := range manager.ActiveSessions() {
		byID[s.ID] = s
	}

	if !byID["hung"].Hung {
		t.Error("a session whose stop request went unanswered past the threshold must be flagged hung")
	}
	if byID["running"].Hung {
		t.Error("a session with no stop request must not be flagged hung")
	}
	if byID["fresh"].Hung {
		t.Error("a session whose stop was just requested must not yet be flagged hung")
	}
}
