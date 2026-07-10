package sp4rk

import (
	"strings"
	"testing"
)

func TestDefaultPromptSetNonEmpty(t *testing.T) {
	ps := DefaultPromptSet()

	for _, field := range []struct {
		name  string
		value string
	}{
		{"BasePrompt", ps.BasePrompt},
		{"PlanPreamble", ps.PlanPreamble},
		{"MultiStepGuidance", ps.MultiStepGuidance},
		{"SingleStepPreamble", ps.SingleStepPreamble},
		{"SingleStepGuidance", ps.SingleStepGuidance},
		{"ReplanPrompt", ps.ReplanPrompt},
		{"VerificationMandate", ps.VerificationMandate},
	} {
		if strings.TrimSpace(field.value) == "" {
			t.Errorf("DefaultPromptSet().%s is empty", field.name)
		}
	}
}

func TestDefaultPromptSetHasPlaceholders(t *testing.T) {
	ps := DefaultPromptSet()

	// The base prompt must reference the placeholders the planner substitutes.
	for _, placeholder := range []string{"AVAILABLE-TOOLS", "AVAILABLE-SKILLS", "MODE-PREAMBLE", "MAX-STEPS", "MODE-JSON-EXAMPLE"} {
		if !strings.Contains(ps.BasePrompt, placeholder) {
			t.Errorf("BasePrompt missing placeholder %q", placeholder)
		}
	}
	// The replan prompt must also carry the essential placeholders.
	for _, placeholder := range []string{"AVAILABLE-TOOLS", "MODE-PREAMBLE", "MODE-JSON-EXAMPLE"} {
		if !strings.Contains(ps.ReplanPrompt, placeholder) {
			t.Errorf("ReplanPrompt missing placeholder %q", placeholder)
		}
	}
}

func TestDefaultReflectorPromptNonEmpty(t *testing.T) {
	p := DefaultReflectorPrompt()
	if strings.TrimSpace(p) == "" {
		t.Error("DefaultReflectorPrompt() is empty")
	}
	// Must mention the three suggested actions the reflector protocol expects.
	for _, action := range []string{"retry", "replan", "abort"} {
		if !strings.Contains(p, action) {
			t.Errorf("DefaultReflectorPrompt() missing action %q", action)
		}
	}
}
