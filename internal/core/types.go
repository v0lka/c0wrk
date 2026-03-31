package core

import (
	"time"

	"github.com/user/agent/internal/llm"
	"github.com/user/agent/internal/tools"
)

// PlanStepEvent represents a single step in a plan for event emission.
type PlanStepEvent struct {
	Description string `json:"description"`
	Status      string `json:"status"` // "pending", "running", "completed", "failed"
}

// EvalCriterionEvent represents a single evaluation criterion for event emission.
type EvalCriterionEvent struct {
	Name        string `json:"name"`
	Description string `json:"description"`
	Passed      bool   `json:"passed"`
}

// Emitter defines the interface for emitting agent execution events.
// Implementations must be nil-safe (all methods are no-ops when receiver is nil).
type Emitter interface {
	Routing(mode, domain, complexity string)
	PlanGenerated(stepCount int, steps []PlanStepEvent)
	PlanStepStart(stepID string, description string)
	PlanStepComplete(stepID string, success bool, duration time.Duration)
	StepStart(stepNum int)
	Thought(stepNum int, content string)
	ToolCall(stepNum int, toolName string, argsPreview string)
	ToolResult(stepNum int, resultLen int, preview string)
	StepComplete(stepNum int, duration time.Duration)
	SubAgentLaunch(stepID, description string)
	SubAgentComplete(stepID string, success bool, duration time.Duration)
	Evaluation(passed, total int, criteria []EvalCriterionEvent)
	Reflection(summary string, insights []string, attempt, maxAttempts int)
	Retry(attempt, maxAttempts int)
	Escalation(fromMode, toMode string)
	ACExtracted(count int)
	AssistantChunk(content string)
	AssistantDone(fullContent string, inputTokens, outputTokens int)
	ContextFill(fillPercent float64, usedTokens, maxTokens int, status string)
}

// noopEmitter is a no-op implementation of Emitter.
// Used as a default when nil emitter is provided.
type noopEmitter struct{}

// ensure noopEmitter implements Emitter.
var _ Emitter = (*noopEmitter)(nil)

func (n *noopEmitter) Routing(_, _, _ string) {}
func (n *noopEmitter) PlanGenerated(_ int, _ []PlanStepEvent) {}
func (n *noopEmitter) PlanStepStart(_, _ string)             {}
func (n *noopEmitter) PlanStepComplete(_ string, _ bool, _ time.Duration) {}
func (n *noopEmitter) StepStart(_ int)        {}
func (n *noopEmitter) Thought(_ int, _ string) {}
func (n *noopEmitter) ToolCall(_ int, _, _ string) {}
func (n *noopEmitter) ToolResult(_, _ int, _ string)           {}
func (n *noopEmitter) StepComplete(_ int, _ time.Duration)     {}
func (n *noopEmitter) SubAgentLaunch(_, _ string)              {}
func (n *noopEmitter) SubAgentComplete(_ string, _ bool, _ time.Duration) {}
func (n *noopEmitter) Evaluation(_, _ int, _ []EvalCriterionEvent) {}
func (n *noopEmitter) Reflection(_ string, _ []string, _, _ int) {}
func (n *noopEmitter) Retry(_, _ int)                          {}
func (n *noopEmitter) Escalation(_, _ string)                  {}
func (n *noopEmitter) ACExtracted(_ int)                       {}
func (n *noopEmitter) AssistantChunk(_ string)                {}
func (n *noopEmitter) AssistantDone(_ string, _, _ int)       {}
func (n *noopEmitter) ContextFill(_ float64, _, _ int, _ string) {}

// RoutingDecision — result of Router classification (AD 4.1).
type RoutingDecision struct {
	Mode               string   `json:"mode"`                // "direct" | "react" | "plan_execute"
	Domain             string   `json:"domain"`              // "code" | "research" | "general" | "mixed"
	Complexity         int      `json:"complexity"`          // 1-5
	CompactionStrategy string   `json:"compaction_strategy"` // "sliding_window" | "summarization" | "hierarchical"
	SuggestedTools     []string `json:"suggested_tools"`
	NeedsClarification bool     `json:"needs_clarification"`
	Confidence         float64  `json:"confidence"`          // 0.0-1.0, routing confidence
}

// AcceptanceCriterion — criterion for evaluating task completion (AD 4.2).
type AcceptanceCriterion struct {
	ID          string `json:"id"`          // "ac_1", "ac_2", ...
	Description string `json:"description"`
	CheckType   string `json:"check_type"` // "programmatic" | "llm_judge"
	CheckCmd    string `json:"check_cmd"`  // for programmatic: "go test ./..."
	StepHint    string `json:"step_hint"`  // optional hint for Planner
}

// Plan — DAG of execution steps (AD 4.3).
type Plan struct {
	Steps []PlanStep `json:"steps"`
}

// PlanStep — single step in the plan (AD 4.3).
type PlanStep struct {
	ID             string   `json:"id"`              // "step_1", "step_2a", ...
	Description    string   `json:"description"`
	DependsOn      []string `json:"depends_on"`
	Parallelizable bool     `json:"parallelizable"`
	EstimatedTools []string `json:"estimated_tools"`
	RelevantAC     []string `json:"relevant_ac"` // IDs of related AcceptanceCriteria
}

// CompletedStep — result of an executed plan step (AD 4.7).
type CompletedStep struct {
	StepID string `json:"step_id"`
	Output string `json:"output"`
	Error  error  `json:"-"` // not serialized
}

// Step — single iteration of the ReAct loop (AD 4.4).
type Step struct {
	Thought     string       `json:"thought"`
	Action      llm.ToolCall `json:"action"` // import from internal/llm
	Observation string       `json:"observation"`
	TokensUsed  int          `json:"tokens_used"`
}

// ExecutorConfig — configuration for the Executor (AD 4.4).
type ExecutorConfig struct {
	MaxSteps           int
	CompactionStrategy string                 // will be resolved to actual strategy by memory package
	Tools              []tools.ToolDescriptor // import from internal/tools
	LLMRole            string                 // "executor" → mapped to model via LLMRouter
}

// ExecutorResult — result of Executor.Run (AD 4.4).
type ExecutorResult struct {
	Output   string `json:"output"`
	Steps    []Step `json:"steps"`
	Finished bool   `json:"finished"` // true if finish action, false if budget exhausted
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
	Criterion  AcceptanceCriterion `json:"criterion"`
	Diagnostic string              `json:"diagnostic"`
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
	TaskType        string    `json:"task_type"` // for Episodic Memory indexing
}

// SemanticSearchResult represents a result from semantic memory search.
type SemanticSearchResult struct {
	Key     string
	Content string
	Score   float64
}

// SubAgentResult — result from a SubAgent (AD 4.7).
type SubAgentResult struct {
	StepID string `json:"step_id"`
	Output string `json:"output"`
	Error  error  `json:"-"`
}

// HandleResult — result of Orchestrator.Handle (Phase 2).
// Provides rich output for CLI display including routing, plan, and evaluation info.
type HandleResult struct {
	Output          string           `json:"output"`
	RoutingDecision *RoutingDecision `json:"routing_decision"`
	Plan            *Plan            `json:"plan,omitempty"`
	EvalResult      *EvalResult      `json:"eval_result,omitempty"`
	// Retry-loop fields (Phase 3)
	AttemptCount    int              `json:"attempt_count,omitempty"`    // Number of attempts made (1 = first try)
	Reflections     []Reflection     `json:"reflections,omitempty"`     // Reflections from failed attempts
	Escalated       bool             `json:"escalated,omitempty"`        // true if mode was escalated
	OriginalMode    string           `json:"original_mode,omitempty"`    // original mode before escalation
}
