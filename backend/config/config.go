// Package config provides configuration loading and validation for the agent.
package config

import (
	"errors"
	"fmt"
	"os"
	"regexp"
	"sort"

	"github.com/v0lka/c0wrk/core/tools"
	"github.com/v0lka/sp4rk/llm"

	"gopkg.in/yaml.v3"
)

// DefaultAgentDir is the default directory for agent files (data, tools, config).
const DefaultAgentDir = ".c0wrk"

// Config is the top-level configuration structure.
type Config struct {
	LogLevel string    `yaml:"log_level"`
	LLM      LLMConfig `yaml:"llm"`
	MCP      MCPConfig `yaml:"mcp"`

	Router        RouterConfig        `yaml:"router"`
	Executor      ExecutorConfig      `yaml:"executor"`
	Security      SecurityConfig      `yaml:"security"`
	Skills        SkillsConfig        `yaml:"skills"`
	Agents        AgentsConfig        `yaml:"agents"`
	Search        SearchConfig        `yaml:"search"`
	ToolLimits    ToolLimitsConfig    `yaml:"toolLimits"`
	Timeouts      TimeoutsConfig      `yaml:"timeouts"`
	Orchestration OrchestrationConfig `yaml:"orchestration"`
	GoalLoop      GoalLoopConfig      `yaml:"goal_loop"`
	VectorIndex   VectorIndexConfig   `yaml:"vector_index"`
	Proxy         ProxyConfig         `yaml:"proxy"`

	// SmallLLM configures optimizations applied when running on a "small"
	// (low-capacity / cheaper) LLM. The master toggle is manual only — there
	// is no auto-detection; the operator decides when to enable it. Each
	// variant carries its own sub-toggle so individual optimizations can be
	// turned off without disabling the whole profile, and every threshold/value
	// is exposed so behaviour can be tuned without a rebuild.
	SmallLLM SmallLLMConfig `yaml:"small_llm"`

	// Updates configures the automatic "check for updates" subsystem that runs
	// in the background after the backend is ready. The state it produces (the
	// timestamp of the last check) is persisted to update_state.json, not to
	// this file; see core/updater/state.go and config.UpdateStatePath.
	Updates UpdatesConfig `yaml:"updates"`
}

// UpdatesConfig controls the background automatic update checker. It is an
// operator-level toggle (config.yaml), complementing the user-level toggle in
// update-settings.json (enabled / auto-check): both must permit a check for the
// background check to run.
type UpdatesConfig struct {
	// Enabled gates the background automatic update check. It is a pointer-bool
	// so callers can distinguish "unset" (defaults to true) from "explicitly
	// disabled" (false), matching the ProxyConfig.SetGlobalEnv convention.
	// When false, the background check never runs, though a user can still
	// trigger a manual check from the UI.
	Enabled *bool `yaml:"enabled"`

	// CheckInterval is the minimum time between automatic checks, expressed as
	// a duration string (e.g. "6h"). Defaults to "6h". It is parsed with
	// time.ParseDuration; an unparseable value is treated as the default.
	CheckInterval string `yaml:"check_interval"`
}

// ProxyConfig holds HTTP/HTTPS proxy settings for all outbound connections.
type ProxyConfig struct {
	Enabled    bool     `yaml:"enabled"`
	URL        string   `yaml:"url"`          // scheme://user:password@host:port
	BypassList []string `yaml:"bypass_list"`  // hostnames/IPs to skip proxy
	TLSCertDir string   `yaml:"tls_cert_dir"` // directory with .pem/.crt CA certs

	// SetGlobalEnv, when true, exports HTTP_PROXY/HTTPS_PROXY/NO_PROXY/SSL_CERT_DIR
	// into the process environment so subprocesses (bash_exec children, MCP
	// stdio servers) inherit the proxy. Default: true when proxy is enabled
	// (backward compat). Set to false to prevent proxy state from leaking into
	// third-party Go libraries that read env vars at init time.
	// Pointer-bool so callers can distinguish "unset" from "explicitly false".
	SetGlobalEnv *bool `yaml:"set_global_env"`
}

// VectorIndexConfig holds vector / hybrid search runtime settings.
//
// Hybrid is a pointer-bool so callers can distinguish "unset" (defaults
// to true) from "explicitly disabled" (false). When Hybrid is false the
// service only writes and reads the chromem vector index; bleve is
// still opened but not consulted at query time.
//
// The HybridRRFK / HybridFanoutMultiplier / HybridFanoutMin fields tune
// Reciprocal Rank Fusion. A zero value falls back to the built-in
// default (60 / 4 / 100).
//
// The HybridVectorScoreFloor / HybridVectorScoreRatio /
// HybridLexicalScoreRatio fields are pointer-float64 so callers can
// distinguish "unset" (defaults applied) from "explicitly zero"
// (threshold disabled). They suppress noise-tail hits before fusion:
//   - VectorScoreFloor: absolute cosine-similarity floor.
//   - VectorScoreRatio: relative cutoff (sim < ratio × top sim rejected).
//   - LexicalScoreRatio: relative BM25 cutoff (score < ratio × top rejected).
type VectorIndexConfig struct {
	Hybrid *bool `yaml:"hybrid"`

	HybridRRFK              int      `yaml:"hybrid_rrf_k"`
	HybridFanoutMultiplier  int      `yaml:"hybrid_fanout_multiplier"`
	HybridFanoutMin         int      `yaml:"hybrid_fanout_min"`
	HybridVectorScoreFloor  *float64 `yaml:"hybrid_vector_score_floor"`
	HybridVectorScoreRatio  *float64 `yaml:"hybrid_vector_score_ratio"`
	HybridLexicalScoreRatio *float64 `yaml:"hybrid_lexical_score_ratio"`

	// EmbeddingThreads controls the ONNX intra-op thread pool used by the
	// embedding model during indexing. 0 (or unset) lets ONNX use all cores —
	// the legacy behaviour and default. Set 1..N to cap intra-op threads and
	// lower the CPU spike during (re)indexing at the cost of throughput.
	EmbeddingThreads int `yaml:"embedding_threads"`
}

// LLMConfig holds LLM provider configuration with fixed provider schema.
type LLMConfig struct {
	DefaultModel        string                               `yaml:"default_model"` // cross-provider default model (must exist in some provider's Models list)
	Anthropic           AnthropicConfig                      `yaml:"anthropic"`
	OpenAICompatible    map[string]OpenAICompatibleConfig    `yaml:"openai_compatible"`
	AnthropicCompatible map[string]AnthropicCompatibleConfig `yaml:"anthropic_compatible"`
	ChatGPT             ChatGPTConfig                        `yaml:"chatgpt"`
	Models              map[string]ModelOverride             `yaml:"models"`
	Retry               LLMRetryConfig                       `yaml:"retry"`
}

// AnthropicConfig holds Anthropic provider configuration.
type AnthropicConfig struct {
	APIKey string   `yaml:"api_key"`
	Models []string `yaml:"models"` // enabled models for this provider
}

// OpenAICompatibleConfig holds OpenAI-compatible provider configuration.
type OpenAICompatibleConfig struct {
	BaseURL string   `yaml:"base_url"`
	APIKey  string   `yaml:"api_key"`
	Models  []string `yaml:"models"` // enabled models for this provider
}

// AnthropicCompatibleConfig holds Anthropic-compatible provider configuration
// (custom endpoints speaking Anthropic's Messages API, e.g. a proxy or gateway).
type AnthropicCompatibleConfig struct {
	BaseURL string   `yaml:"base_url"`
	APIKey  string   `yaml:"api_key"`
	Models  []string `yaml:"models"` // enabled models for this provider
}

// ChatGPTConfig holds ChatGPT (OpenAI) provider configuration.
type ChatGPTConfig struct {
	APIKey string   `yaml:"api_key"`
	Models []string `yaml:"models"` // enabled models for this provider
}

// ModelOverride allows overriding built-in model metadata.
// Fields use omitempty so a 0/empty/nil value (meaning "inherit the built-in
// default") is not serialized — only fields that actually differ from the
// built-in metadata are persisted to config.yaml.
//
// TokenizerType/Family/Protocol use the empty string as the "inherit" sentinel
// (the built-in resolver derives them via DetectFamily/DetectProtocol when
// unset). Capabilities uses a nil pointer: nil = inherit default, a non-nil
// value overrides all four capability flags atomically. The string and pointer
// sentinels ensure a deliberate override to "default"/""/all-false is still
// distinguishable from "no override", so a user can force e.g. a false
// Attachment capability that differs from the built-in true.
type ModelOverride struct {
	ContextWindow int                    `yaml:"context_window,omitempty"`
	OutputLimit   int                    `yaml:"output_limit,omitempty"`
	TokenizerType string                 `yaml:"tokenizer_type,omitempty"`
	Family        string                 `yaml:"family,omitempty"`
	Protocol      string                 `yaml:"protocol,omitempty"`
	Capabilities  *llm.ModelCapabilities `yaml:"capabilities,omitempty"`
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

// RouterConfig holds router settings.
type RouterConfig struct {
	HistoryWindow int `yaml:"history_window"`
}

// ToolResultBudgetConfig configures tool result size limits to prevent single tool outputs
// from consuming too much of the context window.
type ToolResultBudgetConfig struct {
	HardCapTokens   int     `yaml:"hard_cap_tokens"`   // absolute max tokens per tool result (default: 4096)
	MaxFillFraction float64 `yaml:"max_fill_fraction"` // max fraction of available context space (default: 0.3)
	CacheTTLSeconds int     `yaml:"cacheTTLSeconds"`   // TTL in seconds for MCP tool cache entries (default: 300)
}

// ToolOutputPruningConfig configures selective pruning of old tool outputs to save context.
type ToolOutputPruningConfig struct {
	KeepLastN        int      `yaml:"keepLastN"`
	ProtectedTools   []string `yaml:"protectedTools"`
	ThresholdPercent float64  `yaml:"thresholdPercent"` // Context fill % below which pruning is skipped (default: 50)
}

// HistoryMutationConfig configures regular (non-emergency) history mutation
// to reduce O(n²) replay cost. Unlike emergency compaction, mutation runs on
// every BuildPrompt call and replaces old tool results with cache references.
type HistoryMutationConfig struct {
	ToolResultEvictionStep int  `yaml:"toolResultEvictionStep"` // evict tool results to cache refs after N steps (default: 10)
	EvictStepStatus        bool `yaml:"evictStepStatus"`        // evict update_checklist results immediately
	DedupRepeatedReads     bool `yaml:"dedupRepeatedReads"`     // replace duplicate file reads with reference
}

// CircuitBreakerConfig holds circuit breaker thresholds for executor protection.
type CircuitBreakerConfig struct {
	RepeatNudgeThreshold         int `yaml:"repeatNudgeThreshold"`         // consecutive identical tool calls before nudge
	RepeatAbortThreshold         int `yaml:"repeatAbortThreshold"`         // consecutive identical tool calls before abort
	TruncationAbortThreshold     int `yaml:"truncationAbortThreshold"`     // consecutive truncated responses before abort
	ParseErrorAbortThreshold     int `yaml:"parseErrorAbortThreshold"`     // consecutive parse errors before abort
	FruitlessNudgeThreshold      int `yaml:"fruitlessNudgeThreshold"`      // consecutive minimal-result calls before nudge (default: 4)
	FruitlessAbortThreshold      int `yaml:"fruitlessAbortThreshold"`      // consecutive minimal-result calls before abort (default: 6)
	FruitlessMaxResultLen        int `yaml:"fruitlessMaxResultLen"`        // result length at or below which a call is "fruitless" (default: 32)
	SameToolRepeatNudgeThreshold int `yaml:"sameToolRepeatNudgeThreshold"` // same tool with varied args, similar results (default: 6)
	SameToolRepeatAbortThreshold int `yaml:"sameToolRepeatAbortThreshold"` // abort threshold (default: 10)
	SameToolResultSizeDelta      int `yaml:"sameToolResultSizeDelta"`      // max result length difference to consider "similar" (default: 64)
}

// ExecutorConfig holds executor settings.
type ExecutorConfig struct {
	MaxRetries         int                     `yaml:"max_retries"`
	OutputTokenReserve int                     `yaml:"output_token_reserve"`
	Compaction         CompactionConfig        `yaml:"compaction"`
	ToolResultBudget   ToolResultBudgetConfig  `yaml:"tool_result_budget"`
	ToolOutputPruning  ToolOutputPruningConfig `yaml:"toolOutputPruning"`
	HistoryMutation    HistoryMutationConfig   `yaml:"historyMutation"`
	CircuitBreaker     CircuitBreakerConfig    `yaml:"circuitBreaker"`
}

// CompactionConfig holds context compaction settings.
type CompactionConfig struct {
	SlidingWindow       SlidingWindowConfig  `yaml:"sliding_window"`
	Summarization       SummarizationConfig  `yaml:"summarization"`
	Hierarchical        HierarchicalConfig   `yaml:"hierarchical"`
	Thresholds          CompactionThresholds `yaml:"thresholds"`
	MaxSummarizeTokens  int                  `yaml:"maxSummarizeTokens"`  // max tokens for summarization LLM calls (default: 16000)
	ObservationTruncate int                  `yaml:"observationTruncate"` // chars to truncate observations in summaries (default: 500)
	SafetyMarginPercent int                  `yaml:"safetyMarginPercent"` // % of context window reserved as safety margin (default: 5)
}

// CompactionThresholds defines context window usage thresholds for compaction triggers.
type CompactionThresholds struct {
	PredictivePercent int `yaml:"predictive_percent"`
	WarningPercent    int `yaml:"warning_percent"`
	EmergencyPercent  int `yaml:"emergency_percent"`
	PreWarningPercent int `yaml:"pre_warning_percent"`
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

// InjectionDefenseConfig holds prompt injection defense settings.
type InjectionDefenseConfig struct {
	Enabled *bool `yaml:"enabled"` // pointer to distinguish "not set" from "false"; nil defaults to true
}

// SecurityConfig holds security settings.
type SecurityConfig struct {
	Judge            JudgeConfig                 `yaml:"judge"`
	InjectionDefense InjectionDefenseConfig      `yaml:"injection_defense"`
	ToolPolicies     map[string]ToolPolicyConfig `yaml:"tool_policies"`
	DefaultPolicy    string                      `yaml:"default_policy"`

	// AutoApproveWorkspaceWrites, when true, auto-executes file write tools
	// (write_file, edit_file, delete_file, delete_directory, create_directory)
	// without user confirmation when all paths are within the session workspace.
	// Symlink traversals are still intercepted and forced to confirmation
	// regardless of this setting. Default: false (always confirm writes).
	AutoApproveWorkspaceWrites bool `yaml:"auto_approve_workspace_writes"`

	// AgentsMDMaxBytes caps the AGENTS.md content size injected into prompts.
	// AGENTS.md is workspace-controlled untrusted input; without a cap a large or
	// malicious file could flood the context window.
	//   0  = use default (65536)
	//  -1  = no cap (unlimited — USE WITH CAUTION)
	AgentsMDMaxBytes int `yaml:"agents_md_max_bytes"`
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
	ReadDefaultLines int `yaml:"readDefaultLines"` // max lines per read call (default: 2000)

	// Search limits
	WebSearchMaxResults int `yaml:"webSearchMaxResults"` // max web search results (default: 5)

	// Per-tool Stage 1 truncation defaults (line/byte-based, applied before token budget).
	// If omitted for a tool, no Stage 1 truncation is applied.
	PerToolTruncation map[string]ToolTruncationConfig `yaml:"perToolTruncation"`
}

// ToolTruncationConfig — per-tool truncation settings for the universal caching layer.
type ToolTruncationConfig struct {
	MaxLines int `yaml:"maxLines"` // 0 = no line-based truncation
	MaxBytes int `yaml:"maxBytes"` // 0 = no byte-based truncation
}

// TimeoutsConfig holds configurable timeout values for various operations.
type TimeoutsConfig struct {
	BashMaxTimeout           int `yaml:"bashMaxTimeout"`           // seconds, default: 120
	BashWaitDelay            int `yaml:"bashWaitDelay"`            // seconds, default: 5
	RipgrepTimeout           int `yaml:"ripgrepTimeout"`           // seconds, default: 60
	WebFetchTimeout          int `yaml:"webFetchTimeout"`          // seconds, default: 30
	WebSearchTimeout         int `yaml:"webSearchTimeout"`         // seconds, default: 30
	PersistenceTimeout       int `yaml:"persistenceTimeout"`       // seconds, default: 5
	LLMRequestTimeout        int `yaml:"llmRequestTimeout"`        // seconds, default: 600 (10 min) — main chat loop
	ServiceLLMRequestTimeout int `yaml:"serviceLLMRequestTimeout"` // seconds, default: 120 (2 min) — one-shot service LLM requests (session title, commit message, prompt optimization)
}

// OrchestrationConfig holds orchestration-specific limits and settings.
type OrchestrationConfig struct {
	MaxDependencyContextChars int `yaml:"maxDependencyContextChars"` // default: 8000
	MaxSummaryLength          int `yaml:"maxSummaryLength"`          // default: 500
	MaxJudgeCacheSize         int `yaml:"maxJudgeCacheSize"`         // default: 1000
	// MaxRedelegationDepth caps recursive delegation when allow_redelegate is
	// true (ASI07-R6). 0 means "use the orchestrator default" (currently 2).
	MaxRedelegationDepth int `yaml:"maxRedelegationDepth"` // default: 2
}

// GoalLoopConfig holds settings for the goal-derivation / verification loop.
//
// Verification controls whether the loop runs the independent verifier.
//   - "independent" (default): the goal loop uses an independent verifier turn
//     to confirm task completion before declaring success.
//   - "off": the verifier is disabled; the loop relies solely on the agent's
//     own declare_goal_status verdict.
type GoalLoopConfig struct {
	Verification string `yaml:"verification"` // "independent" | "off"; default "independent"
}

// SkillsConfig holds Agent Skills discovery configuration.
type SkillsConfig struct {
	// Dirs lists skill discovery directories in priority order (highest first).
	// Paths may be absolute or relative to the agent directory (~/.c0wrk).
	Dirs []string `yaml:"dirs"`
}

// AgentsConfig holds Subagent Profile (AGENT.md) discovery configuration.
// Mirrors SkillsConfig: the project-local `.agents/agents` directory is always
// scanned automatically (see core/builder.go) and does NOT need to be listed.
type AgentsConfig struct {
	// Dirs lists subagent profile discovery directories in priority order
	// (highest first). Paths may be absolute or relative to the agent dir.
	Dirs []string `yaml:"dirs"`
}

// envVarPattern matches ${ENV_VAR} patterns for substitution.
var envVarPattern = regexp.MustCompile(`\$\{([^}]+)\}`)

// SmallLLMConfig configures optimizations applied when running on a "small"
// (low-capacity / cheaper) LLM. The master toggle is manual only — there is no
// auto-detection; the operator decides when to enable the profile. Each variant
// carries its own sub-toggle so individual optimizations can be turned off
// independently, and every threshold/value is exposed so behaviour can be tuned
// without a rebuild.
type SmallLLMConfig struct {
	// Enabled is the master toggle for the small-LLM profile. When false, every
	// variant sub-toggle is ignored. There is no auto-detection — this must be
	// set explicitly. Default: false.
	Enabled bool `yaml:"enabled"`

	// EssentialTools narrows the visible tool set to a curated subset to reduce
	// the schema/token overhead injected into every prompt.
	EssentialTools EssentialToolsConfig `yaml:"essential_tools"`

	// SystemPrompt applies prompt-simplification variants (lite, few-shot,
	// reasoning scaffold) to shrink the system prompt size.
	SystemPrompt SystemPromptConfig `yaml:"system_prompt"`

	// Sampling overrides LLM sampling parameters for more deterministic,
	// lower-effort generation suitable for smaller models.
	Sampling SmallLLMSamplingConfig `yaml:"sampling"`

	// LoopHardening tightens the executor circuit-breaker thresholds so a small
	// model that repeats itself or fails to make progress is nudged/aborted
	// sooner, conserving the token budget.
	LoopHardening LoopHardeningConfig `yaml:"loop_hardening"`
}

// EssentialToolsConfig narrows the tool set visible to a small LLM to reduce
// per-prompt schema overhead.
type EssentialToolsConfig struct {
	// Enabled gates this variant. When false the full tool set is exposed
	// regardless of the master SmallLLM.Enabled toggle.
	Enabled bool `yaml:"enabled"`

	// AlwaysPresent is the allow-list of tool names always exposed when this
	// variant is active. Tools not in this list are hidden from the model.
	AlwaysPresent []string `yaml:"always_present"`

	// MaxTools caps the total number of tools exposed to the model after the
	// always-present set is applied.
	MaxTools int `yaml:"max_tools"`
}

// SystemPromptConfig applies prompt-simplification variants to shrink the
// system prompt injected for a small LLM. Each flag is independent; FewShot
// and ReasoningScaffold are only honored when Lite is active (both are
// tailored to the compact lite directive).
type SystemPromptConfig struct {
	// Lite trims verbose guidance from the base system prompt, swapping the
	// verbose OrchestratorSystem directive for the compact OrchestratorSystemLite.
	Lite bool `yaml:"lite"`
	// FewShot appends curated worked-example ReAct cycles demonstrating correct
	// tool-call format, tool choice, error recovery, and finish. Only applied
	// when Lite is active.
	FewShot bool `yaml:"few_shot"`
	// ReasoningScaffold appends a lightweight three-step reasoning scaffold
	// (goal → tool+why → args) tailored to small-model instruction following.
	// Only applied when Lite is active.
	ReasoningScaffold bool `yaml:"reasoning_scaffold"`
}

// SmallLLMSamplingConfig overrides LLM sampling parameters for a small model.
type SmallLLMSamplingConfig struct {
	// Enabled gates this variant.
	Enabled bool `yaml:"enabled"`

	// Temperature sets generation temperature (lower = more deterministic).
	Temperature float64 `yaml:"temperature"`

	// TopP sets nucleus-sampling probability mass.
	TopP float64 `yaml:"top_p"`

	// ReasoningEffort controls reasoning depth: "" (inherit) | "off" | "low" |
	// "medium". Smaller models generally benefit from reduced reasoning effort.
	ReasoningEffort string `yaml:"reasoning_effort"`
}

// LoopHardeningConfig tightens executor circuit-breaker thresholds for a small
// LLM so loops are caught earlier, conserving the token budget.
type LoopHardeningConfig struct {
	// Enabled gates this variant.
	Enabled bool `yaml:"enabled"`

	// RepeatNudgeThreshold is the number of consecutive identical tool calls
	// before a corrective nudge is issued.
	RepeatNudgeThreshold int `yaml:"repeat_nudge_threshold"`

	// ParseErrorAbortThreshold is the number of consecutive response parse
	// errors before the executor aborts.
	ParseErrorAbortThreshold int `yaml:"parse_error_abort_threshold"`

	// FruitlessNudgeThreshold is the number of consecutive minimal-result tool
	// calls before a corrective nudge is issued.
	FruitlessNudgeThreshold int `yaml:"fruitless_nudge_threshold"`

	// FruitlessAbortThreshold is the number of consecutive minimal-result tool
	// calls before the executor aborts.
	FruitlessAbortThreshold int `yaml:"fruitless_abort_threshold"`

	// SameToolRepeatNudgeThreshold is the number of same-tool (varied args, similar
	// results) calls before a corrective nudge is issued.
	SameToolRepeatNudgeThreshold int `yaml:"same_tool_repeat_nudge_threshold"`
}

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
	Config     *Config
	LoadErrors []string // non-fatal errors/warnings encountered during load
}

// ProviderWithModels pairs a provider config key with its enabled models.
type ProviderWithModels struct {
	Name         string // config key: "anthropic", "chatgpt", or a named openai_compatible provider
	ProviderType string // Go type constant
	APIKey       string
	BaseURL      string
	Models       []string // enabled models for this one provider
}

// providerEntry is the canonical, single-source-of-truth provider list.
type providerEntry struct {
	name    string
	apiKey  string
	baseURL string
	models  []string
}

// allProviderEntries returns the flat list of all known providers.
func (c *LLMConfig) allProviderEntries() []providerEntry {
	// Sort openai_compatible keys for deterministic order
	openaiKeys := make([]string, 0, len(c.OpenAICompatible))
	for k := range c.OpenAICompatible {
		openaiKeys = append(openaiKeys, k)
	}
	sort.Strings(openaiKeys)
	// Sort anthropic_compatible keys for deterministic order
	anthropicKeys := make([]string, 0, len(c.AnthropicCompatible))
	for k := range c.AnthropicCompatible {
		anthropicKeys = append(anthropicKeys, k)
	}
	sort.Strings(anthropicKeys)
	entries := make([]providerEntry, 0, 2+len(openaiKeys)+len(anthropicKeys))
	entries = append(entries,
		providerEntry{"anthropic", c.Anthropic.APIKey, "", c.Anthropic.Models},
		providerEntry{"chatgpt", c.ChatGPT.APIKey, "", c.ChatGPT.Models},
	)
	for _, name := range openaiKeys {
		cfg := c.OpenAICompatible[name]
		entries = append(entries, providerEntry{name, cfg.APIKey, cfg.BaseURL, cfg.Models})
	}
	for _, name := range anthropicKeys {
		cfg := c.AnthropicCompatible[name]
		entries = append(entries, providerEntry{name, cfg.APIKey, cfg.BaseURL, cfg.Models})
	}
	return entries
}

// providerType maps a config-level provider name to the Go provider type.
func (c *LLMConfig) providerType(name string) string {
	switch name {
	case "anthropic":
		return "anthropic"
	case "chatgpt":
		return "openai"
	default:
		if _, ok := c.OpenAICompatible[name]; ok {
			return "openai"
		}
		if _, ok := c.AnthropicCompatible[name]; ok {
			return "anthropic"
		}
		return ""
	}
}

// GetAllProviderConfigs returns all known providers, including those with no
// models enabled yet. Callers that require enabled models (e.g. the LLM router)
// filter by len(Models) > 0 at the usage site.
func (c *LLMConfig) GetAllProviderConfigs() []ProviderWithModels {
	result := make([]ProviderWithModels, 0, len(c.allProviderEntries()))
	for _, p := range c.allProviderEntries() {
		result = append(result, ProviderWithModels{
			Name:         p.name,
			ProviderType: c.providerType(p.name),
			APIKey:       p.apiKey,
			BaseURL:      p.baseURL,
			Models:       p.models,
		})
	}
	return result
}

// ResolveDefaultModelProvider looks up the provider that owns DefaultModel.
// DefaultModel may be a bare model name (resolved to the first matching
// provider) or a composite identifier "provider/model" (resolved to the named
// provider). Returns the provider config and the bare model name, or an error
// if DefaultModel is empty or not found in any provider's Models list.
func (c *LLMConfig) ResolveDefaultModelProvider() (ProviderWithModels, string, error) {
	if c.DefaultModel == "" {
		return ProviderWithModels{}, "", errors.New("default_model is not set")
	}

	// Composite default_model: resolve to the named provider + bare model.
	if provider, model, ok := llm.ParseCompositeModelID(c.DefaultModel); ok {
		for _, p := range c.allProviderEntries() {
			if p.name != provider {
				continue
			}
			for _, m := range p.models {
				if m == model {
					return ProviderWithModels{
						Name:         p.name,
						ProviderType: c.providerType(p.name),
						APIKey:       p.apiKey,
						BaseURL:      p.baseURL,
						Models:       p.models,
					}, m, nil
				}
			}
		}
		return ProviderWithModels{}, "", fmt.Errorf("default_model %q not found in provider %q enabled models", model, provider)
	}

	// Bare default_model: first matching provider wins.
	for _, p := range c.allProviderEntries() {
		for _, m := range p.models {
			if m == c.DefaultModel {
				return ProviderWithModels{
					Name:         p.name,
					ProviderType: c.providerType(p.name),
					APIKey:       p.apiKey,
					BaseURL:      p.baseURL,
					Models:       p.models,
				}, m, nil
			}
		}
	}

	return ProviderWithModels{}, "", fmt.Errorf("default_model %q not found in any provider's enabled models", c.DefaultModel)
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
	// Validate default_model
	if cfg.LLM.DefaultModel == "" {
		return errors.New("llm.default_model is not set")
	}

	// Validate at least one provider has models
	hasModels := false
	for _, p := range cfg.LLM.allProviderEntries() {
		if len(p.models) > 0 {
			hasModels = true
			break
		}
	}
	if !hasModels {
		return errors.New("at least one provider must have enabled models")
	}

	// Validate default_model exists in some provider's models list
	_, _, err := cfg.LLM.ResolveDefaultModelProvider()
	if err != nil {
		return err
	}

	// Validate that internal tools are not in tool_policies
	for toolName := range cfg.Security.ToolPolicies {
		if tools.IsInternalTool(toolName) {
			return fmt.Errorf("tool %q is an internal tool and cannot have a custom policy", toolName)
		}
	}

	// Validate goal_loop.verification enum.
	switch cfg.GoalLoop.Verification {
	case "independent", "off":
		// valid
	default:
		return fmt.Errorf(
			"goal_loop.verification %q is not valid; must be one of: independent, off",
			cfg.GoalLoop.Verification,
		)
	}

	return nil
}
