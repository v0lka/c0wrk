package session

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"time"

	"github.com/v0lka/c0wrk/core"
	"github.com/v0lka/c0wrk/core/e2s"
	"github.com/v0lka/c0wrk/core/goal"
	"github.com/v0lka/c0wrk/core/tools"
	"github.com/v0lka/c0wrk/core/units"
	"github.com/v0lka/sp4rk/agent"
	"github.com/v0lka/sp4rk/agent/router"
	"github.com/v0lka/sp4rk/orchestration"
)

// TaskStoreAdapter adapts a TaskStore to the core.TaskPersistence interface.
// It handles JSON serialization of core types for storage.
type TaskStoreAdapter struct {
	store TaskStore
}

// NewTaskStoreAdapter creates a new adapter wrapping the given TaskStore.
func NewTaskStoreAdapter(store TaskStore) *TaskStoreAdapter {
	return &TaskStoreAdapter{store: store}
}

// compile-time check
var _ core.TaskPersistence = (*TaskStoreAdapter)(nil)

var (
	emptyJSONObject = json.RawMessage("{}")
	emptyJSONArray  = json.RawMessage("[]")
)

// PersistNewTask creates a new task record with status "in_progress".
func (a *TaskStoreAdapter) PersistNewTask(taskID, sessionID, originalRequest string) error {
	return a.store.SaveTask(context.Background(), TaskRecord{
		ID:              taskID,
		SessionID:       sessionID,
		OriginalRequest: originalRequest,
		RoutingDecision: emptyJSONObject,
		Plan:            emptyJSONObject,
		Reflections:     emptyJSONArray,
		Status:          "in_progress",
		CreatedAt:       time.Now(),
	})
}

// PersistPlan JSON-marshals the plan and updates the task record.
func (a *TaskStoreAdapter) PersistPlan(taskID string, plan *orchestration.Plan) error {
	data, err := json.Marshal(plan)
	if err != nil {
		return fmt.Errorf("marshal plan: %w", err)
	}
	return a.store.UpdateTaskPlan(context.Background(), taskID, data)
}

// PersistRouting JSON-marshals the routing decision and updates the task record.
func (a *TaskStoreAdapter) PersistRouting(taskID string, routing *router.RoutingDecision) error {
	data, err := json.Marshal(routing)
	if err != nil {
		return fmt.Errorf("marshal routing: %w", err)
	}
	return a.store.UpdateTaskRouting(context.Background(), taskID, data)
}

// PersistStepResult creates a TaskStepRecord with JSON-marshaled steps.
func (a *TaskStoreAdapter) PersistStepResult(taskID, stepID, summary, fullOutput, errorText string, steps []agent.Step) error {
	stepsData, err := json.Marshal(steps)
	if err != nil {
		return fmt.Errorf("marshal steps: %w", err)
	}
	return a.store.SaveTaskStep(context.Background(), taskID, TaskStepRecord{
		StepID:     stepID,
		TaskID:     taskID,
		Summary:    summary,
		FullOutput: fullOutput,
		ErrorText:  errorText,
		Steps:      stepsData,
		CreatedAt:  time.Now(),
	})
}

// PersistReflection JSON-marshals the reflection and appends it to the task record.
func (a *TaskStoreAdapter) PersistReflection(taskID string, r orchestration.Reflection) error {
	data, err := json.Marshal(r)
	if err != nil {
		return fmt.Errorf("marshal reflection: %w", err)
	}
	return a.store.AddTaskReflection(context.Background(), taskID, data)
}

// PersistCompletion marks the task as completed.
func (a *TaskStoreAdapter) PersistCompletion(taskID, finalOutput string, attemptCount int) error {
	return a.store.CompleteTask(context.Background(), taskID, finalOutput, attemptCount)
}

// PersistFailure marks the task as failed.
func (a *TaskStoreAdapter) PersistFailure(taskID string) error {
	return a.store.FailTask(context.Background(), taskID)
}

// PersistCancellation marks the task as cancelled.
func (a *TaskStoreAdapter) PersistCancellation(taskID string) error {
	return a.store.CancelTask(context.Background(), taskID)
}

// PersistPause marks the task as paused so it survives app restart as a
// resumable checkpoint (GetUnfinishedTask matches the paused status).
func (a *TaskStoreAdapter) PersistPause(taskID string) error {
	return a.store.PauseTask(context.Background(), taskID)
}

// ReactivateTask reactivates a completed task back to in_progress.
func (a *TaskStoreAdapter) ReactivateTask(taskID string) error {
	return a.store.ReactivateTask(context.Background(), taskID)
}

// PersistFacts JSON-marshals facts and stores them for a task.
func (a *TaskStoreAdapter) PersistFacts(taskID string, facts []orchestration.Fact) error {
	data, err := json.Marshal(facts)
	if err != nil {
		return fmt.Errorf("marshal facts: %w", err)
	}
	return a.store.SaveFacts(context.Background(), taskID, data)
}

// PersistAttachments JSON-marshals attachments and stores them for a task.
func (a *TaskStoreAdapter) PersistAttachments(taskID string, attachments []orchestration.Attachment) error {
	data, err := json.Marshal(attachments)
	if err != nil {
		return fmt.Errorf("marshal attachments: %w", err)
	}
	return a.store.SaveAttachments(context.Background(), taskID, data)
}

// SaveTrajectory JSON-marshals the full Conductor step trajectory and stores it
// for a task, inserting or replacing any previously persisted trajectory.
func (a *TaskStoreAdapter) SaveTrajectory(taskID string, steps []agent.Step) error {
	data, err := json.Marshal(steps)
	if err != nil {
		return fmt.Errorf("marshal trajectory steps: %w", err)
	}
	return a.store.SaveTrajectory(context.Background(), taskID, data)
}

// LoadTrajectory loads the Conductor step trajectory for a task and unmarshals
// it into []agent.Step. Returns nil, nil when no trajectory has been persisted.
func (a *TaskStoreAdapter) LoadTrajectory(taskID string) ([]agent.Step, error) {
	data, err := a.store.LoadTrajectory(context.Background(), taskID)
	if err != nil {
		return nil, fmt.Errorf("load trajectory: %w", err)
	}
	if len(data) == 0 {
		return nil, nil
	}
	var steps []agent.Step
	if err := json.Unmarshal(data, &steps); err != nil {
		return nil, fmt.Errorf("unmarshal trajectory steps: %w", err)
	}
	return steps, nil
}

// PersistGoalState JSON-marshals the goal-loop state and stores it for a task,
// inserting or replacing any previously persisted goal state.
func (a *TaskStoreAdapter) PersistGoalState(taskID string, gs *goal.GoalState) error {
	data, err := json.Marshal(gs)
	if err != nil {
		return fmt.Errorf("marshal goal state: %w", err)
	}
	return a.store.SaveGoalState(context.Background(), taskID, data)
}

// LoadGoalState loads the goal-loop state for a task and unmarshals it into a
// *goal.GoalState. Returns nil, nil when no goal state has been persisted.
func (a *TaskStoreAdapter) LoadGoalState(taskID string) (*goal.GoalState, error) {
	data, err := a.store.LoadGoalState(context.Background(), taskID)
	if err != nil {
		return nil, fmt.Errorf("load goal state: %w", err)
	}
	if len(data) == 0 {
		return nil, nil
	}
	var gs goal.GoalState
	if err := json.Unmarshal(data, &gs); err != nil {
		return nil, fmt.Errorf("unmarshal goal state: %w", err)
	}
	return &gs, nil
}

// PersistE2SState JSON-marshals the E2S execution state (Σ + bookkeeping) and
// stores it for a task, inserting or replacing any previously persisted state.
// Called after every applied state patch so an interrupted run checkpoints its
// Σ and survives app restart.
func (a *TaskStoreAdapter) PersistE2SState(taskID string, st *e2s.E2SState) error {
	data, err := json.Marshal(st)
	if err != nil {
		return fmt.Errorf("marshal e2s state: %w", err)
	}
	return a.store.SaveE2SState(context.Background(), taskID, data)
}

// LoadE2SState loads the E2S execution state for a task and unmarshals it into
// an *e2s.E2SState. Returns nil, nil when no E2S state has been persisted
// (non-E2S tasks).
func (a *TaskStoreAdapter) LoadE2SState(taskID string) (*e2s.E2SState, error) {
	data, err := a.store.LoadE2SState(context.Background(), taskID)
	if err != nil {
		return nil, fmt.Errorf("load e2s state: %w", err)
	}
	if len(data) == 0 {
		return nil, nil
	}
	var st e2s.E2SState
	if err := json.Unmarshal(data, &st); err != nil {
		return nil, fmt.Errorf("unmarshal e2s state: %w", err)
	}
	return &st, nil
}

// PersistDelegationSpec JSON-marshals the delegation spec (task text, tools,
// agent profile, mode, deps, parent/depth) and stores it for a task so a
// paused delegation can be rebuilt and resumed by the system.
func (a *TaskStoreAdapter) PersistDelegationSpec(taskID string, spec tools.DelegationSpec) error {
	data, err := json.Marshal(spec)
	if err != nil {
		return fmt.Errorf("marshal delegation spec: %w", err)
	}
	return a.store.SaveDelegationSpec(context.Background(), taskID, TaskDelegationRecord{
		DelegationID: spec.Task.ID,
		TaskID:       taskID,
		ParentID:     spec.ParentID,
		Depth:        spec.Depth,
		Spec:         data,
		CreatedAt:    time.Now(),
	})
}

// LoadDelegationSpecs loads the delegation specs for a task and unmarshals
// them into []tools.DelegationSpec. Returns nil, nil when none are persisted.
func (a *TaskStoreAdapter) LoadDelegationSpecs(taskID string) ([]tools.DelegationSpec, error) {
	recs, err := a.store.LoadDelegationSpecs(context.Background(), taskID)
	if err != nil {
		return nil, fmt.Errorf("load delegation specs: %w", err)
	}
	if len(recs) == 0 {
		return nil, nil
	}
	specs := make([]tools.DelegationSpec, 0, len(recs))
	for _, rec := range recs {
		var spec tools.DelegationSpec
		if err := json.Unmarshal(rec.Spec, &spec); err != nil {
			return nil, fmt.Errorf("unmarshal delegation spec %s: %w", rec.DelegationID, err)
		}
		specs = append(specs, spec)
	}
	return specs, nil
}

// LoadTaskState loads a task and its steps from the store, deserializes JSON back
// to core types, and returns a populated *core.TaskState.
// Returns nil, nil if the task is not found.
func (a *TaskStoreAdapter) LoadTaskState(taskID string) (*core.TaskState, error) {
	rec, err := a.store.LoadTask(context.Background(), taskID)
	if err != nil {
		return nil, fmt.Errorf("load task: %w", err)
	}
	if rec == nil {
		return nil, nil
	}

	state := &core.TaskState{
		TaskID:          rec.ID,
		SessionID:       rec.SessionID,
		OriginalRequest: rec.OriginalRequest,
		FinalOutput:     rec.FinalOutput,
		Status:          rec.Status,
	}

	// Unmarshal routing decision
	if len(rec.RoutingDecision) > 0 && string(rec.RoutingDecision) != "{}" {
		var routing router.RoutingDecision
		if err := json.Unmarshal(rec.RoutingDecision, &routing); err != nil {
			return nil, fmt.Errorf("unmarshal routing decision: %w", err)
		}
		state.RoutingDecision = &routing
	}

	// Unmarshal plan
	if len(rec.Plan) > 0 && string(rec.Plan) != "{}" {
		var plan orchestration.Plan
		if err := json.Unmarshal(rec.Plan, &plan); err != nil {
			return nil, fmt.Errorf("unmarshal plan: %w", err)
		}
		state.Plan = &plan
	}

	// Unmarshal reflections
	if len(rec.Reflections) > 0 && string(rec.Reflections) != "[]" {
		if err := json.Unmarshal(rec.Reflections, &state.Reflections); err != nil {
			return nil, fmt.Errorf("unmarshal reflections: %w", err)
		}
	}

	// Load step records
	stepRecords, err := a.store.LoadTaskSteps(context.Background(), taskID)
	if err != nil {
		return nil, fmt.Errorf("load task steps: %w", err)
	}

	state.StepResults = make(map[string]orchestration.StepResult, len(stepRecords))
	for _, sr := range stepRecords {
		var errVal error
		if sr.ErrorText != "" {
			errVal = errors.New(sr.ErrorText)
		}

		result := orchestration.StepResult{
			StepID:     sr.StepID,
			Summary:    sr.Summary,
			FullOutput: sr.FullOutput,
			Error:      errVal,
		}

		// Unmarshal steps
		if len(sr.Steps) > 0 && string(sr.Steps) != "[]" {
			if err := json.Unmarshal(sr.Steps, &result.Steps); err != nil {
				return nil, fmt.Errorf("unmarshal steps for %s: %w", sr.StepID, err)
			}
		}

		state.StepResults[sr.StepID] = result
	}

	// Load facts
	factsJSON, err := a.store.LoadFacts(context.Background(), taskID)
	if err != nil {
		return nil, fmt.Errorf("load facts: %w", err)
	}
	if len(factsJSON) > 0 {
		if err := json.Unmarshal(factsJSON, &state.Facts); err != nil {
			return nil, fmt.Errorf("unmarshal facts: %w", err)
		}
	}

	// Load attachments
	attachmentsJSON, err := a.store.LoadAttachments(context.Background(), taskID)
	if err != nil {
		return nil, fmt.Errorf("load attachments: %w", err)
	}
	if len(attachmentsJSON) > 0 {
		if err := json.Unmarshal(attachmentsJSON, &state.Attachments); err != nil {
			return nil, fmt.Errorf("unmarshal attachments: %w", err)
		}
	}

	// Load goal state (nil for non-goal tasks).
	goalJSON, err := a.store.LoadGoalState(context.Background(), taskID)
	if err != nil {
		return nil, fmt.Errorf("load goal state: %w", err)
	}
	if len(goalJSON) > 0 {
		var gs goal.GoalState
		if err := json.Unmarshal(goalJSON, &gs); err != nil {
			return nil, fmt.Errorf("unmarshal goal state: %w", err)
		}
		state.GoalState = &gs
	}

	// Load delegation specs (empty for plan-only tasks or fresh tasks).
	specs, err := a.LoadDelegationSpecs(taskID)
	if err != nil {
		return nil, fmt.Errorf("load delegation specs: %w", err)
	}
	state.Delegations = specs

	return state, nil
}

// GetUnfinishedTaskID returns the ID of the most recent in-progress task for the
// given session, or "" if none exists.
func (a *TaskStoreAdapter) GetUnfinishedTaskID(sessionID string) (string, error) {
	rec, err := a.store.GetUnfinishedTask(context.Background(), sessionID)
	if err != nil {
		return "", err
	}
	if rec == nil {
		return "", nil
	}
	return rec.ID, nil
}

// GetLatestTaskID returns the ID of the most recent task for the session,
// regardless of status, or "" if none exists. Unlike GetUnfinishedTaskID it is
// status-agnostic, so it still locates a task whose row was flipped to a
// terminal status (e.g. cancelled/completed) by CancelTask. Used when a caller
// needs the latest task row regardless of its lifecycle state.
func (a *TaskStoreAdapter) GetLatestTaskID(sessionID string) (string, error) {
	return a.store.GetLatestTaskID(context.Background(), sessionID)
}

// ---------------------------------------------------------------------------
// Durable unit ledger facade (core/units.Store)
// ---------------------------------------------------------------------------

// unitTaskStore is the optional unit-persistence capability a TaskStore may
// implement. It is asserted rather than added to the TaskStore interface so
// stores that predate the unit ledger — and their test doubles — keep
// compiling unchanged; a store without it simply has no durable unit storage.
type unitTaskStore interface {
	SaveTaskUnit(ctx context.Context, rec TaskUnitRecord) error
	LoadTaskUnits(ctx context.Context, taskID string) ([]TaskUnitRecord, error)
}

// unitStoreFacade adapts a task store's optional unit persistence to the
// core/units.Store contract — the facade over existing storage that the unit
// ledger writes through. It reuses the store's SQLite session database (the
// same connection pool and tasks-table lifecycle as every other task record).
type unitStoreFacade struct {
	us unitTaskStore
}

// SaveUnit persists a unit record (spec + status + steps) for a task.
func (f *unitStoreFacade) SaveUnit(rec units.UnitRecord) error {
	return f.us.SaveTaskUnit(context.Background(), toTaskUnitRecord(rec))
}

// LoadUnits returns every unit persisted under a task, across all namespaces.
func (f *unitStoreFacade) LoadUnits(taskID string) ([]units.UnitRecord, error) {
	recs, err := f.us.LoadTaskUnits(context.Background(), taskID)
	if err != nil {
		return nil, err
	}
	out := make([]units.UnitRecord, 0, len(recs))
	for _, rec := range recs {
		out = append(out, fromTaskUnitRecord(rec))
	}
	return out, nil
}

// UnitStore returns the durable unit store backing this task store, or nil
// when the underlying store does not provide unit persistence. The nil return
// is the graceful-degradation signal: a core/units ledger built on it falls
// back to best-effort in-memory operation. This is the ISOLATED-context entry
// point — an isolated executor (e.g. the goal verifier) wraps its task store
// with NewTaskStoreAdapter and calls this to obtain a store for units.NewLedger.
func (a *TaskStoreAdapter) UnitStore() units.Store {
	if us, ok := a.store.(unitTaskStore); ok {
		return &unitStoreFacade{us: us}
	}
	return nil
}

// toTaskUnitRecord maps a core/units record onto its persisted form.
func toTaskUnitRecord(rec units.UnitRecord) TaskUnitRecord {
	return TaskUnitRecord{
		TaskID:    rec.TaskID,
		UnitID:    rec.ID,
		Namespace: rec.Namespace,
		Kind:      string(rec.Kind),
		ParentID:  rec.ParentID,
		Depth:     rec.Depth,
		Status:    string(rec.Status),
		Spec:      rec.Spec,
		Steps:     rec.Steps,
		CreatedAt: rec.CreatedAt,
		UpdatedAt: rec.UpdatedAt,
	}
}

// fromTaskUnitRecord maps a persisted unit back onto its core/units form.
func fromTaskUnitRecord(rec TaskUnitRecord) units.UnitRecord {
	return units.UnitRecord{
		ID:        rec.UnitID,
		TaskID:    rec.TaskID,
		Namespace: rec.Namespace,
		Kind:      units.UnitKind(rec.Kind),
		ParentID:  rec.ParentID,
		Depth:     rec.Depth,
		Status:    units.UnitStatus(rec.Status),
		Spec:      rec.Spec,
		Steps:     rec.Steps,
		CreatedAt: rec.CreatedAt,
		UpdatedAt: rec.UpdatedAt,
	}
}
