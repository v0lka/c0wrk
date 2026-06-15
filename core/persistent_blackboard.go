package core

import (
	"log/slog"

	"github.com/v0lka/c0wrk/sdk/agent"
	"github.com/v0lka/c0wrk/sdk/orchestration"
)

// ---------------------------------------------------------------------------
// PersistableBlackboard — interface for blackboards that support persistence
// ---------------------------------------------------------------------------

// PersistableBlackboard extends orchestration.Blackboard with persistence lifecycle methods.
// The orchestrator uses this interface (via type assertion) to drive task
// completion / failure / routing without depending on the concrete type
// that lives in backend/session.
type PersistableBlackboard interface {
	orchestration.Blackboard
	SetEmitter(emitter Emitter)
	SetRouting(routing *RoutingDecision)
	CompleteTask(attemptCount int)
	FailTask()
	ReactivateTask()
	TaskID() string
}

// BlackboardRestoreFunc restores a PersistableBlackboard from persistence.
// Returns nil, nil if the task is not found.
type BlackboardRestoreFunc func(taskID, sessionID string, store TaskPersistence, logger *slog.Logger, opts ...orchestration.MapBlackboardOption) (PersistableBlackboard, error)

// ---------------------------------------------------------------------------
// TaskPersistence — core-side abstraction for task storage
// ---------------------------------------------------------------------------

// TaskPersistence provides persistent storage for task state.
// Implementations must be safe for concurrent use.
// This interface lives in core/ to avoid a dependency from core -> backend.
type TaskPersistence interface {
	PersistNewTask(taskID, sessionID, originalRequest string) error
	PersistPlan(taskID string, plan *orchestration.Plan) error
	PersistRouting(taskID string, routing *RoutingDecision) error
	PersistStepResult(taskID, stepID, summary, fullOutput, errorText string, steps []agent.Step) error
	PersistReflection(taskID string, r orchestration.Reflection) error
	PersistCompletion(taskID, finalOutput string, attemptCount int) error
	PersistFailure(taskID string) error
	PersistFacts(taskID string, facts []orchestration.Fact) error
	// Restoration
	LoadTaskState(taskID string) (*TaskState, error)
	GetUnfinishedTaskID(sessionID string) (string, error) // returns "" if none
	// Task lifecycle
	ReactivateTask(taskID string) error
}

// ---------------------------------------------------------------------------
// TaskState — restored state from persistence
// ---------------------------------------------------------------------------

// TaskState holds the restored state of a persisted task.
type TaskState struct {
	TaskID          string
	SessionID       string
	OriginalRequest string
	RoutingDecision *RoutingDecision
	Plan            *orchestration.Plan
	StepResults     map[string]orchestration.StepResult
	Reflections     []orchestration.Reflection
	FinalOutput     string
	Facts           []orchestration.Fact // keyword-tagged facts
	Status          string // "in_progress", "completed", "failed"
}
