package session

import (
	"encoding/json"
	"errors"
	"fmt"
	"time"

	"github.com/user/agent/core"
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

// PersistNewTask creates a new task record with status "in_progress".
func (a *TaskStoreAdapter) PersistNewTask(taskID, sessionID, originalRequest string) error {
	return a.store.SaveTask(TaskRecord{
		ID:              taskID,
		SessionID:       sessionID,
		OriginalRequest: originalRequest,
		RoutingDecision: json.RawMessage("{}"),
		Plan:            json.RawMessage("{}"),
		Criteria:        json.RawMessage("[]"),
		EvalResult:      json.RawMessage("{}"),
		Reflections:     json.RawMessage("[]"),
		Status:          "in_progress",
		CreatedAt:       time.Now(),
	})
}

// PersistPlan JSON-marshals the plan and updates the task record.
func (a *TaskStoreAdapter) PersistPlan(taskID string, plan *core.Plan) error {
	data, err := json.Marshal(plan)
	if err != nil {
		return fmt.Errorf("marshal plan: %w", err)
	}
	return a.store.UpdateTaskPlan(taskID, data)
}

// PersistCriteria JSON-marshals the criteria and updates the task record.
func (a *TaskStoreAdapter) PersistCriteria(taskID string, criteria []core.AcceptanceCriterion) error {
	data, err := json.Marshal(criteria)
	if err != nil {
		return fmt.Errorf("marshal criteria: %w", err)
	}
	return a.store.UpdateTaskCriteria(taskID, data)
}

// PersistRouting JSON-marshals the routing decision and updates the task record.
func (a *TaskStoreAdapter) PersistRouting(taskID string, routing *core.RoutingDecision) error {
	data, err := json.Marshal(routing)
	if err != nil {
		return fmt.Errorf("marshal routing: %w", err)
	}
	return a.store.UpdateTaskRouting(taskID, data)
}

// PersistStepResult creates a TaskStepRecord with JSON-marshaled steps.
func (a *TaskStoreAdapter) PersistStepResult(taskID, stepID, summary, fullOutput, errorText string, steps []core.Step) error {
	stepsData, err := json.Marshal(steps)
	if err != nil {
		return fmt.Errorf("marshal steps: %w", err)
	}
	return a.store.SaveTaskStep(taskID, TaskStepRecord{
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
func (a *TaskStoreAdapter) PersistReflection(taskID string, r core.Reflection) error {
	data, err := json.Marshal(r)
	if err != nil {
		return fmt.Errorf("marshal reflection: %w", err)
	}
	return a.store.AddTaskReflection(taskID, data)
}

// PersistCompletion JSON-marshals the eval result and marks the task as completed.
func (a *TaskStoreAdapter) PersistCompletion(taskID, finalOutput string, evalResult *core.EvalResult, attemptCount int) error {
	var evalData json.RawMessage
	if evalResult != nil {
		var err error
		evalData, err = json.Marshal(evalResult)
		if err != nil {
			return fmt.Errorf("marshal eval result: %w", err)
		}
	} else {
		evalData = json.RawMessage("{}")
	}
	return a.store.CompleteTask(taskID, finalOutput, evalData, attemptCount)
}

// PersistFailure marks the task as failed.
func (a *TaskStoreAdapter) PersistFailure(taskID string) error {
	return a.store.FailTask(taskID)
}

// LoadTaskState loads a task and its steps from the store, deserializes JSON back
// to core types, and returns a populated *core.TaskState.
// Returns nil, nil if the task is not found.
func (a *TaskStoreAdapter) LoadTaskState(taskID string) (*core.TaskState, error) {
	rec, err := a.store.LoadTask(taskID)
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
		var routing core.RoutingDecision
		if err := json.Unmarshal(rec.RoutingDecision, &routing); err != nil {
			return nil, fmt.Errorf("unmarshal routing decision: %w", err)
		}
		state.RoutingDecision = &routing
	}

	// Unmarshal plan
	if len(rec.Plan) > 0 && string(rec.Plan) != "{}" {
		var plan core.Plan
		if err := json.Unmarshal(rec.Plan, &plan); err != nil {
			return nil, fmt.Errorf("unmarshal plan: %w", err)
		}
		state.Plan = &plan
	}

	// Unmarshal criteria
	if len(rec.Criteria) > 0 && string(rec.Criteria) != "[]" {
		if err := json.Unmarshal(rec.Criteria, &state.Criteria); err != nil {
			return nil, fmt.Errorf("unmarshal criteria: %w", err)
		}
	}

	// Unmarshal reflections
	if len(rec.Reflections) > 0 && string(rec.Reflections) != "[]" {
		if err := json.Unmarshal(rec.Reflections, &state.Reflections); err != nil {
			return nil, fmt.Errorf("unmarshal reflections: %w", err)
		}
	}

	// Load step records
	stepRecords, err := a.store.LoadTaskSteps(taskID)
	if err != nil {
		return nil, fmt.Errorf("load task steps: %w", err)
	}

	state.StepResults = make(map[string]core.StepResult, len(stepRecords))
	for _, sr := range stepRecords {
		var errVal error
		if sr.ErrorText != "" {
			errVal = errors.New(sr.ErrorText)
		}

		result := core.StepResult{
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

	return state, nil
}

// GetUnfinishedTaskID returns the ID of the most recent in-progress task for the
// given session, or "" if none exists.
func (a *TaskStoreAdapter) GetUnfinishedTaskID(sessionID string) (string, error) {
	rec, err := a.store.GetUnfinishedTask(sessionID)
	if err != nil {
		return "", err
	}
	if rec == nil {
		return "", nil
	}
	return rec.ID, nil
}
