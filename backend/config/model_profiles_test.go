package config

import (
	"reflect"
	"slices"
	"testing"
)

// goldenModelProfilesTools is the literal always-present tool list. The golden test
// pins the exact catalog bytes: any drift in defaultModelProfilesAlwaysPresent (or in
// the catalog) must surface here and be updated consciously.
var goldenModelProfilesTools = []string{
	"read_file",
	"read_attachment",
	"read_skill_resource",
	"tool_result_read",
	"write_file",
	"edit_file",
	"list_directory",
	"glob",
	"ripgrep",
	"bash_exec",
	"posh_exec",
	"semantic_search",
	"store_fact",
	"search_facts",
	"update_checklist",
	"ask_user",
	"finish",
}

// goldenModelProfiles fixes the exact catalog: 6 profiles, fixed ids, names,
// kinds, and all 25 knob values each. Sources: four-model study (2026-09-11),
// the embedded-local-model prefill/token cost measurements (bonsai.2-27b), and
// the docs/development/model-profiles-defaults-research.md addendum (generic).
func goldenModelProfiles() []ModelProfile {
	return []ModelProfile{
		{
			ID:   "qwen3.8-27b",
			Name: "Qwen3.8-27B (minimal)",
			Kind: ModelProfileKindPredefined,
			Config: ModelProfileConfig{
				EssentialTools: EssentialToolsConfig{
					Enabled:             false,
					AlwaysPresent:       goldenModelProfilesTools,
					CompactDescriptions: false,
				},
				SystemPrompt: SystemPromptConfig{
					Lite:              false,
					FewShot:           false,
					ReasoningScaffold: false,
				},
				Sampling: ModelProfilesSamplingConfig{
					Enabled:           true,
					Temperature:       0,
					TopP:              0,
					TopK:              0,
					RepetitionPenalty: 0,
					PresencePenalty:   0,
					ReasoningEffort:   "medium",
				},
				LoopHardening: LoopHardeningConfig{
					Enabled:                      true,
					RepeatNudgeThreshold:         2,
					ParseErrorAbortThreshold:     3,
					FruitlessNudgeThreshold:      3,
					FruitlessAbortThreshold:      5,
					SameToolRepeatNudgeThreshold: 4,
				},
				Context: ModelProfilesContextConfig{
					Enabled: false,
					Compaction: ModelProfilesCompactionConfig{
						KeepLast:       8,
						BlockSize:      5,
						TriggerPercent: 85,
					},
					ToolOutputKeepLastN: 3,
					OutputTokenReserve:  16384,
				},
			},
		},
		{
			ID:   "qwen3.6-35b-a3b",
			Name: "Qwen3.6-35B-A3B (medium)",
			Kind: ModelProfileKindPredefined,
			Config: ModelProfileConfig{
				EssentialTools: EssentialToolsConfig{
					Enabled:             true,
					AlwaysPresent:       goldenModelProfilesTools,
					CompactDescriptions: true,
				},
				SystemPrompt: SystemPromptConfig{
					Lite:              true,
					FewShot:           false,
					ReasoningScaffold: false,
				},
				Sampling: ModelProfilesSamplingConfig{
					Enabled:           true,
					Temperature:       0,
					TopP:              0,
					TopK:              0,
					RepetitionPenalty: 0,
					PresencePenalty:   1.5,
					ReasoningEffort:   "medium",
				},
				LoopHardening: LoopHardeningConfig{
					Enabled:                      true,
					RepeatNudgeThreshold:         2,
					ParseErrorAbortThreshold:     3,
					FruitlessNudgeThreshold:      3,
					FruitlessAbortThreshold:      5,
					SameToolRepeatNudgeThreshold: 4,
				},
				Context: ModelProfilesContextConfig{
					Enabled: true,
					Compaction: ModelProfilesCompactionConfig{
						KeepLast:       6,
						BlockSize:      5,
						TriggerPercent: 80,
					},
					ToolOutputKeepLastN: 2,
					OutputTokenReserve:  16384,
				},
			},
		},
		{
			ID:   "gemma-4-26b-a4b-it",
			Name: "Gemma-4-26B-A4B-it (maximal)",
			Kind: ModelProfileKindPredefined,
			Config: ModelProfileConfig{
				EssentialTools: EssentialToolsConfig{
					Enabled:             true,
					AlwaysPresent:       goldenModelProfilesTools,
					CompactDescriptions: true,
				},
				SystemPrompt: SystemPromptConfig{
					Lite:              true,
					FewShot:           false,
					ReasoningScaffold: false,
				},
				Sampling: ModelProfilesSamplingConfig{
					Enabled:           true,
					Temperature:       0,
					TopP:              0.95,
					TopK:              64,
					RepetitionPenalty: 0,
					PresencePenalty:   0,
					ReasoningEffort:   "",
				},
				LoopHardening: LoopHardeningConfig{
					Enabled:                      true,
					RepeatNudgeThreshold:         2,
					ParseErrorAbortThreshold:     3,
					FruitlessNudgeThreshold:      3,
					FruitlessAbortThreshold:      5,
					SameToolRepeatNudgeThreshold: 4,
				},
				Context: ModelProfilesContextConfig{
					Enabled: true,
					Compaction: ModelProfilesCompactionConfig{
						KeepLast:       6,
						BlockSize:      5,
						TriggerPercent: 80,
					},
					ToolOutputKeepLastN: 2,
					OutputTokenReserve:  16384,
				},
			},
		},
		{
			ID:   "gemma-4-31b-it",
			Name: "Gemma-4-31B-it (moderate)",
			Kind: ModelProfileKindPredefined,
			Config: ModelProfileConfig{
				EssentialTools: EssentialToolsConfig{
					Enabled:             true,
					AlwaysPresent:       goldenModelProfilesTools,
					CompactDescriptions: false,
				},
				SystemPrompt: SystemPromptConfig{
					Lite:              false,
					FewShot:           false,
					ReasoningScaffold: false,
				},
				Sampling: ModelProfilesSamplingConfig{
					Enabled:           true,
					Temperature:       0,
					TopP:              0.95,
					TopK:              64,
					RepetitionPenalty: 0,
					PresencePenalty:   0,
					ReasoningEffort:   "",
				},
				LoopHardening: LoopHardeningConfig{
					Enabled:                      true,
					RepeatNudgeThreshold:         2,
					ParseErrorAbortThreshold:     3,
					FruitlessNudgeThreshold:      3,
					FruitlessAbortThreshold:      5,
					SameToolRepeatNudgeThreshold: 4,
				},
				Context: ModelProfilesContextConfig{
					Enabled: true,
					Compaction: ModelProfilesCompactionConfig{
						KeepLast:       8,
						BlockSize:      5,
						TriggerPercent: 85,
					},
					ToolOutputKeepLastN: 3,
					OutputTokenReserve:  16384,
				},
			},
		},
		{
			ID:   "bonsai.2-27b",
			Name: "Bonsai 2 27B (embedded)",
			Kind: ModelProfileKindPredefined,
			Config: ModelProfileConfig{
				EssentialTools: EssentialToolsConfig{
					Enabled:             true,
					AlwaysPresent:       goldenModelProfilesTools,
					CompactDescriptions: true,
				},
				SystemPrompt: SystemPromptConfig{
					Lite:              true,
					FewShot:           false,
					ReasoningScaffold: false,
				},
				Sampling: ModelProfilesSamplingConfig{
					Enabled:           true,
					Temperature:       0,
					TopP:              0,
					TopK:              0,
					RepetitionPenalty: 0,
					PresencePenalty:   0,
					ReasoningEffort:   "medium",
				},
				LoopHardening: LoopHardeningConfig{
					Enabled:                      true,
					RepeatNudgeThreshold:         2,
					ParseErrorAbortThreshold:     3,
					FruitlessNudgeThreshold:      3,
					FruitlessAbortThreshold:      5,
					SameToolRepeatNudgeThreshold: 4,
				},
				Context: ModelProfilesContextConfig{
					Enabled: true,
					Compaction: ModelProfilesCompactionConfig{
						KeepLast:       6,
						BlockSize:      5,
						TriggerPercent: 80,
					},
					ToolOutputKeepLastN: 2,
					OutputTokenReserve:  16384,
				},
			},
		},
		{
			ID:   ModelProfilesGenericProfileID,
			Name: "Generic (model-agnostic)",
			Kind: ModelProfileKindPredefined,
			Config: ModelProfileConfig{
				EssentialTools: EssentialToolsConfig{
					Enabled:             true,
					AlwaysPresent:       goldenModelProfilesTools,
					CompactDescriptions: true,
				},
				SystemPrompt: SystemPromptConfig{
					Lite:              true,
					FewShot:           false,
					ReasoningScaffold: false,
				},
				Sampling: ModelProfilesSamplingConfig{
					Enabled:           true,
					Temperature:       0,
					TopP:              0,
					TopK:              0,
					RepetitionPenalty: 0,
					PresencePenalty:   0,
					ReasoningEffort:   "medium",
				},
				LoopHardening: LoopHardeningConfig{
					Enabled:                      true,
					RepeatNudgeThreshold:         2,
					ParseErrorAbortThreshold:     3,
					FruitlessNudgeThreshold:      3,
					FruitlessAbortThreshold:      5,
					SameToolRepeatNudgeThreshold: 4,
				},
				Context: ModelProfilesContextConfig{
					Enabled: true,
					Compaction: ModelProfilesCompactionConfig{
						KeepLast:       6,
						BlockSize:      5,
						TriggerPercent: 80,
					},
					ToolOutputKeepLastN: 2,
					OutputTokenReserve:  16384,
				},
			},
		},
	}
}

func TestPredefinedModelProfilesGolden(t *testing.T) {
	want := goldenModelProfiles()
	got := PredefinedModelProfiles()
	if len(got) != len(want) {
		t.Fatalf("catalog size = %d, want %d", len(got), len(want))
	}
	for i := range want {
		w, g := want[i], got[i]
		if g.ID != w.ID {
			t.Errorf("profile[%d].ID = %q, want %q", i, g.ID, w.ID)
		}
		if g.Name != w.Name {
			t.Errorf("profile %q: Name = %q, want %q", w.ID, g.Name, w.Name)
		}
		if g.Kind != w.Kind {
			t.Errorf("profile %q: Kind = %q, want %q", w.ID, g.Kind, w.Kind)
		}
		if !reflect.DeepEqual(g.Config.EssentialTools, w.Config.EssentialTools) {
			t.Errorf("profile %q: essential_tools = %+v, want %+v", w.ID, g.Config.EssentialTools, w.Config.EssentialTools)
		}
		if !reflect.DeepEqual(g.Config.SystemPrompt, w.Config.SystemPrompt) {
			t.Errorf("profile %q: system_prompt = %+v, want %+v", w.ID, g.Config.SystemPrompt, w.Config.SystemPrompt)
		}
		if !reflect.DeepEqual(g.Config.Sampling, w.Config.Sampling) {
			t.Errorf("profile %q: sampling = %+v, want %+v", w.ID, g.Config.Sampling, w.Config.Sampling)
		}
		if !reflect.DeepEqual(g.Config.LoopHardening, w.Config.LoopHardening) {
			t.Errorf("profile %q: loop_hardening = %+v, want %+v", w.ID, g.Config.LoopHardening, w.Config.LoopHardening)
		}
		if !reflect.DeepEqual(g.Config.Context, w.Config.Context) {
			t.Errorf("profile %q: context = %+v, want %+v", w.ID, g.Config.Context, w.Config.Context)
		}
	}
}

func TestPredefinedModelProfilesCatalog(t *testing.T) {
	wantIDs := []string{"qwen3.8-27b", "qwen3.6-35b-a3b", "gemma-4-26b-a4b-it", "gemma-4-31b-it", "bonsai.2-27b", "generic"}
	profiles := PredefinedModelProfiles()
	if len(profiles) != len(wantIDs) {
		t.Fatalf("catalog has %d profiles, want exactly %d", len(profiles), len(wantIDs))
	}
	for i, p := range profiles {
		if p.ID != wantIDs[i] {
			t.Errorf("profile[%d].ID = %q, want %q", i, p.ID, wantIDs[i])
		}
		if p.Kind != ModelProfileKindPredefined {
			t.Errorf("profile %q: Kind = %q, want %q", p.ID, p.Kind, ModelProfileKindPredefined)
		}
		if p.Name == "" {
			t.Errorf("profile %q: Name must not be empty", p.ID)
		}
		if err := ValidateModelProfileConfig(p.Config); err != nil {
			t.Errorf("profile %q: values do not pass validation: %v", p.ID, err)
		}
		if _, err := NewModelProfile(p.ID, p.Name, p.Kind, p.Config); err != nil {
			t.Errorf("profile %q: must round-trip through the constructor: %v", p.ID, err)
		}
		if !slices.Equal(p.Config.EssentialTools.AlwaysPresent, defaultModelProfilesAlwaysPresent) {
			t.Errorf("profile %q: always_present must equal the built-in default list", p.ID)
		}
	}
	if err := ValidateModelProfilesUnique(profiles); err != nil {
		t.Errorf("catalog must have unique ids and names: %v", err)
	}
}

func TestValidateModelProfilesUniqueRejectsDuplicates(t *testing.T) {
	base := PredefinedModelProfiles()
	if err := ValidateModelProfilesUnique(base); err != nil {
		t.Fatalf("clean catalog must pass, got %v", err)
	}
	if err := ValidateModelProfilesUnique(nil); err != nil {
		t.Errorf("nil list must pass, got %v", err)
	}

	dupID := slices.Clone(base)
	dupID[len(dupID)-1].ID = dupID[0].ID
	if err := ValidateModelProfilesUnique(dupID); err == nil {
		t.Error("duplicate id must be rejected")
	}

	dupName := slices.Clone(base)
	dupName[len(dupName)-1].Name = dupName[0].Name
	if err := ValidateModelProfilesUnique(dupName); err == nil {
		t.Error("duplicate name must be rejected")
	}
}

func TestNewModelProfileValidation(t *testing.T) {
	base, ok := FindPredefinedModelProfile(ModelProfilesGenericProfileID)
	if !ok {
		t.Fatal("generic profile must exist in the catalog")
	}

	cases := []struct {
		name     string
		id       string
		profName string
		kind     ModelProfileKind
		mutate   func(*ModelProfileConfig)
		wantErr  bool
	}{
		{name: "valid custom", id: "my-profile", profName: "My Profile", kind: ModelProfileKindCustom, wantErr: false},
		{name: "valid id with dots", id: "qwen3.8-27b-copy", profName: "Copy", kind: ModelProfileKindCustom, wantErr: false},
		{name: "generic config re-wrapped", id: base.ID, profName: base.Name, kind: base.Kind, wantErr: false},
		{name: "empty id", id: "", profName: "N", kind: ModelProfileKindCustom, wantErr: true},
		{name: "id trimmed to empty", id: "   ", profName: "N", kind: ModelProfileKindCustom, wantErr: true},
		{name: "uppercase id", id: "My-Profile", profName: "N", kind: ModelProfileKindCustom, wantErr: true},
		{name: "spaces in id", id: "my profile", profName: "N", kind: ModelProfileKindCustom, wantErr: true},
		{name: "leading dot", id: ".my", profName: "N", kind: ModelProfileKindCustom, wantErr: true},
		{name: "trailing hyphen", id: "my-", profName: "N", kind: ModelProfileKindCustom, wantErr: true},
		{name: "underscore id", id: "my_profile", profName: "N", kind: ModelProfileKindCustom, wantErr: true},
		{name: "empty name", id: "my", profName: "   ", kind: ModelProfileKindCustom, wantErr: true},
		{name: "unknown kind", id: "my", profName: "N", kind: "weird", wantErr: true},
		{name: "empty kind", id: "my", profName: "N", kind: "", wantErr: true},
		{name: "name is trimmed", id: "my", profName: "  N  ", kind: ModelProfileKindCustom, wantErr: false},

		{name: "temperature zero ok", id: "my", profName: "N", kind: ModelProfileKindCustom,
			mutate: func(c *ModelProfileConfig) { c.Sampling.Temperature = 0 }, wantErr: false},
		{name: "temperature negative", id: "my", profName: "N", kind: ModelProfileKindCustom,
			mutate: func(c *ModelProfileConfig) { c.Sampling.Temperature = -0.1 }, wantErr: true},
		{name: "top_p 1 ok", id: "my", profName: "N", kind: ModelProfileKindCustom,
			mutate: func(c *ModelProfileConfig) { c.Sampling.TopP = 1 }, wantErr: false},
		{name: "top_p above 1", id: "my", profName: "N", kind: ModelProfileKindCustom,
			mutate: func(c *ModelProfileConfig) { c.Sampling.TopP = 1.5 }, wantErr: true},
		{name: "top_p negative", id: "my", profName: "N", kind: ModelProfileKindCustom,
			mutate: func(c *ModelProfileConfig) { c.Sampling.TopP = -0.5 }, wantErr: true},
		{name: "top_k negative", id: "my", profName: "N", kind: ModelProfileKindCustom,
			mutate: func(c *ModelProfileConfig) { c.Sampling.TopK = -1 }, wantErr: true},
		{name: "repetition_penalty 1 ok", id: "my", profName: "N", kind: ModelProfileKindCustom,
			mutate: func(c *ModelProfileConfig) { c.Sampling.RepetitionPenalty = 1 }, wantErr: false},
		{name: "repetition_penalty below 1", id: "my", profName: "N", kind: ModelProfileKindCustom,
			mutate: func(c *ModelProfileConfig) { c.Sampling.RepetitionPenalty = 0.5 }, wantErr: true},
		{name: "repetition_penalty above 2", id: "my", profName: "N", kind: ModelProfileKindCustom,
			mutate: func(c *ModelProfileConfig) { c.Sampling.RepetitionPenalty = 2.1 }, wantErr: true},
		{name: "presence_penalty 2 ok", id: "my", profName: "N", kind: ModelProfileKindCustom,
			mutate: func(c *ModelProfileConfig) { c.Sampling.PresencePenalty = 2 }, wantErr: false},
		{name: "presence_penalty above 2", id: "my", profName: "N", kind: ModelProfileKindCustom,
			mutate: func(c *ModelProfileConfig) { c.Sampling.PresencePenalty = 2.5 }, wantErr: true},
		{name: "presence_penalty negative", id: "my", profName: "N", kind: ModelProfileKindCustom,
			mutate: func(c *ModelProfileConfig) { c.Sampling.PresencePenalty = -0.1 }, wantErr: true},
		{name: "effort off ok", id: "my", profName: "N", kind: ModelProfileKindCustom,
			mutate: func(c *ModelProfileConfig) { c.Sampling.ReasoningEffort = "off" }, wantErr: false},
		{name: "effort low ok", id: "my", profName: "N", kind: ModelProfileKindCustom,
			mutate: func(c *ModelProfileConfig) { c.Sampling.ReasoningEffort = "low" }, wantErr: false},
		{name: "effort empty ok", id: "my", profName: "N", kind: ModelProfileKindCustom,
			mutate: func(c *ModelProfileConfig) { c.Sampling.ReasoningEffort = "" }, wantErr: false},
		{name: "effort xhigh rejected", id: "my", profName: "N", kind: ModelProfileKindCustom,
			mutate: func(c *ModelProfileConfig) { c.Sampling.ReasoningEffort = "xhigh" }, wantErr: true},
		{name: "range checked even when variant disabled", id: "my", profName: "N", kind: ModelProfileKindCustom,
			mutate: func(c *ModelProfileConfig) { c.Sampling.Enabled = false; c.Sampling.TopP = 1.5 }, wantErr: true},

		{name: "loop threshold zero when enabled", id: "my", profName: "N", kind: ModelProfileKindCustom,
			mutate: func(c *ModelProfileConfig) { c.LoopHardening.RepeatNudgeThreshold = 0 }, wantErr: true},
		{name: "loop threshold zero when disabled ok", id: "my", profName: "N", kind: ModelProfileKindCustom,
			mutate: func(c *ModelProfileConfig) { c.LoopHardening.Enabled = false; c.LoopHardening.RepeatNudgeThreshold = 0 }, wantErr: false},
		{name: "loop threshold negative when disabled", id: "my", profName: "N", kind: ModelProfileKindCustom,
			mutate: func(c *ModelProfileConfig) {
				c.LoopHardening.Enabled = false
				c.LoopHardening.FruitlessAbortThreshold = -1
			}, wantErr: true},

		{name: "keep_last 1 when context enabled", id: "my", profName: "N", kind: ModelProfileKindCustom,
			mutate: func(c *ModelProfileConfig) { c.Context.Compaction.KeepLast = 1 }, wantErr: true},
		{name: "keep_last 2 ok", id: "my", profName: "N", kind: ModelProfileKindCustom,
			mutate: func(c *ModelProfileConfig) { c.Context.Compaction.KeepLast = 2 }, wantErr: false},
		{name: "block_size 1 when context enabled", id: "my", profName: "N", kind: ModelProfileKindCustom,
			mutate: func(c *ModelProfileConfig) { c.Context.Compaction.BlockSize = 1 }, wantErr: true},
		{name: "trigger 100 when context enabled", id: "my", profName: "N", kind: ModelProfileKindCustom,
			mutate: func(c *ModelProfileConfig) { c.Context.Compaction.TriggerPercent = 100 }, wantErr: true},
		{name: "trigger 99 ok", id: "my", profName: "N", kind: ModelProfileKindCustom,
			mutate: func(c *ModelProfileConfig) { c.Context.Compaction.TriggerPercent = 99 }, wantErr: false},
		{name: "keep_n 0 when context enabled", id: "my", profName: "N", kind: ModelProfileKindCustom,
			mutate: func(c *ModelProfileConfig) { c.Context.ToolOutputKeepLastN = 0 }, wantErr: true},
		{name: "reserve 0 when context enabled", id: "my", profName: "N", kind: ModelProfileKindCustom,
			mutate: func(c *ModelProfileConfig) { c.Context.OutputTokenReserve = 0 }, wantErr: true},
		{name: "context disabled relaxes window rules", id: "my", profName: "N", kind: ModelProfileKindCustom,
			mutate: func(c *ModelProfileConfig) { c.Context.Enabled = false; c.Context.ToolOutputKeepLastN = 0 }, wantErr: false},
		{name: "negative context value when disabled", id: "my", profName: "N", kind: ModelProfileKindCustom,
			mutate: func(c *ModelProfileConfig) { c.Context.Enabled = false; c.Context.OutputTokenReserve = -1 }, wantErr: true},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			cfg := base.Config
			if tc.mutate != nil {
				tc.mutate(&cfg)
			}
			p, err := NewModelProfile(tc.id, tc.profName, tc.kind, cfg)
			if tc.wantErr && err == nil {
				t.Fatalf("NewModelProfile(%q, %q, %q, …) = %+v, want error", tc.id, tc.profName, tc.kind, p)
			}
			if !tc.wantErr && err != nil {
				t.Fatalf("NewModelProfile(%q, %q, %q, …) unexpected error: %v", tc.id, tc.profName, tc.kind, err)
			}
		})
	}
}

func TestPredefinedModelProfilesDefensiveCopies(t *testing.T) {
	got := PredefinedModelProfiles()
	got[0].ID = "tampered"
	got[0].Config.EssentialTools.AlwaysPresent[0] = "tampered"

	again := PredefinedModelProfiles()
	if again[0].ID == "tampered" {
		t.Error("PredefinedModelProfiles must return defensive copies")
	}
	if again[0].Config.EssentialTools.AlwaysPresent[0] == "tampered" {
		t.Error("PredefinedModelProfiles must deep-copy always_present")
	}

	p, ok := FindPredefinedModelProfile(ModelProfilesGenericProfileID)
	if !ok {
		t.Fatal("generic profile must resolve")
	}
	p.Config.EssentialTools.AlwaysPresent[0] = "tampered"
	p2, ok := FindPredefinedModelProfile(ModelProfilesGenericProfileID)
	if !ok {
		t.Fatal("generic profile must resolve on second lookup")
	}
	if p2.Config.EssentialTools.AlwaysPresent[0] == "tampered" {
		t.Error("FindPredefinedModelProfile must return defensive copies")
	}

	if _, ok := FindPredefinedModelProfile("does-not-exist"); ok {
		t.Error("unknown id must not resolve")
	}
}

// TestModelProfileConfigExposes25Knobs guards the profile contract: exactly
// 25 leaf knobs. Adding or removing a knob changes the contract and must
// consciously update the golden test and the research addendum.
func TestModelProfileConfigExposes25Knobs(t *testing.T) {
	var countLeafFields func(v reflect.Value) int
	countLeafFields = func(v reflect.Value) int {
		if v.Kind() == reflect.Struct {
			n := 0
			for i := range v.NumField() {
				n += countLeafFields(v.Field(i))
			}
			return n
		}
		return 1
	}
	got := countLeafFields(reflect.ValueOf(ModelProfileConfig{}))
	if got != 25 {
		t.Fatalf("ModelProfileConfig exposes %d leaf knobs, want exactly 25", got)
	}
}
