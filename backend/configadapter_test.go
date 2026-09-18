package backend

import (
	"path/filepath"
	"testing"

	"github.com/v0lka/c0wrk/backend/config"
	"github.com/v0lka/c0wrk/core"
	coretools "github.com/v0lka/c0wrk/core/tools"
)

// TestAgentsMDSearchPaths verifies that agentsMDSearchPaths resolves the global
// and c0wrk-specific AGENTS.md paths relative to the user home directory in the
// documented priority order (global first, c0wrk second).
func TestAgentsMDSearchPaths(t *testing.T) {
	// os.UserHomeDir reads $HOME on Unix and %USERPROFILE% on Windows
	// (not %HOME%); override both to a temp dir for a deterministic result
	// on every platform Go supports.
	tmpHome := t.TempDir()
	t.Setenv("HOME", tmpHome)
	t.Setenv("USERPROFILE", tmpHome)

	got := agentsMDSearchPaths()
	want := []string{
		filepath.Join(tmpHome, ".agents", "AGENTS.md"),
		filepath.Join(tmpHome, ".c0wrk", ".agents", "AGENTS.md"),
	}
	if len(got) != len(want) {
		t.Fatalf("expected %d paths, got %d: %v", len(want), len(got), got)
	}
	for i, w := range want {
		if got[i] != w {
			t.Errorf("path[%d]: got %q, want %q", i, got[i], w)
		}
	}
}

// TestAgentsMDSearchPaths_NoHomeDir verifies that a failure to resolve the home
// directory yields nil (no search paths) rather than panicking.
func TestAgentsMDSearchPaths_NoHomeDir(t *testing.T) {
	// Unset HOME and USERPROFILE so os.UserHomeDir returns an error.
	t.Setenv("HOME", "")
	t.Setenv("USERPROFILE", "")

	got := agentsMDSearchPaths()
	if got != nil {
		t.Errorf("expected nil when home dir is unavailable, got %v", got)
	}
}

// modelProfilesTestCatalog returns the predefined catalog plus one custom profile
// ("test-profile") carrying fully-on variant values, for ToBuilderConfig
// mapping tests: the effective profile is resolved from the catalog, so test
// values live in a profile with cfg.ModelProfiles.ActiveProfile pointing at it.
func modelProfilesTestCatalog() []config.ModelProfile {
	customCfg := config.ModelProfileConfig{
		SystemPrompt: config.SystemPromptConfig{Lite: true, FewShot: true, ReasoningScaffold: true},
		Context: config.ModelProfilesContextConfig{
			Enabled:             true,
			Compaction:          config.ModelProfilesCompactionConfig{KeepLast: 6, BlockSize: 5, TriggerPercent: 80},
			ToolOutputKeepLastN: 2,
			OutputTokenReserve:  8192,
		},
	}
	custom, err := config.NewModelProfile("test-profile", "Test Profile", config.ModelProfileKindCustom, customCfg)
	if err != nil {
		panic(err)
	}
	return append(config.PredefinedModelProfiles(), custom)
}

// TestToBuilderConfig_ModelProfilesSystemPrompt verifies the config→builder mapping
// for the prompt-simplification variant: the active profile's
// SystemPrompt.{Lite, FewShot, ReasoningScaffold} all flow into
// BuilderModelProfilesSystemPromptConfig so a profile change takes effect on rebuild.
func TestToBuilderConfig_ModelProfilesSystemPrompt(t *testing.T) {
	cfg := &config.Config{}
	cfg.Experimental.Enabled = true
	cfg.ModelProfiles.Enabled = true
	cfg.ModelProfiles.ActiveProfile = "test-profile"

	bc := ToBuilderConfig(cfg, modelProfilesTestCatalog())

	if !bc.ModelProfiles.Enabled {
		t.Error("ModelProfiles.Enabled not mapped")
	}
	// Lite is the SystemPrompt variant master toggle (there is no separate
	// Enabled field in config/builder — see SystemPromptConfig). It must map
	// straight through so a profile change takes effect on rebuild.
	if !bc.ModelProfiles.SystemPrompt.Lite {
		t.Error("SystemPrompt.Lite not mapped")
	}
	if !bc.ModelProfiles.SystemPrompt.FewShot {
		t.Error("SystemPrompt.FewShot not mapped")
	}
	if !bc.ModelProfiles.SystemPrompt.ReasoningScaffold {
		t.Error("SystemPrompt.ReasoningScaffold not mapped")
	}

	// Master off — even with Lite true, Enabled carries the master gate only.
	cfg.ModelProfiles.Enabled = false
	bc = ToBuilderConfig(cfg, modelProfilesTestCatalog())
	if bc.ModelProfiles.Enabled {
		t.Error("master Enabled should be false")
	}
	// The variant sub-toggle still reflects the profile value (master gating is
	// applied at runtime, not stripped at the mapping layer).
	if !bc.ModelProfiles.SystemPrompt.Lite {
		t.Error("SystemPrompt.Lite should still map the profile value")
	}
}

// TestToBuilderConfig_ModelProfilesContext verifies the config→builder mapping for
// the context-management variant: the active profile's Context.{Enabled,
// Compaction, ToolOutputKeepLastN, OutputTokenReserve} all flow into
// BuilderModelProfilesContext so a profile change takes effect on rebuild.
func TestToBuilderConfig_ModelProfilesContext(t *testing.T) {
	cfg := &config.Config{}
	cfg.Experimental.Enabled = true
	cfg.ModelProfiles.Enabled = true
	cfg.ModelProfiles.ActiveProfile = "test-profile"

	bc := ToBuilderConfig(cfg, modelProfilesTestCatalog())

	if !bc.ModelProfiles.Context.Enabled {
		t.Error("Context.Enabled not mapped")
	}
	if bc.ModelProfiles.Context.Compaction.KeepLast != 6 {
		t.Errorf("Context.Compaction.KeepLast = %d, want 6", bc.ModelProfiles.Context.Compaction.KeepLast)
	}
	if bc.ModelProfiles.Context.Compaction.BlockSize != 5 {
		t.Errorf("Context.Compaction.BlockSize = %d, want 5", bc.ModelProfiles.Context.Compaction.BlockSize)
	}
	if bc.ModelProfiles.Context.Compaction.TriggerPercent != 80 {
		t.Errorf("Context.Compaction.TriggerPercent = %d, want 80", bc.ModelProfiles.Context.Compaction.TriggerPercent)
	}
	if bc.ModelProfiles.Context.ToolOutputKeepLastN != 2 {
		t.Errorf("Context.ToolOutputKeepLastN = %d, want 2", bc.ModelProfiles.Context.ToolOutputKeepLastN)
	}
	if bc.ModelProfiles.Context.OutputTokenReserve != 8192 {
		t.Errorf("Context.OutputTokenReserve = %d, want 8192", bc.ModelProfiles.Context.OutputTokenReserve)
	}

	// Master off — Enabled carries the master gate only; the variant values
	// still map through (gating is applied at runtime by
	// applyContextManagement, not stripped at the mapping layer).
	cfg.ModelProfiles.Enabled = false
	bc = ToBuilderConfig(cfg, modelProfilesTestCatalog())
	if bc.ModelProfiles.Enabled {
		t.Error("master Enabled should be false")
	}
	if !bc.ModelProfiles.Context.Enabled {
		t.Error("Context.Enabled should still map the profile value")
	}
}

// TestToBuilderConfig_ModelProfilesSamplingReasoningEffortDefault verifies the
// seeded reasoning-effort default (docs/development/model-profiles-defaults-research.md, R3)
// end to end on the c0wrk side: a freshly defaulted config (active profile
// "generic") with the sampling variant enabled resolves to reasoning effort
// "medium" in the builder config. applyModelProfilesPresets (core) then seeds it as
// the builder-level default, and sp4rk's qwen mapping sends the native
// reasoning_effort parameter per request.
func TestToBuilderConfig_ModelProfilesSamplingReasoningEffortDefault(t *testing.T) {
	cfg := &config.Config{}
	config.ApplyDefaults(cfg)
	cfg.Experimental.Enabled = true
	cfg.ModelProfiles.Enabled = true
	// Enable the sampling variant on a writable copy of the generic profile so
	// the variant is actually active (values are profile-owned now).
	catalog := config.PredefinedModelProfiles()
	for i := range catalog {
		if catalog[i].ID != config.ModelProfilesGenericProfileID {
			continue
		}
		variantCfg := catalog[i].Config
		variantCfg.Sampling.Enabled = true
		withSampling, err := config.NewModelProfile(catalog[i].ID, catalog[i].Name, catalog[i].Kind, variantCfg)
		if err != nil {
			t.Fatalf("NewModelProfile: %v", err)
		}
		catalog[i] = withSampling
		break
	}

	bc := ToBuilderConfig(cfg, catalog)

	if got := bc.ModelProfiles.Sampling.ReasoningEffort; got != "medium" {
		t.Errorf("fresh config with the sampling variant enabled resolves ReasoningEffort = %q, want the seeded default %q", got, "medium")
	}
}

// TestToBuilderConfig_ModelProfilesNotGatedByExperimental verifies Model Profiles
// is no longer gated by the experimental master switch: the stored
// ModelProfiles.Enabled flows through ToBuilderConfig verbatim regardless of
// whether experimental features are on — the master toggle alone decides.
func TestToBuilderConfig_ModelProfilesNotGatedByExperimental(t *testing.T) {
	cfg := &config.Config{}
	cfg.ModelProfiles.Enabled = true

	// Experimental off (zero value) → the stored Model Profiles master still flows through.
	if bc := ToBuilderConfig(cfg, config.PredefinedModelProfiles()); !bc.ModelProfiles.Enabled {
		t.Error("experimental off must not clear ModelProfiles.Enabled")
	}

	// Experimental on → the stored Model Profiles master flows through too.
	cfg.Experimental.Enabled = true
	if bc := ToBuilderConfig(cfg, config.PredefinedModelProfiles()); !bc.ModelProfiles.Enabled {
		t.Error("experimental on should preserve ModelProfiles.Enabled true")
	}

	// Master off → false regardless of the experimental gate.
	cfg.ModelProfiles.Enabled = false
	if bc := ToBuilderConfig(cfg, config.PredefinedModelProfiles()); bc.ModelProfiles.Enabled {
		t.Error("master off must keep ModelProfiles.Enabled false")
	}
}

// TestToBuilderConfig_ProviderOutputTokenReserve verifies the per-provider
// output_token_reserve plumbing (D4): provider-level values flow into
// BuilderProviderConfig.
func TestToBuilderConfig_ProviderOutputTokenReserve(t *testing.T) {
	cfg := &config.Config{}
	cfg.LLM.Anthropic.OutputTokenReserve = 12288
	cfg.LLM.OpenAICompatible = map[string]config.OpenAICompatibleConfig{
		"lmstudio": {
			BaseURL:            "http://localhost:1234/v1",
			Models:             []string{"qwen/qwen3-coder-30b"},
			OutputTokenReserve: 8192,
		},
	}

	bc := ToBuilderConfig(cfg, config.PredefinedModelProfiles())

	if got := bc.LLM.ProviderConfigs["anthropic"].OutputTokenReserve; got != 12288 {
		t.Errorf("anthropic OutputTokenReserve = %d, want 12288", got)
	}
	if got := bc.LLM.ProviderConfigs["lmstudio"].OutputTokenReserve; got != 8192 {
		t.Errorf("lmstudio OutputTokenReserve = %d, want 8192", got)
	}
	// Providers without an explicit reserve must map zero (inherit), not
	// accidentally inherit some other provider's value.
	cfg.LLM.OpenAICompatible["other"] = config.OpenAICompatibleConfig{Models: []string{"m"}}
	bc = ToBuilderConfig(cfg, config.PredefinedModelProfiles())
	if got := bc.LLM.ProviderConfigs["other"].OutputTokenReserve; got != 0 {
		t.Errorf("other OutputTokenReserve = %d, want 0 (inherit)", got)
	}
}

// TestToBuilderConfig_WebFetchTimeouts verifies the config→builder mapping for
// the web fetch timeout/retry knobs: the proxy-path timeout and the retry
// count flow into BuilderTimeoutsConfig so both reach sp4rk's web_fetch tool
// (each retry doubles the effective client timeout).
func TestToBuilderConfig_WebFetchTimeouts(t *testing.T) {
	cfg := &config.Config{}
	config.ApplyDefaults(cfg)

	bc := ToBuilderConfig(cfg, config.PredefinedModelProfiles())
	if got := bc.Timeouts.WebFetchTimeout; got != 30 {
		t.Errorf("WebFetchTimeout default = %d, want 30", got)
	}
	if got := bc.Timeouts.WebFetchProxyTimeout; got != 30 {
		t.Errorf("WebFetchProxyTimeout default = %d, want 30", got)
	}
	if got := bc.Timeouts.WebFetchRetries; got != 2 {
		t.Errorf("WebFetchRetries default = %d, want 2", got)
	}

	cfg.Timeouts.WebFetchProxyTimeout = 45
	cfg.Timeouts.WebFetchRetries = 3

	bc = ToBuilderConfig(cfg, config.PredefinedModelProfiles())
	if got := bc.Timeouts.WebFetchProxyTimeout; got != 45 {
		t.Errorf("WebFetchProxyTimeout = %d, want 45", got)
	}
	if got := bc.Timeouts.WebFetchRetries; got != 3 {
		t.Errorf("WebFetchRetries = %d, want 3", got)
	}
}

// TestE2SConfigExperimentalGate pins the fail-closed gate for the E2S
// execution mode: core's BuilderE2SConfig.Enabled is exactly the experimental
// master switch (there is no separate e2s toggle), while the numeric knobs
// pass through regardless so tuning survives the gate.
func TestE2SConfigExperimentalGate(t *testing.T) {
	cfg := &config.Config{}
	config.ApplyDefaults(cfg)
	cfg.E2S.MaxSteps = 42

	// Fail-closed: the experimental gate off disables E2S.
	cfg.Experimental.Enabled = false
	if ToBuilderConfig(cfg, config.PredefinedModelProfiles()).E2S.Enabled {
		t.Error("BuilderE2SConfig.Enabled must be false while the experimental gate is off")
	}

	// Gate on: E2S is available and the stored knobs pass through verbatim.
	cfg.Experimental.Enabled = true
	got := ToBuilderConfig(cfg, config.PredefinedModelProfiles()).E2S
	if !got.Enabled {
		t.Error("BuilderE2SConfig.Enabled must follow the experimental gate")
	}
	if got.MaxSteps != 42 {
		t.Errorf("BuilderE2SConfig.MaxSteps = %d, want 42 (values pass through)", got.MaxSteps)
	}
}

// TestToBuilderConfig_SilentModeEnumPin pins the silent-mode mode vocabulary
// shared by backend/config and core. The mode strings travel verbatim through
// ToBuilderConfig into core's silent-mode posture (BuilderSilentModeConfig →
// tools.SilentModeState), where core interprets them against its own constants
// (the silentToolTerminal switch and the AskUserDisabled predicates). core
// cannot import backend/config, so this package is the only layer that sees
// BOTH dictionaries: a config-side rename that core does not follow would make
// every tool_confirm mode silently fall back to the judge default and keep
// ask_user registered in unattended runs — a security-posture regression no
// core-side test can catch (core tests spell core's literals, config tests
// spell config's).
func TestToBuilderConfig_SilentModeEnumPin(t *testing.T) {
	// Direct dictionary comparison: the config enum constants must equal the
	// core vocabulary constants the registry interprets them with.
	if config.SilentToolConfirmJudge != coretools.SilentToolConfirmJudge ||
		config.SilentToolConfirmAllow != coretools.SilentToolConfirmAllow ||
		config.SilentToolConfirmDeny != coretools.SilentToolConfirmDeny {
		t.Fatalf(
			"silent-mode tool_confirm enum desynchronized: config(judge=%q, allow=%q, deny=%q) vs core(judge=%q, allow=%q, deny=%q) — rename both sides together",
			config.SilentToolConfirmJudge, config.SilentToolConfirmAllow, config.SilentToolConfirmDeny,
			coretools.SilentToolConfirmJudge, coretools.SilentToolConfirmAllow, coretools.SilentToolConfirmDeny,
		)
	}
	if config.SilentAskUserDisable != coretools.SilentAskUserDisable {
		t.Fatalf(
			"silent-mode ask_user enum desynchronized: config=%q vs core=%q — rename both sides together",
			config.SilentAskUserDisable, coretools.SilentAskUserDisable,
		)
	}
	// Same pin for the unified autonomy-mode vocabulary: config re-declares
	// core's constants (core never imports backend/config), so a one-sided
	// rename would silently strand the posture on the loader's fail-safe
	// "standard".
	if config.AutonomyModeStandard != core.AutonomyModeStandard ||
		config.AutonomyModeAssisted != core.AutonomyModeAssisted ||
		config.AutonomyModeSilent != core.AutonomyModeSilent {
		t.Fatalf(
			"autonomy-mode enum desynchronized: config(standard=%q, assisted=%q, silent=%q) vs core(standard=%q, assisted=%q, silent=%q) — rename both sides together",
			config.AutonomyModeStandard, config.AutonomyModeAssisted, config.AutonomyModeSilent,
			core.AutonomyModeStandard, core.AutonomyModeAssisted, core.AutonomyModeSilent,
		)
	}

	// Through the real adapter: the config enums must flow into the builder
	// posture verbatim (core/builder.go copies these strings unchanged into
	// tools.SilentModeState, which core interprets with its constants).
	cfg := &config.Config{}
	cfg.Security.AutonomyMode = config.AutonomyModeSilent
	cfg.Security.SilentMode = config.SilentModeConfig{
		ToolConfirm: config.SilentSubPolicyConfig{Mode: config.SilentToolConfirmDeny},
		StepLimit:   config.SilentSubPolicyConfig{Mode: config.SilentStepLimitStop},
		AskUser:     config.SilentSubPolicyConfig{Mode: config.SilentAskUserDisable},
	}
	builderSecurity := ToBuilderConfig(cfg, config.PredefinedModelProfiles()).Security
	posture := builderSecurity.SilentMode
	if posture.ToolConfirm != coretools.SilentToolConfirmDeny || posture.AskUser != coretools.SilentAskUserDisable {
		t.Fatalf("ToBuilderConfig must pass the silent-mode modes through verbatim, got %+v", posture)
	}
	if builderSecurity.AutonomyMode != config.AutonomyModeSilent {
		t.Fatalf("ToBuilderConfig must carry autonomy_mode verbatim, got %q", builderSecurity.AutonomyMode)
	}

	// Behaviorally: both ask_user predicates (the registration config and its
	// builder mirror) must recognize the config enum value the adapter
	// forwards — a drifted "disable" spelling would leave ask_user live in
	// unattended runs.
	if !(coretools.BuiltinToolsConfig{
		AutonomyMode: coretools.AutonomyModeSilent,
		SilentMode:   coretools.SilentModeState{AskUser: config.SilentAskUserDisable},
	}).AskUserDisabled() {
		t.Error("tools.BuiltinToolsConfig.AskUserDisabled must recognize the silent autonomy mode plus config.SilentAskUserDisable")
	}
	if !builderSecurity.AskUserDisabled() {
		t.Error("core.BuilderSecurityConfig.AskUserDisabled must recognize the silent autonomy mode plus config.SilentAskUserDisable")
	}
	if builderSecurity.SilentModeEnabled() != true || builderSecurity.SmartApproveEnabled() != true {
		t.Error("the silent autonomy mode must derive SilentModeEnabled and SmartApproveEnabled")
	}
	// The assisted mode derives only Smart Approve; standard derives neither.
	assistCfg := &config.Config{}
	assistCfg.Security.AutonomyMode = config.AutonomyModeAssisted
	assistSecurity := ToBuilderConfig(assistCfg, config.PredefinedModelProfiles()).Security
	if assistSecurity.SmartApproveEnabled() != true || assistSecurity.SilentModeEnabled() != false {
		t.Error("the assisted autonomy mode must derive SmartApproveEnabled only")
	}
	stdCfg := &config.Config{}
	stdCfg.Security.AutonomyMode = config.AutonomyModeStandard
	stdSecurity := ToBuilderConfig(stdCfg, config.PredefinedModelProfiles()).Security
	if stdSecurity.SmartApproveEnabled() || stdSecurity.SilentModeEnabled() {
		t.Error("the standard autonomy mode must derive neither flag")
	}
}
