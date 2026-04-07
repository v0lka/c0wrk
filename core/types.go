// Package core implements the orchestration engine including routing, planning, evaluation, reflection, and execution of LLM agent workflows.
package core

import (
	"time"

	"github.com/user/agent/sdk/agent"
	tools "github.com/user/agent/sdk/tools"
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
// Event types for the Emitter
// ---------------------------------------------------------------------------

// PlanStepEvent represents a single step in a plan for event emission.
type PlanStepEvent struct {
	ID          string   `json:"id"`
	Description string   `json:"description"`
	Status      string   `json:"status"` // "pending", "running", "completed", "failed"
	DependsOn   []string `json:"depends_on"`
}

// EvalCriterionEvent represents a single evaluation criterion for event emission.
type EvalCriterionEvent struct {
	Name        string `json:"name"`
	Description string `json:"description"`
	Passed      bool   `json:"passed"`
	Status      string `json:"status"`              // "pass", "fail", or "unclear"
	Diagnostic  string `json:"diagnostic,omitempty"` // evaluation reasoning / intent verification feedback
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
	ACExtracted(count int, criteria []EvalCriterionEvent)
	// Service emits a general service message without metadata.
	Service(content string)
	// ServiceWithMeta emits a service message with metadata for frontend filtering.
	// The meta map can contain arbitrary key-value pairs, e.g., {"phase": "orchestration"}.
	ServiceWithMeta(content string, meta map[string]any)
}

// PlanStepScopable is an optional interface that Emitter implementations
// can implement to support scoping events to a plan step.
type PlanStepScopable interface {
	WithPlanStepID(id string) Emitter
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
func (n *noopEmitter) ACExtracted(_ int, _ []EvalCriterionEvent)          {}
func (n *noopEmitter) Service(_ string)                                   {}
func (n *noopEmitter) ServiceWithMeta(_ string, _ map[string]any)         {}

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
type AcceptanceCriterion struct {
	ID          string `json:"id"` // "ac_1", "ac_2", ...
	Description string `json:"description"`
	CheckType   string `json:"check_type"` // "programmatic" | "llm_judge"
	CheckCmd    string `json:"check_cmd"`  // for programmatic: "go test ./..."
	StepHint    string `json:"step_hint"`  // optional hint for Planner
}

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
type Plan struct {
	Steps []PlanStep `json:"steps"`
}

// PlanStep — single step in the plan (AD 4.3).
type PlanStep struct {
	ID             string        `json:"id"` // "step_1", "step_2a", ...
	Description    string        `json:"description"`
	DependsOn      []string      `json:"depends_on"`
	Parallelizable bool          `json:"parallelizable"`
	EstimatedTools []string      `json:"estimated_tools"`
	RelevantAC     []string      `json:"relevant_ac"`             // IDs of related AcceptanceCriteria
	AgentProfile   *AgentProfile `json:"agent_profile,omitempty"` // optional specialization
}

// CompletedStep — result of an executed plan step (AD 4.7).
type CompletedStep struct {
	StepID string `json:"step_id"`
	Output string `json:"output"`
	Error  error  `json:"-"` // not serialized
	Steps  []Step `json:"steps,omitempty"` // actual executor steps for evaluator evidence
}

// ExecutorConfig — configuration for the Executor (AD 4.4).
type ExecutorConfig struct {
	MaxSteps           int
	CompactionStrategy string                 // will be resolved to actual strategy by memory package
	Tools              []tools.ToolDescriptor
}

// TaskDefinition — defines a task for the Executor.
type TaskDefinition struct {
	Task     string                 `json:"task"`
	Criteria []AcceptanceCriterion  `json:"criteria"`
	Tools    []tools.ToolDescriptor `json:"tools"`
}

// EvalResult — result of Evaluator checking AC (AD 4.5).
type EvalResult struct {
	Passed    []EvalDetail `json:"passed"`
	Failed    []EvalDetail `json:"failed"`
	Unclear   []EvalDetail `json:"unclear"`
	AllPassed bool         `json:"all_passed"`
}

// EvalDetail — detail for a single AC evaluation (AD 4.5).
type EvalDetail struct {
	Criterion          AcceptanceCriterion `json:"criterion"`
	Diagnostic         string              `json:"diagnostic"`
	Reconsidered       bool                `json:"reconsidered,omitempty"`
	OriginalDiagnostic string              `json:"original_diagnostic,omitempty"`
}

// IntentVerification holds the result of Tier 2 intent-based verification.
type IntentVerification struct {
	Passed   bool   `json:"passed"`
	Feedback string `json:"feedback"` // structured explanation for replan/reflector
	Steps    []Step `json:"steps"`    // verification steps taken (for audit trail)
}

// Reflection — result of Reflector analysis (AD 4.6).
type Reflection struct {
	Summary         string    `json:"summary"`          // brief summary of what happened
	FailedCriteria  []string  `json:"failed_criteria"`  // IDs of failed acceptance criteria
	Hypotheses      []string  `json:"hypotheses"`       // what might have gone wrong
	SuggestedAction string    `json:"suggested_action"` // "retry" | "replan" | "abort"
	Reasoning       string    `json:"reasoning"`        // explanation for the suggested action
	FailureAnalysis string    `json:"failure_analysis"` // detailed failure analysis
	RootCause       string    `json:"root_cause"`       // identified root cause
	ActionPlan      string    `json:"action_plan"`      // what to do differently
	Timestamp       time.Time `json:"timestamp"`
}

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
	// Retry-loop fields (Phase 3)
	AttemptCount int          `json:"attempt_count,omitempty"` // Number of attempts made (1 = first try)
	Reflections  []Reflection `json:"reflections,omitempty"`   // Reflections from failed attempts
}
