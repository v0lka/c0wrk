// Package prompt provides a composable prompt builder with model-tier awareness.
// It implements a 3-layer architecture: shared core + model adapter + sampling config.
package prompt

// ModelTier represents the capability class of an LLM.
type ModelTier string

const (
	// TierLarge represents high-capability models (GPT-4+, Claude, Gemini Pro, DeepSeek-V3).
	TierLarge ModelTier = "large"
	// TierSmall represents mid-range models (Llama 70B, Qwen 72B, Mistral, etc.).
	TierSmall ModelTier = "small"
)
