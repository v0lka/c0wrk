package session

import (
	"context"
	"errors"
	"fmt"
	"time"

	"github.com/v0lka/c0wrk/core"
	sdktools "github.com/v0lka/c0wrk/sdk/tools"
	"github.com/v0lka/c0wrk/sdk/orchestration"
)

// SendMessage sends a user message to a session's orchestrator (async).
// Runs in a goroutine, results come via events.
func (m *Manager) SendMessage(ctx context.Context, id, text, mode string, activeSkills []string) error {
	session, err := m.getOrRestoreSession(id)
	if err != nil {
		return fmt.Errorf("failed to restore session: %w", err)
	}
	if session == nil {
		return fmt.Errorf("session not found: %s", id)
	}

	session.mu.Lock()
	// Check if already active (prevent double-send on the same session)
	if session.active {
		session.mu.Unlock()
		return errors.New("session is already processing a task")
	}

	// Set active and create cancellable context with session ID
	session.active = true
	doneCh := make(chan struct{})
	session.done = doneCh
	taskCtx, cancel := context.WithCancel(ContextWithSessionID(ctx, id))
	// Enrich context with session workspace path for tool security heuristics
	taskCtx = sdktools.WithWorkspacePath(taskCtx, session.WorkspacePath)
	taskCtx = sdktools.WithTempDir(taskCtx, session.TempDir)
	taskCtx = sdktools.WithCoherence(taskCtx, m.fileTracker)
	session.cancel = cancel
	session.mu.Unlock()

	// Snapshot envInfo under read lock
	m.mu.RLock()
	envInfo := m.envInfo
	m.mu.RUnlock()
	if envInfo != nil {
		taskCtx = sdktools.WithEnvInfo(taskCtx, envInfo)
	}

	// Emit message received event
	m.emitFunc(Event{
		SessionID: id,
		Type:      "message_received",
		Data: MessageReceivedData{
			SessionID: id,
			Text:      text,
		},
	})

	// Check if this is the first message (session has default name)
	// and spawn title generation in background.
	session.mu.Lock()
	sessionName := session.Name
	session.mu.Unlock()
	m.mu.RLock()
	titleGen := m.titleGen
	store := m.sessionStore
	m.mu.RUnlock()
	if sessionName == "Session "+safeSessionPrefix(id) && titleGen != nil {
		go func() {
			ctx, cancel := context.WithTimeout(context.Background(), 15*time.Second)
			defer cancel()
			title := titleGen.Generate(ctx, text, activeSkills)
			if title == "" {
				return
			}
			if err := m.RenameSession(id, title); err != nil {
				m.log().Warn("failed to rename session with generated title", "session", id, "error", err)
				return
			}
			m.log().Info("session auto-named", "session", id, "title", title)
			// Persist rename to store
			if store != nil {
				if err := store.RenameSession(context.Background(), id, title); err != nil {
					m.log().Warn("failed to persist session title", "session", id, "error", err)
				}
			}
		}()
	}

	// Launch goroutine to handle the message
	go func(ctx context.Context, msg string, skills []string) {
		defer close(doneCh)
		defer func() {
			session.mu.Lock()
			session.active = false
			session.cancel = nil
			session.done = nil
			session.mu.Unlock()
		}()

		// Get last completed task ID for continuation
		session.mu.Lock()
		lastTaskID := session.lastCompletedTaskID
		session.mu.Unlock()

		result, err := session.orchestrator.HandleMessage(ctx, msg, id, core.HandleOptions{
			TaskID:        lastTaskID,
			ExecutionMode: mode,
			UserSkills:    skills,
		})

		// Distinguish partial-success (incomplete plan) from total failure: the
		// SDK now wraps partial executions in ErrExecutionIncomplete and returns
		// a non-nil result. Treat that as a successful-with-warning task so the
		// best-effort output is delivered and the user can resume via the
		// resumable-task safety net.
		incomplete := err != nil && errors.Is(err, orchestration.ErrExecutionIncomplete) && result != nil
		if incomplete {
			m.log().Warn("task completed with incomplete execution", "session_id", id, "task_id", lastTaskID, "error", err)
			err = nil // fall through to the success path below; emitResumableIfUnfinished will surface resumability
		}

		// Fallback: if continuation failed (restore error) and we had a TaskID, retry fresh
		if err != nil && lastTaskID != "" {
			m.log().Warn("continuation failed, falling back to fresh workflow", "session_id", id, "task_id", lastTaskID, "error", err)
			session.mu.Lock()
			session.lastCompletedTaskID = "" // clear to avoid repeated failures
			session.mu.Unlock()
			result, err = session.orchestrator.HandleMessage(ctx, msg, id, core.HandleOptions{
				TaskID:        "",
				ExecutionMode: mode,
				UserSkills:    skills,
			})
			if err != nil && errors.Is(err, orchestration.ErrExecutionIncomplete) && result != nil {
				m.log().Warn("task completed with incomplete execution (after fallback)", "session_id", id, "error", err)
				err = nil
			}
		}

		if err != nil {
			// Check if it was a cancellation
			if ctx.Err() == context.Canceled {
				m.emitFunc(Event{
					SessionID: id,
					Type:      "task_cancelled",
					Data: TaskCancelledData{
						SessionID: id,
					},
				})
				// Mark any in-progress task as completed so it's not left resumable.
				m.mu.RLock()
				ts := m.taskStore
				m.mu.RUnlock()
				if ts != nil {
					adapter := NewTaskStoreAdapter(ts)
					if tid, tErr := adapter.GetUnfinishedTaskID(id); tErr == nil && tid != "" {
						if pErr := adapter.PersistCompletion(tid, "", 0); pErr != nil {
							m.log().Warn("failed to persist completion on session done", "task", tid, "error", pErr)
						}
					}
				}
				return
			}

			// Emit error event
			m.emitFunc(Event{
				SessionID: id,
				Type:      "error",
				Data: ErrorData{
					SessionID: id,
					Error:     err.Error(),
				},
			})
			m.emitResumableIfUnfinished(id)
			return
		}

		// Store the task ID for potential continuations
		if pbb, ok := result.Blackboard.(*PersistentBlackboard); ok {
			session.mu.Lock()
			session.lastCompletedTaskID = pbb.TaskID()
			session.mu.Unlock()
		}

		// Safety net: if context was cancelled but orchestrator returned no error,
		// still treat as cancellation — do not emit partial results as final.
		if ctx.Err() == context.Canceled {
			m.emitFunc(Event{
				SessionID: id,
				Type:      "task_cancelled",
				Data: TaskCancelledData{
					SessionID: id,
				},
			})
			m.mu.RLock()
			ts := m.taskStore
			m.mu.RUnlock()
			if ts != nil {
				adapter := NewTaskStoreAdapter(ts)
				if tid, tErr := adapter.GetUnfinishedTaskID(id); tErr == nil && tid != "" {
					if pErr := adapter.PersistCompletion(tid, "", 0); pErr != nil {
						m.log().Warn("failed to persist completion on cancel safety-net", "task", tid, "error", pErr)
					}
				}
			}
			return
		}

		// Emit done event with result
		m.emitFunc(Event{
			SessionID: id,
			Type:      "task_complete",
			Data: TaskCompleteData{
				SessionID:       id,
				Output:          result.Output,
				RoutingDecision: result.RoutingDecision,
				Plan:            result.Plan,
				AttemptCount:    result.AttemptCount,
				Reflections:     result.Reflections,
			},
		})
		m.emitResumableIfUnfinished(id)
	}(taskCtx, text, activeSkills)

	return nil
}

// ResumeTask checks for an unfinished task in the given session and resumes it.
// Returns nil if no unfinished task exists or if the task store is not configured.
// This is called on app restart to resume interrupted tasks.
func (m *Manager) ResumeTask(ctx context.Context, id string) error {
	m.mu.RLock()
	ts := m.taskStore
	m.mu.RUnlock()

	if ts == nil {
		return nil // no task persistence — nothing to resume
	}

	session, restoreErr := m.getOrRestoreSession(id)
	if restoreErr != nil {
		return fmt.Errorf("failed to restore session: %w", restoreErr)
	}
	if session == nil {
		return fmt.Errorf("session not found: %s", id)
	}

	adapter := NewTaskStoreAdapter(ts)
	taskID, err := adapter.GetUnfinishedTaskID(id)
	if err != nil {
		return fmt.Errorf("failed to check unfinished tasks: %w", err)
	}
	if taskID == "" {
		return nil // no unfinished task
	}

	// Load task state and restore blackboard.
	bb, err := RestoreBlackboard(taskID, id, adapter, nil)
	if err != nil {
		return fmt.Errorf("failed to restore blackboard: %w", err)
	}
	if bb == nil {
		return nil // task record not found (race condition or cleanup)
	}

	// Load routing decision from task state.
	state, err := adapter.LoadTaskState(taskID)
	if err != nil {
		return fmt.Errorf("failed to load task state: %w", err)
	}
	if state == nil || state.RoutingDecision == nil {
		return fmt.Errorf("cannot resume task %s: missing routing decision", taskID)
	}

	if bb.GetPlan() == nil {
		return fmt.Errorf("cannot resume task %s: no plan in restored state", taskID)
	}

	session.mu.Lock()
	if session.active {
		session.mu.Unlock()
		return errors.New("session is already processing a task")
	}
	session.active = true
	resumeDoneCh := make(chan struct{})
	session.done = resumeDoneCh
	taskCtx, cancel := context.WithCancel(ContextWithSessionID(ctx, id))
	taskCtx = sdktools.WithWorkspacePath(taskCtx, session.WorkspacePath)
	taskCtx = sdktools.WithTempDir(taskCtx, session.TempDir)
	taskCtx = sdktools.WithCoherence(taskCtx, m.fileTracker)
	session.cancel = cancel
	session.mu.Unlock()

	// Snapshot envInfo under read lock
	m.mu.RLock()
	envInfo := m.envInfo
	m.mu.RUnlock()
	if envInfo != nil {
		taskCtx = sdktools.WithEnvInfo(taskCtx, envInfo)
	}

	// Emit resume event so the frontend knows a task is resuming.
	m.emitFunc(Event{
		SessionID: id,
		Type:      "task_resumed",
		Data: MessageReceivedData{
			SessionID: id,
			Text:      state.OriginalRequest,
		},
	})

	// Launch goroutine (same pattern as SendMessage).
	go func() {
		defer close(resumeDoneCh)
		defer func() {
			session.mu.Lock()
			session.active = false
			session.cancel = nil
			session.done = nil
			session.mu.Unlock()
		}()

		result, err := session.orchestrator.Resume(taskCtx, bb, state.RoutingDecision)

		// Treat partial execution like the SendMessage path — deliver best-effort
		// output and rely on emitResumableIfUnfinished to expose resumability.
		if err != nil && errors.Is(err, orchestration.ErrExecutionIncomplete) && result != nil {
			m.log().Warn("resumed task completed with incomplete execution", "session_id", id, "error", err)
			err = nil
		}

		if err != nil {
			if taskCtx.Err() == context.Canceled {
				m.emitFunc(Event{
					SessionID: id,
					Type:      "task_cancelled",
					Data: TaskCancelledData{
						SessionID: id,
					},
				})
				// Mark the restored task as completed so it's not left resumable.
				bb.CompleteTask(0)
				return
			}

			m.emitFunc(Event{
				SessionID: id,
				Type:      "error",
				Data: ErrorData{
					SessionID: id,
					Error:     err.Error(),
				},
			})
			m.emitResumableIfUnfinished(id)
			return
		}

		// Store the task ID for potential continuations
		if pbb, ok := result.Blackboard.(*PersistentBlackboard); ok {
			session.mu.Lock()
			session.lastCompletedTaskID = pbb.TaskID()
			session.mu.Unlock()
		}

		m.emitFunc(Event{
			SessionID: id,
			Type:      "task_complete",
			Data: TaskCompleteData{
				SessionID:       id,
				Output:          result.Output,
				RoutingDecision: result.RoutingDecision,
				Plan:            result.Plan,
				AttemptCount:    result.AttemptCount,
				Reflections:     result.Reflections,
			},
		})
		m.emitResumableIfUnfinished(id)
	}()

	return nil
}

// CancelUnfinishedTask discards any unfinished task in the given session by
// marking it as completed in the task store. After this returns successfully,
// the session no longer has a resumable task and emitResumableIfUnfinished
// will not emit a "task_failed_resumable" event for it.
// Returns nil if no task store is configured or no unfinished task exists.
func (m *Manager) CancelUnfinishedTask(sessionID string) error {
	m.mu.RLock()
	ts := m.taskStore
	m.mu.RUnlock()
	if ts == nil {
		return nil
	}

	adapter := NewTaskStoreAdapter(ts)
	taskID, err := adapter.GetUnfinishedTaskID(sessionID)
	if err != nil {
		return fmt.Errorf("failed to look up unfinished task: %w", err)
	}
	if taskID == "" {
		return nil
	}
	if err := adapter.PersistCompletion(taskID, "", 0); err != nil {
		return fmt.Errorf("failed to mark task as completed: %w", err)
	}
	return nil
}

// emitResumableIfUnfinished checks whether the session has an unfinished task
// in the task store and, if so, emits a "task_failed_resumable" event so the
// frontend can offer a Resume button.
func (m *Manager) emitResumableIfUnfinished(sessionID string) {
	m.mu.RLock()
	ts := m.taskStore
	m.mu.RUnlock()
	if ts == nil {
		return
	}

	adapter := NewTaskStoreAdapter(ts)
	taskID, err := adapter.GetUnfinishedTaskID(sessionID)
	if err != nil {
		m.log().Warn("failed to get unfinished task ID", "session", sessionID, "error", err)
	}
	if taskID == "" {
		return
	}

	m.emitFunc(Event{
		SessionID: sessionID,
		Type:      "task_failed_resumable",
		Data: TaskFailedResumableData{
			Message: "Plan execution failed. You can resume to retry from where it left off.",
		},
	})
}

// CancelTask cancels the currently running task in a session.
// It signals cancellation and waits (with timeout) for the task goroutine to finish.
func (m *Manager) CancelTask(id string) error {
	session, err := m.getOrRestoreSession(id)
	if err != nil {
		return fmt.Errorf("failed to restore session: %w", err)
	}
	if session == nil {
		return fmt.Errorf("session not found: %s", id)
	}

	session.mu.Lock()
	if !session.active {
		session.mu.Unlock()
		return errors.New("no active task to cancel")
	}

	doneCh := session.done
	if session.cancel != nil {
		session.cancel()
	}
	session.mu.Unlock()

	// Wait for the goroutine to finish so the task_cancelled event is emitted
	// before this method returns to the frontend.
	if doneCh != nil {
		select {
		case <-doneCh:
		case <-time.After(m.stopTimeout):
			m.log().Warn("timed out waiting for task goroutine to stop on cancel", "session_id", id)
		}
	}

	return nil
}

// GetBlackboardState returns the current blackboard state for a session.
// It uses the in-memory lastCompletedTaskID if available, otherwise falls back
// to the most recent task ID from the database.
// Returns nil, nil if no task state is available.
func (m *Manager) GetBlackboardState(sessionID string) (*BlackboardState, error) {
	sess, err := m.getOrRestoreSession(sessionID)
	if err != nil {
		return nil, fmt.Errorf("failed to restore session: %w", err)
	}
	if sess == nil {
		return nil, fmt.Errorf("session not found: %s", sessionID)
	}

	m.mu.RLock()
	ts := m.taskStore
	m.mu.RUnlock()

	if ts == nil {
		return nil, nil // no task persistence — no blackboard state
	}

	// Try in-memory lastCompletedTaskID first.
	sess.mu.Lock()
	taskID := sess.lastCompletedTaskID
	sess.mu.Unlock()

	// Fallback: query the database for the latest task.
	if taskID == "" {
		dbTaskID, dbErr := ts.GetLatestTaskID(context.Background(), sessionID)
		if dbErr != nil {
			return nil, fmt.Errorf("failed to get latest task ID: %w", dbErr)
		}
		if dbTaskID == "" {
			return nil, nil // no tasks for this session
		}
		taskID = dbTaskID
	}

	adapter := NewTaskStoreAdapter(ts)
	state, err := adapter.LoadTaskState(taskID)
	if err != nil {
		return nil, fmt.Errorf("failed to load task state: %w", err)
	}
	if state == nil {
		return nil, nil
	}

	return &BlackboardState{TaskState: state}, nil
}

// BlackboardState wraps a core.TaskState for the GetBlackboardState API.
type BlackboardState struct {
	TaskState *core.TaskState
}
