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

// StepOutputStore provides read access to completed step outputs.
type StepOutputStore = agent.StepOutputStore

// StepOutputEntry describes a completed step's output for listing.
type StepOutputEntry = agent.StepOutputEntry

// FileChange represents a filesystem modification made by an agent step.
type FileChange = agent.FileChange

// ToolResultBudget — tool result truncation config.
type ToolResultBudget = agent.ToolResultBudget

// CircuitBreakerConfig — circuit breaker thresholds for executor protection.
type CircuitBreakerConfig = agent.CircuitBreakerConfig

// LLMCaller is the interface Executor needs from the LLM layer.
type LLMCaller = agent.LLMCaller

// ToolExecutor is the interface Executor needs from the tools layer.
type ToolExecutor = agent.ToolExecutor

// CompactionStrategy defines an algorithm for compressing step history.
type CompactionStrategy = agent.CompactionStrategy

// CompactionResult holds before/after fill percentages from a compaction operation.
type CompactionResult = agent.CompactionResult

// StepLimitFunc is called when an executor reaches its step limit.
type StepLimitFunc = agent.StepLimitFunc

// StepLimitResponse represents the user's decision when the step limit is reached.
type StepLimitResponse = agent.StepLimitResponse

// Step limit response constants.
var (
	StepLimitAllowOnce   = agent.StepLimitAllowOnce
	StepLimitAllowAlways = agent.StepLimitAllowAlways
	StepLimitDeny        = agent.StepLimitDeny
)

// ---------------------------------------------------------------------------
// Type aliases for types that moved to sdk/orchestration
// ---------------------------------------------------------------------------

// CompletedStep — result of an executed plan step (AD 4.7).
type CompletedStep = orchestration.CompletedStep

// PlanStepEvent represents a single step in a plan for event emission.
type PlanStepEvent = orchestration.PlanStepEvent

// Re-export blackboard types from sdk/orchestration.
type StepResult = orchestration.StepResult
type BlackboardEntry = orchestration.BlackboardEntry
type Blackboard = orchestration.Blackboard
type MapBlackboard = orchestration.MapBlackboard
type MapBlackboardOption = orchestration.MapBlackboardOption
type Fact = orchestration.Fact

var (
	NewMapBlackboard     = orchestration.NewMapBlackboard
	WithMaxSummaryTokens = orchestration.WithMaxSummaryTokens
	WithMaxSummaryLen    = orchestration.WithMaxSummaryLen
	GenerateSummary      = orchestration.GenerateSummary
)

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
	PlanGenerated(stepCount int, steps []PlanStepEvent)
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
	// FileRollbackError reports a file rollback failure for a plan step.
	FileRollbackError(stepID string, err error)
	// SkillsActivated reports the skills matched and activated for the current task.
	SkillsActivated(skillNames []string)
	// StepTodoUpdate emits a to-do list update for a plan step.
	StepTodoUpdate(stepID string, items []TodoItem)
}

// TodoItem represents a single checklist item in a step's to-do list.
type TodoItem struct {
	Text    string
	Checked bool
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
func (n *noopEmitter) PlanGenerated(_ int, _ []PlanStepEvent)                       {}
func (n *noopEmitter) PlanStepStart(_, _, _ string)                                 {}
func (n *noopEmitter) PlanStepComplete(_ string, _ bool, _ time.Duration, _ string) {}
func (n *noopEmitter) Reflection(_ *orchestration.Reflection, _, _ int)             {}
func (n *noopEmitter) Retry(_, _ int)                                               {}
func (n *noopEmitter) StepRetry(_ string, _, _ int)                                 {}
func (n *noopEmitter) Service(_ string)                                             {}
func (n *noopEmitter) ServiceWithMeta(_ string, _ map[string]any)                   {}
func (n *noopEmitter) ReplanFailed(_ error)                                         {}
func (n *noopEmitter) FileRollbackError(_ string, _ error)                          {}
func (n *noopEmitter) SkillsActivated(_ []string)                                   {}
func (n *noopEmitter) StepTodoUpdate(_ string, _ []TodoItem)                        {}

// ---------------------------------------------------------------------------
// emitterEventsAdapter wraps a core Emitter to implement orchestration.Events.
// ---------------------------------------------------------------------------

type emitterEventsAdapter struct {
	Emitter
	logger *slog.Logger
}

func (a *emitterEventsAdapter) log() *slog.Logger {
	if a.logger != nil {
		return a.logger
	}
	return slog.Default()
}

var _ orchestration.Events = (*emitterEventsAdapter)(nil)

func (a *emitterEventsAdapter) OnPlanGenerated(n int, steps []PlanStepEvent) {
	a.PlanGenerated(n, steps)
}
func (a *emitterEventsAdapter) OnStepStarted(id, desc, summary string) {
	a.PlanStepStart(id, desc, summary)
}
func (a *emitterEventsAdapter) OnStepCompleted(id string, ok bool, d time.Duration, errMsg string) {
	a.PlanStepComplete(id, ok, d, errMsg)
}
func (a *emitterEventsAdapter) OnReflected(reflection *orchestration.Reflection, attempt, maxAttempts int) {
	a.Reflection(reflection, attempt, maxAttempts)
}
func (a *emitterEventsAdapter) OnRetry(attempt, maxAttempts int) {
	a.Retry(attempt, maxAttempts)
}
func (a *emitterEventsAdapter) OnStepRetry(stepID string, attempt, maxAttempts int) {
	a.StepRetry(stepID, attempt, maxAttempts)
}
func (a *emitterEventsAdapter) OnService(content string) {
	a.Service(content)
}
func (a *emitterEventsAdapter) OnServiceMeta(content string, meta map[string]any) {
	a.ServiceWithMeta(content, meta)
}
func (a *emitterEventsAdapter) OnReplanFailed(err error) {
	a.log().Debug("event adapter: replan failed", "error", err)
	a.ReplanFailed(err)
}
func (a *emitterEventsAdapter) OnFileRollbackError(stepID string, err error) {
	a.log().Debug("event adapter: file rollback error", "stepID", stepID, "error", err)
	a.FileRollbackError(stepID, err)
}
func (a *emitterEventsAdapter) OnStepTodoUpdate(stepID string, items []agent.TodoItem) {
	coreItems := make([]TodoItem, len(items))
	for i, item := range items {
		coreItems[i] = TodoItem{Text: item.Text, Checked: item.Checked}
	}
	a.StepTodoUpdate(stepID, coreItems)
}

// WithStepID implements orchestration.StepScopable.
func (a *emitterEventsAdapter) WithStepID(id string) orchestration.Events {
	if s, ok := a.Emitter.(PlanStepScopable); ok {
		return &emitterEventsAdapter{Emitter: s.WithPlanStepID(id), logger: a.logger}
	}
	return a
}

// WithRetryAttempt implements orchestration.RetryScopable.
func (a *emitterEventsAdapter) WithRetryAttempt(attempt int) orchestration.Events {
	if r, ok := a.Emitter.(RetryAttemptScopable); ok {
		return &emitterEventsAdapter{Emitter: r.WithRetryAttempt(attempt), logger: a.logger}
	}
	return a
}

// ---------------------------------------------------------------------------
// c0wrk-specific types
// ---------------------------------------------------------------------------

// RoutingDecision — result of Router classification (AD 4.1).
type RoutingDecision struct {
	Domain             string   `json:"domain"`     // "code" | "research" | "general" | "mixed"
	Complexity         int      `json:"complexity"` // 1-5
	NeedsClarification bool     `json:"needs_clarification"`
	MatchedSkills      []string `json:"matched_skills,omitempty"` // skills selected by the router
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
	Task  string                 `json:"task"`
	Tools []tools.ToolDescriptor `json:"tools"`
}

// Reflection — result of Reflector analysis (AD 4.6).
// Type alias for sdk/orchestration.Reflection.
type Reflection = orchestration.Reflection

// AgentProfile defines a specialized agent role for plan step execution.
type AgentProfile struct {
	// Role controls system prompt customization; tool filtering uses AllowedTools; strategy uses Domain.
	Role           string   `json:"role"`                      // "researcher", "coder", "tester", "executor" (default)
	SystemPrompt   string   `json:"system_prompt,omitempty"`   // role-specific prompt override (optional)
	AllowedTools   []string `json:"allowed_tools,omitempty"`   // subset of available tools (empty = all)
	MaxSteps       int      `json:"max_steps,omitempty"`       // budget per agent (0 = use default)
	Domain         string   `json:"domain,omitempty"`          // "code" | "research" | "general" - affects compaction strategy
	KeepLastN      int      `json:"keep_last_n,omitempty"`     // per-step KeepLastN override (0 = use role default)
	ProtectedTools []string `json:"protected_tools,omitempty"` // per-step ProtectedTools override (nil = use role default)
}

// DefaultAgentProfile returns the default executor profile.
func DefaultAgentProfile() AgentProfile {
	return AgentProfile{Role: "executor"}
}

// HandleResult — result of Orchestrator.Handle (Phase 2).
// Provides rich output for CLI display including routing and plan info.
type HandleResult struct {
	Output          string           `json:"output"`
	RoutingDecision *RoutingDecision `json:"routing_decision"`
	Plan            *Plan            `json:"plan,omitempty"`
	Blackboard      Blackboard       `json:"-"` // shared state for downstream consumers (not serialized)
	// Retry-loop fields (Phase 3)
	AttemptCount int          `json:"attempt_count,omitempty"` // Number of attempts made (1 = first try)
	Reflections  []Reflection `json:"reflections,omitempty"`   // Reflections from failed attempts
}

// HandleOptions controls how a message is processed by HandleMessage.
type HandleOptions struct {
	TaskID        string // non-empty = continuation of existing task
	ExecutionMode string // "normal" = synthetic plan, "advanced" = full Plan&Execute
}
