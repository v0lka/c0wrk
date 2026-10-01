package config

import (
	"strings"
	"testing"

	"gopkg.in/yaml.v3"
)

// minimalValidChatGPTConfig builds the smallest config that passes validate:
// one default model owned by the chatgpt provider, with defaults applied (the
// load pipeline order) so unrelated enums (goal_loop.verification, …) are
// seeded. Auth-mode tests mutate the mode from this baseline.
func minimalValidChatGPTConfig(t *testing.T) *Config {
	t.Helper()
	cfg := &Config{}
	src := `
llm:
  default_model: gpt-4o
  chatgpt:
    api_key: k
    models: ["gpt-4o"]
`
	if err := yaml.Unmarshal([]byte(src), cfg); err != nil {
		t.Fatalf("yaml.Unmarshal() failed: %v", err)
	}
	ApplyDefaults(cfg)
	return cfg
}

// TestChatGPTAuthMode_DefaultIsAPIKey pins the default posture: an omitted
// auth section is seeded with "api_key" by ApplyDefaults, and the empty mode
// also validates clean — a programmatically built config that bypassed
// ApplyDefaults must get the historical behavior, never a rejection.
func TestChatGPTAuthMode_DefaultIsAPIKey(t *testing.T) {
	cfg := minimalValidChatGPTConfig(t)
	if got := cfg.LLM.ChatGPT.Auth.Mode; got != ChatGPTAuthModeAPIKey {
		t.Errorf("after ApplyDefaults Auth.Mode = %q, want %q", got, ChatGPTAuthModeAPIKey)
	}
	if err := validate(cfg); err != nil {
		t.Fatalf("validate() with the defaulted mode: %v", err)
	}

	// The bypassed-defaults reading: an empty mode is the historical api_key
	// posture, not an error.
	cfg.LLM.ChatGPT.Auth.Mode = ""
	if err := validate(cfg); err != nil {
		t.Fatalf("validate() with an unset mode: %v", err)
	}
}

// TestChatGPTAuthMode_EnumAccepted verifies both enum values validate clean
// through the real YAML → validate pipeline.
func TestChatGPTAuthMode_EnumAccepted(t *testing.T) {
	for _, mode := range []string{ChatGPTAuthModeAPIKey, ChatGPTAuthModeOAuth} {
		cfg := minimalValidChatGPTConfig(t)
		cfg.LLM.ChatGPT.Auth.Mode = mode
		if err := validate(cfg); err != nil {
			t.Errorf("validate() with mode %q: %v", mode, err)
		}
	}
}

// TestChatGPTAuthMode_UnknownRejected verifies the enum fails closed: a typo
// must fail the load with the key named, not silently keep key auth while the
// operator believes subscription auth is on.
func TestChatGPTAuthMode_UnknownRejected(t *testing.T) {
	cfg := minimalValidChatGPTConfig(t)
	cfg.LLM.ChatGPT.Auth.Mode = "OAuth" // case-sensitive on purpose
	err := validate(cfg)
	if err == nil {
		t.Fatal("validate() accepted mode \"OAuth\" — the enum must be exact")
	}
	if !strings.Contains(err.Error(), "llm.chatgpt.auth.mode") {
		t.Errorf("err = %v, want it to name llm.chatgpt.auth.mode", err)
	}
	for _, want := range []string{ChatGPTAuthModeAPIKey, ChatGPTAuthModeOAuth} {
		if !strings.Contains(err.Error(), want) {
			t.Errorf("err = %v, want it to list the valid value %q", err, want)
		}
	}
}

// TestChatGPTAuthMode_RoundTrip preserves the mode through a YAML marshal →
// unmarshal cycle, so a persisted oauth config reloads as oauth (and the
// omitempty default stays absent rather than materializing noise).
func TestChatGPTAuthMode_RoundTrip(t *testing.T) {
	original := minimalValidChatGPTConfig(t)
	original.LLM.ChatGPT.Auth.Mode = ChatGPTAuthModeOAuth

	data, err := yaml.Marshal(original)
	if err != nil {
		t.Fatalf("yaml.Marshal() failed: %v", err)
	}
	var restored Config
	if err := yaml.Unmarshal(data, &restored); err != nil {
		t.Fatalf("round-trip yaml.Unmarshal() failed: %v", err)
	}
	if got := restored.LLM.ChatGPT.Auth.Mode; got != ChatGPTAuthModeOAuth {
		t.Errorf("round-tripped Auth.Mode = %q, want %q", got, ChatGPTAuthModeOAuth)
	}
}

// TestChatGPTAuthMode_MarshalShape pins the omitempty choice from both
// sides: a defaults-applied config persists the mode explicitly (mode:
// api_key), while a config whose mode was never seeded marshals no auth
// section at all — hand-written api_key configs stay byte-stable.
func TestChatGPTAuthMode_MarshalShape(t *testing.T) {
	defaulted := minimalValidChatGPTConfig(t)
	data, err := yaml.Marshal(defaulted)
	if err != nil {
		t.Fatalf("yaml.Marshal() failed: %v", err)
	}
	if !strings.Contains(string(data), "mode: "+ChatGPTAuthModeAPIKey) {
		t.Errorf("a defaults-applied config must persist the mode explicitly:\n%s", data)
	}

	unset := &Config{}
	src := `
llm:
  chatgpt:
    api_key: k
`
	if err := yaml.Unmarshal([]byte(src), unset); err != nil {
		t.Fatalf("yaml.Unmarshal() failed: %v", err)
	}
	data, err = yaml.Marshal(unset)
	if err != nil {
		t.Fatalf("yaml.Marshal() failed: %v", err)
	}
	if strings.Contains(string(data), "auth:") {
		t.Errorf("an unseeded config must marshal no auth section:\n%s", data)
	}
}
