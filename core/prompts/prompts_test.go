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
		{"PlannerPlan", PlannerPlan},
		{"PlannerReplan", PlannerReplan},
		{"OrchestratorSystem", OrchestratorSystem},
		{"ReflectorSystem", ReflectorSystem},
		{"RouterSystem", RouterSystem},
		{"RawACExtractorSystem", RawACExtractorSystem},
		{"ACEnricherSystem", ACEnricherSystem},
		{"EvaluatorJudge", EvaluatorJudge},
		{"EvaluatorReconsider", EvaluatorReconsider},
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
		{"PlannerPlan", PlannerPlan, []string{"plan", "step"}},
		{"PlannerReplan", PlannerReplan, []string{"plan"}},
		{"OrchestratorSystem", OrchestratorSystem, []string{"task"}},
		{"ReflectorSystem", ReflectorSystem, []string{"reflect"}},
		{"RouterSystem", RouterSystem, []string{"classif"}},
		{"EvaluatorJudge", EvaluatorJudge, []string{"evaluat"}},
		{"EvaluatorReconsider", EvaluatorReconsider, []string{"evaluat"}},
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
		"PlannerPlan":          PlannerPlan,
		"PlannerReplan":        PlannerReplan,
		"OrchestratorSystem":   OrchestratorSystem,
		"ReflectorSystem":      ReflectorSystem,
		"RouterSystem":         RouterSystem,
		"RawACExtractorSystem": RawACExtractorSystem,
		"ACEnricherSystem":     ACEnricherSystem,
		"EvaluatorJudge":       EvaluatorJudge,
		"EvaluatorReconsider":  EvaluatorReconsider,
	}

	seen := make(map[string]string) // content -> name
	for name, content := range prompts {
		if prev, exists := seen[content]; exists {
			t.Errorf("%s and %s have identical content", name, prev)
		}
		seen[content] = name
	}
}
