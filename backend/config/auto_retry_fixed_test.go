package config

import (
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func TestFixedAutoRetrySeconds(t *testing.T) {
	for _, provider := range []string{"anthropic", "chatgpt", "kimi"} {
		for _, seconds := range []int{0, 30, MaxAutoRetrySeconds(), -1, MaxAutoRetrySeconds() + 1} {
			t.Run(fmt.Sprintf("%s/%d", provider, seconds), func(t *testing.T) {
				path := filepath.Join(t.TempDir(), "config.yaml")
				content := fmt.Sprintf("llm:\n  default_model: %s/test-model\n  %s:\n    api_key: k\n    models: [test-model]\n    auto_retry_seconds: %d\n", provider, provider, seconds)
				if err := os.WriteFile(path, []byte(content), 0o600); err != nil {
					t.Fatal(err)
				}
				cfg, err := Load(path)
				if seconds < 0 || seconds > MaxAutoRetrySeconds() {
					if err == nil {
						t.Errorf("Load(%s, %d) succeeded, want bounds error", provider, seconds)
					}
					return
				}
				if err != nil {
					t.Fatalf("Load(%s, %d): %v", provider, seconds, err)
				}
				check := func(cfg *Config) {
					t.Helper()
					found := false
					for _, p := range cfg.LLM.GetAllProviderConfigs() {
						if p.Name != provider {
							continue
						}
						found = true
						if p.AutoRetrySeconds != seconds {
							t.Errorf("GetAllProviderConfigs(%s) interval = %d, want %d", provider, p.AutoRetrySeconds, seconds)
						}
					}
					if !found {
						t.Errorf("provider %q missing from GetAllProviderConfigs", provider)
					}
				}
				check(cfg)
				if err := Save(cfg, path); err != nil {
					t.Fatal(err)
				}
				raw, err := os.ReadFile(path)
				if err != nil {
					t.Fatal(err)
				}
				wantKeys := 1
				if seconds == 0 {
					wantKeys = 0
				}
				// The count anchors on a leading space so the section-level
				// llm.network_auto_retry_seconds key (materialized by
				// ApplyDefaults, ADR-128) cannot ride the substring.
				if got := strings.Count(string(raw), " auto_retry_seconds:"); got != wantKeys {
					t.Errorf("Save(%s, %d) interval keys = %d, want %d\n%s", provider, seconds, got, wantKeys, raw)
				}
				loaded, err := Load(path)
				if err != nil {
					t.Fatal(err)
				}
				check(loaded)
			})
		}
		t.Run(provider+"/omitted", func(t *testing.T) {
			cfg := loadFromYAML(t, fmt.Sprintf("llm:\n  default_model: %s/test-model\n  %s:\n    api_key: k\n    models: [test-model]\n", provider, provider))
			for _, p := range cfg.LLM.GetAllProviderConfigs() {
				if p.AutoRetrySeconds != 0 {
					t.Errorf("Load(omitted).%s interval = %d, want 0", p.Name, p.AutoRetrySeconds)
				}
			}
		})
	}
}

func TestFixedAutoRetryHiddenKimiLoadValidation(t *testing.T) {
	path := filepath.Join(t.TempDir(), "config.yaml")
	content := `llm:
  default_model: kimi/legacy
  kimi:
    auto_retry_seconds: -1
  openai_compatible:
    kimi:
      models: [legacy]
      api_key: k
      base_url: https://example.test/v1
      auto_retry_seconds: 30
`
	if err := os.WriteFile(path, []byte(content), 0o600); err != nil {
		t.Fatal(err)
	}
	if _, err := Load(path); err == nil {
		t.Fatal("Load(hidden fixed Kimi interval -1) succeeded, want bounds error")
	}
}
