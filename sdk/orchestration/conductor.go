package orchestration

import (
	"context"
	"errors"
	"fmt"
	"strings"
	"sync"

	"github.com/v0lka/sp4rk/agent"
	"github.com/v0lka/sp4rk/llm"
	"github.com/v0lka/sp4rk/tools"
)

// ConductorConfig holds the dependencies for a Conductor: a single ReAct loop
// that owns a task end-to-end. This is the sp4rk-level primitive; the host
// application's core layer wraps it with Conductor-specific tools (delegate,
// declare_plan, reflect, cancel_delegation) via context injection before
// calling Run.
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

	// NonCacheableTools lists additional tool names whose results should not be
	// cached. These are consumer-specific meta-tools (e.g. delegate, declare_plan)
	// that extend the sp4rk-provided defaults. Empty = sp4rk defaults only.
	NonCacheableTools []string

	// ConversationHistory holds prior user/assistant exchanges from the
	// session. When non-empty, the Conductor injects it into the
	// ContextManager so the LLM sees the dialogue context leading up to the
	// current message. Without this, a follow-up like "implement variant a"
	// has no referent — the Conductor only sees the current message.
	ConversationHistory []llm.Message
}

// Conductor runs a single Executor.Run that owns a task end-to-end.
// This is the sp4rk primitive; the host application adds Conductor-specific tools
// (delegate, declare_plan, reflect, cancel_delegation) through context
// injection before calling Run.
//
// Conductor is safe for concurrent use: SetReasoningEffort may be called from
// one goroutine while Run executes on another. The reasoning-effort field is
// guarded by mu; all other fields are set only at construction time and read
// during a single Run, so they require no additional synchronization.
type Conductor struct {
	cfg ConductorConfig
	mu  sync.RWMutex
}

// NewConductor creates a Conductor from the given config.
func NewConductor(cfg ConductorConfig) *Conductor {
	if cfg.MaxSteps == 0 {
		cfg.MaxSteps = 80
	}
	return &Conductor{cfg: cfg}
}

// SetReasoningEffort updates the reasoning effort for subsequent runs.
// Safe to call concurrently with Run.
func (c *Conductor) SetReasoningEffort(effort string) {
	c.mu.Lock()
	defer c.mu.Unlock()
	c.cfg.ReasoningEffort = effort
}

// Run launches the Conductor: a single Executor.Run that owns the task.
// The caller is responsible for injecting Conductor-specific context values
// (DelegationRegistry, DelegationLauncher, PlanPublisher, ReflectionRunner,
// TrajectoryStore) into ctx before calling Run if those tools are desired.
// The method injects StepOutputStore and FactStore from the blackboard.
//
// compactionStrategy selects the context compaction strategy: "sliding_window",
// "summarization", or "hierarchical". The caller derives this from the
// routing domain and complexity.
//
// events may be nil; a NoopEvents instance is used in that case.
func (c *Conductor) Run(
	ctx context.Context,
	message string,
	bb Blackboard,
	availableTools []tools.ToolDescriptor,
	events agent.Events,
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
	// Resolve always returns usable metadata — the ok flag indicates whether
	// the model was found in a known source, but the fallback (ContextWindow=128000,
	// OutputLimit=4096) is always usable. Using the fallback when ok=false is
	// critical: a zero ContextWindow disables compaction entirely, causing the
	// conversation to grow unbounded until the API rejects it.
	var modelMeta llm.ModelMetadata
	if c.cfg.ModelRegistry != nil {
		modelMeta, _ = c.cfg.ModelRegistry.Resolve(ctx, c.cfg.Model)
	}
	if modelMeta.ContextWindow == 0 {
		modelMeta.ContextWindow = 128000
		modelMeta.OutputLimit = 4096
		modelMeta.TokenizerType = "approximate"
	}

	systemPrompt := c.cfg.SystemPrompt(ctx, message, modelMeta)

	cm := c.cfg.ContextFactory(systemPrompt, modelMeta, compactionStrategy)
	if ccm, ok := cm.(TaskAware); ok {
		ccm.SetTask(message)
	}

	// Inject prior conversation (previous exchanges) so the LLM sees the
	// dialogue context leading up to the current message. The ContextManager
	// must implement ConversationAware — sp4rk's memory.ContextWindow does.
	if len(c.cfg.ConversationHistory) > 0 {
		if pcm, ok := cm.(ConversationAware); ok {
			pcm.SetPriorConversation(c.cfg.ConversationHistory)
		}
	}

	// Build the executor caller: wire context tracker correction if the
	// context manager exposes one (TrackerProvider) and the caller supports
	// tracker injection.
	caller := c.cfg.LLM
	if ctm, ok := cm.(TrackerProvider); ok {
		if tc, ok2 := caller.(interface {
			WithContextTracker(*llm.ContextTokenTracker) agent.LLMCaller
		}); ok2 {
			caller = tc.WithContextTracker(ctm.ContextTracker())
		}
	}

	// The finish-join guard is implemented via a FinishGuard callback on the
	// Executor (see SetFinishGuard below) rather than wrapping the tool
	// executor, because finish is handled inline by the executor and never
	// reaches tools.Execute(). The callback checks for pending async
	// delegations and rejects finish with a nudge when any are still running.
	executor := agent.NewExecutor(
		caller,
		c.cfg.Tools,
		c.cfg.MaxSteps,
		agent.WithTokenCounter(c.cfg.TokenCounter),
		agent.WithEvents(events),
		agent.WithToolResultBudget(c.cfg.ToolResultBudget),
		agent.WithCircuitBreaker(c.cfg.CircuitBreaker),
		agent.WithHITL(c.cfg.HITLHandler),
	)
	// Read reasoning effort under the read lock so a concurrent
	// SetReasoningEffort call cannot race with this snapshot.
	c.mu.RLock()
	effort := c.cfg.ReasoningEffort
	c.mu.RUnlock()
	if effort != "" {
		executor.SetReasoningEffort(effort)
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
	if len(c.cfg.NonCacheableTools) > 0 {
		executor.AddNonCacheableTools(c.cfg.NonCacheableTools...)
	}

	// Finish-join guard: reject finish when pending async delegations exist,
	// preventing the Conductor from abandoning background work silently.
	executor.SetFinishGuard(func(ctx context.Context) error {
		if reg := delegationRegistryFromContext(ctx); reg != nil {
			if pending := reg.ListPending(); len(pending) > 0 {
				return fmt.Errorf("you have %d pending async delegation(s): %s. Call cancel_delegation for each if you no longer need them, or wait for them to complete via read_step_output before calling finish", len(pending), strings.Join(pending, ", "))
			}
		}
		return nil
	})

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
		if errors.Is(err, context.Canceled) || ctx.Err() != nil {
			status = ExecutionStatusCancelled
		} else {
			status = ExecutionStatusFailed
		}
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

// --- Minimal DelegationRegistry interface for finish-join ---
//
// The sp4rk Conductor needs to check for pending async delegations to
// implement the finish-join guard. Rather than importing a full
// DelegationRegistry (which would create a circular dependency), sp4rk
// defines a minimal interface that the host application's registry satisfies
// structurally.

// PendingDelegations is implemented by the host application's delegation
// registry. The sp4rk Conductor uses it to check for pending async delegations.
type PendingDelegations interface {
	ListPending() []string
}

type delegationRegistryContextKey struct{}

// WithDelegationRegistry injects a PendingDelegations into the context.
// The host application calls this before Conductor.Run.
func WithDelegationRegistry(ctx context.Context, reg PendingDelegations) context.Context {
	return context.WithValue(ctx, delegationRegistryContextKey{}, reg)
}

func delegationRegistryFromContext(ctx context.Context) PendingDelegations {
	if v, ok := ctx.Value(delegationRegistryContextKey{}).(PendingDelegations); ok {
		return v
	}
	return nil
}
