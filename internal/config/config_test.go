package config

import (
	"os"
	"path/filepath"
	"testing"
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

// TestEnvVarSubstitution tests that ${ENV_VAR} patterns are replaced with env values.
func TestEnvVarSubstitution(t *testing.T) {
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

	if cfg.LLM.Anthropic.APIKey != testAPIKey {
		t.Errorf("Expected api_key %q after env substitution, got %q", testAPIKey, cfg.LLM.Anthropic.APIKey)
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
	if cfg.Executor.MaxRetries != 1 {
		t.Errorf("Expected default max_retries 1, got %d", cfg.Executor.MaxRetries)
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

	// Check Memory defaults
	if cfg.Memory.Episodic.RetentionDays != 90 {
		t.Errorf("Expected default retention_days 90, got %d", cfg.Memory.Episodic.RetentionDays)
	}
	if cfg.Memory.Episodic.RetrievalLimit != 5 {
		t.Errorf("Expected default retrieval_limit 5, got %d", cfg.Memory.Episodic.RetrievalLimit)
	}
	if cfg.Memory.Constitution.UpdateIntervalSessions != 10 {
		t.Errorf("Expected default update_interval_sessions 10, got %d", cfg.Memory.Constitution.UpdateIntervalSessions)
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

	// Check Docker/Skills defaults
	if cfg.Skills.Docker.WarmPoolThreshold != 5 {
		t.Errorf("Expected default warm_pool_threshold 5, got %d", cfg.Skills.Docker.WarmPoolThreshold)
	}
	if cfg.Skills.Docker.WarmPoolIdleTimeout != "60s" {
		t.Errorf("Expected default warm_pool_idle_timeout '60s', got %q", cfg.Skills.Docker.WarmPoolIdleTimeout)
	}
	if cfg.Skills.Docker.DefaultMemory != "256m" {
		t.Errorf("Expected default default_memory '256m', got %q", cfg.Skills.Docker.DefaultMemory)
	}
	if cfg.Skills.Docker.DefaultCPU != "0.5" {
		t.Errorf("Expected default default_cpu '0.5', got %q", cfg.Skills.Docker.DefaultCPU)
	}
	if cfg.Skills.Docker.DefaultTimeout != "30s" {
		t.Errorf("Expected default default_timeout '30s', got %q", cfg.Skills.Docker.DefaultTimeout)
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
