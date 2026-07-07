package orchestration

import (
	"errors"
	"time"

	"github.com/v0lka/sp4rk/agent"
)

// ErrExecutionIncomplete indicates a plan execution ended before all steps
// completed (e.g. step limit reached, context cancelled). It accompanies a
// non-nil *ExecutionResult carrying best-effort output. Callers should
// errors.Is-check and use the returned result for partial output.
var ErrExecutionIncomplete = errors.New("plan execution incomplete")

// Plan is a DAG of execution steps.
type Plan struct {
	Steps              []PlanStep `json:"steps"`
	ExplorationContext string     `json:"exploration_context,omitempty"`
}

// PlanStep is a single step in the execution plan.
type PlanStep struct {
	ID             string   `json:"id"`
	Summary        string   `json:"summary"`
	Description    string   `json:"description"`
	DependsOn      []string `json:"depends_on"`
	Parallelizable bool     `json:"parallelizable"`
	EstimatedTools []string `json:"estimated_tools"`
	// Profile holds optional step-level configuration.
	// During JSON deserialization this is map[string]any; consumers should
	// convert to a domain-specific profile (e.g. *planner.AgentProfile).
	Profile any `json:"profile,omitempty"`
}

// CompletedStep holds the result of an executed plan step.
type CompletedStep struct {
	StepID string       `json:"step_id"`
	Output string       `json:"output"`
	Error  error        `json:"-"`
	Steps  []agent.Step `json:"steps,omitempty"`
}

// StepResult holds both a summary and the full output of a completed step.
type StepResult struct {
	StepID     string
	Summary    string
	FullOutput string
	Error      error
	Steps      []agent.Step
}

// BlackboardEntry represents a search result from the blackboard.
type BlackboardEntry struct {
	Type    string // "step_result", "criterion", "reflection", etc.
	Key     string
	Summary string
}

// Reflection is the result of failure analysis.
type Reflection struct {
	Summary         string    `json:"summary"`
	Hypotheses      []string  `json:"hypotheses"`
	SuggestedAction string    `json:"suggested_action"` // "retry" | "replan" | "abort"
	Reasoning       string    `json:"reasoning"`
	FailureAnalysis string    `json:"failure_analysis"`
	RootCause       string    `json:"root_cause"`
	ActionPlan      string    `json:"action_plan"`
	Timestamp       time.Time `json:"timestamp"`
}

// ExecutionStatus classifies the terminal outcome of a plan execution.
// It is the typed success contract: callers must consult Status instead of
// parsing Output suffixes to distinguish full success from degraded outcomes.
type ExecutionStatus string

const (
	// ExecutionStatusSuccess — all plan steps completed without errors.
	ExecutionStatusSuccess ExecutionStatus = "success"
	// ExecutionStatusPartial — some steps were never attempted (execution
	// incomplete); accompanies ErrExecutionIncomplete.
	ExecutionStatusPartial ExecutionStatus = "partial"
	// ExecutionStatusFailed — all steps were attempted, some failed, and the
	// retry budget is exhausted.
	ExecutionStatusFailed ExecutionStatus = "failed"
	// ExecutionStatusAborted — the reflector recommended aborting after step failures.
	ExecutionStatusAborted ExecutionStatus = "aborted"
	// ExecutionStatusCancelled — the context was cancelled mid-execution.
	ExecutionStatusCancelled ExecutionStatus = "cancelled"
)

// ExecutionResult is the output of Orchestrator.Execute.
type ExecutionResult struct {
	Output       string          `json:"output"`
	Plan         *Plan           `json:"plan,omitempty"`
	Blackboard   Blackboard      `json:"-"`
	AttemptCount int             `json:"attempt_count,omitempty"`
	Reflections  []Reflection    `json:"reflections,omitempty"`
	Status       ExecutionStatus `json:"status,omitempty"`
	FailedSteps  int             `json:"failed_steps,omitempty"` // steps that finished with an error in the final attempt
}

// PlanStepEvent represents a step in a plan for event emission.
type PlanStepEvent struct {
	ID          string   `json:"id"`
	Summary     string   `json:"summary"`
	Description string   `json:"description"`
	Status      string   `json:"status"`
	DependsOn   []string `json:"depends_on"`
}

// Fact represents a keyword-tagged piece of information for inter-step communication.
type Fact struct {
	Keywords []string `json:"keywords"` // 3-5 keywords for retrieval
	Content  string   `json:"content"`  // the fact text
	Author   string   `json:"author"`   // step ID that wrote it
}
