package orchestration

import (
	"context"
	"time"

	"github.com/user/agent/sdk/agent"
	"github.com/user/agent/sdk/llm"
	"github.com/user/agent/sdk/tools"
)

// Planner generates and regenerates DAG execution plans.
type Planner interface {
	Plan(ctx context.Context, task string, criteria []Criterion, tools []tools.ToolDescriptor, reflections []Reflection) (*Plan, error)
	Replan(ctx context.Context, plan *Plan, completed []CompletedStep, failedStep CompletedStep, reflection *Reflection, criteria []Criterion, reflections []Reflection) (*Plan, error)
}

// CriteriaExtractor extracts success criteria from a request.
type CriteriaExtractor interface {
	Extract(ctx context.Context, request string) ([]Criterion, error)
}

// Evaluator checks if execution results satisfy criteria.
type Evaluator interface {
	Evaluate(ctx context.Context, result string, criteria []Criterion, bb Blackboard) (*EvalResult, error)
}

// Reflector analyzes failures and produces corrective insights.
type Reflector interface {
	Reflect(ctx context.Context, trajectory []agent.Step, evalResult *EvalResult, plan *Plan, prevReflections []Reflection) (*Reflection, error)
}

// Verifier performs post-evaluation intent verification.
type Verifier interface {
	Verify(ctx context.Context, userMessage, finalOutput, changeSummary string) (*VerificationResult, error)
}

// Events provides hooks for observing orchestration lifecycle.
type Events interface {
	agent.AgentEvents
	OnPlanGenerated(stepCount int, steps []PlanStepEvent)
	OnStepStarted(stepID, description string)
	OnStepCompleted(stepID string, success bool, duration time.Duration)
	OnEvaluated(passed, total int, criteria []EvalCriterionEvent)
	OnReflected(summary string, insights []string, attempt, maxAttempts int)
	OnRetry(attempt, maxAttempts int)
	OnStepRetry(stepID string, attempt, maxAttempts int)
	OnCriteriaExtracted(count int, criteria []EvalCriterionEvent)
	OnService(content string)
	OnServiceMeta(content string, meta map[string]any)
}

// StepScopable is an optional interface that Events implementations
// can implement to support scoping events to a plan step.
type StepScopable interface {
	WithStepID(id string) Events
}

// ContextManagerFactory creates a ContextManager for a new task step.
type ContextManagerFactory func(systemPrompt string, modelMeta llm.ModelMetadata, compactionStrategy string) agent.ContextManager

// BlackboardFactory creates a Blackboard for a new task.
type BlackboardFactory func(taskID string) Blackboard

// SystemPromptFactory creates system prompts for step executors.
// ctx carries workspace path; stepDescription is the step's task; criteria are the step's ACs.
type SystemPromptFactory func(ctx context.Context, stepDescription string, criteria []Criterion) string

// Blackboard provides structured access to shared task state.
// All methods are safe for concurrent use.
type Blackboard interface {
	GetOriginalRequest() string
	GetCriteria() []Criterion
	GetPlan() *Plan
	GetStepResult(stepID string) (StepResult, bool)
	GetStepSummary(stepID string) string
	GetStepsByAC(criterionID string) []StepResult
	GetAllStepResults() map[string]StepResult
	GetReflections() []Reflection
	GetFinalResult() string
	SetOriginalRequest(req string)
	SetCriteria(criteria []Criterion)
	SetPlan(plan *Plan)
	SetStepResult(stepID string, output string, err error, steps []agent.Step)
	AddReflection(r Reflection)
	SetFinalResult(result string)
	Search(query string) []BlackboardEntry
}
