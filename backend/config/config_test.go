package config

import (
	"fmt"
	"io"
	"log/slog"
	"os"
	"path/filepath"
	"testing"

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
	if cfg.Executor.MaxReactSteps != 50 {
		t.Errorf("Expected default max_react_steps 50, got %d", cfg.Executor.MaxReactSteps)
	}
	if cfg.Executor.MaxRetries != 2 {
		t.Errorf("Expected default max_retries 2, got %d", cfg.Executor.MaxRetries)
	}
	if cfg.Executor.OutputTokenReserve != 4096 {
		t.Errorf("Expected default output_token_reserve 4096, got %d", cfg.Executor.OutputTokenReserve)
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

	// Check Security defaults
	if cfg.Security.DefaultPolicy != "user_confirm" {
		t.Errorf("Expected default policy 'user_confirm', got %q", cfg.Security.DefaultPolicy)
	}
	if cfg.Security.ToolPolicies == nil {
		t.Error("Expected ToolPolicies to be initialized")
	}
	// Check default tool policies
	if bashPolicy, ok := cfg.Security.ToolPolicies["bash_exec"]; !ok {
		t.Error("Expected default bash_exec policy")
	} else if bashPolicy.Policy != "user_confirm" {
		t.Errorf("Expected bash_exec policy 'user_confirm', got %q", bashPolicy.Policy)
	}
	if writeFilePolicy, ok := cfg.Security.ToolPolicies["write_file"]; !ok {
		t.Error("Expected default write_file policy")
	} else if writeFilePolicy.Policy != "user_confirm" {
		t.Errorf("Expected write_file policy 'user_confirm', got %q", writeFilePolicy.Policy)
	}
	if editFilePolicy, ok := cfg.Security.ToolPolicies["edit_file"]; !ok {
		t.Error("Expected default edit_file policy")
	} else if editFilePolicy.Policy != "user_confirm" {
		t.Errorf("Expected edit_file policy 'user_confirm', got %q", editFilePolicy.Policy)
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

	// Check Models map is initialized
	if cfg.LLM.Models == nil {
		t.Error("Expected Models map to be initialized")
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

// TestGetAllProviderConfigs tests multi-provider config resolution.
func TestGetAllProviderConfigs(t *testing.T) {
	cfg := LLMConfig{
		DefaultModel: "claude-3-haiku",
		Anthropic: AnthropicConfig{
			APIKey: "anthropic-key",
			Models: []string{"claude-3-haiku", "claude-opus"},
		},
		OpenAICompatible: map[string]OpenAICompatibleConfig{
			"deepseek": {
				APIKey:  "deepseek-key",
				BaseURL: "https://api.deepseek.com",
				Models:  []string{"deepseek-chat"},
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

// TestConfigValidation_RejectsInternalToolPolicies tests that config validation
// rejects a config where an internal tool name appears in Security.ToolPolicies.
func TestConfigValidation_RejectsInternalToolPolicies(t *testing.T) {
	internalTools := []string{"ask_user", "finish", "list_step_outputs", "read_skill_resource", "read_step_output", "search_facts", "semantic_search", "update_checklist", "declare_step_complete", "store_fact", "tool_result_read"}

	for _, toolName := range internalTools {
		t.Run(toolName, func(t *testing.T) {
			content := fmt.Sprintf(`
llm:
  default_model: claude-3-haiku
  anthropic:
    api_key: "test-key"
    models:
      - claude-3-haiku
security:
  tool_policies:
    %s:
      policy: always_allow
`, toolName)
			configPath := writeTestConfig(t, content)

			_, err := Load(configPath)
			if err == nil {
				t.Fatalf("expected error when internal tool %q is in tool_policies, got nil", toolName)
			}

			expectedSubstring := "internal tool"
			if !contains(err.Error(), expectedSubstring) {
				t.Errorf("expected error to contain %q, got: %v", expectedSubstring, err)
			}
		})
	}
}

// TestConfigValidation_AcceptsNonInternalToolPolicies tests that config validation
// accepts a config where a non-internal tool appears in Security.ToolPolicies.
func TestConfigValidation_AcceptsNonInternalToolPolicies(t *testing.T) {
	nonInternalTools := []string{"bash_exec", "file_write", "file_read"}

	for _, toolName := range nonInternalTools {
		t.Run(toolName, func(t *testing.T) {
			content := fmt.Sprintf(`
llm:
  default_model: claude-3-haiku
  anthropic:
    api_key: "test-key"
    models:
      - claude-3-haiku
security:
  tool_policies:
    %s:
      policy: always_allow
`, toolName)
			configPath := writeTestConfig(t, content)

			cfg, err := Load(configPath)
			if err != nil {
				t.Fatalf("unexpected error for non-internal tool %q: %v", toolName, err)
			}

			// Verify the policy was loaded correctly
			if cfg.Security.ToolPolicies == nil {
				t.Fatalf("expected ToolPolicies to be initialized")
			}
			policy, ok := cfg.Security.ToolPolicies[toolName]
			if !ok {
				t.Errorf("expected policy for tool %q to be loaded", toolName)
			}
			if policy.Policy != "always_allow" {
				t.Errorf("expected policy 'always_allow' for tool %q, got %q", toolName, policy.Policy)
			}
		})
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
	if cfg.Executor.MaxReactSteps != 50 {
		t.Errorf("MaxReactSteps = %d, want 50", cfg.Executor.MaxReactSteps)
	}
	if cfg.LogLevel != "DEBUG" {
		t.Errorf("LogLevel = %q, want 'DEBUG'", cfg.LogLevel)
	}
	if cfg.Security.DefaultPolicy != "user_confirm" {
		t.Errorf("DefaultPolicy = %q, want 'user_confirm'", cfg.Security.DefaultPolicy)
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
	if loaded.Executor.MaxReactSteps != 50 {
		t.Errorf("round-tripped MaxReactSteps = %d, want 50", loaded.Executor.MaxReactSteps)
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
	tmpHome := t.TempDir()
	t.Setenv("HOME", tmpHome)

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
	if resolved.Config.Executor.MaxReactSteps != 50 {
		t.Errorf("MaxReactSteps = %d, want 50", resolved.Config.Executor.MaxReactSteps)
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
