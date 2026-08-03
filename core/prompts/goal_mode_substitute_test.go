package prompts

import "testing"

func TestGoalModeSubstitute_ResolvesAllPlaceholders(t *testing.T) {
	const (
		condition  = "all tests pass"
		verify     = "go test ./..."
		budgetLine = "max 5 turns"
	)
	tmpl := "{goal_condition} then {goal_verify_clause} with {goal_budget_line}"
	want := "all tests pass then go test ./... with max 5 turns"

	got := GoalModeSubstitute(tmpl, condition, verify, budgetLine)
	if got != want {
		t.Errorf("GoalModeSubstitute = %q, want %q", got, want)
	}
}

func TestGoalModeSubstitute_PassesThroughUnchanged(t *testing.T) {
	const unchanged = "plain text with no placeholders"
	got := GoalModeSubstitute(unchanged, "c", "v", "b")
	if got != unchanged {
		t.Errorf("expected unchanged text, got %q", got)
	}
}

func TestGoalModeSubstitute_EmptyReplacements(t *testing.T) {
	tmpl := "[{goal_condition}][{goal_verify_clause}][{goal_budget_line}]"
	want := "[][][]"
	got := GoalModeSubstitute(tmpl, "", "", "")
	if got != want {
		t.Errorf("GoalModeSubstitute with empty = %q, want %q", got, want)
	}
}
