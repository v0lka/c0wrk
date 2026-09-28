package backend

import (
	"testing"

	"github.com/v0lka/c0wrk/backend/config"
)

// The adaptive request budget keys (ADR-071) must reach the BuilderConfig:
// the per-provider timeout_class, the per-model request_timeout, and the
// kill-switch — defaulting a programmatic (defaults-bypassing) config to the
// enabled-by-default posture.
func TestToBuilderConfig_AdaptiveBudgetKeys(t *testing.T) {
	cfg := &config.Config{}
	config.ApplyDefaults(cfg)
	cfg.LLM.DefaultModel = "qwen3"
	cfg.LLM.OpenAICompatible = map[string]config.OpenAICompatibleConfig{
		"selfhosted": {
			BaseURL:      "http://127.0.0.1:1234/v1",
			APIKey:       "k",
			Models:       []string{"qwen3"},
			TimeoutClass: "local",
		},
	}
	cfg.LLM.Models = map[string]config.ModelOverride{
		"qwen3": {RequestTimeout: 1800},
	}

	bc := ToBuilderConfig(cfg, config.PredefinedModelProfiles())

	if got := bc.LLM.ProviderConfigs["selfhosted"].TimeoutClass; got != "local" {
		t.Errorf("selfhosted TimeoutClass = %q, want local", got)
	}
	if got := bc.LLM.Models["qwen3"].RequestTimeout; got != 1800 {
		t.Errorf("qwen3 RequestTimeout = %d, want 1800", got)
	}
	if !bc.Timeouts.AdaptiveBudgetEnabled {
		t.Error("AdaptiveBudgetEnabled = false, want the default true")
	}
	if bc.Timeouts.LLMRequestTimeout != 0 {
		t.Errorf("LLMRequestTimeout = %d, want 0 (no opinion under the adaptive budget)", bc.Timeouts.LLMRequestTimeout)
	}

	// An explicit kill-switch-off must map through verbatim, and a
	// defaults-bypassing config (nil Enabled) must default to enabled.
	off := false
	cfg.Timeouts.AdaptiveBudget.Enabled = &off
	bc = ToBuilderConfig(cfg, config.PredefinedModelProfiles())
	if bc.Timeouts.AdaptiveBudgetEnabled {
		t.Error("AdaptiveBudgetEnabled = true, want the explicit false")
	}

	bc = ToBuilderConfig(&config.Config{LLM: config.LLMConfig{
		DefaultModel: "m",
		OpenAICompatible: map[string]config.OpenAICompatibleConfig{
			"p": {BaseURL: "http://127.0.0.1:1/v1", Models: []string{"m"}},
		},
	}}, config.PredefinedModelProfiles())
	if !bc.Timeouts.AdaptiveBudgetEnabled {
		t.Error("AdaptiveBudgetEnabled = false for a defaults-bypassing config, want the nil→true default")
	}
}
