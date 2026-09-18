package config

import (
	"os"
	"path/filepath"
	"strings"
	"testing"

	"gopkg.in/yaml.v3"
)

// TestSilentMode_DefaultsAndValidation pins the security.silent_mode contract:
// each sub-policy is seeded with its documented default, an explicit value
// survives ApplyDefaults, and every sub-policy enum rejects an unknown value.
// The container itself no longer has a master switch — liveness is decided by
// security.autonomy_mode ("silent"), which defaults to "standard".
func TestSilentMode_DefaultsAndValidation(t *testing.T) {
	// Zero-value config: sub-policy defaults seeded, autonomy standard.
	cfg := &Config{}
	ApplyDefaults(cfg)
	if got, want := cfg.Security.AutonomyMode, AutonomyModeStandard; got != want {
		t.Errorf("security.autonomy_mode default = %q, want %q", got, want)
	}
	if got, want := cfg.Security.SilentMode, SilentModeDefaults(); got != want {
		t.Errorf("silent-mode defaults = %+v, want %+v", got, want)
	}

	// Explicit values (including the autonomy mode) survive ApplyDefaults
	// untouched.
	explicit := &Config{Security: SecurityConfig{
		AutonomyMode: AutonomyModeSilent,
		SilentMode: SilentModeConfig{
			ToolConfirm: SilentSubPolicyConfig{Mode: SilentToolConfirmDeny},
			StepLimit:   SilentSubPolicyConfig{Mode: SilentStepLimitStop},
			AskUser:     SilentSubPolicyConfig{Mode: SilentAskUserEnable},
		}}}
	ApplyDefaults(explicit)
	if got, want := explicit.Security.AutonomyMode, AutonomyModeSilent; got != want {
		t.Errorf("ApplyDefaults clobbered autonomy mode: got %q want %q", got, want)
	}
	if got, want := explicit.Security.SilentMode, (SilentModeConfig{
		ToolConfirm: SilentSubPolicyConfig{Mode: SilentToolConfirmDeny},
		StepLimit:   SilentSubPolicyConfig{Mode: SilentStepLimitStop},
		AskUser:     SilentSubPolicyConfig{Mode: SilentAskUserEnable},
	}); got != want {
		t.Errorf("ApplyDefaults clobbered explicit silent-mode values: got %+v want %+v", got, want)
	}

	// Every documented value is accepted.
	valid := []SilentModeConfig{
		SilentModeDefaults(),
		{
			ToolConfirm: SilentSubPolicyConfig{Mode: SilentToolConfirmJudge},
			StepLimit:   SilentSubPolicyConfig{Mode: SilentStepLimitAuto},
			AskUser:     SilentSubPolicyConfig{Mode: SilentAskUserDisable},
		},
		{
			ToolConfirm: SilentSubPolicyConfig{Mode: SilentToolConfirmAllow},
			StepLimit:   SilentSubPolicyConfig{Mode: SilentStepLimitStop},
			AskUser:     SilentSubPolicyConfig{Mode: SilentAskUserEnable},
		},
		{
			ToolConfirm: SilentSubPolicyConfig{Mode: SilentToolConfirmDeny},
			StepLimit:   SilentSubPolicyConfig{Mode: SilentStepLimitAllowAlways},
			AskUser:     SilentSubPolicyConfig{Mode: SilentAskUserDisable},
		},
	}
	for _, c := range valid {
		if err := ValidateSilentMode(c); err != nil {
			t.Errorf("ValidateSilentMode(%+v) = %v, want nil", c, err)
		}
	}

	// Each sub-policy rejects an unknown enum, naming the offending field.
	invalid := []struct {
		name string
		sm   SilentModeConfig
	}{
		{"tool_confirm", SilentModeConfig{ToolConfirm: SilentSubPolicyConfig{Mode: "always"}}},
		{"step_limit", SilentModeConfig{StepLimit: SilentSubPolicyConfig{Mode: "forever"}}},
		{"ask_user", SilentModeConfig{AskUser: SilentSubPolicyConfig{Mode: "maybe"}}},
	}
	for _, tc := range invalid {
		err := ValidateSilentMode(tc.sm)
		if err == nil {
			t.Errorf("ValidateSilentMode(%s bad enum) = nil, want an error", tc.name)
			continue
		}
		if !strings.Contains(err.Error(), "security.silent_mode."+tc.name+".mode") {
			t.Errorf("error %q must name security.silent_mode.%s.mode", err, tc.name)
		}
	}
}

// TestAutonomyMode_AskUserDisabled pins the predicate that suppresses ask_user
// registration: only when the autonomy mode is "silent" AND the ask_user
// sub-policy says "disable".
func TestAutonomyMode_AskUserDisabled(t *testing.T) {
	if (SecurityConfig{AutonomyMode: AutonomyModeStandard, SilentMode: SilentModeConfig{AskUser: SilentSubPolicyConfig{Mode: SilentAskUserDisable}}}).AskUserDisabled() {
		t.Error("standard mode must never disable ask_user")
	}
	if (SecurityConfig{AutonomyMode: AutonomyModeAssisted, SilentMode: SilentModeConfig{AskUser: SilentSubPolicyConfig{Mode: SilentAskUserDisable}}}).AskUserDisabled() {
		t.Error("assisted mode must never disable ask_user")
	}
	if (SecurityConfig{AutonomyMode: AutonomyModeSilent, SilentMode: SilentModeConfig{AskUser: SilentSubPolicyConfig{Mode: SilentAskUserEnable}}}).AskUserDisabled() {
		t.Error("ask_user.mode=enable must not disable ask_user")
	}
	if !(SecurityConfig{AutonomyMode: AutonomyModeSilent, SilentMode: SilentModeConfig{AskUser: SilentSubPolicyConfig{Mode: SilentAskUserDisable}}}).AskUserDisabled() {
		t.Error("silent mode + ask_user.mode=disable must disable ask_user")
	}
}

// TestSilentMode_YAMLRoundTrip verifies the yaml keys the docs promise and a
// faithful marshal/unmarshal round trip. The legacy `enabled` key must NOT be
// written anymore — the container persists only the three sub-policies.
func TestSilentMode_YAMLRoundTrip(t *testing.T) {
	src := SilentModeConfig{
		ToolConfirm: SilentSubPolicyConfig{Mode: SilentToolConfirmAllow},
		StepLimit:   SilentSubPolicyConfig{Mode: SilentStepLimitStop},
		AskUser:     SilentSubPolicyConfig{Mode: SilentAskUserEnable},
	}
	data, err := yaml.Marshal(src)
	if err != nil {
		t.Fatalf("yaml.Marshal: %v", err)
	}
	for _, key := range []string{"tool_confirm", "step_limit", "ask_user", "mode"} {
		if !strings.Contains(string(data), key) {
			t.Errorf("silent-mode yaml is missing key %q; got:\n%s", key, data)
		}
	}
	if strings.Contains(string(data), "enabled") {
		t.Errorf("silent-mode yaml must not carry the legacy enabled key; got:\n%s", data)
	}
	var restored SilentModeConfig
	if err := yaml.Unmarshal(data, &restored); err != nil {
		t.Fatalf("yaml.Unmarshal: %v", err)
	}
	if restored != src {
		t.Errorf("round-tripped silent-mode = %+v, want %+v", restored, src)
	}
}

// TestSilentMode_Load verifies the end-to-end loader: a valid block loads and
// preserves values, and an invalid sub-policy enum fails the load.
func TestSilentMode_Load(t *testing.T) {
	const base = `
llm:
  default_model: claude-3-haiku
  anthropic:
    api_key: "test-key"
    models:
      - claude-3-haiku
security:
  silent_mode:
`

	t.Run("valid block loads", func(t *testing.T) {
		content := base + `    tool_confirm: {mode: deny}
    step_limit: {mode: stop}
    ask_user: {mode: enable}
`
		cfg, err := Load(writeTestConfig(t, content))
		if err != nil {
			t.Fatalf("Load() failed: %v", err)
		}
		got := cfg.Security.SilentMode
		want := SilentModeConfig{
			ToolConfirm: SilentSubPolicyConfig{Mode: SilentToolConfirmDeny},
			StepLimit:   SilentSubPolicyConfig{Mode: SilentStepLimitStop},
			AskUser:     SilentSubPolicyConfig{Mode: SilentAskUserEnable},
		}
		if got != want {
			t.Errorf("loaded silent-mode = %+v, want %+v", got, want)
		}
	})

	t.Run("absent block defaults", func(t *testing.T) {
		cfg, err := Load(writeTestConfig(t, base))
		if err != nil {
			t.Fatalf("Load() failed: %v", err)
		}
		if got, want := cfg.Security.AutonomyMode, AutonomyModeStandard; got != want {
			t.Errorf("absent autonomy_mode defaults to %q, want %q", got, want)
		}
		if got, want := cfg.Security.SilentMode, SilentModeDefaults(); got != want {
			t.Errorf("defaulted silent-mode = %+v, want %+v", got, want)
		}
	})

	for name, line := range map[string]string{
		"tool_confirm": "    tool_confirm: {mode: always}\n",
		"step_limit":   "    step_limit: {mode: forever}\n",
		"ask_user":     "    ask_user: {mode: maybe}\n",
	} {
		t.Run("invalid "+name+" enum rejected", func(t *testing.T) {
			if _, err := Load(writeTestConfig(t, base+line)); err == nil {
				t.Errorf("Load() with an invalid %s enum must fail", name)
			}
		})
	}
}

// TestSilentMode_RemovedReviewPromptKeyIgnored pins the removal of the
// review_prompt sub-policy: a config file still carrying the stale
// security.silent_mode.review_prompt key loads without an error and without
// warnings (the yaml decode is non-strict, so the unknown key is ignored
// rather than rejected), and the surviving sub-policies load normally. The
// stale key simply disappears at the next Save.
func TestSilentMode_RemovedReviewPromptKeyIgnored(t *testing.T) {
	const base = `
llm:
  default_model: claude-3-haiku
  anthropic:
    api_key: "test-key"
    models:
      - claude-3-haiku
security:
  silent_mode:
    review_prompt: {mode: allow}
    tool_confirm: {mode: deny}
`
	result, err := LoadWithResult(writeTestConfig(t, base))
	if err != nil {
		t.Fatalf("Load() with the stale review_prompt key must succeed, got: %v", err)
	}
	if len(result.LoadErrors) != 0 {
		t.Errorf("the stale review_prompt key must be silently ignored, got warnings: %v", result.LoadErrors)
	}
	want := SilentModeConfig{
		ToolConfirm: SilentSubPolicyConfig{Mode: SilentToolConfirmDeny},
		StepLimit:   SilentSubPolicyConfig{Mode: SilentStepLimitAuto},
		AskUser:     SilentSubPolicyConfig{Mode: SilentAskUserDisable},
	}
	if got := result.Config.Security.SilentMode; got != want {
		t.Errorf("loaded silent-mode = %+v, want %+v (stale key ignored, others intact)", got, want)
	}
}

// autonomyBase is the minimal loadable config for the autonomy-migration
// tests; securityContent is appended under the `security:` key.
const autonomyBase = `
llm:
  default_model: claude-3-haiku
  anthropic:
    api_key: "test-key"
    models:
      - claude-3-haiku
`

// loadAutonomy loads autonomyBase plus the given security-block yaml and
// returns the result (config + load warnings) or fails the test.
func loadAutonomy(t *testing.T, securityContent string) *LoadResult {
	t.Helper()
	result, err := LoadWithResult(writeTestConfig(t, autonomyBase+securityContent))
	if err != nil {
		t.Fatalf("LoadWithResult() failed: %v", err)
	}
	return result
}

// TestAutonomyMode_Migration pins the legacy-key migration matrix: the
// pre-enum smart_approve / silent_mode.enabled booleans map onto
// security.autonomy_mode (silent wins — an explicit unattended posture beats
// assisted), every observed legacy key produces a load warning, and an
// unknown enum value fails safe to standard with a warning.
func TestAutonomyMode_Migration(t *testing.T) {
	const silentPolicies = `    tool_confirm: {mode: deny}
    step_limit: {mode: stop}
    ask_user: {mode: enable}
`
	cases := []struct {
		name        string
		security    string
		wantMode    string
		wantWarning bool
	}{
		{
			name:        "smart_approve true maps to assisted",
			security:    "security:\n  smart_approve: true\n",
			wantMode:    AutonomyModeAssisted,
			wantWarning: true,
		},
		{
			name:        "silent_mode.enabled true maps to silent and keeps policies",
			security:    "security:\n  silent_mode:\n    enabled: true\n" + silentPolicies,
			wantMode:    AutonomyModeSilent,
			wantWarning: true,
		},
		{
			name:        "both false maps to standard",
			security:    "security:\n  smart_approve: false\n  silent_mode:\n    enabled: false\n",
			wantMode:    AutonomyModeStandard,
			wantWarning: true,
		},
		{
			name:        "both true maps to silent (silent wins)",
			security:    "security:\n  smart_approve: true\n  silent_mode:\n    enabled: true\n" + silentPolicies,
			wantMode:    AutonomyModeSilent,
			wantWarning: true,
		},
		{
			name:     "no legacy keys default to standard without warnings",
			security: "security:\n  silent_mode:\n" + silentPolicies,
			wantMode: AutonomyModeStandard,
		},
		{
			name:        "unknown enum value fails safe to standard",
			security:    "security:\n  autonomy_mode: foo\n",
			wantMode:    AutonomyModeStandard,
			wantWarning: true,
		},
		{
			name:        "explicit enum wins over legacy keys",
			security:    "security:\n  autonomy_mode: assisted\n  smart_approve: true\n  silent_mode:\n    enabled: true\n",
			wantMode:    AutonomyModeAssisted,
			wantWarning: true,
		},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			result := loadAutonomy(t, tc.security)
			if got := result.Config.Security.AutonomyMode; got != tc.wantMode {
				t.Errorf("autonomy_mode = %q, want %q", got, tc.wantMode)
			}
			if tc.wantWarning && len(result.LoadErrors) == 0 {
				t.Errorf("expected a load warning, got none (LoadErrors = %v)", result.LoadErrors)
			}
			if !tc.wantWarning && len(result.LoadErrors) != 0 {
				t.Errorf("expected no load warnings, got %v", result.LoadErrors)
			}
			// The silent-mode sub-policies survive the migration verbatim
			// whenever they were written.
			if strings.Contains(tc.security, "tool_confirm") {
				want := SilentModeConfig{
					ToolConfirm: SilentSubPolicyConfig{Mode: SilentToolConfirmDeny},
					StepLimit:   SilentSubPolicyConfig{Mode: SilentStepLimitStop},
					AskUser:     SilentSubPolicyConfig{Mode: SilentAskUserEnable},
				}
				if got := result.Config.Security.SilentMode; got != want {
					t.Errorf("migrated silent-mode policies = %+v, want %+v (policies must survive migration)", got, want)
				}
			}
		})
	}
}

// TestAutonomyMode_MigrationWarningsNameLegacyKeys verifies the load warnings
// name the deprecated keys and the resolution, so an operator can see what a
// legacy config was migrated to.
func TestAutonomyMode_MigrationWarningsNameLegacyKeys(t *testing.T) {
	result := loadAutonomy(t, "security:\n  smart_approve: true\n  silent_mode:\n    enabled: true\n")
	joined := strings.Join(result.LoadErrors, "\n")
	for _, want := range []string{"smart_approve", "silent_mode.enabled", AutonomyModeSilent} {
		if !strings.Contains(joined, want) {
			t.Errorf("load warning %q must mention %q", joined, want)
		}
	}

	unknown := loadAutonomy(t, "security:\n  autonomy_mode: foo\n")
	joined = strings.Join(unknown.LoadErrors, "\n")
	for _, want := range []string{"autonomy_mode", "foo", AutonomyModeStandard} {
		if !strings.Contains(joined, want) {
			t.Errorf("unknown-value warning %q must mention %q", joined, want)
		}
	}
}

// TestAutonomyMode_RoundTripDropsLegacyKeys verifies the save round trip: a
// legacy config loads (migrated onto the enum), Save rewrites it without the
// legacy smart_approve / silent_mode.enabled keys, and reloading the rewritten
// file preserves the mode with NO further warnings (the migration is one
// time).
func TestAutonomyMode_RoundTripDropsLegacyKeys(t *testing.T) {
	dir := t.TempDir()
	legacyPath := filepath.Join(dir, "legacy.yaml")
	rewrittenPath := filepath.Join(dir, "rewritten.yaml")
	legacy := autonomyBase + `security:
  smart_approve: true
  silent_mode:
    enabled: true
    tool_confirm: {mode: deny}
`
	if err := os.WriteFile(legacyPath, []byte(legacy), 0o644); err != nil {
		t.Fatalf("WriteFile: %v", err)
	}

	// Load: both legacy keys migrate onto the enum (silent wins).
	result, err := LoadWithResult(legacyPath)
	if err != nil {
		t.Fatalf("LoadWithResult() failed: %v", err)
	}
	if got := result.Config.Security.AutonomyMode; got != AutonomyModeSilent {
		t.Fatalf("migrated autonomy_mode = %q, want %q", got, AutonomyModeSilent)
	}
	if len(result.LoadErrors) == 0 {
		t.Fatal("expected a migration warning on the first load")
	}

	// Save: the rewritten file must carry the enum and drop both legacy keys.
	if err := Save(result.Config, rewrittenPath); err != nil {
		t.Fatalf("Save() failed: %v", err)
	}
	data, err := os.ReadFile(rewrittenPath)
	if err != nil {
		t.Fatalf("ReadFile: %v", err)
	}
	var doc map[string]any
	if err := yaml.Unmarshal(data, &doc); err != nil {
		t.Fatalf("yaml.Unmarshal(saved): %v", err)
	}
	sec, _ := doc["security"].(map[string]any)
	if sec == nil {
		t.Fatalf("saved config has no security block:\n%s", data)
	}
	if _, ok := sec["smart_approve"]; ok {
		t.Errorf("saved config still carries the legacy smart_approve key:\n%s", data)
	}
	sm, _ := sec["silent_mode"].(map[string]any)
	if sm == nil {
		t.Fatalf("saved config has no security.silent_mode block:\n%s", data)
	}
	if _, ok := sm["enabled"]; ok {
		t.Errorf("saved config still carries the legacy silent_mode.enabled key:\n%s", data)
	}
	if got := sec["autonomy_mode"]; got != AutonomyModeSilent {
		t.Errorf("saved autonomy_mode = %v, want %q", got, AutonomyModeSilent)
	}

	// Reload: the mode survives and the migration stays quiet.
	reloaded, err := LoadWithResult(rewrittenPath)
	if err != nil {
		t.Fatalf("LoadWithResult(rewritten) failed: %v", err)
	}
	if got := reloaded.Config.Security.AutonomyMode; got != AutonomyModeSilent {
		t.Errorf("reloaded autonomy_mode = %q, want %q", got, AutonomyModeSilent)
	}
	if len(reloaded.LoadErrors) != 0 {
		t.Errorf("rewritten config must load without warnings, got %v", reloaded.LoadErrors)
	}
}
