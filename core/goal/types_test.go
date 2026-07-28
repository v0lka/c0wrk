package goal

import (
	"encoding/json"
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

func TestNormalizeVerificationMode_DefaultsEmptyToExecutable(t *testing.T) {
	got, err := NormalizeVerificationMode("")
	if err != nil {
		t.Fatalf("empty mode returned error: %v", err)
	}
	if got != VerificationModeExecutable {
		t.Errorf("empty mode = %q, want %q", got, VerificationModeExecutable)
	}
}

func TestNormalizeVerificationMode_PassThroughKnownValues(t *testing.T) {
	cases := []struct {
		name string
		mode string
	}{
		{"executable", VerificationModeExecutable},
		{"re_derivation", VerificationModeReDerivation},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			got, err := NormalizeVerificationMode(tc.mode)
			if err != nil {
				t.Fatalf("%q returned error: %v", tc.mode, err)
			}
			if got != tc.mode {
				t.Errorf("NormalizeVerificationMode(%q) = %q, want %q", tc.mode, got, tc.mode)
			}
		})
	}
}

func TestNormalizeVerificationMode_RejectsUnknownValue(t *testing.T) {
	got, err := NormalizeVerificationMode("bogus")
	if err == nil {
		t.Fatalf("expected error for unknown mode, got %q", got)
	}
	if got != "" {
		t.Errorf("unknown mode should return empty string, got %q", got)
	}
}

func TestGoalState_VerificationModeRoundTripsJSON(t *testing.T) {
	original := GoalState{
		Condition:        "done",
		VerifyClause:     "tests pass",
		VerificationMode: VerificationModeReDerivation,
		Status:           StatusActive,
	}

	data, err := json.Marshal(original)
	if err != nil {
		t.Fatalf("marshal: %v", err)
	}

	var restored GoalState
	if err := json.Unmarshal(data, &restored); err != nil {
		t.Fatalf("unmarshal: %v", err)
	}
	if restored.VerificationMode != VerificationModeReDerivation {
		t.Errorf("VerificationMode did not round-trip: got %q, want %q",
			restored.VerificationMode, VerificationModeReDerivation)
	}
}
