package orchestration

import (
	"github.com/user/agent/sdk/agent"
	"github.com/user/agent/sdk/llm"
	"github.com/user/agent/sdk/tools"
)

// Config holds configuration for the Orchestrator.
type Config struct {
	// Required
	Planner      Planner
	LLM          agent.LLMCaller
	Tools        agent.ToolExecutor
	ToolRegistry *tools.ToolRegistry
	TokenCounter llm.TokenCounter

	// Optional infrastructure
	ModelRegistry  *llm.ModelRegistry
	ContextFactory ContextManagerFactory
	StateFactory   BlackboardFactory   // optional, nil = new MapBlackboard per task
	Events         Events              // optional, nil = NoopEvents
	SystemPrompt   SystemPromptFactory // optional, nil = default minimal prompt

	// Optional strategies
	Reflection Reflector // optional, nil = replan without insights

	// ContextSetup is called after ContextFactory creates a CM and before the executor runs.
	// Allows consumers to inject task-specific context (e.g., SetTask) into the CM.
	ContextSetup func(cm agent.ContextManager, taskDesc string)

	// Tuning
	MaxRetries       int
	MaxSteps         int
	ToolResultBudget agent.ToolResultBudget

	// StepConfigurator resolves step-specific execution parameters from a PlanStep.
	// If nil, default values are used (all tools, cfg.MaxSteps, no custom prompt).
	StepConfigurator StepConfigurator
}

// StepConfigurator resolves step-specific execution parameters from a PlanStep.
type StepConfigurator func(step PlanStep, defaults StepDefaults) StepConfig

// StepDefaults holds default values passed to a StepConfigurator.
type StepDefaults struct {
	MaxSteps int
	AllTools []tools.ToolDescriptor
}

// StepConfig holds resolved step-specific execution parameters.
type StepConfig struct {
	MaxSteps           int
	AllowedTools       []tools.ToolDescriptor // filtered tool set (empty = use all)
	SystemPrompt       string                 // custom prompt override (empty = use factory)
	CompactionStrategy string                 // compaction strategy name (empty = default)
}
