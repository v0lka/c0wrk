package core

import (
	"testing"

	"github.com/v0lka/c0wrk/core/markitdown"
	"github.com/v0lka/sp4rk/llm"
)

// visionTestProviders builds a provider map covering every mapping branch:
// native anthropic (no BaseURL), chatgpt/openai (no BaseURL), a custom
// OpenAI-compatible endpoint, and an anthropic_compatible proxy (anthropic
// type WITH BaseURL).
func visionTestProviders(apiKey string) map[string]BuilderProviderConfig {
	return map[string]BuilderProviderConfig{
		"anthropic": {ProviderType: "anthropic", APIKey: apiKey, Models: []string{"claude-x"}},
		"chatgpt":   {ProviderType: "openai", APIKey: apiKey, Models: []string{"gpt-x"}},
		"lmstudio":  {ProviderType: "openai", APIKey: apiKey, BaseURL: "http://localhost:1234/v1", Models: []string{"local-vision"}},
		"proxy":     {ProviderType: "anthropic", APIKey: apiKey, BaseURL: "https://proxy.example/api/anthropic", Models: []string{"claude-x"}},
		"nokey":     {ProviderType: "openai", APIKey: "", Models: []string{"gpt-x"}},
	}
}

// newVisionRegistry builds a registry with user overrides covering the
// capability matrix: forced vision on, forced vision off, and inherit
// (built-in / optimistic fallback).
func newVisionRegistry() *llm.ModelRegistry {
	off := false
	on := true
	// gpt-x resolves via the optimistic unknown-model fallback
	// (Attachment=true); claude-x and local-vision are pinned explicitly.
	return llm.NewModelRegistry(map[string]llm.ModelMetadata{
		"claude-x":     {Capabilities: &llm.ModelCapabilities{Attachment: on}},
		"local-vision": {Capabilities: &llm.ModelCapabilities{Attachment: on}},
		"text-only":    {Capabilities: &llm.ModelCapabilities{Attachment: off}},
	})
}

// TestResolveMarkitdownVisionOptions verifies the full mapping matrix:
// provider type → endpoint derivation, vision capability gate, and the
// nil-return conditions (missing key, anthropic_compatible proxy,
// non-vision model).
func TestResolveMarkitdownVisionOptions(t *testing.T) {
	const key = "sk-test"
	providers := visionTestProviders(key)
	reg := newVisionRegistry()
	expand := func(s string) string { return s }

	tests := []struct {
		name         string
		activeModel  string
		providerName string
		wantNil      bool
		wantBaseURL  string
		wantModel    string
	}{
		{"native anthropic routes to compat layer", "anthropic/claude-x", "anthropic", false, "https://api.anthropic.com/v1", "claude-x"},
		{"chatgpt defaults to official openai root", "chatgpt/gpt-x", "chatgpt", false, "https://api.openai.com/v1", "gpt-x"},
		{"openai compatible keeps custom base url", "lmstudio/local-vision", "lmstudio", false, "http://localhost:1234/v1", "local-vision"},
		{"anthropic compatible proxy yields nil", "proxy/claude-x", "proxy", true, "", ""},
		{"non-vision model yields nil", "lmstudio/text-only", "lmstudio", true, "", ""},
		{"missing api key yields nil", "chatgpt/gpt-x", "nokey", true, "", ""},
		{"unknown provider yields nil", "chatgpt/gpt-x", "ghost", true, "", ""},
		{"empty model yields nil", "", "chatgpt", true, "", ""},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got := resolveMarkitdownVisionOptions(tt.activeModel, tt.providerName, providers, reg, expand)
			if tt.wantNil {
				if got != nil {
					t.Fatalf("expected nil options, got %+v", got)
				}
				return
			}
			if got == nil {
				t.Fatal("expected non-nil options, got nil")
			}
			if got.BaseURL != tt.wantBaseURL {
				t.Errorf("BaseURL = %q, want %q", got.BaseURL, tt.wantBaseURL)
			}
			if got.Model != tt.wantModel {
				t.Errorf("Model = %q, want %q", got.Model, tt.wantModel)
			}
			if got.APIKey != key {
				t.Errorf("APIKey = %q, want %q", got.APIKey, key)
			}
		})
	}
}

// TestNewMarkitdownVisionResolver_PerDocument re-resolves through a REAL
// router after SetModel: the resolver closure must reflect the newly active
// model (per-document semantics), not a cached snapshot.
func TestNewMarkitdownVisionResolver_PerDocument(t *testing.T) {
	const key = "sk-test"
	cfg := &BuilderConfig{
		LLM: BuilderLLMConfig{
			ProviderConfigs: visionTestProviders(key),
		},
		ExpandEnvVars: func(s string) string { return s },
	}
	reg := newVisionRegistry()

	router, err := llm.NewRouter(t.Context(), llm.RouterConfig{
		Providers: []llm.ProviderEntry{
			{Name: "anthropic", ProviderType: "anthropic", APIKey: key, Models: []string{"claude-x"}},
			{Name: "lmstudio", ProviderType: "openai", APIKey: key, BaseURL: "http://localhost:1234/v1", Models: []string{"local-vision"}},
		},
	}, reg)
	if err != nil {
		t.Fatalf("NewRouter: %v", err)
	}

	resolver := newMarkitdownVisionResolver(router, reg, cfg)
	if resolver == nil {
		t.Fatal("expected non-nil resolver for fully-wired inputs")
	}

	// First provider's first model is active by default.
	if got := resolver(); got == nil || got.Model != "claude-x" {
		t.Fatalf("initial resolve = %+v, want model claude-x", got)
	}

	// Switch mid-session: the NEXT resolver call must see the new model.
	if err := router.SetModel(t.Context(), "lmstudio/local-vision"); err != nil {
		t.Fatalf("SetModel: %v", err)
	}
	got := resolver()
	if got == nil {
		t.Fatal("resolve after switch = nil, want local-vision options")
	}
	if got.Model != "local-vision" || got.BaseURL != "http://localhost:1234/v1" {
		t.Errorf("resolve after switch = %+v, want {local-vision http://localhost:1234/v1}", got)
	}
}

// TestNewMarkitdownVisionResolver_NilDeps verifies the fail-closed disable:
// any missing dependency yields a nil resolver (no vision assistance).
func TestNewMarkitdownVisionResolver_NilDeps(t *testing.T) {
	cfg := &BuilderConfig{}
	if r := newMarkitdownVisionResolver(nil, newVisionRegistry(), cfg); r != nil {
		t.Error("nil router must yield nil resolver")
	}
	if r := newMarkitdownVisionResolver(&llm.Router{}, nil, cfg); r != nil {
		t.Error("nil registry must yield nil resolver")
	}
	if r := newMarkitdownVisionResolver(&llm.Router{}, newVisionRegistry(), nil); r != nil {
		t.Error("nil config must yield nil resolver")
	}
}

// TestVisionOptionsCacheKey verifies the cache-key contract: complete options
// produce a stable non-empty key; nil/incomplete produce "" (shared plain
// cache), and different models produce different keys.
func TestVisionOptionsCacheKey(t *testing.T) {
	var nilOpts *markitdown.VisionOptions
	if got := nilOpts.CacheKey(); got != "" {
		t.Errorf("nil options CacheKey = %q, want \"\"", got)
	}
	incomplete := &markitdown.VisionOptions{APIKey: "k", Model: "m"} // no BaseURL
	if got := incomplete.CacheKey(); got != "" {
		t.Errorf("incomplete options CacheKey = %q, want \"\"", got)
	}

	a := &markitdown.VisionOptions{APIKey: "k", BaseURL: "https://x/v1", Model: "m1"}
	b := &markitdown.VisionOptions{APIKey: "k", BaseURL: "https://x/v1", Model: "m2"}
	first := a.CacheKey()
	if first == "" || first != a.CacheKey() {
		t.Error("complete options must yield a stable non-empty key")
	}
	if a.CacheKey() == b.CacheKey() {
		t.Error("different models must yield different cache keys")
	}
}
