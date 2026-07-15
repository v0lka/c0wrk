package session

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"time"

	"github.com/v0lka/c0wrk/core"
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
