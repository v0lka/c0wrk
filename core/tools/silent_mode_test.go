package tools

import (
	"context"
	"encoding/json"
	"strings"
	"testing"
)

// TestClone_CopiesAutonomyAndSilentMode verifies Clone carries the autonomy
// mode and the silent-mode sub-policies to the per-session registry and keeps
// them independent of later parent mutations (the Clone contract the
// security-settings push relies on).
func TestClone_CopiesAutonomyAndSilentMode(t *testing.T) {
	parent := NewToolRegistry()
	initial := SilentModeState{
		ToolConfirm: "deny",
		StepLimit:   "stop",
		AskUser:     "disable",
	}
	parent.ApplySecurityState(nil, false, AutonomyModeSilent, initial)

	child := parent.Clone()
	if got := child.SilentMode(); got != initial {
		t.Fatalf("cloned silent mode = %+v, want %+v", got, initial)
	}
	if got := child.AutonomyMode(); got != AutonomyModeSilent {
		t.Fatalf("cloned autonomy mode = %q, want %q", got, AutonomyModeSilent)
	}

	// A later push to the parent must not leak into the child: the clone keeps
	// its own copied posture (independence is what makes per-session pushes
	// safe).
	parent.ApplySecurityState(nil, false, AutonomyModeStandard, SilentModeState{})
	if got := child.AutonomyMode(); got != AutonomyModeSilent {
		t.Error("child autonomy mode must be independent of the parent after a later parent push")
	}
	if got := parent.AutonomyMode(); got != AutonomyModeStandard {
		t.Error("parent autonomy mode must reflect its own push")
	}
}

// TestRegisterBuiltinTools_SilentModeDisablesAskUser verifies the build-time
// behavior: without an AskUserFunc the tool is not registered at all; with one,
// it is registered unless the silent autonomy mode's ask_user sub-policy
// disables it — in which case it is registered with a nil callback so a call
// resolves to the explicit "not available" result (never blocking the agent)
// rather than a missing-tool error.
func TestRegisterBuiltinTools_SilentModeDisablesAskUser(t *testing.T) {
	askFn := func(ctx context.Context, req AskUserRequest) (AskUserResponse, error) {
		return AskUserResponse{}, nil
	}
	askInput, err := json.Marshal(map[string]any{
		"questions": []map[string]any{
			{"id": "q1", "question": "Proceed?", "options": []map[string]any{{"label": "Yes", "value": "yes"}}},
		},
	})
	if err != nil {
		t.Fatalf("marshal input: %v", err)
	}

	cases := []struct {
		name         string
		autonomy     string
		askUser      string
		wantReg      bool
		wantNotAvail bool
	}{
		{"standard mode", AutonomyModeStandard, "disable", true, false},
		{"assisted mode", AutonomyModeAssisted, "disable", true, false},
		{"silent mode, ask_user enable", AutonomyModeSilent, "enable", true, false},
		{"silent mode, ask_user disable", AutonomyModeSilent, "disable", true, true},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			r := NewToolRegistry()
			// Only the silent autonomy mode plus the "disable" sub-policy
			// suppresses the ask_user callback.
			if err := RegisterBuiltinTools(r, BuiltinToolsConfig{
				AskUserFunc:  askFn,
				SilentMode:   SilentModeState{AskUser: tc.askUser},
				AutonomyMode: tc.autonomy,
			}); err != nil {
				t.Fatalf("RegisterBuiltinTools: %v", err)
			}
			tool, ok := r.Get("ask_user")
			if ok != tc.wantReg {
				t.Fatalf("ask_user registered = %v, want %v", ok, tc.wantReg)
			}
			if !tc.wantReg {
				return
			}
			res, err := tool.Execute(context.Background(), askInput)
			if err != nil {
				t.Fatalf("Execute: %v", err)
			}
			if tc.wantNotAvail {
				if !res.IsError || !strings.Contains(res.Content, "not available") {
					t.Errorf("disabled ask_user must report the not-available result, got %+v", res)
				}
			} else if res.IsError {
				t.Errorf("available ask_user must run the callback, got %+v", res)
			}
		})
	}

	// A nil AskUserFunc still means "no ask_user" regardless of the autonomy
	// mode.
	r := NewToolRegistry()
	if err := RegisterBuiltinTools(r, BuiltinToolsConfig{AutonomyMode: AutonomyModeSilent, SilentMode: SilentModeState{AskUser: "disable"}}); err != nil {
		t.Fatalf("RegisterBuiltinTools: %v", err)
	}
	if _, ok := r.Get("ask_user"); ok {
		t.Error("ask_user must not be registered without an AskUserFunc")
	}
}
