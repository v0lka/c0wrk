package memory

// DefaultCompactionThresholds returns sensible defaults for context window compaction triggers.
// Keep in sync with backend/config/defaults.go and sdk/framework.go.
func DefaultCompactionThresholds() CompactionThresholds {
	return CompactionThresholds{
		PredictivePercent: 85,
		WarningPercent:    92,
		EmergencyPercent:  98,
	}
}

// DefaultToolOutputPruning returns sensible defaults for tool output pruning.
// Keep in sync with backend/config/defaults.go.
func DefaultToolOutputPruning() ToolOutputPruning {
	return ToolOutputPruning{
		KeepLastN:        3,
		ThresholdPercent: 50,
	}
}
