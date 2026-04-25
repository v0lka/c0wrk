package config

// defaultProtectedTools is the default list of tools whose output is always preserved during pruning.
var defaultProtectedTools = []string{"store_fact", "search_facts"}

// ApplyDefaults sets default values for zero-value fields in the configuration.
func ApplyDefaults(cfg *Config) {
	// Log level defaults
	if cfg.LogLevel == "" {
		cfg.LogLevel = "DEBUG"
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
		cfg.Executor.Compaction.Hierarchical.EnabledAboveSteps = 40
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

	// Tool result budget defaults
	if cfg.Executor.ToolResultBudget.HardCapTokens == 0 {
		cfg.Executor.ToolResultBudget.HardCapTokens = 8192
	}
	if cfg.Executor.ToolResultBudget.MaxFillFraction == 0 {
		cfg.Executor.ToolResultBudget.MaxFillFraction = 0.3
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
	if cfg.ToolLimits.ReadMaxLineLength == 0 {
		cfg.ToolLimits.ReadMaxLineLength = 2000
	}
	if cfg.ToolLimits.ReadMaxBytes == 0 {
		cfg.ToolLimits.ReadMaxBytes = 51200 // 50KB
	}
	if cfg.ToolLimits.RipgrepMaxResults == 0 {
		cfg.ToolLimits.RipgrepMaxResults = 200
	}
	if cfg.ToolLimits.RipgrepMaxLineLength == 0 {
		cfg.ToolLimits.RipgrepMaxLineLength = 2000
	}
	if cfg.ToolLimits.GlobMaxResults == 0 {
		cfg.ToolLimits.GlobMaxResults = 200
	}
	if cfg.ToolLimits.FileSearchMaxMatches == 0 {
		cfg.ToolLimits.FileSearchMaxMatches = 100
	}
	if cfg.ToolLimits.WebSearchMaxResults == 0 {
		cfg.ToolLimits.WebSearchMaxResults = 5
	}
	if cfg.ToolLimits.WebFetchMaxBodySize == 0 {
		cfg.ToolLimits.WebFetchMaxBodySize = 102400 // 100KB
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
	// Synthetic plan threshold default (0 means not set, use default of 2)
	// Note: We check for 0 and set to 2, but 0 is also a valid value (disable synthetic plans).
	// This is acceptable because the zero value behavior matches the default.
	if cfg.Orchestration.SyntheticPlanThreshold == 0 {
		cfg.Orchestration.SyntheticPlanThreshold = 2
	}
}
