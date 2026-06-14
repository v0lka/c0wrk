package core

import "log/slog"

// ---------------------------------------------------------------------------
// PersistableBlackboard — interface for blackboards that support persistence
// ---------------------------------------------------------------------------

// PersistableBlackboard extends Blackboard with persistence lifecycle methods.
// The orchestrator uses this interface (via type assertion) to drive task
// completion / failure / routing without depending on the concrete type
// that lives in backend/session.
type PersistableBlackboard interface {
	Blackboard
	SetEmitter(emitter Emitter)
	SetRouting(routing *RoutingDecision)
	CompleteTask(attemptCount int)
	FailTask()
	ReactivateTask()
	TaskID() string
}

// BlackboardRestoreFunc restores a PersistableBlackboard from persistence.
// Returns nil, nil if the task is not found.
type BlackboardRestoreFunc func(taskID, sessionID string, store TaskPersistence, logger *slog.Logger, opts ...MapBlackboardOption) (PersistableBlackboard, error)

// ---------------------------------------------------------------------------
// TaskPersistence — core-side abstraction for task storage
// ---------------------------------------------------------------------------

// TaskPersistence provides persistent storage for task state.
// Implementations must be safe for concurrent use.
// This interface lives in core/ to avoid a dependency from core -> backend.
type TaskPersistence interface {
	PersistNewTask(taskID, sessionID, originalRequest string) error
	PersistPlan(taskID string, plan *Plan) error
	PersistRouting(taskID string, routing *RoutingDecision) error
	PersistStepResult(taskID, stepID, summary, fullOutput, errorText string, steps []Step) error
	PersistReflection(taskID string, r Reflection) error
	PersistCompletion(taskID, finalOutput string, attemptCount int) error
	PersistFailure(taskID string) error
	PersistFacts(taskID string, facts []Fact) error
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
	Plan            *Plan
	StepResults     map[string]StepResult
	Reflections     []Reflection
	FinalOutput     string
	Facts           []Fact // keyword-tagged facts
	Status          string // "in_progress", "completed", "failed"
}
