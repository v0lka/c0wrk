package core

import (
	"fmt"
	"log/slog"

	"github.com/user/agent/sdk/orchestration"
)

// ---------------------------------------------------------------------------
// TaskPersistence — core-side abstraction for task storage
// ---------------------------------------------------------------------------

// TaskPersistence provides persistent storage for task state.
// Implementations must be safe for concurrent use.
// This interface lives in core/ to avoid a dependency from core -> backend.
type TaskPersistence interface {
	PersistNewTask(taskID, sessionID, originalRequest string) error
	PersistPlan(taskID string, plan *Plan) error
	PersistCriteria(taskID string, criteria []AcceptanceCriterion) error
	PersistRouting(taskID string, routing *RoutingDecision) error
	PersistStepResult(taskID, stepID, summary, fullOutput, errorText string, steps []Step) error
	PersistReflection(taskID string, r Reflection) error
	PersistCompletion(taskID, finalOutput string, evalResult *EvalResult, attemptCount int) error
	PersistFailure(taskID string) error
	// Restoration
	LoadTaskState(taskID string) (*TaskState, error)
	GetUnfinishedTaskID(sessionID string) (string, error) // returns "" if none
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
	Criteria        []AcceptanceCriterion
	StepResults     map[string]StepResult
	Reflections     []Reflection
	FinalOutput     string
	Status          string // "in_progress", "completed", "failed"
}

// ---------------------------------------------------------------------------
// PersistentBlackboard — decorator that persists write operations
// ---------------------------------------------------------------------------

// compile-time check
var _ Blackboard = (*PersistentBlackboard)(nil)

// PersistentBlackboard wraps a MapBlackboard and persists write operations to a TaskPersistence store.
// Read methods delegate to the embedded MapBlackboard. Write methods delegate AND persist.
// All persistence calls are best-effort: errors are logged but do not propagate to callers.
type PersistentBlackboard struct {
	*MapBlackboard
	taskID    string
	sessionID string
	store     TaskPersistence
	logger    *slog.Logger
}

// NewPersistentBlackboard creates a PersistentBlackboard that wraps a fresh MapBlackboard.
// The logger is optional (nil-safe).
func NewPersistentBlackboard(taskID, sessionID string, store TaskPersistence, logger *slog.Logger, opts ...MapBlackboardOption) *PersistentBlackboard {
	return &PersistentBlackboard{
		MapBlackboard: NewMapBlackboard(opts...),
		taskID:        taskID,
		sessionID:     sessionID,
		store:         store,
		logger:        logger,
	}
}

// ---------------------------------------------------------------------------
// Write method overrides
// ---------------------------------------------------------------------------

// SetOriginalRequest sets the original user request and persists a new task record.
func (pb *PersistentBlackboard) SetOriginalRequest(req string) {
	pb.MapBlackboard.SetOriginalRequest(req)
	if err := pb.store.PersistNewTask(pb.taskID, pb.sessionID, req); err != nil {
		pb.logWarn("failed to persist new task", "task_id", pb.taskID, "error", err)
	}
}

// SetCriteria stores criteria and persists them.
func (pb *PersistentBlackboard) SetCriteria(criteria []AcceptanceCriterion) {
	pb.MapBlackboard.SetCriteria(criteria)
	if err := pb.store.PersistCriteria(pb.taskID, criteria); err != nil {
		pb.logWarn("failed to persist criteria", "task_id", pb.taskID, "error", err)
	}
}

// SetPlan stores a plan and persists it.
func (pb *PersistentBlackboard) SetPlan(plan *Plan) {
	pb.MapBlackboard.SetPlan(plan)
	if err := pb.store.PersistPlan(pb.taskID, plan); err != nil {
		pb.logWarn("failed to persist plan", "task_id", pb.taskID, "error", err)
	}
}

// SetStepResult records a step result and persists it.
// The summary is generated using the same logic as MapBlackboard.
func (pb *PersistentBlackboard) SetStepResult(stepID, output string, err error, steps []Step) {
	pb.MapBlackboard.SetStepResult(stepID, output, err, steps)

	summary := orchestration.GenerateSummary(output)
	// Apply the same token-budget cap as MapBlackboard.
	if pb.MaxSummaryTokens() > 0 {
		maxChars := pb.MaxSummaryTokens() * 4
		if len(summary) > maxChars {
			summary = summary[:maxChars] + "..."
		}
	}

	var errText string
	if err != nil {
		errText = err.Error()
	}

	if pErr := pb.store.PersistStepResult(pb.taskID, stepID, summary, output, errText, steps); pErr != nil {
		pb.logWarn("failed to persist step result", "task_id", pb.taskID, "step_id", stepID, "error", pErr)
	}
}

// AddReflection appends a reflection and persists it.
func (pb *PersistentBlackboard) AddReflection(r Reflection) {
	pb.MapBlackboard.AddReflection(r)
	if err := pb.store.PersistReflection(pb.taskID, r); err != nil {
		pb.logWarn("failed to persist reflection", "task_id", pb.taskID, "error", err)
	}
}

// SetFinalResult sets the final result string.
// Does NOT call PersistCompletion — the orchestrator calls CompleteTask() separately
// with the full data (eval result + attempt count).
func (pb *PersistentBlackboard) SetFinalResult(result string) {
	pb.MapBlackboard.SetFinalResult(result)
}

// SetRouting persists the routing decision for the task.
func (pb *PersistentBlackboard) SetRouting(routing *RoutingDecision) {
	if err := pb.store.PersistRouting(pb.taskID, routing); err != nil {
		pb.logWarn("failed to persist routing", "task_id", pb.taskID, "error", err)
	}
}

// ---------------------------------------------------------------------------
// Additional methods (not part of Blackboard interface)
// ---------------------------------------------------------------------------

// CompleteTask marks the task as completed with evaluation results.
// Called by the orchestrator after Handle() succeeds.
func (pb *PersistentBlackboard) CompleteTask(evalResult *EvalResult, attemptCount int) {
	finalOutput := pb.GetFinalResult()
	if err := pb.store.PersistCompletion(pb.taskID, finalOutput, evalResult, attemptCount); err != nil {
		pb.logWarn("failed to persist task completion", "task_id", pb.taskID, "error", err)
	}
}

// FailTask marks the task as failed.
func (pb *PersistentBlackboard) FailTask() {
	if err := pb.store.PersistFailure(pb.taskID); err != nil {
		pb.logWarn("failed to persist task failure", "task_id", pb.taskID, "error", err)
	}
}

// TaskID returns the task ID.
func (pb *PersistentBlackboard) TaskID() string {
	return pb.taskID
}

// ---------------------------------------------------------------------------
// Restoration
// ---------------------------------------------------------------------------

// RestoreBlackboard loads a task's state from persistence and hydrates a PersistentBlackboard.
// Returns nil, nil if the task is not found.
func RestoreBlackboard(taskID, sessionID string, store TaskPersistence, logger *slog.Logger, opts ...MapBlackboardOption) (*PersistentBlackboard, error) {
	state, err := store.LoadTaskState(taskID)
	if err != nil {
		return nil, fmt.Errorf("failed to load task state: %w", err)
	}
	if state == nil {
		return nil, nil
	}

	mb := NewMapBlackboard(opts...)

	// Hydrate the in-memory blackboard with restored state.
	mb.SetOriginalRequest(state.OriginalRequest)
	if state.Plan != nil {
		mb.SetPlan(state.Plan)
	}
	if state.Criteria != nil {
		mb.SetCriteria(state.Criteria)
	}
	for stepID, sr := range state.StepResults {
		// Re-use the raw data; SetStepResult would regenerate the summary,
		// so we populate the map directly via SetStepResultRaw.
		mb.SetStepResultRaw(stepID, sr)
	}
	for _, r := range state.Reflections {
		mb.AddReflection(r)
	}
	if state.FinalOutput != "" {
		mb.SetFinalResult(state.FinalOutput)
	}

	return &PersistentBlackboard{
		MapBlackboard: mb,
		taskID:        taskID,
		sessionID:     sessionID,
		store:         store,
		logger:        logger,
	}, nil
}

// ---------------------------------------------------------------------------
// Helpers
// ---------------------------------------------------------------------------

// logWarn logs a warning message if the logger is non-nil.
func (pb *PersistentBlackboard) logWarn(msg string, args ...any) {
	if pb.logger != nil {
		pb.logger.Warn(msg, args...)
	}
}
