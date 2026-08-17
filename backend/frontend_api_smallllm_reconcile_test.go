package backend

import (
	"slices"
	"testing"

	"github.com/v0lka/c0wrk/core/smallllm"
)

// TestUpdateSmallLLMConfig_ReconcilesStaleMaxTools reproduces the locked-out
// settings panel: a config persisted by an older build can carry an
// essential_tools.max_tools below the guaranteed set (always_present ∪
// protected orchestration tools) — the union grows when new tools join the
// protected set while the stored list/cap keep their old values. Before the
// reconciliation, validateSmallLLMConfig rejected EVERY save (including a
// bare master-toggle flip, since the UI persists the full profile) and the
// protected tools are locked chips the user cannot un-pin, so the only
// remedy was hand-editing config.yaml. The update path now raises the cap to
// the guaranteed count before validation and the save self-heals the stale
// config.
func TestUpdateSmallLLMConfig_ReconcilesStaleMaxTools(t *testing.T) {
	f, _, _ := newTestAPI(t)

	cfg := validSmallLLMConfig()
	// The exact stale-config shape found in the wild: a 13-entry
	// always_present list (the 12 shipped defaults plus update_checklist,
	// all 5 protected tools overlapping) with max_tools still at 12 — the
	// union grew past the stored cap across releases. guaranteed = 13 > 12.
	cfg.EssentialTools.AlwaysPresent = []string{
		"read_file", "write_file", "edit_file", "list_directory", "glob",
		"ripgrep", "bash_exec", "semantic_search", "store_fact", "search_facts",
		"ask_user", "finish", "update_checklist",
	}
	cfg.EssentialTools.MaxTools = 12

	if err := f.UpdateSmallLLMConfig(cfg); err != nil {
		t.Fatalf("save must succeed after cap reconciliation, got: %v", err)
	}

	want := len(unionAlwaysPresent(cfg.EssentialTools.AlwaysPresent, smallllm.ProtectedToolNames()))
	if want != 13 {
		t.Fatalf("test premise broken: guaranteed count = %d, want 13", want)
	}
	if got := f.config.SmallLLM.EssentialTools.MaxTools; got != want {
		t.Errorf("in-memory MaxTools = %d, want %d (raised to the guaranteed count)", got, want)
	}
	// The read path must serve the reconciled value too (UI revert-on-failure).
	if got := f.GetSmallLLMConfig().EssentialTools.MaxTools; got != want {
		t.Errorf("GetSmallLLMConfig MaxTools = %d, want %d", got, want)
	}
}

// TestReconcileSmallLLMCap_Passthrough pins the guard rails: the unlimited
// sentinel (0) and negative caps are untouched — validation keeps rejecting
// negatives — and a cap already ≥ the guaranteed set is left alone.
func TestReconcileSmallLLMCap_Passthrough(t *testing.T) {
	base := func() SmallLLMConfigResponse {
		cfg := validSmallLLMConfig()
		cfg.EssentialTools.AlwaysPresent = []string{"read_file"}
		return cfg
	}

	unlimited := base()
	unlimited.EssentialTools.MaxTools = 0
	if r := reconcileSmallLLMCap(&unlimited); r.from != 0 || r.to != 0 {
		t.Errorf("unlimited sentinel must pass through, got %+v", r)
	}

	negative := base()
	negative.EssentialTools.MaxTools = -1
	if r := reconcileSmallLLMCap(&negative); r.from != -1 || r.to != -1 {
		t.Errorf("negative cap must pass through (validation rejects it), got %+v", r)
	}

	healthy := base()
	healthy.EssentialTools.MaxTools = 50
	if r := reconcileSmallLLMCap(&healthy); r.from != 50 || r.to != 50 {
		t.Errorf("cap above the guaranteed set must be unchanged, got %+v", r)
	}
	if !slices.Contains([]int{50}, healthy.EssentialTools.MaxTools) {
		t.Errorf("healthy cap mutated: %d", healthy.EssentialTools.MaxTools)
	}
}
