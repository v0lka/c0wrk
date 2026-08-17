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
// than — or, for the reserve, equal to — the general executor baselines
// (10 / 7 / 85 / 3 / 8192).
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
	if got := cfg.SmallLLM.Context.OutputTokenReserve; got != 8192 {
		t.Errorf("context.output_token_reserve default = %d, want 8192", got)
	}
}
