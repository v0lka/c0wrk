package prompts

import (
	"strings"
	"testing"
)

func TestJudgeSystem_NonEmpty(t *testing.T) {
	if JudgeSystem == "" {
		t.Fatal("JudgeSystem is empty, expected embedded content")
	}
	trimmed := strings.TrimSpace(JudgeSystem)
	if trimmed == "" {
		t.Fatal("JudgeSystem is blank (whitespace only)")
	}
}

func TestJudgeSystem_ContainsExpectedKeywords(t *testing.T) {
	lower := strings.ToLower(JudgeSystem)
	keywords := []string{"tool", "safe"}
	for _, kw := range keywords {
		if !strings.Contains(lower, kw) {
			t.Errorf("JudgeSystem does not contain expected keyword %q", kw)
		}
	}
}
