package backend

import (
	"testing"

	"github.com/v0lka/c0wrk/backend/config"
	"github.com/v0lka/c0wrk/backend/providerauth"
)

func TestToBuilderConfigKimiAuthModes(t *testing.T) {
	cfg := subscriptionAuthFixture()
	cfg.LLM.Kimi = config.KimiConfig{APIKey: "moonshot-key", Models: []string{"kimi-k3"}}

	for _, mode := range []string{"", config.KimiAuthModeAPIKey} {
		cfg.LLM.Kimi.Auth.Mode = mode
		got := ToBuilderConfig(cfg, config.PredefinedModelProfiles()).LLM.ProviderConfigs["kimi"]
		if got.SubscriptionAuth || got.BaseURL != config.KimiAPIKeyBaseURL || got.APIKey != "moonshot-key" || got.ProviderType != "openai" {
			t.Errorf("api_key mode %q entry = %+v", mode, got)
		}
	}

	cfg.LLM.Kimi.Auth.Mode = config.KimiAuthModeOAuth
	cfg.LLM.Kimi.Models = []string{"k3"}
	got := ToBuilderConfig(cfg, config.PredefinedModelProfiles()).LLM.ProviderConfigs["kimi"]
	if !got.SubscriptionAuth || got.BaseURL != providerauth.Kimi().APIBaseURL {
		t.Errorf("oauth entry = %+v, want marker and pinned endpoint", got)
	}
	if got.APIKey != "moonshot-key" {
		t.Errorf("static key = %q, want retained but overridden on wire", got.APIKey)
	}
	if sibling := ToBuilderConfig(cfg, config.PredefinedModelProfiles()).LLM.ProviderConfigs["chatgpt"]; sibling.SubscriptionAuth {
		t.Error("Kimi oauth mode marked ChatGPT as subscription auth")
	}
}

func TestKimiLegacyCompatibleProviderPreissueCoexistence(t *testing.T) {
	tests := []struct {
		name            string
		openAI          bool
		anthropic       bool
		wantType        string
		wantLegacyCount int
		wantBaseURL     string
		wantAPIKey      string
	}{
		{name: "openai", openAI: true, wantType: "openai", wantLegacyCount: 1, wantBaseURL: "https://openai.legacy.test/v1", wantAPIKey: "legacy-openai-key"},
		{name: "anthropic", anthropic: true, wantType: "anthropic", wantLegacyCount: 1, wantBaseURL: "https://anthropic.legacy.test", wantAPIKey: "legacy-anthropic-key"},
		{name: "both", openAI: true, anthropic: true, wantType: "openai", wantLegacyCount: 2, wantBaseURL: "https://anthropic.legacy.test", wantAPIKey: "legacy-anthropic-key"},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			cfg := subscriptionAuthFixture()
			cfg.LLM.Kimi = config.KimiConfig{
				APIKey: "fixed-key", Models: []string{"fixed-model"},
				Auth: config.KimiAuthConfig{Mode: config.KimiAuthModeOAuth},
			}
			cfg.LLM.OpenAICompatible = map[string]config.OpenAICompatibleConfig{}
			cfg.LLM.AnthropicCompatible = map[string]config.AnthropicCompatibleConfig{}
			if tt.openAI {
				cfg.LLM.OpenAICompatible["kimi"] = config.OpenAICompatibleConfig{
					BaseURL: "https://openai.legacy.test/v1", APIKey: "legacy-openai-key", Models: []string{"legacy-openai-model"},
				}
			}
			if tt.anthropic {
				cfg.LLM.AnthropicCompatible["kimi"] = config.AnthropicCompatibleConfig{
					BaseURL: "https://anthropic.legacy.test", APIKey: "legacy-anthropic-key", Models: []string{"legacy-anthropic-model"},
				}
			}

			var legacy []config.ProviderWithModels
			for _, provider := range cfg.LLM.GetAllProviderConfigs() {
				if provider.Name == "kimi" {
					legacy = append(legacy, provider)
				}
				if provider.Name == "kimi" && len(provider.Models) == 1 && provider.Models[0] == "fixed-model" {
					t.Errorf("fixed Kimi entry shadows a legacy-compatible config: %+v", provider)
				}
			}
			if len(legacy) != tt.wantLegacyCount {
				t.Fatalf("legacy entries = %d, want %d: %+v", len(legacy), tt.wantLegacyCount, legacy)
			}
			if legacy[0].ProviderType != tt.wantType {
				t.Errorf("first legacy provider type = %q, want %q", legacy[0].ProviderType, tt.wantType)
			}

			got := ToBuilderConfig(cfg, config.PredefinedModelProfiles()).LLM.ProviderConfigs["kimi"]
			if got.ProviderType != tt.wantType || got.BaseURL != tt.wantBaseURL || got.APIKey != tt.wantAPIKey {
				t.Errorf("adapter legacy entry = %+v", got)
			}
			if got.SubscriptionAuth {
				t.Errorf("legacy entry was marked as fixed OAuth: %+v", got)
			}
			for _, model := range got.Models {
				if model == "fixed-model" {
					t.Errorf("fixed models overwrote legacy models: %+v", got.Models)
				}
			}
		})
	}
}

func TestToBuilderConfigKimiRegions(t *testing.T) {
	for _, tc := range []struct {
		region  string
		baseURL string
	}{
		{region: config.KimiRegionGlobal, baseURL: "https://api.kimi.ai/coding/v1"},
		{region: config.KimiRegionMainlandCN, baseURL: "https://api.kimi.com/coding/v1"},
	} {
		t.Run(tc.region, func(t *testing.T) {
			cfg := subscriptionAuthFixture()
			cfg.LLM.Kimi = config.KimiConfig{
				APIKey: "moonshot-key", Models: []string{"k3"},
				Auth: config.KimiAuthConfig{Mode: config.KimiAuthModeOAuth, Region: tc.region},
			}
			got := ToBuilderConfig(cfg, config.PredefinedModelProfiles()).LLM.ProviderConfigs["kimi"]
			if !got.SubscriptionAuth || got.BaseURL != tc.baseURL {
				t.Errorf("OAuth region entry = %+v, want base URL %q", got, tc.baseURL)
			}
		})
	}
}
