package session

import (
	"context"
	"encoding/json"
	"testing"
	"time"

	"github.com/v0lka/c0wrk/core/goal"
	"github.com/v0lka/sp4rk/agent"
	"github.com/v0lka/sp4rk/agent/router"
	"github.com/v0lka/sp4rk/llm"
	"github.com/v0lka/sp4rk/orchestration"
)

func TestTaskStoreAdapter_RoundTrip(t *testing.T) {
	store, sessionID, cleanup := setupTestStoreWithSession(t)
	defer cleanup()

	adapter := NewTaskStoreAdapter(store)
	taskID := "adapter-round-trip"

	// 1. PersistNewTask
	if err := adapter.PersistNewTask(taskID, sessionID, "build a CLI tool"); err != nil {
		t.Fatalf("PersistNewTask failed: %v", err)
	}

	// 2. PersistPlan
	plan := &orchestration.Plan{
		Steps: []orchestration.PlanStep{
			{ID: "step_1", Description: "write code"},
			{ID: "step_2", Description: "run tests", DependsOn: []string{"step_1"}},
		},
	}
	if err := adapter.PersistPlan(taskID, plan); err != nil {
		t.Fatalf("PersistPlan failed: %v", err)
	}

	// 3. PersistRouting
	routing := &router.RoutingDecision{
		Domain:     "code",
		Complexity: 3,
	}
	if err := adapter.PersistRouting(taskID, routing); err != nil {
		t.Fatalf("PersistRouting failed: %v", err)
	}

	// 4. PersistStepResult
	steps := []agent.Step{{Thought: "thinking about code"}}
	if err := adapter.PersistStepResult(taskID, "step_1", "wrote code", "full output of step 1", "", steps); err != nil {
		t.Fatalf("PersistStepResult failed: %v", err)
	}

	// 5. PersistReflection
	reflection := orchestration.Reflection{
		Summary:         "first attempt analysis",
		SuggestedAction: "retry",
		Timestamp:       time.Now().Truncate(time.Second),
	}
	if err := adapter.PersistReflection(taskID, reflection); err != nil {
		t.Fatalf("PersistReflection failed: %v", err)
	}

	// 6. PersistCompletion
	if err := adapter.PersistCompletion(taskID, "task done", 2); err != nil {
		t.Fatalf("PersistCompletion failed: %v", err)
	}

	// 7. LoadTaskState — verify full round-trip
	state, err := adapter.LoadTaskState(taskID)
	if err != nil {
		t.Fatalf("LoadTaskState failed: %v", err)
	}
	if state == nil {
		t.Fatal("expected non-nil state")
	}

	// Verify fields
	if state.TaskID != taskID {
		t.Errorf("TaskID: got %q", state.TaskID)
	}
	if state.SessionID != sessionID {
		t.Errorf("SessionID: got %q", state.SessionID)
	}
	if state.OriginalRequest != "build a CLI tool" {
		t.Errorf("OriginalRequest: got %q", state.OriginalRequest)
	}
	if state.Status != "completed" {
		t.Errorf("Status: got %q, want %q", state.Status, "completed")
	}
	if state.FinalOutput != "task done" {
		t.Errorf("FinalOutput: got %q", state.FinalOutput)
	}

	// Routing
	if state.RoutingDecision == nil {
		t.Fatal("expected non-nil RoutingDecision")
	}
	if state.RoutingDecision.Domain != "code" {
		t.Errorf("RoutingDecision.Domain: got %q", state.RoutingDecision.Domain)
	}
	if state.RoutingDecision.Complexity != 3 {
		t.Errorf("RoutingDecision.Complexity: got %d", state.RoutingDecision.Complexity)
	}

	// Plan
	if state.Plan == nil {
		t.Fatal("expected non-nil Plan")
	}
	if len(state.Plan.Steps) != 2 {
		t.Errorf("Plan.Steps: got %d, want 2", len(state.Plan.Steps))
	}

	// StepResults
	if len(state.StepResults) != 1 {
		t.Fatalf("StepResults: got %d, want 1", len(state.StepResults))
	}
	sr, ok := state.StepResults["step_1"]
	if !ok {
		t.Fatal("step_1 not found in StepResults")
	}
	if sr.Summary != "wrote code" {
		t.Errorf("StepResult.Summary: got %q", sr.Summary)
	}
	if sr.FullOutput != "full output of step 1" {
		t.Errorf("StepResult.FullOutput: got %q", sr.FullOutput)
	}
	if len(sr.Steps) != 1 || sr.Steps[0].Thought != "thinking about code" {
		t.Errorf("StepResult.Steps: got %v", sr.Steps)
	}

	// Reflections
	if len(state.Reflections) != 1 {
		t.Fatalf("Reflections: got %d, want 1", len(state.Reflections))
	}
	if state.Reflections[0].Summary != "first attempt analysis" {
		t.Errorf("Reflection.Summary: got %q", state.Reflections[0].Summary)
	}
	if state.Reflections[0].SuggestedAction != "retry" {
		t.Errorf("Reflection.SuggestedAction: got %q", state.Reflections[0].SuggestedAction)
	}
}

func TestTaskStoreAdapter_GetUnfinishedTaskID(t *testing.T) {
	store, sessionID, cleanup := setupTestStoreWithSession(t)
	defer cleanup()

	adapter := NewTaskStoreAdapter(store)

	// Create a completed task
	if err := store.SaveTask(context.Background(), TaskRecord{
		ID: "done-task", SessionID: sessionID, OriginalRequest: "old",
		RoutingDecision: json.RawMessage(`{}`), Plan: json.RawMessage(`{}`),
		Reflections: json.RawMessage(`[]`), Status: "completed", CreatedAt: time.Now(),
	}); err != nil {
		t.Fatalf("SaveTask failed: %v", err)
	}

	// Create an in-progress task
	if err := store.SaveTask(context.Background(), TaskRecord{
		ID: "active-task", SessionID: sessionID, OriginalRequest: "current",
		RoutingDecision: json.RawMessage(`{}`), Plan: json.RawMessage(`{}`),
		Reflections: json.RawMessage(`[]`), Status: "in_progress",
		CreatedAt: time.Now().Add(time.Second),
	}); err != nil {
		t.Fatalf("SaveTask failed: %v", err)
	}

	id, err := adapter.GetUnfinishedTaskID(sessionID)
	if err != nil {
		t.Fatalf("GetUnfinishedTaskID failed: %v", err)
	}
	if id != "active-task" {
		t.Errorf("expected 'active-task', got %q", id)
	}
}

func TestTaskStoreAdapter_GetUnfinishedTaskID_None(t *testing.T) {
	store, sessionID, cleanup := setupTestStoreWithSession(t)
	defer cleanup()

	adapter := NewTaskStoreAdapter(store)

	// Only a completed task
	if err := store.SaveTask(context.Background(), TaskRecord{
		ID: "done-task", SessionID: sessionID, OriginalRequest: "old",
		RoutingDecision: json.RawMessage(`{}`), Plan: json.RawMessage(`{}`),
		Reflections: json.RawMessage(`[]`), Status: "completed", CreatedAt: time.Now(),
	}); err != nil {
		t.Fatalf("SaveTask failed: %v", err)
	}

	id, err := adapter.GetUnfinishedTaskID(sessionID)
	if err != nil {
		t.Fatalf("GetUnfinishedTaskID failed: %v", err)
	}
	if id != "" {
		t.Errorf("expected empty string, got %q", id)
	}
}

func TestTaskStoreAdapter_PersistFailure(t *testing.T) {
	store, sessionID, cleanup := setupTestStoreWithSession(t)
	defer cleanup()

	adapter := NewTaskStoreAdapter(store)
	taskID := "adapter-fail"

	if err := adapter.PersistNewTask(taskID, sessionID, "test"); err != nil {
		t.Fatalf("PersistNewTask failed: %v", err)
	}

	if err := adapter.PersistFailure(taskID); err != nil {
		t.Fatalf("PersistFailure failed: %v", err)
	}

	state, err := adapter.LoadTaskState(taskID)
	if err != nil {
		t.Fatalf("LoadTaskState failed: %v", err)
	}
	if state == nil {
		t.Fatal("expected non-nil state")
	}
	if state.Status != "failed" {
		t.Errorf("Status: got %q, want 'failed'", state.Status)
	}
}

func TestTaskStoreAdapter_LoadTaskState_NotFound(t *testing.T) {
	store, _, cleanup := setupTestStoreWithSession(t)
	defer cleanup()

	adapter := NewTaskStoreAdapter(store)

	state, err := adapter.LoadTaskState("nonexistent")
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if state != nil {
		t.Error("expected nil state for missing task")
	}
}

func TestTaskStoreAdapter_TrajectoryRoundTrip(t *testing.T) {
	store, sessionID, cleanup := setupTestStoreWithSession(t)
	defer cleanup()

	adapter := NewTaskStoreAdapter(store)
	taskID := "adapter-traj"

	if err := adapter.PersistNewTask(taskID, sessionID, "build trajectory"); err != nil {
		t.Fatalf("PersistNewTask failed: %v", err)
	}

	steps := []agent.Step{
		{
			Thought:          "thinking about the problem",
			ReasoningContent: "step-by-step reasoning",
			Action: llm.ToolCall{
				ID:    "call-1",
				Name:  "read_file",
				Input: json.RawMessage(`{"path":"a.go"}`),
			},
			Observation:   "file contents here",
			ResponseGroup: 42,
		},
		{
			Thought:     "second thought",
			Observation: "another observation",
		},
	}

	if err := adapter.SaveTrajectory(taskID, steps); err != nil {
		t.Fatalf("SaveTrajectory failed: %v", err)
	}

	loaded, err := adapter.LoadTrajectory(taskID)
	if err != nil {
		t.Fatalf("LoadTrajectory failed: %v", err)
	}
	if loaded == nil {
		t.Fatal("expected non-nil loaded trajectory")
	}
	if len(loaded) != 2 {
		t.Fatalf("expected 2 steps, got %d", len(loaded))
	}

	// Verify all special fields round-trip: Thought, ReasoningContent,
	// Action, Observation, ResponseGroup.
	s := loaded[0]
	if s.Thought != "thinking about the problem" {
		t.Errorf("Thought: got %q", s.Thought)
	}
	if s.ReasoningContent != "step-by-step reasoning" {
		t.Errorf("ReasoningContent: got %q", s.ReasoningContent)
	}
	if s.Observation != "file contents here" {
		t.Errorf("Observation: got %q", s.Observation)
	}
	if s.ResponseGroup != 42 {
		t.Errorf("ResponseGroup: got %d", s.ResponseGroup)
	}
	if s.Action.ID != "call-1" {
		t.Errorf("Action.ID: got %q", s.Action.ID)
	}
	if s.Action.Name != "read_file" {
		t.Errorf("Action.Name: got %q", s.Action.Name)
	}
	if string(s.Action.Input) != `{"path":"a.go"}` {
		t.Errorf("Action.Input: got %s", s.Action.Input)
	}
}

func TestTaskStoreAdapter_LoadTrajectory_NotFound(t *testing.T) {
	store, sessionID, cleanup := setupTestStoreWithSession(t)
	defer cleanup()

	adapter := NewTaskStoreAdapter(store)
	taskID := "adapter-traj-missing"

	if err := adapter.PersistNewTask(taskID, sessionID, "test"); err != nil {
		t.Fatalf("PersistNewTask failed: %v", err)
	}

	// No trajectory persisted → nil, nil (not an error).
	loaded, err := adapter.LoadTrajectory(taskID)
	if err != nil {
		t.Fatalf("LoadTrajectory should not error on missing, got: %v", err)
	}
	if loaded != nil {
		t.Errorf("expected nil trajectory, got %v", loaded)
	}
}

func TestTaskStoreAdapter_SaveAndLoadGoalState(t *testing.T) {
	store, sessionID, cleanup := setupTestStoreWithSession(t)
	defer cleanup()

	adapter := NewTaskStoreAdapter(store)
	taskID := "adapter-goal"

	if err := adapter.PersistNewTask(taskID, sessionID, "goal task"); err != nil {
		t.Fatalf("PersistNewTask failed: %v", err)
	}

	gs := &goal.GoalState{
		Condition:    "all goal tests pass",
		VerifyClause: "go test ./core/goal/...",
		Budget:       goal.GoalBudget{MaxTurns: 5},
		TurnCount:    2,
		Status:       goal.StatusActive, // paused goals stay active
		LastVerdict: &goal.Verdict{
			Status:     "not_met",
			Reason:     "still working",
			DeclaredAt: time.Date(2026, 7, 18, 12, 0, 0, 0, time.UTC),
			Evidence:   []goal.GoalEvidence{{Type: goal.EvidenceTypeFile, Ref: "main.go", Summary: "wip"}},
		},
		CreatedAt: time.Date(2026, 7, 18, 11, 0, 0, 0, time.UTC),
	}

	if err := adapter.PersistGoalState(taskID, gs); err != nil {
		t.Fatalf("PersistGoalState failed: %v", err)
	}

	loaded, err := adapter.LoadGoalState(taskID)
	if err != nil {
		t.Fatalf("LoadGoalState failed: %v", err)
	}
	if loaded == nil {
		t.Fatal("expected non-nil loaded goal state")
	}

	// Verify the full state round-trips, including nested Budget and Verdict.
	if loaded.Condition != gs.Condition {
		t.Errorf("Condition: got %q, want %q", loaded.Condition, gs.Condition)
	}
	if loaded.VerifyClause != gs.VerifyClause {
		t.Errorf("VerifyClause: got %q, want %q", loaded.VerifyClause, gs.VerifyClause)
	}
	if loaded.Status != goal.StatusActive {
		t.Errorf("Status: got %q, want %q", loaded.Status, goal.StatusActive)
	}
	if loaded.Budget.MaxTurns != 5 {
		t.Errorf("Budget.MaxTurns: got %d, want 5", loaded.Budget.MaxTurns)
	}
	if loaded.TurnCount != 2 {
		t.Errorf("TurnCount: got %d, want 2", loaded.TurnCount)
	}
	if loaded.LastVerdict == nil {
		t.Fatal("LastVerdict should round-trip")
	}
	if loaded.LastVerdict.Status != "not_met" {
		t.Errorf("LastVerdict.Status: got %q, want not_met", loaded.LastVerdict.Status)
	}
	if len(loaded.LastVerdict.Evidence) != 1 || loaded.LastVerdict.Evidence[0].Ref != "main.go" {
		t.Errorf("LastVerdict.Evidence did not round-trip: %+v", loaded.LastVerdict.Evidence)
	}
}

func TestTaskStoreAdapter_LoadGoalState_NotFound(t *testing.T) {
	store, sessionID, cleanup := setupTestStoreWithSession(t)
	defer cleanup()

	adapter := NewTaskStoreAdapter(store)
	taskID := "adapter-goal-missing"

	if err := adapter.PersistNewTask(taskID, sessionID, "test"); err != nil {
		t.Fatalf("PersistNewTask failed: %v", err)
	}

	// No goal state persisted → nil, nil (not an error).
	loaded, err := adapter.LoadGoalState(taskID)
	if err != nil {
		t.Fatalf("LoadGoalState should not error on missing, got: %v", err)
	}
	if loaded != nil {
		t.Errorf("expected nil goal state, got %+v", loaded)
	}
}

// TestTaskStoreAdapter_LoadTaskState_PopulatesGoalState verifies that
// LoadTaskState hydrates TaskState.GoalState from the persisted goal-state
// blob (acceptance criterion #2). A task with no persisted goal state leaves
// GoalState nil.
func TestTaskStoreAdapter_LoadTaskState_PopulatesGoalState(t *testing.T) {
	store, sessionID, cleanup := setupTestStoreWithSession(t)
	defer cleanup()

	adapter := NewTaskStoreAdapter(store)
	taskID := "adapter-loadstate-goal"

	if err := adapter.PersistNewTask(taskID, sessionID, "goal task"); err != nil {
		t.Fatalf("PersistNewTask failed: %v", err)
	}

	gs := &goal.GoalState{
		Condition: "ship the feature",
		Status:    goal.StatusActive, // paused goals stay active
	}
	if err := adapter.PersistGoalState(taskID, gs); err != nil {
		t.Fatalf("PersistGoalState failed: %v", err)
	}

	state, err := adapter.LoadTaskState(taskID)
	if err != nil {
		t.Fatalf("LoadTaskState failed: %v", err)
	}
	if state == nil {
		t.Fatal("expected non-nil task state")
	}
	if state.GoalState == nil {
		t.Fatal("expected TaskState.GoalState to be populated")
	}
	if state.GoalState.Condition != "ship the feature" {
		t.Errorf("GoalState.Condition = %q, want %q", state.GoalState.Condition, "ship the feature")
	}
	if state.GoalState.Status != goal.StatusActive {
		t.Errorf("GoalState.Status = %q, want %q", state.GoalState.Status, goal.StatusActive)
	}
}

// TestTaskStoreAdapter_LoadTaskState_NilGoalStateForNonGoalTask verifies that
// a task with no persisted goal state leaves GoalState nil (non-goal tasks).
func TestTaskStoreAdapter_LoadTaskState_NilGoalStateForNonGoalTask(t *testing.T) {
	store, sessionID, cleanup := setupTestStoreWithSession(t)
	defer cleanup()

	adapter := NewTaskStoreAdapter(store)
	taskID := "adapter-loadstate-nogoal"

	if err := adapter.PersistNewTask(taskID, sessionID, "plain task"); err != nil {
		t.Fatalf("PersistNewTask failed: %v", err)
	}

	state, err := adapter.LoadTaskState(taskID)
	if err != nil {
		t.Fatalf("LoadTaskState failed: %v", err)
	}
	if state == nil {
		t.Fatal("expected non-nil task state")
	}
	if state.GoalState != nil {
		t.Errorf("expected nil GoalState for non-goal task, got %+v", state.GoalState)
	}
}
