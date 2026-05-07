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
	families := []string{"mistral", "kimi", "default", "unknown"}
	for _, family := range families {
		t.Run(family, func(t *testing.T) {
			cfg := ResolveReasoning(ReasoningHigh, family)
			if cfg.Enabled {
				t.Errorf("expected Enabled=false for family %q", family)
			}
		})
	}
}

func TestResolveReasoning_DeepSeek(t *testing.T) {
	tests := []struct {
		effort           ReasoningEffort
		wantEnabled      bool
		wantOpenAIEffort string
		wantThinking     string
	}{
		{ReasoningOff, false, "", "disabled"},
		{ReasoningLow, true, "high", "enabled"},
		{ReasoningMedium, true, "high", "enabled"},
		{ReasoningHigh, true, "high", "enabled"},
		{ReasoningMaximum, true, "max", "enabled"},
		{"unknown", true, "high", "enabled"}, // defaults to high
	}

	for _, tt := range tests {
		t.Run(string(tt.effort), func(t *testing.T) {
			cfg := ResolveReasoning(tt.effort, "deepseek")
			if cfg.Enabled != tt.wantEnabled {
				t.Errorf("Enabled = %v, want %v", cfg.Enabled, tt.wantEnabled)
			}
			if cfg.DeepSeekThinking != tt.wantThinking {
				t.Errorf("DeepSeekThinking = %q, want %q", cfg.DeepSeekThinking, tt.wantThinking)
			}
			if tt.wantEnabled && cfg.OpenAIEffort != tt.wantOpenAIEffort {
				t.Errorf("OpenAIEffort = %q, want %q", cfg.OpenAIEffort, tt.wantOpenAIEffort)
			}
		})
	}
}

func TestResolveReasoning_Off(t *testing.T) {
	// Verify ReasoningOff disables reasoning across all supported families
	families := []string{"anthropic", "openai_flagship", "openai_standard", "gemini", "deepseek"}
	for _, family := range families {
		t.Run(family, func(t *testing.T) {
			cfg := ResolveReasoning(ReasoningOff, family)
			if cfg.Enabled {
				t.Errorf("expected Enabled=false for family %q with ReasoningOff", family)
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
		{"tester", ReasoningHigh, ReasoningHigh}, // tester is primary

		// Analytical auxiliary agents get reduced reasoning
		{"router", ReasoningHigh, ReasoningLow},
		{"router", ReasoningMedium, ReasoningMinimal},
		{"reflector", ReasoningMaximum, ReasoningLow},
		{"reflector", ReasoningLow, ReasoningMinimal},
		{"researcher", ReasoningHigh, ReasoningLow},    // researcher is analytical
		{"researcher", ReasoningMaximum, ReasoningLow}, // researcher is analytical
		{"researcher", ReasoningMedium, ReasoningMinimal},

		// Mechanical auxiliary agents get reasoning disabled
		{"compaction", ReasoningHigh, ReasoningOff},
		{"title", ReasoningMedium, ReasoningOff},
		{"summary", ReasoningHigh, ReasoningOff},
		{"judge", ReasoningHigh, ReasoningOff}, // judge is mechanical

		// Unknown role passes through
		{"custom_role", ReasoningHigh, ReasoningHigh},

		// Empty base effort returns ReasoningOff for all roles
		{"orchestrator", "", ReasoningOff},
		{"router", "", ReasoningOff},
		{"reflector", "", ReasoningOff},
		{"compaction", "", ReasoningOff},
		{"title", "", ReasoningOff},
		{"custom_role", "", ReasoningOff},
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

func TestResolveAgentReasoningMode(t *testing.T) {
	tests := []struct {
		name     string
		role     string
		base     ReasoningEffort
		overrides map[string]string
		expected ReasoningEffort
	}{
		// No overrides: falls back to AgentReasoningMode
		{"no overrides primary", "coder", ReasoningHigh, nil, ReasoningHigh},
		{"no overrides analytical", "researcher", ReasoningHigh, nil, ReasoningLow},
		{"no overrides mechanical", "title", ReasoningHigh, nil, ReasoningOff},

		// Override takes precedence
		{"override researcher", "researcher", ReasoningHigh, map[string]string{"researcher": "medium"}, ReasoningMedium},
		{"override coder", "coder", ReasoningHigh, map[string]string{"coder": "low"}, ReasoningLow},
		{"override judge to enabled", "judge", ReasoningHigh, map[string]string{"judge": "high"}, ReasoningHigh},

		// Override for different role is ignored
		{"unrelated override", "coder", ReasoningHigh, map[string]string{"researcher": "medium"}, ReasoningHigh},

		// Empty override value falls back to default
		{"empty override", "researcher", ReasoningHigh, map[string]string{"researcher": ""}, ReasoningLow},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got := ResolveAgentReasoningMode(tt.role, tt.base, tt.overrides)
			if got != tt.expected {
				t.Errorf("ResolveAgentReasoningMode(%q, %q, %v) = %q, want %q", tt.role, tt.base, tt.overrides, got, tt.expected)
			}
		})
	}
}
