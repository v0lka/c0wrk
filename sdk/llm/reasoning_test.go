package llm

import "testing"

func TestResolveReasoning_Anthropic(t *testing.T) {
	tests := []struct {
		effort      ReasoningEffort
		wantEnabled bool
		wantBudget  int
	}{
		{ReasoningMinimal, true, 1024},
		{ReasoningLow, true, 2000},
		{ReasoningMedium, true, 5000},
		{ReasoningHigh, true, 10000},
		{ReasoningMaximum, true, 32000},
		{"unknown", true, 10000}, // defaults to high
	}

	for _, tt := range tests {
		t.Run(string(tt.effort), func(t *testing.T) {
			cfg := ResolveReasoning(tt.effort, "anthropic")
			if cfg.Enabled != tt.wantEnabled {
				t.Errorf("Enabled = %v, want %v", cfg.Enabled, tt.wantEnabled)
			}
			if cfg.BudgetTokens != tt.wantBudget {
				t.Errorf("BudgetTokens = %d, want %d", cfg.BudgetTokens, tt.wantBudget)
			}
		})
	}
}

func TestResolveReasoning_OpenAI(t *testing.T) {
	tests := []struct {
		effort     ReasoningEffort
		family     string
		wantEffort string
	}{
		{ReasoningMinimal, "openai_flagship", "low"},
		{ReasoningLow, "openai_flagship", "low"},
		{ReasoningMedium, "openai_flagship", "medium"},
		{ReasoningHigh, "openai_flagship", "high"},
		{ReasoningMaximum, "openai_flagship", "high"},
		{ReasoningMedium, "openai_standard", "medium"},
	}

	for _, tt := range tests {
		t.Run(tt.family+"/"+string(tt.effort), func(t *testing.T) {
			cfg := ResolveReasoning(tt.effort, tt.family)
			if !cfg.Enabled {
				t.Fatal("expected Enabled=true for OpenAI")
			}
			if cfg.OpenAIEffort != tt.wantEffort {
				t.Errorf("OpenAIEffort = %q, want %q", cfg.OpenAIEffort, tt.wantEffort)
			}
		})
	}
}

func TestResolveReasoning_Gemini(t *testing.T) {
	tests := []struct {
		effort     ReasoningEffort
		wantLevel  string
		wantBudget int
	}{
		{ReasoningMinimal, "minimal", 0},
		{ReasoningLow, "low", 2048},
		{ReasoningMedium, "medium", 4096},
		{ReasoningHigh, "high", 8192},
		{ReasoningMaximum, "high", 16384},
	}

	for _, tt := range tests {
		t.Run(string(tt.effort), func(t *testing.T) {
			cfg := ResolveReasoning(tt.effort, "gemini")
			if !cfg.Enabled {
				t.Fatal("expected Enabled=true for Gemini")
			}
			if cfg.GeminiThinkingLevel != tt.wantLevel {
				t.Errorf("GeminiThinkingLevel = %q, want %q", cfg.GeminiThinkingLevel, tt.wantLevel)
			}
			if cfg.GeminiThinkingBudget != tt.wantBudget {
				t.Errorf("GeminiThinkingBudget = %d, want %d", cfg.GeminiThinkingBudget, tt.wantBudget)
			}
		})
	}
}

func TestResolveReasoning_UnsupportedFamily(t *testing.T) {
	families := []string{"deepseek", "mistral", "kimi", "default", "unknown"}
	for _, family := range families {
		t.Run(family, func(t *testing.T) {
			cfg := ResolveReasoning(ReasoningHigh, family)
			if cfg.Enabled {
				t.Errorf("expected Enabled=false for family %q", family)
			}
		})
	}
}

func TestAgentReasoningMode(t *testing.T) {
	tests := []struct {
		role     string
		base     ReasoningEffort
		expected ReasoningEffort
	}{
		// Primary agents pass through base effort
		{"orchestrator", ReasoningHigh, ReasoningHigh},
		{"planner", ReasoningMaximum, ReasoningMaximum},
		{"coder", ReasoningMedium, ReasoningMedium},
		{"executor", ReasoningLow, ReasoningLow},

		// Auxiliary agents get reduced reasoning
		{"router", ReasoningHigh, ReasoningLow},
		{"router", ReasoningMedium, ReasoningMinimal},
		{"reflector", ReasoningMaximum, ReasoningLow},
		{"reflector", ReasoningLow, ReasoningMinimal},
		{"compaction", ReasoningHigh, ReasoningLow},
		{"title", ReasoningMedium, ReasoningMinimal},
		{"summary", ReasoningHigh, ReasoningLow},

		// Unknown role passes through
		{"custom_role", ReasoningHigh, ReasoningHigh},

		// Empty base effort returns empty for all roles
		{"orchestrator", "", ""},
		{"router", "", ""},
		{"reflector", "", ""},
		{"compaction", "", ""},
		{"title", "", ""},
		{"custom_role", "", ""},
	}

	for _, tt := range tests {
		t.Run(tt.role+"/"+string(tt.base), func(t *testing.T) {
			got := AgentReasoningMode(tt.role, tt.base)
			if got != tt.expected {
				t.Errorf("AgentReasoningMode(%q, %q) = %q, want %q", tt.role, tt.base, got, tt.expected)
			}
		})
	}
}
