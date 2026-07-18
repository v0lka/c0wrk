package goal

import (
	"testing"
	"time"
)

// testDeadline is a non-zero time used by budget tests.
var testDeadline = time.Date(2026, 7, 18, 12, 0, 0, 0, time.UTC)

func TestGoalStatus_IsTerminal(t *testing.T) {
	cases := []struct {
		status GoalStatus
		want   bool
	}{
		{StatusActive, false},
		{StatusPaused, false},
		{StatusBlockedIdle, false},
		{StatusMet, true},
		{StatusExhausted, true},
		{StatusCancelled, true},
	}
	for _, tc := range cases {
		if got := tc.status.IsTerminal(); got != tc.want {
			t.Errorf("%q.IsTerminal() = %v, want %v", tc.status, got, tc.want)
		}
	}
}

func TestGoalBudget_IsUnlimited(t *testing.T) {
	cases := []struct {
		name string
		b    GoalBudget
		want bool
	}{
		{"all zero", GoalBudget{}, true},
		{"only turns set", GoalBudget{MaxTurns: 5}, false},
		{"only tokens set", GoalBudget{MaxTokens: 1000}, false},
		{"only deadline set", GoalBudget{Deadline: testDeadline}, false},
		{"all set", GoalBudget{MaxTurns: 5, MaxTokens: 1000, Deadline: testDeadline}, false},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			if got := tc.b.IsUnlimited(); got != tc.want {
				t.Errorf("IsUnlimited() = %v, want %v", got, tc.want)
			}
		})
	}
}
