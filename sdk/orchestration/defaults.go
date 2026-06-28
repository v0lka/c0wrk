package orchestration

import (
	"github.com/v0lka/c0wrk/sdk/agent"
)

// DefaultOrchestrationConfig returns sensible defaults for the Orchestrator Config.
// This is the canonical source of orchestrator defaults. Use as a starting point
// and override specific fields as needed.
func DefaultOrchestrationConfig() Config {
	return Config{
		MaxSteps:                  50,
		MaxRetries:                2,
		MaxDependencyContextChars: 8000,
		ToolResultBudget:          agent.DefaultToolResultBudget(),
		CircuitBreaker:            agent.DefaultCircuitBreakerConfig(),
	}
}
