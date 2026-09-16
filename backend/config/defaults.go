package config

import (
	"slices"
	"sort"
	"strings"

	"github.com/v0lka/c0wrk/core/e2s"
	"github.com/v0lka/c0wrk/core/vectorindex"
)

// defaultProtectedTools is the default list of tools whose output is always preserved during pruning.
var defaultProtectedTools = []string{"store_fact", "search_facts"}

// defaultSkillDirs is the default list of skill discovery directories used when
// the `skills.dirs` config key is omitted. The current project's
// `.agents/skills` directory is always scanned automatically (see core/builder.go)
// and does NOT need to be listed here.
var defaultSkillDirs = []string{
	"~/.agents/skills",
	"~/.c0wrk/.agents/skills",
}

// defaultAgentDirs is the default list of Subagent Profile discovery
// directories used when the `agents.dirs` config key is omitted. Mirrors
// defaultSkillDirs for AGENT.md files. The current project's `.agents/agents`
// directory is always scanned automatically (see core/builder.go).
var defaultAgentDirs = []string{
	"~/.agents/agents",
	"~/.c0wrk/.agents/agents",
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
	if cfg.Agents.MaxParallelSubagents == 0 {
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
	// group is created with its default policy unless the user set one; the
	// execute group also receives the default command blacklist (the union of
	// the bash/posh lists) unless the user provided one.
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
		if name == ToolGroupExecute && group.Blacklist == nil {
			group.Blacklist = DefaultExecuteGroupBlacklist()
		}
		cfg.Security.Groups[name] = group
	}

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

	// Runtime defaults need no mutation: runtime.memory_soft_limit_mb is a
	// tri-state int whose zero value already IS the default (0 = auto — the
	// desktop layer derives the soft limit from physical RAM, see
	// desktop/memlimit.go; >0 = explicit MiB; -1 = off). Documented here so
	// default hunters find it; the AUTO value (0) and the OFF sentinel (-1)
	// are validated in validate().
}

// gitShellWord matches one shell word whose value may embed whitespace via
// quoting: an unquoted run of non-space, non-quote characters, a
// double-quoted segment, a single-quoted segment, a backslash-escaped
// character (`\ `), or a concatenation of them (`user.name="John Doe"`,
// `'my repo'`). It preserves the flag/value boundary that a bare `\S+`
// loses at a quoted space, so `-C "my repo"` and `-c 'k=v w'` are consumed
// whole instead of leaving an unbalanced fragment before the subcommand.
const gitShellWord = `(?:"[^"]*"|'[^']*'|\\[\s\S]|[^\s"'])+`

// gitGlobalOpts is the global-option preamble G shared by every git pattern
// in BOTH shell blacklists (it carries no (?i) of its own; the posh heads'
// leading (?i) covers it). Without it a single global option between `git`
// and the subcommand defeated EVERY git pattern at once: `git -C repo push`,
// `git -c k=v commit -m msg`, `git --git-dir=.git reset --hard` did not have
// the subcommand immediately after `git\s+`. G slots between `git` and the
// subcommand and admits only genuine git(1) global options, so reads keep
// flowing (`git -C repo status`, `git -p log` stay free):
//   - `-c<v>` / `-C<path>` — attached (`-Crepo`, `-cuser.name=x`) or
//     separated (`-C repo`, `-c k=v`) — and the `-p` / `-P` short switches
//     (paginate / no-pager, no value). The separator between a value-taking
//     flag and its value is `[=\s]*` (zero-or-more `=`/whitespace), NOT a
//     single optional char: `?` admitted at most one space/tab, so a doubled
//     space or a tab (`git -C  repo push`, `git -C\trepo push`) defeated
//     every pattern — a one-character bypass. `*` keeps the attached form
//     (`-Crepo`) working and matches any whitespace run. A separating
//     flag's value is a shell word (gitShellWord), so a quoted /
//     space-containing value stays bound to its flag (`git -C "my repo"
//     push`, `git -c "user.name=John Doe" push`) rather than being mistaken
//     for the subcommand;
//   - value-taking long flags (--git-dir, --work-tree, --namespace,
//     --exec-path, --config-env, --attr-source, --list-cmds), attached (=v)
//     or separated ( v), value likewise a shell word, separator `[=\s]*`;
//   - standalone long flags (--bare, --no-pager, --paginate,
//     --no-replace-objects, --no-optional-locks, --no-lazy-fetch,
//     --no-advice, the *-pathspecs switches).
//
// git's informational globals (--version, --help, --html-path, --man-path,
// --info-path) are deliberately absent: they print and exit, so they cannot
// prefix a mutating subcommand.
const gitGlobalOpts = `(?:\s+(?:-[cC](?:[=\s]*` + gitShellWord + `)?|-[pP]|--(?:git-dir|work-tree|namespace|exec-path|config-env|attr-source|list-cmds)(?:[=\s]*` + gitShellWord + `)?|--(?:bare|no-replace-objects|no-lazy-fetch|no-optional-locks|no-advice|paginate|no-pager|literal-pathspecs|glob-pathspecs|noglob-pathspecs|icase-pathspecs)\b))*`

// defaultBashExecBlacklist returns the POSIX-shell half of the default
// "execute" group blacklist patterns (see DefaultExecuteGroupBlacklist).
func defaultBashExecBlacklist() []string {
	return []string{
		// The blacklist is organized into four destructive categories
		// that are mirrored (conceptually) by the posh_exec blacklist
		// below. Keep the categories in sync when editing either list.
		// (See TestApplyDefaults_BlacklistCategorySymmetry.)

		// --- Destructive file/disk operations ---
		`rm\s+-rf\s+/`,
		`mkfs`,
		`dd\s+if=`,
		// `dd of=` writing to a block or kernel-memory device destroys the
		// disk or escalates privileges (e.g. `dd of=/dev/sda bs=1M` with
		// no if=, which the `dd\s+if=` pattern above misses). Shares the
		// same device-prefix list as the narrowed /dev/ redirect above so
		// benign targets like `dd of=/dev/null` stay unblocked. `[^|]*`
		// prevents the match from jumping across a pipe (mirrors the
		// existing `(tee|dd)\b[^|]*/etc/(passwd|shadow|sudoers)` pattern).
		`dd\b[^|]*\bof=/dev/(sd|hd|vd|xvd|nvme|mmcblk|loop|ram|zram|dm-|md|disk|mapper|mem|kmem|port)`,
		// Write/redirect to a block or kernel-memory device destroys the
		// disk or escalates privileges. Narrowed from a blanket `>\s*/dev/`
		// so the ubiquitous benign /dev family (/dev/null, /dev/zero,
		// /dev/full, /dev/random, /dev/std*, /dev/fd, /dev/tty) — the most
		// common redirect targets in robust shell commands like
		// `cmd 2>/dev/null` — no longer trigger a forced confirmation under
		// allow. The prefix alternation matches every real block
		// device family (SATA/SCSI sd, legacy IDE hd, virtio vd, Xen xvd,
		// NVMe nvme, SD/eMMC mmcblk, loop, RAM disks ram/zram,
		// device-mapper dm-, RAID md, stable symlinks disk/ & mapper/,
		// kernel mem/port) while sharing none of its prefixes with any
		// benign /dev entry.
		`>\s*/dev/(sd|hd|vd|xvd|nvme|mmcblk|loop|ram|zram|dm-|md|disk|mapper|mem|kmem|port)`,

		// --- Power-state (mirrors posh Stop/Restart-Computer) ---
		`\b(shutdown|reboot|halt|poweroff|init\s+[06])\b`,

		// --- Remote-exec / download-cradle (mirrors posh IWR|iex) ---
		// Piped execution of fetched content (curl|sh etc.) — a classic
		// supply-chain / RCE vector. Blocks regardless of policy, even
		// under allow.
		`\b(curl|wget)\b.*\|\s*(?:\S*/)?(?:env\s+)?\b(sh|bash|zsh|dash|ksh|fish|perl\d*|node|ruby|python[\d.]*)\b`,

		// --- Irreversible system writes (mirrors posh Set-Content on System32) ---
		`>\s*/etc/(passwd|shadow|sudoers|group|fstab)\b`,
		`(tee|dd)\b[^|]*/etc/(passwd|shadow|sudoers)\b`,
		`\bchmod\b[^|]*\b777\b[^|]*/(etc|usr|boot|bin|sbin)\b`,
		`>\s*/boot/`,

		// --- Misc hardening (mirrors posh registry/scheduled-task tampering) ---
		`:\(\)\s*\{`,       // fork bomb
		`\bcrontab\s+-r\b`, // wipe crontab
		`\b(iptables|ufw|nft)\b[^|]*(-F\b|--flush\b|-X\b|-P\s+\w+)`, // firewall flush

		// --- Privilege escalation ---
		`sudo\s+`,

		// --- SCM (git) — mutating forms only ---
		// Blocks git operations that change the repository, its history, or
		// the working tree/index; read-only commands (status, log, diff,
		// show, blame, ls-files, rev-parse, describe, fetch, ...) flow
		// through untouched. `git fetch` stays excluded by design: it only
		// adds objects and updates remote-tracking refs (additive /
		// non-destructive). See TestApplyDefaults_GitMutatingBlacklist.
		//
		// RE2 has no lookahead, so the precision below comes from four
		// techniques that replaced the old coarse `\bgit\s+(…)` wholesale
		// patterns:
		//
		//  1. Command-position prefix P = (^|[\s;&|(\x60]|[/\\]) instead of
		//     \b. \b also matched `git …` inside quoted strings that are DATA,
		//     not commands (`rg "git checkout"`, `echo 'git stash'`, `git
		//     log --grep="git rebase"`). P demands genuine command position:
		//     start of input, or right after a separator (; & |), a `$(`
		//     command-substitution parenthesis, a subshell `(`, a newline, a
		//     backtick — so `$(git push)`, `cd repo && git push` and
		//     `xargs git rm` still block — or right after a path separator /
		//     backslash, so path-qualified and escaped invocations
		//     (`/usr/bin/git rm x`, `./git push`, `\git push`,
		//     `C:\Git\bin\git push`) block too. Residual FPs: a newline in
		//     P's class hard-confirms `git <mutating>` text on its own line
		//     inside a heredoc body, and the path class hard-confirms
		//     git-mutating text glued to a path token (`a/git push`) —
		//     accepted: both over-confirm, never under-confirm.
		//
		//  2. Inverted dual-mode patterns. Subcommands that also have
		//     read-only forms (branch, tag, stash, remote, config, reflog,
		//     apply, clean, submodule, worktree, notes, reset, add, rm,
		//     sparse-checkout) enumerate their MUTATING forms instead of
		//     blocking wholesale: a bare non-flag token (create target,
		//     pathspec, patch file, key+value pair), a short-flag cluster
		//     containing a mutating letter (`-[^\s-]*[…]` — the inner
		//     class excludes `-` so long flags never match it), or the
		//     enumerated mutating long flags/subactions. Flag-tolerance is
		//     PER-PATTERN, not universal: where a quiet/verbose flag cannot
		//     hide a read, the pattern carries a `(?:-[^\s-]*[qv]|--quiet|
		//     --verbose)` prefix so the mutation still blocks (`git rm -q
		//     foo.txt`, `git notes --ref=ns add -m x`, `git submodule -q
		//     update`, `git remote -q prune origin`, `git apply -q p.diff`,
		//     `git worktree -q add ../w`), while `reset`/`clean`/`stash`
		//     absorb a leading flag through their own alternations (`git
		//     reset -q --hard`, `git clean -q -f`, `git stash -q push`).
		//     Operands before the mutating flag (`git reset HEAD --hard`,
		//     `git clean . -f`) also block. A lone operand confirms whichever
		//     it is — `git reset main` (moves the branch pointer) and `git
		//     reset README.md` (unstages one path) are syntactically
		//     identical, and no regex separates a commit-ish from a lone
		//     pathspec — while the common index-only spellings (bare `git
		//     reset`, `git reset HEAD <path>`) stay free. Read-only forms
		//     (branch -a/--show-current, tag -l,
		//     stash list/show, remote -v/get-url, config <key> reads, reflog
		//     bare/show, apply --check/--stat, clean -n/--dry-run, add -n,
		//     rm -n/--dry-run, submodule status, worktree list, notes list,
		//     sparse-checkout list) stay unblocked.
		//     Residual FPs: combined short clusters mixing a mutating letter
		//     into a dry-run, or a mutating flag placed before the `-n`
		//     (`git clean -dn`, `git clean -f -n`), quiet index-only forms
		//     (`git reset -q`), and index-only unstages that still match —
		//     a lone pathspec (`git reset README.md`), a leading commit-ish
		//     operand with a pathspec (`git reset origin/main README.md`)
		//     and bare `git reset HEAD` — all recoverable by re-staging.
		//     Residual FNs: `add`, `branch` and `tag` carry no quiet/verbose
		//     prefix rule, so `git add -v f`, `git branch -q -D t` and
		//     `git tag -q -d v1` stay unblocked — `tag` is deliberately
		//     excluded because its `-v` (verify) is a read-only spelling the
		//     generic prefix would false-positive.
		//     Wholesale groups keep blocking every form — they have no
		//     read-only use worth carving out.
		//
		//  3. FN plugs + compensating wrapper. Wholesale coverage for
		//     plumbing that bypasses the porcelain patterns (update-index/
		//     read-tree/checkout-index mutate the index directly, mergetool/
		//     difftool run external tools, `hook run` executes repo hooks;
		//     send-pack/upload-pack/receive-pack/shell/http-backend/
		//     upload-archive/http-push are the transport & server faces of
		//     push/fetch; repack/pack-refs/pack-objects/hash-object -w/
		//     write-tree/mktree/mktag/unpack-objects/index-pack/
		//     update-server-info/multi-pack-index/rerere/backfill rewrite or
		//     grow the object store, credential touches the OS credential
		//     store, for-each-repo dispatches arbitrary subcommands, and the
		//     p4/svn/lfs faces push to remotes). The wrapper closes the FN
		//     that quoting creates: inside `sh -c "git …"`, `bash -lc 'git …'`
		//     or `eval "git …"` the character before `git` is a quote, so P no
		//     longer matches; the pattern keys on interpreter+c-flag (or eval)
		//     followed by `git` and a mutating subcommand. It is (?i) because
		//     `BASH -C "GIT PUSH"` is a real spelling; the cost is
		//     over-confirming benign text after an interpreter (`bash -c
		//     'echo git push'`). Residual FNs: string-splitting evasions
		//     (`g=git; $g push`, `git pu""sh`, variables) — no blacklist
		//     regex can close those — and a bare shell fed from stdin /
		//     here-string / process-substitution (`echo 'git push' | sh`,
		//     `sh <<< 'git push'`, `sh <(echo 'git push')`), which has no
		//     -c/-Command flag to key on; both are accepted residuals, the
		//     execute-group policy still gates every such call.
		//
		//  4. Global-option preamble G (the gitGlobalOpts const, shared
		//     with the posh mirror and both wrapper tails). Without it a
		//     single global option defeated EVERY pattern above at once:
		//     in `git -C repo push`, `git -c k=v commit -m msg` or
		//     `git --git-dir=.git reset --hard` the subcommand no longer
		//     immediately followed `git\s+`. G slots between `git` and
		//     the subcommand and admits only genuine global-option tokens
		//     (see the const's doc comment), so reads keep flowing
		//     (`git -C repo status`, `git -C repo config user.name`).
		//
		// working tree / index / staging — wholesale (no read-only form):
		`(^|[\s;&|(\x60]|[/\\])git` + gitGlobalOpts + `\s+(mv|checkout|switch|restore)\b`,
		// index plumbing & external-tool runners (mergetool, difftool —
		// whose --extcmd is arbitrary exec — and `hook run`) — wholesale FN
		// plugs bypassing the porcelain:
		`(^|[\s;&|(\x60]|[/\\])git` + gitGlobalOpts + `\s+(update-index|read-tree|checkout-index|mergetool|difftool|hook)\b`,
		// add — inverted: -n/--dry-run reads stay free; bare `git add`
		// blocks too (xargs feeds the paths):
		`(^|[\s;&|(\x60]|[/\\])git` + gitGlobalOpts + `\s+add(\s*($|[;&|)\n])|\s+(-[^\s-]*[A-Za-mo-uw-z]|--(all|update|patch|interactive|force|edit|intent-to-add|chmod|renormalize|refresh|sparse|ignore-missing|ignore-removal|pathspec-from-file)\b|--\s|[^\s;&|<>()\x60-]))`,
		// rm — inverted: -n/--dry-run reads stay free; bare `git rm` blocks
		// too (xargs feeds the paths):
		`(^|[\s;&|(\x60]|[/\\])git` + gitGlobalOpts + `\s+rm(\s*($|[;&|)\n])|\s+((?:-[^\s-]*[qv]|--quiet|--verbose)\s+)*(-[^\s-]*[frRF]|--(cached|force|recursive|ignore-unmatch|pathspec-from-file)\b|--\s|[^\s;&|<>()\x60-]))`,
		// clean — inverted: -n/--dry-run reads stay free. The region between
		// `clean` and the mutating flag is matched one token at a time, and
		// no token may be the dry-run `-n`, so `git clean -n -d` stays free
		// while `git clean . -f` and `git clean -q -f` block: a tolerated
		// token is a bare operand, a short cluster free of the dry-run
		// letter `n` (mutating letters included), or a non-mutating long
		// option — never a wildcard that could span `-n`. The flag-first
		// families (`git clean -dn`, `git clean -f -n`) keep the
		// combined-cluster FP. See the technique-2 comment above:
		`(^|[\s;&|(\x60]|[/\\])git` + gitGlobalOpts + `\s+clean\s+(?:(?:[^\s;&|()-][^\s;&|()]*|-[^\sn-][^\sn]*|--(?:exclude|quiet|verbose)\b(?:=[^\s;&|()]+|\s+[^\s;&|()]+)?)\s+)*(-[^\s-]*[dDfFxXiI]|--(force|directory|interactive)\b)`,
		// stash — bare `git stash` (= push), any flag, and the mutating
		// subactions block; `list`/`show` reads stay free:
		`(^|[\s;&|(\x60]|[/\\])git` + gitGlobalOpts + `\s+stash(\s*($|[;&|)\n])|\s+(push|pop|apply|drop|clear|store|branch|create|save)\b|\s+-)`,
		// apply — bare `git apply` (stdin), flagless patch args and
		// index/3way flags block; --check/--stat reads stay free:
		`(^|[\s;&|(\x60]|[/\\])git` + gitGlobalOpts + `\s+apply(\s*($|[;&|)\n])|\s+((?:-[^\s-]*[qv]|--quiet|--verbose)\s+)*(-[^\s-]*[3R]|--(cached|index|3way|reverse|whitespace|exclude|include|directory|recount|build-fake-ancestor|allow-empty|inaccurate-eof|unsafe-paths|binary)\b|--\s|[^\s;&|()\x60-]))`,
		// history / commits / refs (incl. history rewrites) — wholesale:
		`(^|[\s;&|(\x60]|[/\\])git` + gitGlobalOpts + `\s+(commit|am|merge|rebase|revert|cherry-pick|replace|update-ref|symbolic-ref|bisect|filter-branch|filter-repo|fast-import)\b`,
		// reset — inverted: bare `git reset` / `git reset [--] HEAD <path>`
		// (index-only, recoverable) stay free; mutating flags, mode words
		// and commit-ish tokens (HEAD~2, origin/main) block (`-q` is the
		// documented quiet-form FP above):
		`(^|[\s;&|(\x60]|[/\\])git` + gitGlobalOpts + `\s+reset\s+(-[^\s-]+|--(hard|soft|mixed|keep|merge|patch|quiet|interactive|pathspec-from-file)\b|\S*[~^/]|[^;&|()]*\s--(hard|soft|mixed|keep|merge|patch|quiet|interactive)\b|[^\s;&|<>()\x60-]+["'\x60]?\s*($|[;&|\n)]))`,
		// config — inverted: `<key>` reads (incl. --get/--list flag
		// prefixes) stay free; key+value writes and mutating flags
		// (--unset/--add/…) block. `--file f <key>` reads confirm too —
		// the flag's VALUE looks like the write's key; accepted FP of the
		// two-positional rule:
		`(^|[\s;&|(\x60]|[/\\])git` + gitGlobalOpts + `\s+config\s+((?:--[^\s]+|--|-[fF]\s+\S+)\s+)*(-[eE]\b|--(unset(-all)?|add|replace-all|rename-section|remove-section|edit)\b|[^-\s]\S*\s+\S)`,
		// notes — mutating subactions only (list/show reads stay free):
		`(^|[\s;&|(\x60]|[/\\])git` + gitGlobalOpts + `\s+notes\s+((?:-[^\s-]*[qv]|--quiet|--verbose|--ref(=[^\s]+|\s+[^-\s]\S*)?)\s+)*(add|append|copy|edit|prune|remove|merge)\b`,
		// reflog — mutating subactions only (bare/show reads stay free):
		`(^|[\s;&|(\x60]|[/\\])git` + gitGlobalOpts + `\s+reflog\s+(expire|delete)\b`,
		// branch — inverted: listing flags (-a/-l/-v/--show-current) stay
		// free; bare names and create/delete/move/upstream flags block:
		`(^|[\s;&|(\x60]|[/\\])git` + gitGlobalOpts + `\s+branch\s+(-[^\s-]*[dDfFmMcCtuU]|--(delete|move|copy|edit-description|set-upstream-to|unset-upstream|track|force|create-reflog)\b|--\s|[^\s;&|<>()\x60-])`,
		// tag — inverted: -l/-n listings stay free; bare names and
		// create/delete/force flags block:
		`(^|[\s;&|(\x60]|[/\\])git` + gitGlobalOpts + `\s+tag\s+(-[^\s-]*[aAdDfFmsSuU]|--(delete|force|annotate|sign|local-user|edit|create-reflog)\b|--\s|[^\s;&|<>()\x60-])`,
		// remote — mutating subactions only (-v/show/get-url reads stay
		// free):
		`(^|[\s;&|(\x60]|[/\\])git` + gitGlobalOpts + `\s+remote\s+((?:-[^\s-]*[qv]|--quiet|--verbose)\s+)*(add|remove|rm|rename|set-url|set-head|set-branches|prune|update)\b`,
		// submodule — mutating subactions only (status/summary reads stay
		// free):
		`(^|[\s;&|(\x60]|[/\\])git` + gitGlobalOpts + `\s+submodule\s+((?:-[^\s-]*[qv]|--quiet|--verbose)\s+)*(add|init|deinit|update|set-url|set-branch|sync|absorbgitdirs|foreach)\b`,
		// worktree — mutating subactions only (list reads stay free):
		`(^|[\s;&|(\x60]|[/\\])git` + gitGlobalOpts + `\s+worktree\s+((?:-[^\s-]*[qv]|--quiet|--verbose)\s+)*(add|remove|prune|move|repair|lock|unlock)\b`,
		// sparse-checkout — mutating subactions only (list reads stay
		// free):
		`(^|[\s;&|(\x60]|[/\\])git` + gitGlobalOpts + `\s+sparse-checkout\s+(set|add|reapply|disable|init|cone|no-cone)\b`,
		// network / exfil (transmit patch data or spawn a network server
		// bound to the repo) — wholesale:
		`(^|[\s;&|(\x60]|[/\\])git` + gitGlobalOpts + `\s+(clone|push|pull|send-email|imap-send|daemon|instaweb)\b`,
		// bundle create — exports the whole repo (refs + objects) to a
		// portable file: the exfil twin of clone (verify/list-heads
		// reads stay free):
		`(^|[\s;&|(\x60]|[/\\])git` + gitGlobalOpts + `\s+bundle\s+create\b`,
		// transport / server faces (incl. upload-archive, http-push) —
		// wholesale FN plugs bypassing the push/fetch porcelain:
		`(^|[\s;&|(\x60]|[/\\])git` + gitGlobalOpts + `\s+(send-pack|upload-pack|receive-pack|shell|http-backend|upload-archive|http-push)\b`,
		// repo lifecycle / maintenance (repack, pack-refs, server-info,
		// commit/multi-pack indexes, for-each-repo dispatcher) — wholesale:
		`(^|[\s;&|(\x60]|[/\\])git` + gitGlobalOpts + `\s+(init|gc|prune|maintenance|repack|pack-refs|pack-objects|update-server-info|multi-pack-index|for-each-repo|hash-object|write-tree|mktree|mktag|unpack-objects|index-pack|credential|rerere|backfill|p4|svn|lfs)\b`,
		// exfil-to-file flag forms — `git archive --output=f.tar main` writes a
		// repo snapshot to an arbitrary path and `git format-patch -o <dir>`
		// (or --output/--output-directory) writes patch series there; the
		// flagless stdout forms stay free:
		`(^|[\s;&|(\x60]|[/\\])git` + gitGlobalOpts + `\s+archive\s+[^;&|()]*\s?(-o\S*|--output\S*)`,
		`(^|[\s;&|(\x60]|[/\\])git` + gitGlobalOpts + `\s+format-patch\s+[^;&|()]*\s?(-o\S*|--output\S*)`,
		// compensating interpreter wrapper (technique 3 above) — (?i) so
		// BASH -C "GIT PUSH" matches too. Covers every shell interpreter
		// family: POSIX shells (incl. csh/tcsh/ash/ksh93/mksh/yash),
		// PowerShell (-Command/-c), cmd (/c AND /k) and eval. The option
		// run between the interpreter and its -c/-Command flag is
		// value-tolerant (`bash -o pipefail -c`, `bash -euo pipefail -c`,
		// `powershell -ExecutionPolicy Bypass -Command`) and the c-flag may
		// sit anywhere in a short cluster (-c/-lc/-ce/-eci). For the
		// dual-mode subcommands the wrapper mirrors the base bodies: any
		// flag, any bare operand (`sh -c "git add foo.txt"`) or the bare
		// end-form (`sh -c "git stash"`) confirms — reads quoted inside an
		// interpreter (`bash -c 'git config user.name'`) over-confirm,
		// accepted. Script hosts (python/node/perl/ruby/php) and
		// iex/Invoke-Expression get their own line below: their -c/-e
		// payloads are PROGRAM text where `;` is not a shell separator.
		`(?i)(^|[\s;&|(\x60]|[/\\])((sh|bash|zsh|dash|ksh|ksh93|mksh|yash|ash|csh|tcsh|fish)(\.exe)?(\s+-[a-z-]+(=[^\s]+|\s+[^-\s]\S*)?)*\s+-[a-z]*c[a-z]*\b|(powershell|pwsh)(\.exe)?(\s+-[a-z-]+(=[^\s]+|\s+[^-\s]\S*)?)*\s+-(command|c)\b|cmd(\.exe)?(\s+/[a-z:]+)*\s+/[ck]\b|\beval\b)[^;|\n]*git(\.exe)?` + gitGlobalOpts + `\s+(mv|checkout|switch|restore|update-index|read-tree|checkout-index|mergetool|difftool|hook|commit|am|merge|rebase|revert|cherry-pick|replace|update-ref|symbolic-ref|bisect|filter-branch|filter-repo|fast-import|clone|push|pull|send-email|imap-send|daemon|instaweb|bundle\s+create|send-pack|upload-pack|receive-pack|shell|http-backend|upload-archive|http-push|init|gc|prune|maintenance|repack|pack-refs|pack-objects|update-server-info|multi-pack-index|for-each-repo|hash-object|write-tree|mktree|mktag|unpack-objects|index-pack|credential|rerere|backfill|p4|svn|lfs|(reset|branch|tag|stash|remote|config|reflog|apply|clean|submodule|worktree|notes|add|rm|sparse-checkout)(\s+-|\s+[^\s;&|()<>\x60-]|["'\x60]?\s*($|[;&|\n)])))`,
		// script-host wrapper — `python -c`, `node -e`, `perl -e`, `ruby -e`,
		// `php -r` (plus --eval) and PowerShell `iex`/`Invoke-Expression`
		// carry program text in which `;` is NOT a command separator
		// (`python -c 'import os; os.system("git push")'`), so this
		// variant's [^|\n]* tail crosses `;` where the shell wrapper's
		// [^;|\n]* tail must not. Accepted FP: a host program that merely
		// PRINTS git-mutating text (`node -e 'console.log("git push")'`)
		// still confirms — same over-confirming class as
		// `bash -c 'echo git push'`:
		`(?i)(^|[\s;&|(\x60]|[/\\])((python3?|node|perl|ruby|php)(\.exe)?(\s+-[a-z-]+(=[^\s]+|\s+[^-\s]\S*)?)*\s+(-[cer]|--eval)\b|\b(iex|Invoke-Expression)\b)[^|\n]*git(\.exe)?` + gitGlobalOpts + `\s+(mv|checkout|switch|restore|update-index|read-tree|checkout-index|mergetool|difftool|hook|commit|am|merge|rebase|revert|cherry-pick|replace|update-ref|symbolic-ref|bisect|filter-branch|filter-repo|fast-import|clone|push|pull|send-email|imap-send|daemon|instaweb|bundle\s+create|send-pack|upload-pack|receive-pack|shell|http-backend|upload-archive|http-push|init|gc|prune|maintenance|repack|pack-refs|pack-objects|update-server-info|multi-pack-index|for-each-repo|hash-object|write-tree|mktree|mktag|unpack-objects|index-pack|credential|rerere|backfill|p4|svn|lfs|(reset|branch|tag|stash|remote|config|reflog|apply|clean|submodule|worktree|notes|add|rm|sparse-checkout)(\s+-|\s+[^\s;&|()<>\x60-]|["'\x60]?\s*($|[;&|\n)])))`,
	}
}

// defaultPoshExecBlacklist returns the PowerShell-originated half of the
// default "execute" group blacklist patterns (see
// DefaultExecuteGroupBlacklist): only the cross-dialect-safe subset — every
// pattern here must also be safe to compile into bash_exec. The
// Windows-alias-only patterns that are NOT cross-dialect safe live in
// core/tools/shelltool_windows.go as a platform supplement instead.
func defaultPoshExecBlacklist() []string {
	return []string{
		// --- Destructive file/disk operations ---
		// These patterns run in the unified execute-group blacklist, which
		// is compiled into bash_exec AND posh_exec. The two shell tools are
		// mutually exclusive per host (Unix registers bash_exec, Windows
		// posh_exec), so a PowerShell-specific pattern can only ever match
		// — and hard-confirm — benign commands of the other dialect. The
		// list therefore keeps only tokens unambiguous on both sides:
		//   - the cmdlet name Remove-Item (no Unix command is spelled like
		//     it) keeps the alias-style short-flag patterns;
		//   - the Remove-Item ALIASES (rm, del, erase, ri, rd, rmdir) are
		//     Windows-only vocabulary: `rm` is the ordinary Unix delete
		//     (`rm -r -f <dir>` is the routine separate-flags spelling of
		//     an in-workspace `rm -rf <dir>`; GNU rm even accepts the
		//     --recursive/--force spellings), and `ri`/`rmdir` are ordinary
		//     Unix tokens (`grep -ri`). They are enforced as a platform
		//     supplement in core/tools/shelltool_windows.go, so they never
		//     compile into bash_exec. See
		//     TestDefaultExecuteGroupBlacklist_CrossDialectSafe.
		`(?i)\bRemove-Item\b.*-r\w*.*-f\w*`,
		`(?i)\bRemove-Item\b.*-f\w*.*-r\w*`,
		`(?i)Format-Volume`,
		`(?i)Clear-Disk`,

		// --- Power-state (mirrors bash shutdown/halt/poweroff) ---
		`(?i)Stop-Computer`,
		`(?i)Restart-Computer`,

		// --- Remote-exec / download-cradle (mirrors bash curl|sh) ---
		// Piped or chained execution of fetched content
		// (Invoke-WebRequest | Invoke-Expression) — the #1 PowerShell
		// RCE / supply-chain vector.
		`(?i)\b(Invoke-WebRequest|iwr|irm|Invoke-RestMethod|curl|wget)\b[^|]*\|\s*(Invoke-Expression|iex)\b`,
		`(?i)\b(Invoke-WebRequest|iwr|irm|Invoke-RestMethod|curl|wget)\b[^;]*;[^;]*\b(Invoke-Expression|iex)\b`,

		// --- Irreversible system writes (mirrors bash >/etc/passwd) ---
		`(?i)\b(Set-Content|Clear-Content|Out-File|Add-Content)\b[^|]*\b(Windows\\System32|\\Windows\\|\\etc\\|\\boot)`,

		// --- Misc hardening ---
		`(?i)\bSet-ItemProperty\b[^|]*HKLM`,                   // registry tampering
		`(?i)\b(Register-ScheduledTask|schtasks\s+/create)\b`, // scheduled tasks
		`(?i)Set-ExecutionPolicy`,                             // execution-policy tampering

		// --- SCM (git) — mutating forms only ---
		// Mirrors bash_exec (same command-position prefix P, same
		// wholesale/inverted split, same global-option preamble G, same FN plugs — see that block for the
		// full rationale, residual FPs (heredoc, `clean -dn`, `reset -q`)
		// and the string-splitting FN). Differences from the bash mirror:
		// every pattern carries (?i) because PowerShell resolves the git
		// executable case-insensitively (`Git commit` / `GIT PUSH` /
		// `BASH -C "GIT PUSH"`-style casings must still match), matches the
		// `git.exe` spelling, and the compensating wrapper covers BOTH
		// interpreter families — the Windows faces (`powershell -Command
		// "git …"`, `pwsh -c 'git …'`, `cmd /c "git …"`) and the POSIX ones
		// (`sh -c "git …"`, `bash -lc 'git …'`, `eval "git …"`).
		// working tree / index / staging — wholesale (no read-only form):
		`(?i)(^|[\s;&|(\x60]|[/\\])git(\.exe)?` + gitGlobalOpts + `\s+(mv|checkout|switch|restore)\b`,
		// index plumbing & external-tool runners (mergetool, difftool —
		// whose --extcmd is arbitrary exec — and `hook run`) — wholesale FN
		// plugs bypassing the porcelain:
		`(?i)(^|[\s;&|(\x60]|[/\\])git(\.exe)?` + gitGlobalOpts + `\s+(update-index|read-tree|checkout-index|mergetool|difftool|hook)\b`,
		// add — inverted: -n/--dry-run reads stay free; bare `git add`
		// blocks too (piped input feeds the paths):
		`(?i)(^|[\s;&|(\x60]|[/\\])git(\.exe)?` + gitGlobalOpts + `\s+add(\s*($|[;&|)\n])|\s+(-[^\s-]*[a-mo-uw-z]|--(all|update|patch|interactive|force|edit|intent-to-add|chmod|renormalize|refresh|sparse|ignore-missing|ignore-removal|pathspec-from-file)\b|--\s|[^\s;&|<>()\x60-]))`,
		// rm — inverted: -n/--dry-run reads stay free; bare `git rm` blocks
		// too (piped input feeds the paths):
		`(?i)(^|[\s;&|(\x60]|[/\\])git(\.exe)?` + gitGlobalOpts + `\s+rm(\s*($|[;&|)\n])|\s+((?:-[^\s-]*[qv]|--quiet|--verbose)\s+)*(-[^\s-]*[frRF]|--(cached|force|recursive|ignore-unmatch|pathspec-from-file)\b|--\s|[^\s;&|<>()\x60-]))`,
		// clean — inverted: -n/--dry-run reads stay free (see the bash list):
		`(?i)(^|[\s;&|(\x60]|[/\\])git(\.exe)?` + gitGlobalOpts + `\s+clean\s+(?:(?:[^\s;&|()-][^\s;&|()]*|-[^\sn-][^\sn]*|--(?:exclude|quiet|verbose)\b(?:=[^\s;&|()]+|\s+[^\s;&|()]+)?)\s+)*(-[^\s-]*[dDfFxXiI]|--(force|directory|interactive)\b)`,
		// stash — bare `git stash` (= push), any flag, and the mutating
		// subactions block; `list`/`show` reads stay free:
		`(?i)(^|[\s;&|(\x60]|[/\\])git(\.exe)?` + gitGlobalOpts + `\s+stash(\s*($|[;&|)\n])|\s+(push|pop|apply|drop|clear|store|branch|create|save)\b|\s+-)`,
		// apply — bare `git apply` (stdin), flagless patch args and
		// index/3way flags block; --check/--stat reads stay free:
		`(?i)(^|[\s;&|(\x60]|[/\\])git(\.exe)?` + gitGlobalOpts + `\s+apply(\s*($|[;&|)\n])|\s+((?:-[^\s-]*[qv]|--quiet|--verbose)\s+)*(-[^\s-]*[3R]|--(cached|index|3way|reverse|whitespace|exclude|include|directory|recount|build-fake-ancestor|allow-empty|inaccurate-eof|unsafe-paths|binary)\b|--\s|[^\s;&|()\x60-]))`,
		// history / commits / refs (incl. history rewrites) — wholesale:
		`(?i)(^|[\s;&|(\x60]|[/\\])git(\.exe)?` + gitGlobalOpts + `\s+(commit|am|merge|rebase|revert|cherry-pick|replace|update-ref|symbolic-ref|bisect|filter-branch|filter-repo|fast-import)\b`,
		// reset — inverted: bare `git reset` / `git reset [--] HEAD <path>`
		// stay free; mutating flags, mode words and commit-ish tokens block:
		`(?i)(^|[\s;&|(\x60]|[/\\])git(\.exe)?` + gitGlobalOpts + `\s+reset\s+(-[^\s-]+|--(hard|soft|mixed|keep|merge|patch|quiet|interactive|pathspec-from-file)\b|\S*[~^/]|[^;&|()]*\s--(hard|soft|mixed|keep|merge|patch|quiet|interactive)\b|[^\s;&|<>()\x60-]+["'\x60]?\s*($|[;&|\n)]))`,
		// config — inverted: `<key>` reads stay free; key+value writes and
		// mutating flags (--unset/--add/…) block:
		`(?i)(^|[\s;&|(\x60]|[/\\])git(\.exe)?` + gitGlobalOpts + `\s+config\s+((?:--[^\s]+|--|-[fF]\s+\S+)\s+)*(-[eE]\b|--(unset(-all)?|add|replace-all|rename-section|remove-section|edit)\b|[^-\s]\S*\s+\S)`,
		// notes — mutating subactions only (list/show reads stay free):
		`(?i)(^|[\s;&|(\x60]|[/\\])git(\.exe)?` + gitGlobalOpts + `\s+notes\s+((?:-[^\s-]*[qv]|--quiet|--verbose|--ref(=[^\s]+|\s+[^-\s]\S*)?)\s+)*(add|append|copy|edit|prune|remove|merge)\b`,
		// reflog — mutating subactions only (bare/show reads stay free):
		`(?i)(^|[\s;&|(\x60]|[/\\])git(\.exe)?` + gitGlobalOpts + `\s+reflog\s+(expire|delete)\b`,
		// branch — inverted: listing flags (-a/-l/-v/--show-current) stay
		// free; bare names and create/delete/move/upstream flags block:
		`(?i)(^|[\s;&|(\x60]|[/\\])git(\.exe)?` + gitGlobalOpts + `\s+branch\s+(-[^\s-]*[dDfFmMcCtuU]|--(delete|move|copy|edit-description|set-upstream-to|unset-upstream|track|force|create-reflog)\b|--\s|[^\s;&|<>()\x60-])`,
		// tag — inverted: -l/-n listings stay free; bare names and
		// create/delete/force flags block:
		`(?i)(^|[\s;&|(\x60]|[/\\])git(\.exe)?` + gitGlobalOpts + `\s+tag\s+(-[^\s-]*[aAdDfFmsSuU]|--(delete|force|annotate|sign|local-user|edit|create-reflog)\b|--\s|[^\s;&|<>()\x60-])`,
		// remote — mutating subactions only (-v/show/get-url reads stay
		// free):
		`(?i)(^|[\s;&|(\x60]|[/\\])git(\.exe)?` + gitGlobalOpts + `\s+remote\s+((?:-[^\s-]*[qv]|--quiet|--verbose)\s+)*(add|remove|rm|rename|set-url|set-head|set-branches|prune|update)\b`,
		// submodule — mutating subactions only (status/summary reads stay
		// free):
		`(?i)(^|[\s;&|(\x60]|[/\\])git(\.exe)?` + gitGlobalOpts + `\s+submodule\s+((?:-[^\s-]*[qv]|--quiet|--verbose)\s+)*(add|init|deinit|update|set-url|set-branch|sync|absorbgitdirs|foreach)\b`,
		// worktree — mutating subactions only (list reads stay free):
		`(?i)(^|[\s;&|(\x60]|[/\\])git(\.exe)?` + gitGlobalOpts + `\s+worktree\s+((?:-[^\s-]*[qv]|--quiet|--verbose)\s+)*(add|remove|prune|move|repair|lock|unlock)\b`,
		// sparse-checkout — mutating subactions only (list reads stay
		// free):
		`(?i)(^|[\s;&|(\x60]|[/\\])git(\.exe)?` + gitGlobalOpts + `\s+sparse-checkout\s+(set|add|reapply|disable|init|cone|no-cone)\b`,
		// network / exfil (transmit patch data or spawn a network server
		// bound to the repo) — wholesale:
		`(?i)(^|[\s;&|(\x60]|[/\\])git(\.exe)?` + gitGlobalOpts + `\s+(clone|push|pull|send-email|imap-send|daemon|instaweb)\b`,
		// bundle create — exports the whole repo (refs + objects) to a
		// portable file: the exfil twin of clone (verify/list-heads
		// reads stay free):
		`(?i)(^|[\s;&|(\x60]|[/\\])git(\.exe)?` + gitGlobalOpts + `\s+bundle\s+create\b`,
		// transport / server faces (incl. upload-archive, http-push) —
		// wholesale FN plugs bypassing the push/fetch porcelain:
		`(?i)(^|[\s;&|(\x60]|[/\\])git(\.exe)?` + gitGlobalOpts + `\s+(send-pack|upload-pack|receive-pack|shell|http-backend|upload-archive|http-push)\b`,
		// repo lifecycle / maintenance (repack, pack-refs, server-info,
		// commit/multi-pack indexes, for-each-repo dispatcher) — wholesale:
		`(?i)(^|[\s;&|(\x60]|[/\\])git(\.exe)?` + gitGlobalOpts + `\s+(init|gc|prune|maintenance|repack|pack-refs|pack-objects|update-server-info|multi-pack-index|for-each-repo|hash-object|write-tree|mktree|mktag|unpack-objects|index-pack|credential|rerere|backfill|p4|svn|lfs)\b`,
		// exfil-to-file flag forms (see bash mirror):
		`(?i)(^|[\s;&|(\x60]|[/\\])git(\.exe)?` + gitGlobalOpts + `\s+archive\s+[^;&|()]*\s?(-o\S*|--output\S*)`,
		`(?i)(^|[\s;&|(\x60]|[/\\])git(\.exe)?` + gitGlobalOpts + `\s+format-patch\s+[^;&|()]*\s?(-o\S*|--output\S*)`,
		// compensating interpreter wrapper + script-host wrapper — identical
		// to the bash mirror's two lines (both interpreter families, cmd
		// /c and /k, iex/Invoke-Expression, python/node/perl/ruby/php
		// payloads); the union deduplicates the exact duplicates, but the
		// posh list keeps its own copies so the per-dialect test tables
		// stay self-contained. See the bash mirror's comment for the full
		// rationale (value-tolerant option runs, c-anywhere clusters,
		// bare-operand dual-mode forms, the [^|\n]* script-host tail).
		`(?i)(^|[\s;&|(\x60]|[/\\])((sh|bash|zsh|dash|ksh|ksh93|mksh|yash|ash|csh|tcsh|fish)(\.exe)?(\s+-[a-z-]+(=[^\s]+|\s+[^-\s]\S*)?)*\s+-[a-z]*c[a-z]*\b|(powershell|pwsh)(\.exe)?(\s+-[a-z-]+(=[^\s]+|\s+[^-\s]\S*)?)*\s+-(command|c)\b|cmd(\.exe)?(\s+/[a-z:]+)*\s+/[ck]\b|\beval\b)[^;|\n]*git(\.exe)?` + gitGlobalOpts + `\s+(mv|checkout|switch|restore|update-index|read-tree|checkout-index|mergetool|difftool|hook|commit|am|merge|rebase|revert|cherry-pick|replace|update-ref|symbolic-ref|bisect|filter-branch|filter-repo|fast-import|clone|push|pull|send-email|imap-send|daemon|instaweb|bundle\s+create|send-pack|upload-pack|receive-pack|shell|http-backend|upload-archive|http-push|init|gc|prune|maintenance|repack|pack-refs|pack-objects|update-server-info|multi-pack-index|for-each-repo|hash-object|write-tree|mktree|mktag|unpack-objects|index-pack|credential|rerere|backfill|p4|svn|lfs|(reset|branch|tag|stash|remote|config|reflog|apply|clean|submodule|worktree|notes|add|rm|sparse-checkout)(\s+-|\s+[^\s;&|()<>\x60-]|["'\x60]?\s*($|[;&|\n)])))`,
		`(?i)(^|[\s;&|(\x60]|[/\\])((python3?|node|perl|ruby|php)(\.exe)?(\s+-[a-z-]+(=[^\s]+|\s+[^-\s]\S*)?)*\s+(-[cer]|--eval)\b|\b(iex|Invoke-Expression)\b)[^|\n]*git(\.exe)?` + gitGlobalOpts + `\s+(mv|checkout|switch|restore|update-index|read-tree|checkout-index|mergetool|difftool|hook|commit|am|merge|rebase|revert|cherry-pick|replace|update-ref|symbolic-ref|bisect|filter-branch|filter-repo|fast-import|clone|push|pull|send-email|imap-send|daemon|instaweb|bundle\s+create|send-pack|upload-pack|receive-pack|shell|http-backend|upload-archive|http-push|init|gc|prune|maintenance|repack|pack-refs|pack-objects|update-server-info|multi-pack-index|for-each-repo|hash-object|write-tree|mktree|mktag|unpack-objects|index-pack|credential|rerere|backfill|p4|svn|lfs|(reset|branch|tag|stash|remote|config|reflog|apply|clean|submodule|worktree|notes|add|rm|sparse-checkout)(\s+-|\s+[^\s;&|()<>\x60-]|["'\x60]?\s*($|[;&|\n)])))`,
	}
}

// DefaultExecuteGroupBlacklist returns the default blacklist for the
// "execute" security group: the union of the bash_exec and posh_exec default
// lists. Both shells share the group, so the group-level blacklist covers
// both dialects; exact duplicate patterns (the two compensating interpreter
// wrappers, intentionally shared verbatim by both dialect lists) are
// deduplicated.
// Because every pattern in the union is compiled into BOTH shell tools, each
// pattern must be safe to apply to the other dialect: a pattern may only
// hard-confirm command text that is dangerous under whichever shell reads it
// (dialect-specific tokens, or token+flag combinations the other shell's
// command set cannot express benignly). Patterns that cannot be made
// dialect-neutral (the PowerShell Remove-Item aliases — `rm` is the ordinary
// Unix delete) are NOT part of this union; they are enforced as a
// Windows-only platform supplement in core/tools/shelltool_windows.go.
func DefaultExecuteGroupBlacklist() []string {
	unified := defaultBashExecBlacklist()
	seen := make(map[string]struct{}, len(unified))
	for _, pattern := range unified {
		seen[pattern] = struct{}{}
	}
	for _, pattern := range defaultPoshExecBlacklist() {
		if _, dup := seen[pattern]; dup {
			continue
		}
		seen[pattern] = struct{}{}
		unified = append(unified, pattern)
	}
	return unified
}

// StoreDefaultBlacklistAsUnset implements the store-as-unset rule: groups in
// which the execute blacklist is exactly the shipped default pattern list are
// returned with that blacklist stored as UNSET (nil), so a config that merely
// carries the defaults keeps tracking future default improvements instead of
// pinning today's list. It is the single rule behind BOTH persistence
// boundaries — Save applies it to every config write (LLM setup, MCP, search,
// the security tab, ... all funnel through it), and the runtime
// security-settings update applies it to its in-memory replacement. nil and
// every other list pass through untouched; an explicitly emptied list does
// not match, so clearing the blacklist stays an intentional choice. The
// comparison is order-sensitive, mirroring how the list is stored and
// compared everywhere else. The input map is never mutated: when the rule
// applies, a detached copy is returned (the group set has a handful of
// entries).
func StoreDefaultBlacklistAsUnset(groups map[string]GroupPolicyConfig) map[string]GroupPolicyConfig {
	exec, ok := groups[ToolGroupExecute]
	if !ok || exec.Blacklist == nil || !slices.Equal(exec.Blacklist, DefaultExecuteGroupBlacklist()) {
		return groups
	}
	out := make(map[string]GroupPolicyConfig, len(groups))
	for name, g := range groups {
		out[name] = g
	}
	exec.Blacklist = nil
	out[ToolGroupExecute] = exec
	return out
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
