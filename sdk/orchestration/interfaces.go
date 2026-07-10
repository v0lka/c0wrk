package orchestration

import (
	"context"
	"time"

	"github.com/v0lka/sp4rk/agent"
	"github.com/v0lka/sp4rk/llm"
	"github.com/v0lka/sp4rk/skills"
	"github.com/v0lka/sp4rk/tools"
)

// Planner generates and regenerates DAG execution plans.
// The signatures match planner.Planner so that the reference implementation
// satisfies this interface directly (verified by a compile-time
// `var _ orchestration.Planner = (*Planner)(nil)` check in the planner package).
type Planner interface {
	Plan(ctx context.Context, task string, availableTools []tools.ToolDescriptor, reflections []Reflection, availableSkills []skills.SkillDescriptor, singleStep bool, conversationHistory []llm.Message) (*Plan, error)
	Replan(ctx context.Context, originalPlan *Plan, completed []CompletedStep, failedStep CompletedStep, reflection *Reflection, sessionReflections []Reflection, availableSkills []skills.SkillDescriptor) (*Plan, error)
	PlanContinuation(ctx context.Context, originalRequest string, existingPlan *Plan, completedSteps []CompletedStep, newMessage string, availableTools []tools.ToolDescriptor, availableSkills []skills.SkillDescriptor, singleStep bool, conversationHistory []llm.Message, taskComplete bool) (*Plan, error)
}

// Reflector analyzes failures and produces corrective insights.
type Reflector interface {
	Reflect(ctx context.Context, trajectory []agent.Step, plan *Plan, prevReflections []Reflection) (*Reflection, error)
}

// Events provides hooks for observing orchestration lifecycle.
type Events interface {
	agent.Events
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

// TaskAware is an optional capability interface for ContextManager
// implementations that can receive the formatted task content (the user
// message). The Conductor type-asserts the ContextManager against this
// interface and calls SetTask when supported. sp4rk's memory.ContextWindow
// implements it.
type TaskAware interface {
	SetTask(task string)
}

// ConversationAware is an optional capability interface for ContextManager
// implementations that can receive prior conversation messages (previous
// user/assistant exchanges) to render before the current task content. The
// Conductor type-asserts against this interface when ConversationHistory is
// configured. sp4rk's memory.ContextWindow implements it.
type ConversationAware interface {
	SetPriorConversation(msgs []llm.Message)
}

// TrackerProvider is an optional capability interface for ContextManager
// implementations that expose their token tracker. The Conductor uses it to
// wire API-reported token corrections from the LLM caller back into the
// context window's fill accounting. sp4rk's memory.ContextWindow implements
// it.
type TrackerProvider interface {
	ContextTracker() *llm.ContextTokenTracker
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
