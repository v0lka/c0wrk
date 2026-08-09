package config

import (
	"strings"

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

// defaultSmallLLMAlwaysPresent is the default always-present tool allow-list
// exposed when the small-LLM essential-tools variant is active. It balances a
// minimal schema footprint against enough capability to navigate, edit,
// search, and finalize tasks. MCP-backed tools are layered on separately at
// runtime.
var defaultSmallLLMAlwaysPresent = []string{
	"read_file",
	"write_file",
	"edit_file",
	"list_directory",
	"glob",
	"ripgrep",
	"bash_exec",
	"semantic_search",
	"store_fact",
	"search_facts",
	"ask_user",
	"finish",
}

// ApplyDefaults sets default values for zero-value fields in the configuration.
func ApplyDefaults(cfg *Config) {
	// Log level defaults to DEBUG for maximum diagnostic visibility.
	if cfg.LogLevel == "" {
		cfg.LogLevel = "DEBUG"
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
		cfg.Executor.OutputTokenReserve = 4096
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
	if cfg.Security.DefaultPolicy == "" {
		cfg.Security.DefaultPolicy = "user_confirm"
	}
	if cfg.Security.ToolPolicies == nil {
		cfg.Security.ToolPolicies = make(map[string]ToolPolicyConfig)
	}
	// Default tool policies
	if _, ok := cfg.Security.ToolPolicies["bash_exec"]; !ok {
		cfg.Security.ToolPolicies["bash_exec"] = ToolPolicyConfig{
			Policy: "user_confirm",
			Blacklist: []string{
				// The blacklist is organized into four destructive categories
				// that are mirrored (conceptually) by the posh_exec blacklist
				// below. Keep the categories in sync when editing either list.
				// (See TestApplyDefaults_BlacklistCategorySymmetry.)

				// --- Destructive file/disk operations ---
				`rm\s+-rf\s+/`,
				`mkfs`,
				`dd\s+if=`,
				`>\s*/dev/`,

				// --- Power-state (mirrors posh Stop/Restart-Computer) ---
				`\b(shutdown|reboot|halt|poweroff|init\s+[06])\b`,

				// --- Remote-exec / download-cradle (mirrors posh IWR|iex) ---
				// Piped execution of fetched content (curl|sh etc.) — a classic
				// supply-chain / RCE vector. Blocks regardless of policy, even
				// under always_allow.
				`\b(curl|wget)\b.*\|\s*(?:\S*/)?(?:env\s+)?\b(sh|bash|zsh|dash|ksh|fish|perl\d*|node|ruby|python[\d.]*)\b`,

				// --- Irreversible system writes (mirrors posh Set-Content on System32) ---
				`>\s*/etc/(passwd|shadow|sudoers|group|fstab)\b`,
				`(tee|dd)\b[^|]*/etc/(passwd|shadow|sudoers)\b`,
				`\bchmod\b[^|]*\b777\b[^|]*/(etc|usr|boot|bin|sbin)\b`,
				`>\s*/boot/`,

				// --- Misc hardening (mirrors posh registry/scheduled-task tampering) ---
				`:\(\)\s*\{`,                                           // fork bomb
				`\bcrontab\s+-r\b`,                                     // wipe crontab
				`\b(iptables|ufw|nft)\b[^|]*(-F\b|--flush\b|-X\b|-P\s+\w+)`, // firewall flush

				// --- Privilege escalation & SCM ---
				`sudo\s+`,
				`\bgit\b`,
			},
		}
	}
	// posh_exec (PowerShell) mirrors bash_exec's user_confirm policy but with a
	// Windows/PowerShell-specific blacklist organized into the same destructive
	// categories as bash_exec (destructive file/disk, power-state,
	// remote-exec/download-cradle, irreversible system writes, misc hardening)
	// so the two shells stay conceptually aligned. PowerShell is
	// case-insensitive for cmdlets and matches parameters by case-insensitive
	// prefix (-r/-rec/-recurse, -f/-fo/-force), and Remove-Item has aliases
	// (del, erase, ri, rm, rd, rmdir). RE2 has no lookaheads, so the two-order
	// requirement for -Recurse + -Force is expressed as two patterns.
	if _, ok := cfg.Security.ToolPolicies["posh_exec"]; !ok {
		cfg.Security.ToolPolicies["posh_exec"] = ToolPolicyConfig{
			Policy: "user_confirm",
			Blacklist: []string{
				// --- Destructive file/disk operations ---
				`(?i)\b(Remove-Item|del|erase|ri|rm|rd|rmdir)\b.*-r\w*.*-f\w*`,
				`(?i)\b(Remove-Item|del|erase|ri|rm|rd|rmdir)\b.*-f\w*.*-r\w*`,
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
				`(?i)\bSet-ItemProperty\b[^|]*HKLM`,                    // registry tampering
				`(?i)\b(Register-ScheduledTask|schtasks\s+/create)\b`,  // scheduled tasks
				`(?i)Set-ExecutionPolicy`,                              // execution-policy tampering

				// --- SCM ---
				`\bgit\b`,
			},
		}
	}
	if _, ok := cfg.Security.ToolPolicies["write_file"]; !ok {
		cfg.Security.ToolPolicies["write_file"] = ToolPolicyConfig{
			Policy: "user_confirm",
		}
	}
	if _, ok := cfg.Security.ToolPolicies["edit_file"]; !ok {
		cfg.Security.ToolPolicies["edit_file"] = ToolPolicyConfig{
			Policy: "user_confirm",
		}
	}
	if _, ok := cfg.Security.ToolPolicies["web_search"]; !ok {
		cfg.Security.ToolPolicies["web_search"] = ToolPolicyConfig{
			Policy: "always_allow",
		}
	}
	if _, ok := cfg.Security.ToolPolicies["web_fetch"]; !ok {
		cfg.Security.ToolPolicies["web_fetch"] = ToolPolicyConfig{
			Policy: "always_allow",
		}
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

	// Small-LLM profile defaults. The master toggle defaults to false (manual
	// only — no auto-detection); the operator opts in explicitly. The
	// per-variant sub-toggles default to false too, so each optimization only
	// activates when both the master toggle and its own toggle are on. Every
	// value/threshold is populated with a sensible default so nothing requires
	// a rebuild once enabled.
	if cfg.SmallLLM.EssentialTools.AlwaysPresent == nil {
		cfg.SmallLLM.EssentialTools.AlwaysPresent = defaultSmallLLMAlwaysPresent
	}
	if cfg.SmallLLM.EssentialTools.MaxTools == 0 {
		cfg.SmallLLM.EssentialTools.MaxTools = 12
	}
	if cfg.SmallLLM.Sampling.Temperature == 0 {
		cfg.SmallLLM.Sampling.Temperature = 0.1
	}
	if cfg.SmallLLM.Sampling.TopP == 0 {
		cfg.SmallLLM.Sampling.TopP = 0.9
	}
	// ReasoningEffort defaults to "" (inherit the model's default). It is left
	// unset rather than forced so an explicit "" in YAML is preserved.

	// Loop-hardening thresholds — tighter than the baseline CircuitBreaker so a
	// small model that repeats itself or makes no progress is caught sooner.
	if cfg.SmallLLM.LoopHardening.RepeatNudgeThreshold == 0 {
		cfg.SmallLLM.LoopHardening.RepeatNudgeThreshold = 2
	}
	if cfg.SmallLLM.LoopHardening.ParseErrorAbortThreshold == 0 {
		cfg.SmallLLM.LoopHardening.ParseErrorAbortThreshold = 3
	}
	if cfg.SmallLLM.LoopHardening.FruitlessNudgeThreshold == 0 {
		cfg.SmallLLM.LoopHardening.FruitlessNudgeThreshold = 3
	}
	if cfg.SmallLLM.LoopHardening.FruitlessAbortThreshold == 0 {
		cfg.SmallLLM.LoopHardening.FruitlessAbortThreshold = 5
	}
	if cfg.SmallLLM.LoopHardening.SameToolRepeatNudgeThreshold == 0 {
		cfg.SmallLLM.LoopHardening.SameToolRepeatNudgeThreshold = 4
	}

	// Self-update defaults. Enabled defaults to true (a pointer-bool so an
	// explicit `enabled: false` in YAML is respected rather than overwritten by
	// the default). CheckInterval defaults to 6h, a conservative cadence that
	// avoids hammering the GitHub unauthenticated rate limit while still
	// surfacing new releases promptly.
	if cfg.Updates.Enabled == nil {
		enabled := true
		cfg.Updates.Enabled = &enabled
	}
	if cfg.Updates.CheckInterval == "" {
		cfg.Updates.CheckInterval = "6h"
	}
}
