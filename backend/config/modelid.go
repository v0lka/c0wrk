package config

import (
	"errors"
	"fmt"

	"github.com/v0lka/sp4rk/llm"
)

// Config-layer composite model identifier resolution. The pure string
// helpers (CompositeModelID, ParseCompositeModelID, IsCompositeModelID,
// BareModel) live in github.com/v0lka/sp4rk/llm and are reused here; this
// file adds only the provider-set resolution (ResolveModelID, AllModelIDs).
// A composite identifier is "providerName/modelName" — the provider name is
// the config key, the model name may itself contain "/", so ids split on
// the FIRST "/" only (specs/domains/llm-providers.md).

// ResolveModelID resolves a bare or composite model identifier to its
// canonical composite form ("providerName/modelName"). A composite id is
// validated against the configured providers and returned as-is; a bare name
// is scanned for in deterministic provider order (anthropic, chatgpt, then
// openai_compatible keys sorted alphabetically) — the first match wins, and
// ambiguous reports that more than one provider exposes it (callers should
// log a warning so users can switch to an explicit composite identifier).
// Errors when the model is not enabled in any provider.
func (c *LLMConfig) ResolveModelID(id string) (compositeID string, ambiguous bool, err error) {
	if id == "" {
		return "", false, errors.New("model identifier is empty")
	}

	// Composite input: validate the named provider exposes the named model.
	if provider, model, ok := llm.ParseCompositeModelID(id); ok {
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
				matches = append(matches, llm.CompositeModelID(p.name, m))
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
			ids = append(ids, llm.CompositeModelID(p.name, m))
		}
	}
	return ids
}
