package session

import (
	"errors"
	"log/slog"
	"os"
	"sync"
	"testing"
	"time"

	"github.com/v0lka/c0wrk/core"
	"github.com/v0lka/sp4rk/agent"
	"github.com/v0lka/sp4rk/agent/router"
	"github.com/v0lka/sp4rk/orchestration"
)

// ---------------------------------------------------------------------------
// Mock TaskPersistence
// ---------------------------------------------------------------------------

type newTaskCall struct {
	taskID, sessionID, originalRequest string
}

type planCall struct {
	taskID string
	plan   *orchestration.Plan
}

type routingCall struct {
	taskID  string
	routing *router.RoutingDecision
}

type stepResultCall struct {
	taskID, stepID, summary, fullOutput, errorText string
	steps                                          []agent.Step
}

type reflectionCall struct {
	taskID     string
	reflection orchestration.Reflection
}

type completionCall struct {
	taskID, finalOutput string
	attemptCount        int
}

type failureCall struct {
	taskID string
}

type mockTaskPersistence struct {
	mu sync.Mutex

	newTaskCalls    []newTaskCall
	planCalls       []planCall
	routingCalls    []routingCall
	stepResultCalls []stepResultCall
	reflectionCalls []reflectionCall
	completionCalls []completionCall
	failureCalls    []failureCall

	// Control error behavior
	persistError error

	// For LoadTaskState
	loadState *core.TaskState
	loadErr   error
}

func (m *mockTaskPersistence) PersistNewTask(taskID, sessionID, originalRequest string) error {
	m.mu.Lock()
	defer m.mu.Unlock()
	m.newTaskCalls = append(m.newTaskCalls, newTaskCall{taskID, sessionID, originalRequest})
	return m.persistError
}

func (m *mockTaskPersistence) PersistPlan(taskID string, plan *orchestration.Plan) error {
	m.mu.Lock()
	defer m.mu.Unlock()
	m.planCalls = append(m.planCalls, planCall{taskID, plan})
	return m.persistError
}

func (m *mockTaskPersistence) PersistRouting(taskID string, routing *router.RoutingDecision) error {
	m.mu.Lock()
	defer m.mu.Unlock()
	m.routingCalls = append(m.routingCalls, routingCall{taskID, routing})
	return m.persistError
}

func (m *mockTaskPersistence) PersistStepResult(taskID, stepID, summary, fullOutput, errorText string, steps []agent.Step) error {
	m.mu.Lock()
	defer m.mu.Unlock()
	m.stepResultCalls = append(m.stepResultCalls, stepResultCall{taskID, stepID, summary, fullOutput, errorText, steps})
	return m.persistError
}

func (m *mockTaskPersistence) PersistReflection(taskID string, r orchestration.Reflection) error {
	m.mu.Lock()
	defer m.mu.Unlock()
	m.reflectionCalls = append(m.reflectionCalls, reflectionCall{taskID, r})
	return m.persistError
}

func (m *mockTaskPersistence) PersistCompletion(taskID, finalOutput string, attemptCount int) error {
	m.mu.Lock()
	defer m.mu.Unlock()
	m.completionCalls = append(m.completionCalls, completionCall{taskID, finalOutput, attemptCount})
	return m.persistError
}

func (m *mockTaskPersistence) PersistFailure(taskID string) error {
	m.mu.Lock()
	defer m.mu.Unlock()
	m.failureCalls = append(m.failureCalls, failureCall{taskID})
	return m.persistError
}

func (m *mockTaskPersistence) PersistCancellation(taskID string) error {
	m.mu.Lock()
	defer m.mu.Unlock()
	return m.persistError
}

func (m *mockTaskPersistence) PersistFacts(taskID string, facts []orchestration.Fact) error {
	m.mu.Lock()
	defer m.mu.Unlock()
	return m.persistError
}

func (m *mockTaskPersistence) LoadTaskState(taskID string) (*core.TaskState, error) {
	m.mu.Lock()
	defer m.mu.Unlock()
	return m.loadState, m.loadErr
}

func (m *mockTaskPersistence) GetUnfinishedTaskID(sessionID string) (string, error) {
	return "", nil
}

func (m *mockTaskPersistence) ReactivateTask(taskID string) error {
	m.mu.Lock()
	defer m.mu.Unlock()
	return m.persistError
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

func TestPersistentBlackboard_SetPlan(t *testing.T) {
	mock := &mockTaskPersistence{}
	pb := NewPersistentBlackboard("t1", "s1", mock, testLogger())

	plan := &orchestration.Plan{Steps: []orchestration.PlanStep{{ID: "step_1", Description: "write code"}}}
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
	steps := []agent.Step{{Thought: "thinking"}}
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

	r := orchestration.Reflection{Summary: "things went wrong", SuggestedAction: "retry"}
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
		t.Errorf("GetFinalResult: got %q, got", got)
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
	pb.CompleteTask(3)

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
	pb.SetPlan(&orchestration.Plan{Steps: []orchestration.PlanStep{{ID: "s1"}}})
	pb.SetStepResult("s1", "output", nil, nil)
	pb.AddReflection(orchestration.Reflection{Summary: "r1"})
	pb.SetFinalResult("done")

	// All reads should delegate to MapBlackboard
	if pb.GetOriginalRequest() != "req" {
		t.Error("GetOriginalRequest delegation failed")
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
}

func TestPersistentBlackboard_BestEffortErrors(t *testing.T) {
	mock := &mockTaskPersistence{persistError: errors.New("storage down")}
	pb := NewPersistentBlackboard("t1", "s1", mock, testLogger())

	// All write methods should NOT panic even though persistence fails
	pb.SetOriginalRequest("req")
	pb.SetPlan(&orchestration.Plan{Steps: []orchestration.PlanStep{{ID: "s1"}}})
	pb.SetStepResult("s1", "output", nil, nil)
	pb.AddReflection(orchestration.Reflection{Summary: "r"})
	pb.SetRouting(&router.RoutingDecision{Domain: "code"})
	pb.CompleteTask(1)
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
	pb.SetPlan(nil)
	pb.SetStepResult("s1", "out", nil, nil)
	pb.AddReflection(orchestration.Reflection{})
	pb.SetRouting(&router.RoutingDecision{})
	pb.CompleteTask(0)
	pb.FailTask()
}

func TestPersistentBlackboard_SetRouting(t *testing.T) {
	mock := &mockTaskPersistence{}
	pb := NewPersistentBlackboard("t1", "s1", mock, testLogger())

	routing := &router.RoutingDecision{Domain: "code", Complexity: 3}
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
		loadState: &core.TaskState{
			TaskID:          "t1",
			SessionID:       "s1",
			OriginalRequest: "build CLI",
			Plan:            &orchestration.Plan{Steps: []orchestration.PlanStep{{ID: "step_1", Description: "write code"}}},
			StepResults: map[string]orchestration.StepResult{
				"step_1": {StepID: "step_1", Summary: "wrote code", FullOutput: "full output"},
			},
			Reflections: []orchestration.Reflection{{Summary: "attempt 1 failed", Timestamp: time.Now()}},
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

func TestPersistentBlackboard_NotifyChanged(t *testing.T) {
	mock := &mockTaskPersistence{}
	pb := NewPersistentBlackboard("t1", "s1", mock, testLogger())

	var changes []string
	var mu sync.Mutex
	pb.SetOnChanged(func(changeType string) {
		mu.Lock()
		changes = append(changes, changeType)
		mu.Unlock()
	})

	// Each write method should trigger a notifyChanged call.
	pb.SetPlan(&orchestration.Plan{Steps: []orchestration.PlanStep{{ID: "s1", Description: "d"}}})
	pb.SetStepResult("step_1", "output", nil, nil)
	pb.AddReflection(orchestration.Reflection{Summary: "r1"})
	pb.StoreFact(orchestration.Fact{Keywords: []string{"k"}, Content: "c"})
	pb.CompleteTask(1)

	// Wait briefly for async persistence goroutines to finish.
	time.Sleep(100 * time.Millisecond)

	mu.Lock()
	got := append([]string{}, changes...)
	mu.Unlock()

	expected := []string{"plan", "step_result", "reflection", "fact", "completed"}
	if len(got) != len(expected) {
		t.Fatalf("expected %d change notifications, got %d: %v", len(expected), len(got), got)
	}
	for i, want := range expected {
		if got[i] != want {
			t.Errorf("change[%d]: got %q, want %q", i, got[i], want)
		}
	}
}

func TestPersistentBlackboard_NotifyChanged_NilSafe(t *testing.T) {
	mock := &mockTaskPersistence{}
	pb := NewPersistentBlackboard("t1", "s1", mock, testLogger())

	// No callback set — should not panic.
	pb.SetPlan(&orchestration.Plan{Steps: []orchestration.PlanStep{{ID: "s1", Description: "d"}}})
	pb.StoreFact(orchestration.Fact{Keywords: []string{"k"}, Content: "c"})
	pb.CompleteTask(0)
}
