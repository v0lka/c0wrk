// Package config provides configuration loading and validation for the agent.
package config

import (
	"fmt"
	"os"
	"regexp"

	"gopkg.in/yaml.v3"
)

// DefaultAgentDir is the default directory for agent files (data, skills, config).
const DefaultAgentDir = ".c0wrk"

// Config is the top-level configuration structure.
type Config struct {
	LogLevel string         `yaml:"log_level"`
	Theme    string         `yaml:"theme"`
	LLM      LLMConfig      `yaml:"llm"`
	MCP      MCPConfig      `yaml:"mcp"`
	Skills   SkillsConfig   `yaml:"skills"`
	Memory   MemoryConfig   `yaml:"memory"`
	Router   RouterConfig   `yaml:"router"`
	Executor ExecutorConfig `yaml:"executor"`
	Security SecurityConfig `yaml:"security"`
	Search   SearchConfig   `yaml:"search"`
}

// LLMConfig holds LLM provider and role configuration.
type LLMConfig struct {
	Providers map[string]ProviderConfig `yaml:"providers"`
	Roles     map[string]RoleConfig     `yaml:"roles"`
	Defaults  LLMDefaults               `yaml:"defaults"`
	Models    map[string]ModelOverride  `yaml:"models"`
}

// ModelOverride allows overriding built-in model metadata.
type ModelOverride struct {
	ContextWindow int `yaml:"context_window"`
	OutputLimit   int `yaml:"output_limit"`
}

// ProviderConfig defines an LLM provider's connection details.
type ProviderConfig struct {
	Type      string `yaml:"type"`
	APIKey    string `yaml:"api_key"`
	BaseURL   string `yaml:"base_url"`
	ProjectID string `yaml:"project_id"`
	Location  string `yaml:"location"`
}

// RoleConfig maps an agent role to a specific provider and model.
type RoleConfig struct {
	Provider string `yaml:"provider"`
	Model    string `yaml:"model"`
}

// LLMDefaults contains default parameters for LLM calls.
type LLMDefaults struct {
	MaxTokens   int     `yaml:"max_tokens"`
	Temperature float64 `yaml:"temperature"`
}

// MCPConfig holds MCP server configurations.
type MCPConfig struct {
	Servers map[string]MCPServerConfig `yaml:"servers"`
}

// MCPServerConfig defines how to launch an MCP server.
type MCPServerConfig struct {
	Command string            `yaml:"command"`
	Args    []string          `yaml:"args"`
	Env     map[string]string `yaml:"env"`
}

// SkillsConfig configures the skills system.
type SkillsConfig struct {
	Directory string       `yaml:"directory"`
	Docker    DockerConfig `yaml:"docker"`
}

// DockerConfig holds Docker-related settings for skill execution.
type DockerConfig struct {
	WarmPoolThreshold   int    `yaml:"warm_pool_threshold"`
	WarmPoolIdleTimeout string `yaml:"warm_pool_idle_timeout"`
	DefaultMemory       string `yaml:"default_memory"`
	DefaultCPU          string `yaml:"default_cpu"`
	DefaultTimeout      string `yaml:"default_timeout"`
}

// MemoryConfig holds memory system configuration.
type MemoryConfig struct {
	Episodic     EpisodicConfig     `yaml:"episodic"`
	Semantic     SemanticConfig     `yaml:"semantic"`
	Constitution ConstitutionConfig `yaml:"constitution"`
}

// EpisodicConfig configures episodic memory storage.
type EpisodicConfig struct {
	Database       string `yaml:"database"`
	RetentionDays  int    `yaml:"retention_days"`
	RetrievalLimit int    `yaml:"retrieval_limit"`
}

// SemanticConfig configures semantic memory and embeddings.
type SemanticConfig struct {
	Database          string `yaml:"database"`
	EmbeddingProvider string `yaml:"embedding_provider"`
	EmbeddingModel    string `yaml:"embedding_model"`
}

// ConstitutionConfig configures the agent's constitution.
type ConstitutionConfig struct {
	File                   string `yaml:"file"`
	UpdateIntervalSessions int    `yaml:"update_interval_sessions"`
}

// RouterConfig holds router settings.
type RouterConfig struct {
	HistoryWindow int `yaml:"history_window"`
}

// ExecutorConfig holds executor settings.
type ExecutorConfig struct {
	MaxReactSteps      int              `yaml:"max_react_steps"`
	MaxRetries         int              `yaml:"max_retries"`
	OutputTokenReserve int              `yaml:"output_token_reserve"`
	Compaction         CompactionConfig `yaml:"compaction"`
}

// CompactionConfig holds context compaction settings.
type CompactionConfig struct {
	SlidingWindow SlidingWindowConfig  `yaml:"sliding_window"`
	Summarization SummarizationConfig  `yaml:"summarization"`
	Hierarchical  HierarchicalConfig   `yaml:"hierarchical"`
	Thresholds    CompactionThresholds `yaml:"thresholds"`
}

// CompactionThresholds defines context window usage thresholds for compaction triggers.
type CompactionThresholds struct {
	PredictivePercent int `yaml:"predictive_percent"`
	WarningPercent    int `yaml:"warning_percent"`
	EmergencyPercent  int `yaml:"emergency_percent"`
}

// SlidingWindowConfig configures sliding window compaction.
type SlidingWindowConfig struct {
	KeepFirst int `yaml:"keep_first"`
	KeepLast  int `yaml:"keep_last"`
}

// SummarizationConfig configures summarization compaction.
type SummarizationConfig struct {
	BlockSize int `yaml:"block_size"`
}

// HierarchicalConfig configures hierarchical compaction.
type HierarchicalConfig struct {
	EnabledAboveSteps int `yaml:"enabled_above_steps"`
}

// SecurityConfig holds security settings.
type SecurityConfig struct {
	Judge         JudgeConfig                 `yaml:"judge"`
	ToolPolicies  map[string]ToolPolicyConfig `yaml:"tool_policies"`
	DefaultPolicy string                      `yaml:"default_policy"`
}

// ToolPolicyConfig holds per-tool security policy configuration.
type ToolPolicyConfig struct {
	Policy    string   `yaml:"policy"`    // "always_allow"|"always_deny"|"user_confirm"|"auto"
	Blacklist []string `yaml:"blacklist"` // regex patterns (e.g. for bash)
}

// JudgeConfig holds LLM-based tool safety judge settings.
type JudgeConfig struct {
	Enabled *bool  `yaml:"enabled"` // whether judge is active (pointer to distinguish unset from false)
	Model   string `yaml:"model"`   // LLM model override for judge calls (empty = use default)
}

// SearchConfig holds web search configuration.
type SearchConfig struct {
	Provider string `yaml:"provider"`
	APIKey   string `yaml:"api_key"`
}

// envVarPattern matches ${ENV_VAR} patterns for substitution.
var envVarPattern = regexp.MustCompile(`\$\{([^}]+)\}`)

// Load reads a configuration file, substitutes environment variables,
// applies defaults, validates the configuration, and returns it.
func Load(path string) (*Config, error) {
	// Read file
	data, err := os.ReadFile(path)
	if err != nil {
		return nil, fmt.Errorf("failed to read config file: %w", err)
	}

	// Substitute ${ENV_VAR} patterns with environment variable values
	content := envVarPattern.ReplaceAllStringFunc(string(data), func(match string) string {
		// Extract the variable name from ${VAR_NAME}
		varName := match[2 : len(match)-1]
		return os.Getenv(varName)
	})

	// Unmarshal YAML
	var cfg Config
	if err := yaml.Unmarshal([]byte(content), &cfg); err != nil {
		return nil, fmt.Errorf("failed to parse config YAML: %w", err)
	}

	// Apply defaults for zero-value fields
	ApplyDefaults(&cfg)

	// Validate configuration
	if err := validate(&cfg); err != nil {
		return nil, fmt.Errorf("config validation failed: %w", err)
	}

	return &cfg, nil
}

// Save writes the configuration to a YAML file atomically.
func Save(cfg *Config, path string) error {
	data, err := yaml.Marshal(cfg)
	if err != nil {
		return fmt.Errorf("failed to marshal config: %w", err)
	}

	// Write atomically: write to temp file, then rename
	tmpPath := path + ".tmp"
	if err := os.WriteFile(tmpPath, data, 0644); err != nil {
		return fmt.Errorf("failed to write temp config file: %w", err)
	}
	if err := os.Rename(tmpPath, path); err != nil {
		// Clean up temp file on rename failure
		_ = os.Remove(tmpPath)
		return fmt.Errorf("failed to rename config file: %w", err)
	}
	return nil
}

// validate checks that the configuration is valid.
func validate(cfg *Config) error {
	// Check that at least one provider is defined
	if len(cfg.LLM.Providers) == 0 {
		return fmt.Errorf("at least one LLM provider must be defined")
	}

	// Check that all roles reference existing providers
	for roleName, role := range cfg.LLM.Roles {
		if role.Provider == "" {
			return fmt.Errorf("role %q has no provider specified", roleName)
		}
		if _, exists := cfg.LLM.Providers[role.Provider]; !exists {
			return fmt.Errorf("role %q references non-existent provider %q", roleName, role.Provider)
		}
	}

	return nil
}
