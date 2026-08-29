package core

import (
	"reflect"
	"testing"

	"github.com/v0lka/sp4rk/agent"
	"github.com/v0lka/sp4rk/llm"
	"github.com/v0lka/sp4rk/prompt"
)

// baselineCircuitBreaker returns a breaker with distinct, recognizable values
// so overrides are unambiguous in assertions.
func baselineCircuitBreaker() agent.CircuitBreakerConfig {
	return agent.CircuitBreakerConfig{
		RepeatNudgeThreshold:         10,
		RepeatAbortThreshold:         20,
		TruncationAbortThreshold:     30,
		ParseErrorAbortThreshold:     40,
		FruitlessNudgeThreshold:      50,
		FruitlessAbortThreshold:      60,
		FruitlessMaxResultLen:        70,
		SameToolRepeatNudgeThreshold: 80,
		SameToolRepeatAbortThreshold: 90,
		SameToolResultSizeDelta:      100,
	}
}

func TestApplyLoopHardening_Enabled_OverridesThresholds(t *testing.T) {
	base := baselineCircuitBreaker()
	profile := BuilderSmallLLMConfig{
		Enabled: true,
		LoopHardening: BuilderLoopHardening{
			Enabled:                      true,
			RepeatNudgeThreshold:         2,
			ParseErrorAbortThreshold:     3,
			FruitlessNudgeThreshold:      3,
			FruitlessAbortThreshold:      5,
			SameToolRepeatNudgeThreshold: 4,
		},
	}

	got := applyLoopHardening(base, profile)

	// The five thresholds present in the profile must reflect SmallLLM values.
	if got.RepeatNudgeThreshold != 2 {
		t.Errorf("RepeatNudgeThreshold = %d, want 2", got.RepeatNudgeThreshold)
	}
	if got.ParseErrorAbortThreshold != 3 {
		t.Errorf("ParseErrorAbortThreshold = %d, want 3", got.ParseErrorAbortThreshold)
	}
	if got.FruitlessNudgeThreshold != 3 {
		t.Errorf("FruitlessNudgeThreshold = %d, want 3", got.FruitlessNudgeThreshold)
	}
	if got.FruitlessAbortThreshold != 5 {
		t.Errorf("FruitlessAbortThreshold = %d, want 5", got.FruitlessAbortThreshold)
	}
	if got.SameToolRepeatNudgeThreshold != 4 {
		t.Errorf("SameToolRepeatNudgeThreshold = %d, want 4", got.SameToolRepeatNudgeThreshold)
	}

	// Thresholds absent from the profile must keep their baseline.
	if got.RepeatAbortThreshold != base.RepeatAbortThreshold {
		t.Errorf("RepeatAbortThreshold changed: %d != %d", got.RepeatAbortThreshold, base.RepeatAbortThreshold)
	}
	if got.TruncationAbortThreshold != base.TruncationAbortThreshold {
		t.Errorf("TruncationAbortThreshold changed: %d != %d", got.TruncationAbortThreshold, base.TruncationAbortThreshold)
	}
	if got.SameToolRepeatAbortThreshold != base.SameToolRepeatAbortThreshold {
		t.Errorf("SameToolRepeatAbortThreshold changed: %d != %d", got.SameToolRepeatAbortThreshold, base.SameToolRepeatAbortThreshold)
	}
}

func TestApplyLoopHardening_Disabled_ReturnsUnchanged(t *testing.T) {
	base := baselineCircuitBreaker()

	// Variant off but master on — no override.
	got := applyLoopHardening(base, BuilderSmallLLMConfig{Enabled: true, LoopHardening: BuilderLoopHardening{Enabled: false}})
	if got != base {
		t.Errorf("variant-off: breaker changed from baseline")
	}

	// Master off but variant on — no override (master gate).
	got = applyLoopHardening(base, BuilderSmallLLMConfig{Enabled: false, LoopHardening: BuilderLoopHardening{Enabled: true}})
	if got != base {
		t.Errorf("master-off: breaker changed from baseline")
	}

	// Fully off — no override.
	got = applyLoopHardening(base, BuilderSmallLLMConfig{})
	if got != base {
		t.Errorf("fully-off: breaker changed from baseline")
	}
}

func TestResolveSamplingFunc_Enabled_ExplicitValuesOverride(t *testing.T) {
	const want = 0.12
	const wantTopP = 0.8
	fn := resolveSamplingFunc(BuilderSmallLLMConfig{
		Enabled: true,
		Sampling: BuilderSmallLLMSampling{
			Enabled:     true,
			Temperature: want,
			TopP:        wantTopP,
		},
	})

	// Explicit SmallLLM values win over the vendor matrix for every family...
	for _, family := range []string{"anthropic", "deepseek", "qwen", "", "unknown"} {
		got := fn(family)
		if got.Temperature == nil {
			t.Fatalf("family %q: got nil temperature", family)
		}
		if *got.Temperature != want {
			t.Errorf("family %q: temperature = %v, want %v", family, *got.Temperature, want)
		}
		if got.TopP == nil || *got.TopP != wantTopP {
			t.Errorf("family %q: top_p = %v, want %v", family, got.TopP, wantTopP)
		}
		// ...while fields the user left unset inherit the vendor preset
		// instead of being clobbered to "off".
		vendor := prompt.DefaultSampling(family)
		if (got.TopK == nil) != (vendor.TopK == nil) ||
			(got.TopK != nil && *got.TopK != *vendor.TopK) {
			t.Errorf("family %q: top_k = %v, want vendor %v (inherit)", family, got.TopK, vendor.TopK)
		}
		if (got.RepetitionPenalty == nil) != (vendor.RepetitionPenalty == nil) ||
			(got.RepetitionPenalty != nil && *got.RepetitionPenalty != *vendor.RepetitionPenalty) {
			t.Errorf("family %q: repetition_penalty = %v, want vendor %v (inherit)", family, got.RepetitionPenalty, vendor.RepetitionPenalty)
		}
		if (got.PresencePenalty == nil) != (vendor.PresencePenalty == nil) ||
			(got.PresencePenalty != nil && *got.PresencePenalty != *vendor.PresencePenalty) {
			t.Errorf("family %q: presence_penalty = %v, want vendor %v (inherit)", family, got.PresencePenalty, vendor.PresencePenalty)
		}
	}
}

func TestResolveSamplingFunc_Enabled_NoExplicitValues_InheritsVendorPreset(t *testing.T) {
	// The 27-30B regression guard: enabling the sampling variant without any
	// explicit values must reproduce the vendor preset exactly — in
	// particular it must NOT fall back to a constant temperature of 0.1 (the
	// removed ApplyDefaults seed).
	fn := resolveSamplingFunc(BuilderSmallLLMConfig{
		Enabled:  true,
		Sampling: BuilderSmallLLMSampling{Enabled: true},
	})

	for _, family := range []string{"anthropic", "openai_flagship", "google", "deepseek", "qwen", "", "unknown"} {
		want := prompt.DefaultSampling(family)
		got := fn(family)
		if (got.Temperature == nil) != (want.Temperature == nil) ||
			(got.Temperature != nil && *got.Temperature != *want.Temperature) {
			t.Errorf("family %q: temperature = %v, want vendor %v", family, got.Temperature, want.Temperature)
		}
		if (got.TopP == nil) != (want.TopP == nil) ||
			(got.TopP != nil && *got.TopP != *want.TopP) {
			t.Errorf("family %q: top_p = %v, want vendor %v", family, got.TopP, want.TopP)
		}
		if (got.TopK == nil) != (want.TopK == nil) ||
			(got.TopK != nil && *got.TopK != *want.TopK) {
			t.Errorf("family %q: top_k = %v, want vendor %v", family, got.TopK, want.TopK)
		}
		if (got.RepetitionPenalty == nil) != (want.RepetitionPenalty == nil) ||
			(got.RepetitionPenalty != nil && *got.RepetitionPenalty != *want.RepetitionPenalty) {
			t.Errorf("family %q: repetition_penalty = %v, want vendor %v", family, got.RepetitionPenalty, want.RepetitionPenalty)
		}
		if (got.PresencePenalty == nil) != (want.PresencePenalty == nil) ||
			(got.PresencePenalty != nil && *got.PresencePenalty != *want.PresencePenalty) {
			t.Errorf("family %q: presence_penalty = %v, want vendor %v", family, got.PresencePenalty, want.PresencePenalty)
		}
	}
}

func TestResolveSamplingFunc_Enabled_AllSamplingKnobsOverride(t *testing.T) {
	// Full explicit profile: temperature, top_p, top_k, repetition_penalty
	// and presence_penalty are all plumbed through to the router preset.
	const (
		wantTemp = 0.25
		wantTopP = 0.9
		wantTopK = 7
		wantRep  = 1.1
		wantPP   = 1.5
	)
	fn := resolveSamplingFunc(BuilderSmallLLMConfig{
		Enabled: true,
		Sampling: BuilderSmallLLMSampling{
			Enabled:           true,
			Temperature:       wantTemp,
			TopP:              wantTopP,
			TopK:              wantTopK,
			RepetitionPenalty: wantRep,
			PresencePenalty:   wantPP,
		},
	})

	for _, family := range []string{"anthropic", "qwen", "", "unknown"} {
		got := fn(family)
		if got.Temperature == nil || *got.Temperature != wantTemp {
			t.Errorf("family %q: temperature = %v, want %v", family, got.Temperature, wantTemp)
		}
		if got.TopP == nil || *got.TopP != wantTopP {
			t.Errorf("family %q: top_p = %v, want %v", family, got.TopP, wantTopP)
		}
		if got.TopK == nil || *got.TopK != wantTopK {
			t.Errorf("family %q: top_k = %v, want %v", family, got.TopK, wantTopK)
		}
		if got.RepetitionPenalty == nil || *got.RepetitionPenalty != wantRep {
			t.Errorf("family %q: repetition_penalty = %v, want %v", family, got.RepetitionPenalty, wantRep)
		}
		// No family preset sets presence_penalty, so an explicit profile value
		// must be the sole non-nil source — the sanctioned Qwen
		// anti-repetition lever (0–2, instruct default 1.5).
		if got.PresencePenalty == nil || *got.PresencePenalty != wantPP {
			t.Errorf("family %q: presence_penalty = %v, want %v", family, got.PresencePenalty, wantPP)
		}
	}
}

func TestResolveSamplingFunc_Enabled_ZeroTopPInheritsVendor(t *testing.T) {
	fn := resolveSamplingFunc(BuilderSmallLLMConfig{
		Enabled:  true,
		Sampling: BuilderSmallLLMSampling{Enabled: true, Temperature: 0.3},
	})
	got := fn("qwen")
	if got.Temperature == nil || *got.Temperature != 0.3 {
		t.Errorf("temperature = %v, want 0.3", got.Temperature)
	}
	// qwen's vendor preset carries top_p=0.95: an unset profile TopP must
	// inherit it rather than forcing the parameter off.
	vendor := prompt.DefaultSampling("qwen")
	if vendor.TopP == nil {
		t.Fatal("qwen vendor preset unexpectedly has no top_p")
	}
	if got.TopP == nil || *got.TopP != *vendor.TopP {
		t.Errorf("expected unset TopP to inherit vendor top_p %v, got %v", *vendor.TopP, got.TopP)
	}
}

func TestResolveSamplingFunc_Disabled_MatchesPerFamilyDefault(t *testing.T) {
	// Master off — must fall back to the per-family vendor matrix.
	fnOff := resolveSamplingFunc(BuilderSmallLLMConfig{Enabled: false, Sampling: BuilderSmallLLMSampling{Enabled: true}})
	// Variant off but master on — same fallback.
	fnVariantOff := resolveSamplingFunc(BuilderSmallLLMConfig{Enabled: true, Sampling: BuilderSmallLLMSampling{Enabled: false}})

	for _, family := range []string{"anthropic", "openai_flagship", "google", "deepseek", "qwen", "", "unknown"} {
		want := prompt.DefaultSampling(family)
		for label, fn := range map[string]llm.SamplingFunc{"master-off": fnOff, "variant-off": fnVariantOff} {
			got := fn(family)
			if (got.Temperature == nil) != (want.Temperature == nil) {
				t.Errorf("family %q (%s): nil mismatch: got=%v want=%v", family, label, got.Temperature, want.Temperature)
				continue
			}
			if got.Temperature != nil && *got.Temperature != *want.Temperature {
				t.Errorf("family %q (%s): temperature = %v, want %v", family, label, *got.Temperature, *want.Temperature)
			}
			// The full matrix preset is carried through, not only temperature.
			if (got.TopP == nil) != (want.TopP == nil) ||
				(got.TopP != nil && *got.TopP != *want.TopP) {
				t.Errorf("family %q (%s): top_p = %v, want %v", family, label, got.TopP, want.TopP)
			}
			if (got.TopK == nil) != (want.TopK == nil) ||
				(got.TopK != nil && *got.TopK != *want.TopK) {
				t.Errorf("family %q (%s): top_k = %v, want %v", family, label, got.TopK, want.TopK)
			}
			if (got.RepetitionPenalty == nil) != (want.RepetitionPenalty == nil) ||
				(got.RepetitionPenalty != nil && *got.RepetitionPenalty != *want.RepetitionPenalty) {
				t.Errorf("family %q (%s): repetition_penalty = %v, want %v", family, label, got.RepetitionPenalty, want.RepetitionPenalty)
			}
			if (got.PresencePenalty == nil) != (want.PresencePenalty == nil) ||
				(got.PresencePenalty != nil && *got.PresencePenalty != *want.PresencePenalty) {
				t.Errorf("family %q (%s): presence_penalty = %v, want %v", family, label, got.PresencePenalty, want.PresencePenalty)
			}
		}
	}
}

func TestApplySmallLLMPresets_SeedsReasoningEffortDefault(t *testing.T) {
	// Sampling variant active with a reasoning effort → seeds the builder default.
	b := &OrchestratorBuilder{}
	b.applySmallLLMPresets(&BuilderConfig{
		SmallLLM: BuilderSmallLLMConfig{
			Enabled: true,
			Sampling: BuilderSmallLLMSampling{
				Enabled:         true,
				ReasoningEffort: "low",
			},
		},
	})
	if b.reasoningEffort != "low" {
		t.Errorf("reasoningEffort = %q, want %q", b.reasoningEffort, "low")
	}
}

func TestApplySmallLLMPresets_Disabled_LeavesReasoningEffortEmpty(t *testing.T) {
	// Sampling disabled (or master off) → reasoning effort stays empty.
	cases := []struct {
		name    string
		profile BuilderSmallLLMConfig
	}{
		{"master-off", BuilderSmallLLMConfig{Enabled: false, Sampling: BuilderSmallLLMSampling{Enabled: true, ReasoningEffort: "low"}}},
		{"variant-off", BuilderSmallLLMConfig{Enabled: true, Sampling: BuilderSmallLLMSampling{Enabled: false, ReasoningEffort: "low"}}},
		{"empty-effort", BuilderSmallLLMConfig{Enabled: true, Sampling: BuilderSmallLLMSampling{Enabled: true, ReasoningEffort: ""}}},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			b := &OrchestratorBuilder{}
			b.applySmallLLMPresets(&BuilderConfig{SmallLLM: tc.profile})
			if b.reasoningEffort != "" {
				t.Errorf("%s: reasoningEffort = %q, want empty", tc.name, b.reasoningEffort)
			}
		})
	}
}

// baselineExecutorConfig returns an executor config with distinct, recognizable
// context-management values (mirroring the general defaults) so overrides are
// unambiguous in assertions.
func baselineExecutorConfig() BuilderExecutorConfig {
	return BuilderExecutorConfig{
		MaxRetries:         7,
		OutputTokenReserve: 4096,
		Compaction: BuilderCompactionConfig{
			SlidingWindow:       BuilderSlidingWindow{KeepFirst: 2, KeepLast: 10},
			Summarization:       BuilderSummarization{BlockSize: 7, KeepLast: 5},
			MaxSummarizeTokens:  3000,
			ObservationTruncate: 1000,
			Thresholds:          BuilderCompactionThresholds{PredictivePercent: 85, WarningPercent: 90, EmergencyPercent: 95, PreWarningPercent: 75},
			SafetyMarginPercent: 10,
		},
		ToolOutputPruning: BuilderToolOutputPruning{
			KeepLastN:        3,
			ProtectedTools:   []string{"finish"},
			ThresholdPercent: 60,
		},
	}
}

// smallLLMContextProfile builds a full context variant with the variant
// defaults (6 / 5 / 80 / 2 / 16384).
func smallLLMContextProfile(master bool) BuilderSmallLLMConfig {
	return BuilderSmallLLMConfig{
		Enabled: master,
		Context: BuilderSmallLLMContext{
			Enabled: true,
			Compaction: BuilderSmallLLMCompaction{
				KeepLast:       6,
				BlockSize:      5,
				TriggerPercent: 80,
			},
			ToolOutputKeepLastN: 2,
			OutputTokenReserve:  16384,
		},
	}
}

func TestApplyContextManagement_Enabled_OverridesExecutorValues(t *testing.T) {
	got := applyContextManagement(baselineExecutorConfig(), smallLLMContextProfile(true))

	if got.Compaction.SlidingWindow.KeepLast != 6 {
		t.Errorf("SlidingWindow.KeepLast = %d, want 6", got.Compaction.SlidingWindow.KeepLast)
	}
	if got.Compaction.Summarization.BlockSize != 5 {
		t.Errorf("Summarization.BlockSize = %d, want 5", got.Compaction.Summarization.BlockSize)
	}
	if got.Compaction.Thresholds.PredictivePercent != 80 {
		t.Errorf("Thresholds.PredictivePercent = %d, want 80", got.Compaction.Thresholds.PredictivePercent)
	}
	if got.ToolOutputPruning.KeepLastN != 2 {
		t.Errorf("ToolOutputPruning.KeepLastN = %d, want 2", got.ToolOutputPruning.KeepLastN)
	}
	if got.OutputTokenReserve != 16384 {
		t.Errorf("OutputTokenReserve = %d, want 16384", got.OutputTokenReserve)
	}

	// Knobs outside the variant must keep their baseline.
	if got.Compaction.SlidingWindow.KeepFirst != 2 {
		t.Errorf("SlidingWindow.KeepFirst = %d, want baseline 2", got.Compaction.SlidingWindow.KeepFirst)
	}
	if got.Compaction.Summarization.KeepLast != 5 {
		t.Errorf("Summarization.KeepLast = %d, want baseline 5", got.Compaction.Summarization.KeepLast)
	}
	if got.Compaction.Thresholds.WarningPercent != 90 {
		t.Errorf("Thresholds.WarningPercent = %d, want baseline 90", got.Compaction.Thresholds.WarningPercent)
	}
	if got.Compaction.Thresholds.EmergencyPercent != 95 {
		t.Errorf("Thresholds.EmergencyPercent = %d, want baseline 95", got.Compaction.Thresholds.EmergencyPercent)
	}
	if got.Compaction.Thresholds.PreWarningPercent != 75 {
		t.Errorf("Thresholds.PreWarningPercent = %d, want baseline 75", got.Compaction.Thresholds.PreWarningPercent)
	}
	if got.Compaction.SafetyMarginPercent != 10 {
		t.Errorf("SafetyMarginPercent = %d, want baseline 10", got.Compaction.SafetyMarginPercent)
	}
	if got.MaxRetries != 7 {
		t.Errorf("MaxRetries = %d, want baseline 7", got.MaxRetries)
	}
	if !reflect.DeepEqual(got.ToolOutputPruning.ProtectedTools, []string{"finish"}) {
		t.Errorf("ProtectedTools changed: %v", got.ToolOutputPruning.ProtectedTools)
	}
}

func TestApplyContextManagement_EachKnobOverriddenIndependently(t *testing.T) {
	cases := []struct {
		name   string
		knob   func(*BuilderSmallLLMContext)
		verify func(t *testing.T, got BuilderExecutorConfig)
	}{
		{
			name: "keep_last only",
			knob: func(c *BuilderSmallLLMContext) { c.Compaction.KeepLast = 4 },
			verify: func(t *testing.T, got BuilderExecutorConfig) {
				if got.Compaction.SlidingWindow.KeepLast != 4 {
					t.Errorf("KeepLast = %d, want 4", got.Compaction.SlidingWindow.KeepLast)
				}
			},
		},
		{
			name: "block_size only",
			knob: func(c *BuilderSmallLLMContext) { c.Compaction.BlockSize = 3 },
			verify: func(t *testing.T, got BuilderExecutorConfig) {
				if got.Compaction.Summarization.BlockSize != 3 {
					t.Errorf("BlockSize = %d, want 3", got.Compaction.Summarization.BlockSize)
				}
			},
		},
		{
			name: "trigger_percent only",
			knob: func(c *BuilderSmallLLMContext) { c.Compaction.TriggerPercent = 70 },
			verify: func(t *testing.T, got BuilderExecutorConfig) {
				if got.Compaction.Thresholds.PredictivePercent != 70 {
					t.Errorf("PredictivePercent = %d, want 70", got.Compaction.Thresholds.PredictivePercent)
				}
			},
		},
		{
			name: "tool_output_keep_last_n only",
			knob: func(c *BuilderSmallLLMContext) { c.ToolOutputKeepLastN = 1 },
			verify: func(t *testing.T, got BuilderExecutorConfig) {
				if got.ToolOutputPruning.KeepLastN != 1 {
					t.Errorf("KeepLastN = %d, want 1", got.ToolOutputPruning.KeepLastN)
				}
			},
		},
		{
			name: "output_token_reserve only",
			knob: func(c *BuilderSmallLLMContext) { c.OutputTokenReserve = 16384 },
			verify: func(t *testing.T, got BuilderExecutorConfig) {
				if got.OutputTokenReserve != 16384 {
					t.Errorf("OutputTokenReserve = %d, want 16384", got.OutputTokenReserve)
				}
			},
		},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			profile := smallLLMContextProfile(true)
			profile.Context = BuilderSmallLLMContext{Enabled: true}
			tc.knob(&profile.Context)

			got := applyContextManagement(baselineExecutorConfig(), profile)
			tc.verify(t, got)
		})
	}
}

func TestApplyContextManagement_Disabled_ReturnsBaselineByteForByte(t *testing.T) {
	base := baselineExecutorConfig()

	cases := []struct {
		name    string
		profile BuilderSmallLLMConfig
	}{
		{"master-off", BuilderSmallLLMConfig{Enabled: false, Context: smallLLMContextProfile(true).Context}},
		{"variant-off", BuilderSmallLLMConfig{Enabled: true, Context: BuilderSmallLLMContext{Enabled: false, Compaction: BuilderSmallLLMCompaction{KeepLast: 6, BlockSize: 5, TriggerPercent: 80}, ToolOutputKeepLastN: 2, OutputTokenReserve: 16384}}},
		{"fully-off", BuilderSmallLLMConfig{}},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			got := applyContextManagement(base, tc.profile)
			if !reflect.DeepEqual(got, base) {
				t.Errorf("%s: executor config changed from baseline:\ngot  %+v\nwant %+v", tc.name, got, base)
			}
		})
	}
}
