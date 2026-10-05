package config

import (
	"slices"
	"testing"

	"github.com/v0lka/c0wrk/core/modelprofiles"
)

// TestModelProfilesDefaultAlwaysPresentUnionProtected pins the invariant that the
// shipped default always-present list ∪ the protected ReAct-mandatory tools
// (finish, store_fact, search_facts, ask_user, update_checklist,
// read_attachment, read_skill_resource, tool_result_read; all 8 are also
// pinned by default) is exactly the guaranteed core. The static selection
// (modelprofiles.SelectTools) serves exactly this union — the user's pins, the
// protected tools, and every MCP tool — with no slot budget. bash_exec and
// posh_exec are platform alternatives (only one is registered per host), so
// the list carries 17 names while the effective per-host set is 16.
func TestModelProfilesDefaultAlwaysPresentUnionProtected(t *testing.T) {
	guaranteed := make(map[string]struct{}, 20)
	for _, n := range defaultModelProfilesAlwaysPresent {
		guaranteed[n] = struct{}{}
	}
	for _, n := range modelprofiles.ProtectedToolNames() {
		guaranteed[n] = struct{}{}
	}
	const wantGuaranteed = 17
	if len(guaranteed) != wantGuaranteed {
		names := make([]string, 0, len(guaranteed))
		for n := range guaranteed {
			names = append(names, n)
		}
		slices.Sort(names)
		t.Fatalf("expected %d unique guaranteed tools from the defaults, got %d: %v",
			wantGuaranteed, len(guaranteed), names)
	}
}

// TestModelProfilesPersistDefaultsSeeded verifies the slim persisted section's
// defaults: the master toggle stays off (manual only) and the active profile
// seeds to the model-agnostic "generic" id, so a config without an model_profiles:
// section resolves cleanly with no fallback warning.
func TestModelProfilesPersistDefaultsSeeded(t *testing.T) {
	cfg := &Config{}
	ApplyDefaults(cfg)

	if cfg.ModelProfiles.Enabled {
		t.Error("model_profiles.enabled must default to false (manual only)")
	}
	if got := cfg.ModelProfiles.ActiveProfile; got != ModelProfilesGenericProfileID {
		t.Errorf("model_profiles.active_profile default = %q, want %q", got, ModelProfilesGenericProfileID)
	}
}

// TestResolveModelProfilesConfig_GenericProfileDefaults verifies the effective defaults
// a fresh config resolves to through the generic profile — the researched
// variant values that used to be seeded by ApplyDefaults (reasoning_effort
// "medium", loop thresholds 2/3/3/5/4, context 6/5/80/2/16384, the default
// always-present list) now live in the catalog and must still arrive intact.
// The context variant defaults are tighter than the general executor
// baselines (10 / 7 / 85 / 3); the reserve is deliberately LARGER than the
// general 8192 (see defaults.go history / docs/development/model-profiles-defaults-research.md).
func TestResolveModelProfilesConfig_GenericProfileDefaults(t *testing.T) {
	cfg := &Config{}
	ApplyDefaults(cfg)

	resolved, warnings := ResolveModelProfilesConfig(cfg.ModelProfiles, PredefinedModelProfiles())

	if len(warnings) != 0 {
		t.Errorf("fresh config must resolve without warnings, got %v", warnings)
	}
	if resolved.Sampling.ReasoningEffort != "medium" {
		t.Errorf("resolved sampling.reasoning_effort = %q, want the seeded default %q (docs/development/model-profiles-defaults-research.md, R3)", resolved.Sampling.ReasoningEffort, "medium")
	}
	if got := resolved.LoopHardening; got.RepeatNudgeThreshold != 2 || got.ParseErrorAbortThreshold != 3 ||
		got.FruitlessNudgeThreshold != 3 || got.FruitlessAbortThreshold != 5 || got.SameToolRepeatNudgeThreshold != 4 {
		t.Errorf("resolved loop_hardening thresholds = %+v, want 2/3/3/5/4", got)
	}
	ctx := resolved.Context
	if ctx.Compaction.KeepLast != 6 || ctx.Compaction.BlockSize != 5 || ctx.Compaction.TriggerPercent != 80 ||
		ctx.ToolOutputKeepLastN != 2 || ctx.OutputTokenReserve != 16384 {
		t.Errorf("resolved context values = %+v, want 6/5/80/2/16384", ctx)
	}
	if !slices.Equal(resolved.EssentialTools.AlwaysPresent, defaultModelProfilesAlwaysPresent) {
		t.Errorf("resolved always_present = %v, want the shipped default list", resolved.EssentialTools.AlwaysPresent)
	}
}

// TestResolveModelProfilesConfig_ProfileLookup verifies the resolver's three
// resolution paths against a catalog of predefined ∪ custom entries:
// a predefined id returns that profile's values, a custom id returns the
// custom values (explicit values preserved verbatim — the former
// "explicit value wins over the seed" guard), and the master Enabled flag is
// carried over from the persist section untouched.
func TestResolveModelProfilesConfig_ProfileLookup(t *testing.T) {
	customCfg := ModelProfileConfig{}
	customCfg.Context.OutputTokenReserve = 8192 // explicit value must survive resolution
	customCfg.Sampling.PresencePenalty = 1.5
	custom, err := NewModelProfile("my-tuned", "My Tuned", ModelProfileKindCustom, customCfg)
	if err != nil {
		t.Fatalf("NewModelProfile: %v", err)
	}
	catalog := append(PredefinedModelProfiles(), custom)

	// Predefined id → that profile's values.
	predefined, warnings := ResolveModelProfilesConfig(ModelProfilesPersistConfig{Enabled: true, ActiveProfile: "qwen3.8-27b"}, catalog)
	if len(warnings) != 0 {
		t.Errorf("predefined id must resolve without warnings, got %v", warnings)
	}
	if !predefined.Enabled {
		t.Error("master Enabled must carry over from the persist section")
	}
	want, _ := FindPredefinedModelProfile("qwen3.8-27b")
	if !slices.Equal(predefined.EssentialTools.AlwaysPresent, want.Config.EssentialTools.AlwaysPresent) {
		t.Error("predefined profile values did not flow into the resolved config")
	}

	// Custom id → the custom profile's values.
	resolved, warnings := ResolveModelProfilesConfig(ModelProfilesPersistConfig{ActiveProfile: "my-tuned"}, catalog)
	if len(warnings) != 0 {
		t.Errorf("custom id must resolve without warnings, got %v", warnings)
	}
	if got := resolved.Context.OutputTokenReserve; got != 8192 {
		t.Errorf("explicit custom output_token_reserve = %d, want the preserved 8192", got)
	}
	if got := resolved.Sampling.PresencePenalty; got != 1.5 {
		t.Errorf("custom presence_penalty = %v, want 1.5", got)
	}
}

// TestResolveModelProfilesConfig_FallbackToGeneric verifies the soft fallback: an
// empty or unknown active_profile id resolves to the generic profile and
// yields exactly one warning each — a stale id (e.g. a custom profile
// deleted by hand) must never break the run.
func TestResolveModelProfilesConfig_FallbackToGeneric(t *testing.T) {
	generic, _ := FindPredefinedModelProfile(ModelProfilesGenericProfileID)

	for _, tc := range []struct {
		name string
		id   string
	}{
		{name: "unknown id", id: "deleted-externally"},
		{name: "empty id", id: ""},
	} {
		t.Run(tc.name, func(t *testing.T) {
			resolved, warnings := ResolveModelProfilesConfig(ModelProfilesPersistConfig{ActiveProfile: tc.id}, PredefinedModelProfiles())
			if len(warnings) != 1 {
				t.Fatalf("warnings = %v, want exactly one fallback warning", warnings)
			}
			if resolved.Context.OutputTokenReserve != generic.Config.Context.OutputTokenReserve {
				t.Errorf("fallback did not resolve to the generic profile values: %+v", resolved.Context)
			}
		})
	}
}

// TestResolveModelProfilesConfig_RetiredPredefinedID pins the alias resolution
// for the one retired predefined id: intermediate development builds compiled
// the embedded profile in as "bonsai-2-27b" before the dotted rename, and a
// config.yaml whose active_profile was selected against that spelling must
// keep resolving to the bonsai profile (with a visible remap warning) instead
// of dangling into the generic fallback. The alias is consulted only after a
// direct lookup fails, so a custom profile that owns the retired id — exactly
// the pre-bonsai "Bonsai 2 27B" case the dot protects (see
// TestPredefinedBonsaiIDCannotCollideWithLegacyCustom) — still wins outright.
func TestResolveModelProfilesConfig_RetiredPredefinedID(t *testing.T) {
	t.Run("retired id resolves to the renamed profile with one warning", func(t *testing.T) {
		resolved, warnings := ResolveModelProfilesConfig(
			ModelProfilesPersistConfig{ActiveProfile: "bonsai-2-27b"}, PredefinedModelProfiles())
		if len(warnings) != 1 {
			t.Fatalf("warnings = %v, want exactly one remap warning", warnings)
		}
		want := "model_profiles.active_profile \"bonsai-2-27b\" is a retired predefined id; resolving to the \"bonsai.2-27b\" profile"
		if warnings[0] != want {
			t.Errorf("warning = %q, want %q", warnings[0], want)
		}
		bonsai, _ := FindPredefinedModelProfile("bonsai.2-27b")
		if !resolved.SystemPrompt.Lite || resolved.Context.OutputTokenReserve != bonsai.Config.Context.OutputTokenReserve {
			t.Errorf("retired id did not resolve to the bonsai.2-27b values: %+v", resolved)
		}
	})

	t.Run("a custom profile owning the retired id beats the alias", func(t *testing.T) {
		customCfg := ModelProfileConfig{}
		customCfg.Context.OutputTokenReserve = 4096 // marker: neither the zero value nor bonsai's 16384
		custom, err := NewModelProfile("bonsai-2-27b", "Bonsai 2 27B", ModelProfileKindCustom, customCfg)
		if err != nil {
			t.Fatalf("NewModelProfile: %v", err)
		}
		resolved, warnings := ResolveModelProfilesConfig(
			ModelProfilesPersistConfig{ActiveProfile: "bonsai-2-27b"}, append(PredefinedModelProfiles(), custom))
		if len(warnings) != 0 {
			t.Errorf("warnings = %v, want none — the custom profile owns the id outright", warnings)
		}
		if got := resolved.Context.OutputTokenReserve; got != 4096 {
			t.Errorf("output_token_reserve = %d, want the custom profile's 4096 (the alias must not hijack a custom id)", got)
		}
	})
}
