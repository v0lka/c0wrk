package orchestration

import (
	"errors"
	"testing"

	"github.com/v0lka/sp4rk/agent"
)

// ---------------------------------------------------------------------------
// FindReadySteps
// ---------------------------------------------------------------------------

func TestFindReadySteps(t *testing.T) {
	tests := []struct {
		name      string
		plan      *Plan
		completed map[string]CompletedStep
		wantIDs   []string
	}{
		{
			name:      "empty plan",
			plan:      &Plan{Steps: []PlanStep{}},
			completed: map[string]CompletedStep{},
			wantIDs:   []string{},
		},
		{
			name: "single step no deps",
			plan: &Plan{Steps: []PlanStep{
				{ID: "s1", Description: "step 1"},
			}},
			completed: map[string]CompletedStep{},
			wantIDs:   []string{"s1"},
		},
		{
			name: "step already completed is skipped",
			plan: &Plan{Steps: []PlanStep{
				{ID: "s1", Description: "step 1"},
			}},
			completed: map[string]CompletedStep{
				"s1": {StepID: "s1", Output: "done"},
			},
			wantIDs: []string{},
		},
		{
			name: "dependency not yet completed blocks step",
			plan: &Plan{Steps: []PlanStep{
				{ID: "s1", Description: "step 1"},
				{ID: "s2", Description: "step 2", DependsOn: []string{"s1"}},
			}},
			completed: map[string]CompletedStep{},
			wantIDs:   []string{"s1"},
		},
		{
			name: "dependency completed unlocks step",
			plan: &Plan{Steps: []PlanStep{
				{ID: "s1", Description: "step 1"},
				{ID: "s2", Description: "step 2", DependsOn: []string{"s1"}},
			}},
			completed: map[string]CompletedStep{
				"s1": {StepID: "s1", Output: "done"},
			},
			wantIDs: []string{"s2"},
		},
		{
			name: "failed dependency blocks downstream",
			plan: &Plan{Steps: []PlanStep{
				{ID: "s1", Description: "step 1"},
				{ID: "s2", Description: "step 2", DependsOn: []string{"s1"}},
			}},
			completed: map[string]CompletedStep{
				"s1": {StepID: "s1", Error: errors.New("fail")},
			},
			wantIDs: []string{},
		},
		{
			name: "diamond DAG - multiple ready",
			plan: &Plan{Steps: []PlanStep{
				{ID: "s1", Description: "root"},
				{ID: "s2", Description: "left", DependsOn: []string{"s1"}},
				{ID: "s3", Description: "right", DependsOn: []string{"s1"}},
				{ID: "s4", Description: "merge", DependsOn: []string{"s2", "s3"}},
			}},
			completed: map[string]CompletedStep{
				"s1": {StepID: "s1", Output: "done"},
			},
			wantIDs: []string{"s2", "s3"},
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got := FindReadySteps(tt.plan, tt.completed)
			gotIDs := make(map[string]bool)
			for _, s := range got {
				gotIDs[s.ID] = true
			}
			if len(got) != len(tt.wantIDs) {
				t.Fatalf("got %d ready steps, want %d", len(got), len(tt.wantIDs))
			}
			for _, id := range tt.wantIDs {
				if !gotIDs[id] {
					t.Errorf("expected step %q to be ready", id)
				}
			}
		})
	}
}

// ---------------------------------------------------------------------------
// BuildCarryForward
// ---------------------------------------------------------------------------

func TestBuildCarryForward(t *testing.T) {
	tests := []struct {
		name      string
		completed []CompletedStep
		newPlan   *Plan
		wantNil   bool
		wantIDs   map[string]bool
	}{
		{
			name:      "no completed steps",
			completed: nil,
			newPlan:   &Plan{Steps: []PlanStep{{ID: "s1"}}},
			wantNil:   true,
		},
		{
			name: "completed step matches new plan",
			completed: []CompletedStep{
				{StepID: "s1", Output: "done"},
			},
			newPlan: &Plan{Steps: []PlanStep{{ID: "s1"}}},
			wantIDs: map[string]bool{"s1": true},
		},
		{
			name: "completed step not in new plan is dropped",
			completed: []CompletedStep{
				{StepID: "old_step", Output: "done"},
			},
			newPlan: &Plan{Steps: []PlanStep{{ID: "s1"}}},
			wantNil: true,
		},
		{
			name: "failed completed step is not carried",
			completed: []CompletedStep{
				{StepID: "s1", Error: errors.New("fail")},
			},
			newPlan: &Plan{Steps: []PlanStep{{ID: "s1"}}},
			wantNil: true,
		},
		{
			name: "transitive invalidation - dep not carried removes dependent",
			completed: []CompletedStep{
				{StepID: "s1", Output: "done"},
				{StepID: "s2", Output: "done"},
			},
			newPlan: &Plan{Steps: []PlanStep{
				{ID: "s1"},
				{ID: "s2", DependsOn: []string{"s1"}},
				{ID: "s3", DependsOn: []string{"s2"}}, // s3 is new, not in completed
			}},
			// s1 carried, s2 depends on s1 (carried) so s2 carried, s3 not in completed
			wantIDs: map[string]bool{"s1": true, "s2": true},
		},
		{
			name: "dep re-planned invalidates downstream",
			completed: []CompletedStep{
				{StepID: "s2", Output: "done"},
			},
			newPlan: &Plan{Steps: []PlanStep{
				{ID: "s1"},                            // new step, not in completed
				{ID: "s2", DependsOn: []string{"s1"}}, // s2 depends on s1 which is not carried
			}},
			// s1 not carried → s2 depends on s1 → s2 removed
			wantNil: true,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got := BuildCarryForward(tt.completed, tt.newPlan)
			if tt.wantNil {
				if got != nil {
					t.Fatalf("expected nil, got %v", got)
				}
				return
			}
			if got == nil {
				t.Fatal("expected non-nil result")
			}
			if len(got) != len(tt.wantIDs) {
				t.Fatalf("got %d carried, want %d", len(got), len(tt.wantIDs))
			}
			for id := range tt.wantIDs {
				if _, ok := got[id]; !ok {
					t.Errorf("expected %q to be carried forward", id)
				}
			}
		})
	}
}

// ---------------------------------------------------------------------------
// BuildPlanExecutionSteps
// ---------------------------------------------------------------------------

func TestBuildPlanExecutionSteps(t *testing.T) {
	plan := &Plan{Steps: []PlanStep{
		{ID: "s1", Description: "do X"},
		{ID: "s2", Description: "do Y"},
	}}

	t.Run("uses actual executor steps when available", func(t *testing.T) {
		completed := []CompletedStep{
			{StepID: "s1", Steps: []agent.Step{
				{Thought: "thinking", Observation: "result"},
			}},
		}
		got := BuildPlanExecutionSteps(completed, plan)
		if len(got) != 1 {
			t.Fatalf("expected 1 step, got %d", len(got))
		}
		if got[0].Thought != "thinking" {
			t.Error("expected actual executor step thought")
		}
	})

	t.Run("summary fallback when no steps", func(t *testing.T) {
		completed := []CompletedStep{
			{StepID: "s1", Output: "output text"},
		}
		got := BuildPlanExecutionSteps(completed, plan)
		if len(got) != 1 {
			t.Fatalf("expected 1 step, got %d", len(got))
		}
		if got[0].Observation != "output text" {
			t.Errorf("expected observation to be 'output text', got %q", got[0].Observation)
		}
	})

	t.Run("summary fallback with error", func(t *testing.T) {
		completed := []CompletedStep{
			{StepID: "s1", Output: "partial", Error: errors.New("boom")},
		}
		got := BuildPlanExecutionSteps(completed, plan)
		if len(got) != 1 {
			t.Fatalf("expected 1 step, got %d", len(got))
		}
		if got[0].Observation == "" {
			t.Error("expected non-empty observation for failed step")
		}
	})

	t.Run("empty completed list", func(t *testing.T) {
		got := BuildPlanExecutionSteps(nil, plan)
		if len(got) != 0 {
			t.Errorf("expected 0 steps, got %d", len(got))
		}
	})
}

// ---------------------------------------------------------------------------
// AggregateOutput
// ---------------------------------------------------------------------------

func TestAggregateOutput(t *testing.T) {
	t.Run("returns terminal step outputs", func(t *testing.T) {
		plan := &Plan{Steps: []PlanStep{
			{ID: "s1", Description: "root"},
			{ID: "s2", Description: "leaf", DependsOn: []string{"s1"}},
		}}
		completed := map[string]CompletedStep{
			"s1": {StepID: "s1", Output: "root output"},
			"s2": {StepID: "s2", Output: "leaf output"},
		}
		got := AggregateOutput(completed, plan, nil)
		// s1 is depended upon by s2, so only s2 (terminal) should appear
		if !contains(got, "leaf output") {
			t.Error("expected terminal step output")
		}
		if contains(got, "root output") {
			t.Error("non-terminal step output should not appear")
		}
	})

	t.Run("falls back to all outputs when no terminal outputs", func(t *testing.T) {
		plan := &Plan{Steps: []PlanStep{
			{ID: "s1", Description: "only step", DependsOn: []string{"s0"}},
		}}
		// s0 depended upon by s1, but s0 is not in plan steps — s1 is terminal
		// Actually s1 depends on s0, and no step depends on s1, so s1 IS terminal.
		// Let's test the fallback by having a failed terminal step:
		completed := map[string]CompletedStep{
			"s1": {StepID: "s1", Error: errors.New("fail")},
		}
		got := AggregateOutput(completed, plan, nil)
		// s1 has error, so it's skipped in terminal collection → fallback also skips it
		if got != "" {
			t.Errorf("expected empty output for failed step, got %q", got)
		}
	})

	t.Run("single step plan", func(t *testing.T) {
		plan := &Plan{Steps: []PlanStep{
			{ID: "s1", Description: "only step"},
		}}
		completed := map[string]CompletedStep{
			"s1": {StepID: "s1", Output: "the output"},
		}
		got := AggregateOutput(completed, plan, nil)
		if got != "the output" {
			t.Errorf("expected 'the output', got %q", got)
		}
	})

	t.Run("empty plan", func(t *testing.T) {
		got := AggregateOutput(map[string]CompletedStep{}, &Plan{}, nil)
		if got != "" {
			t.Errorf("expected empty output, got %q", got)
		}
	})

	t.Run("multiple terminal steps", func(t *testing.T) {
		plan := &Plan{Steps: []PlanStep{
			{ID: "s1", Description: "root"},
			{ID: "s2", Description: "leaf A", DependsOn: []string{"s1"}},
			{ID: "s3", Description: "leaf B", DependsOn: []string{"s1"}},
		}}
		completed := map[string]CompletedStep{
			"s1": {StepID: "s1", Output: "root out"},
			"s2": {StepID: "s2", Output: "A out"},
			"s3": {StepID: "s3", Output: "B out"},
		}
		got := AggregateOutput(completed, plan, nil)
		if !contains(got, "A out") || !contains(got, "B out") {
			t.Errorf("expected both terminal outputs, got %q", got)
		}
		if contains(got, "root out") {
			t.Error("non-terminal output should not appear")
		}
	})

	t.Run("pre-completed terminal steps are excluded", func(t *testing.T) {
		// Simulate continuation: old plan had s1→s2 (both terminal-looking after merge),
		// new continuation adds s3. s1 and s2 are pre-completed from previous turn.
		plan := &Plan{Steps: []PlanStep{
			{ID: "s1", Description: "old root"},
			{ID: "s2", Description: "old leaf", DependsOn: []string{"s1"}},
			{ID: "s3", Description: "new continuation step"},
		}}
		completed := map[string]CompletedStep{
			"s1": {StepID: "s1", Output: "old root output"},
			"s2": {StepID: "s2", Output: "old leaf output"},
			"s3": {StepID: "s3", Output: "new output"},
		}
		preCompletedIDs := map[string]bool{"s1": true, "s2": true}
		got := AggregateOutput(completed, plan, preCompletedIDs)
		if !contains(got, "new output") {
			t.Errorf("expected new continuation output, got %q", got)
		}
		if contains(got, "old root output") || contains(got, "old leaf output") {
			t.Errorf("pre-completed step output should be excluded, got %q", got)
		}
	})

	t.Run("nil preCompletedIDs includes all terminal outputs", func(t *testing.T) {
		plan := &Plan{Steps: []PlanStep{
			{ID: "s1", Description: "root"},
			{ID: "s2", Description: "leaf", DependsOn: []string{"s1"}},
		}}
		completed := map[string]CompletedStep{
			"s1": {StepID: "s1", Output: "root output"},
			"s2": {StepID: "s2", Output: "leaf output"},
		}
		got := AggregateOutput(completed, plan, nil)
		if !contains(got, "leaf output") {
			t.Error("expected terminal output with nil preCompletedIDs")
		}
	})

	t.Run("all terminals pre-completed falls back to empty", func(t *testing.T) {
		plan := &Plan{Steps: []PlanStep{
			{ID: "s1", Description: "only step"},
		}}
		completed := map[string]CompletedStep{
			"s1": {StepID: "s1", Output: "old output"},
		}
		preCompletedIDs := map[string]bool{"s1": true}
		got := AggregateOutput(completed, plan, preCompletedIDs)
		if got != "" {
			t.Errorf("expected empty output when all terminals are pre-completed, got %q", got)
		}
	})
}

// helper
func contains(s, substr string) bool {
	return len(s) >= len(substr) && s != "" && substr != "" && containsStr(s, substr)
}

func containsStr(s, substr string) bool {
	for i := 0; i <= len(s)-len(substr); i++ {
		if s[i:i+len(substr)] == substr {
			return true
		}
	}
	return false
}
