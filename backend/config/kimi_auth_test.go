package config

import (
	"strings"
	"testing"

	"gopkg.in/yaml.v3"
)

func TestKimiAuthModeDefaultsAndValidation(t *testing.T) {
	cfg := minimalValidChatGPTConfig(t)
	if got := cfg.LLM.Kimi.Auth.Mode; got != KimiAuthModeAPIKey {
		t.Errorf("default Kimi auth mode = %q, want api_key", got)
	}
	for _, mode := range []string{"", KimiAuthModeAPIKey, KimiAuthModeOAuth} {
		cfg.LLM.Kimi.Auth.Mode = mode
		if err := validate(cfg); err != nil {
			t.Errorf("validate mode %q: %v", mode, err)
		}
	}
	cfg.LLM.Kimi.Auth.Mode = "OAuth"
	err := validate(cfg)
	if err == nil || !strings.Contains(err.Error(), "llm.kimi.auth.mode") {
		t.Fatalf("invalid Kimi mode error = %v", err)
	}
}

func TestKimiConfigYAMLRoundTrip(t *testing.T) {
	original := minimalValidChatGPTConfig(t)
	original.LLM.Kimi = KimiConfig{
		APIKey: "secret", Models: []string{"k3"},
		Auth: KimiAuthConfig{Mode: KimiAuthModeOAuth}, OutputTokenReserve: 4096,
	}
	data, err := yaml.Marshal(original)
	if err != nil {
		t.Fatal(err)
	}
	var restored Config
	if err := yaml.Unmarshal(data, &restored); err != nil {
		t.Fatal(err)
	}
	got := restored.LLM.Kimi
	if got.APIKey != "secret" || got.Auth.Mode != KimiAuthModeOAuth || got.OutputTokenReserve != 4096 || len(got.Models) != 1 || got.Models[0] != "k3" {
		t.Errorf("round-tripped Kimi config = %+v", got)
	}
}

func TestKimiRegionDefaultsAndValidation(t *testing.T) {
	cfg := minimalValidChatGPTConfig(t)
	ApplyDefaults(cfg)
	if got := cfg.LLM.Kimi.Auth.Region; got != KimiRegionGlobal {
		t.Errorf("default Kimi region = %q, want global", got)
	}
	for _, region := range []string{KimiRegionGlobal, KimiRegionMainlandCN} {
		cfg.LLM.Kimi.Auth.Region = region
		if err := validate(cfg); err != nil {
			t.Errorf("validate region %q: %v", region, err)
		}
	}
	cfg.LLM.Kimi.Auth.Region = "europe"
	if err := validate(cfg); err == nil || !strings.Contains(err.Error(), "llm.kimi.auth.region") {
		t.Fatalf("invalid Kimi region error = %v", err)
	}
}

func TestKimiConfigYAMLRoundTripRegion(t *testing.T) {
	original := minimalValidChatGPTConfig(t)
	original.LLM.Kimi.Auth.Region = KimiRegionMainlandCN
	data, err := yaml.Marshal(original)
	if err != nil {
		t.Fatal(err)
	}
	var restored Config
	if err := yaml.Unmarshal(data, &restored); err != nil {
		t.Fatal(err)
	}
	if got := restored.LLM.Kimi.Auth.Region; got != KimiRegionMainlandCN {
		t.Errorf("round-tripped Kimi region = %q, want mainland_cn", got)
	}
}
