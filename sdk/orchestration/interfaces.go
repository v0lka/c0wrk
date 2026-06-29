package orchestration

import (
	"context"
	"time"

	"github.com/v0lka/c0wrk/sdk/agent"
	"github.com/v0lka/c0wrk/sdk/llm"
	"github.com/v0lka/c0wrk/sdk/tools"
)

// Planner generates and regenerates DAG execution plans.
type Planner interface {
	Plan(ctx context.Context, task string, tools []tools.ToolDescriptor, reflections []Reflection) (*Plan, error)
	Replan(ctx context.Context, plan *Plan, completed []CompletedStep, failedStep CompletedStep, reflection *Reflection, reflections []Reflection) (*Plan, error)
	PlanContinuation(ctx context.Context, originalRequest string, existingPlan *Plan, completedSteps []CompletedStep, newMessage string, availableTools []tools.ToolDescriptor, conversationHistory []llm.Message) (*Plan, error)
}

// Reflector analyzes failures and produces corrective insights.
type Reflector interface {
	Reflect(ctx context.Context, trajectory []agent.Step, plan *Plan, prevReflections []Reflection) (*Reflection, error)
}

// Events provides hooks for observing orchestration lifecycle.
type Events interface {
	agent.AgentEvents
	OnPlanGenerated(stepCount int, steps []PlanStepEvent)
	OnStepStarted(stepID, description, summary string)
	OnStepCompleted(stepID string, success bool, duration time.Duration, errMsg string)
	OnReflected(reflection *Reflection, attempt, maxAttempts int)
	OnRetry(attempt, maxAttempts int)
	OnStepRetry(stepID string, attempt, maxAttempts int)
	OnService(content string)
	OnServiceMeta(content string, meta map[string]any)
	OnReplanFailed(err error)
	OnStepTodoUpdate(stepID string, items []agent.TodoItem)
}

// StepScopable is an optional interface that Events implementations
// can implement to support scoping events to a plan step.
type StepScopable interface {
	WithStepID(id string) Events
}

// RetryScopable is an optional interface that Events implementations
// can implement to tag events with a retry attempt number.
type RetryScopable interface {
	WithRetryAttempt(attempt int) Events
}

// ContextManagerFactory creates a ContextManager for a new task step.
// pruningOverrides, when provided, override the global pruning configuration
// with step-specific KeepLastN and ProtectedTools values.
type ContextManagerFactory func(systemPrompt string, modelMeta llm.ModelMetadata, compactionStrategy string, pruningOverrides ...PruningOverride) agent.ContextManager

// PruningOverride carries per-step overrides for tool output pruning.
// Zero values mean "use global default".
type PruningOverride struct {
	KeepLastN      int      // 0 = use global default
	ProtectedTools []string // nil = use global default
}

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

	// Fact memory for inter-step communication
	StoreFact(fact Fact)
	SearchFacts(keywords []string) []Fact
	GetFacts() []Fact
}
