package llm

import "strings"

// Composite model identifier helpers.
//
// A composite model identifier has the form "providerName/modelName" and is the
// internal SELECTOR used to route a request to a specific (provider, model)
// pair. This disambiguates models that share the same bare name across multiple
// OpenAI-compatible providers (e.g. "openai/gpt-4" vs "lmstudio/gpt-4").
//
// The bare model name (the part after the first "/") is what is sent to the LLM
// API and used for model metadata lookups. Model names may themselves contain a
// "/" (e.g. "meta-llama/Llama-3-70b"), so identifiers are always split on the
// FIRST "/" only.

// CompositeModelID builds a composite model identifier from a provider name and
// a bare model name.
func CompositeModelID(provider, model string) string {
	return provider + "/" + model
}

// ParseCompositeModelID splits a composite identifier into its provider and bare
// model name. When id has no "/", it is a bare name: provider is "", model is
// id, and ok is false. When id has a "/", it is split on the first "/" only.
func ParseCompositeModelID(id string) (provider, model string, ok bool) {
	if idx := strings.Index(id, "/"); idx >= 0 {
		return id[:idx], id[idx+1:], true
	}
	return "", id, false
}

// IsCompositeModelID reports whether id carries a provider prefix.
func IsCompositeModelID(id string) bool {
	_, _, ok := ParseCompositeModelID(id)
	return ok
}

// BareModel returns the bare model name portion of a composite identifier. If id
// is already bare (no "/"), it is returned unchanged.
func BareModel(id string) string {
	_, model, _ := ParseCompositeModelID(id)
	return model
}

// providerOf returns the provider name portion of a composite identifier, or ""
// when id is a bare model name.
func providerOf(id string) string {
	provider, _, _ := ParseCompositeModelID(id)
	return provider
}
