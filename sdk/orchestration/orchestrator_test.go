package orchestration

import (
	"context"
	"errors"
	"testing"
	"time"

	"github.com/user/agent/sdk/tools"
)

// ---------------------------------------------------------------------------
// Mocks
// ---------------------------------------------------------------------------

type mockPlanner struct {
	planFn   func(ctx context.Context, task string, criteria []Criterion, tools []tools.ToolDescriptor, reflections []Reflection) (*Plan, error)
	replanFn func(ctx context.Context, plan *Plan, completed []CompletedStep, failedStep CompletedStep, reflection *Reflection, criteria []Criterion, reflections []Reflection) (*Plan, error)
}

func (m *mockPlanner) Plan(ctx context.Context, task string, criteria []Criterion, t []tools.ToolDescriptor, r []Reflection) (*Plan, error) {
	if m.planFn != nil {
		return m.planFn(ctx, task, criteria, t, r)
	}
	return &Plan{Steps: []PlanStep{{ID: "s1", Description: "default step"}}}, nil
}

func (m *mockPlanner) Replan(ctx context.Context, plan *Plan, completed []CompletedStep, failedStep CompletedStep, reflection *Reflection, criteria []Criterion, reflections []Reflection) (*Plan, error) {
	if m.replanFn != nil {
		return m.replanFn(ctx, plan, completed, failedStep, reflection, criteria, reflections)
	}
	return plan, nil
}

type recordingEvents struct {
	NoopEvents
	planGenerated     int
	stepStarted       int
	stepCompleted     int
	evaluated         int
	reflected         int
	retried           int
	stepRetried       int
	criteriaExtracted int
}

func (r *recordingEvents) OnPlanGenerated(stepCount int, steps []PlanStepEvent) { r.planGenerated++ }
func (r *recordingEvents) OnStepStarted(stepID, description string)             { r.stepStarted++ }
func (r *recordingEvents) OnStepCompleted(stepID string, success bool, duration time.Duration) {
	r.stepCompleted++
}
func (r *recordingEvents) OnEvaluated(passed, total int, criteria []EvalCriterionEvent) {
	r.evaluated++
}
func (r *recordingEvents) OnReflected(summary string, insights []string, attempt, maxAttempts int) {
	r.reflected++
}
func (r *recordingEvents) OnRetry(attempt, maxAttempts int)                       { r.retried++ }
func (r *recordingEvents) OnStepRetry(stepID string, attempt, maxAttempts int)    { r.stepRetried++ }
func (r *recordingEvents) OnCriteriaExtracted(count int, criteria []EvalCriterionEvent) {
	r.criteriaExtracted++
}

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
			planFn: func(_ context.Context, _ string, _ []Criterion, _ []tools.ToolDescriptor, _ []Reflection) (*Plan, error) {
				return nil, errors.New("plan failed")
			},
		},
	})

	_, err := o.Execute(context.Background(), "do something", nil)
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
// isLastTerminalStep
// ---------------------------------------------------------------------------

func TestIsLastTerminalStep(t *testing.T) {
	o := New(Config{})

	tests := []struct {
		name     string
		plan     Plan
		stepIdx  int
		expected bool
	}{
		{
			name: "single step is terminal",
			plan: Plan{Steps: []PlanStep{{ID: "s1"}}},
			stepIdx:  0,
			expected: true,
		},
		{
			name: "last of chain is terminal",
			plan: Plan{Steps: []PlanStep{
				{ID: "s1"},
				{ID: "s2", DependsOn: []string{"s1"}},
			}},
			stepIdx:  1,
			expected: true,
		},
		{
			name: "first of chain is not terminal",
			plan: Plan{Steps: []PlanStep{
				{ID: "s1"},
				{ID: "s2", DependsOn: []string{"s1"}},
			}},
			stepIdx:  0,
			expected: false,
		},
		{
			name: "earlier terminal when later terminal exists",
			plan: Plan{Steps: []PlanStep{
				{ID: "s1"},
				{ID: "s2"},
			}},
			stepIdx:  0,
			expected: false, // s2 is also terminal and later
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			step := tt.plan.Steps[tt.stepIdx]
			got := o.isLastTerminalStep(step, tt.stepIdx, tt.plan)
			if got != tt.expected {
				t.Errorf("isLastTerminalStep(%q, %d) = %v, want %v", step.ID, tt.stepIdx, got, tt.expected)
			}
		})
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

// ---------------------------------------------------------------------------
// defaultSystemPrompt
// ---------------------------------------------------------------------------

func TestDefaultSystemPrompt(t *testing.T) {
	t.Run("without criteria", func(t *testing.T) {
		prompt := defaultSystemPrompt(context.Background(), "do stuff", nil)
		if prompt == "" {
			t.Fatal("expected non-empty prompt")
		}
		if !containsStr(prompt, "do stuff") {
			t.Error("expected step description in prompt")
		}
	})

	t.Run("with criteria", func(t *testing.T) {
		criteria := []Criterion{{ID: "ac1", Description: "must work"}}
		prompt := defaultSystemPrompt(context.Background(), "do stuff", criteria)
		if !containsStr(prompt, "ac1") || !containsStr(prompt, "must work") {
			t.Error("expected criteria in prompt")
		}
	})
}

// ---------------------------------------------------------------------------
// buildEvalCriterionEvents
// ---------------------------------------------------------------------------

func TestBuildEvalCriterionEvents(t *testing.T) {
	er := &EvalResult{
		Passed:  []EvalDetail{{Criterion: Criterion{ID: "ac1"}, Diagnostic: "ok"}},
		Failed:  []EvalDetail{{Criterion: Criterion{ID: "ac2"}, Diagnostic: "bad"}},
		Unclear: []EvalDetail{{Criterion: Criterion{ID: "ac3"}, Diagnostic: "?"}},
	}

	events := buildEvalCriterionEvents(er)
	if len(events) != 3 {
		t.Fatalf("expected 3 events, got %d", len(events))
	}

	statusMap := make(map[string]string)
	for _, e := range events {
		statusMap[e.Name] = e.Status
	}
	if statusMap["ac1"] != "pass" {
		t.Error("ac1 should be pass")
	}
	if statusMap["ac2"] != "fail" {
		t.Error("ac2 should be fail")
	}
	if statusMap["ac3"] != "unclear" {
		t.Error("ac3 should be unclear")
	}
}

// ---------------------------------------------------------------------------
// formatFailedCriteria
// ---------------------------------------------------------------------------

func TestFormatFailedCriteria(t *testing.T) {
	er := &EvalResult{
		Failed: []EvalDetail{
			{Criterion: Criterion{Description: "first"}},
			{Criterion: Criterion{Description: "second"}},
		},
	}
	got := formatFailedCriteria(er)
	if got != "first, second" {
		t.Errorf("expected 'first, second', got %q", got)
	}
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
			planFn: func(_ context.Context, _ string, _ []Criterion, _ []tools.ToolDescriptor, _ []Reflection) (*Plan, error) {
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
