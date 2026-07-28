package session

import (
	"context"
	"encoding/json"
	"errors"
	"sync"
	"testing"

	"github.com/v0lka/c0wrk/core/goal"
)

// goalTaskStoreFake is a TaskStore mock that records goal-state writes and
// controls GetUnfinishedTask, used to verify ClearGoal's persist ordering.
type goalTaskStoreFake struct {
	mu               sync.Mutex
	savedGoalStates  map[string]json.RawMessage // taskID -> last persisted goal state JSON
	unfinished       *TaskRecord                // returned by GetUnfinishedTask
	unfinishedTaskID string
}

func newGoalTaskStoreFake(taskID string) *goalTaskStoreFake {
	return &goalTaskStoreFake{
		savedGoalStates:  make(map[string]json.RawMessage),
		unfinished:       &TaskRecord{ID: taskID, SessionID: "sess-goal", Status: "in_progress"},
		unfinishedTaskID: taskID,
	}
}

func (f *goalTaskStoreFake) SaveTask(_ context.Context, _ TaskRecord) error { return nil }
func (f *goalTaskStoreFake) UpdateTaskPlan(_ context.Context, _ string, _ json.RawMessage) error {
	return nil
}
func (f *goalTaskStoreFake) UpdateTaskRouting(_ context.Context, _ string, _ json.RawMessage) error {
	return nil
}
func (f *goalTaskStoreFake) SaveTaskStep(_ context.Context, _ string, _ TaskStepRecord) error {
	return nil
}
func (f *goalTaskStoreFake) AddTaskReflection(_ context.Context, _ string, _ json.RawMessage) error {
	return nil
}
func (f *goalTaskStoreFake) CompleteTask(_ context.Context, _, _ string, _ int) error { return nil }
func (f *goalTaskStoreFake) FailTask(_ context.Context, _ string) error               { return nil }
func (f *goalTaskStoreFake) CancelTask(_ context.Context, _ string) error             { return nil }
func (f *goalTaskStoreFake) LoadTask(_ context.Context, _ string) (*TaskRecord, error) {
	return nil, nil
}
func (f *goalTaskStoreFake) LoadTaskSteps(_ context.Context, _ string) ([]TaskStepRecord, error) {
	return nil, nil
}
func (f *goalTaskStoreFake) SaveFacts(_ context.Context, _ string, _ json.RawMessage) error {
	return nil
}
func (f *goalTaskStoreFake) LoadFacts(_ context.Context, _ string) (json.RawMessage, error) {
	return nil, nil
}
func (f *goalTaskStoreFake) SaveAttachments(_ context.Context, _ string, _ json.RawMessage) error {
	return nil
}
func (f *goalTaskStoreFake) LoadAttachments(_ context.Context, _ string) (json.RawMessage, error) {
	return nil, nil
}
func (f *goalTaskStoreFake) SaveTrajectory(_ context.Context, _ string, _ json.RawMessage) error {
	return nil
}
func (f *goalTaskStoreFake) LoadTrajectory(_ context.Context, _ string) (json.RawMessage, error) {
	return nil, nil
}
func (f *goalTaskStoreFake) SaveGoalState(_ context.Context, taskID string, data json.RawMessage) error {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.savedGoalStates[taskID] = data
	return nil
}
func (f *goalTaskStoreFake) LoadGoalState(_ context.Context, taskID string) (json.RawMessage, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	if data, ok := f.savedGoalStates[taskID]; ok {
		return data, nil
	}
	return nil, nil
}
func (f *goalTaskStoreFake) GetUnfinishedTask(_ context.Context, _ string) (*TaskRecord, error) {
	return f.unfinished, nil
}
func (f *goalTaskStoreFake) ReactivateTask(_ context.Context, _ string) error { return nil }
func (f *goalTaskStoreFake) GetLatestTaskID(_ context.Context, _ string) (string, error) {
	// Status-agnostic: ClearGoal uses GetLatestTaskID precisely because
	// CancelTask flips the task row to a terminal status that
	// GetUnfinishedTaskID no longer returns. The fake therefore always reports
	// the task ID regardless of its (in-memory) status.
	return f.unfinishedTaskID, nil
}

// lastSavedGoalStatus returns the Status of the most-recently persisted goal
// state for the task, or "" if none was persisted.
func (f *goalTaskStoreFake) lastSavedGoalStatus(taskID string) goal.GoalStatus {
	f.mu.Lock()
	defer f.mu.Unlock()
	data, ok := f.savedGoalStates[taskID]
	if !ok {
		return ""
	}
	var gs goal.GoalState
	if err := json.Unmarshal(data, &gs); err != nil {
		return ""
	}
	return gs.Status
}

// seedGoalState persists an initial goal state for the task (simulating what a
// running goal loop would have stored).
func (f *goalTaskStoreFake) seedGoalState(taskID string, gs *goal.GoalState) {
	data, _ := json.Marshal(gs)
	f.mu.Lock()
	f.savedGoalStates[taskID] = data
	f.mu.Unlock()
}

// TestClearGoal_PreservesCancelledAgainstNoActiveTask verifies the fix for the
// lost-update race: ClearGoal must persist Status=cancelled as the FINAL state.
// The old implementation persisted cancelled FIRST then called CancelTask,
// allowing a running loop's exit-time persist to clobber it with paused. The
// reordered implementation cancels-and-waits first, then persists cancelled
// last. This test covers the common case where the session has no active task
// (the loop already exited): ClearGoal must still persist cancelled and must
// NOT return an error for the "no active task" condition.
func TestClearGoal_PreservesCancelledAgainstNoActiveTask(t *testing.T) {
	manager, _, _ := testManager(t)

	const taskID = "task-goal-1"
	ts := newGoalTaskStoreFake(taskID)
	ts.seedGoalState(taskID, &goal.GoalState{
		Condition: "ship it",
		Status:    goal.StatusPaused, // the loop's last write before exiting
	})
	manager.SetTaskStore(ts)

	// Create an in-memory session (no active task — the loop already exited).
	sess := &Session{ID: "sess-goal"}
	manager.mu.Lock()
	manager.sessions["sess-goal"] = sess
	manager.mu.Unlock()

	// ClearGoal should: (1) CancelTask (returns ErrNoActiveTask, treated as
	// non-fatal), (2) persist Status=cancelled LAST.
	err := manager.ClearGoal("sess-goal")
	if err != nil {
		t.Fatalf("ClearGoal returned error for no-active-task session: %v (ErrNoActiveTask must be non-fatal)", err)
	}

	// The final persisted state MUST be cancelled (terminal), NOT the seeded
	// paused — proving the cancelled persist was the LAST write.
	if got := ts.lastSavedGoalStatus(taskID); got != goal.StatusCancelled {
		t.Errorf("final persisted goal status = %q, want %q (cancelled must survive as the final write)", got, goal.StatusCancelled)
	}
}

// TestClearGoal_NoGoalTaskIsNoop verifies ClearGoal on a session whose task has
// no persisted goal state is a no-op (no panic, no spurious persist).
func TestClearGoal_NoGoalTaskIsNoop(t *testing.T) {
	manager, _, _ := testManager(t)

	const taskID = "task-none"
	ts := newGoalTaskStoreFake(taskID) // no seeded goal state
	manager.SetTaskStore(ts)

	sess := &Session{ID: "sess-no-goal"}
	manager.mu.Lock()
	manager.sessions["sess-no-goal"] = sess
	manager.mu.Unlock()

	if err := manager.ClearGoal("sess-no-goal"); err != nil {
		t.Fatalf("ClearGoal returned unexpected error: %v", err)
	}
}

// TestCancelTask_ErrNoActiveTaskIsSentinel verifies the refactor that turned
// the literal error into the exported ErrNoActiveTask sentinel, so ClearGoal
// (and other callers) can match it via errors.Is.
func TestCancelTask_ErrNoActiveTaskIsSentinel(t *testing.T) {
	manager, _, _ := testManager(t)

	sess := &Session{ID: "sess-idle"}
	manager.mu.Lock()
	manager.sessions["sess-idle"] = sess
	manager.mu.Unlock()

	err := manager.CancelTask("sess-idle")
	if !errors.Is(err, ErrNoActiveTask) {
		t.Errorf("CancelTask on idle session: err = %v, want errors.Is(ErrNoActiveTask)", err)
	}
}

// --- Goal proposal resolver tests ---

// TestResolveGoalProposal_NoResolverWired verifies that without a resolver
// installed, ResolveGoalProposal returns false (no resolution happened).
func TestResolveGoalProposal_NoResolverWired(t *testing.T) {
	manager, _, _ := testManager(t)
	if manager.ResolveGoalProposal("req-1", "approve", "c", "v", "executable", "") {
		t.Error("ResolveGoalProposal returned true with no resolver wired")
	}
}

// TestResolveGoalProposal_ForwardsDecision verifies the resolver callback is
// invoked with the exact arguments, including the clarify decision + clarification.
func TestResolveGoalProposal_ForwardsDecision(t *testing.T) {
	manager, _, _ := testManager(t)

	var gotReq, gotDecision, gotCond, gotVerify, gotMode, gotClarif string
	manager.SetGoalProposalResolver(func(req, decision, cond, verify, mode, clarif string) bool {
		gotReq, gotDecision, gotCond, gotVerify, gotMode, gotClarif = req, decision, cond, verify, mode, clarif
		return true
	})

	if !manager.ResolveGoalProposal("req-9", "clarify", "", "", "", "which scope?") {
		t.Fatal("expected resolver to return true")
	}
	if gotReq != "req-9" || gotDecision != "clarify" || gotClarif != "which scope?" {
		t.Errorf("resolver received (%q,%q,%q,%q,%q,%q), want (req-9,clarify,?,?,?,which scope?)", gotReq, gotDecision, gotCond, gotVerify, gotMode, gotClarif)
	}
}

// TestResolveGoalProposal_ForwardsVerificationMode verifies the approve path
// forwards the user's chosen verification mode through the resolver so the
// derivation agent's GoalState reflects the sign-off edit.
func TestResolveGoalProposal_ForwardsVerificationMode(t *testing.T) {
	manager, _, _ := testManager(t)

	var gotDecision, gotMode string
	manager.SetGoalProposalResolver(func(_, decision, _, _, mode, _ string) bool {
		gotDecision, gotMode = decision, mode
		return true
	})

	if !manager.ResolveGoalProposal("req-vm", "approve", "cond", "ver", "re_derivation", "") {
		t.Fatal("expected resolver to return true")
	}
	if gotDecision != "approve" || gotMode != "re_derivation" {
		t.Errorf("resolver got decision=%q mode=%q, want (approve,re_derivation)", gotDecision, gotMode)
	}
}

// TestPauseGoal_NilOrchestratorReturnsError verifies PauseGoal finds an
// in-memory session but returns an error when the session has no orchestrator
// (the test factory returns nil orchestrators).
func TestPauseGoal_NilOrchestratorReturnsError(t *testing.T) {
	manager, _, _ := testManager(t)

	sess := &Session{ID: "sess-pause"}
	manager.mu.Lock()
	manager.sessions["sess-pause"] = sess
	manager.mu.Unlock()

	if err := manager.PauseGoal("sess-pause"); err == nil {
		t.Error("expected error for nil orchestrator, got nil")
	}
}

// TestPauseGoal_UnknownSessionReturnsError verifies PauseGoal returns an error
// for a session that doesn't exist.
func TestPauseGoal_UnknownSessionReturnsError(t *testing.T) {
	manager, _, _ := testManager(t)
	if err := manager.PauseGoal("does-not-exist"); err == nil {
		t.Error("expected error for unknown session, got nil")
	}
}

// TestResumeGoal_NoTaskStoreReturnsNil verifies ResumeGoal delegates to
// ResumeTask, which returns nil when there is no resumable task (no task store
// configured).
func TestResumeGoal_NoTaskStoreReturnsNil(t *testing.T) {
	manager, _, _ := testManager(t)
	if err := manager.ResumeGoal(context.Background(), "sess-resume"); err != nil {
		t.Errorf("ResumeGoal with no task store should return nil, got %v", err)
	}
}
