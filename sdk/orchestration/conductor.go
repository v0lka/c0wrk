package orchestration

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"strings"

	"github.com/v0lka/c0wrk/sdk/agent"
	"github.com/v0lka/c0wrk/sdk/llm"
	"github.com/v0lka/c0wrk/sdk/tools"
)

// ConductorConfig holds the dependencies for a Conductor: a single ReAct loop
// that owns a task end-to-end. This is the SDK-level primitive; c0wrk's core
// layer wraps it with Conductor-specific tools (delegate, declare_plan,
// reflect, cancel_delegation) via context injection before calling Run.
type ConductorConfig struct {
	LLM               agent.LLMCaller
	Tools             agent.ToolExecutor
	ToolRegistry      *tools.ToolRegistry
	TokenCounter      llm.TokenCounter
	Model             string
	ModelRegistry     *llm.ModelRegistry
	ContextFactory    ContextManagerFactory
	SystemPrompt      SystemPromptFactory
	MaxSteps          int
	ToolResultBudget  agent.ToolResultBudget
	CircuitBreaker    agent.CircuitBreakerConfig
	HITLHandler       agent.HITLHandler
	ToolCache         *agent.ToolResultCache
	PerToolTruncation map[string]agent.ToolTruncationConfig
	ReasoningEffort   string
	PreWarningPercent int

	// ConversationHistory holds prior user/assistant exchanges from the
	// session. When non-empty, the Conductor injects it into the
	// ContextManager so the LLM sees the dialogue context leading up to the
	// current message. Without this, a follow-up like "implement variant a"
	// has no referent — the Conductor only sees the current message.
	ConversationHistory []llm.Message
}

// Conductor runs a single Executor.Run that owns a task end-to-end.
// This is the SDK primitive; the core layer adds Conductor-specific tools
// (delegate, declare_plan, reflect, cancel_delegation) through context
// injection before calling Run.
type Conductor struct {
	cfg ConductorConfig
}

// NewConductor creates a Conductor from the given config.
func NewConductor(cfg ConductorConfig) *Conductor {
	if cfg.MaxSteps == 0 {
		cfg.MaxSteps = 80
	}
	return &Conductor{cfg: cfg}
}

// SetReasoningEffort updates the reasoning effort for subsequent runs.
func (c *Conductor) SetReasoningEffort(effort string) {
	c.cfg.ReasoningEffort = effort
}

// Run launches the Conductor: a single Executor.Run that owns the task.
// The caller is responsible for injecting Conductor-specific context values
// (DelegationRegistry, DelegationLauncher, PlanPublisher, ReflectionRunner,
// TrajectoryStore) into ctx before calling Run if those tools are desired.
// The method injects StepOutputStore and FactStore from the blackboard.
//
// compactionStrategy selects the context compaction strategy: "sliding_window",
// "summarization", or "hierarchical". The caller (core layer) derives this
// from the routing domain and complexity per ADR-012.
//
// events may be nil; a NoopEvents instance is used in that case.
func (c *Conductor) Run(
	ctx context.Context,
	message string,
	bb Blackboard,
	availableTools []tools.ToolDescriptor,
	events agent.AgentEvents,
	compactionStrategy string,
) (*ExecutionResult, error) {
	if c.cfg.ContextFactory == nil {
		return nil, errors.New("conductor: context factory not configured")
	}
	if c.cfg.SystemPrompt == nil {
		return nil, errors.New("conductor: system prompt factory not configured")
	}
	if compactionStrategy == "" {
		compactionStrategy = "sliding_window"
	}

	if events == nil {
		events = &agent.NoopEvents{}
	}

	// Resolve model metadata for the system prompt and context window.
	modelMeta := llm.ModelMetadata{}
	if c.cfg.ModelRegistry != nil {
		if meta, ok := c.cfg.ModelRegistry.Resolve(ctx, c.cfg.Model); ok {
			modelMeta = meta
		} else if meta, ok := c.cfg.ModelRegistry.Resolve(ctx, ""); ok {
			modelMeta = meta
		}
	}

	systemPrompt := c.cfg.SystemPrompt(ctx, message, modelMeta)

	cm := c.cfg.ContextFactory(systemPrompt, modelMeta, compactionStrategy)
	if ccm, ok := cm.(interface{ SetTask(string) }); ok {
		ccm.SetTask(message)
	}

	// Inject prior conversation (previous exchanges) so the LLM sees the
	// dialogue context leading up to the current message. The ContextManager
	// must support SetPriorConversation — the SDK's memory.ContextWindow does.
	if len(c.cfg.ConversationHistory) > 0 {
		if pcm, ok := cm.(interface{ SetPriorConversation([]llm.Message) }); ok {
			pcm.SetPriorConversation(c.cfg.ConversationHistory)
		}
	}

	// Build the executor caller: wire context tracker correction if the
	// context manager exposes one.
	caller := c.cfg.LLM
	if ctm, ok := cm.(interface {
		ContextTracker() *llm.ContextTokenTracker
	}); ok {
		if tc, ok2 := caller.(interface {
			WithContextTracker(*llm.ContextTokenTracker) agent.LLMCaller
		}); ok2 {
			caller = tc.WithContextTracker(ctm.ContextTracker())
		}
	}

	// Wrap the tool executor with a finish-join guard: if a
	// DelegationRegistry is in the context and has pending async
	// delegations, finish returns a tool error instead of executing.
	// This prevents the Conductor from abandoning background work silently.
	// When no registry is in context (e.g. SDK standalone usage without
	// delegate tool), the guard is a transparent passthrough.
	toolExec := &finishJoinExecutor{inner: c.cfg.Tools}

	executor := agent.NewExecutor(
		caller,
		toolExec,
		c.cfg.TokenCounter,
		c.cfg.MaxSteps,
		events,
		false, // suppressAssistantEvents — Conductor streams assistant events
		c.cfg.ToolResultBudget,
		c.cfg.CircuitBreaker,
		c.cfg.HITLHandler,
	)
	if c.cfg.ReasoningEffort != "" {
		executor.SetReasoningEffort(c.cfg.ReasoningEffort)
	}
	if c.cfg.ToolCache != nil {
		executor.SetToolCache(c.cfg.ToolCache)
	}
	if c.cfg.PerToolTruncation != nil {
		executor.SetPerToolTruncation(c.cfg.PerToolTruncation)
	}
	if c.cfg.PreWarningPercent > 0 {
		executor.SetPreWarningPercent(c.cfg.PreWarningPercent)
	}

	// Inject step output store + fact store + final result store so tools
	// (read_step_output, read_final_result, store_fact, search_facts) can
	// access the blackboard. The final result store exposes the prior task's
	// outcome to a continuation agent when the conversation history alone is
	// insufficient (e.g. after a restart, or when the result was too large
	// to inject verbatim).
	ctx = agent.WithStepOutputStore(ctx, NewStepOutputStore(bb))
	ctx = agent.WithFactStore(ctx, NewFactStore(bb))
	ctx = agent.WithFinalResultStore(ctx, NewFinalResultStore(bb))

	result, err := executor.Run(ctx, availableTools, cm)
	status := ExecutionStatusSuccess
	if err != nil {
		status = ExecutionStatusFailed
	} else if result == nil || !result.Finished {
		status = ExecutionStatusPartial
	}

	output := ""
	if result != nil {
		output = result.Output
	}
	if err != nil && output == "" {
		output = err.Error()
	}

	// If a DelegationRegistry is in context, note any pending async
	// delegations that were not cancelled or completed.
	if reg := delegationRegistryFromContext(ctx); reg != nil {
		if pending := reg.ListPending(); len(pending) > 0 && err == nil {
			output += fmt.Sprintf("\n\n[Note: %d async delegation(s) still pending: %s]", len(pending), strings.Join(pending, ", "))
		}
	}

	return &ExecutionResult{
		Output:      output,
		Blackboard:  bb,
		Status:      status,
		Reflections: bb.GetReflections(),
	}, err
}

// Cleanup releases resources held by the conductor. Currently a no-op;
// per-step dump cleanup is owned by the session layer.
func (c *Conductor) Cleanup() {}

// finishJoinExecutor wraps agent.ToolExecutor to intercept finish calls when
// the Conductor has pending async delegations. If finish is called while
// delegations are still pending (not cancelled or completed), the wrapper
// returns a tool error instead of executing finish — preventing the Conductor
// from abandoning background work silently.
type finishJoinExecutor struct {
	inner agent.ToolExecutor
}

func (f *finishJoinExecutor) Execute(ctx context.Context, name string, input json.RawMessage) (tools.ToolResult, error) {
	if name == "finish" {
		if reg := delegationRegistryFromContext(ctx); reg != nil {
			if pending := reg.ListPending(); len(pending) > 0 {
				return tools.ToolResult{
					Content: fmt.Sprintf("You have %d pending async delegation(s): %s. Call cancel_delegation for each if you no longer need them, or wait for them to complete via read_step_output before calling finish.", len(pending), strings.Join(pending, ", ")),
					IsError: true,
				}, nil
			}
		}
	}
	return f.inner.Execute(ctx, name, input)
}

func (f *finishJoinExecutor) GetToolSource(name string) string {
	return f.inner.GetToolSource(name)
}

func (f *finishJoinExecutor) IsToolUntrusted(name string) bool {
	return f.inner.IsToolUntrusted(name)
}

// --- Minimal DelegationRegistry interface for finish-join ---
//
// The SDK Conductor needs to check for pending async delegations to
// implement the finish-join guard. Rather than importing the full
// core/tools.DelegationRegistry (which would create a circular dependency:
// core/tools imports sdk/tools), the SDK defines a minimal interface that
// the registry satisfies structurally. The actual registry lives in
// core/tools/delegation_registry.go.

// PendingDelegations is implemented by DelegationRegistry (core/tools).
// The SDK Conductor uses it to check for pending async delegations.
type PendingDelegations interface {
	ListPending() []string
}

type delegationRegistryContextKey struct{}

// WithDelegationRegistry injects a PendingDelegations into the context.
// The core layer calls this before Conductor.Run.
func WithDelegationRegistry(ctx context.Context, reg PendingDelegations) context.Context {
	return context.WithValue(ctx, delegationRegistryContextKey{}, reg)
}

func delegationRegistryFromContext(ctx context.Context) PendingDelegations {
	if v, ok := ctx.Value(delegationRegistryContextKey{}).(PendingDelegations); ok {
		return v
	}
	return nil
}
