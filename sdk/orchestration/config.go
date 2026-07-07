package orchestration

import (
	"log/slog"

	"github.com/v0lka/sp4rk/agent"
	"github.com/v0lka/sp4rk/llm"
	"github.com/v0lka/sp4rk/tools"
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

	// CallerForStep returns a step-local LLMCaller for the given ContextManager and step ID.
	// When set, the orchestrator uses this instead of the shared LLM field to create
	// per-step executors, ensuring parallel steps get independent context trackers.
	// If nil, o.cfg.LLM is used for all steps.
	CallerForStep func(cm agent.ContextManager, stepID string) agent.LLMCaller

	// StepDumpTracker manages per-step LLM dump files for this orchestrator instance.
	// Created by the session layer. If nil, per-step dumps are a no-op.
	StepDumpTracker *StepDumpTracker

	// HITLHandler is called when an executor reaches its step limit.
	// If nil, a default handler is used (deny all extensions).
	HITLHandler agent.HITLHandler

	// Tuning
	MaxRetries                int
	MaxSteps                  int
	MaxDependencyContextChars int // default: 8000
	ToolResultBudget          agent.ToolResultBudget
	CircuitBreaker            agent.CircuitBreakerConfig
	PreWarningPercent         int // context fill % for pre-compaction store_fact nudge (0 = disabled)

	// Tool result caching and per-tool Stage 1 truncation.
	ToolCache         *agent.ToolResultCache
	PerToolTruncation map[string]agent.ToolTruncationConfig

	// ReasoningEffort is the reasoning effort applied to step executors.
	// When non-empty, each executor gets this value directly (no role adaptation).
	ReasoningEffort string

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
	KeepLastN          int                    // tool output pruning: keep last N results (0 = use global default)
	ProtectedTools     []string               // tool output pruning: always preserve these tools' output

	// AgentRole is the step's agent role (e.g. "researcher", "coder", "tester", "executor").
	// Used to resolve per-role reasoning effort. Empty defaults to "executor".
	AgentRole string

	// MutationRequired, when true, instructs the executor to reject finish calls
	// that are not preceded by at least one mutating tool execution (write_file,
	// edit_file, create_directory, delete_file, delete_directory). This prevents
	// "false success" on code-modification steps where the agent reads extensively
	// but finishes without making changes. Set by the StepConfigurator based on
	// the step's role and domain.
	MutationRequired bool
}
