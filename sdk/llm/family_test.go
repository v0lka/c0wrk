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

		// Google (Gemini + Gemma)
		{"gemini-2.5-pro", FamilyGoogle},
		{"gemini-2.5-flash", FamilyGoogle},
		{"gemini-2.0-flash", FamilyGoogle},
		{"gemini-custom", FamilyGoogle},
		{"gemma-4-31b-it", FamilyGoogle},
		{"gemma-2-27b", FamilyGoogle},

		// Mistral / Devstral / Codestral
		{"mistral-large-latest", FamilyMistral},
		{"mistral-7b-instruct", FamilyMistral},
		{"devstral-v1", FamilyMistral},
		{"codestral-latest", FamilyMistral},

		// OpenAI Codex (must match before GPT flagship patterns)
		{"codex-mini-latest", FamilyOpenAICodex},
		{"codex-mini-2025-03-25", FamilyOpenAICodex},
		{"gpt-5.3-codex", FamilyOpenAICodex}, // "codex" takes priority over "gpt-5"

		// DeepSeek
		{"deepseek-v4-pro", FamilyDeepSeek},
		{"deepseek-v4-flash", FamilyDeepSeek},
		{"deepseek-chat", FamilyDeepSeek},

		// Qwen / QwQ (Alibaba)
		{"qwen-plus", FamilyQwen},
		{"qwen-max", FamilyQwen},
		{"qwen-2.5-72b", FamilyQwen},
		{"qwq-plus", FamilyQwen},
		{"qwq-32b", FamilyQwen},

		// GLM (Zhipu AI)
		{"glm-5.1", FamilyGLM},
		{"glm-4.7", FamilyGLM},
		{"glm-z1-32b", FamilyGLM},
		{"chatglm-4", FamilyGLM},

		// Kimi (Moonshot AI)
		{"kimi-k2", FamilyKimi},
		{"kimi-k2-thinking", FamilyKimi},

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
		{"GEMINI-2.5-PRO", FamilyGoogle},
		{"DeepSeek-Chat", FamilyDeepSeek},
		{"MISTRAL-LARGE", FamilyMistral},
		{"QWEN-PLUS", FamilyQwen},
		{"GLM-5.1", FamilyGLM},
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
		FamilyOpenAICodex:    "openai_codex",
		FamilyGoogle:         "google",
		FamilyMistral:        "mistral",
		FamilyDeepSeek:       "deepseek",
		FamilyQwen:           "qwen",
		FamilyGLM:            "glm",
		FamilyKimi:           "kimi",
		FamilyDefault:        "default",
	}

	for family, expected := range families {
		if string(family) != expected {
			t.Errorf("Family constant %q != expected %q", string(family), expected)
		}
	}
}
