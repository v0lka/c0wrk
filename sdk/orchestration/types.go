package orchestration

import (
	"time"

	"github.com/user/agent/sdk/agent"
)

// Plan is a DAG of execution steps.
type Plan struct {
	Steps []PlanStep `json:"steps"`
}

// PlanStep is a single step in the execution plan.
type PlanStep struct {
	ID             string   `json:"id"`
	Description    string   `json:"description"`
	DependsOn      []string `json:"depends_on"`
	Parallelizable bool     `json:"parallelizable"`
	EstimatedTools []string `json:"estimated_tools"`
	RelevantAC     []string `json:"relevant_ac"`
	// Profile holds optional step-level configuration as an opaque value.
	// Consumers can type-assert to their domain-specific profile type.
	Profile any `json:"profile,omitempty"`
}

// Criterion defines a success criterion for task evaluation.
type Criterion struct {
	ID          string `json:"id"`
	Description string `json:"description"`
	CheckType   string `json:"check_type"` // "programmatic" | "llm_judge"
	CheckCmd    string `json:"check_cmd"`  // for programmatic checks
	StepHint    string `json:"step_hint"`  // optional hint for planner
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
	StepID      string
	Summary     string
	FullOutput  string
	Error       error
	Steps       []agent.Step
	FileChanges []FileChange // file changes made by this step
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
	FailedCriteria  []string  `json:"failed_criteria"`
	Hypotheses      []string  `json:"hypotheses"`
	SuggestedAction string    `json:"suggested_action"` // "retry" | "replan" | "abort"
	Reasoning       string    `json:"reasoning"`
	FailureAnalysis string    `json:"failure_analysis"`
	RootCause       string    `json:"root_cause"`
	ActionPlan      string    `json:"action_plan"`
	Timestamp       time.Time `json:"timestamp"`
}

// EvalResult holds the outcome of evaluating acceptance criteria.
type EvalResult struct {
	Passed    []EvalDetail `json:"passed"`
	Failed    []EvalDetail `json:"failed"`
	Unclear   []EvalDetail `json:"unclear"`
	AllPassed bool         `json:"all_passed"`
}

// EvalDetail holds detail for a single criterion evaluation.
type EvalDetail struct {
	Criterion          Criterion `json:"criterion"`
	Diagnostic         string    `json:"diagnostic"`
	Reconsidered       bool      `json:"reconsidered,omitempty"`
	OriginalDiagnostic string    `json:"original_diagnostic,omitempty"`
}

// EvalVerdict holds a single criterion verdict recorded by an evaluator agent.
type EvalVerdict struct {
	CriterionID string `json:"criterion_id"`
	Verdict     string `json:"verdict"`     // "YES" | "NO"
	Explanation string `json:"explanation"`
}

// VerificationResult holds the result of intent-based verification.
type VerificationResult struct {
	Passed   bool         `json:"passed"`
	Feedback string       `json:"feedback"`
	Steps    []agent.Step `json:"steps"`
}

// ExecutionResult is the output of Orchestrator.Execute.
type ExecutionResult struct {
	Output       string       `json:"output"`
	Plan         *Plan        `json:"plan,omitempty"`
	EvalResult   *EvalResult  `json:"eval_result,omitempty"`
	Blackboard   Blackboard   `json:"-"`
	AttemptCount int          `json:"attempt_count,omitempty"`
	Reflections  []Reflection `json:"reflections,omitempty"`
}

// PlanStepEvent represents a step in a plan for event emission.
type PlanStepEvent struct {
	ID          string   `json:"id"`
	Description string   `json:"description"`
	Status      string   `json:"status"`
	DependsOn   []string `json:"depends_on"`
}

// EvalCriterionEvent represents an evaluation criterion for event emission.
type EvalCriterionEvent struct {
	Name        string `json:"name"`
	Description string `json:"description"`
	Passed      bool   `json:"passed"`
	Status      string `json:"status"`
	Diagnostic  string `json:"diagnostic,omitempty"`
}

// FileChange is an alias for the canonical type in the agent package.
type FileChange = agent.FileChange
