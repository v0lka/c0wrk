package core

import (
	"strings"
	"testing"

	"github.com/v0lka/sp4rk/orchestration"
)

// TestSerializePlan_HeaderIncludesStepID verifies that each step header
// carries the step ID — `# Step N (id): summary` — so the human reviewer
// sees the identifiers that DependsOn references target and empty or broken
// IDs stand out during plan approval (incident plan-f713b5 follow-up). It
// also verifies that a "Depends on" line sits directly under the header:
// the step's dependencies comma-separated, or exactly "Depends on: (none)"
// when the step has none, making the flattened plan's edges explicit.
func TestSerializePlan_HeaderIncludesStepID(t *testing.T) {
	plan := &orchestration.Plan{
		Steps: []orchestration.PlanStep{
			{
				ID:          "step_1_nudge_cache",
				Summary:     "Warm the compile cache",
				Description: "Run a no-op build so later measurements are stable.",
				DependsOn:   nil,
			},
			{
				ID:          "step_2_measure",
				Summary:     "Measure build time",
				Description: "Time a clean build and record the result.",
				DependsOn:   []string{"step_1_nudge_cache"},
			},
		},
	}

	got := SerializePlan(plan)

	want := strings.Join([]string{
		"# Step 1 (step_1_nudge_cache): Warm the compile cache",
		"",
		"Depends on: (none)",
		"",
		"Run a no-op build so later measurements are stable.",
		"",
		"# Step 2 (step_2_measure): Measure build time",
		"",
		"Depends on: step_1_nudge_cache",
		"",
		"Time a clean build and record the result.",
		"",
	}, "\n")

	if got != want {
		t.Errorf("SerializePlan() markdown mismatch:\n got: %q\nwant: %q", got, want)
	}
}

// TestSerializePlan_EmptyIDStaysVisible ensures that a step with an empty ID
// (which upstream validation is expected to reject) still renders its header
// with empty parentheses instead of silently hiding the gap from the reviewer.
func TestSerializePlan_EmptyIDStaysVisible(t *testing.T) {
	plan := &orchestration.Plan{
		Steps: []orchestration.PlanStep{
			{ID: "", Summary: "Nameless step", Description: "Description."},
		},
	}

	got := SerializePlan(plan)
	if !strings.HasPrefix(got, "# Step 1 (): Nameless step\n") {
		t.Errorf("SerializePlan() should surface an empty ID as \"# Step 1 (): ...\", got: %q", got)
	}
}

// TestSerializePlan_NilAndEmpty guards the empty-input contract of the
// serializer: no plan or a plan without steps yields no markdown.
func TestSerializePlan_NilAndEmpty(t *testing.T) {
	if got := SerializePlan(nil); got != "" {
		t.Errorf("SerializePlan(nil) = %q, want \"\"", got)
	}
	if got := SerializePlan(&orchestration.Plan{}); got != "" {
		t.Errorf("SerializePlan(empty) = %q, want \"\"", got)
	}
}
