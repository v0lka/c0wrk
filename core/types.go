// Package core implements the orchestration engine including routing, planning, evaluation, reflection, and execution of LLM agent workflows.
package core

import (
	"log/slog"
	"time"

	"github.com/user/agent/sdk/agent"
	"github.com/user/agent/sdk/orchestration"
	tools "github.com/user/agent/sdk/tools" // alias: avoids collision with core/tools subpackage
)

// ---------------------------------------------------------------------------
// Type aliases for types that moved to sdk/agent
// ---------------------------------------------------------------------------

// Step — single iteration of the ReAct loop.
type Step = agent.Step

// ExecutorResult — result of Executor.Run.
type ExecutorResult = agent.ExecutorResult

// SubAgentResult — result from a SubAgent.
type SubAgentResult = agent.SubAgentResult

// FillCheck represents the result of a context window fill check.
type FillCheck = agent.FillCheck

// SharedWorkspace provides inter-agent communication via named artifacts.
type SharedWorkspace = agent.SharedWorkspace

// Artifact represents a named output produced by an agent step.
type Artifact = agent.Artifact

// FileChange represents a filesystem modification made by an agent step.
type FileChange = agent.FileChange

// ToolResultBudget — tool result truncation config.
type ToolResultBudget = agent.ToolResultBudget

// LLMCaller is the interface Executor needs from the LLM layer.
type LLMCaller = agent.LLMCaller

// ToolExecutor is the interface Executor needs from the tools layer.
type ToolExecutor = agent.ToolExecutor

// CompactionStrategy defines an algorithm for compressing step history.
type CompactionStrategy = agent.CompactionStrategy

// NewSharedWorkspace creates a new empty SharedWorkspace.
var NewSharedWorkspace = agent.NewSharedWorkspace

// ---------------------------------------------------------------------------
// Type aliases for types that moved to sdk/orchestration
// ---------------------------------------------------------------------------

// CompletedStep — result of an executed plan step (AD 4.7).
type CompletedStep = orchestration.CompletedStep

// EvalResult — result of Evaluator checking AC (AD 4.5).
type EvalResult = orchestration.EvalResult

// EvalDetail — detail for a single AC evaluation (AD 4.5).
type EvalDetail = orchestration.EvalDetail

// IntentVerification holds the result of Tier 2 intent-based verification.
type IntentVerification = orchestration.VerificationResult

// PlanStepEvent represents a single step in a plan for event emission.
type PlanStepEvent = orchestration.PlanStepEvent

// EvalCriterionEvent represents a single evaluation criterion for event emission.
type EvalCriterionEvent = orchestration.EvalCriterionEvent

// ---------------------------------------------------------------------------
// ContextManager — extends sdk/agent.ContextManager with c0wrk-specific SetTask
// ---------------------------------------------------------------------------

// ContextManager is the interface Executor and Orchestrator need for context window management.
// It extends sdk/agent.ContextManager with SetTask for c0wrk-specific task/criteria support.
type ContextManager interface {
	agent.ContextManager
	// SetTask sets the user's task and acceptance criteria into the context window.
	SetTask(task string, criteria []AcceptanceCriterion)
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
	PlanGenerated(stepCount int, steps []PlanStepEvent)
	PlanStepStart(stepID string, description string)
	PlanStepComplete(stepID string, success bool, duration time.Duration)
	Evaluation(passed, total int, criteria []EvalCriterionEvent)
	Reflection(summary string, insights []string, attempt, maxAttempts int)
	Retry(attempt, maxAttempts int)
	StepRetry(stepID string, attempt, maxAttempts int)
	ACExtracted(count int, criteria []EvalCriterionEvent)
	// Service emits a general service message without metadata.
	Service(content string)
	// ServiceWithMeta emits a service message with metadata for frontend filtering.
	// The meta map can contain arbitrary key-value pairs, e.g., {"phase": "orchestration"}.
	ServiceWithMeta(content string, meta map[string]any)
	// EvaluationError reports an evaluation-phase error.
	EvaluationError(err error)
	// ReplanFailed reports a failed replan attempt.
	ReplanFailed(err error)
	// FileRollbackError reports a file rollback failure for a plan step.
	FileRollbackError(stepID string, err error)
	// EvalStepStart emits an evaluation step start event for a criterion.
	EvalStepStart(criterionID string, description string)
	// EvalStepComplete emits an evaluation step completion event for a criterion.
	EvalStepComplete(criterionID string, success bool, duration time.Duration)
}

// PlanStepScopable is an optional interface that Emitter implementations
// can implement to support scoping events to a plan step.
type PlanStepScopable interface {
	WithPlanStepID(id string) Emitter
}

// CriterionScopable is an optional interface that Emitter implementations
// can implement to support scoping events to an evaluation criterion.
type CriterionScopable interface {
	WithCriterionID(id string) Emitter
}

// scopeEmitterToStep returns a scoped emitter if the emitter supports it,
// otherwise returns the original emitter unchanged.
func scopeEmitterToStep(emitter Emitter, stepID string) Emitter {
	if s, ok := emitter.(PlanStepScopable); ok {
		return s.WithPlanStepID(stepID)
	}
	return emitter
}

// noopEmitter is a no-op implementation of Emitter.
// Used as a default when nil emitter is provided.
type noopEmitter struct {
	agent.NoopEvents // provides AgentEvents methods
}

// ensure noopEmitter implements Emitter.
var _ Emitter = (*noopEmitter)(nil)

func (n *noopEmitter) Routing(_, _, _ string)                             {}
func (n *noopEmitter) PlanGenerated(_ int, _ []PlanStepEvent)             {}
func (n *noopEmitter) PlanStepStart(_, _ string)                          {}
func (n *noopEmitter) PlanStepComplete(_ string, _ bool, _ time.Duration) {}
func (n *noopEmitter) Evaluation(_, _ int, _ []EvalCriterionEvent)        {}
func (n *noopEmitter) Reflection(_ string, _ []string, _, _ int)          {}
func (n *noopEmitter) Retry(_, _ int)                                     {}
func (n *noopEmitter) StepRetry(_ string, _, _ int)                       {}
func (n *noopEmitter) ACExtracted(_ int, _ []EvalCriterionEvent)          {}
func (n *noopEmitter) Service(_ string)                                   {}
func (n *noopEmitter) ServiceWithMeta(_ string, _ map[string]any)         {}
func (n *noopEmitter) EvaluationError(_ error)                            {}
func (n *noopEmitter) ReplanFailed(_ error)                               {}
func (n *noopEmitter) FileRollbackError(_ string, _ error)                {}
func (n *noopEmitter) EvalStepStart(_, _ string)                   {}
func (n *noopEmitter) EvalStepComplete(_ string, _ bool, _ time.Duration) {}

// ---------------------------------------------------------------------------
// emitterEventsAdapter wraps a core Emitter to implement orchestration.Events.
// ---------------------------------------------------------------------------

type emitterEventsAdapter struct {
	Emitter
}

var _ orchestration.Events = (*emitterEventsAdapter)(nil)

func (a *emitterEventsAdapter) OnPlanGenerated(n int, steps []PlanStepEvent) {
	a.PlanGenerated(n, steps)
}
func (a *emitterEventsAdapter) OnStepStarted(id, desc string) {
	a.PlanStepStart(id, desc)
}
func (a *emitterEventsAdapter) OnStepCompleted(id string, ok bool, d time.Duration) {
	a.PlanStepComplete(id, ok, d)
}
func (a *emitterEventsAdapter) OnEvaluated(p, t int, c []EvalCriterionEvent) {
	a.Evaluation(p, t, c)
}
func (a *emitterEventsAdapter) OnReflected(s string, insights []string, attempt, maxAttempts int) {
	a.Reflection(s, insights, attempt, maxAttempts)
}
func (a *emitterEventsAdapter) OnRetry(attempt, maxAttempts int) {
	a.Retry(attempt, maxAttempts)
}
func (a *emitterEventsAdapter) OnStepRetry(stepID string, attempt, maxAttempts int) {
	a.StepRetry(stepID, attempt, maxAttempts)
}
func (a *emitterEventsAdapter) OnCriteriaExtracted(n int, c []EvalCriterionEvent) {
	a.ACExtracted(n, c)
}
func (a *emitterEventsAdapter) OnService(content string) {
	a.Service(content)
}
func (a *emitterEventsAdapter) OnServiceMeta(content string, meta map[string]any) {
	a.ServiceWithMeta(content, meta)
}
func (a *emitterEventsAdapter) OnEvaluationError(err error) {
	slog.Debug("event adapter: evaluation error", "error", err)
	a.EvaluationError(err)
}
func (a *emitterEventsAdapter) OnReplanFailed(err error) {
	slog.Debug("event adapter: replan failed", "error", err)
	a.ReplanFailed(err)
}
func (a *emitterEventsAdapter) OnFileRollbackError(stepID string, err error) {
	slog.Debug("event adapter: file rollback error", "stepID", stepID, "error", err)
	a.FileRollbackError(stepID, err)
}

// WithStepID implements orchestration.StepScopable.
func (a *emitterEventsAdapter) WithStepID(id string) orchestration.Events {
	if s, ok := a.Emitter.(PlanStepScopable); ok {
		return &emitterEventsAdapter{s.WithPlanStepID(id)}
	}
	return a
}

// ---------------------------------------------------------------------------
// c0wrk-specific types
// ---------------------------------------------------------------------------

// RoutingDecision — result of Router classification (AD 4.1).
type RoutingDecision struct {
	Domain             string   `json:"domain"`              // "code" | "research" | "general" | "mixed"
	Complexity         int      `json:"complexity"`          // 1-5
	CompactionStrategy string   `json:"compaction_strategy"` // "sliding_window" | "summarization" | "hierarchical"
	SuggestedTools     []string `json:"suggested_tools"`
	NeedsClarification bool     `json:"needs_clarification"`
	Confidence         float64  `json:"confidence"` // 0.0-1.0, routing confidence
}

// AcceptanceCriterion — criterion for evaluating task completion (AD 4.2).
// Type alias for sdk/orchestration.Criterion.
type AcceptanceCriterion = orchestration.Criterion

// RawCriterion — domain-agnostic criterion extracted before routing (Phase 1 of two-phase AC).
type RawCriterion struct {
	ID          string `json:"id"`          // "rc_1", "rc_2"
	Description string `json:"description"` // What must be satisfied
	Nature      string `json:"nature"`      // "objective" | "subjective"
	Implicit    bool   `json:"implicit"`    // Inferred from context, not explicitly stated
	Weight      string `json:"weight"`      // "must" | "should" | "nice_to_have"
	StepHint    string `json:"step_hint"`   // Optional hint for planner
}

// Plan — DAG of execution steps (AD 4.3).
// Type alias for sdk/orchestration.Plan.
type Plan = orchestration.Plan

// PlanStep — single step in the plan (AD 4.3).
// Type alias for sdk/orchestration.PlanStep.
type PlanStep = orchestration.PlanStep

// ExecutorConfig — configuration for the Executor (AD 4.4).
type ExecutorConfig struct {
	MaxSteps           int
	CompactionStrategy string // will be resolved to actual strategy by memory package
	Tools              []tools.ToolDescriptor
}

// TaskDefinition — defines a task for the Executor.
type TaskDefinition struct {
	Task     string                 `json:"task"`
	Criteria []AcceptanceCriterion  `json:"criteria"`
	Tools    []tools.ToolDescriptor `json:"tools"`
}

// Reflection — result of Reflector analysis (AD 4.6).
// Type alias for sdk/orchestration.Reflection.
type Reflection = orchestration.Reflection

// AgentProfile defines a specialized agent role for plan step execution.
type AgentProfile struct {
	// TODO: implement role-based behavior (prompt customization, tool filtering, strategy selection)
	Role         string   `json:"role"`                    // "researcher", "coder", "tester", "executor" (default)
	SystemPrompt string   `json:"system_prompt,omitempty"` // role-specific prompt override (optional)
	AllowedTools []string `json:"allowed_tools,omitempty"` // subset of available tools (empty = all)
	MaxSteps     int      `json:"max_steps,omitempty"`     // budget per agent (0 = use default)
	Domain       string   `json:"domain,omitempty"`        // "code" | "research" | "general" - affects compaction and AC handling
}

// DefaultAgentProfile returns the default executor profile.
func DefaultAgentProfile() AgentProfile {
	return AgentProfile{Role: "executor"}
}

// HandleResult — result of Orchestrator.Handle (Phase 2).
// Provides rich output for CLI display including routing, plan, and evaluation info.
type HandleResult struct {
	Output          string           `json:"output"`
	RoutingDecision *RoutingDecision `json:"routing_decision"`
	Plan            *Plan            `json:"plan,omitempty"`
	EvalResult      *EvalResult      `json:"eval_result,omitempty"`
	Blackboard      Blackboard       `json:"-"` // shared state for downstream consumers (not serialized)
	// Retry-loop fields (Phase 3)
	AttemptCount int          `json:"attempt_count,omitempty"` // Number of attempts made (1 = first try)
	Reflections  []Reflection `json:"reflections,omitempty"`   // Reflections from failed attempts
}
