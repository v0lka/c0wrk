package tools

import (
	"errors"
	"strings"
	"testing"
)

// TestBuildDelegateToolResult_Paused verifies a paused delegation surfaces as a
// distinct "paused" section (not a failure) with a resume hint.
func TestBuildDelegateToolResult_Paused(t *testing.T) {
	res := buildDelegateToolResult([]DelegationResult{
		{ID: "del_1", Status: DelegationStatusPaused, Error: errors.New("paused")},
		{ID: "del_2", Status: DelegationStatusCompleted, Output: "ok"},
	})

	content := res.Content
	if !strings.Contains(content, "## Delegations paused") {
		t.Fatalf("expected a paused section, got:\n%s", content)
	}
	if !strings.Contains(content, "del_1") {
		t.Fatalf("expected paused delegation id in result, got:\n%s", content)
	}
	if strings.Contains(content, "del_1") && !strings.Contains(content, "Re-invoke delegate") {
		t.Fatalf("expected a resume hint for paused delegations, got:\n%s", content)
	}
}

// TestBuildExecutePlanResult_Paused verifies a paused plan step is reported as
// paused (not failed) with a resume hint.
func TestBuildExecutePlanResult_Paused(t *testing.T) {
	res := buildExecutePlanResult([]PlanStepResult{
		{StepID: "step_1", Summary: "do a", Status: "completed", Output: "a"},
		{StepID: "step_2", Summary: "do b", Status: "paused", Error: errors.New("paused")},
	})

	content := res.Content
	if !strings.Contains(content, "paused") {
		t.Fatalf("expected paused status in result, got:\n%s", content)
	}
	if !strings.Contains(content, "[step_2] do b — paused") {
		t.Fatalf("expected step_2 rendered as paused, got:\n%s", content)
	}
	if !strings.Contains(content, "Re-invoke execute_plan") {
		t.Fatalf("expected a resume hint, got:\n%s", content)
	}
}
