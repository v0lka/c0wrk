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
// Sessions under this project receive per-session workspaces and code-oriented
// tools are disabled.
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
	Reflection(reflection *orchestration.Reflection, attempt, maxAttempts int)
	Retry(attempt, maxAttempts int)
	StepRetry(stepID string, attempt, maxAttempts int)
	// Service emits a general service message without metadata.
	Service(content string)
	// ServiceWithMeta emits a service message with metadata for frontend filtering.
	// The meta map can contain arbitrary key-value pairs, e.g., {"phase": "orchestration"}.
	ServiceWithMeta(content string, meta map[string]any)
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

// CurrentStepScopable is an optional interface that Emitter implementations
// can implement to support dynamic plan-step scoping for inline Conductor
// execution. When the Conductor executes a plan step inline (without
// delegating to a subagent), the inlineStepLifecycle calls SetCurrentStepID
// to tag subsequent executor events (tool_call, thought, assistant, etc.)
// with plan_step_id so they nest under the step block in the frontend.
//
// Unlike PlanStepScopable (which returns a scoped copy with a fixed
// planStepID), SetCurrentStepID mutates the receiver in place, allowing a
// single emitter instance to be re-scoped as the Conductor moves between
// steps within one continuous ReAct loop.
type CurrentStepScopable interface {
	SetCurrentStepID(id string)
}

// AttachmentNameResolver is an optional interface that Emitter implementations
// can implement so that read_attachment tool-call events are enriched with the
// attachment's original file name. The orchestrator wires a resolver backed by
// the task's blackboard (which holds both freshly-attached and
// continuation/resume-rehydrated attachments) when the blackboard is set up,
// before any tool executes. This bakes the human-readable name into the
// persisted tool-call metadata, so read_attachment cards render the file name
// even after an app restart — when the frontend's in-memory name cache is
// empty and the blackboard is no longer resident. Returning an empty string
// (unknown id) leaves the event unenriched; the frontend falls back to the id.
type AttachmentNameResolver interface {
	SetAttachmentNameResolver(resolve func(attachmentID string) string)
}

// GoalProposerSetter is implemented by the Orchestrator so the backend can
// inject the goal-proposer hook (the desktop approval flow that
// propose_goal blocks on) AFTER construction. It mirrors the
// AttachmentNameResolver pattern: the proposer depends on backend state (the
// pending-confirmation channel + event emitter) that is not available at
// Orchestrator construction time, so it is wired later by the session layer.
// Without a proposer, goal derivation fails fast rather than silently running
// a non-goal Conductor pass.
type GoalProposerSetter interface {
	SetGoalProposer(proposer tools.GoalProposer)
}

// DisplayContextWindowSetter is an optional interface that Emitter
// implementations can implement to present context-fill information relative
// to the model's advertised context window.
//
// The agent's internal compaction logic operates on an "effective max"
// (context window − output limit − safety margin), and the executor reports
// fill relative to that internal ceiling. The user-facing display, however,
// must reflect the real context window so the status bar doesn't expose
// internal compaction thresholds. The orchestrator resolves the model meta
// and injects the advertised window via SetDisplayContextWindow before the
// first context_fill emission.
type DisplayContextWindowSetter interface {
	SetDisplayContextWindow(window int)
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
func (n *noopEmitter) Reflection(_ *orchestration.Reflection, _, _ int)             {}
func (n *noopEmitter) Retry(_, _ int)                                               {}
func (n *noopEmitter) StepRetry(_ string, _, _ int)                                 {}
func (n *noopEmitter) Service(_ string)                                             {}
func (n *noopEmitter) ServiceWithMeta(_ string, _ map[string]any)                   {}
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
}

// HandleOptions controls how a message is processed by HandleMessage.
type HandleOptions struct {
	TaskID             string                     // non-empty = continuation of existing task
	UserSkills         []string                   // explicitly requested by user via /skill refs (bypass router)
	ModelOverride      string                     // non-empty → use this model for all LLM calls; empty → router default
	ReasoningEffort    string                     // non-empty → native reasoning value for all LLM calls; empty → use family default
	SessionPlansDir    string                     // directory for session-scoped plan files (used by declare_plan tool)
	PendingAttachments []orchestration.Attachment // attachments staged by AttachFiles, flushed into the blackboard before execution
	PendingImages      []llm.ContentBlock         // image attachments staged by AttachFiles, passed to the context window as image content blocks

	// ReviewMode marks a message as carrying code review feedback the agent
	// must act on. Set by the backend when a review is submitted (review
	// status == "submitted"): the user's message contains the review comments
	// (general + per-hunk). When true, HandleMessage sets ReviewModeKey so the
	// system prompt gains a "Code Review" section instructing the agent to
	// address the comments by editing code rather than merely acknowledging
	// them. See specs/domains/review.md.
	ReviewMode bool

	// Goal selects goal mode. When true, HandleMessage dispatches to
	// runGoalLoop: the orchestrator first derives a crisp {condition, verify}
	// goal via propose_goal (with user sign-off), then iterates the Conductor
	// turn-by-turn until the agent declares the goal met (via
	// declare_goal_status), the budget is exhausted, the agent goes idle
	// (anti-spin), or the goal is paused. Goal mode is entered on BOTH a fresh
	// task (TaskID == "") and a continuation (TaskID != ""): on a continuation
	// the prior task's blackboard is restored and the goal loop runs on the
	// inherited facts/history, deriving a fresh goal from the new message
	// (reusing the restored routing via routeOrContinue).
	Goal bool

	// GoalBudgetOverride, when non-nil, tightens the goal's resource caps below
	// the config defaults. Applied at goal activation (turn 1) AFTER config
	// defaults: any field > 0 / non-zero overrides the default; zero-valued
	// fields fall back to the config default. Only meaningful when Goal is true.
	GoalBudgetOverride *goal.GoalBudget
}
