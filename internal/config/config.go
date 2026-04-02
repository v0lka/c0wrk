// Package config provides configuration loading and validation for the agent.
package config

import (
	"errors"
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

// LLMConfig holds LLM provider configuration with fixed provider schema.
type LLMConfig struct {
	ActiveProvider   string                   `yaml:"active_provider"` // "anthropic"|"gemini"|"lmstudio"|"openai_compatible"|"chatgpt"
	Anthropic        AnthropicConfig          `yaml:"anthropic"`
	Gemini           GeminiConfig             `yaml:"gemini"`
	LMStudio         LMStudioConfig           `yaml:"lmstudio"`
	OpenAICompatible OpenAICompatibleConfig   `yaml:"openai_compatible"`
	ChatGPT          ChatGPTConfig            `yaml:"chatgpt"`
	Models           map[string]ModelOverride `yaml:"models"`
}

// AnthropicConfig holds Anthropic provider configuration.
type AnthropicConfig struct {
	APIKey string `yaml:"api_key"`
	Model  string `yaml:"model"`
}

// GeminiConfig holds Gemini provider configuration.
type GeminiConfig struct {
	APIKey string `yaml:"api_key"`
	Model  string `yaml:"model"`
}

// LMStudioConfig holds LM Studio provider configuration.
type LMStudioConfig struct {
	BaseURL string `yaml:"base_url"`
	APIKey  string `yaml:"api_key"`
	Model   string `yaml:"model"`
}

// OpenAICompatibleConfig holds OpenAI-compatible provider configuration.
type OpenAICompatibleConfig struct {
	BaseURL string `yaml:"base_url"`
	APIKey  string `yaml:"api_key"`
	Model   string `yaml:"model"`
}

// ChatGPTConfig holds ChatGPT (OpenAI) provider configuration.
type ChatGPTConfig struct {
	APIKey string `yaml:"api_key"`
	Model  string `yaml:"model"`
}

// ModelOverride allows overriding built-in model metadata.
type ModelOverride struct {
	ContextWindow int `yaml:"context_window"`
	OutputLimit   int `yaml:"output_limit"`
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
	Database     string             `yaml:"database"` // single DB path for all persistent memory
	Episodic     EpisodicConfig     `yaml:"episodic"`
	Constitution ConstitutionConfig `yaml:"constitution"`
}

// EpisodicConfig configures episodic memory storage.
type EpisodicConfig struct {
	RetentionDays  int `yaml:"retention_days"`
	RetrievalLimit int `yaml:"retrieval_limit"`
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

// ExpandEnvVars expands ${ENV_VAR} patterns in a string with their environment variable values.
// This is a public function that can be used at runtime for values that bypass config file loading.
func ExpandEnvVars(s string) string {
	return envVarPattern.ReplaceAllStringFunc(s, func(match string) string {
		// Extract the variable name from ${VAR_NAME}
		varName := match[2 : len(match)-1]
		return os.Getenv(varName)
	})
}

// LoadResult contains the result of loading a configuration file.
type LoadResult struct {
	Config       *Config
	Migrated     bool     // true if config was migrated from old format
	MigrationMsg string   // human-readable migration message
	LoadErrors   []string // non-fatal errors/warnings encountered during load
}

// GetActiveProviderConfig returns (providerType, apiKey, baseURL, model) for the active provider.
// providerType is the Go provider type to create: "anthropic", "gemini", "lmstudio", "openai" (for both openai_compatible and chatgpt).
func (c *LLMConfig) GetActiveProviderConfig() (providerType, apiKey, baseURL, model string) {
	switch c.ActiveProvider {
	case "anthropic":
		return "anthropic", c.Anthropic.APIKey, "", c.Anthropic.Model
	case "gemini":
		return "gemini", c.Gemini.APIKey, "", c.Gemini.Model
	case "lmstudio":
		return "lmstudio", c.LMStudio.APIKey, c.LMStudio.BaseURL, c.LMStudio.Model
	case "openai_compatible":
		return "openai", c.OpenAICompatible.APIKey, c.OpenAICompatible.BaseURL, c.OpenAICompatible.Model
	case "chatgpt":
		return "openai", c.ChatGPT.APIKey, "", c.ChatGPT.Model
	default:
		return "", "", "", ""
	}
}

// Load reads a configuration file, substitutes environment variables,
// applies defaults, validates the configuration, and returns it.
// For better error handling and migration support, use LoadWithResult.
func Load(path string) (*Config, error) {
	result, err := LoadWithResult(path)
	if err != nil {
		return nil, err
	}
	return result.Config, nil
}

// LoadWithResult reads a configuration file with full error reporting.
// It substitutes environment variables, applies defaults, and validates.
func LoadWithResult(path string) (*LoadResult, error) {
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

	// Unmarshal as current format
	var cfg Config
	if err := yaml.Unmarshal([]byte(content), &cfg); err != nil {
		return nil, fmt.Errorf("failed to parse config YAML: %w", err)
	}

	// Apply defaults for zero-value fields
	ApplyDefaults(&cfg)

	// Validate configuration
	if err := validate(&cfg); err != nil {
		return &LoadResult{
			Config:     &cfg,
			LoadErrors: []string{"Config validation failed: " + err.Error()},
		}, fmt.Errorf("config validation failed: %w", err)
	}

	return &LoadResult{Config: &cfg}, nil
}

// Save writes the configuration to a YAML file atomically.
func Save(cfg *Config, path string) error {
	data, err := yaml.Marshal(cfg)
	if err != nil {
		return fmt.Errorf("failed to marshal config: %w", err)
	}

	// Write atomically: write to temp file, then rename
	tmpPath := path + ".tmp"
	if err := os.WriteFile(tmpPath, data, 0o644); err != nil {
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
	// Validate active_provider is one of known values
	switch cfg.LLM.ActiveProvider {
	case "anthropic", "gemini", "lmstudio", "openai_compatible", "chatgpt":
		// valid
	case "":
		return errors.New("llm.active_provider must be specified")
	default:
		return fmt.Errorf("llm.active_provider %q is not a valid provider (must be anthropic, gemini, lmstudio, openai_compatible, or chatgpt)", cfg.LLM.ActiveProvider)
	}

	// Validate active provider has model set
	_, _, _, model := cfg.LLM.GetActiveProviderConfig()
	if model == "" {
		return fmt.Errorf("active provider %q must have a model specified", cfg.LLM.ActiveProvider)
	}

	// Validate base_url for providers that require it
	if cfg.LLM.ActiveProvider == "openai_compatible" {
		if cfg.LLM.OpenAICompatible.BaseURL == "" {
			return errors.New("openai_compatible provider requires base_url")
		}
	}

	return nil
}
