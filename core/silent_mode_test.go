package core

import (
	"context"
	"encoding/json"
	"strings"
	"testing"

	"github.com/v0lka/c0wrk/core/tools"
)

// TestApplySecurityPolicies_PinsAutonomyToTaskLaunch verifies the
// one-directional per-task autonomy pinning contract: a runtime push updates
// the shared registry (the authoritative posture source) and the group policies
// of every live session clone. A clone's autonomy mode + silent-mode
// sub-policies are pinned at task launch
// (ToolRegistry.RefreshAutonomyPosture, called by the session manager at fresh
// sends and every resume path), so a task that started interactive can never
// silently turn MORE autonomous mid-run. The opposite (TIGHTENING) direction is
// NOT pinned: a revocation save (a less-permissive posture than the clone is
// running) IS delivered to a running clone immediately via
// ToolRegistry.ApplyAutonomyPostureIfTightening, so switching back to Standard
// stops a running silent task from auto-approving instead of deferring the
// revocation until the task ends or is resumed.
func TestApplySecurityPolicies_PinsAutonomyToTaskLaunch(t *testing.T) {
	cfgOf := func(mode string, sm BuilderSilentModeConfig) *BuilderConfig {
		return &BuilderConfig{
			Security: BuilderSecurityConfig{
				Groups:       map[string]BuilderGroupPolicy{"execute": {Policy: "user_confirm"}},
				AutonomyMode: mode,
				SilentMode:   sm,
			},
			ExpandEnvVars: func(s string) string { return s },
		}
	}

	b := &OrchestratorBuilder{registry: tools.NewToolRegistry()}
	b.applySecurityPolicies(cfgOf(AutonomyModeStandard, BuilderSilentModeConfig{}))

	// The "already-open session": a clone created under the old state.
	session := b.registerSessionRegistry()

	on := BuilderSilentModeConfig{ToolConfirm: "judge", StepLimit: "auto", AskUser: "disable"}
	b.UpdateSecurityPolicies(cfgOf(AutonomyModeSilent, on))

	want := tools.SilentModeState{ToolConfirm: "judge", StepLimit: "auto", AskUser: "disable"}
	if got := b.registry.SilentMode(); got != want {
		t.Errorf("shared registry silent mode = %+v, want %+v", got, want)
	}
	if got := b.registry.AutonomyMode(); got != AutonomyModeSilent {
		t.Errorf("shared registry autonomy mode = %q, want %q", got, AutonomyModeSilent)
	}
	// The live clone must NOT follow: enabling silent from the settings UI
	// must not flip a session that is (or was) running interactively — the
	// reported bug. Group policies still reach it (fail-closed posture,
	// pinned by TestUpdateSecurityPolicies_ReachesLiveSessionRegistries).
	if got := session.AutonomyMode(); got != AutonomyModeStandard {
		t.Errorf("live session autonomy mode = %q, want %q — a runtime enable must not reach a running task", got, AutonomyModeStandard)
	}
	if got := session.SilentMode(); got != (tools.SilentModeState{}) {
		t.Errorf("live session silent mode = %+v, want the zero posture (not pushed)", got)
	}

	// Task-launch boundary: the clone re-syncs from the shared registry and
	// runs the task under the current Settings.
	session.RefreshAutonomyPosture()
	if got := session.AutonomyMode(); got != AutonomyModeSilent {
		t.Errorf("post-refresh session autonomy mode = %q, want %q", got, AutonomyModeSilent)
	}
	if got := session.SilentMode(); got != want {
		t.Errorf("post-refresh session silent mode = %+v, want %+v", got, want)
	}

	// Turning it back off is a TIGHTENING save (silent → standard): the shared
	// registry updates AND the revocation reaches the running clone immediately
	// — a task must not keep auto-approving after the operator reasserts human
	// control (only the escalation direction is pinned). The clone's silent
	// sub-policies are cleared along with the mode.
	b.UpdateSecurityPolicies(cfgOf(AutonomyModeStandard, BuilderSilentModeConfig{}))
	if got := session.AutonomyMode(); got != AutonomyModeStandard {
		t.Errorf("session autonomy mode must follow a tightening save, got %q", got)
	}
	if got := session.SilentMode(); got != (tools.SilentModeState{}) {
		t.Errorf("session silent mode must be cleared by a tightening save, got %+v", got)
	}
	if got := b.registry.AutonomyMode(); got != AutonomyModeStandard {
		t.Errorf("shared registry autonomy mode = %q, want %q", got, AutonomyModeStandard)
	}
}

// TestAutonomyModeVocabularyPin pins the core ↔ core/tools autonomy-mode
// dictionaries against each other: the registry re-declares the enum strings
// (core/tools cannot import core), so a rename on either side must fail this
// test instead of silently desynchronizing the security posture — an
// unrecognized mode value falls back to standard (no automatic gate).
func TestAutonomyModeVocabularyPin(t *testing.T) {
	if AutonomyModeStandard != tools.AutonomyModeStandard ||
		AutonomyModeAssisted != tools.AutonomyModeAssisted ||
		AutonomyModeSilent != tools.AutonomyModeSilent {
		t.Fatalf("core and core/tools autonomy vocabularies drifted: core(%q,%q,%q) tools(%q,%q,%q)",
			AutonomyModeStandard, AutonomyModeAssisted, AutonomyModeSilent,
			tools.AutonomyModeStandard, tools.AutonomyModeAssisted, tools.AutonomyModeSilent)
	}
}

// TestReconcileAskUser_FollowsSilentMode verifies that a runtime silent-mode
// toggle re-registers the ask_user tool on the shared registry with the right
// callback — the live counterpart of the build-time logic in
// RegisterBuiltinTools. When silent mode disables ask_user the tool stays
// registered (visible in the tool catalog) but with a nil callback, so a call
// resolves to the explicit not-available result and the live callback is never
// reached; turning silent mode back off restores it.
func TestReconcileAskUser_FollowsSilentMode(t *testing.T) {
	ran := false
	b := &OrchestratorBuilder{registry: tools.NewToolRegistry()}
	b.askUserFunc = func(ctx context.Context, req tools.AskUserRequest) (tools.AskUserResponse, error) {
		ran = true
		return tools.AskUserResponse{}, nil
	}

	cfgDisable := func(mode string) *BuilderConfig {
		return &BuilderConfig{Security: BuilderSecurityConfig{
			AutonomyMode: mode,
			SilentMode:   BuilderSilentModeConfig{AskUser: "disable"},
		}}
	}
	input := json.RawMessage(`{"questions":[{"id":"q1","question":"Proceed?","options":[{"label":"Yes","value":"yes"}]}]}`)

	// Silent mode disables ask_user: it must stay registered with a nil
	// callback (explicit "not available"), and the live callback must not run.
	b.reconcileAskUser(cfgDisable(AutonomyModeSilent))
	tool, ok := b.registry.Get(ToolAskUser)
	if !ok {
		t.Fatal("ask_user must stay registered when silent mode disables it (a nil callback reports not-available)")
	}
	res, err := tool.Execute(context.Background(), input)
	if err != nil {
		t.Fatalf("Execute: %v", err)
	}
	if !res.IsError || !strings.Contains(res.Content, "not available") {
		t.Errorf("disabled ask_user must report not-available, got %+v", res)
	}
	if ran {
		t.Error("the ask_user callback must not run while silent mode disables it")
	}

	// Turning silent mode back off restores the live callback.
	b.reconcileAskUser(cfgDisable(AutonomyModeStandard))
	tool, ok = b.registry.Get(ToolAskUser)
	if !ok {
		t.Fatal("ask_user must remain registered")
	}
	res, err = tool.Execute(context.Background(), input)
	if err != nil {
		t.Fatalf("Execute: %v", err)
	}
	if res.IsError {
		t.Errorf("ask_user must work again once silent mode is off, got %+v", res)
	}
	if !ran {
		t.Error("the ask_user callback must run once silent mode is off")
	}

	// A builder without an ask_user callback registers nothing, and never panics.
	bare := &OrchestratorBuilder{registry: tools.NewToolRegistry()}
	bare.reconcileAskUser(cfgDisable(AutonomyModeSilent))
	if _, ok := bare.registry.Get(ToolAskUser); ok {
		t.Error("ask_user must not appear without an AskUserFunc")
	}
}
