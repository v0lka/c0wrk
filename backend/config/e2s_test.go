package config

import (
	"strings"
	"testing"

	"github.com/v0lka/c0wrk/core/e2s"

	"gopkg.in/yaml.v3"
)

// TestE2SDefaultsSeeded verifies ApplyDefaults seeds the E2S section (zero →
// default). The section has no master toggle — the mode is gated solely by
// experimental.enabled — so only the numeric knobs are seeded.
func TestE2SDefaultsSeeded(t *testing.T) {
	cfg := &Config{}
	ApplyDefaults(cfg)

	if got := cfg.E2S.MaxSteps; got != 50 {
		t.Errorf("e2s.max_steps default = %d, want 50", got)
	}
	if got := cfg.E2S.StateByteLimit; got != e2s.DefaultStateByteLimit {
		t.Errorf("e2s.state_byte_limit default = %d, want %d (core/e2s.DefaultStateByteLimit)", got, e2s.DefaultStateByteLimit)
	}
	if got := cfg.E2S.PatchRetries; got != 1 {
		t.Errorf("e2s.patch_retries default = %d, want 1", got)
	}
	if got := cfg.E2S.ObservationTruncate; got != 8000 {
		t.Errorf("e2s.observation_truncate default = %d, want 8000", got)
	}
	if got := cfg.E2S.ObservationBudgetTokens; got != 8192 {
		t.Errorf("e2s.observation_budget_tokens default = %d, want 8192", got)
	}
	if got := cfg.E2S.ObservationFillFraction; got != 0.4 {
		t.Errorf("e2s.observation_fill_fraction default = %v, want 0.4", got)
	}
	if got := cfg.E2S.RepeatNudgeThreshold; got != 3 {
		t.Errorf("e2s.repeat_nudge_threshold default = %d, want 3", got)
	}
	if got := cfg.E2S.RepeatAbortThreshold; got != 5 {
		t.Errorf("e2s.repeat_abort_threshold default = %d, want 5", got)
	}
}

// TestE2SExplicitValuesPreserved pins the seeding guard: explicit non-zero
// values from YAML must never be overwritten by the defaults.
func TestE2SExplicitValuesPreserved(t *testing.T) {
	cfg := &Config{
		E2S: E2SConfig{
			MaxSteps:                12,
			StateByteLimit:          4096,
			PatchRetries:            3,
			ObservationTruncate:     512,
			ObservationBudgetTokens: 1024,
			ObservationFillFraction: 0.25,
			RepeatNudgeThreshold:    2,
			RepeatAbortThreshold:    4,
		},
	}
	ApplyDefaults(cfg)

	if cfg.E2S.MaxSteps != 12 || cfg.E2S.StateByteLimit != 4096 || cfg.E2S.PatchRetries != 3 ||
		cfg.E2S.ObservationTruncate != 512 || cfg.E2S.ObservationBudgetTokens != 1024 ||
		cfg.E2S.ObservationFillFraction != 0.25 || cfg.E2S.RepeatNudgeThreshold != 2 ||
		cfg.E2S.RepeatAbortThreshold != 4 {
		t.Errorf("explicit e2s values overwritten by ApplyDefaults: %+v", cfg.E2S)
	}
}

// TestE2SYAMLParsing verifies the e2s: section parses from YAML into the
// typed struct with defaults still seeding unset fields.
func TestE2SYAMLParsing(t *testing.T) {
	raw := `
e2s:
  max_steps: 25
  state_byte_limit: 8192
  observation_budget_tokens: 4096
  observation_fill_fraction: 0.25
`
	var cfg Config
	if err := yaml.Unmarshal([]byte(raw), &cfg); err != nil {
		t.Fatalf("unmarshal: %v", err)
	}
	ApplyDefaults(&cfg)

	if cfg.E2S.MaxSteps != 25 {
		t.Errorf("e2s.max_steps = %d, want 25", cfg.E2S.MaxSteps)
	}
	if cfg.E2S.StateByteLimit != 8192 {
		t.Errorf("e2s.state_byte_limit = %d, want 8192", cfg.E2S.StateByteLimit)
	}
	if cfg.E2S.ObservationBudgetTokens != 4096 {
		t.Errorf("e2s.observation_budget_tokens = %d, want 4096", cfg.E2S.ObservationBudgetTokens)
	}
	if cfg.E2S.ObservationFillFraction != 0.25 {
		t.Errorf("e2s.observation_fill_fraction = %v, want 0.25", cfg.E2S.ObservationFillFraction)
	}
	// Unset fields keep their defaults.
	if cfg.E2S.PatchRetries != 1 || cfg.E2S.ObservationTruncate != 8000 {
		t.Errorf("unset e2s fields lost their defaults: %+v", cfg.E2S)
	}
}

// minimalValidConfig builds the smallest config that passes validate() (the
// LLM section must resolve a default model), so the e2s validation rules can
// be exercised in isolation.
func minimalValidConfig() *Config {
	cfg := &Config{}
	ApplyDefaults(cfg)
	cfg.LLM.DefaultModel = "claude-3-haiku"
	cfg.LLM.Anthropic = AnthropicConfig{APIKey: "key", Models: []string{"claude-3-haiku"}}
	return cfg
}

// TestE2SNegativeValuesRejected verifies validate() fails fast on negative
// numeric e2s values (only a hand-written YAML can produce them — the
// seeded defaults are positive).
func TestE2SNegativeValuesRejected(t *testing.T) {
	for name, mutate := range map[string]func(*E2SConfig){
		"max_steps":                 func(c *E2SConfig) { c.MaxSteps = -1 },
		"state_byte_limit":          func(c *E2SConfig) { c.StateByteLimit = -100 },
		"patch_retries":             func(c *E2SConfig) { c.PatchRetries = -1 },
		"observation_truncate":      func(c *E2SConfig) { c.ObservationTruncate = -5 },
		"observation_budget_tokens": func(c *E2SConfig) { c.ObservationBudgetTokens = -8 },
		"repeat_nudge_threshold":    func(c *E2SConfig) { c.RepeatNudgeThreshold = -2 },
		"repeat_abort_threshold":    func(c *E2SConfig) { c.RepeatAbortThreshold = -3 },
	} {
		cfg := minimalValidConfig()
		mutate(&cfg.E2S)
		err := validate(cfg)
		if err == nil || !strings.Contains(err.Error(), "e2s."+name) {
			t.Errorf("%s: got %v, want an e2s.%s validation error", name, err, name)
		}
	}

	// The float-valued fill fraction has its own negative check.
	cfg := minimalValidConfig()
	cfg.E2S.ObservationFillFraction = -0.5
	if err := validate(cfg); err == nil || !strings.Contains(err.Error(), "observation_fill_fraction") {
		t.Errorf("got %v, want an e2s.observation_fill_fraction validation error", err)
	}
}

// TestE2SThresholdOrderingRejected verifies the anti-spin invariant: the
// nudge threshold must never exceed the abort threshold, so the model always
// sees at least one corrective nudge before the loop stops.
func TestE2SThresholdOrderingRejected(t *testing.T) {
	cfg := minimalValidConfig()
	cfg.E2S.RepeatNudgeThreshold = 6 // default abort is 5
	if err := validate(cfg); err == nil || !strings.Contains(err.Error(), "repeat_nudge_threshold") {
		t.Fatalf("got %v, want a repeat_nudge_threshold ordering error", err)
	}

	cfg.E2S.RepeatAbortThreshold = 6
	if err := validate(cfg); err != nil {
		t.Fatalf("nudge 6 <= abort 6 must validate, got %v", err)
	}
}

// TestE2SSectionValidByDefault verifies a default-configured e2s section
// survives validate() cleanly.
func TestE2SSectionValidByDefault(t *testing.T) {
	if err := validate(minimalValidConfig()); err != nil {
		t.Fatalf("default e2s section must validate, got %v", err)
	}
}

// TestE2SToolsDefaultsSeeded verifies ApplyDefaults seeds the tool-catalog
// narrowing: the core preset is the default, allow/deny default to empty
// sets.
func TestE2SToolsDefaultsSeeded(t *testing.T) {
	cfg := &Config{}
	ApplyDefaults(cfg)

	if got := cfg.E2S.Tools.Preset; got != E2SToolsPresetCore {
		t.Errorf("e2s.tools.preset default = %q, want %q (core)", got, E2SToolsPresetCore)
	}
	if len(cfg.E2S.Tools.Allow) != 0 || len(cfg.E2S.Tools.Deny) != 0 {
		t.Errorf("e2s.tools allow/deny must default to empty, got allow=%v deny=%v", cfg.E2S.Tools.Allow, cfg.E2S.Tools.Deny)
	}
}

// TestE2SToolsPresetValidation verifies the e2s.tools validation rules: an
// unknown preset is a typo and must fail fast at load (the core filter would
// otherwise silently fall back to core), and allow/deny entries must be
// non-empty tool names.
func TestE2SToolsPresetValidation(t *testing.T) {
	for _, preset := range []string{E2SToolsPresetCore, E2SToolsPresetAll} {
		cfg := minimalValidConfig()
		cfg.E2S.Tools.Preset = preset
		if err := validate(cfg); err != nil {
			t.Errorf("preset %q must validate, got %v", preset, err)
		}
	}

	cfg := minimalValidConfig()
	cfg.E2S.Tools.Preset = "core+"
	if err := validate(cfg); err == nil || !strings.Contains(err.Error(), "e2s.tools.preset") {
		t.Errorf("got %v, want an e2s.tools.preset validation error", err)
	}

	cfg = minimalValidConfig()
	cfg.E2S.Tools.Allow = []string{"web_search", "   "}
	if err := validate(cfg); err == nil || !strings.Contains(err.Error(), "e2s.tools.allow") {
		t.Errorf("got %v, want an e2s.tools.allow validation error", err)
	}

	cfg = minimalValidConfig()
	cfg.E2S.Tools.Deny = []string{""}
	if err := validate(cfg); err == nil || !strings.Contains(err.Error(), "e2s.tools.deny") {
		t.Errorf("got %v, want an e2s.tools.deny validation error", err)
	}
}

// TestE2SToolsYAMLParsing verifies the e2s.tools subsection parses from YAML
// (the documented config surface) with allow/deny lists intact.
func TestE2SToolsYAMLParsing(t *testing.T) {
	yamlSrc := `
e2s:
  max_steps: 7
  tools:
    preset: all
    allow: [semantic_search]
    deny: [web_search, bash_exec]
`
	var cfg Config
	if err := yaml.Unmarshal([]byte(yamlSrc), &cfg); err != nil {
		t.Fatalf("unmarshal: %v", err)
	}
	if cfg.E2S.Tools.Preset != "all" {
		t.Errorf("tools.preset = %q, want all", cfg.E2S.Tools.Preset)
	}
	if len(cfg.E2S.Tools.Allow) != 1 || cfg.E2S.Tools.Allow[0] != "semantic_search" {
		t.Errorf("tools.allow = %v, want [semantic_search]", cfg.E2S.Tools.Allow)
	}
	if len(cfg.E2S.Tools.Deny) != 2 || cfg.E2S.Tools.Deny[0] != "web_search" || cfg.E2S.Tools.Deny[1] != "bash_exec" {
		t.Errorf("tools.deny = %v, want [web_search bash_exec]", cfg.E2S.Tools.Deny)
	}
}
