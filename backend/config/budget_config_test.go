package config

import (
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

// This file pins the config surface of the adaptive per-model LLM request
// budget (ADR-071): the kill-switch default and explicit-off posture, the
// per-model llm.models.<name>.request_timeout bound, and the per-provider
// timeout_class enum — plus the round-trips that keep hand-edited values from
// being dropped by a save.

// loadFromYAML writes content to a temp config.yaml and loads it through the
// normal pipeline (normalize → defaults → validate).
func loadFromYAML(t *testing.T, content string) *Config {
	t.Helper()
	path := filepath.Join(t.TempDir(), "config.yaml")
	if err := os.WriteFile(path, []byte(content), 0o600); err != nil {
		t.Fatalf("write: %v", err)
	}
	result, err := Load(path)
	if err != nil {
		t.Fatalf("Load: %v", err)
	}
	return result
}

const budgetBaseYAML = `llm:
  default_model: "qwen3"
  openai_compatible:
    selfhosted:
      base_url: "http://127.0.0.1:1234/v1"
      api_key: "k"
      models: ["qwen3"]
`

// The kill-switch defaults to ON, and the unset llmRequestTimeout flows
// through as 0 ("no opinion") instead of being coerced to the legacy 600 s —
// the legacy semantics now live inside the budget resolver's kill-switch-off
// path, not in the config.
func TestAdaptiveBudget_DefaultsOnAndNoTimeoutCoercion(t *testing.T) {
	cfg := loadFromYAML(t, budgetBaseYAML)
	if cfg.Timeouts.AdaptiveBudget.Enabled == nil || !*cfg.Timeouts.AdaptiveBudget.Enabled {
		t.Errorf("adaptive_budget.enabled = %v, want default true", cfg.Timeouts.AdaptiveBudget.Enabled)
	}
	if cfg.Timeouts.LLMRequestTimeout != 0 {
		t.Errorf("llmRequestTimeout = %d, want 0 (no opinion; the 0→600 coercion moved into the resolver)", cfg.Timeouts.LLMRequestTimeout)
	}
}

// An explicit `enabled: false` must survive ApplyDefaults (a plain bool would
// be coerced back to true) — the kill-switch-off posture is the byte-for-byte
// pre-ADR-071 behavior.
func TestAdaptiveBudget_ExplicitFalseSurvivesDefaults(t *testing.T) {
	cfg := loadFromYAML(t, budgetBaseYAML+`timeouts:
  adaptive_budget:
    enabled: false
`)
	if cfg.Timeouts.AdaptiveBudget.Enabled == nil || *cfg.Timeouts.AdaptiveBudget.Enabled {
		t.Errorf("adaptive_budget.enabled = %v, want the explicit false", cfg.Timeouts.AdaptiveBudget.Enabled)
	}
}

// The kill-switch round-trips through Save/Load.
func TestAdaptiveBudget_SaveRoundTrip(t *testing.T) {
	cfg := &Config{}
	ApplyDefaults(cfg)
	cfg.LLM.DefaultModel = "qwen3"
	cfg.LLM.OpenAICompatible = map[string]OpenAICompatibleConfig{
		"selfhosted": {BaseURL: "http://127.0.0.1:1234/v1", APIKey: "k", Models: []string{"qwen3"}},
	}
	off := false
	cfg.Timeouts.AdaptiveBudget.Enabled = &off

	path := filepath.Join(t.TempDir(), "config.yaml")
	if err := Save(cfg, path); err != nil {
		t.Fatalf("Save: %v", err)
	}
	loaded, err := Load(path)
	if err != nil {
		t.Fatalf("Load: %v", err)
	}
	if loaded.Timeouts.AdaptiveBudget.Enabled == nil || *loaded.Timeouts.AdaptiveBudget.Enabled {
		t.Errorf("round-tripped adaptive_budget.enabled = %v, want false", loaded.Timeouts.AdaptiveBudget.Enabled)
	}
}

// llm.models.<name>.request_timeout accepts [0, 3600]; 0/unset means "no
// opinion". The bound mirrors the auto_retry_seconds rationale: a positive
// value is armed EXACTLY as configured and never escalated, so an absurd
// value would silently disable the stalled-upstream protection.
func TestModelRequestTimeout_Validation(t *testing.T) {
	for _, tt := range []struct {
		name    string
		value   int
		wantErr bool
	}{
		{"zero is no opinion", 0, false},
		{"boundary 3600", 3600, false},
		{"negative", -1, true},
		{"oversized", 3601, true},
		{"absurdly large", 1_000_000_000, true},
	} {
		t.Run(tt.name, func(t *testing.T) {
			content := fmt.Sprintf(budgetBaseYAML+`  models:
    qwen3:
      request_timeout: %d
`, tt.value)
			path := filepath.Join(t.TempDir(), "config.yaml")
			if err := os.WriteFile(path, []byte(content), 0o600); err != nil {
				t.Fatalf("write: %v", err)
			}
			_, err := Load(path)
			if tt.wantErr && err == nil {
				t.Errorf("Load accepted request_timeout %d, want rejection", tt.value)
			}
			if !tt.wantErr && err != nil {
				t.Errorf("Load rejected request_timeout %d: %v", tt.value, err)
			}
			if !tt.wantErr {
				if got := loadFromYAML(t, content).LLM.Models["qwen3"].RequestTimeout; got != tt.value {
					t.Errorf("request_timeout = %d, want %d", got, tt.value)
				}
			}
		})
	}
}

// request_timeout is omitted from the YAML when unset, so a saved config does
// not grow boilerplate.
func TestModelRequestTimeout_OmitEmpty(t *testing.T) {
	cfg := &Config{}
	ApplyDefaults(cfg)
	cfg.LLM.DefaultModel = "qwen3"
	cfg.LLM.OpenAICompatible = map[string]OpenAICompatibleConfig{
		"selfhosted": {BaseURL: "http://127.0.0.1:1234/v1", APIKey: "k", Models: []string{"qwen3"}},
	}
	cfg.LLM.Models = map[string]ModelOverride{
		"qwen3": {ContextWindow: 262144}, // no request_timeout
	}
	path := filepath.Join(t.TempDir(), "config.yaml")
	if err := Save(cfg, path); err != nil {
		t.Fatalf("Save: %v", err)
	}
	raw, err := os.ReadFile(path)
	if err != nil {
		t.Fatalf("read: %v", err)
	}
	if strings.Contains(string(raw), "request_timeout") {
		t.Error("saved YAML contains request_timeout despite no override — omitempty is broken")
	}
}

// The per-provider timeout_class override accepts exactly the enum —
// "local" | "remote" | unset. The reserved "embedded" class is deliberately
// NOT configurable (that provider's envelope is fixed by its backend-owned
// identity), and an unknown value must be rejected rather than silently
// inferred.
func TestProviderTimeoutClass_Validation(t *testing.T) {
	for _, tt := range []struct {
		value   string
		wantErr bool
	}{
		{"local", false},
		{"remote", false},
		{"embedded", true},
		{"bogus", true},
		{"Local", true}, // strict enum: the resolver tolerates case, config does not
	} {
		t.Run("openai_compatible "+tt.value, func(t *testing.T) {
			content := fmt.Sprintf(budgetBaseYAML+`      timeout_class: %q
`, tt.value)
			path := filepath.Join(t.TempDir(), "config.yaml")
			if err := os.WriteFile(path, []byte(content), 0o600); err != nil {
				t.Fatalf("write: %v", err)
			}
			_, err := Load(path)
			if tt.wantErr && err == nil {
				t.Errorf("Load accepted timeout_class %q, want rejection", tt.value)
			}
			if !tt.wantErr && err != nil {
				t.Errorf("Load rejected timeout_class %q: %v", tt.value, err)
			}
		})
		t.Run("anthropic_compatible "+tt.value, func(t *testing.T) {
			content := fmt.Sprintf(`llm:
  default_model: "claude-sonnet-4-20250514"
  anthropic_compatible:
    gateway:
      base_url: "https://claude.lan:8443"
      api_key: "k"
      models: ["claude-sonnet-4-20250514"]
      timeout_class: %q
`, tt.value)
			path := filepath.Join(t.TempDir(), "config.yaml")
			if err := os.WriteFile(path, []byte(content), 0o600); err != nil {
				t.Fatalf("write: %v", err)
			}
			_, err := Load(path)
			if tt.wantErr && err == nil {
				t.Errorf("Load accepted timeout_class %q, want rejection", tt.value)
			}
			if !tt.wantErr && err != nil {
				t.Errorf("Load rejected timeout_class %q: %v", tt.value, err)
			}
		})
	}
}

// timeout_class round-trips through Save/Load and GetAllProviderConfigs.
func TestProviderTimeoutClass_RoundTrip(t *testing.T) {
	cfg := &Config{}
	ApplyDefaults(cfg)
	cfg.LLM.DefaultModel = "qwen3"
	cfg.LLM.OpenAICompatible = map[string]OpenAICompatibleConfig{
		"selfhosted": {BaseURL: "http://127.0.0.1:1234/v1", APIKey: "k", Models: []string{"qwen3"}, TimeoutClass: "local"},
	}
	cfg.LLM.AnthropicCompatible = map[string]AnthropicCompatibleConfig{
		"gateway": {BaseURL: "https://claude.lan:8443", APIKey: "k", Models: []string{"qwen3"}, TimeoutClass: "remote"},
	}
	path := filepath.Join(t.TempDir(), "config.yaml")
	if err := Save(cfg, path); err != nil {
		t.Fatalf("Save: %v", err)
	}
	loaded, err := Load(path)
	if err != nil {
		t.Fatalf("Load: %v", err)
	}
	if got := loaded.LLM.OpenAICompatible["selfhosted"].TimeoutClass; got != "local" {
		t.Errorf("openai timeout_class = %q, want local", got)
	}
	if got := loaded.LLM.AnthropicCompatible["gateway"].TimeoutClass; got != "remote" {
		t.Errorf("anthropic timeout_class = %q, want remote", got)
	}
	for _, p := range loaded.LLM.GetAllProviderConfigs() {
		if p.Name == "selfhosted" && p.TimeoutClass != "local" {
			t.Errorf("GetAllProviderConfigs[%s].TimeoutClass = %q, want local", p.Name, p.TimeoutClass)
		}
	}
}
