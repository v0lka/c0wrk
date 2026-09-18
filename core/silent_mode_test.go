package core

import (
	"context"
	"encoding/json"
	"strings"
	"testing"

	"github.com/v0lka/c0wrk/core/tools"
)

// TestApplySecurityPolicies_PushesSilentModeToClones verifies the runtime push
// reaches the shared registry AND every live session clone — the same contract
// the group policies uphold, so a silent-mode toggle from the settings UI takes
// effect on already-open sessions without an app restart.
func TestApplySecurityPolicies_PushesSilentModeToClones(t *testing.T) {
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
	if got := session.SilentMode(); got != want {
		t.Errorf("live session silent mode = %+v, want %+v — a runtime toggle must reach already-open sessions", got, want)
	}
	if got := b.registry.AutonomyMode(); got != AutonomyModeSilent {
		t.Errorf("shared registry autonomy mode = %q, want %q", got, AutonomyModeSilent)
	}
	if got := session.AutonomyMode(); got != AutonomyModeSilent {
		t.Errorf("live session autonomy mode = %q, want %q", got, AutonomyModeSilent)
	}

	// Turning it back off propagates too (replacement, not merge).
	b.UpdateSecurityPolicies(cfgOf(AutonomyModeStandard, BuilderSilentModeConfig{}))
	if got := session.AutonomyMode(); got != AutonomyModeStandard {
		t.Errorf("session autonomy mode must follow the runtime push off, got %q", got)
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
