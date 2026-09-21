package tools

import (
	"context"
	"encoding/json"
	"strings"
	"testing"

	sdktools "github.com/v0lka/sp4rk/tools"
)

// TestClone_CopiesAutonomyAndSilentMode verifies Clone carries the autonomy
// mode and the silent-mode sub-policies to the per-session registry and keeps
// them independent of later parent mutations (the Clone contract the
// security-settings push relies on).
func TestClone_CopiesAutonomyAndSilentMode(t *testing.T) {
	parent := NewToolRegistry()
	initial := SilentModeState{
		ToolConfirm:  "deny",
		StepLimit:    "stop",
		AskUser:      "disable",
		ReviewPrompt: "suppress",
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

// TestRefreshAutonomyPosture_SyncsCloneFromParent pins the per-task autonomy
// contract at the registry level: a parent (shared registry) posture change —
// what a Settings save does — must not leak into a clone, and the clone picks
// the new posture up ONLY at an explicit refresh (the task-launch boundary
// the session manager calls).
func TestRefreshAutonomyPosture_SyncsCloneFromParent(t *testing.T) {
	parent := NewToolRegistry()
	parent.ApplySecurityState(nil, false, AutonomyModeStandard, SilentModeState{})
	child := parent.Clone()

	// Settings save: the shared registry flips to silent; the clone stays
	// pinned to the posture it is executing under.
	on := SilentModeState{ToolConfirm: "judge", StepLimit: "auto", AskUser: "disable", ReviewPrompt: "suppress"}
	parent.ApplySecurityState(nil, false, AutonomyModeSilent, on)
	if got := child.AutonomyMode(); got != AutonomyModeStandard {
		t.Fatalf("clone autonomy mode = %q, want %q (pinned until the task-launch refresh)", got, AutonomyModeStandard)
	}

	// Task launch: the refresh re-syncs mode and sub-policies as one unit.
	child.RefreshAutonomyPosture()
	if got := child.AutonomyMode(); got != AutonomyModeSilent {
		t.Fatalf("post-refresh clone autonomy mode = %q, want %q", got, AutonomyModeSilent)
	}
	if got := child.SilentMode(); got != on {
		t.Fatalf("post-refresh clone silent mode = %+v, want %+v", got, on)
	}

	// A later Settings save does not leak either; the next refresh picks it
	// up (replacement, not merge).
	parent.ApplySecurityState(nil, false, AutonomyModeAssisted, SilentModeState{})
	if got := child.AutonomyMode(); got != AutonomyModeSilent {
		t.Fatalf("clone autonomy mode = %q, want %q (still pinned after another parent push)", got, AutonomyModeSilent)
	}
	child.RefreshAutonomyPosture()
	if got := child.AutonomyMode(); got != AutonomyModeAssisted {
		t.Fatalf("post-refresh clone autonomy mode = %q, want %q", got, AutonomyModeAssisted)
	}
	if got := child.SilentMode(); got != (SilentModeState{}) {
		t.Fatalf("post-refresh clone silent mode = %+v, want the zero posture", got)
	}
}

// TestRefreshAutonomyPosture_NoParentIsNoop verifies the shared registry
// itself (no parent link) treats a refresh as a no-op instead of clearing the
// posture — the shared registry IS the authoritative source.
func TestRefreshAutonomyPosture_NoParentIsNoop(t *testing.T) {
	shared := NewToolRegistry()
	on := SilentModeState{ToolConfirm: "deny", StepLimit: "stop", AskUser: "enable", ReviewPrompt: "allow"}
	shared.ApplySecurityState(nil, false, AutonomyModeSilent, on)

	shared.RefreshAutonomyPosture()

	if got := shared.AutonomyMode(); got != AutonomyModeSilent {
		t.Errorf("shared registry autonomy mode = %q, want %q (refresh is a no-op)", got, AutonomyModeSilent)
	}
	if got := shared.SilentMode(); got != on {
		t.Errorf("shared registry silent mode = %+v, want %+v (refresh is a no-op)", got, on)
	}
}

// TestApplyGroupPolicies_PreservesAutonomyPosture pins the split-delivery
// contract of the runtime push: ApplyGroupPolicies replaces the group→policy
// map and the auto-approval flag but must leave the autonomy mode and the
// silent-mode sub-policies untouched (those change only at task launch via
// RefreshAutonomyPosture).
func TestApplyGroupPolicies_PreservesAutonomyPosture(t *testing.T) {
	r := NewToolRegistry()
	on := SilentModeState{ToolConfirm: "allow", StepLimit: "deny", AskUser: "enable", ReviewPrompt: "allow"}
	r.ApplySecurityState(
		map[sdktools.ToolGroup]sdktools.ToolPolicy{sdktools.GroupExecute: sdktools.PolicyUserConfirm},
		false, AutonomyModeSilent, on,
	)

	r.ApplyGroupPolicies(
		map[sdktools.ToolGroup]sdktools.ToolPolicy{sdktools.GroupExecute: sdktools.PolicyAlwaysDeny},
		true,
	)

	if got := r.GroupPolicies()[sdktools.GroupExecute]; got != sdktools.PolicyAlwaysDeny {
		t.Errorf("execute policy = %v, want always_deny (replaced)", got)
	}
	r.mu.RLock()
	autoApprove := r.autoApproveWorkspaceWrites
	r.mu.RUnlock()
	if !autoApprove {
		t.Error("auto-approve must follow ApplyGroupPolicies")
	}
	if got := r.AutonomyMode(); got != AutonomyModeSilent {
		t.Errorf("autonomy mode = %q, want %q (preserved)", got, AutonomyModeSilent)
	}
	if got := r.SilentMode(); got != on {
		t.Errorf("silent mode = %+v, want %+v (preserved)", got, on)
	}
}

// TestApplyAutonomyPostureIfTightening pins the de-escalation exception to
// per-task posture pinning: a TIGHTENING posture (a strictly-less-automatic
// rank) is applied so a revoked unattended posture stops auto-approving a
// running task immediately, while an equal-or-looser (escalation) posture is
// ignored so a task can never silently BECOME unattended mid-run.
func TestApplyAutonomyPostureIfTightening(t *testing.T) {
	tightened := SilentModeState{ToolConfirm: "deny", StepLimit: "stop", AskUser: "disable", ReviewPrompt: "suppress"}
	seed := SilentModeState{ToolConfirm: "judge"}
	tests := []struct {
		name      string
		from      string
		to        string
		toSilent  SilentModeState
		wantApply bool
	}{
		{"silent to standard tightens", AutonomyModeSilent, AutonomyModeStandard, SilentModeState{}, true},
		{"silent to assisted tightens", AutonomyModeSilent, AutonomyModeAssisted, SilentModeState{}, true},
		{"assisted to standard tightens", AutonomyModeAssisted, AutonomyModeStandard, SilentModeState{}, true},
		{"standard to silent escalates (pinned)", AutonomyModeStandard, AutonomyModeSilent, tightened, false},
		{"assisted to silent escalates (pinned)", AutonomyModeAssisted, AutonomyModeSilent, tightened, false},
		{"standard to assisted escalates (pinned)", AutonomyModeStandard, AutonomyModeAssisted, tightened, false},
		// A same-mode save that revokes a silent sub-policy is a tightening and
		// must reach the running clone (findings: same-mode tightening).
		{"silent to silent sub-policy tightening applies", AutonomyModeSilent, AutonomyModeSilent, tightened, true},
		{"silent to silent tool_confirm deny from allow applies", AutonomyModeSilent, AutonomyModeSilent, SilentModeState{ToolConfirm: "deny"}, true},
		// A same-mode save that would loosen a sub-policy must be ignored.
		{"silent to silent looser sub-policy ignored", AutonomyModeSilent, AutonomyModeSilent, SilentModeState{ToolConfirm: "allow"}, false},
		{"silent to silent equal sub-policy (no change)", AutonomyModeSilent, AutonomyModeSilent, seed, false},
		// Sub-policies are inert outside the silent posture: a same-rank
		// non-silent save changes nothing.
		{"assisted to assisted sub-policy change ignored", AutonomyModeAssisted, AutonomyModeAssisted, tightened, false},
		{"unknown ranks as standard (no change)", AutonomyModeStandard, "weird", tightened, false},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			r := NewToolRegistry()
			r.ApplySecurityState(nil, false, tt.from, seed)
			got := r.ApplyAutonomyPostureIfTightening(tt.to, tt.toSilent)
			if got != tt.wantApply {
				t.Fatalf("ApplyAutonomyPostureIfTightening(%q) = %v, want %v", tt.to, got, tt.wantApply)
			}
			wantMode, wantSilent := tt.from, seed
			if tt.wantApply {
				wantMode, wantSilent = tt.to, tt.toSilent
			}
			if gotMode := r.AutonomyMode(); gotMode != wantMode {
				t.Errorf("autonomy mode = %q, want %q", gotMode, wantMode)
			}
			if gotSilent := r.SilentMode(); gotSilent != wantSilent {
				t.Errorf("silent mode = %+v, want %+v", gotSilent, wantSilent)
			}
		})
	}
}
