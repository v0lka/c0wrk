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
	Plan(ctx context.Context, task string, tools []tools.ToolDescriptor, reflections []Reflection) (*Plan, error)
	Replan(ctx context.Context, plan *Plan, completed []CompletedStep, failedStep CompletedStep, reflection *Reflection, reflections []Reflection) (*Plan, error)
	PlanContinuation(ctx context.Context, originalRequest string, existingPlan *Plan, completedSteps []CompletedStep, newMessage string, availableTools []tools.ToolDescriptor) (*Plan, error)
}

// Reflector analyzes failures and produces corrective insights.
type Reflector interface {
	Reflect(ctx context.Context, trajectory []agent.Step, plan *Plan, prevReflections []Reflection) (*Reflection, error)
}

// Events provides hooks for observing orchestration lifecycle.
type Events interface {
	agent.AgentEvents
	OnPlanGenerated(stepCount int, steps []PlanStepEvent)
	OnStepStarted(stepID, description string)
	OnStepCompleted(stepID string, success bool, duration time.Duration, errMsg string)
	OnReflected(reflection *Reflection, attempt, maxAttempts int)
	OnRetry(attempt, maxAttempts int)
	OnStepRetry(stepID string, attempt, maxAttempts int)
	OnService(content string)
	OnServiceMeta(content string, meta map[string]any)
	OnReplanFailed(err error)
	OnFileRollbackError(stepID string, err error)
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
// ctx carries workspace path; stepDescription is the step's task; modelMeta provides model capabilities.
type SystemPromptFactory func(ctx context.Context, stepDescription string, modelMeta llm.ModelMetadata) string

// Blackboard provides structured access to shared task state.
// All methods are safe for concurrent use.
type Blackboard interface {
	GetOriginalRequest() string
	GetPlan() *Plan
	GetStepResult(stepID string) (StepResult, bool)
	GetStepSummary(stepID string) string
	GetAllStepResults() map[string]StepResult
	GetReflections() []Reflection
	GetFinalResult() string
	SetOriginalRequest(req string)
	SetPlan(plan *Plan)
	SetStepResult(stepID string, output string, err error, steps []agent.Step)
	AddReflection(r Reflection)
	SetFinalResult(result string)
	Search(query string) []BlackboardEntry

	// File change tracking
	SetStepFileChanges(stepID string, changes []FileChange)
	GetStepFileChanges(stepID string) []FileChange
	GetAllFileChanges() map[string][]FileChange // stepID -> changes
	GetSessionFileChanges() []FileChange        // aggregated: one entry per unique path

	// Fact memory for inter-step communication
	StoreFact(fact Fact)
	SearchFacts(keywords []string) []Fact
}
