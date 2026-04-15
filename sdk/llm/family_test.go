package llm

import "testing"

func TestDetectFamily(t *testing.T) {
	tests := []struct {
		modelID  string
		expected ModelFamily
	}{
		// Empty model ID
		{"", FamilyDefault},

		// OpenAI Standard (gpt-4.1 must be checked before gpt-4 flagship)
		{"gpt-4.1", FamilyOpenAIStandard},
		{"gpt-4.1-mini", FamilyOpenAIStandard},
		{"gpt-4.1-nano", FamilyOpenAIStandard},

		// OpenAI Flagship
		{"gpt-4o", FamilyOpenAIFlagship},
		{"gpt-4o-mini", FamilyOpenAIFlagship},
		{"gpt-4-turbo", FamilyOpenAIFlagship},
		{"gpt-5", FamilyOpenAIFlagship},
		{"gpt-5.4", FamilyOpenAIFlagship},
		{"o1", FamilyOpenAIFlagship},
		{"o1-mini", FamilyOpenAIFlagship},
		{"o3", FamilyOpenAIFlagship},
		{"o3-mini", FamilyOpenAIFlagship},
		{"o4-mini", FamilyOpenAIFlagship},

		// Anthropic
		{"claude-opus-4.6", FamilyAnthropic},
		{"claude-sonnet-4.5", FamilyAnthropic},
		{"claude-3.5-sonnet", FamilyAnthropic},
		{"claude-haiku-4.5", FamilyAnthropic},
		{"claude-custom", FamilyAnthropic},

		// Gemini
		{"gemini-2.5-pro", FamilyGemini},
		{"gemini-2.5-flash", FamilyGemini},
		{"gemini-2.0-flash", FamilyGemini},
		{"gemini-custom", FamilyGemini},

		// Mistral / Devstral / Codestral
		{"mistral-large-latest", FamilyMistral},
		{"mistral-7b-instruct", FamilyMistral},
		{"devstral-v1", FamilyMistral},
		{"codestral-latest", FamilyMistral},

		// DeepSeek
		{"deepseek-chat", FamilyDeepSeek},
		{"deepseek-reasoner", FamilyDeepSeek},
		{"deepseek-v3", FamilyDeepSeek},

		// Kimi / Moonshot / Qwen
		{"kimi-k2", FamilyKimi},
		{"moonshot-v1", FamilyKimi},
		{"qwen-2.5-72b", FamilyKimi},

		// Default (no specific pattern)
		{"grok-4", FamilyDefault},
		{"llama-3.1-70b", FamilyDefault},
		{"phi-3-mini", FamilyDefault},
		{"unknown-model", FamilyDefault},
	}

	for _, tt := range tests {
		t.Run(tt.modelID, func(t *testing.T) {
			got := DetectFamily(tt.modelID)
			if got != tt.expected {
				t.Errorf("DetectFamily(%q) = %q, want %q", tt.modelID, got, tt.expected)
			}
		})
	}
}

func TestDetectFamily_CaseInsensitive(t *testing.T) {
	// DetectFamily lowercases the input, so mixed case should still work
	tests := []struct {
		modelID  string
		expected ModelFamily
	}{
		{"Claude-Opus-4.6", FamilyAnthropic},
		{"GPT-4O", FamilyOpenAIFlagship},
		{"GEMINI-2.5-PRO", FamilyGemini},
		{"DeepSeek-Chat", FamilyDeepSeek},
		{"MISTRAL-LARGE", FamilyMistral},
	}

	for _, tt := range tests {
		t.Run(tt.modelID, func(t *testing.T) {
			got := DetectFamily(tt.modelID)
			if got != tt.expected {
				t.Errorf("DetectFamily(%q) = %q, want %q", tt.modelID, got, tt.expected)
			}
		})
	}
}

func TestModelFamilyConstants(t *testing.T) {
	// Verify the string values of family constants
	families := map[ModelFamily]string{
		FamilyAnthropic:      "anthropic",
		FamilyOpenAIFlagship: "openai_flagship",
		FamilyOpenAIStandard: "openai_standard",
		FamilyGemini:         "gemini",
		FamilyMistral:        "mistral",
		FamilyDeepSeek:       "deepseek",
		FamilyKimi:           "kimi",
		FamilyDefault:        "default",
	}

	for family, expected := range families {
		if string(family) != expected {
			t.Errorf("Family constant %q != expected %q", string(family), expected)
		}
	}
}
