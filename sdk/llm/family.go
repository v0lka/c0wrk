package llm

import "strings"

// ModelFamily represents a model provider family for prompt and parameter adaptation.
type ModelFamily string

// Model family identifiers used by DetectFamily and the built-in registry.
const (
	FamilyAnthropic      ModelFamily = "anthropic"
	FamilyOpenAIFlagship ModelFamily = "openai_flagship"
	FamilyOpenAIStandard ModelFamily = "openai_standard"
	FamilyGoogle         ModelFamily = "google"
	FamilyMistral        ModelFamily = "mistral"
	FamilyDeepSeek       ModelFamily = "deepseek"
	FamilyOpenAICodex    ModelFamily = "openai_codex"
	FamilyQwen           ModelFamily = "qwen"
	FamilyGLM            ModelFamily = "glm"
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

	// OpenAI Codex (check before GPT patterns — models like "gpt-5.3-codex" need the Responses API)
	if strings.Contains(id, "codex") {
		return FamilyOpenAICodex
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

	// Google (Gemini and Gemma)
	if strings.Contains(id, "gemini") || strings.Contains(id, "gemma") {
		return FamilyGoogle
	}

	// Mistral / Devstral
	if strings.Contains(id, "mistral") || strings.Contains(id, "devstral") || strings.Contains(id, "codestral") {
		return FamilyMistral
	}

	// DeepSeek
	if strings.Contains(id, "deepseek") {
		return FamilyDeepSeek
	}

	// Qwen / QwQ (Alibaba)
	if strings.Contains(id, "qwen") || strings.Contains(id, "qwq") {
		return FamilyQwen
	}

	// GLM / ChatGLM (Zhipu AI)
	if strings.Contains(id, "glm") {
		return FamilyGLM
	}

	// Kimi (Moonshot AI)
	if strings.Contains(id, "kimi") {
		return FamilyKimi
	}

	// xAI Grok — maps to default (no specific prompt adaptation needed)
	// All other models
	return FamilyDefault
}
