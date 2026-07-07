package agent

// DefaultCircuitBreakerConfig returns sensible defaults for executor circuit breaker thresholds.
//
// Values are calibrated against real-session telemetry (80 sessions, 251 executor
// runs): the previous defaults (fruitless 4/6, sameTool 6/10, maxResultLen 32)
// triggered false aborts during legitimate batch file edits — edit_file success
// is 24 bytes ("successfully edited file"), well below the 32-byte fruitless
// threshold. Mutating tools and meta-tools are now excluded in the detectors
// (see circuitBreakerExemptTools), so the thresholds below apply only to
// exploration/search tools where consecutive short results genuinely indicate
// a stuck loop.
func DefaultCircuitBreakerConfig() CircuitBreakerConfig {
	return CircuitBreakerConfig{
		RepeatNudgeThreshold:         3,
		RepeatAbortThreshold:         5,
		TruncationAbortThreshold:     3,
		ParseErrorAbortThreshold:     3,
		FruitlessNudgeThreshold:      5,
		FruitlessAbortThreshold:      8,
		FruitlessMaxResultLen:        48,
		SameToolRepeatNudgeThreshold: 8,
		SameToolRepeatAbortThreshold: 12,
		SameToolResultSizeDelta:      128,
	}
}

// DefaultToolResultBudget returns sensible defaults for tool result truncation.
func DefaultToolResultBudget() ToolResultBudget {
	return ToolResultBudget{
		HardCapTokens:   30000,
		MaxFillFraction: 0.4,
	}
}

// DefaultToolTruncationConfig returns sensible per-tool truncation defaults.
// The returned map is a fresh copy; callers may freely modify it.
func DefaultToolTruncationConfig() map[string]ToolTruncationConfig {
	return map[string]ToolTruncationConfig{
		"read_file":      {MaxLines: 50000},
		"ripgrep":        {MaxLines: 5000},
		"glob":           {MaxLines: 5000},
		"list_directory": {MaxLines: 5000},
		"web_fetch":      {MaxBytes: 2097152},
		"bash_exec":      {MaxLines: 10000},
	}
}
