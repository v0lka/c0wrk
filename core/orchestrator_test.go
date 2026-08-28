package core

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"
	"unicode/utf8"

	"github.com/v0lka/c0wrk/core/goal"
	coretools "github.com/v0lka/c0wrk/core/tools"
	"github.com/v0lka/sp4rk/agent"
	"github.com/v0lka/sp4rk/agent/router"
	"github.com/v0lka/sp4rk/llm"
	"github.com/v0lka/sp4rk/orchestration"
	"github.com/v0lka/sp4rk/prompt"
	"github.com/v0lka/sp4rk/skills"
	tools "github.com/v0lka/sp4rk/tools"
	"github.com/v0lka/sp4rk/tools/builtins"
)

// mockTool implements tools.Tool for testing.
type mockTool struct {
	name        string
	description string
	result      tools.ToolResult
	group       tools.ToolGroup
}

func (m *mockTool) Name() string                    { return m.name }
func (m *mockTool) Description() string             { return m.description }
func (m *mockTool) InputSchema() json.RawMessage    { return json.RawMessage(`{"type":"object"}`) }
func (m *mockTool) DefaultPolicy() tools.ToolPolicy { return tools.PolicyAlwaysAllow }
func (m *mockTool) IsUntrusted() bool               { return false }
func (m *mockTool) Group() tools.ToolGroup          { return m.group }
func (m *mockTool) Execute(ctx context.Context, input json.RawMessage) (tools.ToolResult, error) {
	return m.result, nil
}

// createTestRegistry creates a sp4rk ToolRegistry with mock tools for
// testing (the plain SDK registry — no c0wrk policy layer involved).
func createTestRegistry() *tools.ToolRegistry {
	reg := tools.NewToolRegistry()
	// Register a mock bash_exec tool
	reg.Register(&mockTool{
		name:        "bash_exec",
		description: "Execute bash commands",
		group:       tools.GroupExecute,
		result:      tools.ToolResult{Content: "PASSED:Build succeeded", IsError: false},
	})
	// Register a mock write_file tool
	reg.Register(&mockTool{
		name:        "write_file",
		description: "Write files",
		group:       tools.GroupLocalWrite,
		result:      tools.ToolResult{Content: "File written", IsError: false},
	})
	return reg
}

// Helper to create a context factory for tests
func testContextFactory(systemPrompt string, modelMeta llm.ModelMetadata, compactionStrategy string, _ ...orchestration.PruningOverride) ContextManager {
	return &mockContextManager{
		systemPrompt:   systemPrompt,
		taskDefinition: "",
	}
}

// TestOrchestrator_HandleResultContainsRoutingDecision verifies HandleResult always has routing info.
func TestOrchestrator_HandleResultContainsRoutingDecision(t *testing.T) {
	callIdx := 0
	mockLLM := &mockLLMCaller{
		callFn: func(ctx context.Context, req llm.ChatRequest) (*llm.ChatResponse, error) {
			callIdx++
			if callIdx == 1 {
				// Router
				return &llm.ChatResponse{
					Message: llm.Message{
						Role:    "assistant",
						Content: `{"domain": "general", "complexity": 2, "compaction_strategy": "sliding_window", "suggested_tools": [], "needs_clarification": false}`,
					},
					StopReason: "end_turn",
				}, nil
			}
			// Planner — return a valid single-step plan
			return &llm.ChatResponse{
				Message: llm.Message{
					Role:    "assistant",
					Content: `{"steps": [{"id": "step_1", "summary": "Test step", "description": "What: test\nHow: test\nWhere: test\nAcceptance Criteria: pass", "depends_on": [], "parallelizable": false, "estimated_tools": []}]}`,
				},
				StopReason: "end_turn",
			}, nil
		},
	}

	registry := createTestRegistry()
	counter := llm.NewSimpleTokenCounter()

	orchestrator := NewOrchestrator(OrchestratorConfig{}, OrchestratorDeps{
		Router:         newCoreRouter(mockLLM, 5),
		LLM:            mockLLM,
		ToolExec:       registry,
		ToolRegistry:   registry,
		TokenCounter:   counter,
		ContextFactory: testContextFactory,
		CircuitBreaker: defaultCircuitBreakerConfig,
	})

	result, err := orchestrator.HandleMessage(context.Background(), "test", "", HandleOptions{})
	if err != nil {
		t.Fatalf("Handle failed: %v", err)
	}

	if result.RoutingDecision == nil {
		t.Fatal("RoutingDecision should not be nil")
	}
}

// TestFinishTool_DefaultPolicy tests the finish tool default policy.
func TestFinishTool_DefaultPolicy(t *testing.T) {
	ft := agent.NewFinishTool()
	if ft.DefaultPolicy() != tools.PolicyAlwaysAllow {
		t.Errorf("expected PolicyAlwaysAllow, got %v", ft.DefaultPolicy())
	}
}

// TestFinishTool_Execute tests the finish tool execution.
func TestFinishTool_Execute(t *testing.T) {
	tests := []struct {
		name        string
		input       string
		wantContent string
		wantError   bool
	}{
		{"valid input", `{"answer":"done!"}`, "done!", false},
		{"empty answer", `{"answer":""}`, "", false},
		{"invalid json", `{invalid}`, "", true},
		{"missing answer field", `{"other":"val"}`, "", false},
	}

	ft := agent.NewFinishTool()
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			result, err := ft.Execute(context.Background(), json.RawMessage(tt.input))
			if err != nil {
				t.Fatalf("unexpected Go error: %v", err)
			}
			if result.IsError != tt.wantError {
				t.Errorf("IsError = %v, want %v (content: %s)", result.IsError, tt.wantError, result.Content)
			}
			if !tt.wantError && result.Content != tt.wantContent {
				t.Errorf("Content = %q, want %q", result.Content, tt.wantContent)
			}
		})
	}
}

type mockTaskStore struct {
	taskState *TaskState
	loadErr   error
}

func (m *mockTaskStore) PersistNewTask(taskID, sessionID, originalRequest string) error {
	return nil
}

func (m *mockTaskStore) PersistPlan(taskID string, plan *orchestration.Plan) error { return nil }
func (m *mockTaskStore) PersistRouting(taskID string, routing *router.RoutingDecision) error {
	return nil
}
func (m *mockTaskStore) PersistStepResult(taskID, stepID, summary, fullOutput, errorText string, steps []agent.Step) error {
	return nil
}
func (m *mockTaskStore) PersistReflection(taskID string, r orchestration.Reflection) error {
	return nil
}
func (m *mockTaskStore) PersistCompletion(taskID, finalOutput string, attemptCount int) error {
	return nil
}
func (m *mockTaskStore) PersistFailure(taskID string) error                           { return nil }
func (m *mockTaskStore) PersistCancellation(taskID string) error                      { return nil }
func (m *mockTaskStore) PersistPause(taskID string) error                             { return nil }
func (m *mockTaskStore) PersistFacts(taskID string, facts []orchestration.Fact) error { return nil }
func (m *mockTaskStore) PersistAttachments(taskID string, attachments []orchestration.Attachment) error {
	return nil
}
func (m *mockTaskStore) SaveTrajectory(taskID string, steps []agent.Step) error   { return nil }
func (m *mockTaskStore) LoadTrajectory(taskID string) ([]agent.Step, error)       { return nil, nil }
func (m *mockTaskStore) PersistGoalState(taskID string, gs *goal.GoalState) error { return nil }
func (m *mockTaskStore) LoadGoalState(taskID string) (*goal.GoalState, error)     { return nil, nil }
func (m *mockTaskStore) LoadTaskState(taskID string) (*TaskState, error) {
	if m.loadErr != nil {
		return nil, m.loadErr
	}
	return m.taskState, nil
}
func (m *mockTaskStore) GetUnfinishedTaskID(sessionID string) (string, error) {
	return "", nil
}
func (m *mockTaskStore) ReactivateTask(taskID string) error { return nil }

// TestHandleMessage_ReactivatesTask verifies that ReactivateTask is called on any continuation.
func TestHandleMessage_ReactivatesTask(t *testing.T) {
	callIdx := 0
	reactivateCalled := false

	mockLLM := &mockLLMCaller{
		callFn: func(ctx context.Context, req llm.ChatRequest) (*llm.ChatResponse, error) {
			callIdx++
			switch callIdx {
			case 1: // Router
				return &llm.ChatResponse{
					Message: llm.Message{
						Role:    "assistant",
						Content: `{"domain": "code", "complexity": 3, "compaction_strategy": "sliding_window", "suggested_tools": [], "needs_clarification": false}`,
					},
					StopReason: "end_turn",
				}, nil
			case 2: // PlanContinuation
				return &llm.ChatResponse{
					Message: llm.Message{
						Role:    "assistant",
						Content: `{"steps": [{"id": "cont_1", "description": "cont", "depends_on": [], "parallelizable": true}]}`,
					},
					StopReason: "end_turn",
				}, nil
			default: // Executor
				return &llm.ChatResponse{
					Message: llm.Message{
						Role:    "assistant",
						Content: "Done",
						ToolCalls: []llm.ToolCall{{
							ID:    "call_1",
							Name:  "finish",
							Input: json.RawMessage(`{"answer": "done"}`),
						}},
					},
					StopReason: "tool_use",
				}, nil
			}
		},
	}

	registry := createTestRegistry()
	counter := llm.NewSimpleTokenCounter()

	r := newCoreRouter(mockLLM, 5)

	orchestrator := NewOrchestrator(OrchestratorConfig{}, OrchestratorDeps{
		Router:         r,
		LLM:            mockLLM,
		ToolExec:       registry,
		ToolRegistry:   registry,
		TokenCounter:   counter,
		ContextFactory: testContextFactory,
		CircuitBreaker: defaultCircuitBreakerConfig,
	})
	// Create mock store that tracks ReactivateTask calls
	mockStore := &mockTaskStoreWithReactivate{
		taskState: &TaskState{
			TaskID:          "task-123",
			SessionID:       "session-456",
			OriginalRequest: "original task",
			Status:          "completed",
			Plan: &orchestration.Plan{
				Steps: []orchestration.PlanStep{
					{ID: "step_1", Description: "First step"},
				},
			},
			StepResults: map[string]orchestration.StepResult{
				"step_1": {StepID: "step_1", FullOutput: "output 1"},
			},
		},
		reactivateFn: func(taskID string) error {
			reactivateCalled = true
			return nil
		},
	}
	orchestrator.SetTaskStore(mockStore)
	orchestrator.SetBlackboardRestoreFunc(testBlackboardRestoreFunc())

	_, err := orchestrator.HandleMessage(context.Background(), "Continue", "session-456", HandleOptions{TaskID: "task-123"})
	if err != nil {
		t.Fatalf("HandleMessage failed: %v", err)
	}

	if !reactivateCalled {
		t.Error("expected ReactivateTask to be called for continuation")
	}
}

// TestHandleMessage_RoutingFailureDoesNotReactivateTask verifies the commit
// point of continuation reactivation: when a continuation fails BEFORE its
// execution starts (routing error), the anchor task keeps its prior terminal
// status — ReactivateTask must NOT have flipped it back to in_progress, and
// FailTask must NOT have flipped it to failed. Regression guard for the
// orphaned in_progress task: the manager's fresh-workflow fallback then
// created a new task row, nothing ever closed the reactivated anchor, the
// session kept has_unfinished_task=true forever, and every app restart
// re-injected the "Task failed / Resume" banner over an otherwise
// successfully completed session. The FailTask guard covers the mirror bug:
// routing failure used to flip the completed anchor to failed, which counts
// as unfinished/resumable forever and dead-ends the manager's fresh-retry
// fallback (shouldRetryContinuationFresh) on the resumable banner.
func TestHandleMessage_RoutingFailureDoesNotReactivateTask(t *testing.T) {
	reactivateCalled := false
	failCalled := false

	// Every LLM call fails — the router call in particular, mirroring the
	// real-world repro (router provider outage before execution started).
	mockLLM := &mockLLMCaller{
		callFn: func(_ context.Context, _ llm.ChatRequest) (*llm.ChatResponse, error) {
			return nil, errors.New("router LLM call failed: connection refused")
		},
	}

	registry := createTestRegistry()
	r := newCoreRouter(mockLLM, 5)

	orchestrator := NewOrchestrator(OrchestratorConfig{}, OrchestratorDeps{
		Router:         r,
		LLM:            mockLLM,
		ToolExec:       registry,
		ToolRegistry:   registry,
		TokenCounter:   llm.NewSimpleTokenCounter(),
		ContextFactory: testContextFactory,
		CircuitBreaker: defaultCircuitBreakerConfig,
	})
	mockStore := &mockTaskStoreWithReactivate{
		taskState: &TaskState{
			TaskID:          "task-123",
			SessionID:       "session-456",
			OriginalRequest: "original task",
			Status:          "completed",
		},
		reactivateFn: func(string) error {
			reactivateCalled = true
			return nil
		},
		failFn: func(string) error {
			failCalled = true
			return nil
		},
	}
	orchestrator.SetTaskStore(mockStore)
	orchestrator.SetBlackboardRestoreFunc(testBlackboardRestoreFunc())

	_, err := orchestrator.HandleMessage(context.Background(), "Continue", "session-456", HandleOptions{TaskID: "task-123"})
	if err == nil {
		t.Fatal("expected HandleMessage to fail with the routing error")
	}

	if reactivateCalled {
		t.Error("ReactivateTask must not be called when the continuation fails before execution starts (routing error)")
	}
	if failCalled {
		t.Error("FailTask must not be called when a routing failure hits a non-reactivated completed anchor: flipping it to failed leaves the session permanently resumable (failed counts as unfinished) and blocks the manager's fresh-retry fallback")
	}
}

// TestHandleMessage_RoutingFailureFailsFreshTask verifies the other side of
// the routing-failure FailTask gate: a LIVE task row must still be flipped to
// failed. A fresh task's row is created in_progress (SetOriginalRequest →
// PersistNewTask) before routing runs, so a routing failure must mark it
// failed — otherwise the row lingers in_progress as a silent-resume
// candidate. The goal path (reactivated before routing) is covered by the
// same live-row condition in routeAndActivateSkills.
func TestHandleMessage_RoutingFailureFailsFreshTask(t *testing.T) {
	failCalled := false

	// Every LLM call fails — the router call in particular.
	mockLLM := &mockLLMCaller{
		callFn: func(_ context.Context, _ llm.ChatRequest) (*llm.ChatResponse, error) {
			return nil, errors.New("router LLM call failed: connection refused")
		},
	}

	registry := createTestRegistry()
	r := newCoreRouter(mockLLM, 5)

	mockStore := &mockTaskStoreWithReactivate{
		failFn: func(string) error {
			failCalled = true
			return nil
		},
	}

	orchestrator := NewOrchestrator(OrchestratorConfig{}, OrchestratorDeps{
		Router:         r,
		LLM:            mockLLM,
		ToolExec:       registry,
		ToolRegistry:   registry,
		TokenCounter:   llm.NewSimpleTokenCounter(),
		ContextFactory: testContextFactory,
		CircuitBreaker: defaultCircuitBreakerConfig,
		// Fresh task: setupBlackboard uses the factory, so the blackboard is
		// a PersistableBlackboard wired to the mock store (FailTask →
		// PersistFailure).
		BBFactory: func(taskID string) orchestration.Blackboard {
			return &testPersistableBlackboard{
				MapBlackboard: orchestration.NewMapBlackboard(),
				taskID:        taskID,
				store:         mockStore,
			}
		},
	})
	orchestrator.SetTaskStore(mockStore)

	_, err := orchestrator.HandleMessage(context.Background(), "Fresh task", "session-789", HandleOptions{})
	if err == nil {
		t.Fatal("expected HandleMessage to fail with the routing error")
	}

	if !failCalled {
		t.Error("FailTask must be called when routing fails on a fresh task: its in_progress row must be marked failed, not left lingering as a silent-resume candidate")
	}
}

// mockTaskStoreWithReactivate is a mock TaskPersistence that tracks
// ReactivateTask and PersistFailure calls.
type mockTaskStoreWithReactivate struct {
	taskState    *TaskState
	loadErr      error
	reactivateFn func(taskID string) error
	failFn       func(taskID string) error
}

func (m *mockTaskStoreWithReactivate) PersistNewTask(taskID, sessionID, originalRequest string) error {
	return nil
}
func (m *mockTaskStoreWithReactivate) PersistPlan(taskID string, plan *orchestration.Plan) error {
	return nil
}
func (m *mockTaskStoreWithReactivate) PersistRouting(taskID string, routing *router.RoutingDecision) error {
	return nil
}
func (m *mockTaskStoreWithReactivate) PersistStepResult(taskID, stepID, summary, fullOutput, errorText string, steps []agent.Step) error {
	return nil
}
func (m *mockTaskStoreWithReactivate) PersistReflection(taskID string, r orchestration.Reflection) error {
	return nil
}
func (m *mockTaskStoreWithReactivate) PersistCompletion(taskID, finalOutput string, attemptCount int) error {
	return nil
}
func (m *mockTaskStoreWithReactivate) PersistFailure(taskID string) error {
	if m.failFn != nil {
		return m.failFn(taskID)
	}
	return nil
}
func (m *mockTaskStoreWithReactivate) PersistCancellation(taskID string) error { return nil }
func (m *mockTaskStoreWithReactivate) PersistPause(taskID string) error        { return nil }
func (m *mockTaskStoreWithReactivate) PersistFacts(taskID string, facts []orchestration.Fact) error {
	return nil
}
func (m *mockTaskStoreWithReactivate) PersistAttachments(taskID string, attachments []orchestration.Attachment) error {
	return nil
}
func (m *mockTaskStoreWithReactivate) SaveTrajectory(taskID string, steps []agent.Step) error {
	return nil
}
func (m *mockTaskStoreWithReactivate) LoadTrajectory(taskID string) ([]agent.Step, error) {
	return nil, nil
}
func (m *mockTaskStoreWithReactivate) PersistGoalState(taskID string, gs *goal.GoalState) error {
	return nil
}
func (m *mockTaskStoreWithReactivate) LoadGoalState(taskID string) (*goal.GoalState, error) {
	return nil, nil
}
func (m *mockTaskStoreWithReactivate) LoadTaskState(taskID string) (*TaskState, error) {
	if m.loadErr != nil {
		return nil, m.loadErr
	}
	return m.taskState, nil
}
func (m *mockTaskStoreWithReactivate) GetUnfinishedTaskID(sessionID string) (string, error) {
	return "", nil
}
func (m *mockTaskStoreWithReactivate) ReactivateTask(taskID string) error {
	if m.reactivateFn != nil {
		return m.reactivateFn(taskID)
	}
	return nil
}

// TestBuildSkillAugmentedRoutingMessage verifies the message augmentation logic.
func TestBuildSkillAugmentedRoutingMessage(t *testing.T) {
	// Create a SkillManager with a test skill
	skillDir := t.TempDir()
	checkDir := filepath.Join(skillDir, "code-check")
	if err := os.MkdirAll(checkDir, 0o755); err != nil {
		t.Fatalf("mkdir: %v", err)
	}
	md := "---\nname: code-check\ndescription: Run static analysis on source code\n---\nbody\n"
	if err := os.WriteFile(filepath.Join(checkDir, "SKILL.md"), []byte(md), 0o644); err != nil {
		t.Fatalf("write: %v", err)
	}
	sm := skills.NewSkillManager([]string{skillDir}, nil)
	if err := sm.Scan(); err != nil {
		t.Fatalf("scan: %v", err)
	}

	o := &Orchestrator{skillManager: sm}

	t.Run("single skill with args", func(t *testing.T) {
		got := o.buildSkillAugmentedRoutingMessage("src/main.go", []string{"code-check"})
		if !strings.Contains(got, "/code-check") {
			t.Errorf("expected /code-check in message, got: %s", got)
		}
		if !strings.Contains(got, "Run static analysis") {
			t.Errorf("expected skill description in message, got: %s", got)
		}
		if !strings.Contains(got, "src/main.go") {
			t.Errorf("expected original args in message, got: %s", got)
		}
	})

	t.Run("skill without args", func(t *testing.T) {
		got := o.buildSkillAugmentedRoutingMessage("", []string{"code-check"})
		if !strings.Contains(got, "/code-check") {
			t.Errorf("expected /code-check in message, got: %s", got)
		}
		if strings.HasSuffix(got, " ") {
			t.Errorf("message should not have trailing space, got: %q", got)
		}
	})

	t.Run("unknown skill falls back to bare name", func(t *testing.T) {
		got := o.buildSkillAugmentedRoutingMessage("args", []string{"unknown-skill"})
		if !strings.Contains(got, "/unknown-skill") {
			t.Errorf("expected /unknown-skill in message, got: %s", got)
		}
		if strings.Contains(got, "skill:") {
			t.Errorf("unknown skill should not have description, got: %s", got)
		}
	})
}

// TestResolveTaskMessage verifies the effective-task resolution that feeds the
// Conductor and conversation history. A bare skill invocation (e.g. just
// "/code-check" with no extra text) is stripped to "" by preprocessing; without
// restoration the Conductor would receive an empty task and the provider may
// reject the request (HTTP 400 "messages parameter is illegal").
func TestResolveTaskMessage(t *testing.T) {
	skillDir := t.TempDir()
	checkDir := filepath.Join(skillDir, "code-check")
	if err := os.MkdirAll(checkDir, 0o755); err != nil {
		t.Fatalf("mkdir: %v", err)
	}
	md := "---\nname: code-check\ndescription: Run static analysis on source code\n---\nbody\n"
	if err := os.WriteFile(filepath.Join(checkDir, "SKILL.md"), []byte(md), 0o644); err != nil {
		t.Fatalf("write: %v", err)
	}
	sm := skills.NewSkillManager([]string{skillDir}, nil)
	if err := sm.Scan(); err != nil {
		t.Fatalf("scan: %v", err)
	}
	o := &Orchestrator{skillManager: sm}

	t.Run("empty message with skill is restored to non-empty", func(t *testing.T) {
		got := o.resolveTaskMessage("", []string{"code-check"})
		if got == "" {
			t.Fatal("expected non-empty task message for bare skill invocation; got empty (provider would reject HTTP 400)")
		}
		if !strings.Contains(got, "/code-check") {
			t.Errorf("expected /code-check in task message, got: %s", got)
		}
		if !strings.Contains(got, "Run static analysis") {
			t.Errorf("expected skill description in task message, got: %s", got)
		}
	})

	t.Run("message with args keeps args and adds skill context", func(t *testing.T) {
		got := o.resolveTaskMessage("src/main.go", []string{"code-check"})
		if !strings.Contains(got, "/code-check") || !strings.Contains(got, "src/main.go") {
			t.Errorf("expected skill ref and args in task message, got: %s", got)
		}
	})

	t.Run("no skills returns message unchanged", func(t *testing.T) {
		if got := o.resolveTaskMessage("just text", nil); got != "just text" {
			t.Errorf("expected passthrough without skills, got: %s", got)
		}
	})

	t.Run("nil skill manager returns message unchanged", func(t *testing.T) {
		noMgr := &Orchestrator{skillManager: nil}
		if got := noMgr.resolveTaskMessage("", []string{"code-check"}); got != "" {
			t.Errorf("expected passthrough with nil skill manager, got: %q", got)
		}
	})
}

// TestHandleMessage_EmptySkillMessage_PropagatesNonEmptyTaskToConductor is the
// regression test for the HTTP 400 "messages parameter is illegal" failure:
// a bare skill invocation (e.g. just "/code-check") is stripped to "" by
// preprocessing. The Conductor must still receive a non-empty task — otherwise
// the request is system-only and the provider rejects it.
func TestHandleMessage_EmptySkillMessage_PropagatesNonEmptyTaskToConductor(t *testing.T) {
	skillDir := t.TempDir()
	checkDir := filepath.Join(skillDir, "code-check")
	if err := os.MkdirAll(checkDir, 0o755); err != nil {
		t.Fatalf("mkdir: %v", err)
	}
	md := "---\nname: code-check\ndescription: Run static analysis on source code\n---\nbody\n"
	if err := os.WriteFile(filepath.Join(checkDir, "SKILL.md"), []byte(md), 0o644); err != nil {
		t.Fatalf("write: %v", err)
	}
	sm := skills.NewSkillManager([]string{skillDir}, nil)
	if err := sm.Scan(); err != nil {
		t.Fatalf("scan: %v", err)
	}

	// Capturing context factory: records every CM created so we can inspect
	// the task each one received via SetTask (the Conductor calls SetTask on
	// the CM it builds).
	var createdCMs []*mockContextManager
	captureFactory := func(systemPrompt string, _ llm.ModelMetadata, _ string, _ ...orchestration.PruningOverride) ContextManager {
		cm := &mockContextManager{systemPrompt: systemPrompt}
		createdCMs = append(createdCMs, cm)
		return cm
	}

	callIdx := 0
	mockLLM := &mockLLMCaller{
		callFn: func(ctx context.Context, req llm.ChatRequest) (*llm.ChatResponse, error) {
			callIdx++
			if callIdx == 1 {
				// Router: classify as general; the skill is user-specified.
				return &llm.ChatResponse{
					Message: llm.Message{
						Role:    "assistant",
						Content: `{"domain": "general", "complexity": 2, "needs_clarification": false, "matched_skills": []}`,
					},
					StopReason: "end_turn",
				}, nil
			}
			// Conductor: finish immediately.
			return &llm.ChatResponse{
				Message: llm.Message{
					Role: "assistant",
					ToolCalls: []llm.ToolCall{{
						ID:    "call_1",
						Name:  "finish",
						Input: json.RawMessage(`{"answer":"done"}`),
					}},
				},
				StopReason: "tool_use",
			}, nil
		},
	}

	registry := createTestRegistry()
	counter := llm.NewSimpleTokenCounter()

	orchestrator := NewOrchestrator(OrchestratorConfig{}, OrchestratorDeps{
		Router:         newCoreRouter(mockLLM, 5),
		LLM:            mockLLM,
		ToolExec:       registry,
		ToolRegistry:   registry,
		TokenCounter:   counter,
		ContextFactory: captureFactory,
		CircuitBreaker: defaultCircuitBreakerConfig,
		SkillManager:   sm,
	})

	// Bare skill invocation: preprocessor would strip "/code-check" → "".
	if _, err := orchestrator.HandleMessage(context.Background(), "", "session-skill", HandleOptions{UserSkills: []string{"code-check"}}); err != nil {
		t.Fatalf("HandleMessage failed: %v", err)
	}

	// At least one context manager must have received a non-empty task
	// containing the restored skill reference.
	var conductorTask string
	for _, cm := range createdCMs {
		if strings.Contains(cm.taskDefinition, "/code-check") {
			conductorTask = cm.taskDefinition
			break
		}
	}
	if conductorTask == "" {
		t.Fatalf("expected Conductor to receive a non-empty task with /code-check for bare skill invocation; got %d CMs with tasks: %v", len(createdCMs), cmTasks(createdCMs))
	}
	if !strings.Contains(conductorTask, "Run static analysis") {
		t.Errorf("expected Conductor task to include skill description, got: %s", conductorTask)
	}
}

// cmTasks returns the task definitions set on each context manager (for diagnostics).
func cmTasks(cms []*mockContextManager) []string {
	tasks := make([]string, len(cms))
	for i, cm := range cms {
		tasks[i] = cm.taskDefinition
	}
	return tasks
}

// TestBuildSystemPrompt_PlanMode verifies that buildSystemPrompt includes the
// Plan Context section when PlanModeKey is set in the context.
func TestBuildSystemPrompt_PlanMode(t *testing.T) {
	ctx := context.WithValue(context.Background(), PlanModeKey, true)
	ctx = tools.WithWorkspacePath(ctx, "/test/workspace")

	modelMeta := llm.ModelMetadata{Family: "openai_flagship"}
	result := buildSystemPrompt(ctx, "test message", modelMeta)

	if !strings.Contains(result, "Plan Context") {
		t.Error("plan mode prompt should contain Plan Context section")
	}
	if !strings.Contains(result, "read_step_output") {
		t.Error("plan mode prompt should contain read_step_output")
	}
	if !strings.Contains(result, "list_step_outputs") {
		t.Error("plan mode prompt should contain list_step_outputs")
	}
}

// TestBuildSystemPrompt_ReactMode verifies that buildSystemPrompt does NOT include
// the Plan Context section when PlanModeKey is not set (ReAct mode).
func TestBuildSystemPrompt_ReactMode(t *testing.T) {
	ctx := context.Background()
	ctx = tools.WithWorkspacePath(ctx, "/test/workspace")

	modelMeta := llm.ModelMetadata{Family: "openai_flagship"}
	result := buildSystemPrompt(ctx, "test message", modelMeta)

	if strings.Contains(result, "Plan Context") {
		t.Error("react mode prompt should NOT contain Plan Context section")
	}
	if strings.Contains(result, "read_step_output") {
		t.Error("react mode prompt should NOT contain read_step_output")
	}
	if strings.Contains(result, "list_step_outputs") {
		t.Error("react mode prompt should NOT contain list_step_outputs")
	}
}

// TestBuildSystemPrompt_PlanWithActiveSkills verifies that skill instructions
// appear in the system prompt when PlanModeKey IS set (Plan&Execute mode)
// and active skills are injected via context.
func TestBuildSystemPrompt_PlanWithActiveSkills(t *testing.T) {
	ctx := context.WithValue(context.Background(), PlanModeKey, true)
	ctx = tools.WithWorkspacePath(ctx, "/test/workspace")
	ctx = WithActiveSkills(ctx, &ActiveSkills{
		Skills: []*skills.Skill{
			{
				Metadata: skills.SkillMetadata{
					Name:        "pdf-processing",
					Description: "Extract PDF text and tables.",
				},
				Body:    "Step 1: Read the PDF file using read_file.",
				DirPath: "/skills/pdf-processing",
			},
		},
	})

	modelMeta := llm.ModelMetadata{Family: "openai_flagship"}
	result := buildSystemPrompt(ctx, "process this PDF", modelMeta)

	if !strings.Contains(result, "Active Skills") {
		t.Error("plan mode prompt should contain Active Skills section when skills are active")
	}
	if !strings.Contains(result, "pdf-processing") {
		t.Error("plan mode prompt should contain the skill name")
	}
	if !strings.Contains(result, "Step 1: Read the PDF file") {
		t.Error("plan mode prompt should contain the skill body")
	}
	// Plan Context should also be present (plan mode)
	if !strings.Contains(result, "Plan Context") {
		t.Error("plan mode prompt should contain Plan Context section")
	}
}

// TestBuildSystemPrompt_ReactWithActiveSkills verifies that skill instructions
// appear in the system prompt when PlanModeKey is NOT set (ReAct mode).
// This is the key verification: skills must work in BOTH execution modes.
func TestBuildSystemPrompt_ReactWithActiveSkills(t *testing.T) {
	ctx := context.Background()
	ctx = tools.WithWorkspacePath(ctx, "/test/workspace")
	ctx = WithActiveSkills(ctx, &ActiveSkills{
		Skills: []*skills.Skill{
			{
				Metadata: skills.SkillMetadata{
					Name:        "data-analysis",
					Description: "Analyze datasets and generate visualizations.",
					// AllowedTools is intentionally NOT set: the skill policy
					// layer is gone (ADR-024), and the prompt must render no
					// tool-permission line even when frontmatter carries one —
					// asserted by TestBuildSystemPrompt_SkillAllowedToolsNotGranted.
				},
				Body:    "1. Read the dataset using read_file.\n2. Process with jq.",
				DirPath: "/skills/data-analysis",
			},
		},
	})

	modelMeta := llm.ModelMetadata{Family: "openai_flagship"}
	result := buildSystemPrompt(ctx, "analyze this dataset", modelMeta)

	if !strings.Contains(result, "Active Skills") {
		t.Error("react mode prompt should contain Active Skills section when skills are active")
	}
	if !strings.Contains(result, "data-analysis") {
		t.Error("react mode prompt should contain the skill name")
	}
	if !strings.Contains(result, "Read the dataset") {
		t.Error("react mode prompt should contain the skill body")
	}
	if strings.Contains(result, "Allowed tools:") {
		t.Error("react mode prompt must NOT contain an 'Allowed tools:' line — the skill policy layer was removed (ADR-024)")
	}
	// Plan Context should NOT be present (ReAct mode)
	if strings.Contains(result, "Plan Context") {
		t.Error("react mode prompt should NOT contain Plan Context section")
	}
	// ReAct mode should include the Completion section
	if !strings.Contains(result, "single-step mode") {
		t.Error("react mode prompt should contain single-step mode completion instruction")
	}
}

// TestBuildSystemPrompt_SkillAllowedToolsNotGranted guards the ADR-024 removal
// of the skill policy layer: skill frontmatter may still declare an
// allowed-tools field (Claude-Code vocabulary), but the system prompt must
// render no permission line for it — the engine grants nothing based on that
// field, so promising "Allowed tools:" would mislead the model into assuming
// permissions that no gate enforces.
func TestBuildSystemPrompt_SkillAllowedToolsNotGranted(t *testing.T) {
	ctx := tools.WithWorkspacePath(context.Background(), "/test/workspace")
	ctx = WithActiveSkills(ctx, &ActiveSkills{
		Skills: []*skills.Skill{
			{
				Metadata: skills.SkillMetadata{
					Name:         "data-analysis",
					Description:  "Analyze datasets and generate visualizations.",
					AllowedTools: "Read Write Bash(jq:*)",
				},
				Body:    "1. Read the dataset using read_file.\n2. Process with jq.",
				DirPath: "/skills/data-analysis",
			},
		},
	})

	for _, planMode := range []bool{true, false} {
		testCtx := ctx
		if planMode {
			testCtx = context.WithValue(ctx, PlanModeKey, true)
		}
		result := buildSystemPrompt(testCtx, "analyze this dataset", llm.ModelMetadata{Family: "openai_flagship"})
		if strings.Contains(result, "Allowed tools:") {
			t.Errorf("planMode=%v: prompt renders an 'Allowed tools:' line for skill frontmatter; the skill policy layer is removed (ADR-024) and no engine gate honors it", planMode)
		}
		if !strings.Contains(result, "Read the dataset") {
			t.Errorf("planMode=%v: skill body must still render", planMode)
		}
	}
}

// TestBuildSystemPrompt_NoActiveSkills verifies that when no active skills
// are in the context, neither mode includes the Active Skills section.
func TestBuildSystemPrompt_NoActiveSkills(t *testing.T) {
	for _, planMode := range []bool{true, false} {
		name := "ReAct"
		ctx := context.Background()
		if planMode {
			name = "Plan"
			ctx = context.WithValue(ctx, PlanModeKey, true)
		}
		ctx = tools.WithWorkspacePath(ctx, "/test/workspace")

		t.Run(name, func(t *testing.T) {
			modelMeta := llm.ModelMetadata{Family: "openai_flagship"}
			result := buildSystemPrompt(ctx, "test message", modelMeta)

			if strings.Contains(result, "Active Skills") {
				t.Errorf("%s mode prompt should NOT contain Active Skills section when no skills are active", name)
			}
		})
	}
}

// TestBuildSystemPrompt_SkillsInStablePart verifies that active skills and
// AGENTS.md are placed in the stable (cacheable) prefix before the
// CacheBreakMarker, so provider-side prompt caching can reuse them across
// ReAct iterations.
func TestBuildSystemPrompt_SkillsInStablePart(t *testing.T) {
	ctx := tools.WithWorkspacePath(context.Background(), "/test/workspace")
	ctx = tools.WithEnvInfo(ctx, &tools.EnvInfo{
		OS:   "darwin",
		Arch: "arm64",
	})
	ctx = WithAgentsMD(ctx, &AgentsMD{Content: "project conventions"})
	ctx = WithActiveSkills(ctx, &ActiveSkills{
		Skills: []*skills.Skill{{
			Metadata: skills.SkillMetadata{Name: "test-skill"},
			Body:     "skill body content",
		}},
	})
	// Vector hints ensure the volatile tail is non-empty so CacheBreakMarker
	// is preserved by Builder.Build().
	ctx = WithVectorSearchHints(ctx, &VectorSearchHints{
		Files: []VectorSearchHint{{FilePath: "hint.go", Summary: "hint"}},
	})

	modelMeta := llm.ModelMetadata{Family: "glm"}
	result := buildSystemPrompt(ctx, "test", modelMeta)

	parts := strings.SplitN(result, prompt.CacheBreakMarker, 2)
	if len(parts) != 2 {
		t.Fatalf("expected CacheBreakMarker to split prompt, got %d parts", len(parts))
	}
	stable, volatile := parts[0], parts[1]
	if !strings.Contains(stable, "test-skill") {
		t.Error("skills should be in stable (cacheable) part")
	}
	if !strings.Contains(stable, "project conventions") {
		t.Error("AGENTS.md content should be in stable (cacheable) part")
	}
	if !strings.Contains(stable, "## Workspace") {
		t.Error("workspace context should be in stable (cacheable) part")
	}
	if !strings.Contains(stable, "## Environment") {
		t.Error("env block should be in stable (cacheable) part")
	}
	// Volatile part should not contain skills or AGENTS.md.
	if strings.Contains(volatile, "test-skill") {
		t.Error("skills should NOT be in volatile part")
	}
	if strings.Contains(volatile, "project conventions") {
		t.Error("AGENTS.md content should NOT be in volatile part")
	}
	// Vector hints should be in volatile part.
	if !strings.Contains(volatile, "Relevant Project Files") {
		t.Error("vector hints should be in volatile part")
	}
}

// TestBuildSystemPrompt_WorkDirectories verifies that auxiliary work
// directories injected via core.WithWorkDirectories appear in the system
// prompt's "Additional Work Directories" section (with each path and its
// description) and that the section lands in the stable (cacheable) prefix.
func TestBuildSystemPrompt_WorkDirectories(t *testing.T) {
	ctx := tools.WithWorkspacePath(context.Background(), "/test/workspace")
	ctx = WithWorkDirectories(ctx, []WorkDirectory{
		{Path: "/aux/build", Description: "build artifacts"},
		{Path: "/aux/logs"},
	})
	// Vector hints ensure the volatile tail is non-empty so CacheBreakMarker
	// is preserved by Builder.Build().
	ctx = WithVectorSearchHints(ctx, &VectorSearchHints{
		Files: []VectorSearchHint{{FilePath: "hint.go", Summary: "hint"}},
	})

	modelMeta := llm.ModelMetadata{Family: "openai_flagship"}
	result := buildSystemPrompt(ctx, "test message", modelMeta)

	if !strings.Contains(result, "## Additional Work Directories") {
		t.Error("expected 'Additional Work Directories' section in system prompt")
	}
	if !strings.Contains(result, "/aux/build") {
		t.Error("expected auxiliary path /aux/build in system prompt")
	}
	if !strings.Contains(result, "build artifacts") {
		t.Error("expected auxiliary directory description in system prompt")
	}
	if !strings.Contains(result, "/aux/logs") {
		t.Error("expected auxiliary path /aux/logs in system prompt")
	}

	// The section must be in the stable (cacheable) prefix, before the marker.
	parts := strings.SplitN(result, prompt.CacheBreakMarker, 2)
	if len(parts) != 2 {
		t.Fatalf("expected CacheBreakMarker to split prompt, got %d parts", len(parts))
	}
	if !strings.Contains(parts[0], "## Additional Work Directories") {
		t.Error("work directories section should be in stable (cacheable) part")
	}
}

// TestOrchestrator_VectorSearchHints_NilFunc verifies that when vectorSearchFunc is nil,
// HandleMessage works normally without injecting hints (no panic).
func TestOrchestrator_VectorSearchHints_NilFunc(t *testing.T) {
	mockLLM := &mockLLMCaller{
		callFn: func(ctx context.Context, req llm.ChatRequest) (*llm.ChatResponse, error) {
			// Verify no vector search hints in system prompt
			for _, msg := range req.Messages {
				if msg.Role == "system" && strings.Contains(msg.Content, "Relevant Project Files") {
					t.Error("system prompt should NOT contain vector search hints when func is nil")
				}
			}
			// Router returns needs_clarification to short-circuit
			return &llm.ChatResponse{
				Message: llm.Message{
					Role:    "assistant",
					Content: `{"domain": "general", "complexity": 1, "compaction_strategy": "sliding_window", "suggested_tools": [], "needs_clarification": true}`,
				},
				StopReason: "end_turn",
			}, nil
		},
	}

	registry := createTestRegistry()
	counter := llm.NewSimpleTokenCounter()

	orchestrator := NewOrchestrator(OrchestratorConfig{}, OrchestratorDeps{
		Router:         newCoreRouter(mockLLM, 5),
		LLM:            mockLLM,
		ToolExec:       registry,
		ToolRegistry:   registry,
		TokenCounter:   counter,
		ContextFactory: testContextFactory,
		CircuitBreaker: defaultCircuitBreakerConfig,
	})

	result, err := orchestrator.HandleMessage(context.Background(), "test query", "", HandleOptions{})
	if err != nil {
		t.Fatalf("Handle failed: %v", err)
	}
	if result == nil {
		t.Fatal("expected non-nil result")
	}
}

// TestOrchestrator_VectorSearchHints_WithResults verifies that when vectorSearchFunc
// returns results, hints are injected into the context and available downstream.
func TestOrchestrator_VectorSearchHints_WithResults(t *testing.T) {
	// Create a vector search function that returns test results
	searchFunc := func(ctx context.Context, opts builtins.VectorSearchOptions) ([]builtins.VectorSearchResult, error) {
		return []builtins.VectorSearchResult{
			{
				FilePath: "src/main.go",
				FileName: "main.go",
				Content:  "package main\n\nfunc main() {\n\tfmt.Println(\"hello\")\n}",
				Score:    0.95,
			},
			{
				FilePath: "src/handler.go",
				FileName: "handler.go",
				Content:  "func handleRequest(w http.ResponseWriter, r *http.Request) {\n\tw.WriteHeader(200)\n}",
				Score:    0.85,
			},
		}, nil
	}

	// Test the hint injection directly via injectVectorSearchHints
	o := &Orchestrator{
		vectorSearchFunc: searchFunc,
	}

	ctx := context.Background()
	ctx = o.injectVectorSearchHints(ctx, "how does the main function work")

	hints := VectorSearchHintsFromContext(ctx)
	if hints == nil {
		t.Fatal("expected vector search hints in context")
	}
	if len(hints.Files) != 2 {
		t.Fatalf("expected 2 hints, got %d", len(hints.Files))
	}
	if hints.Files[0].FilePath != "src/main.go" {
		t.Errorf("expected first hint path 'src/main.go', got %q", hints.Files[0].FilePath)
	}
	if hints.Files[1].FilePath != "src/handler.go" {
		t.Errorf("expected second hint path 'src/handler.go', got %q", hints.Files[1].FilePath)
	}
}

// TestOrchestrator_VectorSearchHints_ContentTruncation verifies that hint summaries
// are truncated to 100 characters.
func TestOrchestrator_VectorSearchHints_ContentTruncation(t *testing.T) {
	longContent := strings.Repeat("a", 200)
	searchFunc := func(ctx context.Context, opts builtins.VectorSearchOptions) ([]builtins.VectorSearchResult, error) {
		return []builtins.VectorSearchResult{
			{
				FilePath: "long.go",
				FileName: "long.go",
				Content:  longContent,
				Score:    0.9,
			},
		}, nil
	}

	o := &Orchestrator{
		vectorSearchFunc: searchFunc,
	}

	ctx := o.injectVectorSearchHints(context.Background(), "query")
	hints := VectorSearchHintsFromContext(ctx)
	if hints == nil {
		t.Fatal("expected hints")
	}
	if utf8.RuneCountInString(hints.Files[0].Summary) != 100 {
		t.Errorf("expected summary length 100, got %d", len(hints.Files[0].Summary))
	}
}

// TestOrchestrator_VectorSearchHints_ErrorSkipped verifies that when the vector search
// function returns an error, hints are silently skipped.
func TestOrchestrator_VectorSearchHints_ErrorSkipped(t *testing.T) {
	searchFunc := func(ctx context.Context, opts builtins.VectorSearchOptions) ([]builtins.VectorSearchResult, error) {
		return nil, context.DeadlineExceeded
	}

	o := &Orchestrator{
		vectorSearchFunc: searchFunc,
	}

	ctx := o.injectVectorSearchHints(context.Background(), "query")
	hints := VectorSearchHintsFromContext(ctx)
	if hints != nil {
		t.Error("expected nil hints when search returns error")
	}
}

// --- AGENTS.md integration tests ---

// TestOrchestrator_AgentsMD_InjectedWhenPresent verifies that when AGENTS.md
// exists in the workspace root, its content is injected into the context
// and it appears as the first VectorSearchHint.
func TestOrchestrator_AgentsMD_InjectedWhenPresent(t *testing.T) {
	tmpDir := t.TempDir()
	agentsContent := "# Project Instructions\nAlways run tests before committing."
	if err := os.WriteFile(filepath.Join(tmpDir, "AGENTS.md"), []byte(agentsContent), 0o644); err != nil {
		t.Fatalf("failed to write AGENTS.md: %v", err)
	}

	o := &Orchestrator{}
	ctx := tools.WithWorkspacePath(context.Background(), tmpDir)
	ctx = o.injectVectorSearchHints(ctx, "test query")

	// Verify AgentsMD is in context
	amd := AgentsMDFromContext(ctx)
	if amd == nil {
		t.Fatal("expected AgentsMD in context")
	}
	if amd.Content != agentsContent {
		t.Errorf("expected AgentsMD content %q, got %q", agentsContent, amd.Content)
	}

	// Verify AGENTS.md appears as the first hint
	hints := VectorSearchHintsFromContext(ctx)
	if hints == nil {
		t.Fatal("expected VectorSearchHints in context")
	}
	if len(hints.Files) != 1 {
		t.Fatalf("expected 1 hint (AGENTS.md), got %d", len(hints.Files))
	}
	if hints.Files[0].FilePath != "AGENTS.md" {
		t.Errorf("expected hint file path 'AGENTS.md', got %q", hints.Files[0].FilePath)
	}
}

// TestOrchestrator_AgentsMD_AbsentWhenNoWorkspace verifies that no AgentsMD
// is injected when there is no workspace path in the context.
func TestOrchestrator_AgentsMD_AbsentWhenNoWorkspace(t *testing.T) {
	o := &Orchestrator{}
	ctx := o.injectVectorSearchHints(context.Background(), "test query")

	amd := AgentsMDFromContext(ctx)
	if amd != nil {
		t.Error("expected nil AgentsMD when no workspace path")
	}
}

// TestOrchestrator_AgentsMD_AbsentWhenFileMissing verifies that no AgentsMD
// is injected when the workspace exists but has no AGENTS.md file.
func TestOrchestrator_AgentsMD_AbsentWhenFileMissing(t *testing.T) {
	tmpDir := t.TempDir()

	o := &Orchestrator{}
	ctx := tools.WithWorkspacePath(context.Background(), tmpDir)
	ctx = o.injectVectorSearchHints(ctx, "test query")

	amd := AgentsMDFromContext(ctx)
	if amd != nil {
		t.Error("expected nil AgentsMD when AGENTS.md file is missing")
	}
	hints := VectorSearchHintsFromContext(ctx)
	if hints != nil {
		t.Error("expected nil VectorSearchHints when no vector results and no AGENTS.md")
	}
}

// TestOrchestrator_AgentsMD_WithVectorSearch verifies that when both vector
// search results and AGENTS.md exist, AGENTS.md is prepended as the first hint.
func TestOrchestrator_AgentsMD_WithVectorSearch(t *testing.T) {
	tmpDir := t.TempDir()
	agentsContent := "# Project Instructions\nRun make test before committing."
	if err := os.WriteFile(filepath.Join(tmpDir, "AGENTS.md"), []byte(agentsContent), 0o644); err != nil {
		t.Fatalf("failed to write AGENTS.md: %v", err)
	}

	searchFunc := func(ctx context.Context, opts builtins.VectorSearchOptions) ([]builtins.VectorSearchResult, error) {
		return []builtins.VectorSearchResult{
			{
				FilePath: "src/main.go",
				FileName: "main.go",
				Content:  "package main",
				Score:    0.9,
			},
		}, nil
	}

	o := &Orchestrator{
		vectorSearchFunc: searchFunc,
	}

	ctx := tools.WithWorkspacePath(context.Background(), tmpDir)
	ctx = o.injectVectorSearchHints(ctx, "test query")

	// Verify AgentsMD is in context
	amd := AgentsMDFromContext(ctx)
	if amd == nil {
		t.Fatal("expected AgentsMD in context")
	}

	// Verify AGENTS.md is the first hint, followed by vector results
	hints := VectorSearchHintsFromContext(ctx)
	if hints == nil {
		t.Fatal("expected VectorSearchHints in context")
	}
	if len(hints.Files) != 2 {
		t.Fatalf("expected 2 hints (AGENTS.md + vector result), got %d", len(hints.Files))
	}
	if hints.Files[0].FilePath != "AGENTS.md" {
		t.Errorf("expected first hint to be 'AGENTS.md', got %q", hints.Files[0].FilePath)
	}
	if hints.Files[1].FilePath != "src/main.go" {
		t.Errorf("expected second hint to be 'src/main.go', got %q", hints.Files[1].FilePath)
	}
}

// TestOrchestrator_AgentsMD_NilVectorSearchFunc verifies that AGENTS.md
// is still injected even when vectorSearchFunc is nil.
func TestOrchestrator_AgentsMD_NilVectorSearchFunc(t *testing.T) {
	tmpDir := t.TempDir()
	agentsContent := "# Instructions\nUse go test."
	if err := os.WriteFile(filepath.Join(tmpDir, "AGENTS.md"), []byte(agentsContent), 0o644); err != nil {
		t.Fatalf("failed to write AGENTS.md: %v", err)
	}

	o := &Orchestrator{
		vectorSearchFunc: nil, // no vector search
	}

	ctx := tools.WithWorkspacePath(context.Background(), tmpDir)
	ctx = o.injectVectorSearchHints(ctx, "test query")

	// AgentsMD should still be injected
	amd := AgentsMDFromContext(ctx)
	if amd == nil {
		t.Fatal("expected AgentsMD in context even with nil vectorSearchFunc")
	}

	// VectorSearchHints should contain only AGENTS.md
	hints := VectorSearchHintsFromContext(ctx)
	if hints == nil {
		t.Fatal("expected VectorSearchHints with AGENTS.md hint")
	}
	if len(hints.Files) != 1 || hints.Files[0].FilePath != "AGENTS.md" {
		t.Errorf("expected single AGENTS.md hint, got %v", hints.Files)
	}
}

// TestOrchestrator_AgentsMD_MultiSourceConcatenation verifies that AGENTS.md
// content from the configured search paths (global, c0wrk) is concatenated
// ahead of the workspace-root file, in priority order, when all sources exist.
func TestOrchestrator_AgentsMD_MultiSourceConcatenation(t *testing.T) {
	globalDir := t.TempDir()
	c0wrkDir := t.TempDir()
	wsDir := t.TempDir()

	globalContent := "# Global agent rules\nBe concise."
	c0wrkContent := "# c0wrk rules\nUse Go 1.26."
	projectContent := "# Project rules\nRun make test."

	globalPath := filepath.Join(globalDir, "AGENTS.md")
	c0wrkPath := filepath.Join(c0wrkDir, "AGENTS.md")
	if err := os.WriteFile(globalPath, []byte(globalContent), 0o644); err != nil {
		t.Fatalf("write global AGENTS.md: %v", err)
	}
	if err := os.WriteFile(c0wrkPath, []byte(c0wrkContent), 0o644); err != nil {
		t.Fatalf("write c0wrk AGENTS.md: %v", err)
	}
	if err := os.WriteFile(filepath.Join(wsDir, "AGENTS.md"), []byte(projectContent), 0o644); err != nil {
		t.Fatalf("write project AGENTS.md: %v", err)
	}

	o := &Orchestrator{
		config: OrchestratorConfig{
			AgentsMDSearchPaths: []string{globalPath, c0wrkPath},
		},
	}

	ctx := tools.WithWorkspacePath(context.Background(), wsDir)
	ctx = o.injectVectorSearchHints(ctx, "test query")

	amd := AgentsMDFromContext(ctx)
	if amd == nil {
		t.Fatal("expected AgentsMD in context")
	}

	// Global content must appear before c0wrk content, which must appear
	// before project content.
	globalIdx := strings.Index(amd.Content, globalContent)
	c0wrkIdx := strings.Index(amd.Content, c0wrkContent)
	projectIdx := strings.Index(amd.Content, projectContent)
	if globalIdx < 0 || c0wrkIdx < 0 || projectIdx < 0 {
		t.Fatalf("missing content; got %q", amd.Content)
	}
	if globalIdx >= c0wrkIdx || c0wrkIdx >= projectIdx {
		t.Errorf("expected global < c0wrk < project order; got global=%d c0wrk=%d project=%d in %q",
			globalIdx, c0wrkIdx, projectIdx, amd.Content)
	}
}

// TestOrchestrator_AgentsMD_SearchPathMissingFile verifies that a missing
// search-path file is silently skipped while remaining sources are still
// injected.
func TestOrchestrator_AgentsMD_SearchPathMissingFile(t *testing.T) {
	globalDir := t.TempDir()
	wsDir := t.TempDir()

	globalContent := "# Global agent rules\nBe concise."
	projectContent := "# Project rules\nRun make test."

	globalPath := filepath.Join(globalDir, "AGENTS.md")
	if err := os.WriteFile(globalPath, []byte(globalContent), 0o644); err != nil {
		t.Fatalf("write global AGENTS.md: %v", err)
	}
	if err := os.WriteFile(filepath.Join(wsDir, "AGENTS.md"), []byte(projectContent), 0o644); err != nil {
		t.Fatalf("write project AGENTS.md: %v", err)
	}

	o := &Orchestrator{
		config: OrchestratorConfig{
			// Second path does not exist on disk.
			AgentsMDSearchPaths: []string{globalPath, filepath.Join(t.TempDir(), "AGENTS.md")},
		},
	}

	ctx := tools.WithWorkspacePath(context.Background(), wsDir)
	ctx = o.injectVectorSearchHints(ctx, "test query")

	amd := AgentsMDFromContext(ctx)
	if amd == nil {
		t.Fatal("expected AgentsMD in context")
	}
	if !strings.Contains(amd.Content, globalContent) {
		t.Errorf("expected global content in %q", amd.Content)
	}
	if !strings.Contains(amd.Content, projectContent) {
		t.Errorf("expected project content in %q", amd.Content)
	}
}

// TestOrchestrator_AgentsMD_SearchPathsOnlyNoWorkspace verifies that search
// paths are read even when there is no workspace (CHAT / No Project mode),
// so global and c0wrk instructions still apply.
func TestOrchestrator_AgentsMD_SearchPathsOnlyNoWorkspace(t *testing.T) {
	globalDir := t.TempDir()
	globalContent := "# Global agent rules\nBe concise."
	globalPath := filepath.Join(globalDir, "AGENTS.md")
	if err := os.WriteFile(globalPath, []byte(globalContent), 0o644); err != nil {
		t.Fatalf("write global AGENTS.md: %v", err)
	}

	o := &Orchestrator{
		config: OrchestratorConfig{
			AgentsMDSearchPaths: []string{globalPath},
		},
	}

	// No workspace path in context — simulates CHAT / No Project mode.
	ctx := o.injectVectorSearchHints(context.Background(), "test query")

	amd := AgentsMDFromContext(ctx)
	if amd == nil {
		t.Fatal("expected AgentsMD in context even without workspace")
	}
	if !strings.Contains(amd.Content, globalContent) {
		t.Errorf("expected global content in %q", amd.Content)
	}
}

// TestOrchestrator_AgentsMD_RouterPromptInjection verifies that when
// AGENTS.md is present in the workspace, its content appears in the router's
// system prompt during routeAndActivateSkills. The router needs project context
// (tech stack, build commands, conventions) to correctly match skills.
func TestOrchestrator_AgentsMD_RouterPromptInjection(t *testing.T) {
	tmpDir := t.TempDir()
	agentsContent := "# Project Instructions\nTech stack: Go 1.26, React 19.\nRun `make test` before committing."
	if err := os.WriteFile(filepath.Join(tmpDir, "AGENTS.md"), []byte(agentsContent), 0o644); err != nil {
		t.Fatalf("failed to write AGENTS.md: %v", err)
	}

	var routerPromptSystem string
	callIdx := 0
	mockLLM := &mockLLMCaller{
		callFn: func(ctx context.Context, req llm.ChatRequest) (*llm.ChatResponse, error) {
			callIdx++
			// Capture the router system prompt (first call).
			if callIdx == 1 && len(req.Messages) > 0 && req.Messages[0].Role == "system" {
				routerPromptSystem = req.Messages[0].Content
			}
			// First call: router — return valid routing (no clarification).
			if callIdx == 1 {
				return &llm.ChatResponse{
					Message: llm.Message{
						Role:    "assistant",
						Content: `{"domain": "code", "complexity": 3, "needs_clarification": false, "matched_skills": []}`,
					},
					StopReason: "end_turn",
				}, nil
			}
			// Subsequent calls: Conductor executor — return a finish tool call.
			return &llm.ChatResponse{
				Message: llm.Message{
					Role: "assistant",
					ToolCalls: []llm.ToolCall{{
						ID:    "call_1",
						Name:  "finish",
						Input: json.RawMessage(`{"answer":"done"}`),
					}},
				},
				StopReason: "tool_use",
			}, nil
		},
	}

	registry := createTestRegistry()
	counter := llm.NewSimpleTokenCounter()

	r := newCoreRouter(mockLLM, 5)

	orchestrator := NewOrchestrator(OrchestratorConfig{}, OrchestratorDeps{
		Router:         r,
		LLM:            mockLLM,
		ToolExec:       registry,
		ToolRegistry:   registry,
		TokenCounter:   counter,
		ContextFactory: testContextFactory,
		CircuitBreaker: defaultCircuitBreakerConfig,
	})

	ctx := tools.WithWorkspacePath(context.Background(), tmpDir)
	_, err := orchestrator.HandleMessage(ctx, "build the project", "", HandleOptions{})
	if err != nil {
		t.Fatalf("HandleMessage failed: %v", err)
	}

	if routerPromptSystem == "" {
		t.Fatal("router system prompt was not captured")
	}

	// Verify AGENTS.md advisory framing appears in the router's system prompt.
	if !strings.Contains(routerPromptSystem, "<untrusted-content source=\"AGENTS.md\">") {
		t.Error("router system prompt should contain untrusted-content AGENTS.md tag")
	}
	if !strings.Contains(routerPromptSystem, "AGENTS.md") {
		t.Error("router system prompt should reference AGENTS.md")
	}
	if !strings.Contains(routerPromptSystem, "Tech stack: Go 1.26, React 19.") {
		t.Error("router system prompt should contain verbatim AGENTS.md content")
	}
}

// TestOrchestrator_AgentsMD_RouterPromptAbsentWhenNoWorkspace verifies that
// the router prompt does NOT contain AGENTS.md when there is no workspace
// (and therefore no AGENTS.md in context).
func TestOrchestrator_AgentsMD_RouterPromptAbsentWhenNoWorkspace(t *testing.T) {
	var routerPromptSystem string
	mockLLM := &mockLLMCaller{
		callFn: func(ctx context.Context, req llm.ChatRequest) (*llm.ChatResponse, error) {
			// Capture the router system prompt and short-circuit with
			// needs_clarification so the planner/executor are never called.
			if len(req.Messages) > 0 && req.Messages[0].Role == "system" {
				routerPromptSystem = req.Messages[0].Content
			}
			return &llm.ChatResponse{
				Message: llm.Message{
					Role:    "assistant",
					Content: `{"domain": "code", "complexity": 3, "needs_clarification": true}`,
				},
				StopReason: "end_turn",
			}, nil
		},
	}

	registry := createTestRegistry()
	counter := llm.NewSimpleTokenCounter()

	r := newCoreRouter(mockLLM, 5)

	orchestrator := NewOrchestrator(OrchestratorConfig{}, OrchestratorDeps{
		Router:         r,
		LLM:            mockLLM,
		ToolExec:       registry,
		ToolRegistry:   registry,
		TokenCounter:   counter,
		ContextFactory: testContextFactory,
		CircuitBreaker: defaultCircuitBreakerConfig,
	})

	// No workspace path in context — AGENTS.md should not be read or injected.
	ctx := context.Background()
	_, err := orchestrator.HandleMessage(ctx, "build the project", "", HandleOptions{})
	if err != nil {
		t.Fatalf("HandleMessage failed: %v", err)
	}

	if routerPromptSystem == "" {
		t.Fatal("router system prompt was not captured")
	}

	if strings.Contains(routerPromptSystem, "<untrusted-content source=\"AGENTS.md\">") {
		t.Error("router system prompt should NOT contain AGENTS.md untrusted-content tag when no workspace")
	}
}

// TestOrchestrator_Research_RouterPromptInjection verifies that in RESEARCH
// mode the router's system prompt contains the research-awareness block and
// the keyword-matching hints, so natural-language messages ("start an
// experiment", "add a hypothesis") can surface research-* skills without an
// explicit "/" prefix. It proves IsResearch flows from HandleMessage through
// to the router context.
func TestOrchestrator_Research_RouterPromptInjection(t *testing.T) {
	var routerPromptSystem string
	callIdx := 0
	mockLLM := &mockLLMCaller{
		callFn: func(ctx context.Context, req llm.ChatRequest) (*llm.ChatResponse, error) {
			callIdx++
			if callIdx == 1 && len(req.Messages) > 0 && req.Messages[0].Role == "system" {
				routerPromptSystem = req.Messages[0].Content
			}
			if callIdx == 1 {
				return &llm.ChatResponse{
					Message: llm.Message{
						Role:    "assistant",
						Content: `{"domain": "code", "complexity": 3, "needs_clarification": false, "matched_skills": []}`,
					},
					StopReason: "end_turn",
				}, nil
			}
			return &llm.ChatResponse{
				Message: llm.Message{
					Role: "assistant",
					ToolCalls: []llm.ToolCall{{
						ID:    "call_1",
						Name:  "finish",
						Input: json.RawMessage(`{"answer":"done"}`),
					}},
				},
				StopReason: "tool_use",
			}, nil
		},
	}

	registry := createTestRegistry()
	counter := llm.NewSimpleTokenCounter()
	r := newCoreRouter(mockLLM, 5)
	orchestrator := NewOrchestrator(OrchestratorConfig{}, OrchestratorDeps{
		Router:         r,
		LLM:            mockLLM,
		ToolExec:       registry,
		ToolRegistry:   registry,
		TokenCounter:   counter,
		ContextFactory: testContextFactory,
		CircuitBreaker: defaultCircuitBreakerConfig,
	})

	// RESEARCH mode: set the flag + research-root path pointing at the shared
	// research testdata fixture so a real snapshot is built.
	ctx := tools.WithWorkspacePath(context.Background(), "research/testdata")
	ctx = coretools.WithResearch(ctx)
	ctx = coretools.WithResearchRoot(ctx, "research/testdata")

	if _, err := orchestrator.HandleMessage(ctx, "start an experiment", "", HandleOptions{}); err != nil {
		t.Fatalf("HandleMessage failed: %v", err)
	}

	if routerPromptSystem == "" {
		t.Fatal("router system prompt was not captured")
	}
	// Research-awareness block (root path + active R-NNN + phase hint).
	if !strings.Contains(routerPromptSystem, "Research Context") {
		t.Error("router prompt should contain the Research Context section in RESEARCH mode")
	}
	// Keyword-matching hints that let natural-language intents map to research-*.
	if !strings.Contains(routerPromptSystem, "Research Skill Matching") {
		t.Error("router prompt should contain the Research Skill Matching hints")
	}
	if !strings.Contains(routerPromptSystem, "research-experiment") {
		t.Error("router prompt should reference research-experiment for natural-language experiment intents")
	}
}

// ---------------------------------------------------------------------------
// Per-step skills & tools — Normal-mode invariant and narrowing tests (Task 6)
// ---------------------------------------------------------------------------

// ---------------------------------------------------------------------------
// Conversation History Tests — full history, no truncation, planner compaction
// ---------------------------------------------------------------------------

// TestConversationHistory_NoTruncation verifies that after multiple messages,
// the conversationHistory accumulates all user+assistant messages without trimming.
func TestConversationHistory_NoTruncation(t *testing.T) {
	callIdx := 0
	mockLLM := &mockLLMCaller{
		callFn: func(ctx context.Context, req llm.ChatRequest) (*llm.ChatResponse, error) {
			callIdx++
			switch callIdx {
			case 1: // Router — first message "msg 1"
				return &llm.ChatResponse{
					Message:    llm.Message{Role: "assistant", Content: `{"domain": "code", "complexity": 3, "compaction_strategy": "sliding_window", "suggested_tools": [], "needs_clarification": false}`},
					StopReason: "end_turn",
				}, nil
			case 2: // Planner — first message
				return &llm.ChatResponse{
					Message:    llm.Message{Role: "assistant", Content: `{"steps": [{"id": "step_1", "summary": "Test", "description": "What: test\nHow: test\nWhere: test\nAcceptance Criteria: pass", "depends_on": [], "parallelizable": false, "estimated_tools": []}]}`},
					StopReason: "end_turn",
				}, nil
			case 3: // Executor — first message finish
				return &llm.ChatResponse{
					Message: llm.Message{
						Role: "assistant", Content: "Task completed",
						ToolCalls: []llm.ToolCall{{ID: "c1", Name: "finish", Input: json.RawMessage(`{"answer": "Done msg1"}`)}},
					},
					StopReason: "tool_use",
				}, nil
			case 4: // Router — continuation "msg 2"
				return &llm.ChatResponse{
					Message:    llm.Message{Role: "assistant", Content: `{"domain": "code", "complexity": 2, "compaction_strategy": "sliding_window", "suggested_tools": [], "needs_clarification": false}`},
					StopReason: "end_turn",
				}, nil
			case 5: // PlanContinuation — "msg 2"
				return &llm.ChatResponse{
					Message:    llm.Message{Role: "assistant", Content: `{"steps": [{"id": "cont_1", "summary": "Continue", "description": "What: continue\nHow: continue\nWhere: core\nAcceptance Criteria: done", "depends_on": [], "parallelizable": false, "estimated_tools": []}]}`},
					StopReason: "end_turn",
				}, nil
			case 6: // Executor — continuation "msg 2" finish
				return &llm.ChatResponse{
					Message: llm.Message{
						Role: "assistant", Content: "Continuation 1 done",
						ToolCalls: []llm.ToolCall{{ID: "c2", Name: "finish", Input: json.RawMessage(`{"answer": "Done msg2"}`)}},
					},
					StopReason: "tool_use",
				}, nil
			case 7: // Router — continuation "msg 3"
				return &llm.ChatResponse{
					Message:    llm.Message{Role: "assistant", Content: `{"domain": "code", "complexity": 2, "compaction_strategy": "sliding_window", "suggested_tools": [], "needs_clarification": false}`},
					StopReason: "end_turn",
				}, nil
			case 8: // PlanContinuation — "msg 3"
				return &llm.ChatResponse{
					Message:    llm.Message{Role: "assistant", Content: `{"steps": [{"id": "cont_2", "summary": "Continue more", "description": "What: continue\nHow: continue\nWhere: core\nAcceptance Criteria: done", "depends_on": [], "parallelizable": false, "estimated_tools": []}]}`},
					StopReason: "end_turn",
				}, nil
			default: // Executor — continuation "msg 3" finish
				return &llm.ChatResponse{
					Message: llm.Message{
						Role: "assistant", Content: "Continuation 2 done",
						ToolCalls: []llm.ToolCall{{ID: "c3", Name: "finish", Input: json.RawMessage(`{"answer": "Done msg3"}`)}},
					},
					StopReason: "tool_use",
				}, nil
			}
		},
	}

	registry := createTestRegistry()
	counter := llm.NewSimpleTokenCounter()

	r := newCoreRouter(mockLLM, 5)

	orchestrator := NewOrchestrator(OrchestratorConfig{}, OrchestratorDeps{
		Router:         r,
		LLM:            mockLLM,
		ToolExec:       registry,
		ToolRegistry:   registry,
		TokenCounter:   counter,
		ContextFactory: testContextFactory,
		CircuitBreaker: defaultCircuitBreakerConfig,
	})

	// Set up task persistence with a mutable task state for continuations.
	taskState := &TaskState{
		TaskID:          "task-fullhist",
		SessionID:       "session-1",
		OriginalRequest: "write REST API",
		Status:          "completed",
		Plan: &orchestration.Plan{
			Steps: []orchestration.PlanStep{
				{ID: "step_1", Description: "Create REST API"},
			},
		},
		StepResults: map[string]orchestration.StepResult{
			"step_1": {StepID: "step_1", FullOutput: "REST API created"},
		},
	}
	mockStore := &mockTaskStore{taskState: taskState}
	orchestrator.SetTaskStore(mockStore)
	orchestrator.SetBlackboardRestoreFunc(testBlackboardRestoreFunc())

	// First message.
	_, err := orchestrator.HandleMessage(context.Background(), "write REST API", "session-1", HandleOptions{})
	if err != nil {
		t.Fatalf("First message failed: %v", err)
	}

	// Second message (continuation).
	_, err = orchestrator.HandleMessage(context.Background(), "add auth", "session-1", HandleOptions{TaskID: "task-fullhist"})
	if err != nil {
		t.Fatalf("Second message (continuation) failed: %v", err)
	}

	// Third message (continuation).
	_, err = orchestrator.HandleMessage(context.Background(), "add tests", "session-1", HandleOptions{TaskID: "task-fullhist"})
	if err != nil {
		t.Fatalf("Third message (continuation) failed: %v", err)
	}

	history := orchestrator.ConversationHistory()
	// Expected: 3 user messages + 3 assistant messages = 6 total.
	if len(history) != 6 {
		t.Errorf("expected 6 messages in conversationHistory (3 user + 3 assistant), got %d", len(history))
	}

	// Verify all user messages are present in order.
	userMsgs := make([]string, 0, 3)
	for _, msg := range history {
		if msg.Role == "user" {
			userMsgs = append(userMsgs, msg.Content)
		}
	}
	if len(userMsgs) != 3 {
		t.Errorf("expected 3 user messages, got %d: %v", len(userMsgs), userMsgs)
	}
	if userMsgs[0] != "write REST API" {
		t.Errorf("expected first user message 'write REST API', got %q", userMsgs[0])
	}
	if userMsgs[1] != "add auth" {
		t.Errorf("expected second user message 'add auth', got %q", userMsgs[1])
	}
	if userMsgs[2] != "add tests" {
		t.Errorf("expected third user message 'add tests', got %q", userMsgs[2])
	}
}

// TestConversationHistory_RouterHistoryUnchanged verifies that the router still
// uses its own HistoryWindow (not the full history) and is unaffected by this change.
func TestConversationHistory_RouterHistoryUnchanged(t *testing.T) {
	// The router receives o.conversationHistory, which is now the full history.
	// The router's internal HistoryWindow setting controls how many messages it uses.
	// This test verifies that the router correctly limits its own window.

	callIdx := 0
	mockLLM := &mockLLMCaller{
		callFn: func(ctx context.Context, req llm.ChatRequest) (*llm.ChatResponse, error) {
			callIdx++
			if callIdx == 1 { // Router
				return &llm.ChatResponse{
					Message:    llm.Message{Role: "assistant", Content: `{"domain": "general", "complexity": 1, "compaction_strategy": "sliding_window", "suggested_tools": [], "needs_clarification": false}`},
					StopReason: "end_turn",
				}, nil
			}
			// Planner
			return &llm.ChatResponse{
				Message:    llm.Message{Role: "assistant", Content: `{"steps": [{"id": "step_1", "summary": "Test", "description": "What: test\nHow: test\nWhere: test\nAcceptance Criteria: pass", "depends_on": [], "parallelizable": false, "estimated_tools": []}]}`},
				StopReason: "end_turn",
			}, nil
		},
	}

	registry := createTestRegistry()
	counter := llm.NewSimpleTokenCounter()

	// Router with HistoryWindow=2 (only last 2 messages).
	r := newCoreRouter(mockLLM, 2)

	orchestrator := NewOrchestrator(OrchestratorConfig{}, OrchestratorDeps{
		Router:         r,
		LLM:            mockLLM,
		ToolExec:       registry,
		ToolRegistry:   registry,
		TokenCounter:   counter,
		ContextFactory: testContextFactory,
		CircuitBreaker: defaultCircuitBreakerConfig,
	})

	// Pre-populate history with 10 messages (5 exchanges).
	for i := 0; i < 5; i++ {
		orchestrator.conversationHistory = append(orchestrator.conversationHistory,
			llm.Message{Role: "user", Content: fmt.Sprintf("msg %d", i)},
			llm.Message{Role: "assistant", Content: fmt.Sprintf("resp %d", i)},
		)
	}

	// HandleMessage should succeed. The router will receive the full history,
	// but internally it should limit to HistoryWindow=2.
	_, err := orchestrator.HandleMessage(context.Background(), "test", "session-4", HandleOptions{})
	if err != nil {
		t.Fatalf("HandleMessage failed: %v", err)
	}

	// After HandleMessage, conversationHistory should have grown to 12 messages.
	history := orchestrator.ConversationHistory()
	if len(history) != 12 {
		t.Errorf("expected 12 messages in history (10 pre-populated + 2 new), got %d", len(history))
	}
}

// TestHandleMessage_ModelOverride_UpdatesConfigModel verifies that when a
// ModelOverride is provided, o.config.Model is updated to the bare name of
// the override. This ensures ContextWindow, buildSystemPrompt (family
// selection), and TokenizerType all reflect the active model rather than the
// default.
func TestHandleMessage_ModelOverride_UpdatesConfigModel(t *testing.T) {
	callIdx := 0
	mockLLM := &mockLLMCaller{
		callFn: func(ctx context.Context, req llm.ChatRequest) (*llm.ChatResponse, error) {
			callIdx++
			if callIdx == 1 {
				return &llm.ChatResponse{
					Message: llm.Message{
						Role:    "assistant",
						Content: `{"domain": "general", "complexity": 2, "compaction_strategy": "sliding_window", "suggested_tools": [], "needs_clarification": false}`,
					},
					StopReason: "end_turn",
				}, nil
			}
			return &llm.ChatResponse{
				Message: llm.Message{
					Role:    "assistant",
					Content: `{"steps": [{"id": "step_1", "summary": "Test", "description": "What: test\nHow: test\nWhere: test\nAcceptance Criteria: pass", "depends_on": [], "parallelizable": false, "estimated_tools": []}]}`,
				},
				StopReason: "end_turn",
			}, nil
		},
	}

	registry := createTestRegistry()
	counter := llm.NewSimpleTokenCounter()

	// Build an LLM Router with two models so SetModel has a target to switch to.
	llmRouter, err := llm.NewRouter(context.Background(), llm.RouterConfig{
		Providers: []llm.ProviderEntry{
			{Name: "openai", ProviderType: "openai", BaseURL: "http://localhost:9999", Models: []string{"default-model", "override-model"}},
		},
		MaxRetries:     1,
		InitialBackoff: 10 * time.Millisecond,
		MaxBackoff:     100 * time.Millisecond,
	}, nil)
	if err != nil {
		t.Fatalf("failed to build llm router: %v", err)
	}

	orchestrator := NewOrchestrator(OrchestratorConfig{
		Model: "default-model",
	}, OrchestratorDeps{
		Router:         newCoreRouter(mockLLM, 5),
		LLM:            mockLLM,
		ModelSwitcher:  llmRouter,
		ToolExec:       registry,
		ToolRegistry:   registry,
		TokenCounter:   counter,
		ContextFactory: testContextFactory,
		CircuitBreaker: defaultCircuitBreakerConfig,
	})

	if orchestrator.config.Model != "default-model" {
		t.Fatalf("expected initial config.Model 'default-model', got %q", orchestrator.config.Model)
	}

	_, err = orchestrator.HandleMessage(context.Background(), "test", "", HandleOptions{
		ModelOverride: "override-model",
	})
	if err != nil {
		t.Fatalf("HandleMessage failed: %v", err)
	}

	if orchestrator.config.Model != "override-model" {
		t.Errorf("expected config.Model updated to 'override-model', got %q", orchestrator.config.Model)
	}
}

// TestHandleMessage_ModelOverride_CompositeID_UpdatesConfigModel verifies that
// a composite model override (e.g. "Zen/glm-5.2") is reduced to the bare model
// name when stored in config.Model.
func TestHandleMessage_ModelOverride_CompositeID_UpdatesConfigModel(t *testing.T) {
	callIdx := 0
	mockLLM := &mockLLMCaller{
		callFn: func(ctx context.Context, req llm.ChatRequest) (*llm.ChatResponse, error) {
			callIdx++
			if callIdx == 1 {
				return &llm.ChatResponse{
					Message: llm.Message{
						Role:    "assistant",
						Content: `{"domain": "general", "complexity": 2, "compaction_strategy": "sliding_window", "suggested_tools": [], "needs_clarification": false}`,
					},
					StopReason: "end_turn",
				}, nil
			}
			return &llm.ChatResponse{
				Message: llm.Message{
					Role:    "assistant",
					Content: `{"steps": [{"id": "step_1", "summary": "Test", "description": "What: test\nHow: test\nWhere: test\nAcceptance Criteria: pass", "depends_on": [], "parallelizable": false, "estimated_tools": []}]}`,
				},
				StopReason: "end_turn",
			}, nil
		},
	}

	registry := createTestRegistry()
	counter := llm.NewSimpleTokenCounter()

	llmRouter, err := llm.NewRouter(context.Background(), llm.RouterConfig{
		Providers: []llm.ProviderEntry{
			{Name: "Zen", ProviderType: "openai", BaseURL: "http://localhost:9999", Models: []string{"glm-5.2"}},
			{Name: "DeepSeek", ProviderType: "openai", BaseURL: "http://localhost:9998", Models: []string{"deepseek-v4-pro"}},
		},
		MaxRetries:     1,
		InitialBackoff: 10 * time.Millisecond,
		MaxBackoff:     100 * time.Millisecond,
	}, nil)
	if err != nil {
		t.Fatalf("failed to build llm router: %v", err)
	}
	// Default to the DeepSeek model.
	if err := llmRouter.SetModel(context.Background(), "DeepSeek/deepseek-v4-pro"); err != nil {
		t.Fatalf("failed to set default model: %v", err)
	}

	orchestrator := NewOrchestrator(OrchestratorConfig{
		Model: "deepseek-v4-pro",
	}, OrchestratorDeps{
		Router:         newCoreRouter(mockLLM, 5),
		LLM:            mockLLM,
		ModelSwitcher:  llmRouter,
		ToolExec:       registry,
		ToolRegistry:   registry,
		TokenCounter:   counter,
		ContextFactory: testContextFactory,
		CircuitBreaker: defaultCircuitBreakerConfig,
	})

	_, err = orchestrator.HandleMessage(context.Background(), "test", "", HandleOptions{
		ModelOverride: "Zen/glm-5.2",
	})
	if err != nil {
		t.Fatalf("HandleMessage failed: %v", err)
	}

	// config.Model should be the BARE name, not the composite id.
	if orchestrator.config.Model != "glm-5.2" {
		t.Errorf("expected config.Model 'glm-5.2' (bare), got %q", orchestrator.config.Model)
	}
}

// TestAugmentWithAttachments verifies the user message is extended with a list
// of the session's attached files so the LLM can request their content via the
// read_attachment tool. Only the Conductor sees this section; the clean message
// is what gets recorded in conversation history.
func TestAugmentWithAttachments(t *testing.T) {
	o := new(Orchestrator)

	t.Run("no attachments leaves message unchanged", func(t *testing.T) {
		bb := orchestration.NewMapBlackboard()
		got := o.augmentWithAttachments("do something", bb)
		if got != "do something" {
			t.Errorf("expected unchanged message, got %q", got)
		}
	})

	t.Run("attachments appended with read_attachment guidance", func(t *testing.T) {
		bb := orchestration.NewMapBlackboard()
		bb.AddAttachment(orchestration.Attachment{
			ID:           "att-1",
			OriginalName: "report.pdf",
			Format:       "pdf",
			SizeBytes:    2048,
		})
		bb.AddAttachment(orchestration.Attachment{
			ID:           "att-2",
			OriginalName: "data.csv",
			Format:       "csv",
			SizeBytes:    512,
		})

		got := o.augmentWithAttachments("analyze these", bb)

		if !strings.Contains(got, "analyze these") {
			t.Errorf("augmented message lost the original text: %q", got)
		}
		if !strings.Contains(got, "## Attached files") {
			t.Errorf("missing '## Attached files' header: %q", got)
		}
		if !strings.Contains(got, "read_attachment") {
			t.Errorf("missing read_attachment guidance: %q", got)
		}
		for _, want := range []string{"att-1", "report.pdf", "pdf", "2048", "att-2", "data.csv", "csv", "512"} {
			if !strings.Contains(got, want) {
				t.Errorf("augmented message missing %q: %q", want, got)
			}
		}
	})

	t.Run("empty message gets attachment section only", func(t *testing.T) {
		bb := orchestration.NewMapBlackboard()
		bb.AddAttachment(orchestration.Attachment{ID: "x", OriginalName: "x.md", Format: "md", SizeBytes: 1})
		got := o.augmentWithAttachments("", bb)
		if !strings.HasPrefix(got, "## Attached files") {
			t.Errorf("expected attachment header at start for empty message, got %q", got)
		}
	})
}

// TestOrchestrator_CleanupHookRunsOnce verifies the OnCleanup lifecycle hook:
// Cleanup invokes the hook exactly once (session delete and app shutdown both
// call it), leaving repeated calls as no-ops, and a nil hook stays safe. The
// builder relies on this contract to release per-session registry tracking
// (registerSessionRegistry) without double-unregistering.
func TestOrchestrator_CleanupHookRunsOnce(t *testing.T) {
	var calls int
	o := NewOrchestrator(OrchestratorConfig{}, OrchestratorDeps{
		OnCleanup: func() { calls++ },
	})

	o.Cleanup()
	if calls != 1 {
		t.Fatalf("Cleanup hook called %d times, want exactly 1", calls)
	}

	// Idempotency: the second Cleanup (app shutdown after session delete)
	// must not re-run the hook.
	o.Cleanup()
	if calls != 1 {
		t.Fatalf("Cleanup hook called %d times after repeated Cleanup, want 1", calls)
	}

	// Nil hook must be safe.
	NewOrchestrator(OrchestratorConfig{}, OrchestratorDeps{}).Cleanup()
}
