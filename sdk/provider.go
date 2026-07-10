package sp4rk

import "github.com/v0lka/sp4rk/llm"

// Anthropic returns a [llm.ProviderEntry] for the Anthropic API with the given
// API key and enabled models.
//
//	sp4rk.Anthropic(os.Getenv("ANTHROPIC_API_KEY"), "claude-sonnet-4-5", "claude-haiku-3-5")
//
// The returned entry is exactly what [LLMConfig] expects — pass it to the
// FrameworkBuilder via [FrameworkBuilder.Provider]/[FrameworkBuilder.Anthropic]
// or use it directly in a classic [Config].
func Anthropic(apiKey string, models ...string) llm.ProviderEntry {
	return llm.ProviderEntry{
		Name:         "anthropic",
		ProviderType: "anthropic",
		APIKey:       apiKey,
		Models:       models,
	}
}

// OpenAI returns a [llm.ProviderEntry] for the OpenAI API with the given API
// key and enabled models.
//
//	sp4rk.OpenAI(os.Getenv("OPENAI_API_KEY"), "gpt-4o", "gpt-4o-mini")
func OpenAI(apiKey string, models ...string) llm.ProviderEntry {
	return llm.ProviderEntry{
		Name:         "openai",
		ProviderType: "openai",
		APIKey:       apiKey,
		Models:       models,
	}
}

// OpenAICompatible returns a [llm.ProviderEntry] for an OpenAI-compatible API
// (e.g. Together, Groq, a local vLLM/Ollama endpoint) reachable at baseURL.
//
// The logical name distinguishes this provider from a built-in OpenAI entry
// when several providers are configured simultaneously.
//
//	sp4rk.OpenAICompatible("groq", "https://api.groq.com/openai/v1", key, "llama-3.3-70b-versatile")
func OpenAICompatible(name, baseURL, apiKey string, models ...string) llm.ProviderEntry {
	return llm.ProviderEntry{
		Name:         name,
		ProviderType: "openai",
		APIKey:       apiKey,
		BaseURL:      baseURL,
		Models:       models,
	}
}

// ─── FrameworkBuilder methods ───────────────────────────────────────────────

// Anthropic appends an Anthropic provider entry. Repeatable to register multiple
// providers. Equivalent to [FrameworkBuilder.Provider]([Anthropic]).
func (b *FrameworkBuilder) Anthropic(apiKey string, models ...string) *FrameworkBuilder {
	b.opts.providers = append(b.opts.providers, Anthropic(apiKey, models...))
	return b
}

// OpenAI appends an OpenAI provider entry. Repeatable.
func (b *FrameworkBuilder) OpenAI(apiKey string, models ...string) *FrameworkBuilder {
	b.opts.providers = append(b.opts.providers, OpenAI(apiKey, models...))
	return b
}

// OpenAICompatible appends an OpenAI-compatible provider (e.g. Together, Groq,
// a local vLLM/Ollama endpoint) reachable at baseURL.
func (b *FrameworkBuilder) OpenAICompatible(name, baseURL, apiKey string, models ...string) *FrameworkBuilder {
	b.opts.providers = append(b.opts.providers, OpenAICompatible(name, baseURL, apiKey, models...))
	return b
}

// Provider appends a single pre-built [llm.ProviderEntry]. Use this for custom
// provider types not covered by the dedicated methods.
func (b *FrameworkBuilder) Provider(p llm.ProviderEntry) *FrameworkBuilder {
	b.opts.providers = append(b.opts.providers, p)
	return b
}

// Providers appends a set of provider entries at once. Useful when providers are
// assembled conditionally before the chain.
func (b *FrameworkBuilder) Providers(ps ...llm.ProviderEntry) *FrameworkBuilder {
	b.opts.providers = append(b.opts.providers, ps...)
	return b
}

// DefaultModel overrides the auto-selected default model. Accepts a bare name
// ("claude-sonnet-4-5") or composite ID ("anthropic/claude-sonnet-4-5").
func (b *FrameworkBuilder) DefaultModel(model string) *FrameworkBuilder {
	b.opts.defaultModel = model
	return b
}
