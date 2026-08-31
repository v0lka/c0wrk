package session

import (
	"context"
	"testing"
	"time"
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

// TestShutdown_PausesTaskInProgress verifies the core acceptance criterion:
// when the app shuts down while a task is running, the in-progress task is
// marked paused (not cancelled) so GetUnfinishedTask finds it after restart
// as a clearly resumable paused checkpoint.
func TestShutdown_PausesTaskInProgress(t *testing.T) {
	manager, eventChan, _ := testManager(t)

	taskRec := &TaskRecord{ID: "task-1", SessionID: "s1", Status: "in_progress"}
	store := &recordingCancelTaskStore{
		mockTaskStoreForResumable: mockTaskStoreForResumable{
			unfinished:     taskRec,
			loadTaskResult: taskRec,
		},
	}

	info, err := manager.CreateSession(testProjectID, testWorkspacePath(t))
	if err != nil {
		t.Fatalf("CreateSession failed: %v", err)
	}
	// Set the task store AFTER CreateSession: the nil orchestrator from the
	// test factory would panic if a store were present at creation time.
	// persistPauseIfUnfinished reads the store fresh at shutdown time.
	manager.SetTaskStore(store)
	session, _ := manager.GetSession(info.ID)

	<-launchFakeTaskGoroutine(t, manager, session)

	// App shutdown: sets the shutting-down flag, cancels the active task, and
	// waits for the goroutine to finish.
	manager.Shutdown()

	store.mu.Lock()
	if store.cancelledCalls != 0 {
		t.Errorf("shutdown should not cancel the task, but CancelTask was called %d time(s)", store.cancelledCalls)
	}
	if store.pausedCalls != 1 {
		t.Errorf("shutdown should pause the in-progress task once, got %d PauseTask call(s)", store.pausedCalls)
	}
	if store.pausedID != "task-1" {
		t.Errorf("expected task-1 to be paused, got %q", store.pausedID)
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

// TestShutdown_KeepsFailedTaskUnchanged verifies that a task whose status is
// already terminal (e.g. failed) is NOT repaused on shutdown — only an
// in_progress task is checkpointed as paused.
func TestShutdown_KeepsFailedTaskUnchanged(t *testing.T) {
	manager, _, _ := testManager(t)

	taskRec := &TaskRecord{ID: "task-failed", SessionID: "s1", Status: "failed"}
	store := &recordingCancelTaskStore{
		mockTaskStoreForResumable: mockTaskStoreForResumable{
			unfinished:     taskRec,
			loadTaskResult: taskRec,
		},
	}

	info, err := manager.CreateSession(testProjectID, testWorkspacePath(t))
	if err != nil {
		t.Fatalf("CreateSession failed: %v", err)
	}
	manager.SetTaskStore(store)
	session, _ := manager.GetSession(info.ID)

	<-launchFakeTaskGoroutine(t, manager, session)

	manager.Shutdown()

	store.mu.Lock()
	if store.pausedCalls != 0 {
		t.Errorf("shutdown must not repause a non-in_progress task, got %d PauseTask call(s)", store.pausedCalls)
	}
	if store.cancelledCalls != 0 {
		t.Errorf("shutdown must not cancel a non-in_progress task, got %d CancelTask call(s)", store.cancelledCalls)
	}
	store.mu.Unlock()
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
	// otherwise). See TestShutdown_PausesTaskInProgress for the rationale.
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

// TestShutdown_MidPlan_RestartResumeCompletesAllStepsTerminal covers the
// shutdown flavor of the pause→resume scenario: a task is executing a
// declared two-step plan (s1's subagent is mid-work) when the app SHUTS DOWN.
// The teardown checkpoints the task as paused with its plan (and trajectory)
// intact and no successful step result; RestoreBlackboard — what a restarted
// process runs — hydrates the plan; and a fresh manager over the same stores
// ("restart") resumes the task: the conductor continues the SAME plan to
// completion — every step terminal, the row completed, no re-declared plan.
func TestShutdown_MidPlan_RestartResumeCompletesAllStepsTerminal(t *testing.T) {
	caller1 := &scriptedPlanLLM{script: []scriptedPlanStep{
		{respond: routingJSONResponse("general", 2)}, // router classification
		{respond: declarePlanCall("c1")},             // declare the roadmap
		{respond: executePlanCall("c2")},             // start executing it
		// s1's subagent is gated mid-work when the shutdown lands.
		{respond: bashExecCall("g1"), started: make(chan struct{}), gate: make(chan struct{})},
	}}
	bash := newPassthroughBashExec()
	store := newInMemoryTaskStore()
	sessions := newMockSessionStore()

	eventChan := make(chan Event, 200)
	mgr1 := NewManager(planWorkflowFactory(caller1, bash), func(e Event) { eventChan <- e }, t.TempDir())
	t.Cleanup(mgr1.Shutdown)
	// Both stores set BEFORE CreateSession: the task store wires the
	// PersistentBlackboard factory (real persistence) and the session store
	// lets the "restarted" manager lazily restore the session.
	mgr1.SetTaskStore(store)
	mgr1.SetSessionStore(sessions)

	ws := testWorkspacePath(t)
	info, err := mgr1.CreateSession(testProjectID, ws)
	if err != nil {
		t.Fatalf("CreateSession failed: %v", err)
	}
	// Seed the session row so the "restarted" manager can lazily restore it
	// (CreateSession itself does not persist the session row).
	seedSession(t, sessions, info.ID, testProjectID, "mid-plan shutdown", false)

	if err := mgr1.SendMessage(context.Background(), info.ID, "build the widget in two planned steps", nil, nil, "", "", false, "", false); err != nil {
		t.Fatalf("SendMessage failed: %v", err)
	}

	// Wait until s1's subagent is blocked mid-work.
	select {
	case <-caller1.script[3].started:
	case <-time.After(5 * time.Second):
		t.Fatal("timeout waiting for the s1 subagent to reach its (gated) LLM call")
	}

	// App shutdown mid-plan. Shutdown cancels the running task and joins its
	// goroutine; the gated LLM call unblocks exactly on that cancellation
	// (the gate selects on ctx.Done), so no timing window is involved.
	shutdownDone := make(chan struct{})
	go func() {
		mgr1.Shutdown()
		close(shutdownDone)
	}()
	select {
	case <-shutdownDone:
	case <-time.After(5 * time.Second):
		t.Fatal("Shutdown did not complete")
	}

	// The shutdown checkpoint. The cancellation surfaces as a run error, so
	// HandleMessage marks the row failed (the resumable flavor: failed tasks
	// match GetUnfinishedTask and reappear as resumable after restart) — the
	// plan and trajectory survive, with NO successful step result (s1 was cut
	// mid-work, s2 never started), which is exactly what keeps the plan
	// continuable on resume.
	adapter := NewTaskStoreAdapter(store)
	taskID, err := adapter.GetUnfinishedTaskID(info.ID)
	if err != nil || taskID == "" {
		t.Fatalf("GetUnfinishedTaskID after shutdown = %q, %v — the failed task must remain resumable", taskID, err)
	}
	state, err := adapter.LoadTaskState(taskID)
	if err != nil || state == nil {
		t.Fatalf("LoadTaskState after shutdown = %+v, %v", state, err)
	}
	if state.Status != "failed" {
		t.Errorf("checkpointed row status = %q, want failed (resumable; must not be completed/cancelled)", state.Status)
	}
	if state.Plan == nil || len(state.Plan.Steps) != 2 {
		t.Fatalf("checkpointed plan = %+v, want the declared two-step plan to survive the shutdown", state.Plan)
	}
	if sr, ok := state.StepResults["s1"]; ok && sr.Error == nil {
		t.Errorf("s1 result after shutdown = %+v, want absent or non-success (cut mid-work)", sr)
	}
	if sr, ok := state.StepResults["s2"]; ok && sr.Error == nil {
		t.Errorf("s2 result after shutdown = %+v, want absent or non-success (never started)", sr)
	}
	if traj, err := adapter.LoadTrajectory(taskID); err != nil || len(traj) == 0 {
		t.Fatalf("persisted trajectory after shutdown is empty (%v) — the checkpoint must survive a restart", err)
	}

	// RestoreBlackboard — the first thing a restarted process does for a
	// resumable task — must hydrate the plan and the original request.
	bb, err := RestoreBlackboard(taskID, info.ID, adapter, nil)
	if err != nil || bb == nil {
		t.Fatalf("RestoreBlackboard = %+v, %v", bb, err)
	}
	if plan := bb.GetPlan(); plan == nil || len(plan.Steps) != 2 {
		t.Fatalf("restored plan = %+v, want the declared two-step plan", plan)
	}
	if bb.GetOriginalRequest() != "build the widget in two planned steps" {
		t.Errorf("restored original request = %q", bb.GetOriginalRequest())
	}

	// A resume on the shutting-down manager must be refused: the restart is
	// part of the contract (the teardown drained every task goroutine).
	if err := mgr1.ResumeTask(context.Background(), info.ID, "", "", ""); err == nil {
		t.Error("ResumeTask must be refused once Shutdown has begun")
	}

	// --- "Restart": a fresh manager over the SAME stores ---
	caller2 := &scriptedPlanLLM{script: []scriptedPlanStep{
		// Resumed runs never re-route; the conductor continues the plan.
		{respond: executePlanCall("r1")},
		{respond: finishResponse("s1 resumed done")}, // s1 subagent, re-run
		{respond: finishResponse("s2 done")},         // s2 subagent
		{respond: finishResponse("plan finished")},   // Conductor finishes
	}}
	eventChan2 := make(chan Event, 200)
	mgr2 := NewManager(planWorkflowFactory(caller2, bash), func(e Event) { eventChan2 <- e }, t.TempDir())
	t.Cleanup(mgr2.Shutdown)
	mgr2.SetTaskStore(store)
	mgr2.SetSessionStore(sessions)
	mgr2.SetProjectResolver(func(string) (string, error) { return ws, nil })

	if err := mgr2.ResumeTask(context.Background(), info.ID, "", "", ""); err != nil {
		t.Fatalf("ResumeTask on the restarted manager failed: %v", err)
	}
	complete, ok := waitForEvent(eventChan2, "task_complete", 5*time.Second)
	if !ok {
		t.Fatal("timeout waiting for task_complete event after the restart resume")
	}
	data, ok := complete.Data.(TaskCompleteData)
	if !ok {
		t.Fatalf("expected TaskCompleteData, got %T", complete.Data)
	}
	if !data.Success || data.Output != "plan finished" {
		t.Errorf("restarted completion = success=%v output=%q, want success with %q", data.Success, data.Output, "plan finished")
	}

	// No re-publication after the restart either.
	if n := countEvents(eventChan2, "plan_generated"); n != 0 {
		t.Errorf("restarted resume emitted %d plan_generated event(s) — the approved plan must be continued, not re-declared", n)
	}

	// Every step terminal and error-free, the row completed, the plan intact.
	finalState, err := adapter.LoadTaskState(taskID)
	if err != nil || finalState == nil {
		t.Fatalf("LoadTaskState after the restart resume = %+v, %v", finalState, err)
	}
	if finalState.Status != "completed" {
		t.Errorf("task row status after the restart resume = %q, want completed", finalState.Status)
	}
	sr1, ok := finalState.StepResults["s1"]
	if !ok || sr1.Error != nil || sr1.FullOutput != "s1 resumed done" {
		t.Errorf("s1 after the restart resume = %+v (ok=%v), want a successful step", sr1, ok)
	}
	sr2, ok := finalState.StepResults["s2"]
	if !ok || sr2.Error != nil || sr2.FullOutput != "s2 done" {
		t.Errorf("s2 after the restart resume = %+v (ok=%v), want a successful step", sr2, ok)
	}
	if finalState.Plan == nil || len(finalState.Plan.Steps) != 2 ||
		finalState.Plan.Steps[0].ID != "s1" || finalState.Plan.Steps[1].ID != "s2" ||
		finalState.Plan.Steps[0].Summary != "Do the groundwork" {
		t.Errorf("plan after the restart resume = %+v, want the originally declared two-step plan (append-only)", finalState.Plan)
	}
}
