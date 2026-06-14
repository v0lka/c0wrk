package prompts

import (
	"strings"
	"testing"
)

func TestEmbeddedPrompts_NonEmpty(t *testing.T) {
	tests := []struct {
		name  string
		value string
	}{
		{"PlannerBase", PlannerBase},
		{"PlannerReplan", PlannerReplan},
		{"PlannerInformed", PlannerInformed},
		{"OrchestratorSystem", OrchestratorSystem},
		{"OrchestratorPlanContext", OrchestratorPlanContext},
		{"ReflectorSystem", ReflectorSystem},
		{"RouterSystem", RouterSystem},
		{"VerificationMandate", VerificationMandate},
		{"InjectionDefense", InjectionDefense},
		{"CompactionSummarize", CompactionSummarize},
		// Prompt optimizer prompts
		{"PromptOptimizeExtract", PromptOptimizeExtract},
		{"PromptOptimizeRewrite", PromptOptimizeRewrite},
		// Family-specific orchestrator prompts
		{"OrchestratorDefault", OrchestratorDefault},
		{"OrchestratorAnthropic", OrchestratorAnthropic},
		{"OrchestratorOpenAIFlagship", OrchestratorOpenAIFlagship},
		{"OrchestratorOpenAIStandard", OrchestratorOpenAIStandard},
		{"OrchestratorGoogle", OrchestratorGoogle},
		{"OrchestratorQwen", OrchestratorQwen},
		{"OrchestratorGLM", OrchestratorGLM},
		{"OrchestratorDeepSeek", OrchestratorDeepSeek},
		{"OrchestratorMistral", OrchestratorMistral},
		{"OrchestratorKimi", OrchestratorKimi},
		{"OrchestratorOpenAICodex", OrchestratorOpenAICodex},
		// Family-specific planner prompts
		{"PlannerDefault", PlannerDefault},
		{"PlannerAnthropic", PlannerAnthropic},
		{"PlannerOpenAIFlagship", PlannerOpenAIFlagship},
		{"PlannerOpenAIStandard", PlannerOpenAIStandard},
		{"PlannerGoogle", PlannerGoogle},
		{"PlannerQwen", PlannerQwen},
		{"PlannerGLM", PlannerGLM},
		{"PlannerDeepSeek", PlannerDeepSeek},
		{"PlannerMistral", PlannerMistral},
		{"PlannerKimi", PlannerKimi},
		{"PlannerOpenAICodex", PlannerOpenAICodex},
		// Planner mode-specific composable prompt sections
		{"PlannerPlanPreamble", PlannerPlanPreamble},
		{"PlannerSingleStepPreamble", PlannerSingleStepPreamble},
		{"PlannerMultiStepToT", PlannerMultiStepToT},
		{"PlannerSingleStepToT", PlannerSingleStepToT},
		{"PlannerMultiStepGuidance", PlannerMultiStepGuidance},
		{"PlannerSingleStepGuidance", PlannerSingleStepGuidance},
		{"PlannerDomainAssignment", PlannerDomainAssignment},
		{"PlannerAgentProfiles", PlannerAgentProfiles},
		{"PlannerExtraSections", PlannerExtraSections},
		{"PlannerContinuationPreamble", PlannerContinuationPreamble},
		{"PlannerContinuationSingleStep", PlannerContinuationSingleStep},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			if tt.value == "" {
				t.Errorf("%s is empty, expected embedded content", tt.name)
			}
			trimmed := strings.TrimSpace(tt.value)
			if trimmed == "" {
				t.Errorf("%s is blank (whitespace only)", tt.name)
			}
		})
	}
}

func TestEmbeddedPrompts_ContainExpectedKeywords(t *testing.T) {
	tests := []struct {
		name     string
		value    string
		keywords []string
	}{
		{"PlannerBase", PlannerBase, []string{"step", "agent"}},
		{"PlannerReplan", PlannerReplan, []string{"plan"}},
		{"OrchestratorSystem", OrchestratorSystem, []string{"task"}},
		{"ReflectorSystem", ReflectorSystem, []string{"reflect"}},
		{"RouterSystem", RouterSystem, []string{"classif"}},
		{"PromptOptimizeExtract", PromptOptimizeExtract, []string{"translate", "keyword", "json"}},
		{"PromptOptimizeRewrite", PromptOptimizeRewrite, []string{"optim", "prompt"}},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			lower := strings.ToLower(tt.value)
			for _, kw := range tt.keywords {
				if !strings.Contains(lower, kw) {
					t.Errorf("%s does not contain expected keyword %q", tt.name, kw)
				}
			}
		})
	}
}

func TestEmbeddedPrompts_AreDistinct(t *testing.T) {
	prompts := map[string]string{
		"PlannerBase":        PlannerBase,
		"PlannerReplan":      PlannerReplan,
		"OrchestratorSystem": OrchestratorSystem,
		"ReflectorSystem":    ReflectorSystem,
		"RouterSystem":       RouterSystem,
	}

	seen := make(map[string]string) // content -> name
	for name, content := range prompts {
		if prev, exists := seen[content]; exists {
			t.Errorf("%s and %s have identical content", name, prev)
		}
		seen[content] = name
	}
}

func TestFamilyPrompt_Orchestrator(t *testing.T) {
	families := []string{"anthropic", "openai_flagship", "openai_standard", "google", "deepseek", "mistral", "kimi", "qwen", "glm", "openai_codex", "default"}
	for _, fam := range families {
		t.Run(fam, func(t *testing.T) {
			result := FamilyPrompt("orchestrator", fam)
			if result == "" {
				t.Errorf("FamilyPrompt(orchestrator, %s) returned empty", fam)
			}
		})
	}
}

func TestFamilyPrompt_Planner(t *testing.T) {
	families := []string{"anthropic", "openai_flagship", "openai_standard", "google", "deepseek", "mistral", "kimi", "qwen", "glm", "openai_codex", "default"}
	for _, fam := range families {
		t.Run(fam, func(t *testing.T) {
			result := FamilyPrompt("planner", fam)
			if result == "" {
				t.Errorf("FamilyPrompt(planner, %s) returned empty", fam)
			}
		})
	}
}

func TestFamilyPrompt_UnknownFamily_FallsBackToDefault(t *testing.T) {
	result := FamilyPrompt("orchestrator", "unknown_family")
	if result != OrchestratorDefault {
		t.Error("expected fallback to OrchestratorDefault for unknown family")
	}
	result = FamilyPrompt("planner", "unknown_family")
	if result != PlannerDefault {
		t.Error("expected fallback to PlannerDefault for unknown family")
	}
}

func TestFamilyPrompt_AuxiliaryAgent_ReturnsEmpty(t *testing.T) {
	if result := FamilyPrompt("router", "anthropic"); result != "" {
		t.Error("expected empty for auxiliary agent router")
	}
	if result := FamilyPrompt("reflector", "anthropic"); result != "" {
		t.Error("expected empty for auxiliary agent reflector")
	}
	if result := FamilyPrompt("unknown", "anthropic"); result != "" {
		t.Error("expected empty for unknown agent")
	}
}
