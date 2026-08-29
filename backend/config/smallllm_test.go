package config

import (
	"slices"
	"testing"

	"github.com/v0lka/c0wrk/core/smallllm"
)

// TestSmallLLMDefaultMaxToolsFitsGuaranteedSet pins the invariant that the
// shipped defaults are self-consistent under the slot-budget semantics: the
// guaranteed set (default always-present ∪ protected orchestration tools; MCP
// tools join at runtime) must fit the default max_tools, otherwise
// validateSmallLLMConfig would reject the defaults out of the box.
func TestSmallLLMDefaultMaxToolsFitsGuaranteedSet(t *testing.T) {
	cfg := &Config{}
	ApplyDefaults(cfg)

	const defaultMaxTools = 16
	if cfg.SmallLLM.EssentialTools.MaxTools != defaultMaxTools {
		t.Fatalf("default small_llm.essential_tools.max_tools = %d, want %d",
			cfg.SmallLLM.EssentialTools.MaxTools, defaultMaxTools)
	}

	// Guaranteed set = default always-present (12) ∪ protected (5, of which
	// 4 overlap with the pins) = 13 unique tools.
	guaranteed := make(map[string]struct{}, 16)
	for _, n := range defaultSmallLLMAlwaysPresent {
		guaranteed[n] = struct{}{}
	}
	for _, n := range smallllm.ProtectedToolNames() {
		guaranteed[n] = struct{}{}
	}
	const wantGuaranteed = 13
	if len(guaranteed) != wantGuaranteed {
		names := make([]string, 0, len(guaranteed))
		for n := range guaranteed {
			names = append(names, n)
		}
		slices.Sort(names)
		t.Fatalf("expected %d unique guaranteed tools from the defaults, got %d: %v",
			wantGuaranteed, len(guaranteed), names)
	}
	if len(guaranteed) > cfg.SmallLLM.EssentialTools.MaxTools {
		t.Errorf("default guaranteed set (%d tools) must fit the default max_tools (%d)",
			len(guaranteed), cfg.SmallLLM.EssentialTools.MaxTools)
	}
}

// TestSmallLLMContextDefaultsSeeded verifies that ApplyDefaults seeds the
// context-management variant defaults (zero → variant value), mirroring the
// other variants: values are visible/editable while the profile itself stays
// a no-op until the toggles are enabled. The variant defaults are tighter
// than the general executor baselines (10 / 7 / 85 / 3); the reserve is
// deliberately LARGER than the general 8192 — it feeds only the router's
// context-window validation fallback for models without a resolved
// OutputLimit (effectively unreachable), and the MaxTokens ceiling itself
// is the resolved per-model/per-provider OutputLimit (see defaults.go).
func TestSmallLLMContextDefaultsSeeded(t *testing.T) {
	cfg := &Config{}
	ApplyDefaults(cfg)

	if cfg.SmallLLM.Context.Enabled {
		t.Error("context variant must default to disabled")
	}
	if got := cfg.SmallLLM.Context.Compaction.KeepLast; got != 6 {
		t.Errorf("context.compaction.keep_last default = %d, want 6", got)
	}
	if got := cfg.SmallLLM.Context.Compaction.BlockSize; got != 5 {
		t.Errorf("context.compaction.block_size default = %d, want 5", got)
	}
	if got := cfg.SmallLLM.Context.Compaction.TriggerPercent; got != 80 {
		t.Errorf("context.compaction.trigger_percent default = %d, want 80", got)
	}
	if got := cfg.SmallLLM.Context.ToolOutputKeepLastN; got != 2 {
		t.Errorf("context.tool_output_keep_last_n default = %d, want 2", got)
	}
	if got := cfg.SmallLLM.Context.OutputTokenReserve; got != 16384 {
		t.Errorf("context.output_token_reserve default = %d, want 16384", got)
	}
}

// TestSmallLLMContextReserveExplicitValuePreserved pins the seeding guard:
// an explicit non-zero output_token_reserve from YAML (e.g. the former 8192
// default, still correct for local 16–32K context windows where a 16384
// reserve would eat half or more of the input budget) must never be
// overwritten by the variant default.
func TestSmallLLMContextReserveExplicitValuePreserved(t *testing.T) {
	cfg := &Config{}
	cfg.SmallLLM.Context.OutputTokenReserve = 8192
	ApplyDefaults(cfg)

	if got := cfg.SmallLLM.Context.OutputTokenReserve; got != 8192 {
		t.Errorf("explicit context.output_token_reserve = %d after ApplyDefaults, want the preserved 8192", got)
	}
}

// TestSmallLLMSamplingReasoningEffortDefaultSeeded verifies that ApplyDefaults
// seeds the sampling variant's reasoning effort to "medium": an unset value
// would otherwise inherit the model's own default, which on qwen thinking
// models is "xhigh" — measured overthinking on trivial tasks (22,276
// reasoning tokens / 21 min for a simple SVG vs 3,715 tokens / 137 s with
// thinking off; docs/small-llm-defaults-research.md, R3). "medium" is the
// model's native pre-training regime with no effort instruction injected.
// Like the other variant values the seed is unconditional (visible/editable
// in the UI) while the profile itself stays a no-op until both toggles are on.
func TestSmallLLMSamplingReasoningEffortDefaultSeeded(t *testing.T) {
	cfg := &Config{}
	ApplyDefaults(cfg)

	if cfg.SmallLLM.Sampling.Enabled {
		t.Error("sampling variant must default to disabled")
	}
	if got := cfg.SmallLLM.Sampling.ReasoningEffort; got != "medium" {
		t.Errorf("sampling.reasoning_effort default = %q, want %q", got, "medium")
	}
}

// TestSmallLLMSamplingReasoningEffortExplicitPreserved pins the seeding guard:
// an explicit non-empty reasoning_effort from YAML must never be overwritten
// by the variant default. ("" is the documented "unset" sentinel — at the
// plain-string type level it is indistinguishable from an absent key — and
// resolves to the seeded "medium"; operators wanting the vendor xhigh inherit
// disable the sampling variant instead.)
func TestSmallLLMSamplingReasoningEffortExplicitPreserved(t *testing.T) {
	for _, explicit := range []string{"off", "low", "medium"} {
		cfg := &Config{}
		cfg.SmallLLM.Sampling.ReasoningEffort = explicit
		ApplyDefaults(cfg)

		if got := cfg.SmallLLM.Sampling.ReasoningEffort; got != explicit {
			t.Errorf("explicit sampling.reasoning_effort %q was overwritten to %q by ApplyDefaults", explicit, got)
		}
	}
}
