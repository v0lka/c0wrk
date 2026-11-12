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

// ResolveModelID validates a model identifier against the configured
// providers and returns its canonical composite form
// ("providerName/modelName"). The identifier MUST be a full composite id —
// bare names are rejected (ADR-122): the provider is part of the model's
// identity, and the same bare name may be exposed by several providers. The
// model name may itself contain "/": ids split on the FIRST "/" only
// (specs/domains/llm-providers.md). Errors when the provider is not
// configured or the named model is not enabled in it. The ambiguous return
// is always false (kept for signature stability) — a composite id is
// unambiguous by construction.
func (c *LLMConfig) ResolveModelID(id string) (compositeID string, ambiguous bool, err error) {
	if id == "" {
		return "", false, errors.New("model identifier is empty")
	}

	provider, model, ok := llm.ParseCompositeModelID(id)
	if !ok {
		return "", false, fmt.Errorf("model identifier %q must be a full \"provider/model\" id — bare model names are not accepted", id)
	}
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
