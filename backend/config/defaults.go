package config

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

// setDefault sets *ptr to val if *ptr is nil.
func setDefault[T any](ptr *[]T, val []T) {
	if *ptr == nil {
		*ptr = val
	}
}

// ApplyDefaults sets default values for zero-value fields in the configuration.
func ApplyDefaults(cfg *Config) {
	// Log level defaults
	if cfg.LogLevel == "" {
		cfg.LogLevel = "DEBUG"
	}

	// Skills discovery defaults (nil => apply defaults; empty slice => user
	// explicitly opted out of base dirs, keep it empty).
	if cfg.Skills.Dirs == nil {
		cfg.Skills.Dirs = append([]string(nil), defaultSkillDirs...)
	}

	// LLM retry defaults — keep this in sync with the SDK Router defaults
	// (sdk/llm/router.go) so config-driven and code-driven values agree.
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
	if cfg.Executor.MaxReactSteps == 0 {
		cfg.Executor.MaxReactSteps = 50
	}
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

	// Reasoning defaults
	if cfg.Reasoning.BaseEffort == "" {
		cfg.Reasoning.BaseEffort = "high"
	}

	// LLM retry defaults
	if cfg.LLM.Retry.MaxRetries == 0 {
		cfg.LLM.Retry.MaxRetries = 3
	}
	if cfg.LLM.Retry.InitialBackoff == "" {
		cfg.LLM.Retry.InitialBackoff = "1s"
	}
	if cfg.LLM.Retry.MaxBackoff == "" {
		cfg.LLM.Retry.MaxBackoff = "30s"
	}

	// LMStudio default base URL
	if cfg.LLM.LMStudio.BaseURL == "" {
		cfg.LLM.LMStudio.BaseURL = "http://localhost:1234"
	}

	// Memory defaults
	if cfg.Memory.Database == "" {
		cfg.Memory.Database = "database.db"
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
				`rm\s+-rf\s+/`,
				`sudo\s+`,
				`mkfs`,
				`dd\s+if=`,
				`>\s*/dev/`,
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

	// Tool limits defaults
	if cfg.ToolLimits.ReadDefaultLines == 0 {
		cfg.ToolLimits.ReadDefaultLines = 2000
	}
	if cfg.ToolLimits.WebSearchMaxResults == 0 {
		cfg.ToolLimits.WebSearchMaxResults = 5
	}

	// Per-tool Stage 1 truncation defaults (applied before token budget).
	// These values serve as default page sizes for tool_result_read.
	if cfg.ToolLimits.PerToolTruncation == nil {
		cfg.ToolLimits.PerToolTruncation = map[string]ToolTruncationConfig{
			"read_file":      {MaxLines: 2000, MaxBytes: 0},
			"ripgrep":        {MaxLines: 200, MaxBytes: 0},
			"glob":           {MaxLines: 200, MaxBytes: 0},
			"list_directory": {MaxLines: 200, MaxBytes: 0},
			"web_fetch":      {MaxLines: 0, MaxBytes: 2097152},
			"bash_exec":      {MaxLines: 500, MaxBytes: 0},
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

	// Orchestration defaults
	if cfg.Orchestration.MaxDependencyContextChars == 0 {
		cfg.Orchestration.MaxDependencyContextChars = 8000
	}
	if cfg.Orchestration.MaxSummaryLength == 0 {
		cfg.Orchestration.MaxSummaryLength = 500
	}
	if cfg.Orchestration.MaxHistoryMessages == 0 {
		cfg.Orchestration.MaxHistoryMessages = 20
	}
	if cfg.Orchestration.MaxJudgeCacheSize == 0 {
		cfg.Orchestration.MaxJudgeCacheSize = 1000
	}
	if cfg.Orchestration.MaxPlannerExploreSteps == 0 {
		cfg.Orchestration.MaxPlannerExploreSteps = 7
	}

	// Workspace ignore patterns.
	setDefault(&cfg.Workspace.IgnoreDirs, []string{
		"vendor", "node_modules", "__pycache__",
		"dist", "build", "target", "bin", "obj",
	})
	setDefault(&cfg.Workspace.IgnoreExtensions, []string{
		".exe", ".dll", ".so", ".dylib", ".bin", ".dat",
		".zip", ".tar", ".gz", ".7z", ".rar",
		".jpg", ".jpeg", ".png", ".gif", ".svg", ".ico",
		".mp3", ".mp4", ".mov", ".avi", ".wav",
		".pdf", ".doc", ".docx", ".xls", ".xlsx",
		".ttf", ".otf", ".woff", ".woff2",
		".pyc", ".class", ".o", ".a", ".lib",
		".lock", ".min.js", ".min.css",
		".onnx", ".onnx_data",
	})
	setDefault(&cfg.Workspace.IgnoreFileNames, []string{})

	// Vector index / hybrid search defaults. The generic setDefault
	// helper only supports slices, so we hand-roll this for *bool.
	if cfg.VectorIndex.Hybrid == nil {
		trueVal := true
		cfg.VectorIndex.Hybrid = &trueVal
	}

	// Proxy defaults
	if cfg.Proxy.BypassList == nil {
		cfg.Proxy.BypassList = []string{"localhost", "127.0.0.1"}
	}
}
