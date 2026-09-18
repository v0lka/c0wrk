package backend

import (
	"strings"
	"testing"

	"github.com/v0lka/c0wrk/backend/config"
	"github.com/v0lka/c0wrk/core"
)

// TestUpdateSecuritySettings_SilentModeRoundTrip verifies the silent-mode
// posture survives a Get → Update → Get round trip through the security RPC:
// the sub-policies round-trip verbatim and liveness follows the unified
// autonomy mode (security.autonomy_mode).
func TestUpdateSecuritySettings_SilentModeRoundTrip(t *testing.T) {
	f, mock, _ := newTestAPI(t)

	// Defaults: standard autonomy, sub-policies at their documented defaults.
	if got := f.GetSecuritySettings().AutonomyMode; got != config.AutonomyModeStandard {
		t.Fatalf("autonomy mode must default to %q, got %q", config.AutonomyModeStandard, got)
	}
	got := f.GetSecuritySettings().SilentMode
	if got.ToolConfirm.Mode != config.SilentToolConfirmJudge || got.AskUser.Mode != config.SilentAskUserDisable {
		t.Fatalf("default silent mode = %+v", got)
	}

	in := SilentModeResponse{
		ToolConfirm:  SilentSubPolicyResponse{Mode: config.SilentToolConfirmDeny},
		StepLimit:    SilentSubPolicyResponse{Mode: config.SilentStepLimitStop},
		AskUser:      SilentSubPolicyResponse{Mode: config.SilentAskUserEnable},
		ReviewPrompt: SilentSubPolicyResponse{Mode: config.SilentReviewPromptAllow},
	}
	if err := f.UpdateSecuritySettings(SecuritySettingsResponse{
		Groups:       fullGroupPayload(nil),
		AutonomyMode: config.AutonomyModeSilent,
		SilentMode:   in,
	}); err != nil {
		t.Fatalf("UpdateSecuritySettings: %v", err)
	}

	wantCfg := config.SilentModeConfig{
		ToolConfirm:  config.SilentSubPolicyConfig{Mode: config.SilentToolConfirmDeny},
		StepLimit:    config.SilentSubPolicyConfig{Mode: config.SilentStepLimitStop},
		AskUser:      config.SilentSubPolicyConfig{Mode: config.SilentAskUserEnable},
		ReviewPrompt: config.SilentSubPolicyConfig{Mode: config.SilentReviewPromptAllow},
	}
	if got := f.config.Security.SilentMode; got != wantCfg {
		t.Errorf("stored silent mode = %+v, want %+v", got, wantCfg)
	}
	if got := f.config.Security.AutonomyMode; got != config.AutonomyModeSilent {
		t.Errorf("stored autonomy mode = %q, want %q", got, config.AutonomyModeSilent)
	}
	if got := f.GetSecuritySettings().SilentMode; got != in {
		t.Errorf("GetSecuritySettings.SilentMode = %+v, want %+v", got, in)
	}

	// The runtime push (the live-session path) forwards the security state
	// to the builder, which broadcasts it to the shared registry and every
	// session clone — so the change applies without an app restart.
	if mock.updateSecPolicyLastCfg == nil {
		t.Fatal("UpdateSecuritySettings must push a BuilderConfig to the builder")
	}
	if got := mock.updateSecPolicyLastCfg.Security.AutonomyMode; got != core.AutonomyModeSilent {
		t.Errorf("pushed builder autonomy mode = %q, want %q", got, core.AutonomyModeSilent)
	}
	if got := mock.updateSecPolicyLastCfg.Security.SilentMode; got != (core.BuilderSilentModeConfig{
		ToolConfirm:  config.SilentToolConfirmDeny,
		StepLimit:    config.SilentStepLimitStop,
		AskUser:      config.SilentAskUserEnable,
		ReviewPrompt: config.SilentReviewPromptAllow,
	}) {
		t.Errorf("pushed builder silent mode = %+v, want the stored posture", got)
	}
}

// TestUpdateSecuritySettings_RejectsInvalidSilentModeEnum verifies a UI-sourced
// invalid enum is rejected and mutates nothing.
func TestUpdateSecuritySettings_RejectsInvalidSilentModeEnum(t *testing.T) {
	f, _, _ := newTestAPI(t)
	before := f.config.Security.SilentMode

	err := f.UpdateSecuritySettings(SecuritySettingsResponse{
		Groups:     fullGroupPayload(nil),
		SilentMode: SilentModeResponse{ToolConfirm: SilentSubPolicyResponse{Mode: "always"}},
	})
	if err == nil {
		t.Fatal("expected an error for an invalid silent-mode enum")
	}
	if !strings.Contains(err.Error(), "security.silent_mode.tool_confirm.mode") {
		t.Errorf("error %q must name the offending field", err)
	}
	if f.config.Security.SilentMode != before {
		t.Error("an invalid payload must mutate nothing")
	}
}

// TestUpdateSecuritySettings_UnsetSilentModeModeDefaults verifies a payload
// that selects the silent autonomy mode but omits every sub-policy mode is
// accepted, with the omitted modes defaulted (the UI need not send every
// mode).
func TestUpdateSecuritySettings_UnsetSilentModeModeDefaults(t *testing.T) {
	f, _, _ := newTestAPI(t)

	if err := f.UpdateSecuritySettings(SecuritySettingsResponse{
		Groups:       fullGroupPayload(nil),
		AutonomyMode: config.AutonomyModeSilent,
	}); err != nil {
		t.Fatalf("UpdateSecuritySettings: %v", err)
	}
	if got := f.config.Security.AutonomyMode; got != config.AutonomyModeSilent {
		t.Errorf("autonomy mode = %q, want the stored silent selection", got)
	}
	got := f.config.Security.SilentMode
	if got.ToolConfirm.Mode != config.SilentToolConfirmJudge || got.AskUser.Mode != config.SilentAskUserDisable {
		t.Errorf("omitted modes must default, got %+v", got)
	}
}
