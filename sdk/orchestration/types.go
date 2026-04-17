package orchestration

import (
	"time"

	"github.com/user/agent/sdk/agent"
)

// Plan is a DAG of execution steps.
type Plan struct {
	Steps              []PlanStep `json:"steps"`
	ExplorationContext string     `json:"exploration_context,omitempty"`
}

// PlanStep is a single step in the execution plan.
type PlanStep struct {
	ID             string   `json:"id"`
	Description    string   `json:"description"`
	DependsOn      []string `json:"depends_on"`
	Parallelizable bool     `json:"parallelizable"`
	EstimatedTools []string `json:"estimated_tools"`
	// Profile holds optional step-level configuration as an opaque value.
	// Consumers can type-assert to their domain-specific profile type.
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
	Hypotheses      []string  `json:"hypotheses"`
	SuggestedAction string    `json:"suggested_action"` // "retry" | "replan" | "abort"
	Reasoning       string    `json:"reasoning"`
	FailureAnalysis string    `json:"failure_analysis"`
	RootCause       string    `json:"root_cause"`
	ActionPlan      string    `json:"action_plan"`
	Timestamp       time.Time `json:"timestamp"`
}

// ExecutionResult is the output of Orchestrator.Execute.
type ExecutionResult struct {
	Output       string       `json:"output"`
	Plan         *Plan        `json:"plan,omitempty"`
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

// Fact represents a keyword-tagged piece of information for inter-step communication.
type Fact struct {
	Keywords []string `json:"keywords"` // 3-5 keywords for retrieval
	Content  string   `json:"content"`  // the fact text
	Author   string   `json:"author"`   // step ID that wrote it
}

// FileChange is an alias for the canonical type in the agent package.
type FileChange = agent.FileChange
