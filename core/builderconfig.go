package core

// BuilderConfig holds all configuration that the OrchestratorBuilder needs.
// It is defined in core so that core never imports backend/config.
// The backend layer constructs a BuilderConfig from *config.Config via ToBuilderConfig.
type BuilderConfig struct {
	LLM           BuilderLLMConfig
	Reasoning     BuilderReasoningConfig
	Router        BuilderRouterConfig
	Executor      BuilderExecutorConfig
	Security      BuilderSecurityConfig
	Skills        BuilderSkillsConfig
	Search        BuilderSearchConfig
	MCP           BuilderMCPConfig
	Orchestration BuilderOrchestrationConfig
	ToolLimits    BuilderToolLimitsConfig
	Timeouts      BuilderTimeoutsConfig
	Proxy         BuilderProxyConfig

	// ExpandEnvVars resolves ${ENV_VAR} patterns in a string.
	// Injected by the backend so core does not import os/config.
	ExpandEnvVars func(string) string
}

// ---------------------------------------------------------------------------
// LLM
// ---------------------------------------------------------------------------

// BuilderLLMConfig holds the LLM provider settings the builder needs.
type BuilderLLMConfig struct {
	ActiveProvider string // e.g. "anthropic", "gemini", "lmstudio", "openai_compatible", "chatgpt"

	// Resolved active-provider fields (output of GetActiveProviderConfig).
	ProviderType string // Go provider type: "anthropic", "gemini", "lmstudio", "openai"
	APIKey       string // raw value (may contain ${ENV_VAR})
	BaseURL      string // raw value
	Model        string // active model name

	Retry BuilderRetryConfig

	// Model metadata overrides keyed by model name.
	Models map[string]BuilderModelOverride

	// Per-provider raw configs used only by ListProviderModels.
	ChatGPTAPIKey       string
	OpenAICompatBaseURL string
	OpenAICompatAPIKey  string
	LMStudioBaseURL     string
	LMStudioAPIKey      string
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
}

// ---------------------------------------------------------------------------
// Reasoning
// ---------------------------------------------------------------------------

// BuilderReasoningConfig holds reasoning/thinking settings.
type BuilderReasoningConfig struct {
	// BaseEffort is the default reasoning effort level for primary agents.
	// Valid values: "off", "minimal", "low", "medium", "high", "maximum".
	// Empty string defaults to "high" when the model supports reasoning.
	BaseEffort string

	// RoleOverrides allows overriding the reasoning effort for specific agent roles.
	// Keys are role names (e.g. "researcher", "coder"), values are ReasoningEffort levels.
	RoleOverrides map[string]string
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
	MaxReactSteps      int
	MaxRetries         int
	OutputTokenReserve int
	Compaction         BuilderCompactionConfig
	ToolResultBudget   BuilderToolResultBudget
	ToolOutputPruning  BuilderToolOutputPruning
	CircuitBreaker     BuilderCircuitBreaker
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
	DistantRatio float64
	MiddleRatio  float64
	RecentRatio  float64
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
	MaxHistoryMessages        int
	MaxDependencyContextChars int
	MaxJudgeCacheSize         int
	MaxPlannerExploreSteps    int // Max steps for planner exploration. Default: 7
}

// ---------------------------------------------------------------------------
// Tool limits
// ---------------------------------------------------------------------------

// BuilderToolLimitsConfig holds configurable limits for built-in tools.
type BuilderToolLimitsConfig struct {
	ReadDefaultLines     int
	ReadMaxLineLength    int
	ReadMaxBytes         int

	RipgrepMaxResults    int
	RipgrepMaxLineLength int
	GlobMaxResults       int

	WebSearchMaxResults int
	WebFetchMaxBodySize int

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
	BashMaxTimeout   int
	BashWaitDelay    int
	RipgrepTimeout   int
	WebFetchTimeout  int
	WebSearchTimeout int
}

// ---------------------------------------------------------------------------
// Proxy
// ---------------------------------------------------------------------------

// BuilderProxyConfig holds HTTP/HTTPS proxy settings.
type BuilderProxyConfig struct {
	Enabled    bool
	URL        string   // env-expanded proxy URL
	BypassList []string // hostnames/IPs to skip proxy
	TLSCertDir string   // env-expanded path to custom CA certs directory
}
