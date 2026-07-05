// Package core implements the orchestration engine including routing, planning, evaluation, reflection, and execution of LLM agent workflows.
package core

import (
	"time"

	"github.com/v0lka/c0wrk/sdk/agent"
	"github.com/v0lka/c0wrk/sdk/agent/router"
	"github.com/v0lka/c0wrk/sdk/orchestration"
	sdktools "github.com/v0lka/c0wrk/sdk/tools"
)

// NoProjectID is the well-known identifier for the "No Project" pseudo-project.
// Sessions under this project receive per-session workspaces and code-oriented
// tools are disabled.
const NoProjectID = "__no_project__"

// ---------------------------------------------------------------------------
// ContextManager — extends sdk/agent.ContextManager with c0wrk-specific SetTask
// ---------------------------------------------------------------------------

// ContextManager is the interface Executor and Orchestrator need for context window management.
// It extends sdk/agent.ContextManager with SetTask for c0wrk-specific task support.
type ContextManager interface {
	agent.ContextManager
	// SetTask sets the user's task into the context window.
	SetTask(task string)
}

// ---------------------------------------------------------------------------
// Emitter — c0wrk-specific event interface (superset of agent.AgentEvents)
// ---------------------------------------------------------------------------

// Emitter defines the interface for emitting agent execution events.
// Implementations must be nil-safe (all methods are no-ops when receiver is nil).
type Emitter interface {
	// Embed generic agent events from sdk/agent
	agent.AgentEvents

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

// noopEmitter is a no-op implementation of Emitter.
// Used as a default when nil emitter is provided.
type noopEmitter struct {
	agent.NoopEvents // provides AgentEvents methods
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
	TaskID          string   // non-empty = continuation of existing task
	UserSkills      []string // explicitly requested by user via /skill refs (bypass router)
	ModelOverride   string   // non-empty → use this model for all LLM calls; empty → router default
	ReasoningEffort string   // non-empty → native reasoning value for all LLM calls; empty → use family default
	SessionPlansDir string   // directory for session-scoped plan files (used by declare_plan tool)
}
