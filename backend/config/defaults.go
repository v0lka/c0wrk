package config

import (
	"slices"
	"sort"
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
	// Manual compaction target: the fill % of the context window that a
	// user-triggered compaction aims to compact the history down to. Unset
	// (0) falls back to 30%.
	if cfg.Executor.Compaction.ManualTargetPercent == 0 {
		cfg.Executor.Compaction.ManualTargetPercent = 30
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
		// Slot budget for router-matched tools on top of the never-trimmed
		// guaranteed set: always_present (12) ∪ protected (5, 4 overlap) =
		// 13 unique tools, MCP joins at runtime. 16 leaves 3 free slots with
		// the default always-present list; validateSmallLLMConfig rejects
		// configs where the guaranteed set alone exceeds the budget.
		cfg.SmallLLM.EssentialTools.MaxTools = 16
	}
	// Sampling parameters are deliberately NOT seeded: zero means "inherit
	// the vendor preset" (see SmallLLMSamplingConfig). Seeding a constant
	// temperature here previously forced 0.1/top_p 0.9 onto every family the
	// moment the sampling variant was enabled, clobbering vendor-tuned
	// presets (the 27-30B regression). Users who want an override set an
	// explicit value in YAML or the UI.
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

	// Small-LLM context-management variant defaults. Like the other variants,
	// these are seeded unconditionally (zero → variant default) so the values
	// are visible/editable; the profile itself stays a no-op until both the
	// master toggle and the variant toggle are enabled. Zero continues to mean
	// "do not override" at apply time.
	if cfg.SmallLLM.Context.Compaction.KeepLast == 0 {
		cfg.SmallLLM.Context.Compaction.KeepLast = 6
	}
	if cfg.SmallLLM.Context.Compaction.BlockSize == 0 {
		cfg.SmallLLM.Context.Compaction.BlockSize = 5
	}
	if cfg.SmallLLM.Context.Compaction.TriggerPercent == 0 {
		cfg.SmallLLM.Context.Compaction.TriggerPercent = 80
	}
	if cfg.SmallLLM.Context.ToolOutputKeepLastN == 0 {
		cfg.SmallLLM.Context.ToolOutputKeepLastN = 2
	}
	if cfg.SmallLLM.Context.OutputTokenReserve == 0 {
		cfg.SmallLLM.Context.OutputTokenReserve = 8192
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
}

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

		// --- SCM (git) — mutating subcommands only ---
		// Blocks git operations that change the repository, its history, or
		// the working tree/index. Read-only commands (status, log, diff,
		// show, blame, ls-files, rev-parse, describe, fetch, ...) are
		// intentionally NOT blocked so they flow through normally. Dual-mode
		// subcommands (branch, tag, config, stash, remote) are blocked
		// wholesale: RE2 has no lookahead, so flagless mutating forms (e.g.
		// `git branch x`, `git stash`, `git config k v`, `git tag v1`)
		// cannot be reliably separated from read-only forms. `git fetch` is
		// excluded — it only adds objects and updates remote-tracking refs,
		// never the working tree, local branches, or history (additive /
		// non-destructive). See TestApplyDefaults_GitMutatingBlacklist.
		// working tree / index / staging:
		`\bgit\s+(add|rm|mv|clean|checkout|switch|restore|stash|apply)\b`,
		// history / commits / refs (incl. history rewrites):
		`\bgit\s+(commit|am|merge|rebase|revert|cherry-pick|reset|notes|replace|update-ref|symbolic-ref|reflog|bisect|filter-branch|filter-repo|fast-import)\b`,
		// branch / tag / remote / submodule / network / exfil (transmit patch
		// data or spawn a network server bound to the repo):
		`\bgit\s+(branch|tag|remote|submodule|clone|push|pull|send-email|imap-send|daemon|instaweb)\b`,
		// repo lifecycle / config / maintenance:
		`\bgit\s+(init|config|gc|prune|worktree|maintenance)\b`,
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

		// --- SCM (git) — mutating subcommands only ---
		// Mirrors bash_exec. See that block for the full rationale: only
		// mutating git subcommands are blocked; read-only commands (status,
		// log, diff, show, fetch, ...) are NOT blocked; dual-mode subcommands
		// (branch, tag, config, stash, remote) are blocked wholesale because
		// RE2 has no lookahead. Patterns are case-insensitive ((?i) prefix)
		// because PowerShell resolves the git executable case-insensitively,
		// so `Git commit` / `GIT PUSH` must still match.
		// working tree / index / staging:
		`(?i)\bgit\s+(add|rm|mv|clean|checkout|switch|restore|stash|apply)\b`,
		// history / commits / refs (incl. history rewrites):
		`(?i)\bgit\s+(commit|am|merge|rebase|revert|cherry-pick|reset|notes|replace|update-ref|symbolic-ref|reflog|bisect|filter-branch|filter-repo|fast-import)\b`,
		// branch / tag / remote / submodule / network / exfil:
		`(?i)\bgit\s+(branch|tag|remote|submodule|clone|push|pull|send-email|imap-send|daemon|instaweb)\b`,
		// repo lifecycle / config / maintenance:
		`(?i)\bgit\s+(init|config|gc|prune|worktree|maintenance)\b`,
	}
}

// DefaultExecuteGroupBlacklist returns the default blacklist for the
// "execute" security group: the union of the bash_exec and posh_exec default
// lists. Both shells share the group, so the group-level blacklist covers
// both dialects; exact duplicate patterns (none today) are deduplicated.
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
