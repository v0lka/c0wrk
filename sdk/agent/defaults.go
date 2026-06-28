package agent

// DefaultCircuitBreakerConfig returns sensible defaults for executor circuit breaker thresholds.
// These values are kept in sync with backend/config/defaults.go.
func DefaultCircuitBreakerConfig() CircuitBreakerConfig {
	return CircuitBreakerConfig{
		RepeatNudgeThreshold:         3,
		RepeatAbortThreshold:         4,
		TruncationAbortThreshold:     3,
		ParseErrorAbortThreshold:     3,
		FruitlessNudgeThreshold:      4,
		FruitlessAbortThreshold:      6,
		FruitlessMaxResultLen:        32,
		SameToolRepeatNudgeThreshold: 6,
		SameToolRepeatAbortThreshold: 10,
		SameToolResultSizeDelta:      64,
	}
}

// DefaultToolResultBudget returns sensible defaults for tool result truncation.
// These values are kept in sync with backend/config/defaults.go.
func DefaultToolResultBudget() ToolResultBudget {
	return ToolResultBudget{
		HardCapTokens:   30000,
		MaxFillFraction: 0.4,
	}
}

// DefaultToolTruncationConfig is a shared reference to sensible per-tool truncation defaults.
// Callers must not modify the returned map; copy it first if customization is needed.
var DefaultToolTruncationConfig = map[string]ToolTruncationConfig{
	"read_file":      {MaxLines: 50000},
	"ripgrep":        {MaxLines: 5000},
	"glob":           {MaxLines: 5000},
	"list_directory": {MaxLines: 5000},
	"web_fetch":      {MaxBytes: 2097152},
	"bash_exec":      {MaxLines: 10000},
}
