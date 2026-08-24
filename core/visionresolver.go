package core

import (
	"github.com/v0lka/c0wrk/core/markitdown"
	"github.com/v0lka/sp4rk/llm"
)

// Default OpenAI-compatible API roots used when a provider config carries no
// explicit base URL. markitdown's captioning client speaks the OpenAI Chat
// Completions wire format exclusively, so vision options can only be produced
// for providers with a known OpenAI-compatible surface:
//
//   - ProviderType "openai" (chatgpt + openai_compatible): the configured
//     BaseURL as-is; the official OpenAI API root when unset.
//   - ProviderType "anthropic" WITHOUT a custom BaseURL (the native
//     "anthropic" provider): Anthropic's documented OpenAI SDK compatibility
//     layer. markitdown swallows per-image captioning errors internally, so a
//     compatibility gap degrades to caption-less conversion, never a failure.
//   - ProviderType "anthropic" WITH a custom BaseURL (anthropic_compatible
//     proxies): NO vision options — such endpoints speak the Anthropic
//     Messages API and cannot be assumed to expose /chat/completions.
const (
	visionOpenAIDefaultBaseURL   = "https://api.openai.com/v1"
	visionAnthropicCompatBaseURL = "https://api.anthropic.com/v1"
	visionProviderTypeOpenAI     = "openai"
	visionProviderTypeAnthropic  = "anthropic"
)

// newMarkitdownVisionResolver builds the per-document vision resolver used for
// markitdown document conversion. The returned closure inspects the router's
// CURRENT active model on every invocation (per-document semantics): a
// mid-session model switch is picked up by the very next conversion.
//
// It returns nil (disable vision assistance entirely) when any dependency is
// missing — the builder then skips attaching a resolver and conversions run
// exactly as they did before vision support.
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

// resolveMarkitdownVisionOptions maps the given active-model snapshot onto
// markitdown vision connection parameters, or nil when the model must not be
// used for captioning. Pure function (no I/O, no locks) so the mapping matrix
// is unit-testable without a live router.
//
// Nil results (in order of evaluation): unknown active model or provider,
// non-vision-capable model, missing API key, or a provider whose endpoint has
// no known OpenAI-compatible surface (see the provider-type notes above).
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

	// Vision capability gate: the registry resolves the bare model against
	// user overrides, built-ins, fuzzy matches, and optimistic fallbacks
	// (unknown models are assumed vision-capable — see ResolveLocal).
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
		// chatgpt (official OpenAI) or a user-declared OpenAI-compatible
		// endpoint — both speak Chat Completions by definition.
		baseURL = expandEnv(prov.BaseURL)
		if baseURL == "" {
			baseURL = visionOpenAIDefaultBaseURL
		}
	case visionProviderTypeAnthropic:
		// A custom BaseURL marks an anthropic_compatible proxy (Anthropic
		// Messages API) — no OpenAI-compatible surface can be assumed.
		if expandEnv(prov.BaseURL) != "" {
			return nil
		}
		// The native provider: route through Anthropic's official OpenAI SDK
		// compatibility layer.
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
