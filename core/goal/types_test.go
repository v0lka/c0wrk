package goal

import (
	"testing"
)

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
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			if got := tc.b.IsUnlimited(); got != tc.want {
				t.Errorf("IsUnlimited() = %v, want %v", got, tc.want)
			}
		})
	}
}
