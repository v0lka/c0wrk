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
		{"OrchestratorSystemLite", OrchestratorSystemLite},
		{"OrchestratorLiteScaffold", OrchestratorLiteScaffold},
		{"OrchestratorLiteFewShot", OrchestratorLiteFewShot},
		{"OrchestratorPlanContext", OrchestratorPlanContext},
		{"GoalMode", GoalMode},
		{"GoalDerivation", GoalDerivation},
		{"GoalVerification", GoalVerification},
		{"GoalReDerivation", GoalReDerivation},
		{"ReflectorSystem", ReflectorSystem},
		{"RouterSystem", RouterSystem},
		{"VerificationMandate", VerificationMandate},
		{"InjectionDefense", InjectionDefense},
		{"CodeReviewMode", CodeReviewMode},
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
		{"PlannerContinuationIncompletePreamble", PlannerContinuationIncompletePreamble},
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
		{"OrchestratorSystemLite", OrchestratorSystemLite, []string{"react", "finish"}},
		{"OrchestratorLiteScaffold", OrchestratorLiteScaffold, []string{"scaffold", "step"}},
		{"OrchestratorLiteFewShot", OrchestratorLiteFewShot, []string{"action", "finish"}},
		{"ReflectorSystem", ReflectorSystem, []string{"reflect"}},
		{"RouterSystem", RouterSystem, []string{"classif"}},
		{"PromptOptimizeExtract", PromptOptimizeExtract, []string{"translate", "keyword", "json"}},
		{"PromptOptimizeRewrite", PromptOptimizeRewrite, []string{"optim", "prompt"}},
		{"GoalDerivation", GoalDerivation, []string{"verification_mode", "executable", "re_derivation"}},
		{"GoalVerification", GoalVerification, []string{"verify clause", "work product"}},
		{"GoalReDerivation", GoalReDerivation, []string{"re-derivation", "delegate"}},
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

// TestGoalVerificationDirectiveByMode covers the mode -> directive selection
// and the shared placeholder resolution for BOTH directives.
func TestGoalVerificationDirectiveByMode(t *testing.T) {
	const (
		cond     = "CONDITION-X7"
		verify   = "VERIFY-Y7"
		evidence = "EVIDENCE-Z7"
	)

	// Executable mode (and the empty default) -> executable directive.
	for _, mode := range []string{"executable", ""} {
		out := GoalVerificationDirectiveByMode(mode, cond, verify, evidence)
		if !strings.Contains(out, "Re-run the verify clause") {
			t.Errorf("mode %q: expected executable directive signature, got: %s", mode, out)
		}
		if strings.Contains(out, "Re-derivation Mode") {
			t.Errorf("mode %q: executable directive must not carry re-derivation signature", mode)
		}
		if !strings.Contains(out, cond) || !strings.Contains(out, verify) || !strings.Contains(out, evidence) {
			t.Errorf("mode %q: placeholders not resolved, got: %s", mode, out)
		}
		if strings.Contains(out, "{goal_condition}") || strings.Contains(out, "{shell_tool}") {
			t.Errorf("mode %q: unresolved placeholder remains, got: %s", mode, out)
		}
	}

	// re_derivation mode -> re-derivation directive.
	out := GoalVerificationDirectiveByMode("re_derivation", cond, verify, evidence)
	if !strings.Contains(out, "Re-derivation Mode") {
		t.Errorf("re_derivation mode: expected re-derivation directive signature, got: %s", out)
	}
	if strings.Contains(out, "Re-run the verify clause") {
		t.Errorf("re_derivation mode: must not carry executable directive signature")
	}
	if !strings.Contains(out, cond) || !strings.Contains(out, verify) || !strings.Contains(out, evidence) {
		t.Errorf("re_derivation mode: placeholders not resolved, got: %s", out)
	}
	if strings.Contains(out, "{reported_evidence}") || strings.Contains(out, "{shell_tool}") {
		t.Errorf("re_derivation mode: unresolved placeholder remains, got: %s", out)
	}
}

// TestGoalVerificationSubstitute_LeavesUnknownTextIntact verifies the
// substitution is a pure template fill — text outside the known placeholders
// is preserved verbatim.
func TestGoalVerificationSubstitute_LeavesUnknownTextIntact(t *testing.T) {
	in := "preamble {goal_condition} middle {goal_verify_clause} tail {reported_evidence} done"
	out := GoalVerificationSubstitute(in, "C", "V", "E")
	if out != "preamble C middle V tail E done" {
		t.Errorf("unexpected substitution result: %s", out)
	}
}
