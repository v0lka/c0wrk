package config

import (
	"os"
	"path/filepath"
	"testing"

	"gopkg.in/yaml.v3"
)

// TestLoadMinimalConfig tests loading a minimal YAML config with active_provider and provider config.
func TestLoadMinimalConfig(t *testing.T) {
	content := `
llm:
  active_provider: anthropic
  anthropic:
    api_key: "test-key"
    model: claude-3-haiku
`
	configPath := writeTestConfig(t, content)

	cfg, err := Load(configPath)
	if err != nil {
		t.Fatalf("Load() failed: %v", err)
	}

	// Verify active provider
	if cfg.LLM.ActiveProvider != "anthropic" {
		t.Errorf("Expected active_provider 'anthropic', got %q", cfg.LLM.ActiveProvider)
	}

	// Verify anthropic config
	if cfg.LLM.Anthropic.APIKey != "test-key" {
		t.Errorf("Expected api_key 'test-key', got %q", cfg.LLM.Anthropic.APIKey)
	}
	if cfg.LLM.Anthropic.Model != "claude-3-haiku" {
		t.Errorf("Expected model 'claude-3-haiku', got %q", cfg.LLM.Anthropic.Model)
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
  active_provider: anthropic
  anthropic:
    api_key: "${TEST_API_KEY}"
    model: claude-3-haiku
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

// TestInvalidProviderError tests that an invalid active_provider returns an error.
func TestInvalidProviderError(t *testing.T) {
	content := `
llm:
  active_provider: invalid_provider
  anthropic:
    api_key: "test-key"
    model: claude-3-haiku
`
	configPath := writeTestConfig(t, content)

	_, err := Load(configPath)
	if err == nil {
		t.Fatal("Expected error when active_provider is invalid, got nil")
	}

	// Verify error message mentions the issue
	expectedSubstring := "not a valid provider"
	if !contains(err.Error(), expectedSubstring) {
		t.Errorf("Expected error to contain %q, got: %v", expectedSubstring, err)
	}
}

// TestMissingModelError tests that missing model on active provider returns an error.
func TestMissingModelError(t *testing.T) {
	content := `
llm:
  active_provider: anthropic
  anthropic:
    api_key: "test-key"
    # No model specified
`
	configPath := writeTestConfig(t, content)

	_, err := Load(configPath)
	if err == nil {
		t.Fatal("Expected error when model is missing, got nil")
	}

	// Verify error message mentions the issue
	expectedSubstring := "must have a model specified"
	if !contains(err.Error(), expectedSubstring) {
		t.Errorf("Expected error to contain %q, got: %v", expectedSubstring, err)
	}
}

// TestDefaultsApplied tests that defaults are applied for missing fields.
func TestDefaultsApplied(t *testing.T) {
	content := `
llm:
  active_provider: anthropic
  anthropic:
    api_key: "test-key"
    model: claude-3-haiku
`
	configPath := writeTestConfig(t, content)

	cfg, err := Load(configPath)
	if err != nil {
		t.Fatalf("Load() failed: %v", err)
	}

	// Check Executor defaults
	if cfg.Executor.MaxReactSteps != 30 {
		t.Errorf("Expected default max_react_steps 30, got %d", cfg.Executor.MaxReactSteps)
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
	if cfg.Executor.Compaction.Hierarchical.EnabledAboveSteps != 40 {
		t.Errorf("Expected default enabled_above_steps 40, got %d", cfg.Executor.Compaction.Hierarchical.EnabledAboveSteps)
	}

	// Check Router defaults
	if cfg.Router.HistoryWindow != 10 {
		t.Errorf("Expected default history_window 10, got %d", cfg.Router.HistoryWindow)
	}

	// Check Security defaults
	if cfg.Security.Judge.Enabled == nil || !*cfg.Security.Judge.Enabled {
		t.Error("Expected default judge enabled to be true")
	}
	if cfg.Security.DefaultPolicy != "auto" {
		t.Errorf("Expected default policy 'auto', got %q", cfg.Security.DefaultPolicy)
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
	if fileOpsPolicy, ok := cfg.Security.ToolPolicies["file_ops"]; !ok {
		t.Error("Expected default file_ops policy")
	} else if fileOpsPolicy.Policy != "auto" {
		t.Errorf("Expected file_ops policy 'auto', got %q", fileOpsPolicy.Policy)
	}

	// Check LMStudio default base URL
	if cfg.LLM.LMStudio.BaseURL != "http://localhost:1234" {
		t.Errorf("Expected default lmstudio base_url 'http://localhost:1234', got %q", cfg.LLM.LMStudio.BaseURL)
	}

	// Check Models map is initialized
	if cfg.LLM.Models == nil {
		t.Error("Expected Models map to be initialized")
	}
}

// TestOpenAICompatibleRequiresBaseURL tests that openai_compatible provider requires base_url.
func TestOpenAICompatibleRequiresBaseURL(t *testing.T) {
	content := `
llm:
  active_provider: openai_compatible
  openai_compatible:
    api_key: "test-key"
    model: deepseek-chat
    # No base_url specified
`
	configPath := writeTestConfig(t, content)

	_, err := Load(configPath)
	if err == nil {
		t.Fatal("Expected error when openai_compatible has no base_url, got nil")
	}

	// Verify error message mentions the issue
	expectedSubstring := "requires base_url"
	if !contains(err.Error(), expectedSubstring) {
		t.Errorf("Expected error to contain %q, got: %v", expectedSubstring, err)
	}
}

// TestLoadWithResult_NoErrors tests that clean load returns no errors.
func TestLoadWithResult_NoErrors(t *testing.T) {
	content := `
llm:
  active_provider: anthropic
  anthropic:
    api_key: "test-key"
    model: claude-3-haiku
`
	configPath := writeTestConfig(t, content)

	result, err := LoadWithResult(configPath)
	if err != nil {
		t.Fatalf("LoadWithResult() failed: %v", err)
	}

	// Verify no migration happened
	if result.Migrated {
		t.Error("Expected Migrated to be false")
	}
	if result.MigrationMsg != "" {
		t.Errorf("Expected MigrationMsg to be empty, got %q", result.MigrationMsg)
	}
	if len(result.LoadErrors) != 0 {
		t.Errorf("Expected no load errors, got %v", result.LoadErrors)
	}
}

// TestGetActiveProviderConfig tests the GetActiveProviderConfig method for all providers.
func TestGetActiveProviderConfig(t *testing.T) {
	tests := []struct {
		name         string
		config       LLMConfig
		wantProvType string
		wantAPIKey   string
		wantBaseURL  string
		wantModel    string
	}{
		{
			name: "anthropic",
			config: LLMConfig{
				ActiveProvider: "anthropic",
				Anthropic: AnthropicConfig{
					APIKey: "anthropic-key",
					Model:  "claude-3-haiku",
				},
			},
			wantProvType: "anthropic",
			wantAPIKey:   "anthropic-key",
			wantBaseURL:  "",
			wantModel:    "claude-3-haiku",
		},
		{
			name: "gemini",
			config: LLMConfig{
				ActiveProvider: "gemini",
				Gemini: GeminiConfig{
					APIKey: "gemini-key",
					Model:  "gemini-pro",
				},
			},
			wantProvType: "gemini",
			wantAPIKey:   "gemini-key",
			wantBaseURL:  "",
			wantModel:    "gemini-pro",
		},
		{
			name: "lmstudio",
			config: LLMConfig{
				ActiveProvider: "lmstudio",
				LMStudio: LMStudioConfig{
					BaseURL: "http://localhost:1234",
					APIKey:  "",
					Model:   "local-model",
				},
			},
			wantProvType: "lmstudio",
			wantAPIKey:   "",
			wantBaseURL:  "http://localhost:1234",
			wantModel:    "local-model",
		},
		{
			name: "openai_compatible",
			config: LLMConfig{
				ActiveProvider: "openai_compatible",
				OpenAICompatible: OpenAICompatibleConfig{
					BaseURL: "https://api.deepseek.com",
					APIKey:  "deepseek-key",
					Model:   "deepseek-chat",
				},
			},
			wantProvType: "openai",
			wantAPIKey:   "deepseek-key",
			wantBaseURL:  "https://api.deepseek.com",
			wantModel:    "deepseek-chat",
		},
		{
			name: "chatgpt",
			config: LLMConfig{
				ActiveProvider: "chatgpt",
				ChatGPT: ChatGPTConfig{
					APIKey: "openai-key",
					Model:  "gpt-4o",
				},
			},
			wantProvType: "openai",
			wantAPIKey:   "openai-key",
			wantBaseURL:  "",
			wantModel:    "gpt-4o",
		},
		{
			name:         "unknown_provider",
			config:       LLMConfig{ActiveProvider: "unknown"},
			wantProvType: "",
			wantAPIKey:   "",
			wantBaseURL:  "",
			wantModel:    "",
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			gotProvType, gotAPIKey, gotBaseURL, gotModel := tt.config.GetActiveProviderConfig()
			if gotProvType != tt.wantProvType {
				t.Errorf("GetActiveProviderConfig() providerType = %q, want %q", gotProvType, tt.wantProvType)
			}
			if gotAPIKey != tt.wantAPIKey {
				t.Errorf("GetActiveProviderConfig() apiKey = %q, want %q", gotAPIKey, tt.wantAPIKey)
			}
			if gotBaseURL != tt.wantBaseURL {
				t.Errorf("GetActiveProviderConfig() baseURL = %q, want %q", gotBaseURL, tt.wantBaseURL)
			}
			if gotModel != tt.wantModel {
				t.Errorf("GetActiveProviderConfig() model = %q, want %q", gotModel, tt.wantModel)
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
	cfg.LLM.ActiveProvider = "anthropic"
	cfg.LLM.Anthropic.APIKey = "test-key-123"
	cfg.LLM.Anthropic.Model = "claude-3-5-sonnet"

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

	if loaded.LLM.ActiveProvider != "anthropic" {
		t.Errorf("ActiveProvider = %q, want 'anthropic'", loaded.LLM.ActiveProvider)
	}
	if loaded.LLM.Anthropic.APIKey != "test-key-123" {
		t.Errorf("APIKey = %q, want 'test-key-123'", loaded.LLM.Anthropic.APIKey)
	}
}

func TestSave_AtomicWrite(t *testing.T) {
	cfg := &Config{}
	ApplyDefaults(cfg)
	cfg.LLM.ActiveProvider = "anthropic"
	cfg.LLM.Anthropic.APIKey = "key"
	cfg.LLM.Anthropic.Model = "model"

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
	cfg.LLM.ActiveProvider = "anthropic"

	err := Save(cfg, "/nonexistent/deeply/nested/dir/config.yaml")
	if err == nil {
		t.Error("expected error for invalid path")
	}
}

func TestSave_PreservesEnvVarReferences(t *testing.T) {
	t.Setenv("MY_SECRET_KEY", "actual-secret-value")

	content := `
llm:
  active_provider: anthropic
  anthropic:
    api_key: "${MY_SECRET_KEY}"
    model: claude-3-haiku
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
			name: "backward compat - no transport field defaults to empty",
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

// TestMCPServerConfig_BackwardCompat tests that configs without transport field still work.
func TestMCPServerConfig_BackwardCompat(t *testing.T) {
	// This simulates loading an existing config file that doesn't have the transport field
	content := `
llm:
  active_provider: anthropic
  anthropic:
    api_key: "test-key"
    model: claude-3-haiku
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
