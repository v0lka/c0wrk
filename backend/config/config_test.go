package config

import (
	"encoding/json"
	"fmt"
	"io"
	"log/slog"
	"os"
	"path/filepath"
	"reflect"
	"strings"
	"testing"

	"github.com/v0lka/c0wrk/core/vectorindex"

	"gopkg.in/yaml.v3"
)

// TestLoadMinimalConfig tests loading a minimal YAML config with default_model and provider models.
func TestLoadMinimalConfig(t *testing.T) {
	content := `
llm:
  default_model: claude-3-haiku
  anthropic:
    api_key: "test-key"
    models:
      - claude-3-haiku
`
	configPath := writeTestConfig(t, content)

	cfg, err := Load(configPath)
	if err != nil {
		t.Fatalf("Load() failed: %v", err)
	}

	// Verify default_model
	if cfg.LLM.DefaultModel != "claude-3-haiku" {
		t.Errorf("Expected default_model 'claude-3-haiku', got %q", cfg.LLM.DefaultModel)
	}

	// Verify anthropic config
	if cfg.LLM.Anthropic.APIKey != "test-key" {
		t.Errorf("Expected api_key 'test-key', got %q", cfg.LLM.Anthropic.APIKey)
	}
	if len(cfg.LLM.Anthropic.Models) != 1 || cfg.LLM.Anthropic.Models[0] != "claude-3-haiku" {
		t.Errorf("Expected models [claude-3-haiku], got %v", cfg.LLM.Anthropic.Models)
	}
}

// TestEnvVarPreservationAndExpansion tests that ${ENV_VAR} patterns are preserved
// in the config struct after Load(), and that ExpandEnvVars resolves them at runtime.
func TestEnvVarPreservationAndExpansion(t *testing.T) {
	// Set test environment variable
	testAPIKey := "secret-api-key-12345"
	t.Setenv("TEST_API_KEY", testAPIKey)

	content := `
llm:
  default_model: claude-3-haiku
  anthropic:
    api_key: "${TEST_API_KEY}"
    models:
      - claude-3-haiku
`
	configPath := writeTestConfig(t, content)

	cfg, err := Load(configPath)
	if err != nil {
		t.Fatalf("Load() failed: %v", err)
	}

	// After Load(), the raw ${...} reference should be preserved in the struct
	if cfg.LLM.Anthropic.APIKey != "${TEST_API_KEY}" {
		t.Errorf("Expected raw reference ${TEST_API_KEY}, got %q", cfg.LLM.Anthropic.APIKey)
	}

	// ExpandEnvVars should resolve it at runtime
	resolved := ExpandEnvVars(cfg.LLM.Anthropic.APIKey)
	if resolved != testAPIKey {
		t.Errorf("Expected ExpandEnvVars to return %q, got %q", testAPIKey, resolved)
	}
}

// TestLoadAnthropicCompatible tests loading an anthropic_compatible provider from YAML.
func TestLoadAnthropicCompatible(t *testing.T) {
	content := `
llm:
  default_model: claude-sonnet-4-20250514
  anthropic:
    api_key: "anthropic-key"
    models:
      - claude-3-haiku
  anthropic_compatible:
    my-proxy:
      base_url: "https://my-anthropic-proxy.example.com"
      api_key: "proxy-key"
      models:
        - claude-sonnet-4-20250514
    another-proxy:
      base_url: "https://another.example.com"
      api_key: ""
      models:
        - claude-opus-4-20250514
`
	configPath := writeTestConfig(t, content)

	cfg, err := Load(configPath)
	if err != nil {
		t.Fatalf("Load() failed: %v", err)
	}

	if len(cfg.LLM.AnthropicCompatible) != 2 {
		t.Fatalf("Expected 2 anthropic_compatible providers, got %d", len(cfg.LLM.AnthropicCompatible))
	}

	proxy, ok := cfg.LLM.AnthropicCompatible["my-proxy"]
	if !ok {
		t.Fatal("Expected 'my-proxy' entry in anthropic_compatible")
	}
	if proxy.BaseURL != "https://my-anthropic-proxy.example.com" {
		t.Errorf("my-proxy base_url = %q, want 'https://my-anthropic-proxy.example.com'", proxy.BaseURL)
	}
	if proxy.APIKey != "proxy-key" {
		t.Errorf("my-proxy api_key = %q, want 'proxy-key'", proxy.APIKey)
	}
	if len(proxy.Models) != 1 || proxy.Models[0] != "claude-sonnet-4-20250514" {
		t.Errorf("my-proxy models = %v, want [claude-sonnet-4-20250514]", proxy.Models)
	}

	// Verify providerType resolves anthropic_compatible keys to "anthropic".
	if pt := cfg.LLM.providerType("my-proxy"); pt != "anthropic" {
		t.Errorf("providerType(my-proxy) = %q, want 'anthropic'", pt)
	}
	if pt := cfg.LLM.providerType("another-proxy"); pt != "anthropic" {
		t.Errorf("providerType(another-proxy) = %q, want 'anthropic'", pt)
	}
	// Empty key is allowed (local Anthropic-compatible servers).
	if cfg.LLM.AnthropicCompatible["another-proxy"].APIKey != "" {
		t.Errorf("another-proxy api_key should be empty, got %q", cfg.LLM.AnthropicCompatible["another-proxy"].APIKey)
	}
}

// TestLoadAnthropicCompatible_Omitted tests that a config without the
// anthropic_compatible section loads cleanly (nil map → no entries).
func TestLoadAnthropicCompatible_Omitted(t *testing.T) {
	content := `
llm:
  default_model: claude-3-haiku
  anthropic:
    api_key: "test-key"
    models:
      - claude-3-haiku
`
	configPath := writeTestConfig(t, content)

	cfg, err := Load(configPath)
	if err != nil {
		t.Fatalf("Load() failed: %v", err)
	}
	if len(cfg.LLM.AnthropicCompatible) != 0 {
		t.Errorf("Expected 0 anthropic_compatible providers when section omitted, got %d", len(cfg.LLM.AnthropicCompatible))
	}
}

// TestInvalidProviderError tests that invalid default_model (not in any provider's models) returns an error.
func TestInvalidProviderError(t *testing.T) {
	content := `
llm:
  default_model: nonexistent-model
  anthropic:
    api_key: "test-key"
    models:
      - claude-3-haiku
`
	configPath := writeTestConfig(t, content)

	_, err := Load(configPath)
	if err == nil {
		t.Fatal("Expected error when default_model is not in any provider's models, got nil")
	}

	// Verify error message mentions the issue
	expectedSubstring := "not found in any provider"
	if !contains(err.Error(), expectedSubstring) {
		t.Errorf("Expected error to contain %q, got: %v", expectedSubstring, err)
	}
}

// TestMissingModelError tests that missing default_model returns an error.
func TestMissingModelError(t *testing.T) {
	content := `
llm:
  anthropic:
    api_key: "test-key"
    models:
      - claude-3-haiku
`
	configPath := writeTestConfig(t, content)

	_, err := Load(configPath)
	if err == nil {
		t.Fatal("Expected error when default_model is missing, got nil")
	}

	// Verify error message mentions the issue
	expectedSubstring := "default_model is not set"
	if !contains(err.Error(), expectedSubstring) {
		t.Errorf("Expected error to contain %q, got: %v", expectedSubstring, err)
	}
}

// TestDefaultsApplied tests that defaults are applied for missing fields.
func TestDefaultsApplied(t *testing.T) {
	content := `
llm:
  default_model: claude-3-haiku
  anthropic:
    api_key: "test-key"
    models:
      - claude-3-haiku
`
	configPath := writeTestConfig(t, content)

	cfg, err := Load(configPath)
	if err != nil {
		t.Fatalf("Load() failed: %v", err)
	}

	// Check Executor defaults
	if cfg.Executor.MaxRetries != 2 {
		t.Errorf("Expected default max_retries 2, got %d", cfg.Executor.MaxRetries)
	}
	if cfg.Executor.OutputTokenReserve != 8192 {
		t.Errorf("Expected default output_token_reserve 8192, got %d", cfg.Executor.OutputTokenReserve)
	}

	// Check Compaction defaults
	if cfg.Executor.Compaction.SlidingWindow.KeepFirst != 3 {
		t.Errorf("Expected default keep_first 3, got %d", cfg.Executor.Compaction.SlidingWindow.KeepFirst)
	}
	if cfg.Executor.Compaction.SlidingWindow.KeepLast != 10 {
		t.Errorf("Expected default keep_last 10, got %d", cfg.Executor.Compaction.SlidingWindow.KeepLast)
	}
	if cfg.Executor.Compaction.Summarization.BlockSize != 7 {
		t.Errorf("Expected default block_size 7, got %d", cfg.Executor.Compaction.Summarization.BlockSize)
	}
	if cfg.Executor.Compaction.Hierarchical.EnabledAboveSteps != 25 {
		t.Errorf("Expected default enabled_above_steps 25, got %d", cfg.Executor.Compaction.Hierarchical.EnabledAboveSteps)
	}

	// Check Router defaults
	if cfg.Router.HistoryWindow != 10 {
		t.Errorf("Expected default history_window 10, got %d", cfg.Router.HistoryWindow)
	}

	// Check Security defaults: every configurable group is created with its
	// default policy and the execute group receives the default blacklist.
	for group, want := range defaultToolGroupPolicies {
		got, ok := cfg.Security.Groups[group]
		if !ok {
			t.Errorf("Expected default security group %q", group)
			continue
		}
		if got.Policy != want {
			t.Errorf("Expected group %q policy %q, got %q", group, want, got.Policy)
		}
	}
	if got := cfg.Security.Groups[ToolGroupExecute].Blocklist; got != nil {
		t.Errorf("Expected NO default execute-group blocklist (empty by default), got %v", got)
	}

	// Check LLM retry defaults
	if cfg.LLM.Retry.MaxRetries != 3 {
		t.Errorf("Expected default llm.retry.max_retries 3, got %d", cfg.LLM.Retry.MaxRetries)
	}
	if cfg.LLM.Retry.InitialBackoff != "1s" {
		t.Errorf("Expected default llm.retry.initial_backoff '1s', got %q", cfg.LLM.Retry.InitialBackoff)
	}
	if cfg.LLM.Retry.MaxBackoff != "30s" {
		t.Errorf("Expected default llm.retry.max_backoff '30s', got %q", cfg.LLM.Retry.MaxBackoff)
	}

	// Check Timeouts defaults
	if cfg.Timeouts.LLMRequestTimeout != 600 {
		t.Errorf("Expected default llmRequestTimeout 600, got %d", cfg.Timeouts.LLMRequestTimeout)
	}
	if cfg.Timeouts.ServiceLLMRequestTimeout != 120 {
		t.Errorf("Expected default serviceLLMRequestTimeout 120, got %d", cfg.Timeouts.ServiceLLMRequestTimeout)
	}
	if cfg.Timeouts.GitCommitTimeout != 300 {
		t.Errorf("Expected default gitCommitTimeout 300, got %d", cfg.Timeouts.GitCommitTimeout)
	}

	// Check Models map is initialized
	if cfg.LLM.Models == nil {
		t.Error("Expected Models map to be initialized")
	}
}

// TestApplyDefaults_MaxParallelSubagents pins the documented contract on
// AgentsConfig.MaxParallelSubagents (see the field's doc comment): "<= 0
// resolves to the default (4)". Only == 0 used to be defaulted, so a negative
// value propagated verbatim to the conductor, whose shared limiter treats a
// non-positive cap as "unlimited" — silently disabling the concurrency bound
// the config documents.
func TestApplyDefaults_MaxParallelSubagents(t *testing.T) {
	cases := []struct {
		name  string
		value int
		want  int
	}{
		{"unset zero", 0, 4},
		{"negative one", -1, 4},
		{"negative large", -100, 4},
		{"explicit one", 1, 1},
		{"explicit seven", 7, 7},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			cfg := &Config{}
			cfg.Agents.MaxParallelSubagents = tc.value
			ApplyDefaults(cfg)
			if got := cfg.Agents.MaxParallelSubagents; got != tc.want {
				t.Fatalf("ApplyDefaults with max_parallel_subagents=%d = %d, want %d", tc.value, got, tc.want)
			}
		})
	}
}

// TestApplyDefaults_DiscoveryDirPrecedence pins the documented precedence of
// the default discovery directories: the c0wrk global dir outranks the user's
// ~/.agents dir for BOTH skills and agents, so a c0wrk-managed skill/profile
// wins over a same-named user-wide one. An explicit list is preserved verbatim
// (order and contents untouched).
func TestApplyDefaults_DiscoveryDirPrecedence(t *testing.T) {
	cfg := &Config{}
	ApplyDefaults(cfg)

	wantSkills := []string{"~/.c0wrk/.agents/skills", "~/.agents/skills"}
	if !reflect.DeepEqual(cfg.Skills.Dirs, wantSkills) {
		t.Errorf("Skills.Dirs = %v, want %v (c0wrk global dir first)", cfg.Skills.Dirs, wantSkills)
	}
	wantAgents := []string{"~/.c0wrk/.agents/agents", "~/.agents/agents"}
	if !reflect.DeepEqual(cfg.Agents.Dirs, wantAgents) {
		t.Errorf("Agents.Dirs = %v, want %v (c0wrk global dir first)", cfg.Agents.Dirs, wantAgents)
	}

	// An explicit user list must survive ApplyDefaults untouched — including
	// an intentional reverse order and an explicit empty slice opt-out.
	explicit := &Config{}
	explicit.Skills.Dirs = []string{"~/.agents/skills"}
	explicit.Agents.Dirs = []string{"~/.agents/agents"}
	ApplyDefaults(explicit)
	if !reflect.DeepEqual(explicit.Skills.Dirs, []string{"~/.agents/skills"}) {
		t.Errorf("explicit Skills.Dirs was clobbered: %v", explicit.Skills.Dirs)
	}
	if !reflect.DeepEqual(explicit.Agents.Dirs, []string{"~/.agents/agents"}) {
		t.Errorf("explicit Agents.Dirs was clobbered: %v", explicit.Agents.Dirs)
	}

	empty := &Config{}
	empty.Skills.Dirs = []string{}
	empty.Agents.Dirs = []string{}
	ApplyDefaults(empty)
	if len(empty.Skills.Dirs) != 0 {
		t.Errorf("explicit empty Skills.Dirs must stay empty, got %v", empty.Skills.Dirs)
	}
	if len(empty.Agents.Dirs) != 0 {
		t.Errorf("explicit empty Agents.Dirs must stay empty, got %v", empty.Agents.Dirs)
	}
}

// TestOpenAICompatibleRequiresBaseURL tests that openai_compatible provider requires base_url.
// Note: base_url requirement is now validated at the LLM router level, not at config validation.
// The config simply loads the base_url and it's validated when creating the provider.
func TestOpenAICompatibleRequiresBaseURL(t *testing.T) {
	content := `
llm:
  default_model: deepseek-chat
  openai_compatible:
    deepseek:
      api_key: "test-key"
      models:
        - deepseek-chat
    # No base_url specified — ok, defaults to empty, validated at router level
`
	configPath := writeTestConfig(t, content)

	cfg, err := Load(configPath)
	if err != nil {
		t.Fatalf("Load() failed: %v", err)
	}

	if cfg.LLM.OpenAICompatible["deepseek"].BaseURL != "" {
		t.Errorf("Expected empty base_url for openai_compatible without base_url in config, got %q", cfg.LLM.OpenAICompatible["deepseek"].BaseURL)
	}
}

// TestLoadWithResult_NoErrors tests that clean load returns no errors.
func TestLoadWithResult_NoErrors(t *testing.T) {
	content := `
llm:
  default_model: claude-3-haiku
  anthropic:
    api_key: "test-key"
    models:
      - claude-3-haiku
`
	configPath := writeTestConfig(t, content)

	result, err := LoadWithResult(configPath)
	if err != nil {
		t.Fatalf("LoadWithResult() failed: %v", err)
	}

	if len(result.LoadErrors) != 0 {
		t.Errorf("Expected no load errors, got %v", result.LoadErrors)
	}
}

// TestModelProfilesLegacyInlineSectionIgnoredAndDropped pins the sanctioned reset
// migration: a pre-profiles config.yaml with an inline `small_llm:` section
// (full 25-knob set + master toggle) must load WITHOUT errors — decoding is
// non-strict, so the unknown legacy key is silently ignored — and the next
// save must drop it entirely, persisting only the new slim `model_profiles:` section
// (enabled + active_profile).
func TestModelProfilesLegacyInlineSectionIgnoredAndDropped(t *testing.T) {
	content := `
llm:
  default_model: claude-3-haiku
  anthropic:
    api_key: "test-key"
    models:
      - claude-3-haiku
small_llm:
  enabled: true
  essential_tools:
    enabled: true
    always_present: [read_file, bash_exec]
  system_prompt:
    lite: true
  sampling:
    enabled: true
    temperature: 0.1
    reasoning_effort: low
  loop_hardening:
    enabled: true
    repeat_nudge_threshold: 2
  context:
    enabled: true
    output_token_reserve: 8192
`
	configPath := writeTestConfig(t, content)

	// Load: the legacy section is ignored without errors; the effective state
	// is the fresh modelProfiles defaults (master off, generic active).
	result, err := LoadWithResult(configPath)
	if err != nil {
		t.Fatalf("LoadWithResult() failed on a legacy inline small_llm config: %v", err)
	}
	if len(result.LoadErrors) != 0 {
		t.Fatalf("legacy small_llm.* must be ignored without load errors, got %v", result.LoadErrors)
	}
	if result.Config.ModelProfiles.Enabled {
		t.Error("legacy small_llm.enabled must be ignored (reset migration), want model_profiles.enabled=false")
	}
	if got := result.Config.ModelProfiles.ActiveProfile; got != ModelProfilesGenericProfileID {
		t.Errorf("model_profiles.active_profile = %q, want the seeded %q", got, ModelProfilesGenericProfileID)
	}

	// Save: only the slim modelProfiles section survives; the legacy knobs are gone.
	if err := Save(result.Config, configPath); err != nil {
		t.Fatalf("Save() failed: %v", err)
	}
	saved, err := os.ReadFile(configPath)
	if err != nil {
		t.Fatalf("read saved config: %v", err)
	}
	if strings.Contains(string(saved), "small_llm") {
		t.Errorf("saved config still contains the legacy small_llm section:\n%s", saved)
	}
	for _, want := range []string{"model_profiles:", "enabled: false", "active_profile: " + ModelProfilesGenericProfileID} {
		if !strings.Contains(string(saved), want) {
			t.Errorf("saved config missing %q in the slim modelProfiles section:\n%s", want, saved)
		}
	}

	// The migrated file reloads cleanly with the same state.
	reloaded, err := LoadWithResult(configPath)
	if err != nil {
		t.Fatalf("LoadWithResult() failed on the migrated config: %v", err)
	}
	if len(reloaded.LoadErrors) != 0 {
		t.Errorf("migrated config must load without warnings, got %v", reloaded.LoadErrors)
	}
	if reloaded.Config.ModelProfiles.Enabled || reloaded.Config.ModelProfiles.ActiveProfile != ModelProfilesGenericProfileID {
		t.Errorf("migrated modelProfiles state = %+v, want disabled generic", reloaded.Config.ModelProfiles)
	}
}

// TestResolveAndLoad_ModelProfilesFallbackWarningSurfaced verifies that a stale
// model_profiles.active_profile (e.g. a custom profile deleted by hand) surfaces the
// soft generic fallback through the same load-warnings channel the UI
// displays (configLoadErrors) instead of failing the load.
func TestResolveAndLoad_ModelProfilesFallbackWarningSurfaced(t *testing.T) {
	home := t.TempDir()
	t.Setenv("HOME", home)
	t.Setenv("USERPROFILE", home) // windows parity
	agentDir := AgentDir()
	if err := os.MkdirAll(agentDir, 0o755); err != nil {
		t.Fatalf("mkdir agent dir: %v", err)
	}
	content := `
llm:
  default_model: claude-3-haiku
  anthropic:
    api_key: "test-key"
    models:
      - claude-3-haiku
model_profiles:
  enabled: true
  active_profile: deleted-externally
`
	if err := os.WriteFile(ConfigPath(agentDir), []byte(content), 0o644); err != nil {
		t.Fatalf("write config: %v", err)
	}

	resolved := ResolveAndLoad(slog.New(slog.NewTextHandler(io.Discard, nil)))

	if resolved.Config.ModelProfiles.ActiveProfile != "deleted-externally" {
		t.Errorf("the stored active profile id must be preserved, got %q", resolved.Config.ModelProfiles.ActiveProfile)
	}
	if len(resolved.LoadErrors) != 1 {
		t.Fatalf("LoadErrors = %v, want exactly the ModelProfiles fallback warning", resolved.LoadErrors)
	}
	if !strings.Contains(resolved.LoadErrors[0], "deleted-externally") ||
		!strings.Contains(resolved.LoadErrors[0], ModelProfilesGenericProfileID) {
		t.Errorf("warning must name the stale id and the generic fallback, got %q", resolved.LoadErrors[0])
	}
}

// TestLoadModelProfilesCatalog_MergesCustomProfiles verifies the catalog assembly used
// for resolution: the predefined entries plus the custom store, with store
// problems reported as warnings rather than errors.
func TestLoadModelProfilesCatalog_MergesCustomProfiles(t *testing.T) {
	dir := t.TempDir()

	// No store file yet: the catalog is exactly the predefined set.
	catalog, warnings := LoadModelProfilesCatalog(dir)
	if len(warnings) != 0 {
		t.Errorf("pristine catalog must load without warnings, got %v", warnings)
	}
	if len(catalog) != len(PredefinedModelProfiles()) {
		t.Fatalf("catalog size = %d, want %d (predefined only)", len(catalog), len(PredefinedModelProfiles()))
	}

	custom, err := NewModelProfile("my-tuned", "My Tuned", ModelProfileKindCustom, ModelProfileConfig{})
	if err != nil {
		t.Fatalf("NewModelProfile: %v", err)
	}
	if err := SaveCustomModelProfiles(ModelProfilesPath(dir), []ModelProfile{custom}); err != nil {
		t.Fatalf("SaveCustomModelProfiles: %v", err)
	}

	catalog, warnings = LoadModelProfilesCatalog(dir)
	if len(warnings) != 0 {
		t.Errorf("catalog with a healthy store must load without warnings, got %v", warnings)
	}
	if _, ok := FindModelProfile(catalog, "my-tuned"); !ok {
		t.Error("custom profile missing from the merged catalog")
	}
	if _, ok := FindModelProfile(catalog, ModelProfilesGenericProfileID); !ok {
		t.Error("predefined generic profile missing from the merged catalog")
	}
}

// TestGetAllProviderConfigs tests multi-provider config resolution.
func TestGetAllProviderConfigs(t *testing.T) {
	cfg := LLMConfig{
		DefaultModel: "claude-3-haiku",
		Anthropic: AnthropicConfig{
			APIKey: "anthropic-key",
			Models: []string{"claude-3-haiku", "claude-opus"},

			OutputTokenReserve: 12288,
		},
		OpenAICompatible: map[string]OpenAICompatibleConfig{
			"deepseek": {
				APIKey:  "deepseek-key",
				BaseURL: "https://api.deepseek.com",
				Models:  []string{"deepseek-chat"},

				OutputTokenReserve: 16384,
			},
			"openrouter": {
				APIKey:  "openrouter-key",
				BaseURL: "https://openrouter.ai/api",
				Models:  []string{"openai/gpt-4o"},
			},
		},
		AnthropicCompatible: map[string]AnthropicCompatibleConfig{
			"my-proxy": {
				APIKey:  "proxy-key",
				BaseURL: "https://my-anthropic-proxy.example.com",
				Models:  []string{"claude-sonnet-4-20250514"},
			},
		},
	}

	providers := cfg.GetAllProviderConfigs()
	if len(providers) != 5 {
		t.Fatalf("Expected 5 providers (anthropic + 2 openai_compatible + 1 anthropic_compatible + chatgpt), got %d", len(providers))
	}

	// Check first provider
	if providers[0].Name != "anthropic" {
		t.Errorf("First provider name = %q, want 'anthropic'", providers[0].Name)
	}
	if providers[0].ProviderType != "anthropic" {
		t.Errorf("First provider type = %q, want 'anthropic'", providers[0].ProviderType)
	}
	if len(providers[0].Models) != 2 {
		t.Errorf("Expected 2 anthropic models, got %d", len(providers[0].Models))
	}
	if providers[0].OutputTokenReserve != 12288 {
		t.Errorf("anthropic OutputTokenReserve = %d, want 12288", providers[0].OutputTokenReserve)
	}

	// Check second provider (chatgpt — sorted after anthropic)
	if providers[1].Name != "chatgpt" {
		t.Errorf("Second provider name = %q, want 'chatgpt'", providers[1].Name)
	}
	if providers[1].ProviderType != "openai" {
		t.Errorf("Second provider type = %q, want 'openai'", providers[1].ProviderType)
	}

	// Check third provider (deepseek — sorted after chatgpt)
	if providers[2].Name != "deepseek" {
		t.Errorf("Third provider name = %q, want 'deepseek'", providers[2].Name)
	}
	if providers[2].ProviderType != "openai" {
		t.Errorf("Third provider type = %q, want 'openai'", providers[2].ProviderType)
	}
	if providers[2].BaseURL != "https://api.deepseek.com" {
		t.Errorf("Third provider BaseURL = %q, want 'https://api.deepseek.com'", providers[2].BaseURL)
	}
	if providers[2].OutputTokenReserve != 16384 {
		t.Errorf("deepseek OutputTokenReserve = %d, want 16384", providers[2].OutputTokenReserve)
	}

	// Check fourth provider (openrouter — sorted after deepseek)
	if providers[3].Name != "openrouter" {
		t.Errorf("Fourth provider name = %q, want 'openrouter'", providers[3].Name)
	}
	if providers[3].ProviderType != "openai" {
		t.Errorf("Fourth provider type = %q, want 'openai'", providers[3].ProviderType)
	}

	// Check fifth provider (my-proxy — anthropic_compatible, sorted after openai_compatible)
	if providers[4].Name != "my-proxy" {
		t.Errorf("Fifth provider name = %q, want 'my-proxy'", providers[4].Name)
	}
	if providers[4].ProviderType != "anthropic" {
		t.Errorf("Fifth provider type = %q, want 'anthropic'", providers[4].ProviderType)
	}
	if providers[4].BaseURL != "https://my-anthropic-proxy.example.com" {
		t.Errorf("Fifth provider BaseURL = %q, want 'https://my-anthropic-proxy.example.com'", providers[4].BaseURL)
	}
	if len(providers[4].Models) != 1 || providers[4].Models[0] != "claude-sonnet-4-20250514" {
		t.Errorf("Fifth provider Models = %v, want [claude-sonnet-4-20250514]", providers[4].Models)
	}
}

// TestResolveDefaultModelProvider tests looking up the default model across providers.
func TestResolveDefaultModelProvider(t *testing.T) {
	tests := []struct {
		name         string
		config       LLMConfig
		wantName     string
		wantProvType string
		wantAPIKey   string
		wantModel    string
		wantErr      bool
	}{
		{
			name: "anthropic",
			config: LLMConfig{
				DefaultModel: "claude-3-haiku",
				Anthropic: AnthropicConfig{
					APIKey: "anthropic-key",
					Models: []string{"claude-3-haiku"},
				},
			},
			wantName:     "anthropic",
			wantProvType: "anthropic",
			wantAPIKey:   "anthropic-key",
			wantModel:    "claude-3-haiku",
		},
		{
			name: "chatgpt",
			config: LLMConfig{
				DefaultModel: "gpt-4o",
				ChatGPT: ChatGPTConfig{
					APIKey: "openai-key",
					Models: []string{"gpt-4o"},
				},
			},
			wantName:     "chatgpt",
			wantProvType: "openai",
			wantAPIKey:   "openai-key",
			wantModel:    "gpt-4o",
		},
		{
			name: "not_found",
			config: LLMConfig{
				DefaultModel: "nonexistent",
				Anthropic: AnthropicConfig{
					Models: []string{"claude-3-haiku"},
				},
			},
			wantErr: true,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			prov, gotModel, err := tt.config.ResolveDefaultModelProvider()
			if tt.wantErr {
				if err == nil {
					t.Fatal("Expected error, got nil")
				}
				return
			}
			if err != nil {
				t.Fatalf("ResolveDefaultModelProvider() error: %v", err)
			}
			if prov.Name != tt.wantName {
				t.Errorf("name = %q, want %q", prov.Name, tt.wantName)
			}
			if prov.ProviderType != tt.wantProvType {
				t.Errorf("providerType = %q, want %q", prov.ProviderType, tt.wantProvType)
			}
			if prov.APIKey != tt.wantAPIKey {
				t.Errorf("apiKey = %q, want %q", prov.APIKey, tt.wantAPIKey)
			}
			if gotModel != tt.wantModel {
				t.Errorf("model = %q, want %q", gotModel, tt.wantModel)
			}
		})
	}
}

// writeTestConfig writes content to a temporary YAML file and returns its path.
func writeTestConfig(t *testing.T, content string) string {
	t.Helper()
	tmpDir := t.TempDir()
	configPath := filepath.Join(tmpDir, "config.yaml")
	if err := os.WriteFile(configPath, []byte(content), 0o644); err != nil {
		t.Fatalf("Failed to write test config: %v", err)
	}
	return configPath
}

// TestExpandEnvVars tests the ExpandEnvVars function directly.
func TestExpandEnvVars(t *testing.T) {
	tests := []struct {
		name     string
		envVars  map[string]string
		input    string
		expected string
	}{
		{
			name:     "no env vars",
			input:    "plain text without env vars",
			expected: "plain text without env vars",
		},
		{
			name:     "single env var",
			envVars:  map[string]string{"API_KEY": "secret123"},
			input:    "key: ${API_KEY}",
			expected: "key: secret123",
		},
		{
			name:     "multiple env vars",
			envVars:  map[string]string{"USER": "alice", "HOST": "localhost"},
			input:    "${USER}@${HOST}",
			expected: "alice@localhost",
		},
		{
			name:     "unset env var returns empty",
			input:    "key: ${UNSET_VAR}",
			expected: "key: ",
		},
		{
			name:     "mixed text and env vars",
			envVars:  map[string]string{"MODEL": "gpt-4"},
			input:    "Using model: ${MODEL} for inference",
			expected: "Using model: gpt-4 for inference",
		},
		{
			name:     "empty string",
			input:    "",
			expected: "",
		},
		{
			name:     "env var with underscore",
			envVars:  map[string]string{"DEEPSEEK_API_KEY": "ds-key-123"},
			input:    "${DEEPSEEK_API_KEY}",
			expected: "ds-key-123",
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			// Set environment variables
			for k, v := range tt.envVars {
				t.Setenv(k, v)
			}

			result := ExpandEnvVars(tt.input)
			if result != tt.expected {
				t.Errorf("ExpandEnvVars(%q) = %q, want %q", tt.input, result, tt.expected)
			}
		})
	}
}

// contains checks if substr is in s.
func contains(s, substr string) bool {
	return len(s) >= len(substr) && (s == substr || substr == "" ||
		(s != "" && substr != "" && findSubstring(s, substr)))
}

func findSubstring(s, substr string) bool {
	for i := 0; i <= len(s)-len(substr); i++ {
		if s[i:i+len(substr)] == substr {
			return true
		}
	}
	return false
}

func TestSave_RoundTrip(t *testing.T) {
	cfg := &Config{}
	ApplyDefaults(cfg)
	cfg.LLM.DefaultModel = "claude-3-5-sonnet"
	cfg.LLM.Anthropic.APIKey = "test-key-123"
	cfg.LLM.Anthropic.Models = []string{"claude-3-5-sonnet"}

	// Write to temp file
	tmpDir := t.TempDir()
	path := filepath.Join(tmpDir, "config.yaml")

	if err := Save(cfg, path); err != nil {
		t.Fatalf("Save failed: %v", err)
	}

	// Verify file exists
	if _, err := os.Stat(path); err != nil {
		t.Fatalf("saved file does not exist: %v", err)
	}

	// Load it back
	loaded, err := Load(path)
	if err != nil {
		t.Fatalf("Load failed: %v", err)
	}

	if loaded.LLM.DefaultModel != "claude-3-5-sonnet" {
		t.Errorf("DefaultModel = %q, want 'claude-3-5-sonnet'", loaded.LLM.DefaultModel)
	}
	if loaded.LLM.Anthropic.APIKey != "test-key-123" {
		t.Errorf("APIKey = %q, want 'test-key-123'", loaded.LLM.Anthropic.APIKey)
	}
}

func TestSave_AtomicWrite(t *testing.T) {
	cfg := &Config{}
	ApplyDefaults(cfg)
	cfg.LLM.DefaultModel = "model"
	cfg.LLM.Anthropic.APIKey = "key"
	cfg.LLM.Anthropic.Models = []string{"model"}

	tmpDir := t.TempDir()
	path := filepath.Join(tmpDir, "config.yaml")

	// Save should not leave temp file behind
	if err := Save(cfg, path); err != nil {
		t.Fatalf("Save failed: %v", err)
	}

	tmpPath := path + ".tmp"
	if _, err := os.Stat(tmpPath); !os.IsNotExist(err) {
		t.Error("temp file should not exist after successful save")
	}
}

func TestSave_InvalidPath(t *testing.T) {
	cfg := &Config{}
	ApplyDefaults(cfg)
	cfg.LLM.DefaultModel = "model"
	cfg.LLM.Anthropic.Models = []string{"model"}

	err := Save(cfg, "/nonexistent/deeply/nested/dir/config.yaml")
	if err == nil {
		t.Error("expected error for invalid path")
	}
}

func TestSave_PreservesEnvVarReferences(t *testing.T) {
	t.Setenv("MY_SECRET_KEY", "actual-secret-value")

	content := `
llm:
  default_model: claude-3-haiku
  anthropic:
    api_key: "${MY_SECRET_KEY}"
    models:
      - claude-3-haiku
`
	configPath := writeTestConfig(t, content)

	cfg, err := Load(configPath)
	if err != nil {
		t.Fatalf("Load() failed: %v", err)
	}

	// Config struct should hold the raw reference
	if cfg.LLM.Anthropic.APIKey != "${MY_SECRET_KEY}" {
		t.Fatalf("Expected raw reference ${MY_SECRET_KEY}, got %q", cfg.LLM.Anthropic.APIKey)
	}

	// Save config to a new file
	savePath := filepath.Join(t.TempDir(), "saved_config.yaml")
	if err := Save(cfg, savePath); err != nil {
		t.Fatalf("Save() failed: %v", err)
	}

	// Read saved file and verify ${...} reference is preserved
	savedData, err := os.ReadFile(savePath)
	if err != nil {
		t.Fatalf("failed to read saved config: %v", err)
	}
	savedContent := string(savedData)
	if !findSubstring(savedContent, "${MY_SECRET_KEY}") {
		t.Errorf("saved config should contain ${MY_SECRET_KEY}, got:\n%s", savedContent)
	}
	if findSubstring(savedContent, "actual-secret-value") {
		t.Errorf("saved config should NOT contain the resolved secret value")
	}
}

// TestMCPServerConfig_YAMLUnmarshal tests YAML unmarshaling of MCPServerConfig.
func TestMCPServerConfig_YAMLUnmarshal(t *testing.T) {
	tests := []struct {
		name     string
		yaml     string
		expected MCPServerConfig
	}{
		{
			name: "stdio transport with all fields",
			yaml: `
transport: stdio
command: /usr/bin/mcp-server
args:
  - --port
  - "8080"
env:
  API_KEY: secret123
`,
			expected: MCPServerConfig{
				Transport: "stdio",
				Command:   "/usr/bin/mcp-server",
				Args:      []string{"--port", "8080"},
				Env:       map[string]string{"API_KEY": "secret123"},
			},
		},
		{
			name: "http transport with url and headers",
			yaml: `
transport: http
url: https://api.example.com/mcp
headers:
  Authorization: Bearer token123
  X-Custom-Header: custom-value
`,
			expected: MCPServerConfig{
				Transport: "http",
				URL:       "https://api.example.com/mcp",
				Headers:   map[string]string{"Authorization": "Bearer token123", "X-Custom-Header": "custom-value"},
			},
		},
		{
			name: "no transport field defaults to empty string",
			yaml: `
command: /usr/bin/mcp-server
args:
  - --verbose
`,
			expected: MCPServerConfig{
				Command: "/usr/bin/mcp-server",
				Args:    []string{"--verbose"},
			},
		},
		{
			name: "minimal stdio config",
			yaml: `command: node`,
			expected: MCPServerConfig{
				Command: "node",
			},
		},
		{
			name: "http with env var reference in headers",
			yaml: `
transport: http
url: https://api.example.com/mcp
headers:
  Authorization: "Bearer ${MCP_API_KEY}"
`,
			expected: MCPServerConfig{
				Transport: "http",
				URL:       "https://api.example.com/mcp",
				Headers:   map[string]string{"Authorization": "Bearer ${MCP_API_KEY}"},
			},
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			var cfg MCPServerConfig
			if err := yaml.Unmarshal([]byte(tt.yaml), &cfg); err != nil {
				t.Fatalf("yaml.Unmarshal() failed: %v", err)
			}

			if cfg.Transport != tt.expected.Transport {
				t.Errorf("Transport = %q, want %q", cfg.Transport, tt.expected.Transport)
			}
			if cfg.Command != tt.expected.Command {
				t.Errorf("Command = %q, want %q", cfg.Command, tt.expected.Command)
			}
			if cfg.URL != tt.expected.URL {
				t.Errorf("URL = %q, want %q", cfg.URL, tt.expected.URL)
			}

			// Compare Args slices
			if len(cfg.Args) != len(tt.expected.Args) {
				t.Errorf("Args length = %d, want %d", len(cfg.Args), len(tt.expected.Args))
			} else {
				for i, v := range cfg.Args {
					if v != tt.expected.Args[i] {
						t.Errorf("Args[%d] = %q, want %q", i, v, tt.expected.Args[i])
					}
				}
			}

			// Compare Env maps
			if len(cfg.Env) != len(tt.expected.Env) {
				t.Errorf("Env length = %d, want %d", len(cfg.Env), len(tt.expected.Env))
			} else {
				for k, v := range tt.expected.Env {
					if cfg.Env[k] != v {
						t.Errorf("Env[%q] = %q, want %q", k, cfg.Env[k], v)
					}
				}
			}

			// Compare Headers maps
			if len(cfg.Headers) != len(tt.expected.Headers) {
				t.Errorf("Headers length = %d, want %d", len(cfg.Headers), len(tt.expected.Headers))
			} else {
				for k, v := range tt.expected.Headers {
					if cfg.Headers[k] != v {
						t.Errorf("Headers[%q] = %q, want %q", k, cfg.Headers[k], v)
					}
				}
			}
		})
	}
}

// TestMCPServerConfig_YAMLMarshal tests YAML marshaling of MCPServerConfig.
func TestMCPServerConfig_YAMLMarshal(t *testing.T) {
	tests := []struct {
		name     string
		config   MCPServerConfig
		contains []string
	}{
		{
			name: "stdio config",
			config: MCPServerConfig{
				Transport: "stdio",
				Command:   "/usr/bin/mcp-server",
				Args:      []string{"--port", "8080"},
				Env:       map[string]string{"API_KEY": "secret"},
			},
			contains: []string{"transport: stdio", "command: /usr/bin/mcp-server", "- --port", "- \"8080\"", "API_KEY: secret"},
		},
		{
			name: "http config",
			config: MCPServerConfig{
				Transport: "http",
				URL:       "https://api.example.com/mcp",
				Headers:   map[string]string{"Authorization": "Bearer token"},
			},
			contains: []string{"transport: http", "url: https://api.example.com/mcp", "Authorization: Bearer token"},
		},
		{
			name: "minimal config - empty fields omitted",
			config: MCPServerConfig{
				Command: "node",
			},
			contains: []string{"command: node"},
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			data, err := yaml.Marshal(&tt.config)
			if err != nil {
				t.Fatalf("yaml.Marshal() failed: %v", err)
			}

			output := string(data)
			for _, substr := range tt.contains {
				if !findSubstring(output, substr) {
					t.Errorf("YAML output should contain %q, got:\n%s", substr, output)
				}
			}
		})
	}
}

// TestMCPServerConfig_JSONMarshal tests that MCPServerConfig serializes with the
// lowercase JSON keys the frontend reads. The MCP edit dialog consumes
// `transport`, `command`, `args`, `env`, `url`, and `headers`; without JSON tags,
// encoding/json would emit the capitalized Go field names and every value would
// render empty.
func TestMCPServerConfig_JSONMarshal(t *testing.T) {
	cfg := MCPServerConfig{
		Transport: "http",
		Command:   "/usr/bin/mcp-server",
		Args:      []string{"--port", "8080"},
		Env:       map[string]string{"API_KEY": "secret"},
		URL:       "https://api.example.com/mcp",
		Headers:   map[string]string{"Authorization": "Bearer token"},
	}

	data, err := json.Marshal(cfg)
	if err != nil {
		t.Fatalf("json.Marshal() failed: %v", err)
	}

	var got map[string]any
	if err := json.Unmarshal(data, &got); err != nil {
		t.Fatalf("json.Unmarshal() failed: %v", err)
	}

	for _, key := range []string{"transport", "command", "args", "env", "url", "headers"} {
		if _, ok := got[key]; !ok {
			t.Errorf("JSON output missing lowercase key %q (got %s)", key, data)
		}
	}
	for _, key := range []string{"Transport", "Command", "Args", "Env", "URL", "Headers"} {
		if _, ok := got[key]; ok {
			t.Errorf("JSON output leaked capitalized key %q (got %s)", key, data)
		}
	}

	var restored MCPServerConfig
	if err := json.Unmarshal(data, &restored); err != nil {
		t.Fatalf("json.Unmarshal() round-trip failed: %v", err)
	}
	if restored.Transport != cfg.Transport || restored.Command != cfg.Command || restored.URL != cfg.URL {
		t.Errorf("round-trip mismatch: got %+v, want %+v", restored, cfg)
	}
	if len(restored.Args) != len(cfg.Args) || len(restored.Env) != len(cfg.Env) || len(restored.Headers) != len(cfg.Headers) {
		t.Errorf("round-trip length mismatch: got %+v, want %+v", restored, cfg)
	}
}

// TestMCPServerConfig_RoundTrip tests that YAML marshal/unmarshal preserves all fields.
func TestMCPServerConfig_RoundTrip(t *testing.T) {
	original := MCPServerConfig{
		Transport: "http",
		Command:   "/usr/bin/mcp-server",
		Args:      []string{"--verbose", "--port", "8080"},
		Env:       map[string]string{"API_KEY": "secret", "DEBUG": "true"},
		URL:       "https://api.example.com/mcp",
		Headers:   map[string]string{"Authorization": "Bearer token", "X-Custom": "value"},
	}

	data, err := yaml.Marshal(&original)
	if err != nil {
		t.Fatalf("yaml.Marshal() failed: %v", err)
	}

	var restored MCPServerConfig
	if err := yaml.Unmarshal(data, &restored); err != nil {
		t.Fatalf("yaml.Unmarshal() failed: %v", err)
	}

	if restored.Transport != original.Transport {
		t.Errorf("Transport = %q, want %q", restored.Transport, original.Transport)
	}
	if restored.Command != original.Command {
		t.Errorf("Command = %q, want %q", restored.Command, original.Command)
	}
	if restored.URL != original.URL {
		t.Errorf("URL = %q, want %q", restored.URL, original.URL)
	}
	if len(restored.Args) != len(original.Args) {
		t.Errorf("Args length = %d, want %d", len(restored.Args), len(original.Args))
	}
	if len(restored.Env) != len(original.Env) {
		t.Errorf("Env length = %d, want %d", len(restored.Env), len(original.Env))
	}
	if len(restored.Headers) != len(original.Headers) {
		t.Errorf("Headers length = %d, want %d", len(restored.Headers), len(original.Headers))
	}
}

// TestMCPServerConfig_TimeoutRoundTrip verifies that the per-server timeout and
// call_timeout duration strings survive both YAML and JSON round-trips, and are
// omitted entirely when unset (so an existing config that predates them
// serializes unchanged).
func TestMCPServerConfig_TimeoutRoundTrip(t *testing.T) {
	original := MCPServerConfig{
		Command:     "/usr/bin/mcp-server",
		Timeout:     "30s",
		CallTimeout: "2m",
	}

	// YAML round-trip.
	yamlData, err := yaml.Marshal(&original)
	if err != nil {
		t.Fatalf("yaml.Marshal() failed: %v", err)
	}
	var fromYAML MCPServerConfig
	if err := yaml.Unmarshal(yamlData, &fromYAML); err != nil {
		t.Fatalf("yaml.Unmarshal() failed: %v", err)
	}
	if fromYAML.Timeout != original.Timeout {
		t.Errorf("yaml Timeout = %q, want %q", fromYAML.Timeout, original.Timeout)
	}
	if fromYAML.CallTimeout != original.CallTimeout {
		t.Errorf("yaml CallTimeout = %q, want %q", fromYAML.CallTimeout, original.CallTimeout)
	}

	// JSON round-trip with the lowercase keys the frontend reads.
	jsonData, err := json.Marshal(original)
	if err != nil {
		t.Fatalf("json.Marshal() failed: %v", err)
	}
	var got map[string]any
	if err := json.Unmarshal(jsonData, &got); err != nil {
		t.Fatalf("json.Unmarshal() failed: %v", err)
	}
	for _, key := range []string{"timeout", "call_timeout"} {
		if _, ok := got[key]; !ok {
			t.Errorf("JSON output missing lowercase key %q (got %s)", key, jsonData)
		}
	}
	for _, key := range []string{"Timeout", "CallTimeout"} {
		if _, ok := got[key]; ok {
			t.Errorf("JSON output leaked capitalized key %q (got %s)", key, jsonData)
		}
	}
	var fromJSON MCPServerConfig
	if err := json.Unmarshal(jsonData, &fromJSON); err != nil {
		t.Fatalf("json.Unmarshal() round-trip failed: %v", err)
	}
	if fromJSON.Timeout != original.Timeout || fromJSON.CallTimeout != original.CallTimeout {
		t.Errorf("json round-trip mismatch: got %+v, want %+v", fromJSON, original)
	}

	// Unset durations must be omitted (empty string + omitempty) so a config
	// that does not set them serializes byte-identically to before.
	empty := MCPServerConfig{Command: "node"}
	emptyYAML, err := yaml.Marshal(&empty)
	if err != nil {
		t.Fatalf("yaml.Marshal() failed: %v", err)
	}
	if strings.Contains(string(emptyYAML), "timeout") {
		t.Errorf("empty durations should be omitted from YAML, got %q", emptyYAML)
	}
	emptyJSON, err := json.Marshal(empty)
	if err != nil {
		t.Fatalf("json.Marshal() failed: %v", err)
	}
	if strings.Contains(string(emptyJSON), "timeout") {
		t.Errorf("empty durations should be omitted from JSON, got %q", emptyJSON)
	}
}

// TestCreateDefault_CreatesFileWithDefaults tests that CreateDefault creates a YAML file
// with all default values applied.
func TestCreateDefault_CreatesFileWithDefaults(t *testing.T) {
	tmpDir := t.TempDir()
	path := filepath.Join(tmpDir, "config.yaml")

	cfg, err := CreateDefault(path)
	if err != nil {
		t.Fatalf("CreateDefault() failed: %v", err)
	}

	// File must exist on disk.
	if _, statErr := os.Stat(path); statErr != nil {
		t.Fatalf("expected config file to exist at %s: %v", path, statErr)
	}

	// Returned config must have defaults applied.
	if cfg.LogLevel != "INFO" {
		t.Errorf("LogLevel = %q, want 'INFO'", cfg.LogLevel)
	}
	if cfg.Agents.MaxParallelSubagents != 4 {
		t.Errorf("Agents.MaxParallelSubagents = %d, want 4", cfg.Agents.MaxParallelSubagents)
	}
	if got := cfg.Security.Groups[ToolGroupExecute].Policy; got != GroupPolicyUserConfirm {
		t.Errorf("execute group policy = %q, want %q", got, GroupPolicyUserConfirm)
	}

	// The file must be readable YAML that round-trips back to the same defaults.
	data, err := os.ReadFile(path)
	if err != nil {
		t.Fatalf("failed to read created config: %v", err)
	}
	var loaded Config
	if err := yaml.Unmarshal(data, &loaded); err != nil {
		t.Fatalf("created config is not valid YAML: %v", err)
	}
	if loaded.Executor.MaxRetries != 2 {
		t.Errorf("round-tripped max_retries = %d, want 2", loaded.Executor.MaxRetries)
	}
}

// TestCreateDefault_FailsOnBadPath tests that CreateDefault returns an error
// when the target directory does not exist.
func TestCreateDefault_FailsOnBadPath(t *testing.T) {
	_, err := CreateDefault("/nonexistent/dir/config.yaml")
	if err == nil {
		t.Fatal("expected error for non-existent directory")
	}
}

// TestResolveAndLoad_CreatesDefaultWhenMissing verifies that ResolveAndLoad
// creates a default config file when no config file exists.
func TestResolveAndLoad_CreatesDefaultWhenMissing(t *testing.T) {
	// Use a temp directory as HOME so the primary config path doesn't exist.
	// On Windows os.UserHomeDir() reads %USERPROFILE% (not %HOME%), so set
	// both env vars to cover every platform Go supports.
	tmpHome := t.TempDir()
	t.Setenv("HOME", tmpHome)
	t.Setenv("USERPROFILE", tmpHome)

	// Change to a temp dir where no local config.yaml exists either.
	orig, _ := os.Getwd()
	tmpWd := t.TempDir()
	if err := os.Chdir(tmpWd); err != nil {
		t.Fatalf("chdir: %v", err)
	}
	t.Cleanup(func() { _ = os.Chdir(orig) })

	log := newDiscardLogger()
	resolved := ResolveAndLoad(log)

	// Config must be non-nil with defaults.
	if resolved.Config == nil {
		t.Fatal("expected non-nil Config")
	}
	if resolved.Config.Executor.MaxRetries != 2 {
		t.Errorf("max_retries = %d, want 2", resolved.Config.Executor.MaxRetries)
	}

	// The config file must have been created on disk.
	expectedPath := filepath.Join(tmpHome, DefaultAgentDir, "config.yaml")
	if _, err := os.Stat(expectedPath); err != nil {
		t.Errorf("expected default config file at %s: %v", expectedPath, err)
	}
}

func newDiscardLogger() *slog.Logger {
	return slog.New(slog.NewTextHandler(io.Discard, nil))
}

// TestMCPServerConfig_DefaultTransport tests that configs without transport field still work.
func TestMCPServerConfig_DefaultTransport(t *testing.T) {
	// This simulates loading an existing config file that doesn't have the transport field
	content := `
llm:
  default_model: claude-3-haiku
  anthropic:
    api_key: "test-key"
    models:
      - claude-3-haiku
mcp:
  servers:
    myserver:
      command: /usr/bin/mcp-server
      args:
        - --verbose
      env:
        API_KEY: secret
`
	configPath := writeTestConfig(t, content)

	cfg, err := Load(configPath)
	if err != nil {
		t.Fatalf("Load() failed: %v", err)
	}

	// Verify MCP server config loaded correctly
	if len(cfg.MCP.Servers) != 1 {
		t.Fatalf("Expected 1 MCP server, got %d", len(cfg.MCP.Servers))
	}

	server, ok := cfg.MCP.Servers["myserver"]
	if !ok {
		t.Fatal("Expected 'myserver' in MCP.Servers")
	}

	// Transport should be empty (not defaulting here, defaults handled at usage site)
	if server.Transport != "" {
		t.Errorf("Transport = %q, want empty string", server.Transport)
	}
	if server.Command != "/usr/bin/mcp-server" {
		t.Errorf("Command = %q, want /usr/bin/mcp-server", server.Command)
	}
	if len(server.Args) != 1 || server.Args[0] != "--verbose" {
		t.Errorf("Args = %v, want [--verbose]", server.Args)
	}
	if server.Env["API_KEY"] != "secret" {
		t.Errorf("Env[API_KEY] = %q, want secret", server.Env["API_KEY"])
	}
}

// TestVectorIndexConfig_EmbeddingThreads_YAMLRoundTrip verifies that
// EmbeddingThreads parses from YAML and that a zero/unset value stays 0
// (the legacy "use all cores" default).
func TestVectorIndexConfig_EmbeddingThreads_YAMLRoundTrip(t *testing.T) {
	tests := []struct {
		name string
		yaml string
		want int
	}{
		{
			name: "unset defaults to 0 (legacy all-cores)",
			yaml: `
hybrid: true
`,
			want: 0,
		},
		{
			name: "explicit zero is 0 (legacy all-cores)",
			yaml: `
embedding_threads: 0
`,
			want: 0,
		},
		{
			name: "single thread (minimum load)",
			yaml: `
embedding_threads: 1
`,
			want: 1,
		},
		{
			name: "two threads (balanced)",
			yaml: `
embedding_threads: 2
`,
			want: 2,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			var cfg VectorIndexConfig
			if err := yaml.Unmarshal([]byte(tt.yaml), &cfg); err != nil {
				t.Fatalf("yaml.Unmarshal() failed: %v", err)
			}
			if cfg.EmbeddingThreads != tt.want {
				t.Errorf("EmbeddingThreads = %d, want %d", cfg.EmbeddingThreads, tt.want)
			}
		})
	}

	// Round-trip: a set value must survive marshal + unmarshal unchanged.
	original := VectorIndexConfig{EmbeddingThreads: 4}
	data, err := yaml.Marshal(&original)
	if err != nil {
		t.Fatalf("yaml.Marshal() failed: %v", err)
	}

	var restored VectorIndexConfig
	if err := yaml.Unmarshal(data, &restored); err != nil {
		t.Fatalf("yaml.Unmarshal() failed: %v", err)
	}
	if restored.EmbeddingThreads != 4 {
		t.Errorf("round-tripped EmbeddingThreads = %d, want 4", restored.EmbeddingThreads)
	}
}

// TestVectorIndexConfig_TuningKnobs_Defaults pins the compatibility
// contract for the indexing/search tuning knobs: a zero-value config (an
// existing config.yaml with no vector_index block, or one written before
// the knobs existed) must resolve to exactly the historical hardcoded
// values, so behaviour is identical before and after the knobs were
// introduced.
func TestVectorIndexConfig_TuningKnobs_Defaults(t *testing.T) {
	cfg := &Config{}
	ApplyDefaults(cfg)

	if cfg.VectorIndex.EmbeddingBatchSize != 32 {
		t.Errorf("default embedding_batch_size = %d, want 32 (sp4rk embedding.DefaultBatchSize)", cfg.VectorIndex.EmbeddingBatchSize)
	}
	if cfg.VectorIndex.EmbeddingCacheMaxBytes != 512<<20 {
		t.Errorf("default embedding_cache_max_bytes = %d, want %d", cfg.VectorIndex.EmbeddingCacheMaxBytes, int64(512<<20))
	}
	if cfg.VectorIndex.PrepWorkers != 2 {
		t.Errorf("default prep_workers = %d, want 2", cfg.VectorIndex.PrepWorkers)
	}
	if cfg.VectorIndex.DebounceMs != 1000 {
		t.Errorf("default debounce_ms = %d, want 1000 (the historical hardcoded 1s)", cfg.VectorIndex.DebounceMs)
	}
	if cfg.VectorIndex.ChunkOverlap != 200 {
		t.Errorf("default chunk_overlap = %d, want 200 (the historical hardcoded value)", cfg.VectorIndex.ChunkOverlap)
	}
	gotTimeout := -1
	if cfg.VectorIndex.SearchWaitTimeoutMs != nil {
		gotTimeout = *cfg.VectorIndex.SearchWaitTimeoutMs
	}
	if gotTimeout != 3000 {
		t.Errorf("default search_wait_timeout_ms = %d, want 3000", gotTimeout)
	}
	gotPark := -1
	if cfg.VectorIndex.ParkCapacity != nil {
		gotPark = *cfg.VectorIndex.ParkCapacity
	}
	if gotPark != 3 {
		t.Errorf("default park_capacity = %d, want 3", gotPark)
	}
	gotBudget := int64(-1)
	if cfg.VectorIndex.ParkBudgetMb != nil {
		gotBudget = *cfg.VectorIndex.ParkBudgetMb
	}
	if gotBudget != 1024 {
		t.Errorf("default park_budget_mb = %d, want 1024", gotBudget)
	}
}

// TestVectorIndexConfig_TuningKnobs_YAMLRoundTrip covers YAML parsing of
// the new knobs (including the explicit-0 fail-fast sentinel), the
// interaction of the sentinel with ApplyDefaults, and the full
// marshal/unmarshal round-trip.
func TestVectorIndexConfig_TuningKnobs_YAMLRoundTrip(t *testing.T) {
	const src = `
vector_index:
  embedding_batch_size: 16
  embedding_cache_max_bytes: 1048576
  prep_workers: 4
  debounce_ms: 250
  chunk_overlap: 120
  search_wait_timeout_ms: 0
  park_capacity: 7
  park_budget_mb: 2048
`
	var cfg Config
	if err := yaml.Unmarshal([]byte(src), &cfg); err != nil {
		t.Fatalf("yaml.Unmarshal() failed: %v", err)
	}
	if cfg.VectorIndex.EmbeddingBatchSize != 16 {
		t.Errorf("embedding_batch_size = %d, want 16", cfg.VectorIndex.EmbeddingBatchSize)
	}
	if cfg.VectorIndex.EmbeddingCacheMaxBytes != 1048576 {
		t.Errorf("embedding_cache_max_bytes = %d, want 1048576", cfg.VectorIndex.EmbeddingCacheMaxBytes)
	}
	if cfg.VectorIndex.PrepWorkers != 4 {
		t.Errorf("prep_workers = %d, want 4", cfg.VectorIndex.PrepWorkers)
	}
	if cfg.VectorIndex.DebounceMs != 250 {
		t.Errorf("debounce_ms = %d, want 250", cfg.VectorIndex.DebounceMs)
	}
	if cfg.VectorIndex.ChunkOverlap != 120 {
		t.Errorf("chunk_overlap = %d, want 120", cfg.VectorIndex.ChunkOverlap)
	}
	if cfg.VectorIndex.SearchWaitTimeoutMs == nil || *cfg.VectorIndex.SearchWaitTimeoutMs != 0 {
		t.Errorf("explicit search_wait_timeout_ms: 0 must parse as the fail-fast sentinel (pointer to 0), got %v", cfg.VectorIndex.SearchWaitTimeoutMs)
	}
	if cfg.VectorIndex.ParkCapacity == nil || *cfg.VectorIndex.ParkCapacity != 7 {
		t.Errorf("explicit park_capacity must parse verbatim, got %v", cfg.VectorIndex.ParkCapacity)
	}
	if cfg.VectorIndex.ParkBudgetMb == nil || *cfg.VectorIndex.ParkBudgetMb != 2048 {
		t.Errorf("explicit park_budget_mb must parse verbatim, got %v", cfg.VectorIndex.ParkBudgetMb)
	}

	// ApplyDefaults must fill in unset knobs but PRESERVE the explicit
	// fail-fast sentinel (an unset key resolves to 3000 instead — covered
	// by TestVectorIndexConfig_TuningKnobs_Defaults).
	ApplyDefaults(&cfg)
	if cfg.VectorIndex.EmbeddingBatchSize != 16 {
		t.Errorf("ApplyDefaults clobbered explicit embedding_batch_size: got %d, want 16", cfg.VectorIndex.EmbeddingBatchSize)
	}
	if cfg.VectorIndex.EmbeddingCacheMaxBytes != 1048576 {
		t.Errorf("ApplyDefaults clobbered explicit embedding_cache_max_bytes: got %d, want 1048576", cfg.VectorIndex.EmbeddingCacheMaxBytes)
	}
	if cfg.VectorIndex.SearchWaitTimeoutMs == nil || *cfg.VectorIndex.SearchWaitTimeoutMs != 0 {
		t.Errorf("ApplyDefaults must not overwrite an explicit search_wait_timeout_ms: 0, got %v", cfg.VectorIndex.SearchWaitTimeoutMs)
	}
	if cfg.VectorIndex.ParkCapacity == nil || *cfg.VectorIndex.ParkCapacity != 7 {
		t.Errorf("ApplyDefaults must not overwrite an explicit park_capacity, got %v", cfg.VectorIndex.ParkCapacity)
	}
	if cfg.VectorIndex.ParkBudgetMb == nil || *cfg.VectorIndex.ParkBudgetMb != 2048 {
		t.Errorf("ApplyDefaults must not overwrite an explicit park_budget_mb, got %v", cfg.VectorIndex.ParkBudgetMb)
	}

	// Marshal → unmarshal round-trip preserves every knob verbatim.
	data, err := yaml.Marshal(&cfg)
	if err != nil {
		t.Fatalf("yaml.Marshal() failed: %v", err)
	}
	var restored Config
	if err := yaml.Unmarshal(data, &restored); err != nil {
		t.Fatalf("round-trip yaml.Unmarshal() failed: %v", err)
	}
	if restored.VectorIndex.EmbeddingBatchSize != 16 {
		t.Errorf("round-tripped embedding_batch_size = %d, want 16", restored.VectorIndex.EmbeddingBatchSize)
	}
	if restored.VectorIndex.EmbeddingCacheMaxBytes != 1048576 {
		t.Errorf("round-tripped embedding_cache_max_bytes = %d, want 1048576", restored.VectorIndex.EmbeddingCacheMaxBytes)
	}
	if restored.VectorIndex.PrepWorkers != 4 {
		t.Errorf("round-tripped prep_workers = %d, want 4", restored.VectorIndex.PrepWorkers)
	}
	if restored.VectorIndex.DebounceMs != 250 {
		t.Errorf("round-tripped debounce_ms = %d, want 250", restored.VectorIndex.DebounceMs)
	}
	if restored.VectorIndex.ChunkOverlap != 120 {
		t.Errorf("round-tripped chunk_overlap = %d, want 120", restored.VectorIndex.ChunkOverlap)
	}
	if restored.VectorIndex.SearchWaitTimeoutMs == nil || *restored.VectorIndex.SearchWaitTimeoutMs != 0 {
		t.Errorf("round-tripped search_wait_timeout_ms must stay the fail-fast sentinel (pointer to 0), got %v", restored.VectorIndex.SearchWaitTimeoutMs)
	}
	if restored.VectorIndex.ParkCapacity == nil || *restored.VectorIndex.ParkCapacity != 7 {
		t.Errorf("round-tripped park_capacity = %v, want 7", restored.VectorIndex.ParkCapacity)
	}
	if restored.VectorIndex.ParkBudgetMb == nil || *restored.VectorIndex.ParkBudgetMb != 2048 {
		t.Errorf("round-tripped park_budget_mb = %v, want 2048", restored.VectorIndex.ParkBudgetMb)
	}
}

// TestVectorIndexConfig_ParkCapacity_DisableSentinel pins that an explicit
// park_capacity: 0 survives the full Load path (defaults + validation) as the
// "parking disabled" sentinel — distinct from an unset key, which defaults to
// 3 (covered by TestVectorIndexConfig_TuningKnobs_Defaults). Without the
// pointer type the two would be indistinguishable and parking could never be
// turned off.
func TestVectorIndexConfig_ParkCapacity_DisableSentinel(t *testing.T) {
	content := `
llm:
  default_model: claude-3-haiku
  anthropic:
    api_key: "test-key"
    models:
      - claude-3-haiku
vector_index:
  park_capacity: 0
`
	configPath := writeTestConfig(t, content)

	cfg, err := Load(configPath)
	if err != nil {
		t.Fatalf("Load() failed: %v", err)
	}
	if cfg.VectorIndex.ParkCapacity == nil {
		t.Fatal("park_capacity should be non-nil after Load")
	}
	if *cfg.VectorIndex.ParkCapacity != 0 {
		t.Errorf("explicit park_capacity: 0 must survive as the disable sentinel, got %d", *cfg.VectorIndex.ParkCapacity)
	}
}

// TestVectorIndexConfig_ParkBudget_Sentinels pins the pointer-int64 semantics
// of park_budget_mb through the full Load path (defaults + validation): an
// explicit -1 survives as the "budget disabled" sentinel (park_capacity alone
// bounds the LRU), while an explicit 0 is rejected as ambiguous and any value
// below -1 (only -1 disables, mirroring memory_soft_limit_mb) or above the
// 2 PiB ceiling (which would overflow the MiB→bytes shift) is rejected —
// distinct from an unset key, which resolves to 1024 (covered by
// TestVectorIndexConfig_TuningKnobs_Defaults).
func TestVectorIndexConfig_ParkBudget_Sentinels(t *testing.T) {
	const disable = `
llm:
  default_model: claude-3-haiku
  anthropic:
    api_key: "test-key"
    models:
      - claude-3-haiku
vector_index:
  park_budget_mb: -1
`
	configPath := writeTestConfig(t, disable)
	cfg, err := Load(configPath)
	if err != nil {
		t.Fatalf("Load() with the disable sentinel failed: %v", err)
	}
	if cfg.VectorIndex.ParkBudgetMb == nil {
		t.Fatal("park_budget_mb should be non-nil after Load")
	}
	if *cfg.VectorIndex.ParkBudgetMb != -1 {
		t.Errorf("explicit park_budget_mb: -1 must survive as the disable sentinel, got %d", *cfg.VectorIndex.ParkBudgetMb)
	}

	const ambiguous = `
llm:
  default_model: claude-3-haiku
  anthropic:
    api_key: "test-key"
    models:
      - claude-3-haiku
vector_index:
  park_budget_mb: 0
`
	configPath = writeTestConfig(t, ambiguous)
	if _, err := Load(configPath); err == nil {
		t.Error("Load() with park_budget_mb: 0 must fail validation (ambiguous), got nil error")
	}

	// Only -1 disables the byte budget, mirroring memory_soft_limit_mb: a
	// value below it is a typo, not a sentinel, and must fail fast at load.
	const belowSentinel = `
llm:
  default_model: claude-3-haiku
  anthropic:
    api_key: "test-key"
    models:
      - claude-3-haiku
vector_index:
  park_budget_mb: -2
`
	configPath = writeTestConfig(t, belowSentinel)
	if _, err := Load(configPath); err == nil {
		t.Error("Load() with park_budget_mb: -2 must fail validation (only -1 disables), got nil error")
	}

	// A value above the 2 PiB ceiling is rejected so the MiB→bytes shift cannot
	// overflow to a negative, which the service would read as "budget disabled".
	const aboveCeiling = `
llm:
  default_model: claude-3-haiku
  anthropic:
    api_key: "test-key"
    models:
      - claude-3-haiku
vector_index:
  park_budget_mb: 4294967296
`
	configPath = writeTestConfig(t, aboveCeiling)
	if _, err := Load(configPath); err == nil {
		t.Error("Load() with park_budget_mb: 4294967296 must fail validation (above the 2 PiB ceiling), got nil error")
	}
}

// TestVectorIndexConfig_LegacyYAMLCompat pins that a config written before the
// embedding-optimization cycle (no embedding_cache_max_bytes / content_filter
// keys) still loads through the full Load path (defaults + validation) and
// resolves to behavior-compatible values: the legacy inference knobs keep
// their historical meaning, the embedding cache is additive, and the content
// filter resolves to the package-default policy.
func TestVectorIndexConfig_LegacyYAMLCompat(t *testing.T) {
	content := `
llm:
  default_model: claude-3-haiku
  anthropic:
    api_key: "test-key"
    models:
      - claude-3-haiku
vector_index:
  hybrid: true
  max_file_size: 4194304
  max_chunk_size: 1500
  max_chunks_per_file: 4000
`
	configPath := writeTestConfig(t, content)

	cfg, err := Load(configPath)
	if err != nil {
		t.Fatalf("Load() of a pre-cycle config failed: %v", err)
	}

	vi := cfg.VectorIndex
	// Inference defaults identical to the legacy pipeline: the sp4rk
	// historical batch capacity and the "all cores" thread policy.
	if vi.EmbeddingBatchSize != 32 {
		t.Errorf("legacy config embedding_batch_size = %d, want 32 (sp4rk embedding.DefaultBatchSize)", vi.EmbeddingBatchSize)
	}
	if vi.EmbeddingThreads != 0 {
		t.Errorf("legacy config embedding_threads = %d, want 0 (all cores)", vi.EmbeddingThreads)
	}
	// The embedding cache is additive: a default cap, never a load failure.
	if vi.EmbeddingCacheMaxBytes != 512<<20 {
		t.Errorf("legacy config embedding_cache_max_bytes = %d, want %d (default cap)", vi.EmbeddingCacheMaxBytes, int64(512<<20))
	}
	// The absent content_filter block resolves to exactly the package
	// default policy (enabled with default thresholds) — no partial
	// zero-value leakage from the YAML layer struct.
	resolved := vi.ContentFilter.ResolveContentFilter()
	if !resolved.Enabled {
		t.Errorf("legacy config content filter = disabled, want the default-on package policy")
	}
	if !reflect.DeepEqual(resolved, vectorindex.DefaultContentFilterConfig()) {
		t.Errorf("legacy config content filter = %+v, want %+v", resolved, vectorindex.DefaultContentFilterConfig())
	}
}

// TestVectorIndexConfig_ExecutionProvider_YAMLRoundTrip covers YAML parsing
// of the execution provider / device knobs. Pure struct-level parsing: an
// unset or explicitly empty execution_provider stays "" here — the
// normalization to "auto" happens in ApplyDefaults (covered by
// TestVectorIndexConfig_ExecutionProvider_Defaults below).
func TestVectorIndexConfig_ExecutionProvider_YAMLRoundTrip(t *testing.T) {
	tests := []struct {
		name         string
		yaml         string
		wantProvider string
		wantDeviceID int
	}{
		{
			name:         "unset provider and device parse as zero values",
			yaml:         "hybrid: true\n",
			wantProvider: "",
			wantDeviceID: 0,
		},
		{
			name:         "cuda with device 1",
			yaml:         "execution_provider: cuda\ndevice_id: 1\n",
			wantProvider: "cuda",
			wantDeviceID: 1,
		},
		{
			name:         "cpu keeps device id parseable but irrelevant",
			yaml:         "execution_provider: cpu\ndevice_id: 3\n",
			wantProvider: "cpu",
			wantDeviceID: 3,
		},
		{
			name:         "explicit empty provider string",
			yaml:         "execution_provider: \"\"\ndevice_id: 0\n",
			wantProvider: "",
			wantDeviceID: 0,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			var cfg VectorIndexConfig
			if err := yaml.Unmarshal([]byte(tt.yaml), &cfg); err != nil {
				t.Fatalf("yaml.Unmarshal() failed: %v", err)
			}
			if cfg.ExecutionProvider != tt.wantProvider {
				t.Errorf("ExecutionProvider = %q, want %q", cfg.ExecutionProvider, tt.wantProvider)
			}
			if cfg.DeviceID != tt.wantDeviceID {
				t.Errorf("DeviceID = %d, want %d", cfg.DeviceID, tt.wantDeviceID)
			}
		})
	}

	// Round-trip: set values must survive marshal + unmarshal unchanged.
	original := VectorIndexConfig{ExecutionProvider: "cuda", DeviceID: 1}
	data, err := yaml.Marshal(&original)
	if err != nil {
		t.Fatalf("yaml.Marshal() failed: %v", err)
	}

	var restored VectorIndexConfig
	if err := yaml.Unmarshal(data, &restored); err != nil {
		t.Fatalf("yaml.Unmarshal() failed: %v", err)
	}
	if restored.ExecutionProvider != "cuda" {
		t.Errorf("round-tripped ExecutionProvider = %q, want cuda", restored.ExecutionProvider)
	}
	if restored.DeviceID != 1 {
		t.Errorf("round-tripped DeviceID = %d, want 1", restored.DeviceID)
	}
}

// TestVectorIndexConfig_ExecutionProvider_Defaults pins the defaulting
// contract: an unset (or empty) execution_provider normalizes to "auto",
// an explicit value is preserved, and device_id's zero value is already
// the valid default (nothing to normalize).
func TestVectorIndexConfig_ExecutionProvider_Defaults(t *testing.T) {
	// Zero-value config (no vector_index block at all).
	cfg := &Config{}
	ApplyDefaults(cfg)
	if cfg.VectorIndex.ExecutionProvider != VectorIndexProviderAuto {
		t.Errorf("default execution_provider = %q, want %q", cfg.VectorIndex.ExecutionProvider, VectorIndexProviderAuto)
	}
	if cfg.VectorIndex.DeviceID != 0 {
		t.Errorf("default device_id = %d, want 0", cfg.VectorIndex.DeviceID)
	}

	// Explicit values survive ApplyDefaults untouched.
	cfg = &Config{VectorIndex: VectorIndexConfig{ExecutionProvider: "cuda", DeviceID: 2}}
	ApplyDefaults(cfg)
	if cfg.VectorIndex.ExecutionProvider != "cuda" {
		t.Errorf("ApplyDefaults clobbered explicit execution_provider: got %q, want cuda", cfg.VectorIndex.ExecutionProvider)
	}
	if cfg.VectorIndex.DeviceID != 2 {
		t.Errorf("ApplyDefaults clobbered explicit device_id: got %d, want 2", cfg.VectorIndex.DeviceID)
	}

	// An explicit empty string normalizes to "auto" as well.
	cfg = &Config{VectorIndex: VectorIndexConfig{ExecutionProvider: ""}}
	ApplyDefaults(cfg)
	if cfg.VectorIndex.ExecutionProvider != VectorIndexProviderAuto {
		t.Errorf("empty execution_provider = %q, want %q (normalized to auto)", cfg.VectorIndex.ExecutionProvider, VectorIndexProviderAuto)
	}
}

// TestVectorIndexConfig_ExecutionProvider_Validation verifies that the
// full Load path rejects unknown provider values and negative device ids
// with actionable error messages, and accepts every valid combination.
func TestVectorIndexConfig_ExecutionProvider_Validation(t *testing.T) {
	valid := []struct {
		name     string
		provider string
		deviceID int
	}{
		{name: "auto default device", provider: "auto", deviceID: 0},
		{name: "cpu", provider: "cpu", deviceID: 0},
		{name: "cuda device 0", provider: "cuda", deviceID: 0},
		{name: "cuda device 1", provider: "cuda", deviceID: 1},
	}
	for _, tt := range valid {
		t.Run("valid/"+tt.name, func(t *testing.T) {
			content := fmt.Sprintf(`%s
vector_index:
  execution_provider: %s
  device_id: %d
`, securityGroupsTestBase, tt.provider, tt.deviceID)
			cfg, err := Load(writeTestConfig(t, content))
			if err != nil {
				t.Fatalf("Load() failed for execution_provider=%s device_id=%d: %v", tt.provider, tt.deviceID, err)
			}
			if cfg.VectorIndex.ExecutionProvider != tt.provider {
				t.Errorf("execution_provider = %q, want %q", cfg.VectorIndex.ExecutionProvider, tt.provider)
			}
			if cfg.VectorIndex.DeviceID != tt.deviceID {
				t.Errorf("device_id = %d, want %d", cfg.VectorIndex.DeviceID, tt.deviceID)
			}
		})
	}

	invalid := []struct {
		name     string
		yaml     string
		wantPart string
	}{
		{
			name:     "unknown provider tpu",
			yaml:     "vector_index:\n  execution_provider: tpu\n",
			wantPart: "vector_index.execution_provider",
		},
		{
			name:     "negative device id",
			yaml:     "vector_index:\n  device_id: -1\n",
			wantPart: "vector_index.device_id",
		},
	}
	for _, tt := range invalid {
		t.Run("invalid/"+tt.name, func(t *testing.T) {
			content := securityGroupsTestBase + tt.yaml
			_, err := Load(writeTestConfig(t, content))
			if err == nil {
				t.Fatalf("expected validation error, got nil")
			}
			if !contains(err.Error(), tt.wantPart) {
				t.Errorf("expected error to mention %q, got: %v", tt.wantPart, err)
			}
		})
	}
}

// TestGoalLoopConfig_DefaultsToIndependent verifies that goal_loop.verification
// defaults to "independent" when not specified in the config.
func TestGoalLoopConfig_DefaultsToIndependent(t *testing.T) {
	content := `
llm:
  default_model: claude-3-haiku
  anthropic:
    api_key: "test-key"
    models:
      - claude-3-haiku
`
	configPath := writeTestConfig(t, content)

	cfg, err := Load(configPath)
	if err != nil {
		t.Fatalf("Load() failed: %v", err)
	}

	if cfg.GoalLoop.Verification != "independent" {
		t.Errorf("expected default goal_loop.verification 'independent', got %q", cfg.GoalLoop.Verification)
	}
}

// TestGoalLoopConfig_ValidValues verifies that "independent" and "off" are
// accepted by validation and preserved verbatim.
func TestGoalLoopConfig_ValidValues(t *testing.T) {
	for _, val := range []string{"independent", "off"} {
		t.Run(val, func(t *testing.T) {
			content := fmt.Sprintf(`
llm:
  default_model: claude-3-haiku
  anthropic:
    api_key: "test-key"
    models:
      - claude-3-haiku
goal_loop:
  verification: %s
`, val)
			configPath := writeTestConfig(t, content)

			cfg, err := Load(configPath)
			if err != nil {
				t.Fatalf("Load() failed for verification=%q: %v", val, err)
			}
			if cfg.GoalLoop.Verification != val {
				t.Errorf("expected goal_loop.verification %q, got %q", val, cfg.GoalLoop.Verification)
			}
		})
	}
}

// TestGoalLoopConfig_RejectsInvalidValue verifies that validation rejects an
// invalid goal_loop.verification value with a clear error message.
func TestGoalLoopConfig_RejectsInvalidValue(t *testing.T) {
	content := `
llm:
  default_model: claude-3-haiku
  anthropic:
    api_key: "test-key"
    models:
      - claude-3-haiku
goal_loop:
  verification: weird
`
	configPath := writeTestConfig(t, content)

	_, err := Load(configPath)
	if err == nil {
		t.Fatalf("expected error for invalid goal_loop.verification 'weird', got nil")
	}

	if !contains(err.Error(), "goal_loop.verification") {
		t.Errorf("expected error to mention 'goal_loop.verification', got: %v", err)
	}
}

// securityGroupsTestBase is the minimal LLM section every security-groups test
// needs so Load reaches the security validation stage.
const securityGroupsTestBase = `
llm:
  default_model: claude-3-haiku
  anthropic:
    api_key: "test-key"
    models:
      - claude-3-haiku
`

// TestApplyDefaults_SecurityGroups verifies the default group set and policies
// of the security.groups schema.
func TestApplyDefaults_SecurityGroups(t *testing.T) {
	cfg := &Config{}
	ApplyDefaults(cfg)

	if cfg.Security.Groups == nil {
		t.Fatal("expected Security.Groups to be initialized")
	}

	wantPolicies := map[string]string{
		ToolGroupLocalRead:   GroupPolicyAllow,
		ToolGroupRemoteRead:  GroupPolicyAllow,
		ToolGroupExecute:     GroupPolicyUserConfirm,
		ToolGroupLocalWrite:  GroupPolicyUserConfirm,
		ToolGroupLocalMCP:    GroupPolicyUserConfirm,
		ToolGroupRemoteMCP:   GroupPolicyUserConfirm,
		ToolGroupRemoteWrite: GroupPolicyUserConfirm,
	}
	if len(cfg.Security.Groups) != len(wantPolicies) {
		t.Fatalf("expected %d groups, got %d: %v", len(wantPolicies), len(cfg.Security.Groups), cfg.Security.Groups)
	}
	for name, want := range wantPolicies {
		group, ok := cfg.Security.Groups[name]
		if !ok {
			t.Errorf("missing default group %q", name)
			continue
		}
		if group.Policy != want {
			t.Errorf("group %q policy = %q, want %q", name, group.Policy, want)
		}
	}

	// The blocklist is an execute-only feature AND empty by default: no
	// group — execute included — carries predefined patterns.
	for name, group := range cfg.Security.Groups {
		if len(group.Blocklist) > 0 {
			t.Errorf("group %q unexpectedly carries a blocklist (%v): defaults must seed none", name, group.Blocklist)
		}
	}

	// Defaults must never materialize the reserved system group.
	if _, ok := cfg.Security.Groups[ToolGroupSystem]; ok {
		t.Errorf("reserved group %q must not be created by defaults", ToolGroupSystem)
	}
}

// TestApplyDefaults_SecurityGroups_PartialOverride verifies that user-provided
// group entries survive defaults while missing entries and fields are filled.
func TestApplyDefaults_SecurityGroups_PartialOverride(t *testing.T) {
	content := securityGroupsTestBase + `
security:
  groups:
    execute:
      policy: allow
      blocklist:
        - "custom-dangerous-cmd"
    local_write:
      policy: deny
`
	configPath := writeTestConfig(t, content)

	cfg, err := Load(configPath)
	if err != nil {
		t.Fatalf("Load() failed: %v", err)
	}

	// User overrides must be preserved verbatim.
	execute := cfg.Security.Groups[ToolGroupExecute]
	if execute.Policy != GroupPolicyAllow {
		t.Errorf("execute policy = %q, want %q (user override must survive)", execute.Policy, GroupPolicyAllow)
	}
	if !reflect.DeepEqual(execute.Blocklist, []string{"custom-dangerous-cmd"}) {
		t.Errorf("execute blocklist = %v, want [custom-dangerous-cmd]", execute.Blocklist)
	}
	if localWrite := cfg.Security.Groups[ToolGroupLocalWrite]; localWrite.Policy != GroupPolicyDeny {
		t.Errorf("local_write policy = %q, want %q", localWrite.Policy, GroupPolicyDeny)
	}

	// Groups the user did not mention must get their defaults.
	if localRead := cfg.Security.Groups[ToolGroupLocalRead]; localRead.Policy != GroupPolicyAllow {
		t.Errorf("local_read policy = %q, want default %q", localRead.Policy, GroupPolicyAllow)
	}
	if remoteWrite := cfg.Security.Groups[ToolGroupRemoteWrite]; remoteWrite.Policy != GroupPolicyUserConfirm {
		t.Errorf("remote_write policy = %q, want default %q", remoteWrite.Policy, GroupPolicyUserConfirm)
	}
}

// TestConfigValidation_RejectsUnknownSecurityGroup verifies that an
// unrecognized group name is rejected.
func TestConfigValidation_RejectsUnknownSecurityGroup(t *testing.T) {
	content := securityGroupsTestBase + `
security:
  groups:
    local_read:
      policy: allow
    totally_fake:
      policy: allow
`
	configPath := writeTestConfig(t, content)

	_, err := Load(configPath)
	if err == nil {
		t.Fatal("expected validation error for unknown security group")
	}
	if !contains(err.Error(), `unknown security group "totally_fake"`) {
		t.Errorf("expected error to mention the unknown group, got: %v", err)
	}
}

// TestConfigValidation_TrustedGitRepos verifies the security.trusted_git_repos
// schema: each entry is a {path, fingerprint} mapping (absolute path + optional
// snapshot fingerprint). Paths are cleaned in place; relative, empty, and
// duplicate entries are rejected at load time.
func TestConfigValidation_TrustedGitRepos(t *testing.T) {
	t.Run("mapping entries load with fingerprint", func(t *testing.T) {
		repoOne := filepath.Join(os.TempDir(), "trusted-repo-one")
		repoTwo := filepath.Join(os.TempDir(), "trusted-repo-two")
		content := securityGroupsTestBase + fmt.Sprintf(`
security:
  trusted_git_repos:
    - path: %s
      fingerprint: fp-one
    - path: %s
`, repoOne, repoTwo)

		cfg, err := Load(writeTestConfig(t, content))
		if err != nil {
			t.Fatalf("Load() failed: %v", err)
		}
		want := []TrustedGitRepo{
			{Path: repoOne, Fingerprint: "fp-one"},
			{Path: repoTwo},
		}
		if !reflect.DeepEqual(cfg.Security.TrustedGitRepos, want) {
			t.Errorf("TrustedGitRepos = %v, want %v", cfg.Security.TrustedGitRepos, want)
		}
	})

	t.Run("paths are cleaned in place", func(t *testing.T) {
		repoOne := filepath.Join(os.TempDir(), "trusted-repo-one")
		content := securityGroupsTestBase + fmt.Sprintf(`
security:
  trusted_git_repos:
    - path: %s/
`, repoOne)

		cfg, err := Load(writeTestConfig(t, content))
		if err != nil {
			t.Fatalf("Load() failed: %v", err)
		}
		if got := cfg.Security.TrustedGitRepos[0].Path; got != repoOne {
			t.Errorf("TrustedGitRepos[0].Path = %q, want cleaned %q", got, repoOne)
		}
	})

	t.Run("relative path rejected", func(t *testing.T) {
		content := securityGroupsTestBase + `
security:
  trusted_git_repos:
    - path: relative/repo
`
		_, err := Load(writeTestConfig(t, content))
		if err == nil {
			t.Fatal("expected validation error for a relative trusted_git_repos entry")
		}
		if !contains(err.Error(), "must be an absolute path") {
			t.Errorf("expected error to mention absolute paths, got: %v", err)
		}
	})

	t.Run("empty path rejected", func(t *testing.T) {
		content := securityGroupsTestBase + `
security:
  trusted_git_repos:
    - path: ""
`
		_, err := Load(writeTestConfig(t, content))
		if err == nil {
			t.Fatal("expected validation error for an empty trusted_git_repos entry")
		}
		if !contains(err.Error(), "must not contain empty paths") {
			t.Errorf("expected error to mention empty paths, got: %v", err)
		}
	})

	t.Run("duplicate path rejected", func(t *testing.T) {
		repoOne := filepath.Join(os.TempDir(), "trusted-repo-one")
		content := securityGroupsTestBase + fmt.Sprintf(`
security:
  trusted_git_repos:
    - path: %s
    - path: %s/
`, repoOne, repoOne)

		_, err := Load(writeTestConfig(t, content))
		if err == nil {
			t.Fatal("expected validation error for a duplicate trusted_git_repos entry")
		}
		if !contains(err.Error(), "duplicate path") {
			t.Errorf("expected error to mention duplicate paths, got: %v", err)
		}
	})
}

// TestConfigValidation_TrustedGitReposLegacyStringMigrates verifies the legacy
// string form of security.trusted_git_repos (a bare absolute path, written by
// older builds) migrates transparently: each string becomes a path with no
// fingerprint, so already-trusted repositories keep their warning suppression.
func TestConfigValidation_TrustedGitReposLegacyStringMigrates(t *testing.T) {
	repoOne := filepath.Join(os.TempDir(), "trusted-repo-one")
	repoTwo := filepath.Join(os.TempDir(), "trusted-repo-two")
	content := securityGroupsTestBase + fmt.Sprintf(`
security:
  trusted_git_repos:
    - %s
    - %s
`, repoOne, repoTwo)

	cfg, err := Load(writeTestConfig(t, content))
	if err != nil {
		t.Fatalf("Load() failed: %v", err)
	}
	want := []TrustedGitRepo{
		{Path: repoOne},
		{Path: repoTwo},
	}
	if !reflect.DeepEqual(cfg.Security.TrustedGitRepos, want) {
		t.Errorf("TrustedGitRepos = %v, want %v", cfg.Security.TrustedGitRepos, want)
	}
}

// TestConfigValidation_HardenGitRepos verifies the security.harden_git_repos
// list: absolute entries load (and are cleaned in place), while relative,
// empty, and duplicate entries are rejected.
func TestConfigValidation_HardenGitRepos(t *testing.T) {
	t.Run("absolute paths load verbatim", func(t *testing.T) {
		repoOne := filepath.Join(os.TempDir(), "harden-repo-one")
		repoTwo := filepath.Join(os.TempDir(), "harden-repo-two")
		content := securityGroupsTestBase + fmt.Sprintf(`
security:
  harden_git_repos:
    - %s
    - %s/
`, repoOne, repoTwo)

		cfg, err := Load(writeTestConfig(t, content))
		if err != nil {
			t.Fatalf("Load() failed: %v", err)
		}
		want := []string{repoOne, repoTwo}
		if !reflect.DeepEqual(cfg.Security.HardenGitRepos, want) {
			t.Errorf("HardenGitRepos = %v, want %v", cfg.Security.HardenGitRepos, want)
		}
	})

	t.Run("relative path rejected", func(t *testing.T) {
		content := securityGroupsTestBase + `
security:
  harden_git_repos:
    - relative/repo
`
		_, err := Load(writeTestConfig(t, content))
		if err == nil {
			t.Fatal("expected validation error for a relative harden_git_repos entry")
		}
		if !contains(err.Error(), "must be an absolute path") {
			t.Errorf("expected error to mention absolute paths, got: %v", err)
		}
	})

	t.Run("empty entry rejected", func(t *testing.T) {
		content := securityGroupsTestBase + `
security:
  harden_git_repos:
    - ""
`
		_, err := Load(writeTestConfig(t, content))
		if err == nil {
			t.Fatal("expected validation error for an empty harden_git_repos entry")
		}
		if !contains(err.Error(), "must not contain empty paths") {
			t.Errorf("expected error to mention empty paths, got: %v", err)
		}
	})

	t.Run("duplicate path rejected", func(t *testing.T) {
		repoOne := filepath.Join(os.TempDir(), "harden-repo-one")
		content := securityGroupsTestBase + fmt.Sprintf(`
security:
  harden_git_repos:
    - %s
    - %s/
`, repoOne, repoOne)

		_, err := Load(writeTestConfig(t, content))
		if err == nil {
			t.Fatal("expected validation error for a duplicate harden_git_repos entry")
		}
		if !contains(err.Error(), "duplicate path") {
			t.Errorf("expected error to mention duplicate paths, got: %v", err)
		}
	})
}

// TestConfigValidation_TrustedHardenMutualExclusion verifies a repository root
// cannot appear in both security.trusted_git_repos and
// security.harden_git_repos — the two lists are mutually exclusive (trusted
// suppresses the warning, hardened forces it).
func TestConfigValidation_TrustedHardenMutualExclusion(t *testing.T) {
	repoOne := filepath.Join(os.TempDir(), "shared-repo")
	content := securityGroupsTestBase + fmt.Sprintf(`
security:
  trusted_git_repos:
    - path: %s
  harden_git_repos:
    - %s/
`, repoOne, repoOne)

	_, err := Load(writeTestConfig(t, content))
	if err == nil {
		t.Fatal("expected validation error for a root in both trusted and harden lists")
	}
	if !contains(err.Error(), "cannot be both trusted and hardened") {
		t.Errorf("expected error to mention mutual exclusion, got: %v", err)
	}
}

// TestConfigValidation_RejectsSystemSecurityGroup verifies that the reserved
// system group cannot be configured.
func TestConfigValidation_RejectsSystemSecurityGroup(t *testing.T) {
	content := securityGroupsTestBase + `
security:
  groups:
    system:
      policy: allow
`
	configPath := writeTestConfig(t, content)

	_, err := Load(configPath)
	if err == nil {
		t.Fatal("expected validation error for reserved system group")
	}
	if !contains(err.Error(), `reserved`) {
		t.Errorf("expected error to mention the reserved group, got: %v", err)
	}
}

// TestConfigValidation_RejectsGroupBlocklistOutsideExecute verifies that a
// blocklist is rejected on any group other than execute.
func TestConfigValidation_RejectsGroupBlocklistOutsideExecute(t *testing.T) {
	for _, group := range []string{
		ToolGroupLocalRead, ToolGroupRemoteRead, ToolGroupLocalWrite,
		ToolGroupLocalMCP, ToolGroupRemoteMCP, ToolGroupRemoteWrite,
	} {
		t.Run(group, func(t *testing.T) {
			content := fmt.Sprintf(`%s
security:
  groups:
    %s:
      policy: allow
      blocklist:
        - "some-pattern"
`, securityGroupsTestBase, group)
			configPath := writeTestConfig(t, content)

			_, err := Load(configPath)
			if err == nil {
				t.Fatal("expected validation error for blocklist outside execute")
			}
			if !contains(err.Error(), "does not support a blocklist") {
				t.Errorf("expected error to mention blocklist restriction, got: %v", err)
			}
		})
	}
}

// TestConfigValidation_RejectsInvalidGroupPolicyEnum verifies that non-empty
// policy values outside the group enum are rejected. (An empty policy means
// "unset" and is replaced by the group default during ApplyDefaults, so it is
// valid — see TestApplyDefaults_SecurityGroups_PartialOverride.)
func TestConfigValidation_RejectsInvalidGroupPolicyEnum(t *testing.T) {
	for _, policy := range []string{"always_allow", "sometimes", "ALLOW"} {
		t.Run("policy="+policy, func(t *testing.T) {
			content := fmt.Sprintf(`%s
security:
  groups:
    local_read:
      policy: %q
`, securityGroupsTestBase, policy)
			configPath := writeTestConfig(t, content)

			_, err := Load(configPath)
			if err == nil {
				t.Fatal("expected validation error for invalid group policy")
			}
			if !contains(err.Error(), "invalid policy") {
				t.Errorf("expected error to mention invalid policy, got: %v", err)
			}
		})
	}
}

// TestConfigValidation_AcceptsValidSecurityGroups verifies that a fully valid
// groups section parses, keeps user values, and passes validation.
func TestConfigValidation_AcceptsValidSecurityGroups(t *testing.T) {
	content := securityGroupsTestBase + `
security:
  groups:
    local_read:
      policy: deny
    execute:
      policy: user_confirm
      blocklist:
        - "rm\\s+-rf\\s+/"
    remote_write:
      policy: allow
`
	configPath := writeTestConfig(t, content)

	cfg, err := Load(configPath)
	if err != nil {
		t.Fatalf("Load() failed: %v", err)
	}

	if cfg.Security.Groups[ToolGroupLocalRead].Policy != GroupPolicyDeny {
		t.Errorf("local_read policy = %q, want %q", cfg.Security.Groups[ToolGroupLocalRead].Policy, GroupPolicyDeny)
	}
	if cfg.Security.Groups[ToolGroupRemoteWrite].Policy != GroupPolicyAllow {
		t.Errorf("remote_write policy = %q, want %q", cfg.Security.Groups[ToolGroupRemoteWrite].Policy, GroupPolicyAllow)
	}
	if !contains(strings.Join(cfg.Security.Groups[ToolGroupExecute].Blocklist, "\n"), `rm\s+-rf\s+/`) {
		t.Errorf("execute blocklist = %v, want to contain the user pattern", cfg.Security.Groups[ToolGroupExecute].Blocklist)
	}
}

// TestLoad_BlocklistReadsAndRoundTrips pins the new key end to end: a
// `blocklist:` list on the execute group loads into Blocklist, passes
// validation, and Save writes it back under the same key.
func TestLoad_BlocklistReadsAndRoundTrips(t *testing.T) {
	content := securityGroupsTestBase + `
security:
  groups:
    execute:
      policy: allow
      blocklist:
        - 'sudo\s+'
        - 'mkfs'
`
	cfg, err := Load(writeTestConfig(t, content))
	if err != nil {
		t.Fatalf("Load() failed: %v", err)
	}
	want := []string{`sudo\s+`, `mkfs`}
	if !reflect.DeepEqual(cfg.Security.Groups[ToolGroupExecute].Blocklist, want) {
		t.Fatalf("execute blocklist = %v, want %v", cfg.Security.Groups[ToolGroupExecute].Blocklist, want)
	}
	if cfg.Security.Groups[ToolGroupExecute].LegacyBlacklist != nil {
		t.Errorf("legacy field must stay nil for a blocklist-only config, got %v", cfg.Security.Groups[ToolGroupExecute].LegacyBlacklist)
	}

	saved := filepath.Join(t.TempDir(), "config.yaml")
	if err := Save(cfg, saved); err != nil {
		t.Fatalf("Save() failed: %v", err)
	}
	raw, err := os.ReadFile(saved)
	if err != nil {
		t.Fatalf("read saved config: %v", err)
	}
	text := string(raw)
	if !strings.Contains(text, "blocklist:") {
		t.Errorf("saved config must carry the blocklist key:\n%s", text)
	}
	if strings.Contains(text, "blacklist:") {
		t.Errorf("saved config must not carry the legacy blacklist key:\n%s", text)
	}
	for _, pat := range want {
		if !strings.Contains(text, pat) {
			t.Errorf("saved config must contain pattern %q:\n%s", pat, text)
		}
	}
}

// yamlSingleList renders a []string as YAML list lines under key, single-
// quoting each entry (doubling embedded single quotes) so regex patterns
// round-trip verbatim.
func yamlSingleList(key string, patterns []string) string {
	var b strings.Builder
	b.WriteString(key + ":\n")
	for _, pat := range patterns {
		b.WriteString("        - '" + strings.ReplaceAll(pat, "'", "''") + "'\n")
	}
	return b.String()
}

// TestLoad_LegacyBlacklistMigratesCustom covers the one-time migration for
// existing users: a legacy `blacklist:` list that DIFFERS from the shipped
// legacy default is carried over into Blocklist at load, and the next Save
// persists it under the new `blocklist` key while the stale `blacklist` key
// disappears.
func TestLoad_LegacyBlacklistMigratesCustom(t *testing.T) {
	custom := []string{`custom\s+danger`, `mkfs`}
	content := securityGroupsTestBase + `
security:
  groups:
    execute:
      policy: user_confirm
` + yamlSingleList("      blacklist", custom)
	cfg, err := Load(writeTestConfig(t, content))
	if err != nil {
		t.Fatalf("Load() failed: %v", err)
	}
	exec := cfg.Security.Groups[ToolGroupExecute]
	if !reflect.DeepEqual(exec.Blocklist, custom) {
		t.Fatalf("customized legacy blacklist must migrate verbatim: blocklist = %v, want %v", exec.Blocklist, custom)
	}
	if exec.LegacyBlacklist != nil {
		t.Errorf("legacy field must be cleared after migration, got %v", exec.LegacyBlacklist)
	}

	saved := filepath.Join(t.TempDir(), "config.yaml")
	if err := Save(cfg, saved); err != nil {
		t.Fatalf("Save() failed: %v", err)
	}
	raw, err := os.ReadFile(saved)
	if err != nil {
		t.Fatalf("read saved config: %v", err)
	}
	text := string(raw)
	if !strings.Contains(text, "blocklist:") || !strings.Contains(text, `custom\s+danger`) {
		t.Errorf("saved config must persist the migrated list under blocklist:\n%s", text)
	}
	if strings.Contains(text, "blacklist:") {
		t.Errorf("saved config must drop the legacy blacklist key:\n%s", text)
	}
}

// TestLoad_LegacyBlacklistDefaultDropped covers the removal half: a legacy
// `blacklist:` list that is exactly the old shipped default (or absent)
// disappears — the effective blocklist is empty and Save writes neither key.
func TestLoad_LegacyBlacklistDefaultDropped(t *testing.T) {
	t.Run("default-equal legacy list", func(t *testing.T) {
		content := securityGroupsTestBase + `
security:
  groups:
    execute:
      policy: user_confirm
` + yamlSingleList("      blacklist", legacyDefaultExecuteGroupBlacklist())
		cfg, err := Load(writeTestConfig(t, content))
		if err != nil {
			t.Fatalf("Load() failed: %v", err)
		}
		exec := cfg.Security.Groups[ToolGroupExecute]
		if len(exec.Blocklist) > 0 {
			t.Errorf("default-equal legacy blacklist must be dropped, got %d patterns", len(exec.Blocklist))
		}
		if exec.LegacyBlacklist != nil {
			t.Errorf("legacy field must be cleared, got %d patterns", len(exec.LegacyBlacklist))
		}
		saved := filepath.Join(t.TempDir(), "config.yaml")
		if err := Save(cfg, saved); err != nil {
			t.Fatalf("Save() failed: %v", err)
		}
		raw, err := os.ReadFile(saved)
		if err != nil {
			t.Fatalf("read saved config: %v", err)
		}
		if text := string(raw); strings.Contains(text, "blacklist:") || strings.Contains(text, "blocklist:") {
			t.Errorf("saved config must write neither key:\n%s", text)
		}
	})

	t.Run("missing legacy list", func(t *testing.T) {
		content := securityGroupsTestBase + `
security:
  groups:
    execute:
      policy: user_confirm
`
		cfg, err := Load(writeTestConfig(t, content))
		if err != nil {
			t.Fatalf("Load() failed: %v", err)
		}
		if got := cfg.Security.Groups[ToolGroupExecute].Blocklist; got != nil {
			t.Errorf("blocklist must stay unset, got %v", got)
		}
	})
}

// TestLoad_LegacyBlacklistEdgeCases pins the remaining migration rules: the
// new `blocklist` key wins over a stale legacy value, and a legacy list on a
// non-execute group is dropped silently (it never validated pre-rename).
func TestLoad_LegacyBlacklistEdgeCases(t *testing.T) {
	t.Run("blocklist wins over legacy blacklist", func(t *testing.T) {
		content := securityGroupsTestBase + `
security:
  groups:
    execute:
      policy: allow
      blocklist:
        - 'new-key\s+pattern'
      blacklist:
        - 'stale-legacy-pattern'
`
		cfg, err := Load(writeTestConfig(t, content))
		if err != nil {
			t.Fatalf("Load() failed: %v", err)
		}
		exec := cfg.Security.Groups[ToolGroupExecute]
		if !reflect.DeepEqual(exec.Blocklist, []string{`new-key\s+pattern`}) {
			t.Errorf("explicit blocklist must win over the legacy key, got %v", exec.Blocklist)
		}
		if exec.LegacyBlacklist != nil {
			t.Errorf("legacy field must be cleared, got %v", exec.LegacyBlacklist)
		}
	})

	t.Run("legacy list on non-execute group dropped", func(t *testing.T) {
		content := securityGroupsTestBase + `
security:
  groups:
    local_read:
      policy: allow
      blacklist:
        - 'legacy-pattern'
`
		cfg, err := Load(writeTestConfig(t, content))
		if err != nil {
			t.Fatalf("Load() must not fail for a legacy list on a non-execute group: %v", err)
		}
		localRead := cfg.Security.Groups[ToolGroupLocalRead]
		if len(localRead.Blocklist) > 0 || localRead.LegacyBlacklist != nil {
			t.Errorf("legacy list on a non-execute group must be dropped, got blocklist=%v legacy=%v", localRead.Blocklist, localRead.LegacyBlacklist)
		}
	})
}

// legacySecurityYAML is a pre-group-policies (pre-ADR-024) config file: the
// security section still uses tool_policies/default_policy, and one partial
// groups entry proves new-schema user values survive alongside the legacy
// keys being ignored.
const legacySecurityYAML = `
llm:
  default_model: claude-3-haiku
  anthropic:
    api_key: "test-key"
    models:
      - claude-3-haiku
security:
  judge:
    model: judge-model
  default_policy: always_allow
  tool_policies:
    bash_exec:
      policy: always_allow
      blacklist:
        - "legacy-pattern-.*"
    write_file:
      policy: always_deny
    web_search:
      policy: user_confirm
  groups:
    local_read:
      policy: deny
`

// TestLoad_LegacySecuritySchema_DroppedAndDefaultsApplied verifies the
// ADR-024 contract for existing users: a config written by an older build
// loads without errors, the legacy tool_policies/default_policy keys (and
// their values) never reach the Config struct, and every security group is
// back-filled with its default policy unless the new groups schema set one.
func TestLoad_LegacySecuritySchema_DroppedAndDefaultsApplied(t *testing.T) {
	configPath := writeTestConfig(t, legacySecurityYAML)

	result, err := LoadWithResult(configPath)
	if err != nil {
		t.Fatalf("LoadWithResult() failed on legacy config: %v", err)
	}
	if len(result.LoadErrors) > 0 {
		t.Fatalf("LoadErrors = %v, want none", result.LoadErrors)
	}

	groups := result.Config.Security.Groups
	if len(groups) != len(SortedToolGroupNames()) {
		t.Fatalf("groups count = %d, want %d (%v)", len(groups), len(SortedToolGroupNames()), groups)
	}
	// The one group the user set via the NEW schema keeps its value.
	if got := groups[ToolGroupLocalRead].Policy; got != GroupPolicyDeny {
		t.Errorf("local_read policy = %q, want %q (new-schema value must survive)", got, GroupPolicyDeny)
	}
	// Legacy values must not leak into the new schema: default_policy
	// "always_allow" and the tool_policies entries are discarded, so every
	// other group falls back to its default policy.
	if got := groups[ToolGroupExecute].Policy; got != GroupPolicyUserConfirm {
		t.Errorf("execute policy = %q, want default %q (default_policy must not leak)", got, GroupPolicyUserConfirm)
	}
	if got := groups[ToolGroupRemoteRead].Policy; got != GroupPolicyAllow {
		t.Errorf("remote_read policy = %q, want default %q (tool_policies.web_search must not leak)", got, GroupPolicyAllow)
	}
	if got := groups[ToolGroupLocalWrite].Policy; got != GroupPolicyUserConfirm {
		t.Errorf("local_write policy = %q, want default %q (tool_policies.write_file must not leak)", got, GroupPolicyUserConfirm)
	}
	// The legacy per-tool blacklist must not leak either: the execute group
	// keeps an EMPTY blocklist (defaults seed nothing), not "legacy-pattern-.*".
	if got := groups[ToolGroupExecute].Blocklist; len(got) > 0 {
		t.Errorf("execute blocklist = %v, want empty (legacy per-tool blacklist must not leak)", got)
	}
	// Unrelated security settings survive untouched.
	if result.Config.Security.Judge.Model != "judge-model" {
		t.Errorf("judge model = %q, want %q", result.Config.Security.Judge.Model, "judge-model")
	}
}

// TestLegacySecuritySchema_RoundTripWipesLegacyKeys proves the second half of
// the contract: after loading a legacy config, the next Save writes the new
// groups schema and physically erases the legacy keys from the file.
func TestLegacySecuritySchema_RoundTripWipesLegacyKeys(t *testing.T) {
	configPath := writeTestConfig(t, legacySecurityYAML)

	result, err := LoadWithResult(configPath)
	if err != nil {
		t.Fatalf("LoadWithResult() failed: %v", err)
	}

	savedPath := filepath.Join(t.TempDir(), "config.yaml")
	if err := Save(result.Config, savedPath); err != nil {
		t.Fatalf("Save() failed: %v", err)
	}
	raw, err := os.ReadFile(savedPath)
	if err != nil {
		t.Fatalf("read saved config: %v", err)
	}
	text := string(raw)

	for _, legacyKey := range []string{"tool_policies", "default_policy", "always_allow", "legacy-pattern"} {
		if strings.Contains(text, legacyKey) {
			t.Errorf("saved config still contains legacy key %q:\n%s", legacyKey, text)
		}
	}
	for _, want := range []string{"groups:", "local_read:", "deny"} {
		if !strings.Contains(text, want) {
			t.Errorf("saved config is missing %q:\n%s", want, text)
		}
	}

	// The saved file must load back cleanly (defaults idempotent, validation
	// passes) — this is exactly what the next app start will do.
	reloaded, err := LoadWithResult(savedPath)
	if err != nil {
		t.Fatalf("reload of saved config failed: %v", err)
	}
	if got := reloaded.Config.Security.Groups[ToolGroupLocalRead].Policy; got != GroupPolicyDeny {
		t.Errorf("reloaded local_read policy = %q, want %q", got, GroupPolicyDeny)
	}
}

// TestResolveAndLoad_LegacyConfig_StartsWithoutErrors exercises the real
// startup path (desktop/startup_phases.go -> ResolveAndLoad) against a
// config.yaml written by an older build: startup must succeed with no load
// errors, use the user's file (not a fallback), and expose default group
// policies.
func TestResolveAndLoad_LegacyConfig_StartsWithoutErrors(t *testing.T) {
	tmpHome := t.TempDir()
	t.Setenv("HOME", tmpHome)
	t.Setenv("USERPROFILE", tmpHome)

	orig, _ := os.Getwd()
	tmpWd := t.TempDir()
	if err := os.Chdir(tmpWd); err != nil {
		t.Fatalf("chdir: %v", err)
	}
	t.Cleanup(func() { _ = os.Chdir(orig) })

	// Write the legacy config where the app expects it.
	agentDir := filepath.Join(tmpHome, DefaultAgentDir)
	if err := os.MkdirAll(agentDir, 0o755); err != nil {
		t.Fatalf("mkdir agent dir: %v", err)
	}
	if err := os.WriteFile(ConfigPath(agentDir), []byte(legacySecurityYAML), 0o644); err != nil {
		t.Fatalf("write legacy config: %v", err)
	}

	resolved := ResolveAndLoad(newDiscardLogger())
	if resolved.Config == nil {
		t.Fatal("expected non-nil Config")
	}
	if len(resolved.LoadErrors) > 0 {
		t.Fatalf("LoadErrors = %v, want none (legacy config must not break startup)", resolved.LoadErrors)
	}
	if resolved.ConfigPath != ConfigPath(agentDir) {
		t.Errorf("ConfigPath = %q, want %q", resolved.ConfigPath, ConfigPath(agentDir))
	}
	if got := resolved.Config.Security.Groups[ToolGroupExecute].Policy; got != GroupPolicyUserConfirm {
		t.Errorf("execute policy = %q, want default %q", got, GroupPolicyUserConfirm)
	}
}

// TestVectorIndexConfig_ContentFilter_Defaults pins the content-filter
// compatibility contract: a config without a content_filter block resolves to
// the vectorindex package defaults (policy on, all detectors on, package
// thresholds), so existing configs gain the skip rules with no YAML change.
func TestVectorIndexConfig_ContentFilter_Defaults(t *testing.T) {
	cfg := &Config{}
	ApplyDefaults(cfg)

	want := vectorindex.DefaultContentFilterConfig()
	checkBool := func(name string, got *bool, want bool) {
		if got == nil || *got != want {
			t.Errorf("default content_filter.%s = %v, want %v", name, got, want)
		}
	}
	checkBool("enabled", cfg.VectorIndex.ContentFilter.Enabled, want.Enabled)
	checkBool("detect_generated", cfg.VectorIndex.ContentFilter.DetectGenerated, want.DetectGenerated)
	checkBool("detect_minified", cfg.VectorIndex.ContentFilter.DetectMinified, want.DetectMinified)
	checkBool("detect_pathological", cfg.VectorIndex.ContentFilter.DetectPathological, want.DetectPathological)

	cf := cfg.VectorIndex.ContentFilter
	if cf.GeneratedHeaderBytes != want.GeneratedHeaderBytes ||
		cf.MinifiedMinBytes != want.MinifiedMinBytes ||
		cf.MinifiedMaxLineBytes != want.MinifiedMaxLineBytes ||
		cf.MinifiedMaxWhitespaceRatio != want.MinifiedMaxWhitespaceRatio ||
		cf.PathologicalMinBytes != want.PathologicalMinBytes ||
		cf.PathologicalMaxTokenBytes != want.PathologicalMaxTokenBytes {
		t.Errorf("default content_filter thresholds = %+v, want %+v", cf, want)
	}

	// ResolveContentFilter must reproduce the package defaults exactly.
	if resolved := cf.ResolveContentFilter(); resolved != want {
		t.Errorf("ResolveContentFilter(defaults) = %+v, want %+v", resolved, want)
	}
}

// TestVectorIndexConfig_ContentFilter_YAMLRoundTrip covers parsing an
// explicit content_filter block, the interaction with ApplyDefaults (an
// explicit false must be preserved, never re-enabled), and the marshal
// round-trip.
func TestVectorIndexConfig_ContentFilter_YAMLRoundTrip(t *testing.T) {
	const src = `
vector_index:
  content_filter:
    enabled: false
    detect_generated: false
    detect_minified: true
    detect_pathological: true
    generated_header_bytes: 4096
    minified_min_bytes: 32768
    minified_max_line_bytes: 8192
    minified_max_whitespace_ratio: 0.05
    pathological_min_bytes: 65536
    pathological_max_token_bytes: 16384
`
	var cfg Config
	if err := yaml.Unmarshal([]byte(src), &cfg); err != nil {
		t.Fatalf("yaml.Unmarshal() failed: %v", err)
	}
	cf := cfg.VectorIndex.ContentFilter
	if cf.Enabled == nil || *cf.Enabled {
		t.Errorf("enabled = %v, want explicit false", cf.Enabled)
	}
	if cf.DetectGenerated == nil || *cf.DetectGenerated {
		t.Errorf("detect_generated = %v, want explicit false", cf.DetectGenerated)
	}
	if cf.MinifiedMaxWhitespaceRatio != 0.05 {
		t.Errorf("minified_max_whitespace_ratio = %v, want 0.05", cf.MinifiedMaxWhitespaceRatio)
	}

	// ApplyDefaults fills the unset pointer bools but preserves explicit
	// false values and explicit thresholds.
	ApplyDefaults(&cfg)
	if *cfg.VectorIndex.ContentFilter.Enabled {
		t.Error("ApplyDefaults must preserve explicit content_filter.enabled: false")
	}
	if *cfg.VectorIndex.ContentFilter.DetectGenerated {
		t.Error("ApplyDefaults must preserve explicit detect_generated: false")
	}
	if cfg.VectorIndex.ContentFilter.DetectMinified == nil || !*cfg.VectorIndex.ContentFilter.DetectMinified {
		t.Error("ApplyDefaults must default unset detect_minified to true")
	}
	if cfg.VectorIndex.ContentFilter.GeneratedHeaderBytes != 4096 {
		t.Errorf("ApplyDefaults clobbered explicit generated_header_bytes: %d", cfg.VectorIndex.ContentFilter.GeneratedHeaderBytes)
	}

	// The resolved policy reflects the explicit values.
	resolved := cfg.VectorIndex.ContentFilter.ResolveContentFilter()
	if resolved.Enabled || resolved.DetectGenerated {
		t.Errorf("resolved policy = %+v, want enabled=false detect_generated=false", resolved)
	}
	if resolved.GeneratedHeaderBytes != 4096 || resolved.MinifiedMaxLineBytes != 8192 {
		t.Errorf("resolved thresholds = %+v, want explicit values", resolved)
	}

	data, err := yaml.Marshal(&cfg)
	if err != nil {
		t.Fatalf("yaml.Marshal() failed: %v", err)
	}
	var restored Config
	if err := yaml.Unmarshal(data, &restored); err != nil {
		t.Fatalf("round-trip yaml.Unmarshal() failed: %v", err)
	}
	if restored.VectorIndex.ContentFilter.Enabled == nil || *restored.VectorIndex.ContentFilter.Enabled {
		t.Error("round-tripped content_filter.enabled must stay false")
	}
	if restored.VectorIndex.ContentFilter.MinifiedMaxWhitespaceRatio != 0.05 {
		t.Errorf("round-tripped minified_max_whitespace_ratio = %v, want 0.05", restored.VectorIndex.ContentFilter.MinifiedMaxWhitespaceRatio)
	}
}

// TestVectorIndexConfig_ContentFilter_Validation verifies that negative
// thresholds and out-of-range ratios fail fast at load time.
func TestVectorIndexConfig_ContentFilter_Validation(t *testing.T) {
	bad := []string{
		"vector_index:\n  content_filter:\n    generated_header_bytes: -1\n",
		"vector_index:\n  content_filter:\n    minified_min_bytes: -100\n",
		"vector_index:\n  content_filter:\n    minified_max_line_bytes: -5\n",
		"vector_index:\n  content_filter:\n    pathological_min_bytes: -1\n",
		"vector_index:\n  content_filter:\n    pathological_max_token_bytes: -1\n",
		"vector_index:\n  content_filter:\n    minified_max_whitespace_ratio: 1.5\n",
		"vector_index:\n  content_filter:\n    minified_max_whitespace_ratio: -0.1\n",
	}
	for _, src := range bad {
		var cfg Config
		if err := yaml.Unmarshal([]byte(src), &cfg); err != nil {
			t.Fatalf("yaml.Unmarshal() failed: %v", err)
		}
		if err := validate(&cfg); err == nil {
			t.Errorf("validate() must reject %q", strings.TrimSpace(src))
		}
	}
}

// TestGitConfig_Defaults verifies that an omitted git section resolves to
// auto_fetch=true / auto_fetch_interval="2m" after defaults are applied.
func TestGitConfig_Defaults(t *testing.T) {
	content := `
llm:
  default_model: claude-3-haiku
  anthropic:
    api_key: "test-key"
    models:
      - claude-3-haiku
`
	configPath := writeTestConfig(t, content)

	cfg, err := Load(configPath)
	if err != nil {
		t.Fatalf("Load() failed: %v", err)
	}
	if cfg.Git.AutoFetch == nil || !*cfg.Git.AutoFetch {
		t.Errorf("Expected default git.auto_fetch true, got %v", cfg.Git.AutoFetch)
	}
	if cfg.Git.AutoFetchInterval != "2m" {
		t.Errorf("Expected default git.auto_fetch_interval '2m', got %q", cfg.Git.AutoFetchInterval)
	}
}

// TestGitConfig_ExplicitValues verifies that explicit YAML values win over
// the defaults: auto_fetch: false survives (the pointer-bool default must not
// overwrite it) and a custom interval is preserved verbatim.
func TestGitConfig_ExplicitValues(t *testing.T) {
	content := `
llm:
  default_model: claude-3-haiku
  anthropic:
    api_key: "test-key"
    models:
      - claude-3-haiku
git:
  auto_fetch: false
  auto_fetch_interval: "5m"
`
	configPath := writeTestConfig(t, content)

	cfg, err := Load(configPath)
	if err != nil {
		t.Fatalf("Load() failed: %v", err)
	}
	if cfg.Git.AutoFetch == nil || *cfg.Git.AutoFetch {
		t.Errorf("Expected git.auto_fetch false, got %v", cfg.Git.AutoFetch)
	}
	if cfg.Git.AutoFetchInterval != "5m" {
		t.Errorf("Expected git.auto_fetch_interval '5m', got %q", cfg.Git.AutoFetchInterval)
	}
}

// TestGitConfig_ZeroIntervalPreserved verifies that an explicit "0" interval
// (the "ticker off, event-driven triggers stay on" sentinel) is NOT
// overwritten by the 2m default, and that setting it alone does not flip the
// auto_fetch master gate.
func TestGitConfig_ZeroIntervalPreserved(t *testing.T) {
	content := `
llm:
  default_model: claude-3-haiku
  anthropic:
    api_key: "test-key"
    models:
      - claude-3-haiku
git:
  auto_fetch_interval: "0"
`
	configPath := writeTestConfig(t, content)

	cfg, err := Load(configPath)
	if err != nil {
		t.Fatalf("Load() failed: %v", err)
	}
	if cfg.Git.AutoFetchInterval != "0" {
		t.Errorf("Expected git.auto_fetch_interval '0' to be preserved, got %q", cfg.Git.AutoFetchInterval)
	}
	if cfg.Git.AutoFetch == nil || !*cfg.Git.AutoFetch {
		t.Errorf("Expected default git.auto_fetch true, got %v", cfg.Git.AutoFetch)
	}
}

// TestGitCommitTimeout_DefaultsAndOverride pins the load semantics of
// timeouts.gitCommitTimeout: omitted (or explicit 0) resolves to the default
// 300 seconds, while a positive explicit value survives verbatim.
func TestGitCommitTimeout_DefaultsAndOverride(t *testing.T) {
	baseLLM := `
llm:
  default_model: claude-3-haiku
  anthropic:
    api_key: "test-key"
    models:
      - claude-3-haiku
`
	cases := []struct {
		name    string
		yamlKey string
		want    int
	}{
		{"omitted uses default", "", 300},
		{"explicit zero coerces to default", "timeouts:\n  gitCommitTimeout: 0\n", 300},
		{"explicit override wins", "timeouts:\n  gitCommitTimeout: 900\n", 900},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			configPath := writeTestConfig(t, baseLLM+tc.yamlKey)

			cfg, err := Load(configPath)
			if err != nil {
				t.Fatalf("Load() failed: %v", err)
			}
			if cfg.Timeouts.GitCommitTimeout != tc.want {
				t.Errorf("Expected gitCommitTimeout %d, got %d", tc.want, cfg.Timeouts.GitCommitTimeout)
			}
		})
	}
}

// TestRuntimeConfig_MemorySoftLimit_TriState pins the load semantics of
// runtime.memory_soft_limit_mb: unset decodes to the auto sentinel (0), an
// explicit positive value survives verbatim, and -1 (off) survives as the
// disable sentinel. Anything below -1 is a typo, not a sentinel, and must
// fail validation.
func TestRuntimeConfig_MemorySoftLimit_TriState(t *testing.T) {
	tests := []struct {
		name    string
		yaml    string
		want    int
		wantErr bool
	}{
		{
			name: "unset decodes to auto sentinel",
			yaml: `
llm:
  default_model: claude-3-haiku
  anthropic:
    api_key: "test-key"
    models:
      - claude-3-haiku
`,
			want: 0,
		},
		{
			name: "explicit zero is auto too",
			yaml: `
llm:
  default_model: claude-3-haiku
  anthropic:
    api_key: "test-key"
    models:
      - claude-3-haiku
runtime:
  memory_soft_limit_mb: 0
`,
			want: 0,
		},
		{
			name: "explicit MiB value",
			yaml: `
llm:
  default_model: claude-3-haiku
  anthropic:
    api_key: "test-key"
    models:
      - claude-3-haiku
runtime:
  memory_soft_limit_mb: 2048
`,
			want: 2048,
		},
		{
			name: "off sentinel",
			yaml: `
llm:
  default_model: claude-3-haiku
  anthropic:
    api_key: "test-key"
    models:
      - claude-3-haiku
runtime:
  memory_soft_limit_mb: -1
`,
			want: -1,
		},
		{
			name: "below -1 is rejected",
			yaml: `
llm:
  default_model: claude-3-haiku
  anthropic:
    api_key: "test-key"
    models:
      - claude-3-haiku
runtime:
  memory_soft_limit_mb: -10
`,
			wantErr: true,
		},
		{
			name: "above the 2 PiB ceiling is rejected",
			yaml: `
llm:
  default_model: claude-3-haiku
  anthropic:
    api_key: "test-key"
    models:
      - claude-3-haiku
runtime:
  memory_soft_limit_mb: 4294967296
`,
			wantErr: true,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			configPath := writeTestConfig(t, tt.yaml)

			cfg, err := Load(configPath)
			if tt.wantErr {
				if err == nil {
					t.Fatalf("Load() expected validation error, got nil (value %d)", cfg.Runtime.MemorySoftLimitMB)
				}
				if !strings.Contains(err.Error(), "runtime.memory_soft_limit_mb") {
					t.Errorf("error should name the offending key, got %q", err.Error())
				}
				return
			}
			if err != nil {
				t.Fatalf("Load() failed: %v", err)
			}
			if cfg.Runtime.MemorySoftLimitMB != tt.want {
				t.Errorf("runtime.memory_soft_limit_mb = %d, want %d", cfg.Runtime.MemorySoftLimitMB, tt.want)
			}
		})
	}
}

// TestRuntimeConfig_MemorySoftLimit_RoundTrip verifies the knob survives a
// Save→Load cycle for both the explicit-MiB and the off-sentinel values, so
// a config written by the app reloads with the operator's intent intact.
func TestRuntimeConfig_MemorySoftLimit_RoundTrip(t *testing.T) {
	for _, value := range []int{2048, -1} {
		cfg := &Config{}
		ApplyDefaults(cfg)
		cfg.LLM.DefaultModel = "claude-3-haiku"
		cfg.LLM.Anthropic.APIKey = "test-key"
		cfg.LLM.Anthropic.Models = []string{"claude-3-haiku"}
		cfg.Runtime.MemorySoftLimitMB = value

		path := filepath.Join(t.TempDir(), "config.yaml")
		if err := Save(cfg, path); err != nil {
			t.Fatalf("Save(value %d) failed: %v", value, err)
		}

		loaded, err := Load(path)
		if err != nil {
			t.Fatalf("Load(value %d) failed: %v", value, err)
		}
		if loaded.Runtime.MemorySoftLimitMB != value {
			t.Errorf("round-trip of %d got %d", value, loaded.Runtime.MemorySoftLimitMB)
		}
	}
}

// The per-provider TLS pin (ADR-054) must survive the whole config path:
// YAML load → canonical provider list → resolver. Fixed providers have no
// such key — they talk to vendor endpoints with public certificates.
func TestTLSFingerprint_LoadAndProviderList(t *testing.T) {
	const openaiPin = "k3J9vQ1Z0mF7hD2xS8pL4wR6tY5uI3oP1aE9cX0bN7g="
	const anthropicPin = "Zm9vYmFyYmF6cXV1eDEyMzQ1Njc4OWFiY2RlZmdoaT0="

	content := `
llm:
  default_model: qwen3
  openai_compatible:
    selfhosted:
      base_url: "https://llm.lan:8443/v1"
      api_key: "k"
      models:
        - qwen3
      tls_fingerprint: "` + openaiPin + `"
    plain:
      base_url: "http://127.0.0.1:1234/v1"
      api_key: "k"
      models:
        - llama
  anthropic_compatible:
    gateway:
      base_url: "https://claude.lan:8443"
      api_key: "k"
      models:
        - claude-sonnet-4-20250514
      tls_fingerprint: "` + anthropicPin + `"
`
	tmpDir := t.TempDir()
	path := filepath.Join(tmpDir, "config.yaml")
	if err := os.WriteFile(path, []byte(content), 0o600); err != nil {
		t.Fatalf("writing config: %v", err)
	}
	cfg, err := Load(path)
	if err != nil {
		t.Fatalf("Load: %v", err)
	}

	if got := cfg.LLM.OpenAICompatible["selfhosted"].TLSFingerprint; got != openaiPin {
		t.Errorf("openai_compatible pin = %q, want %q", got, openaiPin)
	}
	if got := cfg.LLM.AnthropicCompatible["gateway"].TLSFingerprint; got != anthropicPin {
		t.Errorf("anthropic_compatible pin = %q, want %q", got, anthropicPin)
	}

	pins := make(map[string]string)
	for _, p := range cfg.LLM.GetAllProviderConfigs() {
		pins[p.Name] = p.TLSFingerprint
	}
	want := map[string]string{
		"selfhosted": openaiPin,
		"gateway":    anthropicPin,
		"plain":      "",
		"anthropic":  "",
		"chatgpt":    "",
	}
	for name, wantPin := range want {
		got, ok := pins[name]
		if !ok {
			t.Errorf("provider %q missing from GetAllProviderConfigs", name)
			continue
		}
		if got != wantPin {
			t.Errorf("GetAllProviderConfigs[%q].TLSFingerprint = %q, want %q", name, got, wantPin)
		}
	}
}

// Both ResolveDefaultModelProvider branches — composite "provider/model" and
// a bare model name — must carry the pin. A branch that drops it would dial
// a pinned provider unpinned.
func TestResolveDefaultModelProvider_CarriesTLSFingerprint(t *testing.T) {
	const pin = "k3J9vQ1Z0mF7hD2xS8pL4wR6tY5uI3oP1aE9cX0bN7g="

	newCfg := func(defaultModel string) *LLMConfig {
		return &LLMConfig{
			DefaultModel: defaultModel,
			OpenAICompatible: map[string]OpenAICompatibleConfig{
				"selfhosted": {
					BaseURL:        "https://llm.lan:8443/v1",
					Models:         []string{"qwen3"},
					TLSFingerprint: pin,
				},
			},
		}
	}

	t.Run("composite identifier", func(t *testing.T) {
		p, model, err := newCfg("selfhosted/qwen3").ResolveDefaultModelProvider()
		if err != nil {
			t.Fatalf("ResolveDefaultModelProvider: %v", err)
		}
		if model != "qwen3" {
			t.Errorf("model = %q, want qwen3", model)
		}
		if p.TLSFingerprint != pin {
			t.Errorf("TLSFingerprint = %q, want %q", p.TLSFingerprint, pin)
		}
	})

	t.Run("bare identifier", func(t *testing.T) {
		p, model, err := newCfg("qwen3").ResolveDefaultModelProvider()
		if err != nil {
			t.Fatalf("ResolveDefaultModelProvider: %v", err)
		}
		if model != "qwen3" {
			t.Errorf("model = %q, want qwen3", model)
		}
		if p.TLSFingerprint != pin {
			t.Errorf("TLSFingerprint = %q, want %q", p.TLSFingerprint, pin)
		}
	})

	t.Run("provider without a pin resolves empty", func(t *testing.T) {
		cfg := &LLMConfig{
			DefaultModel: "llama",
			OpenAICompatible: map[string]OpenAICompatibleConfig{
				"plain": {BaseURL: "http://127.0.0.1:1234/v1", Models: []string{"llama"}},
			},
		}
		p, _, err := cfg.ResolveDefaultModelProvider()
		if err != nil {
			t.Fatalf("ResolveDefaultModelProvider: %v", err)
		}
		if p.TLSFingerprint != "" {
			t.Errorf("TLSFingerprint = %q, want empty", p.TLSFingerprint)
		}
	})
}

// A pin must round-trip through Save→Load unchanged, and a provider without
// a pin must not gain an empty tls_fingerprint key in the written YAML
// (the field is omitempty).
func TestTLSFingerprint_SaveRoundTripAndOmitEmpty(t *testing.T) {
	const pin = "k3J9vQ1Z0mF7hD2xS8pL4wR6tY5uI3oP1aE9cX0bN7g="

	cfg := &Config{}
	ApplyDefaults(cfg)
	cfg.LLM.DefaultModel = "qwen3"
	cfg.LLM.OpenAICompatible = map[string]OpenAICompatibleConfig{
		"selfhosted": {BaseURL: "https://llm.lan:8443/v1", APIKey: "k", Models: []string{"qwen3"}, TLSFingerprint: pin},
		"plain":      {BaseURL: "http://127.0.0.1:1234/v1", APIKey: "k", Models: []string{"llama"}},
	}
	cfg.LLM.AnthropicCompatible = map[string]AnthropicCompatibleConfig{
		"gateway": {BaseURL: "https://claude.lan:8443", APIKey: "k", Models: []string{"claude-sonnet-4-20250514"}, TLSFingerprint: pin},
	}

	path := filepath.Join(t.TempDir(), "config.yaml")
	if err := Save(cfg, path); err != nil {
		t.Fatalf("Save: %v", err)
	}

	raw, err := os.ReadFile(path)
	if err != nil {
		t.Fatalf("reading saved config: %v", err)
	}
	// Exactly two tls_fingerprint keys must appear: the two pinned
	// providers. A third would mean omitempty is not doing its job.
	if got := strings.Count(string(raw), "tls_fingerprint"); got != 2 {
		t.Errorf("saved YAML has %d tls_fingerprint keys, want 2 (omitempty must drop the unpinned provider)\n%s", got, raw)
	}

	loaded, err := Load(path)
	if err != nil {
		t.Fatalf("Load: %v", err)
	}
	if got := loaded.LLM.OpenAICompatible["selfhosted"].TLSFingerprint; got != pin {
		t.Errorf("round-tripped openai pin = %q, want %q", got, pin)
	}
	if got := loaded.LLM.AnthropicCompatible["gateway"].TLSFingerprint; got != pin {
		t.Errorf("round-tripped anthropic pin = %q, want %q", got, pin)
	}
	if got := loaded.LLM.OpenAICompatible["plain"].TLSFingerprint; got != "" {
		t.Errorf("unpinned provider round-tripped with pin %q, want empty", got)
	}
}

// TestNotificationBannerTimeoutDefault pins the pointer-int convention of the
// banner lifetime: an absent key must default to the daemon default (-1), and
// an explicit 0 — "never expire", the reason the setting exists — must
// survive ApplyDefaults instead of being mistaken for an unset field.
func TestNotificationBannerTimeoutDefault(t *testing.T) {
	var cfg Config
	ApplyDefaults(&cfg)
	if cfg.Notifications.BannerTimeoutSeconds == nil {
		t.Fatal("ApplyDefaults left banner_timeout_seconds nil")
	}
	if got := *cfg.Notifications.BannerTimeoutSeconds; got != NotificationBannerTimeoutDaemonDefault {
		t.Errorf("default banner timeout = %d, want %d", got, NotificationBannerTimeoutDaemonDefault)
	}

	never := NotificationBannerTimeoutNever
	explicit := Config{Notifications: NotificationsConfig{BannerTimeoutSeconds: &never}}
	ApplyDefaults(&explicit)
	if got := *explicit.Notifications.BannerTimeoutSeconds; got != NotificationBannerTimeoutNever {
		t.Errorf("explicit 0 was overwritten with %d", got)
	}
}

// TestAutoRetrySeconds_ParsesAndSurfacesInProviderList pins the compatible-only
// schema of the per-provider automatic retry timer: llm.openai_compatible.<name>
// .auto_retry_seconds and llm.anthropic_compatible.<name>.auto_retry_seconds
// parse from YAML, absent keys read as 0, and GetAllProviderConfigs surfaces
// the value for compatible providers while the fixed providers (anthropic,
// chatgpt — which have no such knob in their schema) always report 0.
func TestAutoRetrySeconds_ParsesAndSurfacesInProviderList(t *testing.T) {
	content := `llm:
  default_model: "qwen3"
  anthropic:
    api_key: "k"
    models:
      - "claude-sonnet-4-20250514"
  openai_compatible:
    selfhosted:
      base_url: "https://llm.lan:8443/v1"
      api_key: "k"
      models:
        - "qwen3"
      auto_retry_seconds: 30
    plain:
      base_url: "http://127.0.0.1:1234/v1"
      api_key: "k"
      models:
        - "llama"
  anthropic_compatible:
    gateway:
      base_url: "https://claude.lan:8443"
      api_key: "k"
      models:
        - "claude-sonnet-4-20250514"
      auto_retry_seconds: 30
    plain-gw:
      base_url: "http://127.0.0.2:8443"
      api_key: "k"
      models:
        - "claude-haiku"
  chatgpt:
    api_key: "k"
    models:
      - "gpt-4o"
`
	path := filepath.Join(t.TempDir(), "config.yaml")
	if err := os.WriteFile(path, []byte(content), 0o600); err != nil {
		t.Fatalf("writing config: %v", err)
	}
	cfg, err := Load(path)
	if err != nil {
		t.Fatalf("Load: %v", err)
	}

	if got := cfg.LLM.OpenAICompatible["selfhosted"].AutoRetrySeconds; got != 30 {
		t.Errorf("openai_compatible auto_retry_seconds = %d, want 30", got)
	}
	if got := cfg.LLM.OpenAICompatible["plain"].AutoRetrySeconds; got != 0 {
		t.Errorf("absent openai_compatible auto_retry_seconds = %d, want 0", got)
	}
	if got := cfg.LLM.AnthropicCompatible["gateway"].AutoRetrySeconds; got != 30 {
		t.Errorf("anthropic_compatible auto_retry_seconds = %d, want 30", got)
	}
	if got := cfg.LLM.AnthropicCompatible["plain-gw"].AutoRetrySeconds; got != 0 {
		t.Errorf("absent anthropic_compatible auto_retry_seconds = %d, want 0", got)
	}

	retry := make(map[string]int)
	for _, p := range cfg.LLM.GetAllProviderConfigs() {
		retry[p.Name] = p.AutoRetrySeconds
	}
	want := map[string]int{
		"selfhosted": 30,
		"gateway":    30,
		"plain":      0,
		"plain-gw":   0,
		"anthropic":  0, // fixed providers: no knob, always 0
		"chatgpt":    0, // fixed providers: no knob, always 0
	}
	for name, wantSeconds := range want {
		got, ok := retry[name]
		if !ok {
			t.Errorf("provider %q missing from GetAllProviderConfigs", name)
			continue
		}
		if got != wantSeconds {
			t.Errorf("GetAllProviderConfigs[%q].AutoRetrySeconds = %d, want %d", name, got, wantSeconds)
		}
	}
}

// The timer value must round-trip through Save→Load unchanged, and providers
// without it must not gain an auto_retry_seconds key in the written YAML
// (the field is omitempty).
func TestAutoRetrySeconds_SaveRoundTripAndOmitEmpty(t *testing.T) {
	cfg := &Config{}
	ApplyDefaults(cfg)
	cfg.LLM.DefaultModel = "qwen3"
	cfg.LLM.OpenAICompatible = map[string]OpenAICompatibleConfig{
		"selfhosted": {BaseURL: "https://llm.lan:8443/v1", APIKey: "k", Models: []string{"qwen3"}, AutoRetrySeconds: 30},
		"plain":      {BaseURL: "http://127.0.0.1:1234/v1", APIKey: "k", Models: []string{"llama"}},
	}
	cfg.LLM.AnthropicCompatible = map[string]AnthropicCompatibleConfig{
		"gateway": {BaseURL: "https://claude.lan:8443", APIKey: "k", Models: []string{"claude-sonnet-4-20250514"}, AutoRetrySeconds: 45},
	}

	path := filepath.Join(t.TempDir(), "config.yaml")
	if err := Save(cfg, path); err != nil {
		t.Fatalf("Save: %v", err)
	}

	raw, err := os.ReadFile(path)
	if err != nil {
		t.Fatalf("reading saved config: %v", err)
	}
	// Exactly two auto_retry_seconds keys: the two timer-enabled providers.
	// A third would mean omitempty is not doing its job.
	if got := strings.Count(string(raw), "auto_retry_seconds"); got != 2 {
		t.Errorf("saved YAML has %d auto_retry_seconds keys, want 2 (omitempty must drop disabled providers)\n%s", got, raw)
	}

	loaded, err := Load(path)
	if err != nil {
		t.Fatalf("Load: %v", err)
	}
	if got := loaded.LLM.OpenAICompatible["selfhosted"].AutoRetrySeconds; got != 30 {
		t.Errorf("round-tripped openai auto_retry_seconds = %d, want 30", got)
	}
	if got := loaded.LLM.AnthropicCompatible["gateway"].AutoRetrySeconds; got != 45 {
		t.Errorf("round-tripped anthropic auto_retry_seconds = %d, want 45", got)
	}
	if got := loaded.LLM.OpenAICompatible["plain"].AutoRetrySeconds; got != 0 {
		t.Errorf("disabled provider round-tripped with auto_retry_seconds %d, want 0", got)
	}
}

// TestAutoRetrySeconds_RejectsOutOfRange pins the [0, 3600] bound (ADR-065):
// Load rejects negative and oversized values for both compatible provider
// kinds — the timer must never arm on an unusable window while the banner
// still promises an auto-resend.
func TestAutoRetrySeconds_RejectsOutOfRange(t *testing.T) {
	tests := []struct {
		name    string
		yamlVal int
	}{
		{"negative", -30},
		{"oversized", 3601},
		{"absurdly large", 1_000_000_000},
	}
	for _, tt := range tests {
		t.Run("openai_compatible "+tt.name, func(t *testing.T) {
			content := fmt.Sprintf(`llm:
  default_model: "qwen3"
  openai_compatible:
    selfhosted:
      base_url: "http://127.0.0.1:1234/v1"
      api_key: "k"
      auto_retry_seconds: %d
      models: ["qwen3"]
`, tt.yamlVal)
			path := filepath.Join(t.TempDir(), "config.yaml")
			if err := os.WriteFile(path, []byte(content), 0o600); err != nil {
				t.Fatalf("write: %v", err)
			}
			if _, err := Load(path); err == nil {
				t.Errorf("Load accepted auto_retry_seconds %d, want rejection", tt.yamlVal)
			}
		})
		t.Run("anthropic_compatible "+tt.name, func(t *testing.T) {
			content := fmt.Sprintf(`llm:
  default_model: "claude-sonnet-4-20250514"
  anthropic_compatible:
    gateway:
      base_url: "https://claude.lan:8443"
      api_key: "k"
      auto_retry_seconds: %d
      models: ["claude-sonnet-4-20250514"]
`, tt.yamlVal)
			path := filepath.Join(t.TempDir(), "config.yaml")
			if err := os.WriteFile(path, []byte(content), 0o600); err != nil {
				t.Fatalf("write: %v", err)
			}
			if _, err := Load(path); err == nil {
				t.Errorf("Load accepted auto_retry_seconds %d, want rejection", tt.yamlVal)
			}
		})
	}
	// The boundary itself must stay valid.
	content := `llm:
  default_model: "qwen3"
  openai_compatible:
    selfhosted:
      base_url: "http://127.0.0.1:1234/v1"
      api_key: "k"
      auto_retry_seconds: 3600
      models: ["qwen3"]
`
	path := filepath.Join(t.TempDir(), "config.yaml")
	if err := os.WriteFile(path, []byte(content), 0o600); err != nil {
		t.Fatalf("write: %v", err)
	}
	if _, err := Load(path); err != nil {
		t.Errorf("Load rejected boundary auto_retry_seconds 3600: %v", err)
	}
}
