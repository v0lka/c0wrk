package core

import (
	"testing"

	"github.com/v0lka/sp4rk/agent"
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

func TestResolveSamplingFunc_Enabled_ReturnsSmallLLMTemperature(t *testing.T) {
	const want = 0.12
	fn := resolveSamplingFunc(BuilderSmallLLMConfig{
		Enabled: true,
		Sampling: BuilderSmallLLMSampling{
			Enabled:     true,
			Temperature: want,
		},
	})

	// Regardless of family, the SmallLLM temperature wins.
	for _, family := range []string{"anthropic", "deepseek", "qwen", "", "unknown"} {
		got := fn(family)
		if got == nil {
			t.Fatalf("family %q: got nil temperature", family)
		}
		if *got != want {
			t.Errorf("family %q: temperature = %v, want %v", family, *got, want)
		}
	}
}

func TestResolveSamplingFunc_Disabled_MatchesPerFamilyDefault(t *testing.T) {
	// Master off — must fall back to the pre-SmallLLM behavior exactly.
	fnOff := resolveSamplingFunc(BuilderSmallLLMConfig{Enabled: false, Sampling: BuilderSmallLLMSampling{Enabled: true}})
	// Variant off but master on — same fallback.
	fnVariantOff := resolveSamplingFunc(BuilderSmallLLMConfig{Enabled: true, Sampling: BuilderSmallLLMSampling{Enabled: false}})

	for _, family := range []string{"anthropic", "openai_flagship", "google", "deepseek", "qwen", "", "unknown"} {
		want := prompt.DefaultSampling(family).Temperature
		for label, fn := range map[string]func(string) *float64{"master-off": fnOff, "variant-off": fnVariantOff} {
			got := fn(family)
			if (got == nil) != (want == nil) {
				t.Errorf("family %q (%s): nil mismatch: got=%v want=%v", family, label, got, want)
				continue
			}
			if got != nil && *got != *want {
				t.Errorf("family %q (%s): temperature = %v, want %v", family, label, *got, *want)
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
