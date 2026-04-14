// Package config provides configuration loading and validation for the agent.
package config

import (
	"errors"
	"fmt"
	"os"
	"regexp"

	"gopkg.in/yaml.v3"
)

// DefaultAgentDir is the default directory for agent files (data, tools, config).
const DefaultAgentDir = ".c0wrk"

// Config is the top-level configuration structure.
type Config struct {
	LogLevel string    `yaml:"log_level"`
	Theme    string    `yaml:"theme"`
	LLM      LLMConfig `yaml:"llm"`
	MCP      MCPConfig `yaml:"mcp"`

	Memory        MemoryConfig        `yaml:"memory"`
	Router        RouterConfig        `yaml:"router"`
	Executor      ExecutorConfig      `yaml:"executor"`
	Security      SecurityConfig      `yaml:"security"`
	Search        SearchConfig        `yaml:"search"`
	ToolLimits    ToolLimitsConfig    `yaml:"toolLimits"`
	Timeouts      TimeoutsConfig      `yaml:"timeouts"`
	Orchestration OrchestrationConfig `yaml:"orchestration"`
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
	Retry            LLMRetryConfig           `yaml:"retry"`
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

// LLMRetryConfig configures retry behavior for LLM API calls.
type LLMRetryConfig struct {
	MaxRetries     int    `yaml:"max_retries"`     // max retry attempts (0 = no retries)
	InitialBackoff string `yaml:"initial_backoff"` // initial backoff duration (e.g. "1s")
	MaxBackoff     string `yaml:"max_backoff"`     // maximum backoff duration (e.g. "30s")
}

// MCPConfig holds MCP server configurations.
type MCPConfig struct {
	Servers map[string]MCPServerConfig `yaml:"servers"`
}

// MCPServerConfig defines how to launch an MCP server.
type MCPServerConfig struct {
	Transport string `yaml:"transport,omitempty"` // "stdio" | "http"; default "stdio"

	// stdio fields (existing)
	Command string            `yaml:"command,omitempty"`
	Args    []string          `yaml:"args,omitempty"`
	Env     map[string]string `yaml:"env,omitempty"`

	// http fields (new)
	URL     string            `yaml:"url,omitempty"`
	Headers map[string]string `yaml:"headers,omitempty"`
}

// MemoryConfig holds memory system configuration.
type MemoryConfig struct {
	Database string `yaml:"database"` // single DB path for all persistent memory
}

// RouterConfig holds router settings.
type RouterConfig struct {
	HistoryWindow int `yaml:"history_window"`
}

// ToolResultBudgetConfig configures tool result size limits to prevent single tool outputs
// from consuming too much of the context window.
type ToolResultBudgetConfig struct {
	HardCapTokens   int     `yaml:"hard_cap_tokens"`   // absolute max tokens per tool result (default: 8192)
	MaxFillFraction float64 `yaml:"max_fill_fraction"` // max fraction of available context space (default: 0.3)
}

// ToolOutputPruningConfig configures selective pruning of old tool outputs to save context.
type ToolOutputPruningConfig struct {
	KeepLastN      int      `yaml:"keepLastN"`
	ProtectedTools []string `yaml:"protectedTools"`
}

// CircuitBreakerConfig holds circuit breaker thresholds for executor protection.
type CircuitBreakerConfig struct {
	RepeatNudgeThreshold     int `yaml:"repeatNudgeThreshold"`     // consecutive identical tool calls before nudge
	RepeatAbortThreshold     int `yaml:"repeatAbortThreshold"`     // consecutive identical tool calls before abort
	TruncationAbortThreshold int `yaml:"truncationAbortThreshold"` // consecutive truncated responses before abort
	ParseErrorAbortThreshold int `yaml:"parseErrorAbortThreshold"` // consecutive parse errors before abort
	FruitlessNudgeThreshold      int `yaml:"fruitlessNudgeThreshold"`      // consecutive minimal-result calls before nudge (default: 4)
	FruitlessAbortThreshold      int `yaml:"fruitlessAbortThreshold"`      // consecutive minimal-result calls before abort (default: 6)
	FruitlessMaxResultLen        int `yaml:"fruitlessMaxResultLen"`        // result length at or below which a call is "fruitless" (default: 32)
	SameToolRepeatNudgeThreshold int `yaml:"sameToolRepeatNudgeThreshold"` // same tool with varied args, similar results (default: 6)
	SameToolRepeatAbortThreshold int `yaml:"sameToolRepeatAbortThreshold"` // abort threshold (default: 10)
	SameToolResultSizeDelta      int `yaml:"sameToolResultSizeDelta"`      // max result length difference to consider "similar" (default: 64)
}

// ExecutorConfig holds executor settings.
type ExecutorConfig struct {
	MaxReactSteps      int                     `yaml:"max_react_steps"`
	MaxRetries         int                     `yaml:"max_retries"`
	OutputTokenReserve int                     `yaml:"output_token_reserve"`
	Compaction         CompactionConfig        `yaml:"compaction"`
	ToolResultBudget   ToolResultBudgetConfig  `yaml:"tool_result_budget"`
	ToolOutputPruning  ToolOutputPruningConfig `yaml:"toolOutputPruning"`
	CircuitBreaker     CircuitBreakerConfig    `yaml:"circuitBreaker"`
}

// CompactionConfig holds context compaction settings.
type CompactionConfig struct {
	SlidingWindow          SlidingWindowConfig  `yaml:"sliding_window"`
	Summarization          SummarizationConfig  `yaml:"summarization"`
	Hierarchical           HierarchicalConfig   `yaml:"hierarchical"`
	Thresholds             CompactionThresholds `yaml:"thresholds"`
	MaxSummarizeTokens     int                  `yaml:"maxSummarizeTokens"`     // max tokens for summarization LLM calls (default: 16000)
	ObservationTruncate    int                  `yaml:"observationTruncate"`    // chars to truncate observations in summaries (default: 500)
	SafetyMarginPercent    int                  `yaml:"safetyMarginPercent"`    // % of context window reserved as safety margin (default: 5)
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
	KeepLast  int `yaml:"keepLast"` // number of recent steps to preserve verbatim (default: 5)
}

// HierarchicalConfig configures hierarchical compaction.
type HierarchicalConfig struct {
	EnabledAboveSteps int     `yaml:"enabled_above_steps"`
	DistantRatio      float64 `yaml:"distantRatio"` // ratio for distant zone (default: 0.4)
	MiddleRatio       float64 `yaml:"middleRatio"`  // ratio for middle zone (default: 0.3)
	RecentRatio       float64 `yaml:"recentRatio"`  // ratio for recent zone (default: 0.3)
}

// SecurityConfig holds security settings.
type SecurityConfig struct {
	Judge         JudgeConfig                 `yaml:"judge"`
	ToolPolicies  map[string]ToolPolicyConfig `yaml:"tool_policies"`
	DefaultPolicy string                      `yaml:"default_policy"`
}

// ToolPolicyConfig holds per-tool security policy configuration.
type ToolPolicyConfig struct {
	Policy    string   `yaml:"policy"`    // "always_allow"|"always_deny"|"user_confirm"
	Blacklist []string `yaml:"blacklist"` // regex patterns (e.g. for bash)
}

// JudgeConfig holds LLM-based tool safety judge settings.
type JudgeConfig struct {
	Model string `yaml:"model"` // LLM model override for judge calls (empty = use default)
}

// SearchConfig holds web search configuration.
type SearchConfig struct {
	Provider string `yaml:"provider"`
	APIKey   string `yaml:"api_key"`
}

// ToolLimitsConfig holds configurable limits for builtin tools.
// These limits prevent tool outputs from consuming excessive context.
type ToolLimitsConfig struct {
	// File read limits
	ReadDefaultLines  int `yaml:"readDefaultLines"`  // max lines per read call (default: 2000)
	ReadMaxLineLength int `yaml:"readMaxLineLength"` // max characters per line (default: 2000)
	ReadMaxBytes      int `yaml:"readMaxBytes"`      // total output cap in bytes (default: 51200)

	// Search limits
	RipgrepMaxResults    int `yaml:"ripgrepMaxResults"`    // max matches for ripgrep (default: 200)
	RipgrepMaxLineLength int `yaml:"ripgrepMaxLineLength"` // max chars per ripgrep line (default: 2000)
	GlobMaxResults       int `yaml:"globMaxResults"`       // max glob results (default: 200)
	FileSearchMaxMatches int `yaml:"fileSearchMaxMatches"` // max matches for file content search (default: 100)
	WebSearchMaxResults  int `yaml:"webSearchMaxResults"`  // max web search results (default: 5)

	// Web fetch limit
	WebFetchMaxBodySize int `yaml:"webFetchMaxBodySize"` // max response body size in bytes (default: 102400)

	// Batch limits
	BatchMaxConcurrency int `yaml:"batchMaxConcurrency"` // max parallel tool executions (default: 10)
	BatchMaxResultSize  int `yaml:"batchMaxResultSize"`  // total character budget across results (default: 50000)
}

// TimeoutsConfig holds configurable timeout values for various operations.
type TimeoutsConfig struct {
	BashMaxTimeout     int `yaml:"bashMaxTimeout"`     // seconds, default: 120
	BashWaitDelay      int `yaml:"bashWaitDelay"`      // seconds, default: 5
	RipgrepTimeout     int `yaml:"ripgrepTimeout"`     // seconds, default: 60
	WebFetchTimeout    int `yaml:"webFetchTimeout"`    // seconds, default: 30
	WebSearchTimeout   int `yaml:"webSearchTimeout"`   // seconds, default: 30
	PersistenceTimeout int `yaml:"persistenceTimeout"` // seconds, default: 5
}

// OrchestrationConfig holds orchestration-specific limits and settings.
type OrchestrationConfig struct {
	MaxDependencyContextChars int `yaml:"maxDependencyContextChars"` // default: 8000
	MaxSummaryLength          int `yaml:"maxSummaryLength"`          // default: 500
	MaxHistoryMessages        int `yaml:"maxHistoryMessages"`        // default: 20
	MaxJudgeCacheSize         int `yaml:"maxJudgeCacheSize"`         // default: 1000
	MaxPlannerExploreSteps    int `yaml:"maxPlannerExploreSteps"`    // default: 7
}

// ValidProviders is the canonical set of supported LLM provider names.
var ValidProviders = map[string]bool{
	"anthropic":         true,
	"gemini":            true,
	"lmstudio":          true,
	"openai_compatible": true,
	"chatgpt":           true,
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

// Load reads a configuration file, applies defaults, validates the configuration, and returns it.
// Environment variable references like ${VAR} are preserved as-is in the config struct;
// use ExpandEnvVars() at runtime to resolve them when needed.
// For better error handling and migration support, use LoadWithResult.
func Load(path string) (*Config, error) {
	result, err := LoadWithResult(path)
	if err != nil {
		return nil, err
	}
	return result.Config, nil
}

// LoadWithResult reads a configuration file with full error reporting.
// Environment variable references like ${VAR} are preserved as-is;
// they are resolved at runtime via ExpandEnvVars() when actually needed.
func LoadWithResult(path string) (*LoadResult, error) {
	// Read file
	data, err := os.ReadFile(path)
	if err != nil {
		return nil, fmt.Errorf("failed to read config file: %w", err)
	}

	// Unmarshal as current format (env var references like ${VAR} are preserved as-is;
	// they are resolved at runtime via ExpandEnvVars when actually needed).
	var cfg Config
	if err := yaml.Unmarshal(data, &cfg); err != nil {
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
	if cfg.LLM.ActiveProvider == "" {
		return errors.New("llm.active_provider must be specified")
	}
	if !ValidProviders[cfg.LLM.ActiveProvider] {
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

	// Validate that internal tools are not in tool_policies
	for toolName := range cfg.Security.ToolPolicies {
		if isInternalTool(toolName) {
			return fmt.Errorf("tool %q is an internal tool and cannot have a custom policy", toolName)
		}
	}

	return nil
}

// isInternalTool returns true if the given tool name is an internal tool
// that is always allowed and cannot have a custom policy.
// This must be kept in sync with core/tools/registry.go internalTools.
func isInternalTool(name string) bool {
	internalTools := map[string]bool{
		"ask_user":          true,
		"batch":             true,
		"finish":            true,
		"list_step_outputs": true,
		"read_step_output":  true,
	}
	return internalTools[name]
}
