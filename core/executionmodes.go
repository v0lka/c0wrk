package core

// ExecutionMode values selected by the user via the chat input toggles and
// passed through HandleOptions.ExecutionMode to the orchestrator. The mode
// controls plan granularity (single-step vs multi-step) and is consulted by
// Orchestrator.shouldUseSingleStep.
const (
	// ExecutionModeNormal produces exactly one comprehensive step. Best for
	// regular tasks that don't need DAG decomposition.
	ExecutionModeNormal = "normal"

	// ExecutionModeAdvanced produces a multi-step DAG with parallelization
	// and per-step domain selection. Used for complex multi-step tasks.
	ExecutionModeAdvanced = "advanced"
)
