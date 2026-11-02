package core

import (
	"time"

	"github.com/v0lka/c0wrk/core/embeddedllm"
	"github.com/v0lka/c0wrk/core/proxy"
	"github.com/v0lka/c0wrk/core/tools"
	"github.com/v0lka/sp4rk/llm"
	sdktools "github.com/v0lka/sp4rk/tools"
)

// BuilderConfig carries everything the OrchestratorBuilder needs, converted
// from backend config by ToBuilderConfig; it lives in core because core never
// imports backend/config. See specs/architecture/layers.md.
type BuilderConfig struct {
	LLM           BuilderLLMConfig
	Router        BuilderRouterConfig
	Executor      BuilderExecutorConfig
	Security      BuilderSecurityConfig
	Skills        BuilderSkillsConfig
	Search        BuilderSearchConfig
	MCP           BuilderMCPConfig
	Capabilities  BuilderCapabilitiesConfig
	Orchestration BuilderOrchestrationConfig
	GoalLoop      BuilderGoalLoopConfig
	ModelProfiles BuilderModelProfilesConfig
	E2S           BuilderE2SConfig
	ToolLimits    BuilderToolLimitsConfig
	Timeouts      BuilderTimeoutsConfig
	Proxy         proxy.Config

	// EmbeddedLLM wires the embedded model's ensure-loaded transport into the
	// router build (zero value = no embedded supervisor; see the type below).
	EmbeddedLLM BuilderEmbeddedLLMConfig

	// SubscriptionAuth wires the ChatGPT subscription token source into the
	// marked router entry (zero value = the api_key posture; see the type below).
	SubscriptionAuth BuilderSubscriptionAuthConfig

	// ShellExec carries the operator's shell-exec launch-shape override
	// (nil = built-in default; exactly one of BashExec/PoshExec is live per
	// platform). What the declared shell kind drives is specified in
	// specs/domains/tool-system/builtins.md (Shell Invocation Override).
	ShellExec BuilderShellExecConfig

	// MarkitdownPythonPath lazily resolves the managed venv interpreter for
	// vision-assisted document conversion (nil probe or empty result disables
	// vision; plain CLI conversion is unaffected). Injected by the backend as
	// a closure over toolmanager.VenvPythonPath — a machine-local filesystem
	// fact, not a config setting. See specs/contracts/backend-core.md.
	MarkitdownPythonPath func() string

	// ExpandEnvVars resolves ${ENV_VAR} patterns in a string.
	// Injected by the backend so core does not import os/config.
	ExpandEnvVars func(string) string
}

// ---------------------------------------------------------------------------
// Model Profiles profile
// ---------------------------------------------------------------------------

// BuilderModelProfilesConfig mirrors config.ModelProfilesConfig for the subset
// the orchestrator consumes (values copied via ToBuilderConfig; core never
// imports backend/config). Variant semantics: specs/domains/model-profiles.md.
type BuilderModelProfilesConfig struct {
	// Enabled is the master toggle; when off, behavior is identical to the
	// un-profiled baseline.
	Enabled bool

	// EssentialTools narrows the advertised tool set for small models.
	EssentialTools BuilderModelProfilesEssentialConfig

	// Sampling overrides LLM sampling parameters for low-effort generation.
	Sampling BuilderModelProfilesSampling

	// LoopHardening tightens the executor circuit-breaker thresholds.
	LoopHardening BuilderLoopHardening

	// Context applies aggressive context management (tighter compaction,
	// stricter tool-output pruning, larger output reserve).
	Context BuilderModelProfilesContext

	// SystemPrompt applies prompt-simplification variants (the Lite swap).
	SystemPrompt BuilderModelProfilesSystemPromptConfig
}

// ModelProfilesSettingsFromBuilderConfig is the single mapping from the
// builder-layer config onto the runtime ModelProfilesSettings — Build() and the
// backend's runtime refresh both go through it, so the two can never diverge.
func ModelProfilesSettingsFromBuilderConfig(cfg BuilderModelProfilesConfig) ModelProfilesSettings {
	return ModelProfilesSettings{
		Enabled: cfg.Enabled,
		EssentialTools: ModelProfilesEssentialSettings{
			Enabled:             cfg.EssentialTools.Enabled,
			AlwaysPresent:       cfg.EssentialTools.AlwaysPresent,
			CompactDescriptions: cfg.EssentialTools.CompactDescriptions,
		},
		SystemPrompt: ModelProfilesSystemPromptSettings{
			Lite:              cfg.SystemPrompt.Lite,
			FewShot:           cfg.SystemPrompt.FewShot,
			ReasoningScaffold: cfg.SystemPrompt.ReasoningScaffold,
		},
		LoopHardening: ModelProfilesLoopHardeningSettings{
			Enabled:              cfg.LoopHardening.Enabled,
			RepeatNudgeThreshold: cfg.LoopHardening.RepeatNudgeThreshold,
		},
	}
}

// BuilderModelProfilesSystemPromptConfig holds the prompt-simplification variant
// overrides. Lite is the variant master toggle (config.SystemPromptConfig has no
// separate Enabled field, so it is not duplicated here); FewShot and
// ReasoningScaffold additionally require Lite — they are tailored to it.
// Semantics: specs/domains/model-profiles.md.
type BuilderModelProfilesSystemPromptConfig struct {
	// Lite swaps the verbose core directive for the compact Lite counterpart;
	// every shared section is appended UNCHANGED.
	Lite bool

	// FewShot appends the worked-example block (Lite required).
	FewShot bool

	// ReasoningScaffold appends the three-step thought template (Lite required).
	ReasoningScaffold bool
}

// BuilderModelProfilesSampling holds the sampling-variant overrides. Every
// parameter uses zero as the "not set" sentinel: unset fields inherit the
// per-family vendor preset end-to-end, so enabling the variant with no explicit
// values is a behavioral no-op. Per-parameter ranges: specs/domains/model-profiles.md.
type BuilderModelProfilesSampling struct {
	// Enabled gates this variant (in addition to the master ModelProfiles.Enabled).
	Enabled bool

	// Temperature: generation temperature; 0 inherits the vendor preset.
	Temperature float64

	// TopP: nucleus-sampling probability mass, (0, 1]; 0 inherits.
	TopP float64

	// TopK: top-k sampling, >= 1; 0 inherits.
	TopK int

	// RepetitionPenalty: [1, 2]; 0 inherits.
	RepetitionPenalty float64

	// PresencePenalty: the OpenAI-schema anti-repetition lever, (0, 2]; 0
	// inherits (no family preset sets it, so the field is not sent).
	PresencePenalty float64

	// ReasoningEffort: "" (inherit) | "off" | "low" | "medium"; per-request
	// overrides (HandleOptions.ReasoningEffort) still take precedence.
	ReasoningEffort string
}

// BuilderLoopHardening holds the circuit-breaker tightening overrides. Only
// the thresholds present here are overridden; all others keep their baseline.
type BuilderLoopHardening struct {
	// Enabled gates this variant (in addition to the master ModelProfiles.Enabled).
	Enabled bool

	RepeatNudgeThreshold         int
	ParseErrorAbortThreshold     int
	FruitlessNudgeThreshold      int
	FruitlessAbortThreshold      int
	SameToolRepeatNudgeThreshold int
}

// BuilderModelProfilesCompaction holds the compaction-tightening overrides. Zero
// values mean "do not override" — the executor baseline is kept for that knob.
type BuilderModelProfilesCompaction struct {
	// KeepLast overrides the executor sliding-window keep-last count.
	KeepLast int

	// BlockSize overrides the summarization block size.
	BlockSize int

	// TriggerPercent overrides the predictive compaction trigger percentage.
	TriggerPercent int
}

// BuilderModelProfilesContext holds the aggressive context-management overrides:
// tighter compaction, stricter tool-output pruning, larger output token
// reserve. Applied via applyContextManagement.
type BuilderModelProfilesContext struct {
	// Enabled gates this variant (in addition to the master ModelProfiles.Enabled).
	Enabled bool

	// Compaction overrides the executor compaction knobs.
	Compaction BuilderModelProfilesCompaction

	// ToolOutputKeepLastN overrides the executor tool-output pruning depth.
	ToolOutputKeepLastN int

	// OutputTokenReserve overrides the token budget reserved for output.
	OutputTokenReserve int
}

// BuilderModelProfilesEssentialConfig holds the always-present-tool-set narrowing
// settings for the essential-tools variant.
type BuilderModelProfilesEssentialConfig struct {
	// Enabled gates this variant (in addition to the master ModelProfiles.Enabled).
	Enabled bool

	// AlwaysPresent is the allow-list of tool names always exposed when this
	// variant is active. Protected orchestration tools and all MCP tools are
	// always preserved regardless, and the guaranteed set is never trimmed.
	AlwaysPresent []string

	// CompactDescriptions swaps full builtin tool descriptions for one-line
	// compact variants (model-profile essential-tools extension).
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
	ProviderType string   // Go provider type: "anthropic", "openai"
	APIKey       string   // raw value (may contain ${ENV_VAR})
	BaseURL      string   // raw value
	Models       []string // enabled models for this one provider
	// TLSFingerprint is the base64(SHA-256(SPKI DER)) pin: the pin IS the
	// switch (empty = system verification; there is no accept-any mode).
	// See specs/domains/llm-providers.md (TLS pinning, incl. the proxy override).
	TLSFingerprint string
	// OutputTokenReserve overrides the output-token budget for every model of
	// this provider (0 = inherit the global reserve); a per-model
	// llm.models output_limit still wins. See specs/domains/llm-providers.md.
	OutputTokenReserve int
	// TimeoutClass is the llm.<provider>.timeout_class override for the
	// adaptive request budget: "local" | "remote" (empty = infer; the
	// reserved "embedded" entry's class is fixed). See specs/domains/llm-providers.md.
	TimeoutClass string
	// SubscriptionAuth marks the entry as ChatGPT-subscription (browser OAuth)
	// auth; set only by ToBuilderConfig in oauth mode. With the marker off the
	// entry is built byte-for-byte as before the mode existed.
	// See specs/domains/llm-providers.md.
	SubscriptionAuth bool
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
	// RequestTimeout is llm.models.<name>.request_timeout in seconds: a
	// positive value is a FIXED deadline, never escalated; 0 = no opinion
	// (the adaptive budgets govern). See specs/domains/llm-providers.md.
	RequestTimeout int
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

	// Forecast seeds the LLM-backed strategies' predicted reclaim (EWMA-
	// calibrated per compaction); it never affects availability, which is an
	// exact structural verdict. See specs/domains/memory/compaction.md.
	Forecast BuilderCompactionForecast
}

// BuilderCompactionForecast holds the forecast seeds: the expected fraction of
// a summarized block's input tokens its summary occupies. Defaults and
// calibration: specs/domains/memory/compaction.md.
type BuilderCompactionForecast struct {
	SummarizationRatio       float64 // "summarization" + hierarchical middle (default 0.3)
	HierarchicalDistantRatio float64 // hierarchical distant zone (default 0.15)
	HierarchicalMiddleRatio  float64 // hierarchical middle zone (default 0.3)
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

// Mode values of the unified autonomy posture (security.autonomy_mode). Core
// owns this vocabulary; backend/config re-declares the same strings and
// backend/configadapter_test.go pins the two dictionaries against each other.
// See specs/architecture/security-model.md.
const (
	AutonomyModeStandard = "standard" // default: every interactive prompt reaches a human
	AutonomyModeAssisted = "assisted" // the strict judge resolves escalated calls (former Smart Approve)
	AutonomyModeSilent   = "silent"   // unattended: the four silent-mode sub-policies answer
)

// BuilderSecurityConfig holds security settings.
type BuilderSecurityConfig struct {
	InjectionDefenseEnabled bool

	// Groups is the security schema (security.groups): the registry resolves
	// every non-system tool's policy from its capability group alone; the
	// reserved "system" group never appears here. See
	// specs/decisions/024-group-policies.md.
	Groups map[string]BuilderGroupPolicy

	// AutoApproveWorkspaceWrites auto-executes local_write tools whose targets
	// resolve inside the session workspace without user confirmation.
	AutoApproveWorkspaceWrites bool

	// AutonomyMode is the unified posture (standard | assisted | silent); the
	// registry-facing booleans below derive from it. Empty/unknown behaves as
	// standard (the config loader maps unknowns before they ever reach here).
	AutonomyMode string

	// SilentMode is the silent-mode sub-policy container; live only while
	// AutonomyMode is "silent". See specs/architecture/security-model.md.
	SilentMode BuilderSilentModeConfig

	// AgentsMDMaxBytes caps the combined AGENTS.md content injected into the
	// system prompt: 0 = default (65536), negative = no cap.
	AgentsMDMaxBytes int

	// AgentsMDSearchPaths holds extra absolute AGENTS.md file paths searched
	// (in priority order) ahead of the workspace root; missing files are skipped.
	AgentsMDSearchPaths []string
}

// SilentModeEnabled reports the unattended posture (live only in "silent").
func (s BuilderSecurityConfig) SilentModeEnabled() bool {
	return s.AutonomyMode == AutonomyModeSilent
}

// SmartApproveEnabled reports whether the strict judge resolves escalated calls
// (assisted; silent implies it — silent routes them through the judge anyway).
func (s BuilderSecurityConfig) SmartApproveEnabled() bool {
	return s.AutonomyMode == AutonomyModeAssisted || s.AutonomyMode == AutonomyModeSilent
}

// AskUserDisabled reports whether the unattended posture's ask_user sub-policy
// disables the tool ("disable" == core/tools.SilentAskUserDisable, pinned
// against the config enum by backend/configadapter_test.go).
func (s BuilderSecurityConfig) AskUserDisabled() bool {
	return s.SilentModeEnabled() && s.SilentMode.AskUser == tools.SilentAskUserDisable
}

// BuilderGroupPolicy holds one tool group's security policy and, for the
// execute group only, its command blocklist. Policy values are the short
// config enum: "allow", "user_confirm", "deny".
type BuilderGroupPolicy struct {
	Policy    string
	Blocklist []string
}

// BuilderSilentModeConfig is the core mirror of security.silent_mode: the four
// unattended-operation sub-policies (mode strings are the config enum values
// verbatim; live only while AutonomyMode is "silent"; UserConfirm refines
// ToolConfirm for a fail-closed CONFIRM outcome). ApplyDefaults seeds every
// mode before conversion. See specs/architecture/security-model.md.
type BuilderSilentModeConfig struct {
	ToolConfirm string
	UserConfirm string
	StepLimit   string
	AskUser     string
}

// BuilderSkillsConfig holds Agent Skills discovery directories.
type BuilderSkillsConfig struct {
	Dirs []string // absolute paths to skill directories in priority order
}

// BuilderCapabilitiesConfig carries the per-capability activation modes for
// the skill and agent catalogs (MCP servers carry theirs on BuilderMCPServer);
// an entry absent from a map is "auto" in every context. Enum/shape mirroring:
// specs/domains/capabilities.md.
type BuilderCapabilitiesConfig struct {
	Skills map[string]CapabilityModes
	Agents map[string]CapabilityModes
}

// ---------------------------------------------------------------------------
// Search
// ---------------------------------------------------------------------------

// BuilderSearchConfig holds web-search settings.
type BuilderSearchConfig struct {
	Provider string
	APIKey   string // raw value (may contain ${ENV_VAR})
	BaseURL  string // SearXNG instance URL; raw value (may contain ${ENV_VAR}); only used by the searxng provider
}

// ---------------------------------------------------------------------------
// MCP
// ---------------------------------------------------------------------------

// BuilderMCPConfig holds MCP server definitions.
type BuilderMCPConfig struct {
	Servers        map[string]BuilderMCPServer
	DefaultWorkDir string
}

// CapabilityModes is one capability entry's activation modes per execution
// context (CHAT vs CODE): "auto" | "manual" | "disabled"; empty IS "auto".
// See specs/domains/capabilities.md.
type CapabilityModes struct {
	ChatMode string
	CodeMode string
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

	// ChatMode / CodeMode are the per-server activation modes for CHAT / CODE
	// sessions ("auto" | "manual" | "disabled"; empty = "auto"). A server
	// disabled in BOTH contexts is never dialed. See
	// specs/domains/tool-system/mcp-gateway.md.
	ChatMode string
	CodeMode string

	// Timeout bounds this server's initialization handshake (initialize +
	// tools/list). Zero or negative selects the mcp package default (60s).
	Timeout time.Duration

	// CallTimeout bounds a single tools/call invocation. Zero or negative
	// inherits Timeout (which itself defaults when unset).
	CallTimeout time.Duration
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
	// MaxParallelSubagents caps how many subagents run concurrently, shared by
	// the delegate tool and plan waves (single sp4rk RunSubAgentsParallel
	// chokepoint). <= 0 resolves to the Orchestrator default (4).
	MaxParallelSubagents int
}

// BuilderGoalLoopConfig holds goal-loop settings.
type BuilderGoalLoopConfig struct {
	Verification string // "independent" (default) | "off"
	// Suggest carries the goal-mode suggestion gate settings. Enabled is
	// already resolved from the config layer's pointer-bool (nil = on), so
	// core sees a plain effective gate.
	Suggest GoalSuggestSettings
}

// BuilderE2SConfig mirrors config.E2SConfig for the subset core consumes
// (copied via ToBuilderConfig; Enabled mapped from the experimental gate,
// fail-closed). A zero value means "disabled with loop defaults" — the core/e2s
// Config defaults apply. See specs/domains/e2s.md.
type BuilderE2SConfig struct {
	// Enabled is the effective E2S availability (experimental.enabled); when
	// false the mode is rejected fail-closed and the knobs below are inert.
	Enabled bool
	// MaxSteps caps the number of turns (patch+action cycles) per run.
	MaxSteps int
	// StateByteLimit caps the JSON-encoded size of Σ in bytes.
	StateByteLimit int
	// PatchRetries bounds the corrective re-requests for a rejected patch.
	PatchRetries int
	// MaxObservationChars caps the per-turn observation fed back to the model
	// (the char fallback used when no model window is resolved).
	MaxObservationChars int
	// ObservationBudgetTokens is the per-turn observation budget in tokens
	// (token-proportional mode, engaged when the loop resolves a window).
	ObservationBudgetTokens int
	// ObservationFillFraction caps the observation budget as a fraction of
	// the model context window.
	ObservationFillFraction float64
	// RepeatNudgeThreshold / RepeatAbortThreshold are the anti-spin
	// thresholds (identical consecutive actions before a nudge / an abort).
	RepeatNudgeThreshold int
	RepeatAbortThreshold int
	// Tools narrows the E2S available-tool catalog (preset + allow/deny); the
	// filtered catalog is the dispatch contract (filtered-out names are
	// rejected fail-closed at action dispatch). See filterE2SToolsByConfig in
	// core/orchestrator_e2s.go and specs/domains/e2s.md.
	Tools BuilderE2SToolsConfig
}

// E2S tool-catalog preset values (mirrored by backend/config's constants — the
// two sets must stay in sync; unknown values fail closed to the "core" surface).
const (
	// E2SToolsPresetCore keeps the local_read + local_write + execute +
	// remote_read capability groups plus the E2S system plumbing tools.
	E2SToolsPresetCore = "core"
	// E2SToolsPresetAll restores the full former tool surface.
	E2SToolsPresetAll = "all"
)

// BuilderE2SToolsConfig mirrors config.E2SToolsConfig — the E2S tool-catalog
// narrowing knobs (deny wins over the preset and allow; see specs/domains/e2s.md).
type BuilderE2SToolsConfig struct {
	// Preset selects the base catalog: "core" (default) or "all". Any other
	// value is treated as core (fail-closed to the narrower surface).
	Preset string
	// Allow re-includes tools the preset excluded.
	Allow []string
	// Deny removes tools with precedence over the preset and allow.
	Deny []string
}

// ---------------------------------------------------------------------------
// Tool limits
// ---------------------------------------------------------------------------

// BuilderToolLimitsConfig holds configurable limits for built-in tools.
type BuilderToolLimitsConfig struct {
	ReadDefaultLines int

	WebSearchMaxResults int

	// Glob limits (runaway-walk protection): max filesystem entries visited and
	// max matching paths collected per glob walk (0 = unlimited for each).
	GlobMaxEntries int
	GlobMaxResults int

	// GlobLimitsExplicit reports the glob config was explicitly customized
	// (at least one of the three knobs non-default post-ApplyDefaults): it
	// selects the no-fallback glob constructor so an explicit all-zero config
	// ("0 disables" on all three knobs) is honored verbatim instead of the
	// zero-struct→defaults fallback for a never-populated config.
	// See specs/domains/tool-system/builtins.md (Limits Configuration).
	GlobLimitsExplicit bool

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
	BashMaxTimeout  int
	BashWaitDelay   int
	RipgrepTimeout  int
	GlobTimeout     int // seconds; wall-clock budget for a single glob walk (0 = no timeout)
	ToolCallTimeout int // seconds; ceiling for a single tool call in the ReAct loop (0 = disabled). Installed on the main conductor executor and every subagent executor.
	// ToolCallTimeoutExemptTools replaces the per-tool-call ceiling's exempt
	// set (tool NAMES threaded to the main and every subagent executor):
	// nil keeps sp4rk's built-in default; an explicit list — possibly empty —
	// replaces it wholesale.
	ToolCallTimeoutExemptTools []string
	WebFetchTimeout            int
	WebFetchProxyTimeout       int // seconds; per-attempt web fetch timeout when the proxy is enabled
	WebFetchRetries            int // retry count (not seconds); each retry doubles the active web fetch timeout
	WebSearchTimeout           int
	LLMRequestTimeout          int
	// AdaptiveBudgetEnabled mirrors timeouts.adaptive_budget.enabled (default
	// true): false installs no budget transport (the legacy fixed regime);
	// true lets a positive LLMRequestTimeout act as a fixed override while 0
	// defers to the adaptive budgets. See specs/domains/llm-providers.md.
	AdaptiveBudgetEnabled bool
}

// ---------------------------------------------------------------------------
// Embedded LLM
// ---------------------------------------------------------------------------

// BuilderEmbeddedLLMConfig carries the embedded local model's runtime seam into
// the router build: the provider entry named ProviderName gets an
// embeddedllm.EnsureLoadedTransport on its ProviderEntry.HTTPClient (a request
// to an idle-unloaded model transparently starts it; a completed response
// restarts the idle budget) and is the only entry built with
// llm.ReasoningWireChatTemplateKwargs (the pinned llama.cpp fork reads
// enable_thinking exclusively from chat_template_kwargs). This is the
// PER-BUILD form; OrchestratorBuilder also holds one as its default
// (SetEmbeddedLLM) so per-session routers — whose BuilderConfig is converted
// where the supervisor is unreachable — still get the seam. See
// specs/domains/llm-providers.md and specs/domains/embedded-llm.md.
type BuilderEmbeddedLLMConfig struct {
	// ProviderName is the router entry the transport guards (backend-injected;
	// deliberately not hardcoded — an unset name guards nothing).
	ProviderName string

	// Loader is the supervisor seam; nil disables the transport. In production
	// it is backend.embeddedLoaderRef — resolved lazily at call time because
	// the router is built before the embedded subsystem exists and the
	// per-session conversion holds the backend config lock (taking the
	// subsystem's lock there would invert the documented lock order).
	// See specs/domains/embedded-llm.md.
	Loader embeddedllm.Loader

	// LoadWaitTimeout bounds one request's wait for a cold load;
	// <= 0 → embeddedllm.DefaultLoadWaitTimeout.
	LoadWaitTimeout time.Duration
}

// guards reports whether the ensure-loaded transport applies to the provider
// entry named name. Both halves are required: a loader with no name would guard
// nothing, and a name with no loader must not install a pass-through wrapper
// that shadows the router-level client for no reason.
func (c BuilderEmbeddedLLMConfig) guards(name string) bool {
	return c.Loader != nil && c.ProviderName != "" && c.ProviderName == name
}

// BuilderSubscriptionAuthConfig carries the subscription-auth token source into
// the router build: the entry whose BuilderProviderConfig carries
// SubscriptionAuth gets TokenSource + RequireStreaming, so the static APIKey
// never reaches the wire. This is the PER-BUILD form; OrchestratorBuilder also
// holds one as its default (SetSubscriptionTokenSource) covering the
// per-session routers. See specs/domains/llm-providers.md.
type BuilderSubscriptionAuthConfig struct {
	// ProviderName is the router entry the token source serves (backend-
	// injected; deliberately not hardcoded — an unset name serves nothing).
	ProviderName string

	// TokenSource supplies per-request OAuth bearer credentials (the backend's
	// providerauth.TokenManager in production); nil means signed out — the
	// entry stays listed but every request fails with the actionable
	// "sign in with ChatGPT" error.
	TokenSource llm.TokenSource
}

// serves reports whether this seam supplies credentials to the provider entry
// named name. Both halves are required, mirroring
// BuilderEmbeddedLLMConfig.guards: a source with no name would serve nothing,
// and a name with no source is the signed-out posture, not a guess.
func (c BuilderSubscriptionAuthConfig) serves(name string) bool {
	return c.TokenSource != nil && c.ProviderName != "" && c.ProviderName == name
}

// BuilderShellExecConfig carries the operator's shell-exec launch-shape
// override per tool. A nil entry means "built-in default launch shape" —
// the normal posture. See BuilderConfig.ShellExec.
type BuilderShellExecConfig struct {
	BashExec *sdktools.ShellInvocation
	PoshExec *sdktools.ShellInvocation
}
