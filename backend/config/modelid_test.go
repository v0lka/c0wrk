package config

import (
	"strings"
	"testing"
)

// TestAllModelIDs_DistinctComposites validates the enabled-composite
// enumeration: two OpenAI-compatible providers exposing "gpt-4" carry
// DISTINCT composite identifiers (no bare-name collision), listed in
// deterministic provider order. Membership in AllModelIDs is the resolution
// predicate — a composite id is resolvable exactly when it is listed here.
func TestAllModelIDs_DistinctComposites(t *testing.T) {
	cfg := LLMConfig{
		DefaultModel: "openai/gpt-4",
		OpenAICompatible: map[string]OpenAICompatibleConfig{
			"openai":   {APIKey: "k1", BaseURL: "https://api.openai.com/v1", Models: []string{"gpt-4"}},
			"lmstudio": {APIKey: "", BaseURL: "http://localhost:1234/v1", Models: []string{"gpt-4"}},
		},
	}

	all := cfg.AllModelIDs()
	want := []string{"lmstudio/gpt-4", "openai/gpt-4"}
	if len(all) != len(want) {
		t.Fatalf("AllModelIDs() = %v, want %v", all, want)
	}
	for i, w := range want {
		if all[i] != w {
			t.Errorf("AllModelIDs()[%d] = %q, want %q", i, all[i], w)
		}
	}
}

// TestResolveDefaultModelProvider_Composite verifies a composite default_model
// resolves to the named provider, returning the bare model name.
func TestResolveDefaultModelProvider_Composite(t *testing.T) {
	cfg := LLMConfig{
		DefaultModel: "lmstudio/gpt-4",
		OpenAICompatible: map[string]OpenAICompatibleConfig{
			"openai":   {APIKey: "k1", Models: []string{"gpt-4"}},
			"lmstudio": {APIKey: "", BaseURL: "http://localhost:1234/v1", Models: []string{"gpt-4"}},
		},
	}
	prov, model, err := cfg.ResolveDefaultModelProvider()
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if prov.Name != "lmstudio" {
		t.Errorf("provider name = %q, want lmstudio", prov.Name)
	}
	if model != "gpt-4" {
		t.Errorf("bare model = %q, want gpt-4", model)
	}
	if prov.ProviderType != "openai" {
		t.Errorf("provider type = %q, want openai", prov.ProviderType)
	}
}

// TestResolveDefaultModelProvider_CompositeUnknownProvider ensures a composite
// default_model pointing at an unconfigured provider fails validation.
func TestResolveDefaultModelProvider_CompositeUnknownProvider(t *testing.T) {
	cfg := LLMConfig{
		DefaultModel: "ghost/gpt-4",
		Anthropic:    AnthropicConfig{Models: []string{"gpt-4"}},
	}
	if _, _, err := cfg.ResolveDefaultModelProvider(); err == nil {
		t.Error("expected error for composite default_model with unknown provider")
	}
}

// TestResolveDefaultModelProvider_BareRejected verifies the mandatory-composite
// rule for default_model: a bare name is rejected outright — even one an
// enabled provider uniquely lists — with the composite guidance in the error.
func TestResolveDefaultModelProvider_BareRejected(t *testing.T) {
	cfg := LLMConfig{
		DefaultModel: "claude-3-haiku",
		Anthropic:    AnthropicConfig{APIKey: "k", Models: []string{"claude-3-haiku"}},
	}
	_, _, err := cfg.ResolveDefaultModelProvider()
	if err == nil {
		t.Fatal("expected error for a bare default_model")
	}
	if !strings.Contains(err.Error(), `must be a full "provider/model" identifier`) {
		t.Errorf("error must carry the composite guidance, got: %v", err)
	}
}
