package config

import (
	"github.com/v0lka/sp4rk/llm"
)

// Config-layer composite model identifier resolution. The pure string
// helpers (CompositeModelID, ParseCompositeModelID, IsCompositeModelID,
// BareModel) live in github.com/v0lka/sp4rk/llm and are reused here; this
// file adds only the provider-set enumeration (AllModelIDs). A composite
// identifier is "providerName/modelName" — the provider name is the config
// key, the model name may itself contain "/", so ids split on the FIRST "/"
// only (specs/domains/llm-providers.md).

// AllModelIDs returns the composite identifiers for every enabled model across
// all configured providers, in deterministic provider order. Two providers
// exposing the same bare model name produce two distinct composite identifiers.
// Membership in this enumeration is the resolution predicate: a composite id
// is resolvable exactly when it is listed here (provider configured AND model
// enabled in it).
func (c *LLMConfig) AllModelIDs() []string {
	var ids []string
	for _, p := range c.allProviderEntries() {
		for _, m := range p.models {
			ids = append(ids, llm.CompositeModelID(p.name, m))
		}
	}
	return ids
}
