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

// Adapts a TaskStore to core.TaskPersistence, handling JSON serialization of
// core types at the boundary. See specs/contracts/backend-core.md.
type TaskStoreAdapter struct {
	store TaskStore
}

func NewTaskStoreAdapter(store TaskStore) *TaskStoreAdapter {
	return &TaskStoreAdapter{store: store}
}

// compile-time check
var _ core.TaskPersistence = (*TaskStoreAdapter)(nil)

// BOUNDS EVERY adapter store call: core.TaskPersistence predates context
// threading (no ctx to forward) and all calls share the app's single SQLite
// pool — an unbounded pool-wait would park the synchronous terminal
// finalizers and the task UI would stay "running" forever. Same bound as
// the restore head-reads: contention becomes a prompt, retryable error.
// See specs/contracts/backend-core.md (TaskStoreAdapter store-call deadline).
const storeCallTimeout = restoreDBReadTimeout

// Deadline-bounded context for one adapter store call; call per call site
// with `defer cancel()` (the calls are synchronous).
func boundedStoreCtx() (context.Context, context.CancelFunc) {
	return context.WithTimeout(context.Background(), storeCallTimeout)
}

var (
	emptyJSONObject = json.RawMessage("{}")
	emptyJSONArray  = json.RawMessage("[]")
)

func (a *TaskStoreAdapter) PersistNewTask(taskID, sessionID, originalRequest string) error {
	ctx, cancel := boundedStoreCtx()
	defer cancel()
	return a.store.SaveTask(ctx, TaskRecord{
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

// PersistMCPMentions preserves the request context for authorization-state writes.
func (a *TaskStoreAdapter) PersistMCPMentions(ctx context.Context, taskID string, names []string) error {
	return a.store.SaveMCPMentions(ctx, taskID, names)
}

// LoadMCPMentions restores durable task intent without a cache fallback.
func (a *TaskStoreAdapter) LoadMCPMentions(ctx context.Context, taskID string) ([]string, error) {
	return a.store.LoadMCPMentions(ctx, taskID)
}

func (a *TaskStoreAdapter) PersistPlan(taskID string, plan *orchestration.Plan) error {
	data, err := json.Marshal(plan)
	if err != nil {
		return fmt.Errorf("marshal plan: %w", err)
	}
	ctx, cancel := boundedStoreCtx()
	defer cancel()
	return a.store.UpdateTaskPlan(ctx, taskID, data)
}

func (a *TaskStoreAdapter) PersistRouting(taskID string, routing *router.RoutingDecision) error {
	data, err := json.Marshal(routing)
	if err != nil {
		return fmt.Errorf("marshal routing: %w", err)
	}
	ctx, cancel := boundedStoreCtx()
	defer cancel()
	return a.store.UpdateTaskRouting(ctx, taskID, data)
}

func (a *TaskStoreAdapter) PersistStepResult(taskID, stepID, summary, fullOutput, errorText string, steps []agent.Step) error {
	stepsData, err := json.Marshal(steps)
	if err != nil {
		return fmt.Errorf("marshal steps: %w", err)
	}
	ctx, cancel := boundedStoreCtx()
	defer cancel()
	return a.store.SaveTaskStep(ctx, taskID, TaskStepRecord{
		StepID:     stepID,
		TaskID:     taskID,
		Summary:    summary,
		FullOutput: fullOutput,
		ErrorText:  errorText,
		Steps:      stepsData,
		CreatedAt:  time.Now(),
	})
}

func (a *TaskStoreAdapter) PersistReflection(taskID string, r orchestration.Reflection) error {
	data, err := json.Marshal(r)
	if err != nil {
		return fmt.Errorf("marshal reflection: %w", err)
	}
	ctx, cancel := boundedStoreCtx()
	defer cancel()
	return a.store.AddTaskReflection(ctx, taskID, data)
}

func (a *TaskStoreAdapter) PersistCompletion(taskID, finalOutput string, attemptCount int) error {
	ctx, cancel := boundedStoreCtx()
	defer cancel()
	return a.store.CompleteTask(ctx, taskID, finalOutput, attemptCount)
}

func (a *TaskStoreAdapter) PersistFailure(taskID string) error {
	ctx, cancel := boundedStoreCtx()
	defer cancel()
	return a.store.FailTask(ctx, taskID)
}

func (a *TaskStoreAdapter) PersistCancellation(taskID string) error {
	ctx, cancel := boundedStoreCtx()
	defer cancel()
	return a.store.CancelTask(ctx, taskID)
}

// PersistPause marks the task as paused so it survives app restart as a
// resumable checkpoint (GetUnfinishedTask matches the paused status).
func (a *TaskStoreAdapter) PersistPause(taskID string) error {
	ctx, cancel := boundedStoreCtx()
	defer cancel()
	return a.store.PauseTask(ctx, taskID)
}

func (a *TaskStoreAdapter) ReactivateTask(taskID string) error {
	ctx, cancel := boundedStoreCtx()
	defer cancel()
	return a.store.ReactivateTask(ctx, taskID)
}

func (a *TaskStoreAdapter) PersistFacts(taskID string, facts []orchestration.Fact) error {
	data, err := json.Marshal(facts)
	if err != nil {
		return fmt.Errorf("marshal facts: %w", err)
	}
	ctx, cancel := boundedStoreCtx()
	defer cancel()
	return a.store.SaveFacts(ctx, taskID, data)
}

func (a *TaskStoreAdapter) PersistAttachments(taskID string, attachments []orchestration.Attachment) error {
	data, err := json.Marshal(attachments)
	if err != nil {
		return fmt.Errorf("marshal attachments: %w", err)
	}
	ctx, cancel := boundedStoreCtx()
	defer cancel()
	return a.store.SaveAttachments(ctx, taskID, data)
}

func (a *TaskStoreAdapter) SaveTrajectory(taskID string, steps []agent.Step) error {
	data, err := json.Marshal(steps)
	if err != nil {
		return fmt.Errorf("marshal trajectory steps: %w", err)
	}
	ctx, cancel := boundedStoreCtx()
	defer cancel()
	return a.store.SaveTrajectory(ctx, taskID, data)
}

// Returns nil, nil when no trajectory has been persisted.
func (a *TaskStoreAdapter) LoadTrajectory(taskID string) ([]agent.Step, error) {
	ctx, cancel := boundedStoreCtx()
	defer cancel()
	data, err := a.store.LoadTrajectory(ctx, taskID)
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

func (a *TaskStoreAdapter) PersistGoalState(taskID string, gs *goal.GoalState) error {
	data, err := json.Marshal(gs)
	if err != nil {
		return fmt.Errorf("marshal goal state: %w", err)
	}
	ctx, cancel := boundedStoreCtx()
	defer cancel()
	return a.store.SaveGoalState(ctx, taskID, data)
}

// Returns nil, nil when no goal state has been persisted (non-goal tasks).
func (a *TaskStoreAdapter) LoadGoalState(taskID string) (*goal.GoalState, error) {
	ctx, cancel := boundedStoreCtx()
	defer cancel()
	data, err := a.store.LoadGoalState(ctx, taskID)
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

// Called after every applied state patch so an interrupted run checkpoints
// its Σ and survives app restart.
func (a *TaskStoreAdapter) PersistE2SState(taskID string, st *e2s.E2SState) error {
	data, err := json.Marshal(st)
	if err != nil {
		return fmt.Errorf("marshal e2s state: %w", err)
	}
	ctx, cancel := boundedStoreCtx()
	defer cancel()
	return a.store.SaveE2SState(ctx, taskID, data)
}

// Returns nil, nil when no E2S state has been persisted (non-E2S tasks).
func (a *TaskStoreAdapter) LoadE2SState(taskID string) (*e2s.E2SState, error) {
	ctx, cancel := boundedStoreCtx()
	defer cancel()
	data, err := a.store.LoadE2SState(ctx, taskID)
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

// Stored so a paused delegation can be rebuilt and resumed by the system.
func (a *TaskStoreAdapter) PersistDelegationSpec(taskID string, spec tools.DelegationSpec) error {
	data, err := json.Marshal(spec)
	if err != nil {
		return fmt.Errorf("marshal delegation spec: %w", err)
	}
	ctx, cancel := boundedStoreCtx()
	defer cancel()
	return a.store.SaveDelegationSpec(ctx, taskID, TaskDelegationRecord{
		DelegationID: spec.Task.ID,
		TaskID:       taskID,
		ParentID:     spec.ParentID,
		Depth:        spec.Depth,
		Spec:         data,
		CreatedAt:    time.Now(),
	})
}

// Returns nil, nil when none are persisted.
func (a *TaskStoreAdapter) LoadDelegationSpecs(taskID string) ([]tools.DelegationSpec, error) {
	ctx, cancel := boundedStoreCtx()
	defer cancel()
	recs, err := a.store.LoadDelegationSpecs(ctx, taskID)
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

// Rebuilds a populated *core.TaskState from the store; nil, nil when the
// task is not found.
func (a *TaskStoreAdapter) LoadTaskState(taskID string) (*core.TaskState, error) {
	ctx, cancel := boundedStoreCtx()
	defer cancel()
	rec, err := a.store.LoadTask(ctx, taskID)
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

	if len(rec.RoutingDecision) > 0 && string(rec.RoutingDecision) != "{}" {
		var routing router.RoutingDecision
		if err := json.Unmarshal(rec.RoutingDecision, &routing); err != nil {
			return nil, fmt.Errorf("unmarshal routing decision: %w", err)
		}
		state.RoutingDecision = &routing
	}

	if len(rec.Plan) > 0 && string(rec.Plan) != "{}" {
		var plan orchestration.Plan
		if err := json.Unmarshal(rec.Plan, &plan); err != nil {
			return nil, fmt.Errorf("unmarshal plan: %w", err)
		}
		state.Plan = &plan
	}

	if len(rec.Reflections) > 0 && string(rec.Reflections) != "[]" {
		if err := json.Unmarshal(rec.Reflections, &state.Reflections); err != nil {
			return nil, fmt.Errorf("unmarshal reflections: %w", err)
		}
	}

	ctx2, cancel2 := boundedStoreCtx()
	defer cancel2()
	stepRecords, err := a.store.LoadTaskSteps(ctx2, taskID)
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

		if len(sr.Steps) > 0 && string(sr.Steps) != "[]" {
			if err := json.Unmarshal(sr.Steps, &result.Steps); err != nil {
				return nil, fmt.Errorf("unmarshal steps for %s: %w", sr.StepID, err)
			}
		}

		state.StepResults[sr.StepID] = result
	}

	ctx3, cancel3 := boundedStoreCtx()
	defer cancel3()
	factsJSON, err := a.store.LoadFacts(ctx3, taskID)
	if err != nil {
		return nil, fmt.Errorf("load facts: %w", err)
	}
	if len(factsJSON) > 0 {
		if err := json.Unmarshal(factsJSON, &state.Facts); err != nil {
			return nil, fmt.Errorf("unmarshal facts: %w", err)
		}
	}

	ctx4, cancel4 := boundedStoreCtx()
	defer cancel4()
	attachmentsJSON, err := a.store.LoadAttachments(ctx4, taskID)
	if err != nil {
		return nil, fmt.Errorf("load attachments: %w", err)
	}
	if len(attachmentsJSON) > 0 {
		if err := json.Unmarshal(attachmentsJSON, &state.Attachments); err != nil {
			return nil, fmt.Errorf("unmarshal attachments: %w", err)
		}
	}

	// Load goal state (nil for non-goal tasks).
	ctx5, cancel5 := boundedStoreCtx()
	defer cancel5()
	goalJSON, err := a.store.LoadGoalState(ctx5, taskID)
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

func (a *TaskStoreAdapter) GetUnfinishedTaskID(sessionID string) (string, error) {
	ctx, cancel := boundedStoreCtx()
	defer cancel()
	rec, err := a.store.GetUnfinishedTask(ctx, sessionID)
	if err != nil {
		return "", err
	}
	if rec == nil {
		return "", nil
	}
	return rec.ID, nil
}

// Status-agnostic counterpart of GetUnfinishedTaskID: still locates a task
// whose row was flipped to a terminal status (e.g. by CancelTask).
func (a *TaskStoreAdapter) GetLatestTaskID(sessionID string) (string, error) {
	ctx, cancel := boundedStoreCtx()
	defer cancel()
	return a.store.GetLatestTaskID(ctx, sessionID)
}

// Durable unit ledger facade (core/units.Store).

// Optional unit-persistence capability a TaskStore may implement; asserted
// rather than added to the interface so pre-ledger stores — and their test
// doubles — keep compiling unchanged.
type unitTaskStore interface {
	SaveTaskUnit(ctx context.Context, rec TaskUnitRecord) error
	LoadTaskUnits(ctx context.Context, taskID string) ([]TaskUnitRecord, error)
}

// Adapts a task store's optional unit persistence to the core/units.Store
// contract, reusing the store's SQLite session database.
type unitStoreFacade struct {
	us unitTaskStore
}

func (f *unitStoreFacade) SaveUnit(rec units.UnitRecord) error {
	ctx, cancel := boundedStoreCtx()
	defer cancel()
	return f.us.SaveTaskUnit(ctx, toTaskUnitRecord(rec))
}

// Optional targeted-status capability: the facade settles through a single
// column-scoped conditional UPDATE when present; otherwise a whole-record
// load-mutate-save, correct but able to drop a concurrent writer's spec/steps.
type unitStatusSettler interface {
	SettleTaskUnitStatusIfInFlight(ctx context.Context, taskID, namespace, unitID, status string) (bool, error)
}

// Transitions a unit's status only while it is still in flight
// (pending/running), touching the status column alone.
func (f *unitStoreFacade) SettleUnitStatusIfInFlight(taskID, namespace, id string, status units.UnitStatus) (bool, error) {
	if ss, ok := f.us.(unitStatusSettler); ok {
		ctx, cancel := boundedStoreCtx()
		defer cancel()
		return ss.SettleTaskUnitStatusIfInFlight(ctx, taskID, namespace, id, string(status))
	}
	ctx, cancel := boundedStoreCtx()
	defer cancel()
	recs, err := f.us.LoadTaskUnits(ctx, taskID)
	if err != nil {
		return false, err
	}
	for _, rec := range recs {
		if rec.UnitID != id || rec.Namespace != namespace {
			continue
		}
		if !units.UnitStatus(rec.Status).InFlight() {
			return false, nil
		}
		rec.Status = string(status)
		rec.UpdatedAt = time.Now().UTC()
		ctx, cancel := boundedStoreCtx()
		err := f.us.SaveTaskUnit(ctx, rec)
		cancel()
		return true, err
	}
	return false, nil
}

func (f *unitStoreFacade) LoadUnits(taskID string) ([]units.UnitRecord, error) {
	ctx, cancel := boundedStoreCtx()
	defer cancel()
	recs, err := f.us.LoadTaskUnits(ctx, taskID)
	if err != nil {
		return nil, err
	}
	out := make([]units.UnitRecord, 0, len(recs))
	for _, rec := range recs {
		out = append(out, fromTaskUnitRecord(rec))
	}
	return out, nil
}

// The durable unit store backing this task store, or nil when the store
// has no unit persistence (graceful degradation — a ledger built on nil
// falls back to best-effort in-memory). The ISOLATED-context entry point
// (the goal verifier). See specs/decisions/048-unified-recovery-ledger.md.
func (a *TaskStoreAdapter) UnitStore() units.Store {
	if us, ok := a.store.(unitTaskStore); ok {
		return &unitStoreFacade{us: us}
	}
	return nil
}

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
