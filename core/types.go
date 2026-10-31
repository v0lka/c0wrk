// Package core implements the orchestration engine including routing, planning, evaluation, reflection, and execution of LLM agent workflows.
package core

import (
	"time"

	"github.com/v0lka/c0wrk/core/goal"
	"github.com/v0lka/c0wrk/core/tools"
	"github.com/v0lka/sp4rk/agent"
	"github.com/v0lka/sp4rk/agent/router"
	"github.com/v0lka/sp4rk/llm"
	"github.com/v0lka/sp4rk/orchestration"
	sdktools "github.com/v0lka/sp4rk/tools"
)

// NoProjectID is the well-known identifier for the "No Project" pseudo-project.
// Sessions under this project receive per-session workspaces and index-dependent
// tools (semantic_search) are disabled.
const NoProjectID = "__no_project__"

// ---------------------------------------------------------------------------
// ContextManager — extends github.com/v0lka/sp4rk/agent.ContextManager with c0wrk-specific SetTask
// ---------------------------------------------------------------------------

// ContextManager is the interface Executor and Orchestrator need for context window management.
// It extends github.com/v0lka/sp4rk/agent.ContextManager with SetTask for c0wrk-specific task support.
type ContextManager interface {
	agent.ContextManager
	// SetTask sets the user's task into the context window.
	SetTask(task string)
}

// ---------------------------------------------------------------------------
// Emitter — c0wrk-specific event interface (superset of agent.Events)
// ---------------------------------------------------------------------------

// Emitter defines the interface for emitting agent execution events.
// Implementations must be nil-safe (all methods are no-ops when receiver is nil).
type Emitter interface {
	// Embed generic agent events from github.com/v0lka/sp4rk/agent
	agent.Events

	// c0wrk-specific orchestration events
	Routing(mode, domain, complexity string)
	PlanGenerated(stepCount int, steps []orchestration.PlanStepEvent)
	PlanStepStart(stepID string, description, summary string)
	PlanStepComplete(stepID string, success bool, duration time.Duration, errMsg string)
	// PlanStepPaused emits a plan_step_paused event: the step stopped at a
	// cooperative pause checkpoint — recoverable, not a failure or completion;
	// a later Resume re-enters it from its checkpoint.
	// Payload contract: specs/contracts/event-catalog.md ("plan_step_paused").
	PlanStepPaused(stepID string, duration time.Duration, errMsg string)
	Reflection(reflection *orchestration.Reflection, attempt, maxAttempts int)
	Retry(attempt, maxAttempts int)
	StepRetry(stepID string, attempt, maxAttempts int)
	// Service emits a general service message without metadata.
	Service(content string)
	// ServiceWithMeta emits a service message with metadata for frontend filtering.
	// The meta map can contain arbitrary key-value pairs, e.g., {"phase": "orchestration"}.
	ServiceWithMeta(content string, meta map[string]any)
	// GoalStatus emits a dedicated goal_status session event (NOT the generic
	// service channel, so the goal store's live subscription receives it)
	// carrying the full goal-state snapshot. Payload contract:
	// specs/contracts/event-catalog.md ("goal_status").
	GoalStatus(data map[string]any)
	// GoalProgress emits the dedicated goal_progress session event with
	// turn/budget telemetry mid-loop. specs/contracts/event-catalog.md
	// ("goal_progress").
	GoalProgress(data map[string]any)
	// E2SState emits the dedicated e2s_state session event carrying the full
	// execution-state snapshot Σ, after every applied state patch. Payload
	// contract: specs/contracts/event-catalog.md ("e2s_state").
	E2SState(data map[string]any)
	// ReplanFailed reports a failed replan attempt.
	ReplanFailed(err error)
	// SkillsActivated reports the skills matched and activated for the current task.
	SkillsActivated(skillNames []string)
	// StepTodoUpdate emits a checklist update. stepID may be empty for a
	// standalone checklist (Conductor without a declared plan).
	StepTodoUpdate(stepID string, items []agent.TodoItem)
	// MemoryRead emits an event when the agent reads from its persistent memory (facts, reflections, etc.).
	MemoryRead(stepNum int, content string)
}

// PlanStepScopable is an optional interface that Emitter implementations
// can implement to support scoping events to a plan step.
type PlanStepScopable interface {
	WithPlanStepID(id string) Emitter
}

// RetryAttemptScopable is an optional interface that Emitter implementations
// can implement to tag events with a retry attempt number.
type RetryAttemptScopable interface {
	WithRetryAttempt(attempt int) Emitter
}

// CurrentStepScopable is an optional interface for dynamic plan-step scoping
// of inline Conductor execution: the inlineStepLifecycle calls SetCurrentStepID
// so subsequent executor events (tool_call, thought, …) nest under the step in
// the frontend. Unlike PlanStepScopable (a scoped COPY), SetCurrentStepID
// mutates in place, so one emitter instance is re-scoped as the Conductor
// moves between steps within one continuous ReAct loop.
type CurrentStepScopable interface {
	SetCurrentStepID(id string)
}

// AttachmentNameResolver is an optional interface for emitters whose
// read_attachment tool-call events carry the attachment's original file name.
// The orchestrator wires a blackboard-backed resolver at task setup, so the
// name is baked into the persisted metadata and survives restarts (when the
// frontend's in-memory cache is empty); an unknown id leaves the event
// unenriched and the frontend falls back to the id.
type AttachmentNameResolver interface {
	SetAttachmentNameResolver(resolve func(attachmentID string) string)
}

// GoalProposerSetter is implemented by the Orchestrator so the backend can
// inject the goal-proposer hook (the desktop approval flow propose_goal blocks
// on) after construction — it depends on backend state unavailable at
// construction time. Without a proposer, goal derivation fails fast rather
// than silently running a non-goal Conductor pass.
type GoalProposerSetter interface {
	SetGoalProposer(proposer tools.GoalProposer)
}

// DisplayContextWindowSetter is an optional interface for emitters that
// present context fill against the model's ADVERTISED window (the executor
// reports fill against the internal effective max; basis rationale:
// specs/domains/memory/compaction.md, "Manual Context Compaction"). The
// orchestrator resolves the meta and injects the window before the first
// context_fill emission.
type DisplayContextWindowSetter interface {
	SetDisplayContextWindow(window int)
}

// DisplayContextWindowForModelSetter is an optional interface for a
// LATE-ARRIVING, model-scoped display-window correction: the first fill of a
// task carries the catalog/fallback window (the self-hosted probe has not
// landed yet), and the probe callback pushes the runtime window through here.
// The model argument guards against a stale probe — when the model has since
// been switched, the update is dropped (the new model's own probe or the next
// emitInitialContextFill supplies the correct window).
type DisplayContextWindowForModelSetter interface {
	SetDisplayContextWindowForModel(model string, window int)
}

// LastModelSetter is an optional interface for emitters whose context_fill /
// session_tokens model is synchronized the instant a per-request override is
// applied: the emitter's cached "last model" otherwise lags until the first
// usage report, so the opening fill would show the PREVIOUS task's model. The
// orchestrator injects model+family inside ApplyRequestOverrides (mirroring
// DisplayContextWindowSetter).
type LastModelSetter interface {
	SetLastModel(model, family string)
}

// SessionTokenEmitter is an optional interface Emitter implementations can
// implement to receive cumulative session token totals after every tracked
// LLM call. The builder's UsageTracker observer discovers it via type
// assertion (see core/builder.go); emitters without it simply miss token
// accounting. loggingEmitter forwards it when the inner emitter supports it.
type SessionTokenEmitter interface {
	EmitSessionTokens(totalIn, totalOut int, model, family string)
}

// SessionTokenThroughputEmitter is the timing-augmented extension of
// SessionTokenEmitter: cumulative totals plus the median per-call
// output-token throughput and its sample count. The builder prefers this
// seam, falling back to SessionTokenEmitter. Warming semantics and payload:
// specs/contracts/event-catalog.md ("session_tokens").
type SessionTokenThroughputEmitter interface {
	EmitSessionTokensWithThroughput(totalIn, totalOut int, model, family string, medianOutputTokPerSec float64, throughputSamples int)
}

// noopEmitter is a no-op implementation of Emitter.
// Used as a default when nil emitter is provided.
type noopEmitter struct {
	agent.NoopEvents // provides Events methods
}

// ensure noopEmitter implements Emitter.
var _ Emitter = (*noopEmitter)(nil)

func (n *noopEmitter) Routing(_, _, _ string)                                       {}
func (n *noopEmitter) PlanGenerated(_ int, _ []orchestration.PlanStepEvent)         {}
func (n *noopEmitter) PlanStepStart(_, _, _ string)                                 {}
func (n *noopEmitter) PlanStepComplete(_ string, _ bool, _ time.Duration, _ string) {}
func (n *noopEmitter) PlanStepPaused(_ string, _ time.Duration, _ string)           {}
func (n *noopEmitter) Reflection(_ *orchestration.Reflection, _, _ int)             {}
func (n *noopEmitter) Retry(_, _ int)                                               {}
func (n *noopEmitter) StepRetry(_ string, _, _ int)                                 {}
func (n *noopEmitter) Service(_ string)                                             {}
func (n *noopEmitter) ServiceWithMeta(_ string, _ map[string]any)                   {}
func (n *noopEmitter) GoalStatus(_ map[string]any)                                  {}
func (n *noopEmitter) GoalProgress(_ map[string]any)                                {}
func (n *noopEmitter) E2SState(_ map[string]any)                                    {}
func (n *noopEmitter) ReplanFailed(_ error)                                         {}
func (n *noopEmitter) SkillsActivated(_ []string)                                   {}
func (n *noopEmitter) StepTodoUpdate(_ string, _ []agent.TodoItem)                  {}
func (n *noopEmitter) MemoryRead(_ int, _ string)                                   {}

// ---------------------------------------------------------------------------
// c0wrk-specific types
// ---------------------------------------------------------------------------
// ExecutorConfig — configuration for the Executor (AD 4.4).
type ExecutorConfig struct {
	MaxSteps           int
	CompactionStrategy string // will be resolved to actual strategy by memory package
	Tools              []sdktools.ToolDescriptor
}

// TaskDefinition — defines a task for the Executor.
type TaskDefinition struct {
	Task  string                    `json:"task"`
	Tools []sdktools.ToolDescriptor `json:"tools"`
}

// HandleResult — result of Orchestrator.Handle.
type HandleResult struct {
	Output          string                        `json:"output"`
	RoutingDecision *router.RoutingDecision       `json:"routing_decision"`
	Plan            *orchestration.Plan           `json:"plan,omitempty"`
	Blackboard      orchestration.Blackboard      `json:"-"`
	Reflections     []orchestration.Reflection    `json:"reflections,omitempty"`
	Status          orchestration.ExecutionStatus `json:"status,omitempty"`
	// Err is the typed terminal cause of a DEGRADED outcome — non-nil only
	// when the run returned a best-effort result with a nil error (today: the
	// goal loop's errored-turn halt). It preserves the error VALUE so the
	// session manager's auto-retry classifier can errors.As on the *llm.Error
	// chain (ADR-065); it is deliberately NOT set when the error is returned
	// directly (the transports would race for the same classification).
	// JSON-omitted: the frontend contract is unchanged.
	Err error `json:"-"`
}

// HandleOptions controls how a message is processed by HandleMessage.
type HandleOptions struct {
	TaskID             string                     // non-empty = continuation of existing task
	UserSkills         []string                   // explicitly requested by user via /skill refs (bypass router)
	UserAgents         []string                   // explicitly requested by user via /-mentions (plain or /agent:-qualified; drives the "Requested Subagents" prompt directive)
	UserMCPServers     []string                   // MCP servers explicitly mentioned by user via /-mentions (plain or /mcp:-qualified); enables manual-mode servers task-wide and drives the soft "Requested MCP Servers" prompt directive
	ModelOverride      string                     // non-empty → use this model for all LLM calls; empty → router default
	ReasoningEffort    string                     // non-empty → native reasoning value for all LLM calls; empty → use family default
	SessionPlansDir    string                     // directory for session-scoped plan files (used by declare_plan tool)
	PendingAttachments []orchestration.Attachment // attachments staged by AttachFiles, flushed into the blackboard before execution
	PendingImages      []llm.ContentBlock         // image attachments staged by AttachFiles, passed to the context window as image content blocks

	// ReviewMode marks a message as carrying code review feedback: the user's
	// message holds the review comments, and the system prompt gains a
	// "Code Review" section instructing the agent to address them by editing
	// code. See specs/domains/review.md.
	ReviewMode bool

	// Goal selects goal mode: HandleMessage dispatches to runGoalLoop, which
	// derives a {condition, verify} goal via propose_goal (with user
	// sign-off) and iterates the Conductor turn-by-turn until met, budget
	// exhaustion, anti-spin idle, or pause. Entered on BOTH a fresh task and a
	// continuation. Loop contract: specs/domains/goal-mode.md.
	Goal bool

	// GoalBudgetOverride, when non-nil, tightens the goal's resource caps below
	// the config defaults. Applied at goal activation (turn 1) AFTER config
	// defaults: any field > 0 / non-zero overrides the default; zero-valued
	// fields fall back to the config default. Only meaningful when Goal is true.
	GoalBudgetOverride *goal.GoalBudget

	// E2S selects the explicit-execution-state mode: HandleMessage dispatches
	// to runE2SLoop (externalized state Σ patched per turn, O(1) context,
	// dispatch through the registry with every security gate intact).
	// Mutually exclusive with Goal — an explicit error, never silent
	// preference. See specs/domains/e2s.md.
	E2S bool
}
