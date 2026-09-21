package config

import (
	"sort"
	"strings"

	"github.com/v0lka/c0wrk/core/e2s"
	"github.com/v0lka/c0wrk/core/vectorindex"
)

// defaultProtectedTools is the default list of tools whose output is always preserved during pruning.
var defaultProtectedTools = []string{"store_fact", "search_facts"}

// defaultSkillDirs is the default list of skill discovery directories used when
// the `skills.dirs` config key is omitted. Order is precedence: the c0wrk
// global directory (`~/.c0wrk/.agents/skills`) outranks the user's
// `~/.agents/skills`, so a c0wrk-managed skill wins over a same-named user
// skill. The current project's `.agents/skills` directory is always scanned
// automatically (see core/builder.go) and does NOT need to be listed here.
var defaultSkillDirs = []string{
	"~/.c0wrk/.agents/skills",
	"~/.agents/skills",
}

// defaultAgentDirs is the default list of Subagent Profile discovery
// directories used when the `agents.dirs` config key is omitted. Mirrors
// defaultSkillDirs for AGENT.md files: the c0wrk global directory
// (`~/.c0wrk/.agents/agents`) outranks the user's `~/.agents/agents`. The
// current project's `.agents/agents` directory is always scanned
// automatically (see core/builder.go).
var defaultAgentDirs = []string{
	"~/.c0wrk/.agents/agents",
	"~/.agents/agents",
}

// defaultModelProfilesAlwaysPresent is the default always-present tool allow-list
// exposed when the model-profile essential-tools variant is active. It balances a
// minimal schema footprint against enough capability to navigate, edit,
// search, and finalize tasks, and it pins the tools the ReAct loop itself
// depends on — reading user attachments and activated-skill resources, and
// paging through a truncated tool result (read_attachment, read_skill_resource,
// tool_result_read) plus the step checklist (update_checklist) — so an active
// profile never leaves a session unable to finish its loop. MCP-backed tools
// are layered on separately at runtime.
var defaultModelProfilesAlwaysPresent = []string{
	"read_file",
	"read_attachment",
	"read_skill_resource",
	"tool_result_read",
	"write_file",
	"edit_file",
	"list_directory",
	"glob",
	"ripgrep",
	"bash_exec",
	"posh_exec",
	"semantic_search",
	"store_fact",
	"search_facts",
	"update_checklist",
	"ask_user",
	"finish",
}

// ApplyDefaults sets default values for zero-value fields in the configuration.
func ApplyDefaults(cfg *Config) {
	// Log level defaults to INFO — the production default. DEBUG is verbose
	// (per-step reasoning, raw tool payloads) and is opt-in via config.yaml.
	if cfg.LogLevel == "" {
		cfg.LogLevel = "INFO"
	}

	// Skills discovery defaults (nil => apply defaults; empty slice => user
	// explicitly opted out of base dirs, keep it empty).
	if cfg.Skills.Dirs == nil {
		cfg.Skills.Dirs = append([]string(nil), defaultSkillDirs...)
	}

	// Subagent Profile discovery defaults (nil => apply defaults; empty slice
	// => user explicitly opted out of base dirs, keep it empty). Mirrors skills.
	if cfg.Agents.Dirs == nil {
		cfg.Agents.Dirs = append([]string(nil), defaultAgentDirs...)
	}

	// Subagent concurrency cap. 4 is a safe default: enough parallelism to
	// overlap independent work, few enough to avoid a burst of simultaneous
	// LLM calls / tool executions and the event-rate spike they cause.
	// Any value <= 0 resolves to the default (see AgentsConfig
	// MaxParallelSubagents) so a negative typo can never silently mean
	// "unlimited" downstream — the conductor treats a non-positive cap as
	// no cap at all.
	if cfg.Agents.MaxParallelSubagents <= 0 {
		cfg.Agents.MaxParallelSubagents = 4
	}

	// LLM retry defaults — keep this in sync with the sp4rk Router defaults
	// (github.com/v0lka/sp4rk/llm/router.go) so config-driven and code-driven values agree.
	// Retries cover transient failures: HTTP 429/502/503/529 and network blips.
	if cfg.LLM.Retry.MaxRetries == 0 {
		cfg.LLM.Retry.MaxRetries = 3
	}
	if cfg.LLM.Retry.InitialBackoff == "" {
		cfg.LLM.Retry.InitialBackoff = "1s"
	}
	if cfg.LLM.Retry.MaxBackoff == "" {
		cfg.LLM.Retry.MaxBackoff = "30s"
	}

	// Executor defaults
	if cfg.Executor.MaxRetries == 0 {
		cfg.Executor.MaxRetries = 2
	}
	if cfg.Executor.OutputTokenReserve == 0 {
		// 8192: modern coding/reasoning models regularly emit multi-thousand-
		// token tool-call replies; a smaller reserve risks truncated responses
		// and premature context-window overflow aborts. Only affects the
		// context-window validation budget (input side), so the cost on large
		// windows is negligible.
		cfg.Executor.OutputTokenReserve = 8192
	}

	// Compaction defaults
	if cfg.Executor.Compaction.SlidingWindow.KeepFirst == 0 {
		cfg.Executor.Compaction.SlidingWindow.KeepFirst = 3
	}
	if cfg.Executor.Compaction.SlidingWindow.KeepLast == 0 {
		cfg.Executor.Compaction.SlidingWindow.KeepLast = 10
	}
	if cfg.Executor.Compaction.Summarization.BlockSize == 0 {
		cfg.Executor.Compaction.Summarization.BlockSize = 7
	}
	if cfg.Executor.Compaction.Summarization.KeepLast == 0 {
		cfg.Executor.Compaction.Summarization.KeepLast = 5
	}
	if cfg.Executor.Compaction.Hierarchical.EnabledAboveSteps == 0 {
		cfg.Executor.Compaction.Hierarchical.EnabledAboveSteps = 25
	}
	if cfg.Executor.Compaction.Hierarchical.DistantRatio == 0 {
		cfg.Executor.Compaction.Hierarchical.DistantRatio = 0.4
	}
	if cfg.Executor.Compaction.Hierarchical.MiddleRatio == 0 {
		cfg.Executor.Compaction.Hierarchical.MiddleRatio = 0.3
	}
	if cfg.Executor.Compaction.Hierarchical.RecentRatio == 0 {
		cfg.Executor.Compaction.Hierarchical.RecentRatio = 0.3
	}
	if cfg.Executor.Compaction.MaxSummarizeTokens == 0 {
		cfg.Executor.Compaction.MaxSummarizeTokens = 16000
	}
	if cfg.Executor.Compaction.ObservationTruncate == 0 {
		cfg.Executor.Compaction.ObservationTruncate = 500
	}
	if cfg.Executor.Compaction.SafetyMarginPercent == 0 {
		cfg.Executor.Compaction.SafetyMarginPercent = 5
	}
	// Compression-ratio forecast seeds for manual-compaction prediction. Zero
	// fields are left unset here: the core layer resolves them to sp4rk's
	// conservative defaults (0.3 / 0.15 / 0.3) — mirroring what the prediction
	// already does for a zero CompactionForecast. Applying them here would
	// double-default and make an explicit "0" (an invalid ratio) indistinguishable
	// from "unset".

	// Compaction thresholds defaults
	if cfg.Executor.Compaction.Thresholds.PredictivePercent == 0 {
		cfg.Executor.Compaction.Thresholds.PredictivePercent = 85
	}
	if cfg.Executor.Compaction.Thresholds.WarningPercent == 0 {
		cfg.Executor.Compaction.Thresholds.WarningPercent = 92
	}
	if cfg.Executor.Compaction.Thresholds.EmergencyPercent == 0 {
		cfg.Executor.Compaction.Thresholds.EmergencyPercent = 98
	}
	if cfg.Executor.Compaction.Thresholds.PreWarningPercent == 0 {
		cfg.Executor.Compaction.Thresholds.PreWarningPercent = 75
	}
	// Ensure pre_warning_percent < predictive_percent; clamp if misconfigured.
	if cfg.Executor.Compaction.Thresholds.PreWarningPercent >= cfg.Executor.Compaction.Thresholds.PredictivePercent {
		cfg.Executor.Compaction.Thresholds.PreWarningPercent = cfg.Executor.Compaction.Thresholds.PredictivePercent - 5
	}

	// Tool result budget defaults
	if cfg.Executor.ToolResultBudget.HardCapTokens == 0 {
		cfg.Executor.ToolResultBudget.HardCapTokens = 4096
	}
	if cfg.Executor.ToolResultBudget.MaxFillFraction == 0 {
		cfg.Executor.ToolResultBudget.MaxFillFraction = 0.3
	}
	if cfg.Executor.ToolResultBudget.CacheTTLSeconds == 0 {
		cfg.Executor.ToolResultBudget.CacheTTLSeconds = 300 // 5 minutes for MCP tools
	}

	// Tool output pruning defaults
	if cfg.Executor.ToolOutputPruning.KeepLastN == 0 {
		cfg.Executor.ToolOutputPruning.KeepLastN = 3
	}
	if cfg.Executor.ToolOutputPruning.ProtectedTools == nil {
		cfg.Executor.ToolOutputPruning.ProtectedTools = defaultProtectedTools
	}
	if cfg.Executor.ToolOutputPruning.ThresholdPercent == 0 {
		cfg.Executor.ToolOutputPruning.ThresholdPercent = 50
	}

	// History mutation defaults
	if cfg.Executor.HistoryMutation.ToolResultEvictionStep == 0 {
		cfg.Executor.HistoryMutation.ToolResultEvictionStep = 10
	}
	// EvictStepStatus and DedupRepeatedReads default to false (zero-value).
	// Users opt in via config.yaml.

	// Circuit breaker defaults
	if cfg.Executor.CircuitBreaker.RepeatNudgeThreshold == 0 {
		cfg.Executor.CircuitBreaker.RepeatNudgeThreshold = 3
	}
	if cfg.Executor.CircuitBreaker.RepeatAbortThreshold == 0 {
		cfg.Executor.CircuitBreaker.RepeatAbortThreshold = 4
	}
	if cfg.Executor.CircuitBreaker.TruncationAbortThreshold == 0 {
		cfg.Executor.CircuitBreaker.TruncationAbortThreshold = 3
	}
	if cfg.Executor.CircuitBreaker.ParseErrorAbortThreshold == 0 {
		cfg.Executor.CircuitBreaker.ParseErrorAbortThreshold = 3
	}
	if cfg.Executor.CircuitBreaker.FruitlessNudgeThreshold == 0 {
		cfg.Executor.CircuitBreaker.FruitlessNudgeThreshold = 4
	}
	if cfg.Executor.CircuitBreaker.FruitlessAbortThreshold == 0 {
		cfg.Executor.CircuitBreaker.FruitlessAbortThreshold = 6
	}
	if cfg.Executor.CircuitBreaker.FruitlessMaxResultLen == 0 {
		cfg.Executor.CircuitBreaker.FruitlessMaxResultLen = 32
	}
	if cfg.Executor.CircuitBreaker.SameToolRepeatNudgeThreshold == 0 {
		cfg.Executor.CircuitBreaker.SameToolRepeatNudgeThreshold = 6
	}
	if cfg.Executor.CircuitBreaker.SameToolRepeatAbortThreshold == 0 {
		cfg.Executor.CircuitBreaker.SameToolRepeatAbortThreshold = 10
	}
	if cfg.Executor.CircuitBreaker.SameToolResultSizeDelta == 0 {
		cfg.Executor.CircuitBreaker.SameToolResultSizeDelta = 64
	}

	// Models defaults (initialize empty map if nil)
	if cfg.LLM.Models == nil {
		cfg.LLM.Models = make(map[string]ModelOverride)
	}

	// Router defaults
	if cfg.Router.HistoryWindow == 0 {
		cfg.Router.HistoryWindow = 10
	}

	// Security defaults
	if cfg.Security.InjectionDefense.Enabled == nil {
		t := true
		cfg.Security.InjectionDefense.Enabled = &t
	}

	// Security tool-group defaults (security.groups). Each configurable
	// group is created with its default policy unless the user set one.
	// No blocklist is seeded: the execute-group blocklist is empty by
	// default and purely a user-authored extension.
	if cfg.Security.Groups == nil {
		cfg.Security.Groups = make(map[string]GroupPolicyConfig)
	}
	for name, policy := range defaultToolGroupPolicies {
		group, ok := cfg.Security.Groups[name]
		if !ok {
			group = GroupPolicyConfig{}
		}
		if group.Policy == "" {
			group.Policy = policy
		}
		cfg.Security.Groups[name] = group
	}

	// Autonomy-mode default (security.autonomy_mode). Empty means the config
	// predates the enum AND carried no legacy autonomy keys — the load-time
	// migration (migrateLegacyAutonomyMode) has already resolved those onto
	// the enum before ApplyDefaults runs, so seeding here only covers
	// pristine and programmatically built configs.
	if cfg.Security.AutonomyMode == "" {
		cfg.Security.AutonomyMode = AutonomyModeStandard
	}

	// Silent-mode sub-policy defaults (security.silent_mode). The
	// sub-policies are inert unless the autonomy mode is "silent"; each mode
	// is seeded so a config that names no sub-policy still gets a complete,
	// valid posture. An explicit mode is preserved.
	ApplySilentModeDefaults(&cfg.Security.SilentMode)

	// Search defaults
	if cfg.Search.Provider == "" {
		cfg.Search.Provider = "tavily"
	}
	// Normalize to lowercase for defense-in-depth. The search tool registration
	// in core/tools/builtin_registration.go also lowercases at consumption time,
	// but normalizing here ensures consistent values throughout the pipeline.
	cfg.Search.Provider = strings.ToLower(cfg.Search.Provider)

	// Tool limits defaults
	if cfg.ToolLimits.ReadDefaultLines == 0 {
		cfg.ToolLimits.ReadDefaultLines = 2000
	}
	if cfg.ToolLimits.WebSearchMaxResults == 0 {
		cfg.ToolLimits.WebSearchMaxResults = 5
	}

	// Per-tool Stage 1 truncation defaults (applied before token budget).
	// These limits trigger output fragmentation: when tool output exceeds the
	// limit, it is truncated and a nudge with a cache hash is appended so the
	// LLM can read the full result in fragments via tool_result_read.
	// Set conservative values so fragmentation activates for realistic
	// large-output scenarios (e.g. reading files >2000 lines).
	if cfg.ToolLimits.PerToolTruncation == nil {
		cfg.ToolLimits.PerToolTruncation = map[string]ToolTruncationConfig{
			"read_file":       {MaxLines: 2000, MaxBytes: 0},
			"read_attachment": {MaxLines: 2000, MaxBytes: 0},
			"ripgrep":         {MaxLines: 2000, MaxBytes: 0},
			"glob":            {MaxLines: 2000, MaxBytes: 0},
			"list_directory":  {MaxLines: 2000, MaxBytes: 0},
			"web_fetch":       {MaxLines: 0, MaxBytes: 2097152},
			"bash_exec":       {MaxLines: 5000, MaxBytes: 0},
			"posh_exec":       {MaxLines: 5000, MaxBytes: 0},
		}
	}

	// Timeouts defaults
	if cfg.Timeouts.BashMaxTimeout == 0 {
		cfg.Timeouts.BashMaxTimeout = 120
	}
	if cfg.Timeouts.BashWaitDelay == 0 {
		cfg.Timeouts.BashWaitDelay = 5
	}
	if cfg.Timeouts.RipgrepTimeout == 0 {
		cfg.Timeouts.RipgrepTimeout = 60
	}
	if cfg.Timeouts.WebFetchTimeout == 0 {
		cfg.Timeouts.WebFetchTimeout = 30
	}
	if cfg.Timeouts.WebFetchProxyTimeout == 0 {
		cfg.Timeouts.WebFetchProxyTimeout = 30
	}
	if cfg.Timeouts.WebFetchRetries == 0 {
		cfg.Timeouts.WebFetchRetries = 2
	}
	if cfg.Timeouts.WebSearchTimeout == 0 {
		cfg.Timeouts.WebSearchTimeout = 30
	}
	if cfg.Timeouts.PersistenceTimeout == 0 {
		cfg.Timeouts.PersistenceTimeout = 5
	}
	if cfg.Timeouts.LLMRequestTimeout == 0 {
		cfg.Timeouts.LLMRequestTimeout = 600
	}
	if cfg.Timeouts.ServiceLLMRequestTimeout == 0 {
		cfg.Timeouts.ServiceLLMRequestTimeout = 120
	}

	// Orchestration defaults
	if cfg.Orchestration.MaxDependencyContextChars == 0 {
		cfg.Orchestration.MaxDependencyContextChars = 8000
	}
	if cfg.Orchestration.MaxSummaryLength == 0 {
		cfg.Orchestration.MaxSummaryLength = 500
	}
	if cfg.Orchestration.MaxJudgeCacheSize == 0 {
		cfg.Orchestration.MaxJudgeCacheSize = 1000
	}
	if cfg.Orchestration.MaxRedelegationDepth == 0 {
		cfg.Orchestration.MaxRedelegationDepth = 2
	}

	// Goal loop defaults. Verification defaults to "independent" so the
	// independent verifier turn runs unless explicitly disabled via "off".
	if cfg.GoalLoop.Verification == "" {
		cfg.GoalLoop.Verification = "independent"
	}

	// Vector index / hybrid search defaults. Hybrid is a pointer-bool so
	// callers can distinguish "unset" (defaults applied below) from an
	// explicit false; set it directly here.
	if cfg.VectorIndex.Hybrid == nil {
		trueVal := true
		cfg.VectorIndex.Hybrid = &trueVal
	}

	// Hybrid RRF tuning: zero-valued ints fall back to built-in defaults
	// in vectorindex.ResolveHybridConfig, so only set them when the user
	// provided an explicit non-zero value. The pointer-float thresholds
	// default to the built-in score-gate values when unset.
	hybridDefaults := vectorindex.DefaultHybridConfig()
	if cfg.VectorIndex.HybridVectorScoreFloor == nil {
		floor := hybridDefaults.VectorScoreFloor
		cfg.VectorIndex.HybridVectorScoreFloor = &floor
	}
	if cfg.VectorIndex.HybridVectorScoreRatio == nil {
		ratio := hybridDefaults.VectorScoreRatio
		cfg.VectorIndex.HybridVectorScoreRatio = &ratio
	}
	if cfg.VectorIndex.HybridLexicalScoreRatio == nil {
		ratio := hybridDefaults.LexicalScoreRatio
		cfg.VectorIndex.HybridLexicalScoreRatio = &ratio
	}

	// File-size and chunk-size limits. Zero falls back to the vectorindex
	// package defaults (4 MiB / 1500 chars). These are applied here (not in
	// NewManager/NewService) so they are visible in the resolved config the
	// frontend can inspect, matching the hybrid-threshold pattern above.
	if cfg.VectorIndex.MaxFileSize == 0 {
		cfg.VectorIndex.MaxFileSize = vectorindex.DefaultMaxIndexableFileSize
	}
	if cfg.VectorIndex.MaxChunkSize == 0 {
		cfg.VectorIndex.MaxChunkSize = vectorindex.DefaultMaxChunkSize
	}
	if cfg.VectorIndex.MaxChunksPerFile == 0 {
		cfg.VectorIndex.MaxChunksPerFile = vectorindex.DefaultMaxChunksPerFile
	}

	// Indexing/search tuning knobs. Zero falls back to the vectorindex
	// package defaults — the historical hardcoded values — so a config
	// without a vector_index block resolves to exactly the pre-knob
	// behaviour. SearchWaitTimeoutMs is a pointer-int: nil (unset) resolves
	// to the 3000 ms default, while an explicit 0 is preserved as the
	// "fail fast" sentinel and must NOT be defaulted here.
	if cfg.VectorIndex.EmbeddingBatchSize == 0 {
		cfg.VectorIndex.EmbeddingBatchSize = vectorindex.DefaultEmbeddingBatchSize
	}
	if cfg.VectorIndex.EmbeddingCacheMaxBytes == 0 {
		cfg.VectorIndex.EmbeddingCacheMaxBytes = vectorindex.DefaultEmbeddingCacheMaxBytes
	}
	if cfg.VectorIndex.PrepWorkers == 0 {
		cfg.VectorIndex.PrepWorkers = vectorindex.DefaultPrepWorkers
	}
	if cfg.VectorIndex.DebounceMs == 0 {
		cfg.VectorIndex.DebounceMs = int(vectorindex.DefaultDebounce.Milliseconds())
	}
	if cfg.VectorIndex.ChunkOverlap == 0 {
		cfg.VectorIndex.ChunkOverlap = vectorindex.DefaultChunkOverlap
	}
	// Content-filter defaults are materialized so the resolved config the
	// frontend inspects shows the effective policy. Pointer bools default
	// to true; numeric thresholds to the package defaults. Explicit user
	// values (including explicit false) are preserved.
	filterDefaults := vectorindex.DefaultContentFilterConfig()
	if cfg.VectorIndex.ContentFilter.Enabled == nil {
		enabled := filterDefaults.Enabled
		cfg.VectorIndex.ContentFilter.Enabled = &enabled
	}
	if cfg.VectorIndex.ContentFilter.DetectGenerated == nil {
		v := filterDefaults.DetectGenerated
		cfg.VectorIndex.ContentFilter.DetectGenerated = &v
	}
	if cfg.VectorIndex.ContentFilter.DetectMinified == nil {
		v := filterDefaults.DetectMinified
		cfg.VectorIndex.ContentFilter.DetectMinified = &v
	}
	if cfg.VectorIndex.ContentFilter.DetectPathological == nil {
		v := filterDefaults.DetectPathological
		cfg.VectorIndex.ContentFilter.DetectPathological = &v
	}
	if cfg.VectorIndex.ContentFilter.GeneratedHeaderBytes == 0 {
		cfg.VectorIndex.ContentFilter.GeneratedHeaderBytes = filterDefaults.GeneratedHeaderBytes
	}
	if cfg.VectorIndex.ContentFilter.MinifiedMinBytes == 0 {
		cfg.VectorIndex.ContentFilter.MinifiedMinBytes = filterDefaults.MinifiedMinBytes
	}
	if cfg.VectorIndex.ContentFilter.MinifiedMaxLineBytes == 0 {
		cfg.VectorIndex.ContentFilter.MinifiedMaxLineBytes = filterDefaults.MinifiedMaxLineBytes
	}
	if cfg.VectorIndex.ContentFilter.MinifiedMaxWhitespaceRatio == 0 {
		cfg.VectorIndex.ContentFilter.MinifiedMaxWhitespaceRatio = filterDefaults.MinifiedMaxWhitespaceRatio
	}
	if cfg.VectorIndex.ContentFilter.PathologicalMinBytes == 0 {
		cfg.VectorIndex.ContentFilter.PathologicalMinBytes = filterDefaults.PathologicalMinBytes
	}
	if cfg.VectorIndex.ContentFilter.PathologicalMaxTokenBytes == 0 {
		cfg.VectorIndex.ContentFilter.PathologicalMaxTokenBytes = filterDefaults.PathologicalMaxTokenBytes
	}
	if cfg.VectorIndex.SearchWaitTimeoutMs == nil {
		v := int(vectorindex.DefaultSearchWaitTimeout.Milliseconds())
		cfg.VectorIndex.SearchWaitTimeoutMs = &v
	}
	// ParkCapacity is a pointer-int too: nil (unset) resolves to the default
	// of 3, while an explicit 0 is preserved and disables parking (the
	// historical reopen-every-switch behaviour).
	if cfg.VectorIndex.ParkCapacity == nil {
		v := vectorindex.DefaultParkCapacity
		cfg.VectorIndex.ParkCapacity = &v
	}
	// ParkBudgetMb is a pointer-int64: nil (unset) resolves to the default
	// of 1024 MiB, while an explicit negative is preserved as the
	// "budget disabled" sentinel (park_capacity alone bounds the LRU). An
	// explicit 0 is NOT defaulted here — validate() rejects it as ambiguous.
	if cfg.VectorIndex.ParkBudgetMb == nil {
		v := vectorindex.DefaultParkBudgetBytes >> 20
		cfg.VectorIndex.ParkBudgetMb = &v
	}

	// Execution provider. The empty string (a config written before the
	// knob existed, or no vector_index block at all) normalizes to "auto".
	// DeviceID has no defaulting to do: its zero value (0) is already the
	// valid "first GPU" default; only invalid negatives are rejected by
	// validate().
	if cfg.VectorIndex.ExecutionProvider == "" {
		cfg.VectorIndex.ExecutionProvider = VectorIndexProviderAuto
	}

	// Proxy defaults
	if cfg.Proxy.BypassList == nil {
		cfg.Proxy.BypassList = []string{"localhost", "127.0.0.1"}
	}
	// Default: export proxy env vars for subprocesses (backward compat).
	// Explicit `set_global_env: false` in YAML disables this.
	if cfg.Proxy.Enabled && cfg.Proxy.SetGlobalEnv == nil {
		trueVal := true
		cfg.Proxy.SetGlobalEnv = &trueVal
	}

	// Model Profiles profile persist defaults. The section carries only the master
	// toggle and the active profile id (ModelProfilesPersistConfig): the master toggle
	// defaults to false (manual only — no auto-detection), and the active
	// profile defaults to the model-agnostic "generic" profile. The 25
	// variant knobs are no longer seeded here — they come from the active
	// profile's catalog entry at resolve time (ResolveModelProfilesConfig), where the
	// generic profile carries the researched defaults (reasoning_effort
	// "medium", loop thresholds 2/3/3/5/4, context 6/5/80/2/16384, the
	// default always-present list). A legacy inline small_llm.* section is
	// ignored at load and dropped on the next save.
	if cfg.ModelProfiles.ActiveProfile == "" {
		cfg.ModelProfiles.ActiveProfile = ModelProfilesGenericProfileID
	}

	// E2S execution-mode defaults. Like the Model Profiles profile the section is
	// seeded unconditionally (zero → default) so the values stay visible and
	// editable while the mode itself stays a no-op until experimental.enabled
	// is true (there is no separate e2s master toggle). The byte limit mirrors
	// core/e2s.DefaultStateByteLimit so the domain default and the config
	// default cannot drift apart.
	if cfg.E2S.MaxSteps == 0 {
		cfg.E2S.MaxSteps = 50
	}
	if cfg.E2S.StateByteLimit == 0 {
		cfg.E2S.StateByteLimit = e2s.DefaultStateByteLimit
	}
	if cfg.E2S.PatchRetries == 0 {
		cfg.E2S.PatchRetries = 1
	}
	if cfg.E2S.ObservationTruncate == 0 {
		cfg.E2S.ObservationTruncate = 2000
	}
	if cfg.E2S.RepeatNudgeThreshold == 0 {
		cfg.E2S.RepeatNudgeThreshold = 3
	}
	if cfg.E2S.RepeatAbortThreshold == 0 {
		cfg.E2S.RepeatAbortThreshold = 5
	}

	// Self-update defaults. Enabled is the master switch and defaults to true
	// (a pointer-bool so an explicit `enabled: false` in YAML is respected
	// rather than overwritten by the default). AutoCheck governs the automatic
	// background poll and likewise defaults to true. CheckInterval defaults to
	// 6h, a conservative cadence that avoids hammering the GitHub
	// unauthenticated rate limit while still surfacing new releases promptly.
	if cfg.Updates.Enabled == nil {
		enabled := true
		cfg.Updates.Enabled = &enabled
	}
	if cfg.Updates.AutoCheck == nil {
		autoCheck := true
		cfg.Updates.AutoCheck = &autoCheck
	}
	if cfg.Updates.CheckInterval == "" {
		cfg.Updates.CheckInterval = "6h"
	}

	// Git auto-fetch defaults. AutoFetch is the master gate for every
	// automatic fetch trigger (app startup, project switch, window focus,
	// periodic ticker) and defaults to true (a pointer-bool so an explicit
	// `auto_fetch: false` in YAML is respected rather than overwritten by the
	// default). AutoFetchInterval defaults to 2m; the raw string is parsed by
	// the consumer, and an explicit "0" is preserved here — it disables only
	// the periodic ticker, leaving the event-driven triggers on.
	if cfg.Git.AutoFetch == nil {
		autoFetch := true
		cfg.Git.AutoFetch = &autoFetch
	}
	if cfg.Git.AutoFetchInterval == "" {
		cfg.Git.AutoFetchInterval = "2m"
	}

	// Notification banner lifetime defaults to -1: let the notification
	// daemon decide, which is the behavior that shipped before the setting
	// existed. A pointer-int because 0 is a MEANINGFUL value here ("never
	// expire"), so the Go zero value cannot double as "unset".
	if cfg.Notifications.BannerTimeoutSeconds == nil {
		bannerTimeout := NotificationBannerTimeoutDaemonDefault
		cfg.Notifications.BannerTimeoutSeconds = &bannerTimeout
	}

	// Runtime defaults need no mutation: runtime.memory_soft_limit_mb is a
	// tri-state int whose zero value already IS the default (0 = auto — the
	// desktop layer derives the soft limit from physical RAM, see
	// desktop/memlimit.go; >0 = explicit MiB; -1 = off). Documented here so
	// default hunters find it; the AUTO value (0) and the OFF sentinel (-1)
	// are validated in validate().

	// shell_exec defaults: an active launch-shape override without a
	// declared shell is seeded with the tool's platform-default kind
	// (bash_exec → bash, posh_exec → powershell) — the overwhelmingly common
	// override (a Homebrew bash, a login zsh, a pwsh replacement) keeps the
	// tool's native syntax family. An INVALID explicit kind is not handled
	// here: normalizeShellExec (load pipeline) warns and resets the section.
	if cfg.ShellExec.BashExec.OverrideActive() && cfg.ShellExec.BashExec.Shell == "" {
		cfg.ShellExec.BashExec.Shell = DefaultShellKindBash
	}
	if cfg.ShellExec.PoshExec.OverrideActive() && cfg.ShellExec.PoshExec.Shell == "" {
		cfg.ShellExec.PoshExec.Shell = DefaultShellKindPosh
	}
}

// defaultToolGroupPolicies is the single source of truth for the configurable
// security tool groups (everything except the reserved ToolGroupSystem) and
// their default policies: reads run without confirmation; anything that
// mutates local state, executes commands, or crosses a process/network
// boundary requires user confirmation.
var defaultToolGroupPolicies = map[string]string{
	ToolGroupLocalRead:   GroupPolicyAllow,
	ToolGroupRemoteRead:  GroupPolicyAllow,
	ToolGroupExecute:     GroupPolicyUserConfirm,
	ToolGroupLocalWrite:  GroupPolicyUserConfirm,
	ToolGroupLocalMCP:    GroupPolicyUserConfirm,
	ToolGroupRemoteMCP:   GroupPolicyUserConfirm,
	ToolGroupRemoteWrite: GroupPolicyUserConfirm,
}

// sortedToolGroupNames lists the configurable group names in stable order for
// error messages and documentation.
var sortedToolGroupNames = func() []string {
	names := make([]string, 0, len(defaultToolGroupPolicies))
	for name := range defaultToolGroupPolicies {
		names = append(names, name)
	}
	sort.Strings(names)
	return names
}()

// IsConfigurableToolGroup reports whether name is one of the configurable
// security tool groups (every declared group except the reserved "system"
// group, which policy configuration must never touch). It is the single
// predicate behind config validation and runtime security-settings updates.
func IsConfigurableToolGroup(name string) bool {
	_, ok := defaultToolGroupPolicies[name]
	return ok
}

// SortedToolGroupNames returns a copy of the configurable group names in
// stable order for error messages and documentation.
func SortedToolGroupNames() []string {
	return append([]string(nil), sortedToolGroupNames...)
}
