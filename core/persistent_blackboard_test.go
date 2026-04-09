package core

import (
	"errors"
	"log/slog"
	"os"
	"sync"
	"testing"
	"time"
)

// ---------------------------------------------------------------------------
// Mock TaskPersistence
// ---------------------------------------------------------------------------

type newTaskCall struct {
	taskID, sessionID, originalRequest string
}

type planCall struct {
	taskID string
	plan   *Plan
}

type criteriaCall struct {
	taskID   string
	criteria []AcceptanceCriterion
}

type routingCall struct {
	taskID  string
	routing *RoutingDecision
}

type stepResultCall struct {
	taskID, stepID, summary, fullOutput, errorText string
	steps                                          []Step
}

type reflectionCall struct {
	taskID     string
	reflection Reflection
}

type completionCall struct {
	taskID, finalOutput string
	evalResult          *EvalResult
	attemptCount        int
}

type failureCall struct {
	taskID string
}

type mockTaskPersistence struct {
	mu sync.Mutex

	newTaskCalls    []newTaskCall
	planCalls       []planCall
	criteriaCalls   []criteriaCall
	routingCalls    []routingCall
	stepResultCalls []stepResultCall
	reflectionCalls []reflectionCall
	completionCalls []completionCall
	failureCalls    []failureCall

	// Control error behavior
	persistError error

	// For LoadTaskState
	loadState *TaskState
	loadErr   error
}

func (m *mockTaskPersistence) PersistNewTask(taskID, sessionID, originalRequest string) error {
	m.mu.Lock()
	defer m.mu.Unlock()
	m.newTaskCalls = append(m.newTaskCalls, newTaskCall{taskID, sessionID, originalRequest})
	return m.persistError
}

func (m *mockTaskPersistence) PersistPlan(taskID string, plan *Plan) error {
	m.mu.Lock()
	defer m.mu.Unlock()
	m.planCalls = append(m.planCalls, planCall{taskID, plan})
	return m.persistError
}

func (m *mockTaskPersistence) PersistCriteria(taskID string, criteria []AcceptanceCriterion) error {
	m.mu.Lock()
	defer m.mu.Unlock()
	m.criteriaCalls = append(m.criteriaCalls, criteriaCall{taskID, criteria})
	return m.persistError
}

func (m *mockTaskPersistence) PersistRouting(taskID string, routing *RoutingDecision) error {
	m.mu.Lock()
	defer m.mu.Unlock()
	m.routingCalls = append(m.routingCalls, routingCall{taskID, routing})
	return m.persistError
}

func (m *mockTaskPersistence) PersistStepResult(taskID, stepID, summary, fullOutput, errorText string, steps []Step) error {
	m.mu.Lock()
	defer m.mu.Unlock()
	m.stepResultCalls = append(m.stepResultCalls, stepResultCall{taskID, stepID, summary, fullOutput, errorText, steps})
	return m.persistError
}

func (m *mockTaskPersistence) PersistReflection(taskID string, r Reflection) error {
	m.mu.Lock()
	defer m.mu.Unlock()
	m.reflectionCalls = append(m.reflectionCalls, reflectionCall{taskID, r})
	return m.persistError
}

func (m *mockTaskPersistence) PersistCompletion(taskID, finalOutput string, evalResult *EvalResult, attemptCount int) error {
	m.mu.Lock()
	defer m.mu.Unlock()
	m.completionCalls = append(m.completionCalls, completionCall{taskID, finalOutput, evalResult, attemptCount})
	return m.persistError
}

func (m *mockTaskPersistence) PersistFailure(taskID string) error {
	m.mu.Lock()
	defer m.mu.Unlock()
	m.failureCalls = append(m.failureCalls, failureCall{taskID})
	return m.persistError
}

func (m *mockTaskPersistence) LoadTaskState(taskID string) (*TaskState, error) {
	m.mu.Lock()
	defer m.mu.Unlock()
	return m.loadState, m.loadErr
}

func (m *mockTaskPersistence) GetUnfinishedTaskID(sessionID string) (string, error) {
	return "", nil
}

// ---------------------------------------------------------------------------
// Tests
// ---------------------------------------------------------------------------

func testLogger() *slog.Logger {
	return slog.New(slog.NewTextHandler(os.Stderr, &slog.HandlerOptions{Level: slog.LevelError}))
}

func TestPersistentBlackboard_SetOriginalRequest(t *testing.T) {
	mock := &mockTaskPersistence{}
	pb := NewPersistentBlackboard("t1", "s1", mock, testLogger())

	pb.SetOriginalRequest("build a CLI tool")

	// Verify delegation
	if got := pb.GetOriginalRequest(); got != "build a CLI tool" {
		t.Errorf("GetOriginalRequest: got %q, want %q", got, "build a CLI tool")
	}

	// Verify persistence call
	mock.mu.Lock()
	defer mock.mu.Unlock()
	if len(mock.newTaskCalls) != 1 {
		t.Fatalf("expected 1 PersistNewTask call, got %d", len(mock.newTaskCalls))
	}
	c := mock.newTaskCalls[0]
	if c.taskID != "t1" || c.sessionID != "s1" || c.originalRequest != "build a CLI tool" {
		t.Errorf("PersistNewTask args: got %+v", c)
	}
}

func TestPersistentBlackboard_SetCriteria(t *testing.T) {
	mock := &mockTaskPersistence{}
	pb := NewPersistentBlackboard("t1", "s1", mock, testLogger())

	criteria := []AcceptanceCriterion{
		{ID: "ac_1", Description: "must compile"},
	}
	pb.SetCriteria(criteria)

	// Verify delegation
	got := pb.GetCriteria()
	if len(got) != 1 || got[0].ID != "ac_1" {
		t.Errorf("GetCriteria mismatch: %v", got)
	}

	// Verify persistence
	mock.mu.Lock()
	defer mock.mu.Unlock()
	if len(mock.criteriaCalls) != 1 {
		t.Fatalf("expected 1 PersistCriteria call, got %d", len(mock.criteriaCalls))
	}
	if mock.criteriaCalls[0].taskID != "t1" {
		t.Errorf("PersistCriteria taskID: got %q", mock.criteriaCalls[0].taskID)
	}
}

func TestPersistentBlackboard_SetPlan(t *testing.T) {
	mock := &mockTaskPersistence{}
	pb := NewPersistentBlackboard("t1", "s1", mock, testLogger())

	plan := &Plan{Steps: []PlanStep{{ID: "step_1", Description: "write code"}}}
	pb.SetPlan(plan)

	// Verify delegation
	got := pb.GetPlan()
	if got == nil || len(got.Steps) != 1 || got.Steps[0].ID != "step_1" {
		t.Errorf("GetPlan mismatch: %v", got)
	}

	// Verify persistence
	mock.mu.Lock()
	defer mock.mu.Unlock()
	if len(mock.planCalls) != 1 {
		t.Fatalf("expected 1 PersistPlan call, got %d", len(mock.planCalls))
	}
}

func TestPersistentBlackboard_SetStepResult(t *testing.T) {
	mock := &mockTaskPersistence{}
	pb := NewPersistentBlackboard("t1", "s1", mock, testLogger())

	testErr := errors.New("step failed")
	steps := []Step{{Thought: "thinking"}}
	pb.SetStepResult("step_1", "output text", testErr, steps)

	// Verify delegation
	r, ok := pb.GetStepResult("step_1")
	if !ok {
		t.Fatal("expected to find step result")
	}
	if r.FullOutput != "output text" {
		t.Errorf("FullOutput mismatch: got %q", r.FullOutput)
	}

	// Verify persistence
	mock.mu.Lock()
	defer mock.mu.Unlock()
	if len(mock.stepResultCalls) != 1 {
		t.Fatalf("expected 1 PersistStepResult call, got %d", len(mock.stepResultCalls))
	}
	sc := mock.stepResultCalls[0]
	if sc.taskID != "t1" || sc.stepID != "step_1" {
		t.Errorf("PersistStepResult IDs: got taskID=%q stepID=%q", sc.taskID, sc.stepID)
	}
	if sc.fullOutput != "output text" {
		t.Errorf("PersistStepResult fullOutput: got %q", sc.fullOutput)
	}
	if sc.errorText != "step failed" {
		t.Errorf("PersistStepResult errorText: got %q", sc.errorText)
	}
}

func TestPersistentBlackboard_AddReflection(t *testing.T) {
	mock := &mockTaskPersistence{}
	pb := NewPersistentBlackboard("t1", "s1", mock, testLogger())

	r := Reflection{Summary: "things went wrong", SuggestedAction: "retry"}
	pb.AddReflection(r)

	// Verify delegation
	got := pb.GetReflections()
	if len(got) != 1 || got[0].Summary != "things went wrong" {
		t.Errorf("GetReflections mismatch: %v", got)
	}

	// Verify persistence
	mock.mu.Lock()
	defer mock.mu.Unlock()
	if len(mock.reflectionCalls) != 1 {
		t.Fatalf("expected 1 PersistReflection call, got %d", len(mock.reflectionCalls))
	}
	if mock.reflectionCalls[0].reflection.Summary != "things went wrong" {
		t.Errorf("PersistReflection summary: got %q", mock.reflectionCalls[0].reflection.Summary)
	}
}

func TestPersistentBlackboard_SetFinalResult(t *testing.T) {
	mock := &mockTaskPersistence{}
	pb := NewPersistentBlackboard("t1", "s1", mock, testLogger())

	pb.SetFinalResult("task completed")

	// Verify delegation
	if got := pb.GetFinalResult(); got != "task completed" {
		t.Errorf("GetFinalResult: got %q", got)
	}

	// SetFinalResult does NOT call persistence — verify no completion calls
	mock.mu.Lock()
	defer mock.mu.Unlock()
	if len(mock.completionCalls) != 0 {
		t.Errorf("expected 0 PersistCompletion calls, got %d", len(mock.completionCalls))
	}
}

func TestPersistentBlackboard_CompleteTask(t *testing.T) {
	mock := &mockTaskPersistence{}
	pb := NewPersistentBlackboard("t1", "s1", mock, testLogger())

	pb.SetFinalResult("all done")
	evalResult := &EvalResult{AllPassed: true}
	pb.CompleteTask(evalResult, 3)

	mock.mu.Lock()
	defer mock.mu.Unlock()
	if len(mock.completionCalls) != 1 {
		t.Fatalf("expected 1 PersistCompletion call, got %d", len(mock.completionCalls))
	}
	cc := mock.completionCalls[0]
	if cc.taskID != "t1" {
		t.Errorf("taskID: got %q", cc.taskID)
	}
	if cc.finalOutput != "all done" {
		t.Errorf("finalOutput: got %q", cc.finalOutput)
	}
	if cc.attemptCount != 3 {
		t.Errorf("attemptCount: got %d", cc.attemptCount)
	}
	if cc.evalResult == nil || !cc.evalResult.AllPassed {
		t.Error("evalResult mismatch")
	}
}

func TestPersistentBlackboard_FailTask(t *testing.T) {
	mock := &mockTaskPersistence{}
	pb := NewPersistentBlackboard("t1", "s1", mock, testLogger())

	pb.FailTask()

	mock.mu.Lock()
	defer mock.mu.Unlock()
	if len(mock.failureCalls) != 1 {
		t.Fatalf("expected 1 PersistFailure call, got %d", len(mock.failureCalls))
	}
	if mock.failureCalls[0].taskID != "t1" {
		t.Errorf("taskID: got %q", mock.failureCalls[0].taskID)
	}
}

func TestPersistentBlackboard_ReadDelegation(t *testing.T) {
	mock := &mockTaskPersistence{}
	pb := NewPersistentBlackboard("t1", "s1", mock, testLogger())

	// Populate via MapBlackboard-level writes
	pb.SetOriginalRequest("req")
	pb.SetCriteria([]AcceptanceCriterion{{ID: "ac_1"}})
	pb.SetPlan(&Plan{Steps: []PlanStep{{ID: "s1", RelevantAC: []string{"ac_1"}}}})
	pb.SetStepResult("s1", "output", nil, nil)
	pb.AddReflection(Reflection{Summary: "r1"})
	pb.SetFinalResult("done")

	// All reads should delegate to MapBlackboard
	if pb.GetOriginalRequest() != "req" {
		t.Error("GetOriginalRequest delegation failed")
	}
	if len(pb.GetCriteria()) != 1 {
		t.Error("GetCriteria delegation failed")
	}
	if pb.GetPlan() == nil {
		t.Error("GetPlan delegation failed")
	}
	if _, ok := pb.GetStepResult("s1"); !ok {
		t.Error("GetStepResult delegation failed")
	}
	if pb.GetStepSummary("s1") == "" {
		t.Error("GetStepSummary delegation failed")
	}
	if len(pb.GetAllStepResults()) != 1 {
		t.Error("GetAllStepResults delegation failed")
	}
	if len(pb.GetReflections()) != 1 {
		t.Error("GetReflections delegation failed")
	}
	if pb.GetFinalResult() != "done" {
		t.Error("GetFinalResult delegation failed")
	}
	results := pb.GetStepsByAC("ac_1")
	if len(results) != 1 {
		t.Errorf("GetStepsByAC delegation failed: got %d", len(results))
	}
}

func TestPersistentBlackboard_BestEffortErrors(t *testing.T) {
	mock := &mockTaskPersistence{persistError: errors.New("storage down")}
	pb := NewPersistentBlackboard("t1", "s1", mock, testLogger())

	// All write methods should NOT panic even though persistence fails
	pb.SetOriginalRequest("req")
	pb.SetCriteria([]AcceptanceCriterion{{ID: "ac_1"}})
	pb.SetPlan(&Plan{Steps: []PlanStep{{ID: "s1"}}})
	pb.SetStepResult("s1", "output", nil, nil)
	pb.AddReflection(Reflection{Summary: "r"})
	pb.SetRouting(&RoutingDecision{Domain: "code"})
	pb.CompleteTask(&EvalResult{}, 1)
	pb.FailTask()

	// Verify the in-memory state still works
	if pb.GetOriginalRequest() != "req" {
		t.Error("in-memory state should work despite persistence errors")
	}
}

func TestPersistentBlackboard_NilLogger(t *testing.T) {
	mock := &mockTaskPersistence{persistError: errors.New("fail")}
	pb := NewPersistentBlackboard("t1", "s1", mock, nil) // nil logger

	// Should not panic
	pb.SetOriginalRequest("req")
	pb.SetCriteria(nil)
	pb.SetPlan(nil)
	pb.SetStepResult("s1", "out", nil, nil)
	pb.AddReflection(Reflection{})
	pb.SetRouting(&RoutingDecision{})
	pb.CompleteTask(nil, 0)
	pb.FailTask()
}

func TestPersistentBlackboard_SetRouting(t *testing.T) {
	mock := &mockTaskPersistence{}
	pb := NewPersistentBlackboard("t1", "s1", mock, testLogger())

	routing := &RoutingDecision{Domain: "code", Complexity: 3}
	pb.SetRouting(routing)

	mock.mu.Lock()
	defer mock.mu.Unlock()
	if len(mock.routingCalls) != 1 {
		t.Fatalf("expected 1 PersistRouting call, got %d", len(mock.routingCalls))
	}
	if mock.routingCalls[0].routing.Domain != "code" {
		t.Errorf("routing domain: got %q", mock.routingCalls[0].routing.Domain)
	}
}

func TestPersistentBlackboard_TaskID(t *testing.T) {
	mock := &mockTaskPersistence{}
	pb := NewPersistentBlackboard("my-task", "s1", mock, nil)

	if pb.TaskID() != "my-task" {
		t.Errorf("TaskID: got %q, want %q", pb.TaskID(), "my-task")
	}
}

func TestRestoreBlackboard(t *testing.T) {
	mock := &mockTaskPersistence{
		loadState: &TaskState{
			TaskID:          "t1",
			SessionID:       "s1",
			OriginalRequest: "build CLI",
			Plan:            &Plan{Steps: []PlanStep{{ID: "step_1", Description: "write code"}}},
			Criteria:        []AcceptanceCriterion{{ID: "ac_1", Description: "must compile"}},
			StepResults: map[string]StepResult{
				"step_1": {StepID: "step_1", Summary: "wrote code", FullOutput: "full output"},
			},
			Reflections: []Reflection{{Summary: "attempt 1 failed", Timestamp: time.Now()}},
			FinalOutput: "completed output",
			Status:      "in_progress",
		},
	}

	pb, err := RestoreBlackboard("t1", "s1", mock, testLogger())
	if err != nil {
		t.Fatalf("RestoreBlackboard failed: %v", err)
	}
	if pb == nil {
		t.Fatal("expected non-nil blackboard")
	}

	// Verify restored state
	if pb.GetOriginalRequest() != "build CLI" {
		t.Errorf("OriginalRequest: got %q", pb.GetOriginalRequest())
	}
	if pb.GetPlan() == nil || len(pb.GetPlan().Steps) != 1 {
		t.Error("Plan not restored correctly")
	}
	if len(pb.GetCriteria()) != 1 {
		t.Error("Criteria not restored correctly")
	}
	sr, ok := pb.GetStepResult("step_1")
	if !ok {
		t.Error("StepResult not restored")
	}
	if sr.Summary != "wrote code" {
		t.Errorf("StepResult summary: got %q", sr.Summary)
	}
	if len(pb.GetReflections()) != 1 {
		t.Error("Reflections not restored correctly")
	}
	if pb.GetFinalResult() != "completed output" {
		t.Errorf("FinalResult: got %q", pb.GetFinalResult())
	}
	if pb.TaskID() != "t1" {
		t.Errorf("TaskID: got %q", pb.TaskID())
	}
}

func TestRestoreBlackboard_NotFound(t *testing.T) {
	mock := &mockTaskPersistence{loadState: nil, loadErr: nil}

	pb, err := RestoreBlackboard("t1", "s1", mock, testLogger())
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if pb != nil {
		t.Error("expected nil blackboard when task not found")
	}
}

func TestRestoreBlackboard_Error(t *testing.T) {
	mock := &mockTaskPersistence{loadErr: errors.New("db error")}

	_, err := RestoreBlackboard("t1", "s1", mock, testLogger())
	if err == nil {
		t.Fatal("expected error")
	}
}
