package core

import (
	"github.com/v0lka/c0wrk/core/proxy"
	"github.com/v0lka/sp4rk/llm"
)

// BuilderConfig holds all configuration that the OrchestratorBuilder needs.
// It is defined in core so that core never imports backend/config.
// The backend layer constructs a BuilderConfig from *config.Config via ToBuilderConfig.
type BuilderConfig struct {
	LLM           BuilderLLMConfig
	Router        BuilderRouterConfig
	Executor      BuilderExecutorConfig
	Security      BuilderSecurityConfig
	Skills        BuilderSkillsConfig
	Search        BuilderSearchConfig
	MCP           BuilderMCPConfig
	Orchestration BuilderOrchestrationConfig
	GoalLoop      BuilderGoalLoopConfig
	SmallLLM      BuilderSmallLLMConfig
	ToolLimits    BuilderToolLimitsConfig
	Timeouts      BuilderTimeoutsConfig
	Proxy         proxy.Config

	// ExpandEnvVars resolves ${ENV_VAR} patterns in a string.
	// Injected by the backend so core does not import os/config.
	ExpandEnvVars func(string) string
}

// ---------------------------------------------------------------------------
// Small LLM profile
// ---------------------------------------------------------------------------

// BuilderSmallLLMConfig mirrors config.SmallLLMConfig for the subset of the
// profile that core consumes. core never imports backend/config, so values are
// copied via ToBuilderConfig. Only the master toggle and the variants wired in
// this step (Sampling, LoopHardening) are represented.
type BuilderSmallLLMConfig struct {
	// Enabled is the master toggle. When false every variant sub-toggle is
	// ignored and behavior is identical to the un-profiled baseline.
	Enabled bool

	// EssentialTools narrows the conductor's advertised tool set to an
	// always-present subset to reduce per-prompt schema overhead for small
	// models.
	EssentialTools BuilderSmallLLMEssentialConfig

	// Sampling overrides LLM sampling parameters (temperature, top_p,
	// reasoning effort) for more deterministic, lower-effort generation.
	Sampling BuilderSmallLLMSampling

	// LoopHardening tightens the executor circuit-breaker thresholds so a
	// small model that repeats itself or makes no progress is caught sooner.
	LoopHardening BuilderLoopHardening

	// SystemPrompt applies prompt-simplification variants (currently the Lite
	// core-directive swap) to shrink the system prompt injected for a small
	// model. When Lite is active, buildSystemPromptWith trades the verbose
	// OrchestratorSystem directive for the compact OrchestratorSystemLite.
	SystemPrompt BuilderSmallLLMSystemPromptConfig
}

// BuilderSmallLLMSystemPromptConfig holds the prompt-simplification variant
// overrides for the small-LLM profile. Lite is the variant master toggle (it
// mirrors config.SystemPromptConfig, which has no separate Enabled field, so
// Enabled is not duplicated here). Lite swaps the core directive, FewShot
// appends worked ReAct examples, ReasoningScaffold appends the
// structured-thought template. Each is only honored when the variant is active
// (master SmallLLM.Enabled on AND Lite on); FewShot and ReasoningScaffold
// additionally require Lite, since the examples and scaffold are tailored to
// the lite directive.
type BuilderSmallLLMSystemPromptConfig struct {
	// Lite swaps the verbose OrchestratorSystem core directive for the compact
	// OrchestratorSystemLite directive. The shared sections (family overlay,
	// verification mandate, injection defense, workspace, env, AGENTS.md,
	// skills) are appended UNCHANGED.
	Lite bool

	// FewShot appends the OrchestratorLiteFewShot worked-example block. Only
	// applied when Lite is active.
	FewShot bool

	// ReasoningScaffold appends the OrchestratorLiteScaffold three-step
	// thought template. Only applied when Lite is active.
	ReasoningScaffold bool
}

// BuilderSmallLLMSampling holds the sampling-variant overrides.
type BuilderSmallLLMSampling struct {
	// Enabled gates this variant (in addition to the master SmallLLM.Enabled).
	Enabled bool

	// Temperature sets generation temperature (lower = more deterministic).
	// Applied via the LLM router's SamplingFunc when enabled.
	Temperature float64

	// TopP sets nucleus-sampling probability mass. NOTE: the sp4rk
	// llm.ChatRequest does not expose a TopP field and the router never
	// consumes TopP from the SamplingFunc, so this value is currently carried
	// for forward compatibility and logged but not applied to the request
	// without a sp4rk change. Temperature is the active lever.
	TopP float64

	// ReasoningEffort controls reasoning depth: "" (inherit) | "off" | "low" |
	// "medium". When non-empty it seeds the builder-level default; per-request
	// overrides (HandleOptions.ReasoningEffort) still take precedence.
	ReasoningEffort string
}

// BuilderLoopHardening holds the circuit-breaker tightening overrides. Only
// the thresholds present here are overridden; all others keep their baseline.
type BuilderLoopHardening struct {
	// Enabled gates this variant (in addition to the master SmallLLM.Enabled).
	Enabled bool

	RepeatNudgeThreshold         int
	ParseErrorAbortThreshold     int
	FruitlessNudgeThreshold      int
	FruitlessAbortThreshold      int
	SameToolRepeatNudgeThreshold int
}

// BuilderSmallLLMEssentialConfig holds the always-present-tool-set narrowing
// settings for the essential-tools variant.
type BuilderSmallLLMEssentialConfig struct {
	// Enabled gates this variant (in addition to the master SmallLLM.Enabled).
	Enabled bool

	// AlwaysPresent is the allow-list of tool names always exposed when this
	// variant is active. finish and all MCP tools are always preserved
	// regardless.
	AlwaysPresent []string

	// MaxTools caps the total number of tools exposed after narrowing.
	MaxTools int
}

// ---------------------------------------------------------------------------
// LLM
// ---------------------------------------------------------------------------

// BuilderLLMConfig holds the LLM provider settings the builder needs.
type BuilderLLMConfig struct {
	DefaultModel    string                           // global, cross-provider default model
	ProviderConfigs map[string]BuilderProviderConfig // key = provider name ("anthropic", "openai_compatible", …)

	Retry BuilderRetryConfig

	// Model metadata overrides keyed by model name.
	Models map[string]BuilderModelOverride
}

// BuilderProviderConfig holds configuration for a single LLM provider.
type BuilderProviderConfig struct {
	ProviderType string   // Go provider type: "anthropic", "openai"
	APIKey       string   // raw value (may contain ${ENV_VAR})
	BaseURL      string   // raw value
	Models       []string // enabled models for this one provider
}

// DefaultProviderName returns the logical name of the provider that owns DefaultModel.
// Returns empty string if no provider configs exist or DefaultModel is not found.
func (c BuilderLLMConfig) DefaultProviderName() string {
	for name, pc := range c.ProviderConfigs {
		for _, m := range pc.Models {
			if m == c.DefaultModel {
				return name
			}
		}
	}
	return ""
}

// BuilderRetryConfig configures LLM retry behaviour.
type BuilderRetryConfig struct {
	MaxRetries     int
	InitialBackoff string // duration string, e.g. "1s"
	MaxBackoff     string // duration string, e.g. "30s"
}

// BuilderModelOverride allows overriding built-in model metadata.
type BuilderModelOverride struct {
	ContextWindow int
	OutputLimit   int
	TokenizerType string
	Family        string
	Protocol      string
	Capabilities  *llm.ModelCapabilities
}

// ---------------------------------------------------------------------------
// Router
// ---------------------------------------------------------------------------

// BuilderRouterConfig holds router settings.
type BuilderRouterConfig struct {
	HistoryWindow int
}

// ---------------------------------------------------------------------------
// Executor
// ---------------------------------------------------------------------------

// BuilderExecutorConfig holds executor-level settings.
type BuilderExecutorConfig struct {
	MaxRetries         int
	OutputTokenReserve int
	Compaction         BuilderCompactionConfig
	ToolResultBudget   BuilderToolResultBudget
	ToolOutputPruning  BuilderToolOutputPruning
	HistoryMutation    BuilderHistoryMutation
	CircuitBreaker     BuilderCircuitBreaker
}

// BuilderHistoryMutation configures regular history mutation (tool result
// eviction to cache references, step-status eviction, dedup).
type BuilderHistoryMutation struct {
	ToolResultEvictionStep int
	EvictStepStatus        bool
	DedupRepeatedReads     bool
}

// BuilderCompactionConfig holds compaction settings.
type BuilderCompactionConfig struct {
	SlidingWindow       BuilderSlidingWindow
	Summarization       BuilderSummarization
	Hierarchical        BuilderHierarchical
	Thresholds          BuilderCompactionThresholds
	MaxSummarizeTokens  int
	ObservationTruncate int
	SafetyMarginPercent int
}

// BuilderSlidingWindow configures sliding-window compaction.
type BuilderSlidingWindow struct {
	KeepFirst int
	KeepLast  int
}

// BuilderSummarization configures summarization compaction.
type BuilderSummarization struct {
	BlockSize int
	KeepLast  int
}

// BuilderHierarchical configures hierarchical compaction ratios.
type BuilderHierarchical struct {
	EnabledAboveSteps int // step count at which hierarchical compaction activates
	DistantRatio      float64
	MiddleRatio       float64
	RecentRatio       float64
}

// BuilderCompactionThresholds defines context-window usage thresholds.
type BuilderCompactionThresholds struct {
	PredictivePercent int
	WarningPercent    int
	EmergencyPercent  int
	PreWarningPercent int
}

// BuilderToolResultBudget configures tool-result size limits.
type BuilderToolResultBudget struct {
	HardCapTokens   int
	MaxFillFraction float64
	CacheTTLSeconds int // TTL in seconds for MCP tool cache entries
}

// BuilderToolOutputPruning configures selective pruning of old tool outputs.
type BuilderToolOutputPruning struct {
	KeepLastN        int
	ProtectedTools   []string
	ThresholdPercent float64 // Context fill % below which pruning is skipped (0 = always prune)
}

// BuilderCircuitBreaker holds circuit-breaker thresholds.
type BuilderCircuitBreaker struct {
	RepeatNudgeThreshold         int
	RepeatAbortThreshold         int
	TruncationAbortThreshold     int
	ParseErrorAbortThreshold     int
	FruitlessNudgeThreshold      int
	FruitlessAbortThreshold      int
	FruitlessMaxResultLen        int
	SameToolRepeatNudgeThreshold int
	SameToolRepeatAbortThreshold int
	SameToolResultSizeDelta      int
}

// ---------------------------------------------------------------------------
// Security
// ---------------------------------------------------------------------------

// BuilderSecurityConfig holds security settings.
type BuilderSecurityConfig struct {
	JudgeModel              string
	InjectionDefenseEnabled bool
	ToolPolicies            map[string]BuilderToolPolicy
	DefaultPolicy           string

	// AutoApproveWorkspaceWrites, when true, auto-executes file write tools
	// within the session workspace without user confirmation.
	AutoApproveWorkspaceWrites bool

	// AgentsMDMaxBytes caps the AGENTS.md content read from the workspace before
	// it is injected into the system prompt. 0 means use the default (65536).
	// A negative value disables the cap entirely. The cap applies to the
	// combined content of all AGENTS.md sources.
	AgentsMDMaxBytes int

	// AgentsMDSearchPaths holds extra absolute paths (outside the workspace)
	// to search for AGENTS.md files, in priority order. Content from these
	// paths is concatenated ahead of the workspace-root AGENTS.md. Each path
	// points directly at an AGENTS.md file. Missing files are silently skipped.
	AgentsMDSearchPaths []string
}

// BuilderToolPolicy holds per-tool policy and optional blacklist.
type BuilderToolPolicy struct {
	Policy    string
	Blacklist []string
}

// BuilderSkillsConfig holds Agent Skills discovery directories.
type BuilderSkillsConfig struct {
	Dirs []string // absolute paths to skill directories in priority order
}

// ---------------------------------------------------------------------------
// Search
// ---------------------------------------------------------------------------

// BuilderSearchConfig holds web-search settings.
type BuilderSearchConfig struct {
	Provider string
	APIKey   string // raw value (may contain ${ENV_VAR})
}

// ---------------------------------------------------------------------------
// MCP
// ---------------------------------------------------------------------------

// BuilderMCPConfig holds MCP server definitions.
type BuilderMCPConfig struct {
	Servers        map[string]BuilderMCPServer
	DefaultWorkDir string
}

// BuilderMCPServer defines how to connect to an MCP server.
type BuilderMCPServer struct {
	Transport string
	Command   string
	Args      []string
	Env       map[string]string
	URL       string
	Headers   map[string]string
	WorkDir   string
}

// ---------------------------------------------------------------------------
// Orchestration
// ---------------------------------------------------------------------------

// BuilderOrchestrationConfig holds orchestration-level limits.
type BuilderOrchestrationConfig struct {
	MaxDependencyContextChars int
	MaxJudgeCacheSize         int
}

// BuilderGoalLoopConfig holds goal-loop settings.
type BuilderGoalLoopConfig struct {
	Verification string // "independent" (default) | "off"
}

// ---------------------------------------------------------------------------
// Tool limits
// ---------------------------------------------------------------------------

// BuilderToolLimitsConfig holds configurable limits for built-in tools.
type BuilderToolLimitsConfig struct {
	ReadDefaultLines int

	WebSearchMaxResults int

	// Per-tool Stage 1 truncation (line/byte-based, applied before token budget).
	PerToolTruncation map[string]BuilderToolTruncationConfig
}

// BuilderToolTruncationConfig — per-tool truncation settings.
type BuilderToolTruncationConfig struct {
	MaxLines int
	MaxBytes int
}

// ---------------------------------------------------------------------------
// Timeouts
// ---------------------------------------------------------------------------

// BuilderTimeoutsConfig holds timeout values (in seconds).
type BuilderTimeoutsConfig struct {
	BashMaxTimeout    int
	BashWaitDelay     int
	RipgrepTimeout    int
	WebFetchTimeout   int
	WebSearchTimeout  int
	LLMRequestTimeout int
}
