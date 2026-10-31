package core

import (
	"github.com/v0lka/c0wrk/core/markitdown"
	"github.com/v0lka/sp4rk/llm"
)

// OpenAI-compatible captioning endpoints and provider-type tags — markitdown
// captions only over Chat Completions, so which provider types expose a
// surface is fixed: see specs/domains/session-lifecycle.md. The native
// anthropic type takes no custom BaseURL; a custom BaseURL marks an
// anthropic_compatible proxy, which has no surface.
const (
	visionOpenAIDefaultBaseURL   = "https://api.openai.com/v1"    // official root when a provider sets no BaseURL
	visionAnthropicCompatBaseURL = "https://api.anthropic.com/v1" // native anthropic provider's OpenAI SDK compatibility layer
	visionProviderTypeOpenAI     = "openai"                       // chatgpt + openai_compatible — speak Chat Completions
	visionProviderTypeAnthropic  = "anthropic"                    // native provider type
)

// newMarkitdownVisionResolver builds the per-document vision resolver for
// markitdown conversion: the closure re-reads the active model on every
// invocation, so a mid-session switch applies to the next conversion
// (specs/domains/tool-system/builtins.md, vision assistance). A nil return
// disables vision assistance entirely.
func newMarkitdownVisionResolver(router *llm.Router, registry *llm.ModelRegistry, cfg *BuilderConfig) markitdown.VisionResolver {
	if router == nil || registry == nil || cfg == nil {
		return nil
	}
	providers := cfg.LLM.ProviderConfigs
	expand := cfg.ExpandEnvVars
	return func() *markitdown.VisionOptions {
		return resolveMarkitdownVisionOptions(
			router.ActiveModel(),
			router.ActiveProviderName(),
			providers,
			registry,
			expand,
		)
	}
}

// resolveMarkitdownVisionOptions maps the active-model snapshot onto markitdown
// vision connection parameters; nil when the model must not caption.
func resolveMarkitdownVisionOptions(
	activeModel, providerName string,
	providers map[string]BuilderProviderConfig,
	registry *llm.ModelRegistry,
	expandEnv func(string) string,
) *markitdown.VisionOptions {
	if activeModel == "" || providerName == "" || registry == nil {
		return nil
	}
	prov, ok := providers[providerName]
	if !ok {
		return nil
	}

	bareModel := llm.BareModel(activeModel)

	// Vision capability gate; ResolveLocal assumes unknown models are
	// vision-capable (optimistic fallback).
	meta, _ := registry.ResolveLocal(bareModel)
	if meta.Capabilities == nil || !meta.Capabilities.Attachment {
		return nil
	}

	if expandEnv == nil {
		expandEnv = func(s string) string { return s }
	}
	apiKey := expandEnv(prov.APIKey)
	if apiKey == "" {
		return nil
	}

	var baseURL string
	switch prov.ProviderType {
	case visionProviderTypeOpenAI:
		baseURL = expandEnv(prov.BaseURL)
		if baseURL == "" {
			baseURL = visionOpenAIDefaultBaseURL
		}
	case visionProviderTypeAnthropic:
		if expandEnv(prov.BaseURL) != "" {
			return nil
		}
		baseURL = visionAnthropicCompatBaseURL
	default:
		return nil
	}

	return &markitdown.VisionOptions{
		APIKey:  apiKey,
		BaseURL: baseURL,
		Model:   bareModel,
	}
}
