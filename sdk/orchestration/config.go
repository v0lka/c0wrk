package orchestration

import (
	"log/slog"

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

	// Model is the active model name used for ModelRegistry.Resolve() calls.
	// Must match the model name configured on the LLM router so that context
	// window management uses correct metadata (context size, output limit, tokenizer).
	Model string

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

	// CallerForStep returns a step-local LLMCaller for the given ContextManager.
	// When set, the orchestrator uses this instead of the shared LLM field to create
	// per-step executors, ensuring parallel steps get independent context trackers.
	// If nil, o.cfg.LLM is used for all steps.
	CallerForStep func(cm agent.ContextManager) agent.LLMCaller

	// StepLimitFunc is called when an executor reaches its step limit.
	// If nil, the executor will stop with a budget exhausted error.
	StepLimitFunc agent.StepLimitFunc

	// Tuning
	MaxRetries                int
	MaxSteps                  int
	MaxDependencyContextChars int // default: 8000
	ToolResultBudget          agent.ToolResultBudget
	CircuitBreaker            agent.CircuitBreakerConfig

	// StepConfigurator resolves step-specific execution parameters from a PlanStep.
	// If nil, default values are used (all tools, cfg.MaxSteps, no custom prompt).
	StepConfigurator StepConfigurator

	// Logger is the structured logger for the orchestrator. If nil, slog.Default() is used.
	Logger *slog.Logger
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
	SystemPromptSuffix string                 // appended to system prompt (empty = no suffix)
	CompactionStrategy string                 // compaction strategy name (empty = default)
}
