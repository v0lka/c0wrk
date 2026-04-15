package llm

import "strings"

// ModelFamily represents a model provider family for prompt and parameter adaptation.
type ModelFamily string

const (
	FamilyAnthropic      ModelFamily = "anthropic"
	FamilyOpenAIFlagship ModelFamily = "openai_flagship"
	FamilyOpenAIStandard ModelFamily = "openai_standard"
	FamilyGemini         ModelFamily = "gemini"
	FamilyMistral        ModelFamily = "mistral"
	FamilyDeepSeek       ModelFamily = "deepseek"
	FamilyKimi           ModelFamily = "kimi"
	FamilyDefault        ModelFamily = "default"
)

// DetectFamily determines the model family from a model ID string.
// This implements the guide's selection logic for prompt and parameter adaptation.
func DetectFamily(modelID string) ModelFamily {
	id := strings.ToLower(modelID)
	if id == "" {
		return FamilyDefault
	}

	// OpenAI Standard (check before flagship patterns since gpt-4.1 contains "gpt-4")
	if strings.Contains(id, "gpt-4.1") {
		return FamilyOpenAIStandard
	}

	// OpenAI Flagship
	for _, p := range []string{"gpt-4", "gpt-5", "o1", "o3", "o4"} {
		if strings.Contains(id, p) {
			return FamilyOpenAIFlagship
		}
	}

	// Anthropic
	if strings.Contains(id, "claude") {
		return FamilyAnthropic
	}

	// Google Gemini
	if strings.Contains(id, "gemini") {
		return FamilyGemini
	}

	// Mistral / Devstral
	if strings.Contains(id, "mistral") || strings.Contains(id, "devstral") || strings.Contains(id, "codestral") {
		return FamilyMistral
	}

	// DeepSeek
	if strings.Contains(id, "deepseek") {
		return FamilyDeepSeek
	}

	// Kimi / Moonshot
	if strings.Contains(id, "kimi") || strings.Contains(id, "moonshot") || strings.Contains(id, "qwen") {
		return FamilyKimi
	}

	// xAI Grok — maps to default (no specific prompt adaptation needed)
	// All other models
	return FamilyDefault
}
