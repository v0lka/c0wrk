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

	// MarkitdownPythonPath lazily resolves the managed venv interpreter that
	// can `import markitdown` (toolmanager.VenvPythonPath). It enables
	// vision-assisted document conversion: the read_file document wrapper
	// invokes it when initializing its markitdown converter, which then runs
	// an embedded Python driver (instead of the CLI) when the active model is
	// vision-capable. The lazy probe exists because the tool-manager installs
	// the venv asynchronously after app startup — probing at builder creation
	// would permanently disable vision on fresh installs. A nil probe or an
	// empty result disables vision-assisted conversion; plain CLI conversion
	// is unaffected. Injected by the backend as a closure over
	// toolmanager.VenvPythonPath — a machine-local filesystem fact, not a
	// runtime-editable setting.
	MarkitdownPythonPath func() string

	// ExpandEnvVars resolves ${ENV_VAR} patterns in a string.
	// Injected by the backend so core does not import os/config.
	ExpandEnvVars func(string) string

	// JudgeProviderHook is a TEST-ONLY observation point for judge binding:
	// when non-nil, newJudgeForProvider invokes it with the provider each
	// judge is about to be bound to (the shared registry's rebuild and every
	// session syncer) and builds the judge from the returned provider. It
	// exists because ToolJudge does not expose its provider, so tests assert
	// which provider a binding resolved to through this hook instead of
	// pointer identity alone. Returning the argument unchanged preserves
	// production behavior; returning nil suppresses the judge exactly like a
	// provider-less config. Production leaves it nil.
	JudgeProviderHook func(llm.Provider) llm.Provider
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

	// Context applies aggressive context management: tighter compaction,
	// stricter tool-output pruning, and a larger output token reserve.
	Context BuilderSmallLLMContext

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

// BuilderSmallLLMSampling holds the sampling-variant overrides. Every
// parameter uses zero as the "not set" sentinel: an unset field inherits the
// per-family vendor preset (prompt.DefaultSampling) instead of clobbering it,
// so enabling the variant with no explicit values is a behavioral no-op.
type BuilderSmallLLMSampling struct {
	// Enabled gates this variant (in addition to the master SmallLLM.Enabled).
	Enabled bool

	// Temperature sets generation temperature (lower = more deterministic).
	// 0 (unset) inherits the vendor preset; positive values override it via
	// the LLM router's SamplingFunc when the variant is enabled.
	Temperature float64

	// TopP sets nucleus-sampling probability mass. 0 (unset) inherits the
	// vendor preset; values in (0, 1] override it via the router's
	// SamplingFunc when the variant is enabled.
	TopP float64

	// TopK sets top-k sampling. 0 (unset) inherits the vendor preset; values
	// >= 1 override it via the router's SamplingFunc when the variant is
	// enabled.
	TopK int

	// RepetitionPenalty penalizes repeated tokens. 0 (unset) inherits the
	// vendor preset; values in [1, 2] override it via the router's
	// SamplingFunc when the variant is enabled.
	RepetitionPenalty float64

	// PresencePenalty penalizes tokens already present in the context — the
	// OpenAI-schema anti-repetition lever (Qwen card: 0–2, instruct default
	// 1.5; higher values increase language mixing). 0 (unset) inherits the
	// vendor preset (no family preset sets it, so the field is not sent);
	// values in (0, 2] override it via the router's SamplingFunc when the
	// variant is enabled.
	PresencePenalty float64

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

// BuilderSmallLLMCompaction holds the compaction-tightening overrides. Zero
// values mean "do not override" — the executor baseline is kept for that knob.
type BuilderSmallLLMCompaction struct {
	// KeepLast overrides the executor sliding-window keep-last count.
	KeepLast int

	// BlockSize overrides the summarization block size.
	BlockSize int

	// TriggerPercent overrides the predictive compaction trigger percentage.
	TriggerPercent int
}

// BuilderSmallLLMContext holds the aggressive context-management overrides:
// tighter compaction, stricter tool-output pruning, larger output token
// reserve. Applied via applyContextManagement.
type BuilderSmallLLMContext struct {
	// Enabled gates this variant (in addition to the master SmallLLM.Enabled).
	Enabled bool

	// Compaction overrides the executor compaction knobs.
	Compaction BuilderSmallLLMCompaction

	// ToolOutputKeepLastN overrides the executor tool-output pruning depth.
	ToolOutputKeepLastN int

	// OutputTokenReserve overrides the token budget reserved for output.
	OutputTokenReserve int
}

// BuilderSmallLLMEssentialConfig holds the always-present-tool-set narrowing
// settings for the essential-tools variant.
type BuilderSmallLLMEssentialConfig struct {
	// Enabled gates this variant (in addition to the master SmallLLM.Enabled).
	Enabled bool

	// AlwaysPresent is the allow-list of tool names always exposed when this
	// variant is active. Protected orchestration tools and all MCP tools are
	// always preserved regardless, and the guaranteed set is never trimmed.
	AlwaysPresent []string

	// MaxTools caps the router-matched slots: at most
	// maxTools − len(guaranteed) matched tools are kept, where guaranteed =
	// always-present ∪ protected ∪ MCP. The guaranteed set itself is never
	// trimmed (validation rejects configs where it alone exceeds MaxTools).
	MaxTools int

	// CompactDescriptions swaps full builtin tool descriptions for one-line
	// compact variants (small-LLM essential-tools extension).
	CompactDescriptions bool
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
	ProviderType string   // Go provider type: "anthropic", "openai", or managed subscription profile
	APIKey       string   // raw value (may contain ${ENV_VAR})
	BaseURL      string   // raw value
	Models       []string // enabled models for this one provider
	// TokenResolver is supplied by the host for managed subscriptions. It is
	// deliberately absent from serialized config and contains no static secret.
	TokenResolver llm.AccessTokenResolver
	// AccountHeader configures the documented account-routing header for a
	// managed provider; it is public metadata, never a token.
	AccountHeader string
	// OutputTokenReserve overrides the output-token budget for every model of
	// this provider (0 = inherit the global executor.output_token_reserve).
	// It is seeded into the model-registry overrides as ModelMetadata.OutputLimit,
	// so it drives both the context-window reserve and the executor MaxTokens
	// ceiling. A per-model llm.models output_limit still wins over it.
	OutputTokenReserve int
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
	VerifyOnEdit       BuilderVerifyOnEditConfig
}

// BuilderVerifyOnEditConfig mirrors config.VerifyOnEditConfig: the
// verify-on-edit settings for mechanical edit verification. Zero value =
// disabled.
type BuilderVerifyOnEditConfig struct {
	Enabled        bool
	Command        string
	Timeout        string // Go duration string; empty/invalid falls back to 2m
	MaxOutputChars int    // 0 falls back to agent.DefaultVerifyOnEditCap
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

	// ManualTargetPercent is the target context fill (in % of the context
	// window) that user-triggered manual compaction aims to reach. Zero means
	// "unset" — the consumer falls back to 30.
	ManualTargetPercent int
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

	// Groups maps tool-group names (the sdktools.Group* values) to their
	// policy. This is the security schema (security.groups in config.yaml):
	// the registry resolves every non-system tool's policy from its
	// capability group alone — per-tool policy overrides do not exist.
	// The reserved "system" group is not configurable and never appears here.
	Groups map[string]BuilderGroupPolicy

	// AutoApproveWorkspaceWrites, when true, auto-executes local_write tools
	// whose targets resolve inside the session workspace without user
	// confirmation.
	AutoApproveWorkspaceWrites bool

	// SmartApprove enables strict automatic judging only after a call resolves
	// to PolicyUserConfirm (or a soft escalation under PolicyAlwaysAllow) and
	// existing workspace auto-approval did not allow it.
	SmartApprove bool

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

// BuilderGroupPolicy holds one tool group's security policy and, for the
// execute group only, its command blacklist. Policy values are the short
// config enum: "allow", "user_confirm", "deny".
type BuilderGroupPolicy struct {
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
	// MaxRedelegationDepth caps recursive delegation when allow_redelegate is
	// true (ASI07-R6). Default 2.
	MaxRedelegationDepth int
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
