package sp4rk

import (
	"testing"
)

func TestAnthropic(t *testing.T) {
	got := Anthropic("sk-test", "claude-sonnet-4-5", "claude-haiku-3-5")

	if got.Name != "anthropic" {
		t.Errorf("Name = %q, want %q", got.Name, "anthropic")
	}
	if got.ProviderType != "anthropic" {
		t.Errorf("ProviderType = %q, want %q", got.ProviderType, "anthropic")
	}
	if got.APIKey != "sk-test" {
		t.Errorf("APIKey = %q, want %q", got.APIKey, "sk-test")
	}
	wantModels := []string{"claude-sonnet-4-5", "claude-haiku-3-5"}
	if len(got.Models) != len(wantModels) {
		t.Fatalf("Models len = %d, want %d", len(got.Models), len(wantModels))
	}
	for i, m := range got.Models {
		if m != wantModels[i] {
			t.Errorf("Models[%d] = %q, want %q", i, m, wantModels[i])
		}
	}
}

func TestOpenAI(t *testing.T) {
	got := OpenAI("sk-test", "gpt-4o")

	if got.Name != "openai" || got.ProviderType != "openai" {
		t.Errorf("Name/ProviderType = %q/%q, want openai/openai", got.Name, got.ProviderType)
	}
	if got.APIKey != "sk-test" {
		t.Errorf("APIKey = %q, want sk-test", got.APIKey)
	}
	if len(got.Models) != 1 || got.Models[0] != "gpt-4o" {
		t.Errorf("Models = %v, want [gpt-4o]", got.Models)
	}
}

func TestOpenAICompatible(t *testing.T) {
	got := OpenAICompatible("groq", "https://api.groq.com/openai/v1", "gq-test", "llama-3.3-70b-versatile")

	if got.Name != "groq" {
		t.Errorf("Name = %q, want groq", got.Name)
	}
	// ProviderType must be "openai" so the router uses the OpenAI-compatible client.
	if got.ProviderType != "openai" {
		t.Errorf("ProviderType = %q, want %q", got.ProviderType, "openai")
	}
	if got.BaseURL != "https://api.groq.com/openai/v1" {
		t.Errorf("BaseURL = %q, want the groq URL", got.BaseURL)
	}
	if got.APIKey != "gq-test" {
		t.Errorf("APIKey = %q, want gq-test", got.APIKey)
	}
}
