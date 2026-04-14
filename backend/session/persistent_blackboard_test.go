package session

import (
	"errors"
	"log/slog"
	"os"
	"sync"
	"testing"
	"time"

	"github.com/user/agent/core"
)

// ---------------------------------------------------------------------------
// Mock TaskPersistence
// ---------------------------------------------------------------------------

type newTaskCall struct {
	taskID, sessionID, originalRequest string
}

type planCall struct {
	taskID string
	plan   *core.Plan
}

type routingCall struct {
	taskID  string
	routing *core.RoutingDecision
}

type stepResultCall struct {
	taskID, stepID, summary, fullOutput, errorText string
	steps                                          []core.Step
}

type reflectionCall struct {
	taskID     string
	reflection core.Reflection
}

type completionCall struct {
	taskID, finalOutput string
	attemptCount        int
}

type failureCall struct {
	taskID string
}

type stepFileChangesCall struct {
	taskID, stepID string
	changes        []core.FileChange
}

type mockTaskPersistence struct {
	mu sync.Mutex

	newTaskCalls         []newTaskCall
	planCalls            []planCall
	routingCalls         []routingCall
	stepResultCalls      []stepResultCall
	reflectionCalls      []reflectionCall
	completionCalls      []completionCall
	failureCalls         []failureCall
	stepFileChangesCalls []stepFileChangesCall

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

func (m *mockTaskPersistence) PersistPlan(taskID string, plan *core.Plan) error {
	m.mu.Lock()
	defer m.mu.Unlock()
	m.planCalls = append(m.planCalls, planCall{taskID, plan})
	return m.persistError
}

func (m *mockTaskPersistence) PersistRouting(taskID string, routing *core.RoutingDecision) error {
	m.mu.Lock()
	defer m.mu.Unlock()
	m.routingCalls = append(m.routingCalls, routingCall{taskID, routing})
	return m.persistError
}

func (m *mockTaskPersistence) PersistStepResult(taskID, stepID, summary, fullOutput, errorText string, steps []core.Step) error {
	m.mu.Lock()
	defer m.mu.Unlock()
	m.stepResultCalls = append(m.stepResultCalls, stepResultCall{taskID, stepID, summary, fullOutput, errorText, steps})
	return m.persistError
}

func (m *mockTaskPersistence) PersistReflection(taskID string, r core.Reflection) error {
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

func (m *mockTaskPersistence) PersistStepFileChanges(taskID, stepID string, changes []core.FileChange) error {
	m.mu.Lock()
	defer m.mu.Unlock()
	m.stepFileChangesCalls = append(m.stepFileChangesCalls, stepFileChangesCall{taskID, stepID, changes})
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

	plan := &core.Plan{Steps: []core.PlanStep{{ID: "step_1", Description: "write code"}}}
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
	steps := []core.Step{{Thought: "thinking"}}
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

	r := core.Reflection{Summary: "things went wrong", SuggestedAction: "retry"}
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
	pb.SetPlan(&core.Plan{Steps: []core.PlanStep{{ID: "s1"}}})
	pb.SetStepResult("s1", "output", nil, nil)
	pb.AddReflection(core.Reflection{Summary: "r1"})
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
	pb.SetPlan(&core.Plan{Steps: []core.PlanStep{{ID: "s1"}}})
	pb.SetStepResult("s1", "output", nil, nil)
	pb.AddReflection(core.Reflection{Summary: "r"})
	pb.SetRouting(&core.RoutingDecision{Domain: "code"})
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
	pb.AddReflection(core.Reflection{})
	pb.SetRouting(&core.RoutingDecision{})
	pb.CompleteTask(0)
	pb.FailTask()
}

func TestPersistentBlackboard_SetRouting(t *testing.T) {
	mock := &mockTaskPersistence{}
	pb := NewPersistentBlackboard("t1", "s1", mock, testLogger())

	routing := &core.RoutingDecision{Domain: "code", Complexity: 3}
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
			Plan:            &core.Plan{Steps: []core.PlanStep{{ID: "step_1", Description: "write code"}}},
			StepResults: map[string]core.StepResult{
				"step_1": {StepID: "step_1", Summary: "wrote code", FullOutput: "full output"},
			},
			Reflections: []core.Reflection{{Summary: "attempt 1 failed", Timestamp: time.Now()}},
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

func TestPersistentBlackboard_SetStepFileChanges(t *testing.T) {
	mock := &mockTaskPersistence{}
	pb := NewPersistentBlackboard("t1", "s1", mock, testLogger())

	changes := []core.FileChange{
		{Path: "main.go", Operation: "MODIFY", Diff: "--- a/main.go\n+++ b/main.go", SizeBytes: 1024},
		{Path: "new.go", Operation: "CREATE", SizeBytes: 256},
	}
	pb.SetStepFileChanges("step_1", changes)

	// Verify delegation to MapBlackboard
	got := pb.GetStepFileChanges("step_1")
	if len(got) != 2 {
		t.Fatalf("expected 2 file changes, got %d", len(got))
	}
	if got[0].Path != "main.go" || got[0].Operation != "MODIFY" {
		t.Errorf("first file change mismatch: %+v", got[0])
	}
	if got[1].Path != "new.go" || got[1].Operation != "CREATE" {
		t.Errorf("second file change mismatch: %+v", got[1])
	}

	// Verify persistence call
	mock.mu.Lock()
	defer mock.mu.Unlock()
	if len(mock.stepFileChangesCalls) != 1 {
		t.Fatalf("expected 1 PersistStepFileChanges call, got %d", len(mock.stepFileChangesCalls))
	}
	c := mock.stepFileChangesCalls[0]
	if c.taskID != "t1" || c.stepID != "step_1" {
		t.Errorf("PersistStepFileChanges args: got taskID=%q stepID=%q", c.taskID, c.stepID)
	}
	if len(c.changes) != 2 {
		t.Errorf("PersistStepFileChanges changes: got %d", len(c.changes))
	}
}

func TestPersistentBlackboard_SetStepFileChanges_PersistError(t *testing.T) {
	mock := &mockTaskPersistence{persistError: errors.New("storage down")}
	pb := NewPersistentBlackboard("t1", "s1", mock, testLogger())

	changes := []core.FileChange{
		{Path: "main.go", Operation: "MODIFY"},
	}
	// Should not panic even though persistence fails
	pb.SetStepFileChanges("step_1", changes)

	// In-memory state should still work
	got := pb.GetStepFileChanges("step_1")
	if len(got) != 1 {
		t.Fatalf("expected 1 file change in memory, got %d", len(got))
	}
	if got[0].Path != "main.go" {
		t.Errorf("file change path mismatch: got %q", got[0].Path)
	}
}

func TestRestoreBlackboard_WithFileChanges(t *testing.T) {
	mock := &mockTaskPersistence{
		loadState: &core.TaskState{
			TaskID:          "t1",
			SessionID:       "s1",
			OriginalRequest: "implement feature",
			Plan:            &core.Plan{Steps: []core.PlanStep{{ID: "step_1", Description: "write code"}}},
			StepResults: map[string]core.StepResult{
				"step_1": {StepID: "step_1", Summary: "wrote code", FullOutput: "full output"},
			},
			FileChanges: map[string][]core.FileChange{
				"step_1": {
					{Path: "main.go", Operation: "MODIFY", Diff: "some diff", SizeBytes: 512},
					{Path: "helper.go", Operation: "CREATE", SizeBytes: 128},
				},
			},
			Status: "in_progress",
		},
	}

	pb, err := RestoreBlackboard("t1", "s1", mock, testLogger())
	if err != nil {
		t.Fatalf("RestoreBlackboard failed: %v", err)
	}
	if pb == nil {
		t.Fatal("expected non-nil blackboard")
	}

	// Verify file changes were restored
	changes := pb.GetStepFileChanges("step_1")
	if len(changes) != 2 {
		t.Fatalf("expected 2 file changes, got %d", len(changes))
	}
	if changes[0].Path != "main.go" || changes[0].Operation != "MODIFY" {
		t.Errorf("first file change mismatch: %+v", changes[0])
	}
	if changes[1].Path != "helper.go" || changes[1].Operation != "CREATE" {
		t.Errorf("second file change mismatch: %+v", changes[1])
	}

	// Verify aggregated file changes work
	all := pb.GetAllFileChanges()
	if len(all) != 1 {
		t.Errorf("expected 1 step in all file changes, got %d", len(all))
	}
	if len(all["step_1"]) != 2 {
		t.Errorf("expected 2 changes for step_1, got %d", len(all["step_1"]))
	}
}
