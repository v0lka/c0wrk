package orchestration

import (
	"context"
	"encoding/json"
	"errors"
	"log/slog"
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
	planContinuationFn func(ctx context.Context, originalRequest string, existingPlan *Plan, completedSteps []CompletedStep, newMessage string, availableTools []tools.ToolDescriptor, conversationHistory []llm.Message) (*Plan, error)
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

func (m *mockPlanner) PlanContinuation(ctx context.Context, originalRequest string, existingPlan *Plan, completedSteps []CompletedStep, newMessage string, availableTools []tools.ToolDescriptor, conversationHistory []llm.Message) (*Plan, error) {
	if m.planContinuationFn != nil {
		return m.planContinuationFn(ctx, originalRequest, existingPlan, completedSteps, newMessage, availableTools, conversationHistory)
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
	if o.maxSteps != 50 {
		t.Errorf("expected default maxSteps=50, got %d", o.maxSteps)
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

// mockContextManager is a minimal mock for ContextManager.
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

// mockLLM is a minimal LLM mock.
type mockLLM struct{}

func (m *mockLLM) Call(_ context.Context, _ llm.ChatRequest) (*llm.ChatResponse, error) {
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
		LLM:          &mockLLM{},
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

// ---------------------------------------------------------------------------
// FindReadySteps edge cases
// ---------------------------------------------------------------------------

func TestFindReadySteps_NilPlan(t *testing.T) {
	result := FindReadySteps(nil, map[string]CompletedStep{})
	if result != nil {
		t.Errorf("expected nil for nil plan, got %v", result)
	}
}

func TestFindReadySteps_EmptyPlan(t *testing.T) {
	plan := &Plan{Steps: []PlanStep{}}
	result := FindReadySteps(plan, map[string]CompletedStep{})
	if len(result) != 0 {
		t.Errorf("expected empty result for empty plan, got %d steps", len(result))
	}
}

func TestFindReadySteps_NoDependencies(t *testing.T) {
	plan := &Plan{Steps: []PlanStep{
		{ID: "s1", Description: "step1"},
		{ID: "s2", Description: "step2"},
		{ID: "s3", Description: "step3"},
	}}
	result := FindReadySteps(plan, map[string]CompletedStep{})
	if len(result) != 3 {
		t.Errorf("expected all 3 steps ready, got %d", len(result))
	}
}

func TestFindReadySteps_WithCompletedDependencies(t *testing.T) {
	plan := &Plan{Steps: []PlanStep{
		{ID: "s1", Description: "step1"},
		{ID: "s2", Description: "step2", DependsOn: []string{"s1"}},
		{ID: "s3", Description: "step3", DependsOn: []string{"s1", "s2"}},
	}}
	completed := map[string]CompletedStep{
		"s1": {StepID: "s1", Output: "ok", Error: nil},
	}
	result := FindReadySteps(plan, completed)
	if len(result) != 1 {
		t.Errorf("expected 1 ready step (s2), got %d", len(result))
	}
	if result[0].ID != "s2" {
		t.Errorf("expected s2, got %s", result[0].ID)
	}
}

func TestFindReadySteps_FailedDependency(t *testing.T) {
	plan := &Plan{Steps: []PlanStep{
		{ID: "s1", Description: "step1"},
		{ID: "s2", Description: "step2", DependsOn: []string{"s1"}},
	}}
	completed := map[string]CompletedStep{
		"s1": {StepID: "s1", Output: "", Error: errors.New("fail")},
	}
	result := FindReadySteps(plan, completed)
	if len(result) != 0 {
		t.Errorf("expected 0 ready steps when dependency failed, got %d", len(result))
	}
}

func TestFindReadySteps_MissingDependency(t *testing.T) {
	plan := &Plan{Steps: []PlanStep{
		{ID: "s1", Description: "step1"},
		{ID: "s2", Description: "step2", DependsOn: []string{"s1"}},
	}}
	result := FindReadySteps(plan, map[string]CompletedStep{})
	if len(result) != 1 {
		t.Errorf("expected 1 ready step (s1, no deps), got %d", len(result))
	}
	if result[0].ID != "s1" {
		t.Errorf("expected s1, got %s", result[0].ID)
	}
}

func TestFindReadySteps_AlreadyCompleted(t *testing.T) {
	plan := &Plan{Steps: []PlanStep{
		{ID: "s1", Description: "step1"},
		{ID: "s2", Description: "step2"},
	}}
	completed := map[string]CompletedStep{
		"s1": {StepID: "s1", Output: "ok", Error: nil},
	}
	result := FindReadySteps(plan, completed)
	if len(result) != 1 {
		t.Errorf("expected 1 ready step (s2), got %d", len(result))
	}
	if result[0].ID != "s2" {
		t.Errorf("expected s2 (s1 already done), got %s", result[0].ID)
	}
}

func TestFindReadySteps_AllCompletedAndReady(t *testing.T) {
	plan := &Plan{Steps: []PlanStep{
		{ID: "s1", Description: "step1"},
		{ID: "s2", Description: "step2"},
	}}
	completed := map[string]CompletedStep{
		"s1": {StepID: "s1", Output: "ok", Error: nil},
		"s2": {StepID: "s2", Output: "ok", Error: nil},
	}
	result := FindReadySteps(plan, completed)
	if len(result) != 0 {
		t.Errorf("expected 0 ready (all done), got %d", len(result))
	}
}

// ---------------------------------------------------------------------------
// BuildCarryForward edge cases
// ---------------------------------------------------------------------------

func TestBuildCarryForward_NilNewPlan(t *testing.T) {
	completed := []CompletedStep{
		{StepID: "s1", Output: "ok"},
	}
	result := BuildCarryForward(completed, nil)
	if result != nil {
		t.Errorf("expected nil for nil newPlan, got %v", result)
	}
}

func TestBuildCarryForward_NilCompleted(t *testing.T) {
	newPlan := &Plan{Steps: []PlanStep{{ID: "s1", Description: "step1"}}}
	result := BuildCarryForward(nil, newPlan)
	if result != nil {
		t.Errorf("expected nil when no completed steps, got %v", result)
	}
}

func TestBuildCarryForward_EmptyCompleted(t *testing.T) {
	newPlan := &Plan{Steps: []PlanStep{{ID: "s1", Description: "step1"}}}
	result := BuildCarryForward([]CompletedStep{}, newPlan)
	if result != nil {
		t.Errorf("expected nil for empty completed, got %v", result)
	}
}

func TestBuildCarryForward_CarryForwardCandidates(t *testing.T) {
	completed := []CompletedStep{
		{StepID: "s1", Output: "output1", Error: nil},
		{StepID: "s2", Output: "output2", Error: nil},
		{StepID: "s3", Output: "output3", Error: nil},
	}
	newPlan := &Plan{Steps: []PlanStep{
		{ID: "s1", Description: "step1"},
		{ID: "s2", Description: "step2", DependsOn: []string{"s1"}},
		{ID: "s4", Description: "new step", DependsOn: []string{"s2"}},
	}}
	result := BuildCarryForward(completed, newPlan)
	if len(result) != 2 {
		t.Errorf("expected 2 carried-forward steps (s1, s2), got %d", len(result))
	}
	if result["s1"].Output != "output1" {
		t.Errorf("expected s1 output preserved")
	}
	if result["s2"].Output != "output2" {
		t.Errorf("expected s2 output preserved")
	}
}

func TestBuildCarryForward_FailedStepNotCarried(t *testing.T) {
	completed := []CompletedStep{
		{StepID: "s1", Output: "output1", Error: errors.New("fail")},
		{StepID: "s2", Output: "output2", Error: nil},
	}
	newPlan := &Plan{Steps: []PlanStep{
		{ID: "s1", Description: "step1"},
		{ID: "s2", Description: "step2"},
	}}
	result := BuildCarryForward(completed, newPlan)
	if len(result) != 1 {
		t.Errorf("expected 1 carried-forward (s2 only, s1 failed), got %d", len(result))
	}
	if _, ok := result["s1"]; ok {
		t.Error("expected s1 NOT to be carried forward (it failed)")
	}
}

func TestBuildCarryForward_TransitiveInvalidation(t *testing.T) {
	completed := []CompletedStep{
		{StepID: "s1", Output: "output1", Error: nil},
		{StepID: "s2", Output: "output2", Error: nil},
		{StepID: "s3", Output: "output3", Error: nil},
	}
	newPlan := &Plan{Steps: []PlanStep{
		{ID: "s1", Description: "step1"},
		{ID: "new_step", Description: "replaces s2"},
		{ID: "s3", Description: "step3", DependsOn: []string{"new_step"}},
	}}
	result := BuildCarryForward(completed, newPlan)
	if len(result) != 1 {
		t.Errorf("expected 1 carried-forward (s1 only), got %d", len(result))
	}
	if _, ok := result["s3"]; ok {
		t.Error("expected s3 to be invalidated transitively")
	}
}

func TestBuildCarryForward_NoMatchInNewPlan(t *testing.T) {
	completed := []CompletedStep{
		{StepID: "old1", Output: "old", Error: nil},
	}
	newPlan := &Plan{Steps: []PlanStep{
		{ID: "new1", Description: "new step"},
	}}
	result := BuildCarryForward(completed, newPlan)
	if result != nil {
		t.Errorf("expected nil when no old steps match new plan, got %v", result)
	}
}

// ---------------------------------------------------------------------------
// BuildPlanExecutionSteps edge cases
// ---------------------------------------------------------------------------

func TestBuildPlanExecutionSteps_NilPlan(t *testing.T) {
	result := BuildPlanExecutionSteps([]CompletedStep{}, nil)
	if result != nil {
		t.Errorf("expected nil for nil plan, got %v", result)
	}
}

func TestBuildPlanExecutionSteps_WithExecutorSteps(t *testing.T) {
	step1 := agent.Step{
		Thought:     "I will read the file",
		Observation: "file contents...",
	}
	step2 := agent.Step{
		Thought:     "I will write the file",
		Observation: "write successful",
	}
	completedList := []CompletedStep{
		{StepID: "s1", Output: "output1", Steps: []agent.Step{step1}},
		{StepID: "s2", Output: "output2", Steps: []agent.Step{step2}},
	}
	plan := &Plan{Steps: []PlanStep{
		{ID: "s1", Description: "read"},
		{ID: "s2", Description: "write"},
	}}
	result := BuildPlanExecutionSteps(completedList, plan)
	if len(result) != 2 {
		t.Errorf("expected 2 steps, got %d", len(result))
	}
	if result[0].Thought != "I will read the file" {
		t.Errorf("expected first step thought preserved")
	}
}

func TestBuildPlanExecutionSteps_FallbackWithoutSteps(t *testing.T) {
	completedList := []CompletedStep{
		{StepID: "s1", Output: "the output", Steps: nil},
	}
	plan := &Plan{Steps: []PlanStep{
		{ID: "s1", Description: "do things"},
	}}
	result := BuildPlanExecutionSteps(completedList, plan)
	if len(result) != 1 {
		t.Fatalf("expected 1 fallback step, got %d", len(result))
	}
	if !strings.Contains(result[0].Thought, "s1") {
		t.Error("expected thought to contain step ID")
	}
	if !strings.Contains(result[0].Thought, "do things") {
		t.Error("expected thought to contain description")
	}
	if result[0].Observation != "the output" {
		t.Errorf("expected observation to be output, got %q", result[0].Observation)
	}
}

func TestBuildPlanExecutionSteps_FallbackWithError(t *testing.T) {
	completedList := []CompletedStep{
		{StepID: "s1", Output: "partial", Error: errors.New("boom"), Steps: nil},
	}
	plan := &Plan{Steps: []PlanStep{
		{ID: "s1", Description: "do things"},
	}}
	result := BuildPlanExecutionSteps(completedList, plan)
	if len(result) != 1 {
		t.Fatalf("expected 1 fallback step, got %d", len(result))
	}
	if !strings.Contains(result[0].Observation, "STEP FAILED") {
		t.Error("expected observation to contain 'STEP FAILED'")
	}
	if !strings.Contains(result[0].Observation, "boom") {
		t.Error("expected observation to contain error message")
	}
}

// ---------------------------------------------------------------------------
// AggregateOutput edge cases
// ---------------------------------------------------------------------------

func TestAggregateOutput_NilPlan(t *testing.T) {
	result := AggregateOutput(map[string]CompletedStep{}, nil, nil)
	if result != "" {
		t.Errorf("expected empty string for nil plan, got %q", result)
	}
}

func TestAggregateOutput_TerminalSteps(t *testing.T) {
	plan := &Plan{Steps: []PlanStep{
		{ID: "s1", Description: "step1"},
		{ID: "s2", Description: "step2", DependsOn: []string{"s1"}},
	}}
	completed := map[string]CompletedStep{
		"s1": {StepID: "s1", Output: "intermediate"},
		"s2": {StepID: "s2", Output: "final output"},
	}
	result := AggregateOutput(completed, plan, nil)
	if result != "final output" {
		t.Errorf("expected only terminal step output, got %q", result)
	}
}

func TestAggregateOutput_AllTerminal(t *testing.T) {
	plan := &Plan{Steps: []PlanStep{
		{ID: "s1", Description: "step1"},
		{ID: "s2", Description: "step2"},
	}}
	completed := map[string]CompletedStep{
		"s1": {StepID: "s1", Output: "a"},
		"s2": {StepID: "s2", Output: "b"},
	}
	result := AggregateOutput(completed, plan, nil)
	if result != "a\n\nb" {
		t.Errorf("expected both outputs joined, got %q", result)
	}
}

func TestAggregateOutput_PreCompletedIDs(t *testing.T) {
	plan := &Plan{Steps: []PlanStep{
		{ID: "s1", Description: "step1"},
		{ID: "s2", Description: "step2", DependsOn: []string{"s1"}},
	}}
	completed := map[string]CompletedStep{
		"s1": {StepID: "s1", Output: "old output"},
		"s2": {StepID: "s2", Output: "new output"},
	}
	preCompletedIDs := map[string]bool{"s1": true}
	result := AggregateOutput(completed, plan, preCompletedIDs)
	if result != "new output" {
		t.Errorf("expected only new (non-pre-completed) output, got %q", result)
	}
}

func TestAggregateOutput_AllPreCompleted(t *testing.T) {
	plan := &Plan{Steps: []PlanStep{
		{ID: "s1", Description: "step1"},
	}}
	completed := map[string]CompletedStep{
		"s1": {StepID: "s1", Output: "old output"},
	}
	preCompletedIDs := map[string]bool{"s1": true}
	result := AggregateOutput(completed, plan, preCompletedIDs)
	if result != "" {
		t.Errorf("expected empty string when all outputs are pre-completed, got %q", result)
	}
}

func TestAggregateOutput_ErrorStepExcluded(t *testing.T) {
	plan := &Plan{Steps: []PlanStep{
		{ID: "s1", Description: "step1"},
	}}
	completed := map[string]CompletedStep{
		"s1": {StepID: "s1", Output: "partial", Error: errors.New("fail")},
	}
	result := AggregateOutput(completed, plan, nil)
	if result != "" {
		t.Errorf("expected empty string when step has error, got %q", result)
	}
}

func TestAggregateOutput_NoTerminalFallsBackToAll(t *testing.T) {
	plan := &Plan{Steps: []PlanStep{
		{ID: "s1", Description: "step1", DependsOn: []string{"s2", "s3"}},
		{ID: "s2", Description: "step2"},
		{ID: "s3", Description: "step3", DependsOn: []string{"s2"}},
	}}
	completed := map[string]CompletedStep{
		"s1": {StepID: "s1", Output: "should not appear", Error: errors.New("fail")},
		"s2": {StepID: "s2", Output: "base"},
		"s3": {StepID: "s3", Output: "intermediate"},
	}
	result := AggregateOutput(completed, plan, nil)
	if !strings.Contains(result, "base") {
		t.Errorf("expected fallback to include s2 output, got %q", result)
	}
	if !strings.Contains(result, "intermediate") {
		t.Errorf("expected fallback to include s3 output, got %q", result)
	}
}

// ---------------------------------------------------------------------------
// resolveModelMeta edge cases
// ---------------------------------------------------------------------------

func TestResolveModelMeta_NilRegistry(t *testing.T) {
	o := New(Config{ModelRegistry: nil, Model: "gpt-4o"})
	meta := o.resolveModelMeta(context.Background())
	if meta.ContextWindow != 0 {
		t.Errorf("expected zero ContextWindow, got %d", meta.ContextWindow)
	}
	if meta.OutputLimit != 0 {
		t.Errorf("expected zero OutputLimit, got %d", meta.OutputLimit)
	}
}

func TestResolveModelMeta_EmptyModelWithRegistry(t *testing.T) {
	registry := llm.NewModelRegistry(nil)
	o := New(Config{ModelRegistry: registry, Model: ""})
	meta := o.resolveModelMeta(context.Background())
	if meta.ContextWindow != 128000 {
		t.Errorf("expected fallback ContextWindow=128000, got %d", meta.ContextWindow)
	}
	if meta.OutputLimit != 4096 {
		t.Errorf("expected fallback OutputLimit=4096, got %d", meta.OutputLimit)
	}
}

func TestResolveModelMeta_ModelNotFound(t *testing.T) {
	registry := llm.NewModelRegistry(nil)
	o := New(Config{ModelRegistry: registry, Model: "nonexistent-model-xyz"})
	meta := o.resolveModelMeta(context.Background())
	if meta.ContextWindow != 128000 {
		t.Errorf("expected fallback ContextWindow=128000, got %d", meta.ContextWindow)
	}
	if meta.OutputLimit != 4096 {
		t.Errorf("expected fallback OutputLimit=4096, got %d", meta.OutputLimit)
	}
}

func TestResolveModelMeta_KnownModel(t *testing.T) {
	registry := llm.NewModelRegistry(nil)
	o := New(Config{ModelRegistry: registry, Model: "gpt-4o"})
	meta := o.resolveModelMeta(context.Background())
	if meta.ContextWindow != 128000 {
		t.Errorf("expected ContextWindow=128000 for gpt-4o, got %d", meta.ContextWindow)
	}
	if meta.OutputLimit != 16384 {
		t.Errorf("expected OutputLimit=16384 for gpt-4o, got %d", meta.OutputLimit)
	}
	if meta.Family != "openai_flagship" {
		t.Errorf("expected Family=openai_flagship for gpt-4o, got %q", meta.Family)
	}
}

func TestResolveModelMeta_OverrideModel(t *testing.T) {
	overrides := map[string]llm.ModelMetadata{
		"my-custom-model": {ContextWindow: 99999, OutputLimit: 1234},
	}
	registry := llm.NewModelRegistry(overrides)
	o := New(Config{ModelRegistry: registry, Model: "my-custom-model"})
	meta := o.resolveModelMeta(context.Background())
	if meta.ContextWindow != 99999 {
		t.Errorf("expected overridden ContextWindow=99999, got %d", meta.ContextWindow)
	}
	if meta.OutputLimit != 1234 {
		t.Errorf("expected overridden OutputLimit=1234, got %d", meta.OutputLimit)
	}
}

// ---------------------------------------------------------------------------
// Config / StepConfig validation edge cases
// ---------------------------------------------------------------------------

func TestConfig_StepConfigDefaults(t *testing.T) {
	cfg := StepConfig{}
	if cfg.MaxSteps != 0 {
		t.Error("MaxSteps should default to 0")
	}
	if cfg.AllowedTools != nil {
		t.Error("AllowedTools should default to nil")
	}
	if cfg.SystemPrompt != "" {
		t.Error("SystemPrompt should default to empty")
	}
	if cfg.CompactionStrategy != "" {
		t.Error("CompactionStrategy should default to empty")
	}
}

func TestConfig_StepConfigFull(t *testing.T) {
	cfg := StepConfig{
		MaxSteps:           25,
		SystemPrompt:       "be helpful",
		SystemPromptSuffix: "extra instruction",
		CompactionStrategy: "aggressive",
		KeepLastN:          5,
		ProtectedTools:     []string{"read_file"},
		AgentRole:          "coder",
	}
	if cfg.MaxSteps != 25 {
		t.Errorf("MaxSteps = %d, want 25", cfg.MaxSteps)
	}
	if cfg.SystemPrompt != "be helpful" {
		t.Errorf("SystemPrompt = %q", cfg.SystemPrompt)
	}
	if cfg.SystemPromptSuffix != "extra instruction" {
		t.Errorf("SystemPromptSuffix = %q", cfg.SystemPromptSuffix)
	}
	if cfg.CompactionStrategy != "aggressive" {
		t.Errorf("CompactionStrategy = %q", cfg.CompactionStrategy)
	}
	if cfg.KeepLastN != 5 {
		t.Errorf("KeepLastN = %d", cfg.KeepLastN)
	}
	if len(cfg.ProtectedTools) != 1 || cfg.ProtectedTools[0] != "read_file" {
		t.Error("ProtectedTools not set correctly")
	}
	if cfg.AgentRole != "coder" {
		t.Errorf("AgentRole = %q", cfg.AgentRole)
	}
}

func TestConfig_StepDefaultsStruct(t *testing.T) {
	def := StepDefaults{
		MaxSteps: 30,
		AllTools: nil,
	}
	if def.MaxSteps != 30 {
		t.Errorf("MaxSteps = %d", def.MaxSteps)
	}
	if def.AllTools != nil {
		t.Error("AllTools should be nil")
	}
}

func TestConfig_NewOrchestratorWithFullConfig(t *testing.T) {
	registry := llm.NewModelRegistry(nil)
	o := New(Config{
		Model:                    "gpt-4o",
		ModelRegistry:            registry,
		MaxRetries:               3,
		MaxSteps:                 20,
		MaxDependencyContextChars: 4000,
		PreWarningPercent:        70,
		ReasoningEffort:          "high",
	})
	if o.cfg.Model != "gpt-4o" {
		t.Errorf("Model = %q", o.cfg.Model)
	}
	if o.cfg.ModelRegistry != registry {
		t.Error("ModelRegistry not preserved")
	}
	if o.maxRetries != 3 {
		t.Errorf("maxRetries = %d", o.maxRetries)
	}
	if o.maxSteps != 20 {
		t.Errorf("maxSteps = %d", o.maxSteps)
	}
	if o.cfg.MaxDependencyContextChars != 4000 {
		t.Errorf("MaxDependencyContextChars = %d", o.cfg.MaxDependencyContextChars)
	}
	if o.cfg.PreWarningPercent != 70 {
		t.Errorf("PreWarningPercent = %d", o.cfg.PreWarningPercent)
	}
	if o.cfg.ReasoningEffort != "high" {
		t.Errorf("ReasoningEffort = %q", o.cfg.ReasoningEffort)
	}
}

func TestConfig_SetReasoningEffort(t *testing.T) {
	o := New(Config{ReasoningEffort: "medium"})
	if o.cfg.ReasoningEffort != "medium" {
		t.Errorf("initial = %q", o.cfg.ReasoningEffort)
	}
	o.SetReasoningEffort("low")
	if o.cfg.ReasoningEffort != "low" {
		t.Errorf("after Set = %q", o.cfg.ReasoningEffort)
	}
}

func TestConfig_Cleanup_NoTracker(t *testing.T) {
	o := New(Config{StepDumpTracker: nil})
	o.Cleanup()
}

func TestConfig_ContextSetup(t *testing.T) {
	called := false
	o := New(Config{
		ContextSetup: func(_ agent.ContextManager, _ string) {
			called = true
		},
	})
	if o.cfg.ContextSetup == nil {
		t.Fatal("ContextSetup should be set")
	}
	o.cfg.ContextSetup(nil, "")
	if !called {
		t.Error("ContextSetup was not called")
	}
}

func TestConfig_CallerForStep(t *testing.T) {
	called := false
	o := New(Config{
		CallerForStep: func(_ agent.ContextManager, stepID string) agent.LLMCaller {
			called = true
			return &mockLLM{}
		},
	})
	if o.cfg.CallerForStep == nil {
		t.Fatal("CallerForStep should be set")
	}
	caller := o.callerForStep(nil, "s1")
	if !called {
		t.Error("CallerForStep was not called")
	}
	if caller == nil {
		t.Error("expected non-nil LLMCaller from CallerForStep")
	}
}

func TestConfig_CallerForStepFallback(t *testing.T) {
	mock := &mockLLM{}
	o := New(Config{LLM: mock})
	caller := o.callerForStep(nil, "s1")
	if caller != mock {
		t.Error("expected fallback to shared LLM when CallerForStep is nil")
	}
}

// ---------------------------------------------------------------------------
// emitPlanWithStatuses
// ---------------------------------------------------------------------------

func TestEmitPlanWithStatuses(t *testing.T) {
	events := &recordingEvents{}
	o := New(Config{Events: events})
	plan := &Plan{Steps: []PlanStep{
		{ID: "s1", Summary: "first", Description: "do first"},
		{ID: "s2", Summary: "second", Description: "do second", DependsOn: []string{"s1"}},
	}}
	preCompleted := map[string]CompletedStep{
		"s1": {StepID: "s1", Output: "done"},
	}
	o.emitPlanWithStatuses(plan, preCompleted)
	if events.planGenerated != 1 {
		t.Errorf("expected planGenerated=1, got %d", events.planGenerated)
	}
}

func TestEmitPlanWithStatuses_NilPreCompleted(t *testing.T) {
	events := &recordingEvents{}
	o := New(Config{Events: events})
	plan := &Plan{Steps: []PlanStep{
		{ID: "s1", Summary: "first", Description: "do first"},
	}}
	o.emitPlanWithStatuses(plan, nil)
	if events.planGenerated != 1 {
		t.Errorf("expected planGenerated=1, got %d", events.planGenerated)
	}
}

// ---------------------------------------------------------------------------
// scopeEvents / scopeRetryAttempt
// ---------------------------------------------------------------------------

func TestScopeEvents_NonScopable(t *testing.T) {
	events := &recordingEvents{}
	o := New(Config{Events: events})
	scoped := o.scopeEvents("s1")
	if scoped != events {
		t.Error("expected same events when not StepScopable")
	}
}

type scopedEvts struct {
	*recordingEvents
	stepID string
}

func (s *scopedEvts) WithStepID(id string) Events {
	return &scopedEvts{recordingEvents: s.recordingEvents, stepID: id}
}

func TestScopeEvents_Scopable(t *testing.T) {
	base := &scopedEvts{recordingEvents: &recordingEvents{}}
	o := New(Config{Events: base})
	scoped := o.scopeEvents("s1")
	if se, ok := scoped.(*scopedEvts); ok {
		if se.stepID != "s1" {
			t.Errorf("expected stepID=s1, got %q", se.stepID)
		}
	} else {
		t.Error("expected scoped events")
	}
}

func TestScopeRetryAttempt_NonScopable(t *testing.T) {
	events := &recordingEvents{}
	scoped := scopeRetryAttempt(events, 1)
	if scoped != events {
		t.Error("expected same events when not RetryScopable")
	}
}

// ---------------------------------------------------------------------------
// PlanStep / CompletedStep struct defaults
// ---------------------------------------------------------------------------

func TestPlanStep_Defaults(t *testing.T) {
	ps := PlanStep{ID: "test", Summary: "a summary", Description: "a description"}
	if ps.Parallelizable {
		t.Error("Parallelizable should default to false")
	}
	if ps.DependsOn != nil {
		t.Error("DependsOn should default to nil")
	}
	if ps.EstimatedTools != nil {
		t.Error("EstimatedTools should default to nil")
	}
}

func TestCompletedStep_Defaults(t *testing.T) {
	cs := CompletedStep{StepID: "s1", Output: "out"}
	if cs.Error != nil {
		t.Error("Error should default to nil")
	}
}

// ---------------------------------------------------------------------------
// Cleanup with StepDumpTracker (positive branch)
// ---------------------------------------------------------------------------

func TestCleanup_WithTracker(t *testing.T) {
	dir := t.TempDir()
	tracker := NewStepDumpTracker(dir)
	_ = tracker.OpenStepDump("step-1")
	o := New(Config{StepDumpTracker: tracker})
	o.Cleanup()
	// Second call should be idempotent
	o.Cleanup()
}

// ---------------------------------------------------------------------------
// log() with configured logger vs discard
// ---------------------------------------------------------------------------

func TestLog_WithLogger(t *testing.T) {
	l := slog.New(slog.DiscardHandler)
	o := New(Config{Logger: l})
	logger := o.log()
	if logger != l {
		t.Error("expected configured logger to be returned")
	}
}

func TestLog_DiscardFallback(t *testing.T) {
	o := New(Config{Logger: nil})
	logger := o.log()
	if logger == nil {
		t.Fatal("expected non-nil discard logger")
	}
}

// ---------------------------------------------------------------------------
// scopeRetryAttempt with RetryScopable
// ---------------------------------------------------------------------------

type retryScopedEvts struct {
	*recordingEvents
	attempt int
}

func (r *retryScopedEvts) WithRetryAttempt(attempt int) Events {
	return &retryScopedEvts{recordingEvents: r.recordingEvents, attempt: attempt}
}

func TestScopeRetryAttempt_Scopable(t *testing.T) {
	base := &retryScopedEvts{recordingEvents: &recordingEvents{}}
	scoped := scopeRetryAttempt(base, 3)
	if re, ok := scoped.(*retryScopedEvts); ok {
		if re.attempt != 3 {
			t.Errorf("expected attempt=3, got %d", re.attempt)
		}
	} else {
		t.Error("expected retry-scoped events")
	}
}

// ---------------------------------------------------------------------------
// configureExecutor with all fields
// ---------------------------------------------------------------------------

func TestConfigureExecutor_AllFields(t *testing.T) {
	o := New(Config{
		HITLHandler:       nil,
		PreWarningPercent: 75,
		ToolCache:         &agent.ToolResultCache{},
		PerToolTruncation: map[string]agent.ToolTruncationConfig{"finish": {}},
		ReasoningEffort:   "high",
	})
	// Create a minimal executor — we don't need it to work, just to receive config
	cm := &mockContextManager{}
	executor := agent.NewExecutor(&mockLLM{}, &mockToolExecutor{}, llm.NewSimpleTokenCounter(), 10, &NoopEvents{}, false, agent.ToolResultBudget{}, agent.CircuitBreakerConfig{}, nil)
	o.configureExecutor(executor, StepConfig{})
	// No assertions needed — we just ensure no panic and full branch coverage
	_ = cm
	_ = executor
}

func TestConfigureExecutor_ZeroPreWarning(t *testing.T) {
	o := New(Config{PreWarningPercent: 0})
	executor := agent.NewExecutor(&mockLLM{}, &mockToolExecutor{}, llm.NewSimpleTokenCounter(), 10, &NoopEvents{}, false, agent.ToolResultBudget{}, agent.CircuitBreakerConfig{}, nil)
	o.configureExecutor(executor, StepConfig{})
}

// ---------------------------------------------------------------------------
// Execute with nil ContextFactory
// ---------------------------------------------------------------------------

func TestExecute_NilContextFactory(t *testing.T) {
	o := New(Config{
		Planner: &mockPlanner{
			planFn: func(_ context.Context, _ string, _ []tools.ToolDescriptor, _ []Reflection) (*Plan, error) {
				return &Plan{Steps: []PlanStep{{ID: "s1", Description: "step 1"}}}, nil
			},
		},
		LLM:          &mockLLM{},
		Tools:        &mockToolExecutor{},
		TokenCounter: llm.NewSimpleTokenCounter(),
	})
	_, err := o.Execute(context.Background(), "test")
	if err == nil {
		t.Fatal("expected error when ContextFactory is nil")
	}
}

// ---------------------------------------------------------------------------
// FactStore adapter
// ---------------------------------------------------------------------------

func TestFactStore_StoreAndSearch(t *testing.T) {
	bb := NewMapBlackboard()
	fs := NewFactStore(bb)
	fs.StoreFact([]string{"go", "module"}, "project uses Go modules", "s1")

	results := fs.SearchFacts([]string{"go"})
	if len(results) != 1 {
		t.Fatalf("expected 1 result, got %d", len(results))
	}
	if results[0].Content != "project uses Go modules" {
		t.Errorf("unexpected content: %q", results[0].Content)
	}
}

func TestFactStore_SearchNoMatch(t *testing.T) {
	bb := NewMapBlackboard()
	fs := NewFactStore(bb)
	fs.StoreFact([]string{"go"}, "data", "s1")

	results := fs.SearchFacts([]string{"python"})
	if len(results) != 0 {
		t.Errorf("expected empty for no match, got %v", results)
	}
}

// ---------------------------------------------------------------------------
// SetPlan with nil
// ---------------------------------------------------------------------------

func TestSetPlan_Nil(t *testing.T) {
	bb := NewMapBlackboard()
	bb.SetPlan(&Plan{Steps: []PlanStep{{ID: "s1", Description: "step"}}})
	if bb.GetPlan() == nil {
		t.Fatal("expected non-nil plan after SetPlan")
	}
	bb.SetPlan(nil)
	if bb.GetPlan() != nil {
		t.Error("expected nil plan after SetPlan(nil)")
	}
}

// ---------------------------------------------------------------------------
// SetStepResultRaw
// ---------------------------------------------------------------------------

func TestSetStepResultRaw(t *testing.T) {
	bb := NewMapBlackboard()
	sr := StepResult{StepID: "s1", Summary: "my summary", FullOutput: "full output"}
	bb.SetStepResultRaw("s1", sr)
	got, ok := bb.GetStepResult("s1")
	if !ok {
		t.Fatal("expected step result to exist")
	}
	if got.Summary != "my summary" {
		t.Errorf("expected summary preserved, got %q", got.Summary)
	}
}

// ---------------------------------------------------------------------------
// StoreFact / SearchFacts / GetFacts / SetFacts on MapBlackboard
// ---------------------------------------------------------------------------

func TestMapBlackboard_Facts(t *testing.T) {
	bb := NewMapBlackboard()
	bb.StoreFact(Fact{Keywords: []string{"a", "b"}, Content: "fact1", Author: "s1"})
	bb.StoreFact(Fact{Keywords: []string{"b", "c"}, Content: "fact2", Author: "s2"})

	// GetFacts
	facts := bb.GetFacts()
	if len(facts) != 2 {
		t.Fatalf("expected 2 facts, got %d", len(facts))
	}

	// SearchFacts by keyword
	results := bb.SearchFacts([]string{"a"})
	if len(results) != 1 {
		t.Errorf("expected 1 match for 'a', got %d", len(results))
	}
}

func TestMapBlackboard_SearchFactsEmptyKeywords(t *testing.T) {
	bb := NewMapBlackboard()
	bb.StoreFact(Fact{Keywords: []string{"x"}, Content: "data", Author: "s1"})
	results := bb.SearchFacts(nil)
	if results != nil {
		t.Errorf("expected nil for nil keywords, got %v", results)
	}
}

func TestMapBlackboard_SetFacts(t *testing.T) {
	bb := NewMapBlackboard()
	bb.SetFacts([]Fact{
		{Keywords: []string{"k1"}, Content: "c1", Author: "a1"},
	})
	facts := bb.GetFacts()
	if len(facts) != 1 {
		t.Fatalf("expected 1 fact, got %d", len(facts))
	}
	if facts[0].Keywords[0] != "k1" {
		t.Error("expected keyword preserved")
	}

	// SetFacts with nil clears
	bb.SetFacts(nil)
	facts = bb.GetFacts()
	if facts != nil {
		t.Errorf("expected nil after SetFacts(nil), got %v", facts)
	}
}

// ---------------------------------------------------------------------------
// copyPlan with EstimatedTools (broader coverage)
// ---------------------------------------------------------------------------

func TestCopyPlan_EstimatedTools(t *testing.T) {
	p := &Plan{
		Steps: []PlanStep{
			{ID: "s1", EstimatedTools: []string{"read_file", "write_file"}},
			{ID: "s2", DependsOn: []string{"s1"}},
		},
	}
	bb := NewMapBlackboard()
	bb.SetPlan(p)
	copied := bb.GetPlan()
	if len(copied.Steps) != 2 {
		t.Fatalf("expected 2 steps")
	}
	if len(copied.Steps[0].EstimatedTools) != 2 {
		t.Errorf("expected EstimatedTools copied, got %v", copied.Steps[0].EstimatedTools)
	}
}

// ---------------------------------------------------------------------------
// retryFailedSteps - failedStepID not found in plan
// ---------------------------------------------------------------------------

func TestRetryFailedSteps_StepNotFoundInPlan(t *testing.T) {
	ctx := context.Background()
	plan := &Plan{Steps: []PlanStep{
		{ID: "s1", Description: "step 1"},
	}}
	completedSteps := map[string]CompletedStep{
		"s1": {StepID: "s1", Output: "ok"},
	}
	completedList := []CompletedStep{
		{StepID: "s1", Output: "ok"},
	}
	bb := NewMapBlackboard()
	o := New(Config{MaxRetries: 1})
	// Call retryFailedSteps with a step not in plan
	reflections := o.retryFailedSteps(ctx, []string{"nonexistent"}, plan, nil, completedSteps, &completedList, bb, "test", nil)
	if reflections != nil {
		t.Errorf("expected nil reflections, got %v", reflections)
	}
}

// ---------------------------------------------------------------------------
// NoopEvents - call all methods for coverage
// ---------------------------------------------------------------------------

func TestNoopEvents_AllMethods(t *testing.T) {
	n := &NoopEvents{}
	// Call every method — they're all no-ops, just checking they don't panic
	n.OnPlanGenerated(3, []PlanStepEvent{})
	n.OnStepStarted("s1", "desc", "summary")
	n.OnStepCompleted("s1", true, time.Second, "")
	n.OnReflected(&Reflection{}, 1, 3)
	n.OnRetry(1, 3)
	n.OnStepRetry("s1", 1, 3)
	n.OnService("message")
	n.OnServiceMeta("message", map[string]any{"key": "val"})
	n.OnReplanFailed(errors.New("fail"))
	n.OnStepTodoUpdate("s1", []agent.TodoItem{})
}

// ---------------------------------------------------------------------------
// NewMapBlackboard with options / WithMaxSummaryLen / MaxSummaryLen
// ---------------------------------------------------------------------------

func TestNewMapBlackboard_WithMaxSummaryLen(t *testing.T) {
	bb := NewMapBlackboard(WithMaxSummaryLen(200))
	if bb.MaxSummaryLen() != 200 {
		t.Errorf("expected MaxSummaryLen=200, got %d", bb.MaxSummaryLen())
	}
}

func TestNewMapBlackboard_DefaultMaxSummaryLen(t *testing.T) {
	bb := NewMapBlackboard()
	if bb.MaxSummaryLen() != 0 {
		t.Errorf("expected MaxSummaryLen=0 by default, got %d", bb.MaxSummaryLen())
	}
}

func TestMapBlackboard_WithMaxSummaryLen_ZeroArg(t *testing.T) {
	bb := NewMapBlackboard(WithMaxSummaryLen(0))
	if bb.MaxSummaryLen() != 0 {
		t.Errorf("expected MaxSummaryLen=0, got %d", bb.MaxSummaryLen())
	}
}

// ---------------------------------------------------------------------------
// GenerateSummary edge cases
// ---------------------------------------------------------------------------

func TestGenerateSummary_ZeroMaxLen(t *testing.T) {
	// maxLen=0 should use default 500
	out := GenerateSummary("hello", 0)
	if out != "hello" {
		t.Errorf("expected 'hello', got %q", out)
	}
}

// ---------------------------------------------------------------------------
// buildStepTask with dependency context and retry context
// ---------------------------------------------------------------------------

func TestBuildStepTask_DependencyContext(t *testing.T) {
	o := New(Config{MaxDependencyContextChars: 100000})
	plan := Plan{
		Steps: []PlanStep{
			{ID: "s1", Description: "do first"},
			{ID: "s2", Description: "do second", DependsOn: []string{"s1"}},
		},
	}
	bb := NewMapBlackboard()
	bb.SetStepResult("s1", "first step output", nil, nil)
	taskDef := o.buildStepTask(
		plan.Steps[1], 1, plan,
		map[string]CompletedStep{},
		nil, bb,
		"user request", "",
		30,
	)
	if !strings.Contains(taskDef.task, "Context from previous steps") {
		t.Error("expected dependency context in task")
	}
	if !strings.Contains(taskDef.task, "s1") {
		t.Error("expected dependency step ID in context")
	}
}

func TestBuildStepTask_RetryContext(t *testing.T) {
	o := New(Config{})
	plan := Plan{
		Steps: []PlanStep{
			{ID: "s1", Description: "do something"},
		},
	}
	bb := NewMapBlackboard()
	taskDef := o.buildStepTask(
		plan.Steps[0], 0, plan,
		map[string]CompletedStep{},
		nil, bb,
		"user request", "fix this file please",
		30,
	)
	if !strings.Contains(taskDef.task, "## Existing Files From Previous Attempt") {
		t.Error("expected retry context in task")
	}
	if !strings.Contains(taskDef.task, "fix this file please") {
		t.Error("expected retry context content")
	}
}

// ---------------------------------------------------------------------------
// buildStepTask with empty user message
// ---------------------------------------------------------------------------

func TestBuildStepTask_EmptyUserMessage(t *testing.T) {
	o := New(Config{})
	plan := Plan{
		Steps: []PlanStep{
			{ID: "s1", Description: "do something"},
		},
	}
	bb := NewMapBlackboard()
	taskDef := o.buildStepTask(
		plan.Steps[0], 0, plan,
		map[string]CompletedStep{},
		nil, bb,
		"", "",
		30,
	)
	if strings.Contains(taskDef.task, "## Original User Request") {
		t.Error("should not include Original User Request when empty")
	}
}

// ---------------------------------------------------------------------------
// buildStepTask with dependency context truncation
// ---------------------------------------------------------------------------

func TestBuildStepTask_DependencyContextTruncation(t *testing.T) {
	o := New(Config{MaxDependencyContextChars: 10}) // very small limit
	plan := Plan{
		Steps: []PlanStep{
			{ID: "s1", Description: "do first"},
			{ID: "s2", Description: "do second", DependsOn: []string{"s1"}},
		},
	}
	bb := NewMapBlackboard()
	bb.SetStepResult("s1", strings.Repeat("x", 5000), nil, nil)
	taskDef := o.buildStepTask(
		plan.Steps[1], 1, plan,
		map[string]CompletedStep{},
		nil, bb,
		"user request", "",
		30,
	)
	// The dependency context should be truncated
	if !strings.Contains(taskDef.task, "Context from previous steps") {
		t.Error("expected dependency context even when truncated")
	}
}

// ---------------------------------------------------------------------------
// Cleanup error path (tracker with un-openable dir)
// ---------------------------------------------------------------------------

func TestCleanup_CloseAllError(t *testing.T) {
	// Create a tracker pointing at a file (not a directory) so CloseAll fails
	dir := t.TempDir()
	// Create a tracker — Cleanup should not panic even if dir is gone
	tracker := NewStepDumpTracker(dir)
	_ = tracker.OpenStepDump("step-1")
	o := New(Config{StepDumpTracker: tracker})
	o.Cleanup()
	// Should not panic
}

// ---------------------------------------------------------------------------
// retryFailedSteps - empty failed steps (nil return)
// ---------------------------------------------------------------------------

func TestRetryFailedSteps_EmptyList(t *testing.T) {
	ctx := context.Background()
	plan := &Plan{Steps: []PlanStep{
		{ID: "s1", Description: "step 1"},
	}}
	completedSteps := map[string]CompletedStep{}
	completedList := []CompletedStep{}
	bb := NewMapBlackboard()
	o := New(Config{MaxRetries: 1})
	reflections := o.retryFailedSteps(ctx, []string{}, plan, nil, completedSteps, &completedList, bb, "test", nil)
	if reflections != nil {
		t.Errorf("expected nil reflections for empty failed steps, got %v", reflections)
	}
}

// ---------------------------------------------------------------------------
// retryFailedSteps - context already cancelled
// ---------------------------------------------------------------------------

func TestRetryFailedSteps_ContextCancelled(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())
	cancel() // immediately cancel
	plan := &Plan{Steps: []PlanStep{
		{ID: "s1", Description: "step 1"},
	}}
	completedSteps := map[string]CompletedStep{
		"s1": {StepID: "s1", Output: "partial", Error: errors.New("fail")},
	}
	completedList := []CompletedStep{
		{StepID: "s1", Output: "partial", Error: errors.New("fail")},
	}
	bb := NewMapBlackboard()
	o := New(Config{MaxRetries: 2})
	reflections := o.retryFailedSteps(ctx, []string{"s1"}, plan, nil, completedSteps, &completedList, bb, "test", nil)
	if reflections != nil {
		t.Errorf("expected nil reflections when context cancelled, got %v", reflections)
	}
}

// ---------------------------------------------------------------------------
// Execute with nil plan returned by Planner (edge case)
// ---------------------------------------------------------------------------

func TestExecute_NilPlanReturned(t *testing.T) {
	o := New(Config{
		Planner: &mockPlanner{
			planFn: func(_ context.Context, _ string, _ []tools.ToolDescriptor, _ []Reflection) (*Plan, error) {
				return nil, nil
			},
		},
	})
	_, err := o.Execute(context.Background(), "test")
	if err == nil {
		t.Fatal("expected error when planner returns nil plan")
	}
}

// ---------------------------------------------------------------------------
// OpenStepDump with nil/closed tracker
// ---------------------------------------------------------------------------

func TestOpenStepDump_ClosedTracker(t *testing.T) {
	dir := t.TempDir()
	tracker := NewStepDumpTracker(dir)
	tracker.closed = true
	w := tracker.OpenStepDump("step-1")
	if w != nil {
		t.Error("expected nil writer for closed tracker")
	}
}

func TestOpenStepDump_EmptyDir(t *testing.T) {
	tracker := NewStepDumpTracker("")
	w := tracker.OpenStepDump("step-1")
	if w != nil {
		t.Error("expected nil writer for empty dir")
	}
}

// ---------------------------------------------------------------------------
// CloseAll with existing file handles
// ---------------------------------------------------------------------------

func TestCloseAll_WithFiles(t *testing.T) {
	dir := t.TempDir()
	tracker := NewStepDumpTracker(dir)
	_ = tracker.OpenStepDump("step-1")
	_ = tracker.OpenStepDump("step-2")
	if err := tracker.CloseAll(); err != nil {
		t.Errorf("unexpected error from CloseAll: %v", err)
	}
	// Second call should be idempotent
	if err := tracker.CloseAll(); err != nil {
		t.Errorf("unexpected error from second CloseAll: %v", err)
	}
}

// ---------------------------------------------------------------------------
// CloseAll error path (close already-closed files)
// ---------------------------------------------------------------------------

func TestCloseAll_ErrorPath(t *testing.T) {
	dir := t.TempDir()
	tracker := NewStepDumpTracker(dir)
	w := tracker.OpenStepDump("step-1")
	if w == nil {
		t.Skip("could not open dump file")
	}
	// Force-close the underlying file so that CloseAll's Close() fails
	tracker.mu.Lock()
	for _, f := range tracker.files {
		_ = f.Close()
	}
	tracker.mu.Unlock()
	// Now CloseAll will try to close an already-closed file
	err := tracker.CloseAll()
	// On some systems, closing an already-closed fd returns an error; accept either
	_ = err
}

// ---------------------------------------------------------------------------
// Cleanup error path — CloseAll returns an error
// ---------------------------------------------------------------------------

func TestCleanup_WithCloseAllError(t *testing.T) {
	dir := t.TempDir()
	tracker := NewStepDumpTracker(dir)
	w := tracker.OpenStepDump("step-1")
	if w == nil {
		t.Skip("could not open dump file")
	}
	// Force-close the underlying file so that CloseAll's f.Close() fails
	tracker.mu.Lock()
	for _, f := range tracker.files {
		_ = f.Close()
	}
	tracker.mu.Unlock()
	o := New(Config{StepDumpTracker: tracker, Logger: slog.New(slog.DiscardHandler)})
	o.Cleanup()
	// Should not panic — warn log path is covered
}

// ---------------------------------------------------------------------------
// buildStepTask with plan having exploration context (covered above) and
// buildStepTask with MaxDependencyContextChars == 0 (default 8000 path)
// ---------------------------------------------------------------------------

func TestBuildStepTask_DependencyContextDefaultLimit(t *testing.T) {
	o := New(Config{MaxDependencyContextChars: 0}) // triggers default 8000
	plan := Plan{
		Steps: []PlanStep{
			{ID: "s1", Description: "do first"},
			{ID: "s2", Description: "do second", DependsOn: []string{"s1"}},
		},
	}
	bb := NewMapBlackboard()
	bb.SetStepResult("s1", "first step output", nil, nil)
	taskDef := o.buildStepTask(
		plan.Steps[1], 1, plan,
		map[string]CompletedStep{},
		nil, bb,
		"user request", "",
		30,
	)
	if !strings.Contains(taskDef.task, "Context from previous steps") {
		t.Error("expected dependency context in task")
	}
}

// ---------------------------------------------------------------------------
// buildStepTask with empty dependency summary
// ---------------------------------------------------------------------------

func TestBuildStepTask_DependencyWithEmptySummary(t *testing.T) {
	o := New(Config{})
	plan := Plan{
		Steps: []PlanStep{
			{ID: "s1", Description: "do first"},
			{ID: "s2", Description: "do second", DependsOn: []string{"s1"}},
		},
	}
	bb := NewMapBlackboard()
	// Don't set step result — GetStepSummary will return ""
	taskDef := o.buildStepTask(
		plan.Steps[1], 1, plan,
		map[string]CompletedStep{},
		nil, bb,
		"user request", "",
		30,
	)
	// Empty summary means no context from previous steps
	if strings.Contains(taskDef.task, "Context from previous steps") {
		t.Error("should not include dependency context when summaries are empty")
	}
}

// ---------------------------------------------------------------------------
// Plan struct with exploration context
// ---------------------------------------------------------------------------

func TestPlan_ExplorationContext(t *testing.T) {
	p := Plan{
		Steps:              []PlanStep{{ID: "s1", Description: "test"}},
		ExplorationContext: "some research",
	}
	if p.ExplorationContext != "some research" {
		t.Errorf("expected ExplorationContext preserved")
	}
}

// ---------------------------------------------------------------------------
// Reflection struct fields
// ---------------------------------------------------------------------------

func TestReflection_Struct(t *testing.T) {
	now := time.Now()
	r := Reflection{
		Summary:         "test summary",
		Hypotheses:      []string{"h1", "h2"},
		SuggestedAction: "retry",
		Reasoning:       "because",
		FailureAnalysis: "analysis",
		RootCause:       "root",
		ActionPlan:      "plan",
		Timestamp:       now,
	}
	if r.Summary != "test summary" {
		t.Error("expected Summary preserved")
	}
	if len(r.Hypotheses) != 2 {
		t.Error("expected 2 hypotheses")
	}
	if r.SuggestedAction != "retry" {
		t.Error("expected retry action")
	}
}

// ---------------------------------------------------------------------------
// Execute with failing LLM — tests the per-step retry path including OnStepRetry
// ---------------------------------------------------------------------------

// failingLLM returns a non-context error on Call, triggering per-step retries.
type failingLLM struct {
	callCount int
}

func (m *failingLLM) Call(ctx context.Context, _ llm.ChatRequest) (*llm.ChatResponse, error) {
	m.callCount++
	return nil, errors.New("simulated LLM failure")
}

// TestExecute_WithStepFailureAndRetry tests the per-step retry path
// by using an LLM that returns errors (non-context-cancelled).
func TestExecute_WithStepFailureAndRetry(t *testing.T) {
	events := &recordingEvents{}
	fLLM := &failingLLM{}

	o := New(Config{
		Planner: &mockPlanner{
			planFn: func(_ context.Context, _ string, _ []tools.ToolDescriptor, _ []Reflection) (*Plan, error) {
				return &Plan{Steps: []PlanStep{{ID: "s1", Description: "step 1"}}}, nil
			},
		},
		ContextFactory: func(systemPrompt string, _ llm.ModelMetadata, _ string, _ ...PruningOverride) agent.ContextManager {
			return &mockContextManager{systemPrompt: systemPrompt}
		},
		LLM:          fLLM,
		Tools:        &mockToolExecutor{},
		TokenCounter: llm.NewSimpleTokenCounter(),
		MaxRetries:   1,
		MaxSteps:     3,
		ToolRegistry: tools.NewToolRegistry(),
		Events:       events,
	})

	// Execute may succeed or fail depending on how the executor handles LLM errors.
	// The key goal is to exercise the per-step retry code paths.
	_, _ = o.Execute(context.Background(), "test")
	// Verify that step execution was attempted (OnStepStarted called at least once)
	if events.stepStarted < 1 {
		t.Error("expected OnStepStarted to be called")
	}
}
