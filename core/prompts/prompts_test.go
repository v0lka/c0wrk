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
		{"ReflectorInstructions", ReflectorInstructions},
		{"RouterSystem", RouterSystem},
		{"RouterInstructions", RouterInstructions},
		{"CompactionSummarize", CompactionSummarize},
		// Family-specific orchestrator prompts
		{"OrchestratorDefault", OrchestratorDefault},
		{"OrchestratorAnthropic", OrchestratorAnthropic},
		{"OrchestratorOpenAIFlagship", OrchestratorOpenAIFlagship},
		{"OrchestratorOpenAIStandard", OrchestratorOpenAIStandard},
		{"OrchestratorGemini", OrchestratorGemini},
		{"OrchestratorDeepSeek", OrchestratorDeepSeek},
		{"OrchestratorMistral", OrchestratorMistral},
		// Family-specific planner prompts
		{"PlannerDefault", PlannerDefault},
		{"PlannerAnthropic", PlannerAnthropic},
		{"PlannerOpenAIFlagship", PlannerOpenAIFlagship},
		{"PlannerOpenAIStandard", PlannerOpenAIStandard},
		{"PlannerGemini", PlannerGemini},
		{"PlannerDeepSeek", PlannerDeepSeek},
		{"PlannerMistral", PlannerMistral},
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
	families := []string{"anthropic", "openai_flagship", "openai_standard", "gemini", "deepseek", "mistral", "default"}
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
	families := []string{"anthropic", "openai_flagship", "openai_standard", "gemini", "deepseek", "mistral", "default"}
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
