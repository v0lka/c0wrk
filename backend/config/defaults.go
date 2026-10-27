package config

import (
	"sort"
	"strings"

	"github.com/v0lka/c0wrk/core/e2s"
	"github.com/v0lka/c0wrk/core/vectorindex"
	"github.com/v0lka/sp4rk/tools/builtins/websearch"
)

// defaultProtectedTools is the default list of tools whose output is always preserved during pruning.
var defaultProtectedTools = []string{"store_fact", "search_facts"}

// defaultSkillDirs is the default skill-discovery list (`skills.dirs`
// omitted): c0wrk global outranks user, so a c0wrk-managed skill wins over a
// same-named user one; the project's `.agents/skills` is scanned
// automatically, not listed. See
// specs/decisions/056-research-always-on-global-seeding.md.
var defaultSkillDirs = []string{
	"~/.c0wrk/.agents/skills",
	"~/.agents/skills",
}

// defaultAgentDirs mirrors defaultSkillDirs for AGENT.md discovery
// (`agents.dirs` omitted); the project's `.agents/agents` is auto-scanned.
var defaultAgentDirs = []string{
	"~/.c0wrk/.agents/agents",
	"~/.agents/agents",
}

// defaultCommandDirs mirrors defaultSkillDirs for command templates
// (`commands.dirs` omitted); the project's `.agents/commands` is auto-scanned.
var defaultCommandDirs = []string{
	"~/.c0wrk/.agents/commands",
	"~/.agents/commands",
}

// defaultModelProfilesAlwaysPresent is the model-profile essential-tools
// allow-list: a minimal schema footprint with enough capability to
// navigate/edit/search/finalize, pinning the ReAct loop's own dependencies
// (attachment and skill-resource reads, tool_result_read, update_checklist).
// MCP-backed tools layer on separately at runtime. See
// specs/domains/model-profiles.md.
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

// intPtr constructs the *int knobs whose contract distinguishes an absent
// key (nil → default) from an explicit zero ("0 disables").
func intPtr(v int) *int { return &v }

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

	// Custom command discovery defaults (nil => apply defaults; empty slice
	// => user explicitly opted out of base dirs, keep it empty). Mirrors skills.
	if cfg.Commands.Dirs == nil {
		cfg.Commands.Dirs = append([]string(nil), defaultCommandDirs...)
	}

	// Subagent concurrency cap; <= 0 resolves back to 4 because the conductor
	// treats a non-positive cap as uncapped (no cap at all).
	if cfg.Agents.MaxParallelSubagents <= 0 {
		cfg.Agents.MaxParallelSubagents = 4
	}

	// LLM retry defaults — kept in sync with the sp4rk Router defaults
	// (sp4rk/llm/router.go); they cover transient failures (429/502/503/529, blips).
	if cfg.LLM.Retry.MaxRetries == 0 {
		cfg.LLM.Retry.MaxRetries = 3
	}
	if cfg.LLM.Retry.InitialBackoff == "" {
		cfg.LLM.Retry.InitialBackoff = "1s"
	}
	if cfg.LLM.Retry.MaxBackoff == "" {
		cfg.LLM.Retry.MaxBackoff = "30s"
	}

	// ChatGPT auth mode defaults to the static API key; consumers also read
	// empty as "api_key" — this just makes the persisted value explicit.
	if cfg.LLM.ChatGPT.Auth.Mode == "" {
		cfg.LLM.ChatGPT.Auth.Mode = ChatGPTAuthModeAPIKey
	}

	// Embedded LLM idle-unload defaults; pointers so explicit YAML is
	// respected. The rest of the section is app-written install state (its
	// zero values ARE the not-installed defaults).
	if cfg.EmbeddedLLM.AutoUnload.Enabled == nil {
		autoUnload := true
		cfg.EmbeddedLLM.AutoUnload.Enabled = &autoUnload
	}
	if cfg.EmbeddedLLM.AutoUnload.Minutes == nil {
		minutes := EmbeddedLLMDefaultAutoUnloadMinutes
		cfg.EmbeddedLLM.AutoUnload.Minutes = &minutes
	}

	// embedded_llm.tuning is deliberately NOT seeded: pointer knobs keep
	// "never wrote this" distinct from "wrote auto" (and from an explicit
	// 0/false); materializing defaults would collapse that on the first save.
	// The zero TuningConfig IS the all-Auto default. See TuningConfig.

	// Executor defaults
	if cfg.Executor.MaxRetries == 0 {
		cfg.Executor.MaxRetries = 2
	}
	if cfg.Executor.OutputTokenReserve == 0 {
		// 8192: modern models emit multi-thousand-token tool-call replies;
		// a smaller reserve risks truncation. Input-side budget only.
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
	// Forecast seeds left unset: core resolves sp4rk's conservative defaults
	// (0.3 / 0.15 / 0.3); applying here would double-default and blur an
	// explicit "0" (invalid ratio) vs "unset".

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
	// SearXNG default instance URL (the upstream docker-compose default). An
	// operator who only sets provider: searxng gets a working local-instance
	// default; an explicit search.base_url always wins.
	if strings.EqualFold(cfg.Search.Provider, "searxng") && strings.TrimSpace(cfg.Search.BaseURL) == "" {
		cfg.Search.BaseURL = websearch.DefaultSearxngBaseURL
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
	// Glob limits (runaway-walk protection), mirroring
	// builtins.DefaultGlobLimits: *int so explicit 0 = disabled, nil =
	// default (the "0 disables" contract a plain int cannot express).
	if cfg.ToolLimits.GlobMaxEntries == nil {
		cfg.ToolLimits.GlobMaxEntries = intPtr(500000)
	}
	if cfg.ToolLimits.GlobMaxResults == nil {
		cfg.ToolLimits.GlobMaxResults = intPtr(10000)
	}

	// Per-tool Stage 1 truncation (before the token budget): over-limit
	// output is truncated with a cache-hash nudge for tool_result_read
	// paging; conservative values so fragmentation activates realistically.
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
	// GlobTimeout / ToolCallTimeout are *int for the same reason as the glob
	// budgets above: nil = absent → default, explicit 0 = "disabled/no budget".
	if cfg.Timeouts.GlobTimeout == nil {
		cfg.Timeouts.GlobTimeout = intPtr(30)
	}
	if cfg.Timeouts.ToolCallTimeout == nil {
		cfg.Timeouts.ToolCallTimeout = intPtr(300)
	}
	// The ceiling's exempt tool-name set is left nil when absent: nil
	// threads nil-guarded to sp4rk, which applies its OWN built-in exempt
	// set at execution time (agent/tool_watchdog.go) — no drift, and never
	// frozen into config.yaml on a first save. An explicit list survives
	// verbatim (an explicit empty list = no exemptions).
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
	// LLMRequestTimeout gets NO 0→600 coercion (ADR-071): 0 = "no opinion"
	// under the adaptive budgets; only a positive value is a fixed override.
	if cfg.Timeouts.AdaptiveBudget.Enabled == nil {
		enabled := true
		cfg.Timeouts.AdaptiveBudget.Enabled = &enabled
	}
	if cfg.Timeouts.ServiceLLMRequestTimeout == 0 {
		cfg.Timeouts.ServiceLLMRequestTimeout = 600
	}
	if cfg.Timeouts.GitCommitTimeout == 0 {
		cfg.Timeouts.GitCommitTimeout = 300
	}

	// Shutdown hard-teardown watchdog — the last-resort exit bound; 60 s sits
	// above the embedded model's ~46 s worst-case stop ceiling.
	if cfg.Shutdown.HardDeadline == 0 {
		cfg.Shutdown.HardDeadline = 60
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
	// Goal-mode suggestion on unless disabled (pointer-bool nil = enabled);
	// consulted from complexity 3 up — lower complexity is never a goal.
	if cfg.GoalLoop.Suggest.MinComplexity == 0 {
		cfg.GoalLoop.Suggest.MinComplexity = 3
	}

	// Vector index / hybrid search defaults. Hybrid is a pointer-bool so
	// callers can distinguish "unset" (defaults applied below) from an
	// explicit false; set it directly here.
	if cfg.VectorIndex.Hybrid == nil {
		trueVal := true
		cfg.VectorIndex.Hybrid = &trueVal
	}

	// Hybrid RRF tuning: zero ints fall back to built-in defaults in
	// vectorindex.ResolveHybridConfig; pointer-floats to the score gates.
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

	// File-size and chunk-size limits fall back to the vectorindex defaults;
	// applied HERE so the resolved config the frontend inspects shows them.
	if cfg.VectorIndex.MaxFileSize == 0 {
		cfg.VectorIndex.MaxFileSize = vectorindex.DefaultMaxIndexableFileSize
	}
	if cfg.VectorIndex.MaxChunkSize == 0 {
		cfg.VectorIndex.MaxChunkSize = vectorindex.DefaultMaxChunkSize
	}
	if cfg.VectorIndex.MaxChunksPerFile == 0 {
		cfg.VectorIndex.MaxChunksPerFile = vectorindex.DefaultMaxChunksPerFile
	}

	// Indexing/search knobs: zero falls back to the vectorindex defaults;
	// SearchWaitTimeoutMs is pointer-int (nil → 3000 ms, explicit 0 = fail fast).
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
	// Content-filter defaults materialized so the resolved config shows the
	// effective policy; pointer bools default true (explicit false preserved).
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
	// ParkBudgetMb: pointer-int64, nil → 1024 MiB default; explicit negative =
	// budget disabled; explicit 0 rejected by validate() as ambiguous.
	if cfg.VectorIndex.ParkBudgetMb == nil {
		v := vectorindex.DefaultParkBudgetBytes >> 20
		cfg.VectorIndex.ParkBudgetMb = &v
	}

	// Execution provider: empty normalizes to "auto"; DeviceID's 0 is already
	// the valid "first GPU" (only negatives are rejected).
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

	// Model Profiles persistence carries only the master toggle (default
	// false — manual only) and the active profile ("generic"); the variant
	// knobs come from the catalog at resolve time (ResolveModelProfilesConfig).
	// A legacy inline small_llm.* section is ignored and dropped on next save.
	if cfg.ModelProfiles.ActiveProfile == "" {
		cfg.ModelProfiles.ActiveProfile = ModelProfilesGenericProfileID
	}

	// E2S execution-mode defaults, seeded unconditionally so values stay
	// visible/editable while the mode stays a no-op until experimental.enabled
	// (no separate toggle); the byte limit mirrors core/e2s.DefaultStateByteLimit.
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
		cfg.E2S.ObservationTruncate = 8000
	}
	if cfg.E2S.ObservationBudgetTokens == 0 {
		cfg.E2S.ObservationBudgetTokens = 8192
	}
	if cfg.E2S.ObservationFillFraction == 0 {
		cfg.E2S.ObservationFillFraction = 0.4
	}
	if cfg.E2S.RepeatNudgeThreshold == 0 {
		cfg.E2S.RepeatNudgeThreshold = 3
	}
	if cfg.E2S.RepeatAbortThreshold == 0 {
		cfg.E2S.RepeatAbortThreshold = 5
	}
	// E2S tool catalog: the core preset is the default (the relevant
	// local-work core); allow/deny default to empty sets.
	if cfg.E2S.Tools.Preset == "" {
		cfg.E2S.Tools.Preset = E2SToolsPresetCore
	}

	// Self-update: Enabled/AutoCheck pointer-bools default true (explicit
	// false respected); CheckInterval 6h avoids GitHub rate limiting.
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

	// Git auto-fetch: AutoFetch gates every automatic trigger (default true);
	// AutoFetchInterval 2m, consumer-parsed; explicit "0" disables only the ticker.
	if cfg.Git.AutoFetch == nil {
		autoFetch := true
		cfg.Git.AutoFetch = &autoFetch
	}
	if cfg.Git.AutoFetchInterval == "" {
		cfg.Git.AutoFetchInterval = "2m"
	}

	// Notification banner lifetime defaults to -1 ("daemon decides");
	// pointer-int because 0 is MEANINGFUL ("never expire"), not "unset".
	if cfg.Notifications.BannerTimeoutSeconds == nil {
		bannerTimeout := NotificationBannerTimeoutDaemonDefault
		cfg.Notifications.BannerTimeoutSeconds = &bannerTimeout
	}

	// Runtime needs no mutation: memory_soft_limit_mb is tri-state and its
	// zero value IS the default (0 = auto from physical RAM, desktop/memlimit.go;
	// -1 = off), validated in validate(). Documented for default hunters.

	// shell_exec defaults: an active launch-shape override without a declared
	// shell is seeded with the tool's platform-default kind (bash_exec → bash,
	// posh_exec → powershell) — the overwhelmingly common override keeps the
	// tool's native syntax family. An INVALID explicit kind is not handled
	// here: normalizeShellExec (load pipeline) warns and resets the section.
	if cfg.ShellExec.BashExec.OverrideActive() && cfg.ShellExec.BashExec.Shell == "" {
		cfg.ShellExec.BashExec.Shell = DefaultShellKindBash
	}
	if cfg.ShellExec.PoshExec.OverrideActive() && cfg.ShellExec.PoshExec.Shell == "" {
		cfg.ShellExec.PoshExec.Shell = DefaultShellKindPosh
	}

	// web_access defaults: OFF by default (a zero Enabled never starts the
	// sidecar); only the bind is seeded so the persisted shape names the
	// loopback default; port 0 (auto) and the empty token (minted once, then
	// persisted) are zero values by design. An UNSAFE enabled section is left
	// to normalizeWebAccess (load pipeline).
	if cfg.WebAccess.Bind == "" {
		cfg.WebAccess.Bind = DefaultWebAccessBind
	}
}

// defaultToolGroupPolicies is the single source of truth for the
// configurable security tool groups' default policies: reads run without
// confirmation; anything mutating local state, executing commands, or
// crossing a process/network boundary requires user confirmation.
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

// IsConfigurableToolGroup reports whether name is a configurable group
// (all but the reserved "system"); the predicate behind validation + updates.
func IsConfigurableToolGroup(name string) bool {
	_, ok := defaultToolGroupPolicies[name]
	return ok
}

// SortedToolGroupNames returns a copy of the configurable group names in
// stable order for error messages and documentation.
func SortedToolGroupNames() []string {
	return append([]string(nil), sortedToolGroupNames...)
}
