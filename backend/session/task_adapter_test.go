package session

import (
	"encoding/json"
	"testing"
	"time"

	"github.com/user/agent/core"
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
	plan := &core.Plan{
		Steps: []core.PlanStep{
			{ID: "step_1", Description: "write code", RelevantAC: []string{"ac_1"}},
			{ID: "step_2", Description: "run tests", DependsOn: []string{"step_1"}},
		},
	}
	if err := adapter.PersistPlan(taskID, plan); err != nil {
		t.Fatalf("PersistPlan failed: %v", err)
	}

	// 3. PersistCriteria
	criteria := []core.AcceptanceCriterion{
		{ID: "ac_1", Description: "must compile", CheckType: "programmatic"},
		{ID: "ac_2", Description: "must pass tests", CheckType: "llm_judge"},
	}
	if err := adapter.PersistCriteria(taskID, criteria); err != nil {
		t.Fatalf("PersistCriteria failed: %v", err)
	}

	// 4. PersistRouting
	routing := &core.RoutingDecision{
		Domain:     "code",
		Complexity: 3,
		Confidence: 0.95,
	}
	if err := adapter.PersistRouting(taskID, routing); err != nil {
		t.Fatalf("PersistRouting failed: %v", err)
	}

	// 5. PersistStepResult
	steps := []core.Step{{Thought: "thinking about code"}}
	if err := adapter.PersistStepResult(taskID, "step_1", "wrote code", "full output of step 1", "", steps); err != nil {
		t.Fatalf("PersistStepResult failed: %v", err)
	}

	// 6. PersistReflection
	reflection := core.Reflection{
		Summary:         "first attempt analysis",
		SuggestedAction: "retry",
		FailedCriteria:  []string{"ac_2"},
		Timestamp:       time.Now().Truncate(time.Second),
	}
	if err := adapter.PersistReflection(taskID, reflection); err != nil {
		t.Fatalf("PersistReflection failed: %v", err)
	}

	// 7. PersistCompletion
	evalResult := &core.EvalResult{
		AllPassed: true,
		Passed: []core.EvalDetail{
			{Criterion: core.AcceptanceCriterion{ID: "ac_1"}, Diagnostic: "compiles"},
		},
	}
	if err := adapter.PersistCompletion(taskID, "task done", evalResult, 2); err != nil {
		t.Fatalf("PersistCompletion failed: %v", err)
	}

	// 8. LoadTaskState — verify full round-trip
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

	// Criteria
	if len(state.Criteria) != 2 {
		t.Errorf("Criteria: got %d, want 2", len(state.Criteria))
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
	if err := store.SaveTask(TaskRecord{
		ID: "done-task", SessionID: sessionID, OriginalRequest: "old",
		RoutingDecision: json.RawMessage(`{}`), Plan: json.RawMessage(`{}`),
		Criteria: json.RawMessage(`[]`), EvalResult: json.RawMessage(`{}`),
		Reflections: json.RawMessage(`[]`), Status: "completed", CreatedAt: time.Now(),
	}); err != nil {
		t.Fatalf("SaveTask failed: %v", err)
	}

	// Create an in-progress task
	if err := store.SaveTask(TaskRecord{
		ID: "active-task", SessionID: sessionID, OriginalRequest: "current",
		RoutingDecision: json.RawMessage(`{}`), Plan: json.RawMessage(`{}`),
		Criteria: json.RawMessage(`[]`), EvalResult: json.RawMessage(`{}`),
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
	if err := store.SaveTask(TaskRecord{
		ID: "done-task", SessionID: sessionID, OriginalRequest: "old",
		RoutingDecision: json.RawMessage(`{}`), Plan: json.RawMessage(`{}`),
		Criteria: json.RawMessage(`[]`), EvalResult: json.RawMessage(`{}`),
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

func TestTaskStoreAdapter_PersistCompletion_NilEval(t *testing.T) {
	store, sessionID, cleanup := setupTestStoreWithSession(t)
	defer cleanup()

	adapter := NewTaskStoreAdapter(store)
	taskID := "adapter-nil-eval"

	if err := adapter.PersistNewTask(taskID, sessionID, "test"); err != nil {
		t.Fatalf("PersistNewTask failed: %v", err)
	}

	// PersistCompletion with nil evalResult should not error
	if err := adapter.PersistCompletion(taskID, "done", nil, 1); err != nil {
		t.Fatalf("PersistCompletion failed: %v", err)
	}

	state, err := adapter.LoadTaskState(taskID)
	if err != nil {
		t.Fatalf("LoadTaskState failed: %v", err)
	}
	if state.Status != "completed" {
		t.Errorf("Status: got %q", state.Status)
	}
}
