package config

import (
	"os"
	"path/filepath"
	"testing"
)

// TestLoadMinimalConfig tests loading a minimal YAML config with one provider and one role.
func TestLoadMinimalConfig(t *testing.T) {
	content := `
llm:
  providers:
    anthropic:
      type: anthropic
      api_key: "test-key"
  roles:
    router:
      provider: anthropic
      model: claude-3-haiku
`
	configPath := writeTestConfig(t, content)

	cfg, err := Load(configPath)
	if err != nil {
		t.Fatalf("Load() failed: %v", err)
	}

	// Verify provider was loaded
	if len(cfg.LLM.Providers) != 1 {
		t.Errorf("Expected 1 provider, got %d", len(cfg.LLM.Providers))
	}

	provider, ok := cfg.LLM.Providers["anthropic"]
	if !ok {
		t.Fatal("Expected 'anthropic' provider to exist")
	}
	if provider.Type != "anthropic" {
		t.Errorf("Expected provider type 'anthropic', got %q", provider.Type)
	}
	if provider.APIKey != "test-key" {
		t.Errorf("Expected api_key 'test-key', got %q", provider.APIKey)
	}

	// Verify role was loaded
	role, ok := cfg.LLM.Roles["router"]
	if !ok {
		t.Fatal("Expected 'router' role to exist")
	}
	if role.Provider != "anthropic" {
		t.Errorf("Expected role provider 'anthropic', got %q", role.Provider)
	}
	if role.Model != "claude-3-haiku" {
		t.Errorf("Expected role model 'claude-3-haiku', got %q", role.Model)
	}
}

// TestEnvVarSubstitution tests that ${ENV_VAR} patterns are replaced with env values.
func TestEnvVarSubstitution(t *testing.T) {
	// Set test environment variable
	testAPIKey := "secret-api-key-12345"
	t.Setenv("TEST_API_KEY", testAPIKey)

	content := `
llm:
  providers:
    openai:
      type: openai
      api_key: "${TEST_API_KEY}"
  roles:
    executor:
      provider: openai
      model: gpt-4
`
	configPath := writeTestConfig(t, content)

	cfg, err := Load(configPath)
	if err != nil {
		t.Fatalf("Load() failed: %v", err)
	}

	provider, ok := cfg.LLM.Providers["openai"]
	if !ok {
		t.Fatal("Expected 'openai' provider to exist")
	}
	if provider.APIKey != testAPIKey {
		t.Errorf("Expected api_key %q after env substitution, got %q", testAPIKey, provider.APIKey)
	}
}

// TestNonExistentProviderError tests that referencing a non-existent provider returns an error.
func TestNonExistentProviderError(t *testing.T) {
	content := `
llm:
  providers:
    anthropic:
      type: anthropic
      api_key: "test-key"
  roles:
    router:
      provider: nonexistent
      model: some-model
`
	configPath := writeTestConfig(t, content)

	_, err := Load(configPath)
	if err == nil {
		t.Fatal("Expected error when role references non-existent provider, got nil")
	}

	// Verify error message mentions the issue
	expectedSubstring := "non-existent provider"
	if !contains(err.Error(), expectedSubstring) {
		t.Errorf("Expected error to contain %q, got: %v", expectedSubstring, err)
	}
}

// TestDefaultsApplied tests that defaults are applied for missing fields.
func TestDefaultsApplied(t *testing.T) {
	content := `
llm:
  providers:
    anthropic:
      type: anthropic
      api_key: "test-key"
  roles:
    router:
      provider: anthropic
      model: claude-3-haiku
`
	configPath := writeTestConfig(t, content)

	cfg, err := Load(configPath)
	if err != nil {
		t.Fatalf("Load() failed: %v", err)
	}

	// Check LLM defaults
	if cfg.LLM.Defaults.MaxTokens != 4096 {
		t.Errorf("Expected default max_tokens 4096, got %d", cfg.LLM.Defaults.MaxTokens)
	}

	// Check Executor defaults
	if cfg.Executor.MaxReactSteps != 30 {
		t.Errorf("Expected default max_react_steps 30, got %d", cfg.Executor.MaxReactSteps)
	}
	if cfg.Executor.MaxRetries != 3 {
		t.Errorf("Expected default max_retries 3, got %d", cfg.Executor.MaxRetries)
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
}

// TestNoProviderError tests that an error is returned when no providers are defined.
func TestNoProviderError(t *testing.T) {
	content := `
llm:
  providers: {}
  roles:
    router:
      provider: anthropic
      model: claude-3-haiku
`
	configPath := writeTestConfig(t, content)

	_, err := Load(configPath)
	if err == nil {
		t.Fatal("Expected error when no providers defined, got nil")
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

// contains checks if substr is in s.
func contains(s, substr string) bool {
	return len(s) >= len(substr) && (s == substr || len(substr) == 0 ||
		(len(s) > 0 && len(substr) > 0 && findSubstring(s, substr)))
}

func findSubstring(s, substr string) bool {
	for i := 0; i <= len(s)-len(substr); i++ {
		if s[i:i+len(substr)] == substr {
			return true
		}
	}
	return false
}
