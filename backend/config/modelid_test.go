package config

import "testing"

// TestResolveModelID_TwoProvidersSameName is the core disambiguation test at
// the config layer: two OpenAI-compatible providers exposing "gpt-4" must
// resolve to distinct composite identifiers.
func TestResolveModelID_TwoProvidersSameName(t *testing.T) {
	cfg := LLMConfig{
		DefaultModel: "openai/gpt-4",
		OpenAICompatible: map[string]OpenAICompatibleConfig{
			"openai":   {APIKey: "k1", BaseURL: "https://api.openai.com/v1", Models: []string{"gpt-4"}},
			"lmstudio": {APIKey: "", BaseURL: "http://localhost:1234/v1", Models: []string{"gpt-4"}},
		},
	}

	// Composite inputs resolve directly to the named provider.
	id, amb, err := cfg.ResolveModelID("openai/gpt-4")
	if err != nil || amb || id != "openai/gpt-4" {
		t.Errorf("ResolveModelID(openai/gpt-4) = (%q,%v,%v), want (openai/gpt-4,false,nil)", id, amb, err)
	}
	id, amb, err = cfg.ResolveModelID("lmstudio/gpt-4")
	if err != nil || amb || id != "lmstudio/gpt-4" {
		t.Errorf("ResolveModelID(lmstudio/gpt-4) = (%q,%v,%v), want (lmstudio/gpt-4,false,nil)", id, amb, err)
	}

	// Bare input is ambiguous: resolves to first match with ambiguous=true.
	id, amb, err = cfg.ResolveModelID("gpt-4")
	if err != nil {
		t.Fatalf("ResolveModelID(gpt-4) error: %v", err)
	}
	if !amb {
		t.Error("expected ambiguous=true for bare gpt-4 across two providers")
	}
	// allProviderEntries order: anthropic, chatgpt, then openai_compatible keys
	// sorted → "lmstudio" < "openai", so first match is lmstudio.
	if id != "lmstudio/gpt-4" {
		t.Errorf("ResolveModelID(gpt-4) first match = %q, want lmstudio/gpt-4", id)
	}

	// AllModelIDs lists both composite identifiers (no bare-name collision).
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

// TestResolveModelID_BackwardCompat_Unambiguous confirms a bare name that is
// unique across providers resolves unambiguously (no warning needed).
func TestResolveModelID_BackwardCompat_Unambiguous(t *testing.T) {
	cfg := LLMConfig{
		DefaultModel: "claude-3-haiku",
		Anthropic:    AnthropicConfig{APIKey: "k", Models: []string{"claude-3-haiku"}},
		ChatGPT:      ChatGPTConfig{APIKey: "k", Models: []string{"gpt-4o"}},
	}
	id, amb, err := cfg.ResolveModelID("gpt-4o")
	if err != nil || amb || id != "chatgpt/gpt-4o" {
		t.Errorf("ResolveModelID(gpt-4o) = (%q,%v,%v), want (chatgpt/gpt-4o,false,nil)", id, amb, err)
	}
}

// TestResolveModelID_Errors covers unknown composite provider, unknown model in
// a known provider, and totally unknown bare model.
func TestResolveModelID_Errors(t *testing.T) {
	cfg := LLMConfig{
		OpenAICompatible: map[string]OpenAICompatibleConfig{
			"openai": {Models: []string{"gpt-4"}},
		},
	}
	if _, _, err := cfg.ResolveModelID("nope/gpt-4"); err == nil {
		t.Error("expected error for unknown composite provider")
	}
	if _, _, err := cfg.ResolveModelID("openai/missing"); err == nil {
		t.Error("expected error for model not enabled in named provider")
	}
	if _, _, err := cfg.ResolveModelID("totally-unknown"); err == nil {
		t.Error("expected error for unknown bare model")
	}
	if _, _, err := cfg.ResolveModelID(""); err == nil {
		t.Error("expected error for empty identifier")
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
