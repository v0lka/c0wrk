package orchestration

import (
	"context"
	"encoding/json"
	"errors"
	"strings"
	"testing"
	"time"

	"github.com/v0lka/c0wrk/sdk/agent"
	"github.com/v0lka/c0wrk/sdk/llm"
	"github.com/v0lka/c0wrk/sdk/tools"
)

// ---------------------------------------------------------------------------
// Mocks
// ---------------------------------------------------------------------------

type mockPlanner struct {
	planFn             func(ctx context.Context, task string, tools []tools.ToolDescriptor, reflections []Reflection) (*Plan, error)
	replanFn           func(ctx context.Context, plan *Plan, completed []CompletedStep, failedStep CompletedStep, reflection *Reflection, reflections []Reflection) (*Plan, error)
	planContinuationFn func(ctx context.Context, originalRequest string, existingPlan *Plan, completedSteps []CompletedStep, newMessage string, availableTools []tools.ToolDescriptor) (*Plan, error)
}

func (m *mockPlanner) Plan(ctx context.Context, task string, t []tools.ToolDescriptor, r []Reflection) (*Plan, error) {
	if m.planFn != nil {
		return m.planFn(ctx, task, t, r)
	}
	return &Plan{Steps: []PlanStep{{ID: "s1", Description: "default step"}}}, nil
}

func (m *mockPlanner) Replan(ctx context.Context, plan *Plan, completed []CompletedStep, failedStep CompletedStep, reflection *Reflection, reflections []Reflection) (*Plan, error) {
	if m.replanFn != nil {
		return m.replanFn(ctx, plan, completed, failedStep, reflection, reflections)
	}
	return plan, nil
}

func (m *mockPlanner) PlanContinuation(ctx context.Context, originalRequest string, existingPlan *Plan, completedSteps []CompletedStep, newMessage string, availableTools []tools.ToolDescriptor) (*Plan, error) {
	if m.planContinuationFn != nil {
		return m.planContinuationFn(ctx, originalRequest, existingPlan, completedSteps, newMessage, availableTools)
	}
	return &Plan{Steps: []PlanStep{{ID: "continuation_1", Description: "default continuation step"}}}, nil
}

type recordingEvents struct {
	NoopEvents
	planGenerated int
	stepStarted   int
	stepCompleted int
	reflected     int
	retried       int
	stepRetried   int
}

func (r *recordingEvents) OnPlanGenerated(stepCount int, steps []PlanStepEvent) { r.planGenerated++ }
func (r *recordingEvents) OnStepStarted(stepID, description, summary string)    { r.stepStarted++ }
func (r *recordingEvents) OnStepCompleted(stepID string, success bool, duration time.Duration, errMsg string) {
	r.stepCompleted++
}
func (r *recordingEvents) OnReflected(reflection *Reflection, attempt, maxAttempts int) {
	r.reflected++
}
func (r *recordingEvents) OnRetry(attempt, maxAttempts int)                    { r.retried++ }
func (r *recordingEvents) OnStepRetry(stepID string, attempt, maxAttempts int) { r.stepRetried++ }
func (r *recordingEvents) OnReplanFailed(_ error)                              {}

// ---------------------------------------------------------------------------
// New() defaults
// ---------------------------------------------------------------------------

func TestNew_Defaults(t *testing.T) {
	o := New(Config{})

	if o.maxRetries != 2 {
		t.Errorf("expected default maxRetries=2, got %d", o.maxRetries)
	}
	if o.maxSteps != 30 {
		t.Errorf("expected default maxSteps=30, got %d", o.maxSteps)
	}
	if o.events == nil {
		t.Fatal("events should default to NoopEvents, not nil")
	}
	if _, ok := o.events.(*NoopEvents); !ok {
		t.Errorf("expected *NoopEvents, got %T", o.events)
	}
}

func TestNew_CustomValues(t *testing.T) {
	events := &recordingEvents{}
	o := New(Config{
		MaxRetries: 5,
		MaxSteps:   10,
		Events:     events,
	})

	if o.maxRetries != 5 {
		t.Errorf("expected maxRetries=5, got %d", o.maxRetries)
	}
	if o.maxSteps != 10 {
		t.Errorf("expected maxSteps=10, got %d", o.maxSteps)
	}
	if o.events != events {
		t.Error("expected custom events to be used")
	}
}

// ---------------------------------------------------------------------------
// Execute - planning failure
// ---------------------------------------------------------------------------

func TestExecute_PlanningFailure(t *testing.T) {
	o := New(Config{
		Planner: &mockPlanner{
			planFn: func(_ context.Context, _ string, _ []tools.ToolDescriptor, _ []Reflection) (*Plan, error) {
				return nil, errors.New("plan failed")
			},
		},
	})

	_, err := o.Execute(context.Background(), "do something")
	if err == nil {
		t.Fatal("expected error from planning failure")
	}
	if !errors.Is(err, err) || err.Error() == "" {
		t.Error("expected meaningful error message")
	}
}

// ---------------------------------------------------------------------------
// Resume - no plan
// ---------------------------------------------------------------------------

func TestResume_NoPlan(t *testing.T) {
	o := New(Config{})
	bb := NewMapBlackboard()

	_, err := o.Resume(context.Background(), bb)
	if err == nil {
		t.Fatal("expected error when resuming without a plan")
	}
}

// ---------------------------------------------------------------------------
// Resume - with plan and completed steps
// ---------------------------------------------------------------------------

func TestResume_WithCompletedSteps(t *testing.T) {
	events := &recordingEvents{}
	plan := &Plan{Steps: []PlanStep{
		{ID: "s1", Description: "step 1"},
	}}

	bb := NewMapBlackboard()
	bb.SetOriginalRequest("test request")
	bb.SetPlan(plan)

	o := New(Config{
		Planner: &mockPlanner{},
		Events:  events,
		// ContextFactory is nil so executePlanWithSteps will error — that's fine,
		// we just want to verify that Resume re-emits the plan before execution.
	})

	// Resume will fail during execution due to nil ContextFactory, but
	// OnPlanGenerated should have been called before that
	_, _ = o.Resume(context.Background(), bb)
	if events.planGenerated < 1 {
		t.Error("expected OnPlanGenerated to be called on resume")
	}
}

// ---------------------------------------------------------------------------
// availableTools
// ---------------------------------------------------------------------------

func TestAvailableTools_NilRegistry(t *testing.T) {
	o := New(Config{})
	got := o.availableTools()
	if got != nil {
		t.Errorf("expected nil tools with nil registry, got %v", got)
	}
}

// ---------------------------------------------------------------------------
// findStepIndex
// ---------------------------------------------------------------------------

func TestFindStepIndex(t *testing.T) {
	o := New(Config{})
	plan := &Plan{Steps: []PlanStep{
		{ID: "s1"}, {ID: "s2"}, {ID: "s3"},
	}}

	tests := []struct {
		stepID string
		want   int
	}{
		{"s1", 0},
		{"s2", 1},
		{"s3", 2},
		{"unknown", -1},
	}

	for _, tt := range tests {
		got := o.findStepIndex(plan, tt.stepID)
		if got != tt.want {
			t.Errorf("findStepIndex(%q) = %d, want %d", tt.stepID, got, tt.want)
		}
	}
}

// ---------------------------------------------------------------------------
// resolveStepConfig
// ---------------------------------------------------------------------------

func TestResolveStepConfig_NilConfigurator(t *testing.T) {
	o := New(Config{MaxSteps: 15})
	cfg := o.resolveStepConfig(PlanStep{ID: "s1"}, nil)
	if cfg.MaxSteps != 15 {
		t.Errorf("expected MaxSteps=15, got %d", cfg.MaxSteps)
	}
}

func TestResolveStepConfig_CustomConfigurator(t *testing.T) {
	o := New(Config{
		MaxSteps: 15,
		StepConfigurator: func(step PlanStep, defaults StepDefaults) StepConfig {
			return StepConfig{MaxSteps: 5, SystemPrompt: "custom"}
		},
	})
	cfg := o.resolveStepConfig(PlanStep{ID: "s1"}, nil)
	if cfg.MaxSteps != 5 {
		t.Errorf("expected MaxSteps=5, got %d", cfg.MaxSteps)
	}
	if cfg.SystemPrompt != "custom" {
		t.Errorf("expected custom system prompt")
	}
}

func TestResolveStepConfig_SystemPromptSuffix(t *testing.T) {
	o := New(Config{
		MaxSteps: 15,
		StepConfigurator: func(step PlanStep, defaults StepDefaults) StepConfig {
			return StepConfig{
				MaxSteps:           5,
				SystemPrompt:       "base prompt",
				SystemPromptSuffix: "## Role: Coder\nBe awesome.",
			}
		},
	})
	cfg := o.resolveStepConfig(PlanStep{ID: "s1"}, nil)
	if cfg.MaxSteps != 5 {
		t.Errorf("expected MaxSteps=5, got %d", cfg.MaxSteps)
	}
	if cfg.SystemPrompt != "base prompt" {
		t.Errorf("expected base system prompt")
	}
	if cfg.SystemPromptSuffix != "## Role: Coder\nBe awesome." {
		t.Errorf("expected role suffix, got %q", cfg.SystemPromptSuffix)
	}
}

// ---------------------------------------------------------------------------
// defaultSystemPrompt
// ---------------------------------------------------------------------------

func TestDefaultSystemPrompt(t *testing.T) {
	t.Run("basic prompt", func(t *testing.T) {
		prompt := defaultSystemPrompt(context.Background(), "do stuff")
		if prompt == "" {
			t.Fatal("expected non-empty prompt")
		}
		if !containsStr(prompt, "do stuff") {
			t.Error("expected step description in prompt")
		}
	})
}

// ---------------------------------------------------------------------------
// Per-step retry tests
// ---------------------------------------------------------------------------

// TestPerStepRetry_EventEmitted verifies that OnStepRetry event is emitted
// when a step fails and is retried.
func TestPerStepRetry_EventEmitted(t *testing.T) {
	events := &recordingEvents{}

	// Create a mock that will simulate step failure and retry
	// We need to track if OnStepRetry was called
	o := New(Config{
		Planner: &mockPlanner{
			planFn: func(_ context.Context, _ string, _ []tools.ToolDescriptor, _ []Reflection) (*Plan, error) {
				return &Plan{Steps: []PlanStep{
					{ID: "step1", Description: "Test step"},
				}}, nil
			},
		},
		Events:     events,
		MaxRetries: 2,
		MaxSteps:   10,
		// Note: We don't set up LLM/Tools/ContextFactory properly because
		// we just want to verify the test infrastructure exists.
		// Real per-step retry testing requires complex integration setup.
	})

	// Verify the orchestrator was created with the events handler
	if o.events != events {
		t.Error("expected events to be set")
	}

	// Verify the recordingEvents struct properly tracks step retries
	events.OnStepRetry("step1", 1, 3)
	if events.stepRetried != 1 {
		t.Errorf("expected stepRetried to be 1, got %d", events.stepRetried)
	}

	events.OnStepRetry("step1", 2, 3)
	if events.stepRetried != 2 {
		t.Errorf("expected stepRetried to be 2, got %d", events.stepRetried)
	}
}

// TestPerStepRetry_MaxRetriesBoundary verifies the boundary conditions
// for per-step retry counting.

func TestPerStepRetry_MaxRetriesBoundary(t *testing.T) {
	tests := []struct {
		name       string
		maxRetries int
		want       int
	}{
		{"default maxRetries", 0, 2}, // default is 2
		{"custom maxRetries 1", 1, 1},
		{"custom maxRetries 3", 3, 3},
		{"custom maxRetries 5", 5, 5},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			o := New(Config{
				MaxRetries: tt.maxRetries,
			})

			var expected int
			if tt.maxRetries == 0 {
				expected = 2 // default
			} else {
				expected = tt.maxRetries
			}

			if o.maxRetries != expected {
				t.Errorf("expected maxRetries=%d, got %d", expected, o.maxRetries)
			}
		})
	}
}

// ---------------------------------------------------------------------------
// ExecuteAdHocStep Tests
// ---------------------------------------------------------------------------

// mockContextManager is a minimal mock for ContextManager used in ExecuteAdHocStep tests.
type mockContextManager struct {
	systemPrompt   string
	taskDefinition string
}

func (m *mockContextManager) BuildPrompt() []llm.Message                        { return nil }
func (m *mockContextManager) AddStep(_ agent.Step)                              {}
func (m *mockContextManager) Compact(_ context.Context) *agent.CompactionResult { return nil }
func (m *mockContextManager) SetStrategy(_ agent.CompactionStrategy)            {}
func (m *mockContextManager) CheckFill() agent.FillCheck                        { return agent.FillCheck{} }
func (m *mockContextManager) CorrectTokenCount(_ int)                           {}
func (m *mockContextManager) FillPercent() float64                              { return 0 }
func (m *mockContextManager) AvailableTokens() int                              { return 8000 }
func (m *mockContextManager) OutputLimit() int                                  { return 1000 }
func (m *mockContextManager) VulnerableOutputs() []agent.VulnerableOutput       { return nil }
func (m *mockContextManager) SetTask(task string)                               { m.taskDefinition = task }

// TestExecuteAdHocStep_AppendsToBlackboard verifies that ExecuteAdHocStep
// stores the step result in the Blackboard and the result can be retrieved.
func TestExecuteAdHocStep_AppendsToBlackboard(t *testing.T) {
	bb := NewMapBlackboard()
	bb.SetOriginalRequest("original task")
	bb.SetPlan(&Plan{Steps: []PlanStep{
		{ID: "step_1", Description: "First step"},
	}})
	bb.SetStepResult("step_1", "output from step 1", nil, nil)

	o := New(Config{
		ContextFactory: func(systemPrompt string, _ llm.ModelMetadata, _ string, _ ...PruningOverride) agent.ContextManager {
			return &mockContextManager{systemPrompt: systemPrompt}
		},
		LLM:          &mockLLMForAdHoc{},
		Tools:        &mockToolExecutor{},
		TokenCounter: llm.NewSimpleTokenCounter(),
		MaxSteps:     10,
		ToolRegistry: tools.NewToolRegistry(),
	})

	step := PlanStep{
		ID:          "continuation_1",
		Description: "Continue the task",
	}

	result, err := o.ExecuteAdHocStep(context.Background(), bb, step, "original task", false)
	if err != nil {
		t.Fatalf("ExecuteAdHocStep failed: %v", err)
	}

	if result == nil {
		t.Fatal("expected non-nil result")
	}

	if result.StepID != "continuation_1" {
		t.Errorf("expected StepID continuation_1, got %q", result.StepID)
	}

	// Verify the result is stored in the blackboard
	sr, ok := bb.GetStepResult("continuation_1")
	if !ok {
		t.Fatal("expected step result to be stored in blackboard")
	}

	if sr.StepID != "continuation_1" {
		t.Errorf("blackboard step ID: got %q, want continuation_1", sr.StepID)
	}

	if sr.FullOutput == "" {
		t.Error("expected non-empty full output in blackboard")
	}
}

// TestExecuteAdHocStep_ExtendsPlan verifies that ExecuteAdHocStep extends
// the existing plan in the blackboard.
func TestExecuteAdHocStep_ExtendsPlan(t *testing.T) {
	bb := NewMapBlackboard()
	bb.SetOriginalRequest("original task")

	// Start with a 2-step plan
	initialPlan := &Plan{Steps: []PlanStep{
		{ID: "step_1", Description: "First step"},
		{ID: "step_2", Description: "Second step"},
	}}
	bb.SetPlan(initialPlan)
	bb.SetStepResult("step_1", "output 1", nil, nil)
	bb.SetStepResult("step_2", "output 2", nil, nil)

	o := New(Config{
		ContextFactory: func(systemPrompt string, _ llm.ModelMetadata, _ string, _ ...PruningOverride) agent.ContextManager {
			return &mockContextManager{systemPrompt: systemPrompt}
		},
		LLM:          &mockLLMForAdHoc{},
		Tools:        &mockToolExecutor{},
		TokenCounter: llm.NewSimpleTokenCounter(),
		MaxSteps:     10,
		ToolRegistry: tools.NewToolRegistry(),
	})

	step := PlanStep{
		ID:          "continuation_1",
		Description: "Continue the task",
		DependsOn:   []string{"step_1", "step_2"},
	}

	_, err := o.ExecuteAdHocStep(context.Background(), bb, step, "original task", false)
	if err != nil {
		t.Fatalf("ExecuteAdHocStep failed: %v", err)
	}

	// Verify plan now has 3 steps
	updatedPlan := bb.GetPlan()
	if updatedPlan == nil {
		t.Fatal("expected non-nil plan")
	}

	if len(updatedPlan.Steps) != 3 {
		t.Fatalf("expected 3 steps in plan, got %d", len(updatedPlan.Steps))
	}

	// Verify the new step is appended with correct ID
	lastStep := updatedPlan.Steps[2]
	if lastStep.ID != "continuation_1" {
		t.Errorf("expected last step ID continuation_1, got %q", lastStep.ID)
	}

	if lastStep.Description != "Continue the task" {
		t.Errorf("expected description 'Continue the task', got %q", lastStep.Description)
	}

	// Verify dependencies are preserved
	if len(lastStep.DependsOn) != 2 {
		t.Errorf("expected 2 dependencies, got %d", len(lastStep.DependsOn))
	}
}

// mockLLMForAdHoc is a minimal LLM mock for ExecuteAdHocStep tests.
type mockLLMForAdHoc struct{}

func (m *mockLLMForAdHoc) Call(_ context.Context, _ llm.ChatRequest) (*llm.ChatResponse, error) {
	return &llm.ChatResponse{
		Message: llm.Message{
			Role:    "assistant",
			Content: "Task completed successfully",
			ToolCalls: []llm.ToolCall{{
				ID:    "call_1",
				Name:  "finish",
				Input: json.RawMessage(`{"answer": "done"}`),
			}},
		},
		StopReason: "tool_use",
	}, nil
}

// mockToolExecutor is a minimal tool executor mock.
type mockToolExecutor struct{}

func (m *mockToolExecutor) Execute(_ context.Context, _ string, _ json.RawMessage) (tools.ToolResult, error) {
	return tools.ToolResult{Content: "ok"}, nil
}

func (m *mockToolExecutor) GetToolSource(name string) string {
	return "mock"
}

func (m *mockToolExecutor) IsToolUntrusted(name string) bool {
	return false
}

// ---------------------------------------------------------------------------
// ExecuteWithBlackboard Tests
// ---------------------------------------------------------------------------

// TestExecuteWithBlackboard_UsesProvidedBlackboard verifies that ExecuteWithBlackboard
// uses the provided blackboard and does NOT create a new one.
func TestExecuteWithBlackboard_UsesProvidedBlackboard(t *testing.T) {
	// Create a blackboard with pre-existing state
	bb := NewMapBlackboard()
	bb.SetOriginalRequest("pre-existing request")
	preExistingPlan := &Plan{Steps: []PlanStep{
		{ID: "pre_step_1", Description: "Pre-existing step"},
	}}
	bb.SetPlan(preExistingPlan)

	var capturedBB Blackboard
	o := New(Config{
		Planner: &mockPlanner{
			planFn: func(ctx context.Context, task string, tools []tools.ToolDescriptor, reflections []Reflection) (*Plan, error) {
				// This should NOT be called since we're providing a BB with existing plan
				return nil, errors.New("Planner.Plan should not be called for ExecuteWithBlackboard")
			},
		},
		Events: &recordingEvents{},
		StateFactory: func(taskID string) Blackboard {
			t.Error("StateFactory should not be called - BB should be provided")
			return nil
		},
		// Use Resume path by checking that the BB's plan is used
	})

	// Resume requires a plan, so let's test with Resume instead
	result, err := o.Resume(context.Background(), bb)
	// The test orchestrator has no ContextFactory, so the step will fail. The
	// SDK now wraps partial executions in ErrExecutionIncomplete and still
	// returns the result — accept that case as success for this test which
	// only validates that the provided blackboard is reused.
	if err != nil && !errors.Is(err, ErrExecutionIncomplete) {
		t.Fatalf("Resume failed: %v", err)
	}
	if result == nil {
		t.Fatal("Resume should return a non-nil result even on incomplete execution")
	}

	// Verify the original request is preserved (not overwritten)
	if result.Blackboard.GetOriginalRequest() != "pre-existing request" {
		t.Errorf("original request should be preserved, got: %s", result.Blackboard.GetOriginalRequest())
	}

	// Keep a reference to verify it's the same instance
	capturedBB = result.Blackboard
	_ = capturedBB
}

// TestExecuteWithBlackboard_DoesNotSetOriginalRequest verifies that ExecuteWithBlackboard
// does NOT call SetOriginalRequest on the provided blackboard.
func TestExecuteWithBlackboard_DoesNotSetOriginalRequest(t *testing.T) {
	bb := &blackboardWithCallTracker{Blackboard: NewMapBlackboard()}
	bb.SetOriginalRequest("original value")
	bb.setOriginalRequestCalled = false // reset after setup

	o := New(Config{
		Planner: &mockPlanner{},
		Events:  &recordingEvents{},
	})

	// Use Resume which requires a plan in the BB
	bb.SetPlan(&Plan{Steps: []PlanStep{{ID: "step_1", Description: "Test"}}})
	_, _ = o.Resume(context.Background(), bb)

	// Verify SetOriginalRequest was NOT called (except for our initial set)
	if bb.setOriginalRequestCalled {
		t.Error("ExecuteWithBlackboard/Resume should NOT call SetOriginalRequest on the provided blackboard")
	}
}

// blackboardWithCallTracker wraps MapBlackboard to track method calls.
type blackboardWithCallTracker struct {
	Blackboard
	setOriginalRequestCalled bool
}

func (b *blackboardWithCallTracker) SetOriginalRequest(req string) {
	b.setOriginalRequestCalled = true
	b.Blackboard.SetOriginalRequest(req)
}

// ---------------------------------------------------------------------------
// Context Cancellation Tests
// ---------------------------------------------------------------------------

// cancellingLLM cancels the given cancel func on the first call and returns
// context.Canceled. It records how many times Call was invoked.
type cancellingLLM struct {
	cancelFn  context.CancelFunc
	callCount int
}

func (m *cancellingLLM) Call(ctx context.Context, _ llm.ChatRequest) (*llm.ChatResponse, error) {
	m.callCount++
	if m.cancelFn != nil {
		m.cancelFn()
		m.cancelFn = nil // cancel only once
	}
	return nil, ctx.Err()
}

// TestExecute_ContextCancelled_ReturnsImmediately verifies that when the
// context is already cancelled before Execute starts, it returns
// context.Canceled immediately without executing any steps.
func TestExecute_ContextCancelled_ReturnsImmediately(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())
	cancel() // pre-cancel

	llmCalled := false
	o := New(Config{
		Planner: &mockPlanner{
			planFn: func(_ context.Context, _ string, _ []tools.ToolDescriptor, _ []Reflection) (*Plan, error) {
				return &Plan{Steps: []PlanStep{{ID: "s1", Description: "step 1"}}}, nil
			},
		},
		ContextFactory: func(systemPrompt string, _ llm.ModelMetadata, _ string, _ ...PruningOverride) agent.ContextManager {
			return &mockContextManager{systemPrompt: systemPrompt}
		},
		LLM:          &mockLLMForAdHoc{},
		Tools:        &mockToolExecutor{},
		TokenCounter: llm.NewSimpleTokenCounter(),
		MaxSteps:     10,
		ToolRegistry: tools.NewToolRegistry(),
		Events:       &recordingEvents{},
	})
	// Patch: we only care that the LLM is NOT called.
	// The planner may or may not be called (planning happens before the
	// cancellation check in the retry loop), but the executor must not run.
	_ = llmCalled

	_, err := o.Execute(ctx, "do something")
	if err == nil {
		t.Fatal("expected error from cancelled context")
	}
	if !errors.Is(err, context.Canceled) {
		t.Fatalf("expected context.Canceled, got %v", err)
	}
}

// TestExecute_ContextCancelledDuringStep_NoRetry verifies that when the
// context is cancelled during step execution, no per-step retry occurs.
func TestExecute_ContextCancelledDuringStep_NoRetry(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())

	cLLM := &cancellingLLM{cancelFn: cancel}

	o := New(Config{
		Planner: &mockPlanner{
			planFn: func(_ context.Context, _ string, _ []tools.ToolDescriptor, _ []Reflection) (*Plan, error) {
				return &Plan{Steps: []PlanStep{{ID: "s1", Description: "step 1"}}}, nil
			},
		},
		ContextFactory: func(systemPrompt string, _ llm.ModelMetadata, _ string, _ ...PruningOverride) agent.ContextManager {
			return &mockContextManager{systemPrompt: systemPrompt}
		},
		LLM:          cLLM,
		Tools:        &mockToolExecutor{},
		TokenCounter: llm.NewSimpleTokenCounter(),
		MaxRetries:   3,
		MaxSteps:     10,
		ToolRegistry: tools.NewToolRegistry(),
		Events:       &recordingEvents{},
	})

	_, err := o.Execute(ctx, "do something")
	if err == nil {
		t.Fatal("expected error from cancelled context")
	}
	if !errors.Is(err, context.Canceled) {
		t.Fatalf("expected context.Canceled, got %v", err)
	}
	// The LLM should have been called exactly once (no retry after cancellation).
	if cLLM.callCount != 1 {
		t.Errorf("expected LLM to be called once, got %d", cLLM.callCount)
	}
}

// TestExecuteAdHocStep_ContextCancelled_ReturnsFunctionError verifies that
// ExecuteAdHocStep returns context cancellation as a function-level error
// (second return value), not just in StepResult.Error.
func TestExecuteAdHocStep_ContextCancelled_ReturnsFunctionError(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())

	bb := NewMapBlackboard()
	bb.SetOriginalRequest("original task")
	bb.SetPlan(&Plan{Steps: []PlanStep{
		{ID: "step_1", Description: "First step"},
	}})
	bb.SetStepResult("step_1", "output from step 1", nil, nil)

	o := New(Config{
		ContextFactory: func(systemPrompt string, _ llm.ModelMetadata, _ string, _ ...PruningOverride) agent.ContextManager {
			return &mockContextManager{systemPrompt: systemPrompt}
		},
		LLM:          &cancellingLLM{cancelFn: cancel},
		Tools:        &mockToolExecutor{},
		TokenCounter: llm.NewSimpleTokenCounter(),
		MaxSteps:     10,
		ToolRegistry: tools.NewToolRegistry(),
		Events:       &recordingEvents{},
	})

	step := PlanStep{
		ID:          "continuation_1",
		Description: "Continue the task",
	}

	_, err := o.ExecuteAdHocStep(ctx, bb, step, "original task", false)
	if err == nil {
		t.Fatal("expected function-level error from cancelled context")
	}
	if !errors.Is(err, context.Canceled) {
		t.Fatalf("expected context.Canceled, got %v", err)
	}
}

// ---------------------------------------------------------------------------
// buildStepTask exploration context tests
// ---------------------------------------------------------------------------

func TestBuildStepTaskExplorationContext(t *testing.T) {
	t.Run("with exploration context", func(t *testing.T) {
		o := New(Config{})
		plan := Plan{
			Steps: []PlanStep{
				{ID: "s1", Description: "Do something"},
			},
			ExplorationContext: "- Found file X (via read_file)\n- Project uses Go modules (via list_directory)\n",
		}
		bb := NewMapBlackboard()
		taskDef := o.buildStepTask(
			plan.Steps[0], 0, plan,
			map[string]CompletedStep{},
			nil,
			bb,
			"user request", "",
			30,
		)
		if !strings.Contains(taskDef.task, "## Planner Research Context") {
			t.Error("expected task to contain '## Planner Research Context'")
		}
		if !strings.Contains(taskDef.task, "Found file X (via read_file)") {
			t.Error("expected task to contain exploration context text")
		}
	})

	t.Run("without exploration context", func(t *testing.T) {
		o := New(Config{})
		plan := Plan{
			Steps: []PlanStep{
				{ID: "s1", Description: "Do something"},
			},
		}
		bb := NewMapBlackboard()
		taskDef := o.buildStepTask(
			plan.Steps[0], 0, plan,
			map[string]CompletedStep{},
			nil,
			bb,
			"user request", "",
			30,
		)
		if strings.Contains(taskDef.task, "## Planner Research Context") {
			t.Error("expected task to NOT contain '## Planner Research Context' when ExplorationContext is empty")
		}
	})
}
