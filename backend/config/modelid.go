package config

import (
	"errors"
	"fmt"
	"strings"
)

// This file implements the composite model identifier used to disambiguate
// models that share the same bare name across multiple OpenAI-compatible
// providers.
//
// A composite model identifier has the form "providerName/modelName", e.g.
// "openai/gpt-4" or "lmstudio/gpt-4". The provider name is the config key
// ("anthropic", "chatgpt", or a named openai_compatible provider). The model
// name is the bare model name as exposed by the provider's API — it may itself
// contain a "/" (e.g. "meta-llama/Llama-3-70b"), so the identifier is always
// split on the FIRST "/" only.
//
// The composite identifier is the internal SELECTOR: it uniquely identifies a
// (provider, model) pair so that two providers exposing the same model name can
// be distinguished. The bare model name remains what is sent to the LLM API and
// what is shown to the user (display/UI is unchanged).
//
// Backward compatibility: existing configs and sessions store bare model names.
// ResolveModelID resolves a bare name to its composite identifier (first match,
// with an ambiguity flag when multiple providers expose the same name).

// CompositeModelID builds a composite model identifier from a provider name and
// a bare model name.
func CompositeModelID(provider, model string) string {
	return provider + "/" + model
}

// ParseCompositeModelID splits a composite model identifier into its provider
// and bare model name parts. When id does not contain a "/", it is treated as a
// bare model name: the returned provider is empty, model is id, and ok is
// false. When id contains a "/", it is split on the first "/" only so that model
// names containing "/" (e.g. HuggingFace-style "org/model") are preserved.
func ParseCompositeModelID(id string) (provider, model string, ok bool) {
	if idx := strings.Index(id, "/"); idx >= 0 {
		return id[:idx], id[idx+1:], true
	}
	return "", id, false
}

// IsCompositeModelID reports whether id contains a provider prefix (a "/").
func IsCompositeModelID(id string) bool {
	_, _, ok := ParseCompositeModelID(id)
	return ok
}

// BareModel returns the bare model name portion of a composite identifier. If id
// is already a bare name (no "/"), it is returned unchanged.
func BareModel(id string) string {
	_, model, _ := ParseCompositeModelID(id)
	return model
}

// ProviderOf returns the provider name portion of a composite identifier, or the
// empty string when id is a bare model name.
func ProviderOf(id string) string {
	provider, _, _ := ParseCompositeModelID(id)
	return provider
}

// ResolveModelID resolves a bare or composite model identifier to its canonical
// composite form ("providerName/modelName").
//
// When id is already composite ("provider/model"), it is validated against the
// configured providers and returned as-is.
//
// When id is bare, the configured providers are scanned in deterministic order
// (anthropic, chatgpt, then openai_compatible keys sorted alphabetically) for
// the first provider that exposes a model with that name. The returned
// compositeID targets that provider. When more than one provider exposes the
// same bare name, ambiguous is true and the first match is returned (callers
// should log a warning so users can switch to an explicit composite identifier).
//
// Returns an error when the model is not enabled in any provider.
func (c *LLMConfig) ResolveModelID(id string) (compositeID string, ambiguous bool, err error) {
	if id == "" {
		return "", false, errors.New("model identifier is empty")
	}

	// Composite input: validate the named provider exposes the named model.
	if provider, model, ok := ParseCompositeModelID(id); ok {
		for _, p := range c.allProviderEntries() {
			if p.name != provider {
				continue
			}
			for _, m := range p.models {
				if m == model {
					return id, false, nil
				}
			}
			return "", false, fmt.Errorf("model %q is not enabled in provider %q", model, provider)
		}
		return "", false, fmt.Errorf("provider %q is not configured", provider)
	}

	// Bare input: scan providers for matches.
	var matches []string
	for _, p := range c.allProviderEntries() {
		for _, m := range p.models {
			if m == id {
				matches = append(matches, CompositeModelID(p.name, m))
			}
		}
	}
	switch len(matches) {
	case 0:
		return "", false, fmt.Errorf("model %q is not enabled in any provider", id)
	default:
		return matches[0], len(matches) > 1, nil
	}
}

// AllModelIDs returns the composite identifiers for every enabled model across
// all configured providers, in deterministic provider order. Two providers
// exposing the same bare model name produce two distinct composite identifiers.
func (c *LLMConfig) AllModelIDs() []string {
	var ids []string
	for _, p := range c.allProviderEntries() {
		for _, m := range p.models {
			ids = append(ids, CompositeModelID(p.name, m))
		}
	}
	return ids
}
