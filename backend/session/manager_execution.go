package session

import (
	"context"
	"errors"
	"fmt"
	"time"

	"github.com/v0lka/c0wrk/backend/config"
	"github.com/v0lka/c0wrk/backend/project"
	"github.com/v0lka/c0wrk/core"
	coretools "github.com/v0lka/c0wrk/core/tools"
	"github.com/v0lka/sp4rk/agent"
	"github.com/v0lka/sp4rk/orchestration"
	sdktools "github.com/v0lka/sp4rk/tools"
)

// loadWorkDirectories fetches project-scoped and session-scoped auxiliary work
// directories for the given session. It is best-effort: nil stores or listing
// errors are logged and skipped. Project-scoped entries come first, then
// session-scoped entries. Loaded fresh on every call so mid-session additions
// take effect on the next task.
func (m *Manager) loadWorkDirectories(session *Session) []core.WorkDirectory {
	m.mu.RLock()
	projStore := m.projectStore
	sessStore := m.sessionStore
	m.mu.RUnlock()

	var dirs []core.WorkDirectory
	if session.ProjectID != project.NoProjectID && projStore != nil {
		ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
		recs, err := projStore.ListProjectWorkDirs(ctx, session.ProjectID)
		cancel()
		if err != nil {
			m.log().Warn("failed to list project work directories", "project", session.ProjectID, "error", err)
		} else {
			for _, rec := range recs {
				dirs = append(dirs, core.WorkDirectory{Path: rec.Path, Description: rec.Description})
			}
		}
	}
	if sessStore != nil {
		ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
		recs, err := sessStore.ListSessionWorkDirs(ctx, session.ID)
		cancel()
		if err != nil {
			m.log().Warn("failed to list session work directories", "session", session.ID, "error", err)
		} else {
			for _, rec := range recs {
				dirs = append(dirs, core.WorkDirectory{Path: rec.Path, Description: rec.Description})
			}
		}
	}
	return dirs
}

// injectWorkDirectories loads the session's auxiliary work directories and
// injects them into the context as both allowed roots (security containment)
// and the prompt-facing directory list. Returns ctx unchanged when no
// directories are configured.
func (m *Manager) injectWorkDirectories(ctx context.Context, session *Session) context.Context {
	dirs := m.loadWorkDirectories(session)
	if len(dirs) == 0 {
		return ctx
	}
	paths := make([]string, len(dirs))
	for i := range dirs {
		paths[i] = dirs[i].Path
	}
	ctx = sdktools.WithAllowedRoots(ctx, paths)
	return core.WithWorkDirectories(ctx, dirs)
}

// SendMessage sends a user message to a session's orchestrator (async).
// Runs in a goroutine, results come via events.
func (m *Manager) SendMessage(ctx context.Context, id, text string, activeSkills []string, modelOverride, reasoningEffort string) error {
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
	if session.ProjectID == project.NoProjectID {
		taskCtx = coretools.WithNoProject(taskCtx)
	}
	session.cancel = cancel
	session.mu.Unlock()

	// Snapshot envInfo under read lock
	m.mu.RLock()
	envInfo := m.envInfo
	m.mu.RUnlock()
	if envInfo != nil {
		taskCtx = sdktools.WithEnvInfo(taskCtx, envInfo)
	}

	// Inject auxiliary work directories (allowed roots + prompt list).
	taskCtx = m.injectWorkDirectories(taskCtx, session)

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
		dumpFile := session.DumpFile()
		go func() {
			if dumpFile != nil {
				defer func() { _ = dumpFile.Close() }()
			}
			ctx, cancel := context.WithTimeout(context.Background(), 15*time.Second)
			defer cancel()
			if dumpFile != nil {
				ctx = agent.WithDumpWriter(ctx, dumpFile)
			}
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
			TaskID:          lastTaskID,
			UserSkills:      skills,
			ModelOverride:   modelOverride,
			ReasoningEffort: reasoningEffort,
			SessionPlansDir: config.SessionPlansDir(m.agentDir, session.ProjectID, id),
		})

		// Distinguish partial-success (incomplete plan) from total failure.
		incomplete := err != nil && errors.Is(err, orchestration.ErrExecutionIncomplete) && result != nil
		if incomplete {
			m.log().Warn("task completed with incomplete execution", "session_id", id, "task_id", lastTaskID, "error", err)
			err = nil
		}

		// Fallback: if continuation failed (restore error) and we had a TaskID, retry fresh
		if err != nil && lastTaskID != "" {
			m.log().Warn("continuation failed, falling back to fresh workflow", "session_id", id, "task_id", lastTaskID, "error", err)
			session.mu.Lock()
			session.lastCompletedTaskID = ""
			session.mu.Unlock()
			result, err = session.orchestrator.HandleMessage(ctx, msg, id, core.HandleOptions{
				TaskID:          "",
				UserSkills:      skills,
				ModelOverride:   modelOverride,
				ReasoningEffort: reasoningEffort,
				SessionPlansDir: config.SessionPlansDir(m.agentDir, session.ProjectID, id),
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
				// Mark any in-progress task as cancelled so it's not left
				// resumable and the persisted status reflects reality.
				m.persistCancellationIfUnfinished(id)
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
			m.persistCancellationIfUnfinished(id)
			return
		}

		// Emit done event with result (carries the typed success contract;
		// degraded outcomes surface a resumable action or a fallback warning).
		m.emitTaskComplete(id, result, nil)
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
	if session.ProjectID == project.NoProjectID {
		taskCtx = coretools.WithNoProject(taskCtx)
	}
	session.cancel = cancel
	session.mu.Unlock()

	// Snapshot envInfo under read lock
	m.mu.RLock()
	envInfo := m.envInfo
	m.mu.RUnlock()
	if envInfo != nil {
		taskCtx = sdktools.WithEnvInfo(taskCtx, envInfo)
	}

	// Inject auxiliary work directories (allowed roots + prompt list).
	taskCtx = m.injectWorkDirectories(taskCtx, session)

	// Emit resume event so the frontend knows a task is resuming.
	m.emitFunc(Event{
		SessionID: id,
		Type:      "task_resumed",
		Data: MessageReceivedData{
			SessionID: id,
			Text:      state.OriginalRequest,
		},
	})

	// Mark the prior task_failed_resumable banner as resolved so it does not
	// reappear as pending after the resume goroutine finishes. Done here (at
	// the committed point, right before launching) so a failed restore still
	// leaves the banner actionable for a retry.
	m.resolveResumableTaskMessage(id, taskID, "resumed")

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

		result, err := session.orchestrator.Resume(taskCtx, bb, state.RoutingDecision, config.SessionPlansDir(m.agentDir, session.ProjectID, id))

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
				// Mark the restored task as cancelled so it's not left
				// resumable and the persisted status reflects reality.
				bb.CancelTask()
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

		m.emitTaskComplete(id, result, nil)
	}()

	return nil
}

// CancelUnfinishedTask discards any unfinished task in the given session by
// marking it as cancelled in the task store. After this returns successfully,
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
	if err := adapter.PersistCancellation(taskID); err != nil {
		return fmt.Errorf("failed to mark task as cancelled: %w", err)
	}
	// Mark the prior task_failed_resumable banner as resolved so it does not
	// reappear as pending on session reload after the user discards the task.
	m.resolveResumableTaskMessage(sessionID, taskID, "cancelled")
	return nil
}

// SessionRuntimeStatus describes the live and persisted execution state of a
// session, so the frontend can reconstruct "is something running / resumable"
// after app restart or session switch instead of assuming idle.
type SessionRuntimeStatus struct {
	Active            bool   `json:"active"`
	HasUnfinishedTask bool   `json:"has_unfinished_task"`
	UnfinishedTaskID  string `json:"unfinished_task_id,omitempty"`
}

// GetSessionRuntimeStatus returns whether a task is currently running in the
// session (in-memory) and whether an unfinished (resumable) task is persisted
// in the task store. It never restores a session as a side effect.
func (m *Manager) GetSessionRuntimeStatus(sessionID string) (SessionRuntimeStatus, error) {
	var status SessionRuntimeStatus

	// Memory-only lookup: a session that is not in memory cannot be active,
	// and restoring it here would be an unwanted side effect for a status poll.
	m.mu.RLock()
	sess := m.sessions[sessionID]
	m.mu.RUnlock()
	if sess != nil {
		status.Active = sess.IsActive()
	}

	m.mu.RLock()
	ts := m.taskStore
	m.mu.RUnlock()
	if ts != nil {
		adapter := NewTaskStoreAdapter(ts)
		taskID, err := adapter.GetUnfinishedTaskID(sessionID)
		if err != nil {
			return status, fmt.Errorf("failed to look up unfinished task: %w", err)
		}
		if taskID != "" {
			status.HasUnfinishedTask = true
			status.UnfinishedTaskID = taskID
		}
	}

	return status, nil
}

// persistCancellationIfUnfinished marks the session's unfinished task (if any)
// as cancelled in the task store. Best-effort: errors are logged only.
func (m *Manager) persistCancellationIfUnfinished(sessionID string) {
	m.mu.RLock()
	ts := m.taskStore
	m.mu.RUnlock()
	if ts == nil {
		return
	}
	adapter := NewTaskStoreAdapter(ts)
	tid, err := adapter.GetUnfinishedTaskID(sessionID)
	if err != nil {
		m.log().Warn("failed to look up unfinished task on cancel", "session", sessionID, "error", err)
		return
	}
	if tid == "" {
		return
	}
	if err := adapter.PersistCancellation(tid); err != nil {
		m.log().Warn("failed to persist cancellation", "task", tid, "error", err)
	}
}

// emitResumableIfUnfinished checks whether the session has an unfinished task
// in the task store and, if so, emits a "task_failed_resumable" event so the
// frontend can offer a Resume button. It returns true when the event was
// emitted, so callers that KNOW the execution was degraded can surface a
// fallback warning when the resumable safety net is unavailable (nil task
// store, lookup error, or no unfinished record).
func (m *Manager) emitResumableIfUnfinished(sessionID string) bool {
	m.mu.RLock()
	ts := m.taskStore
	m.mu.RUnlock()
	if ts == nil {
		return false
	}

	adapter := NewTaskStoreAdapter(ts)
	taskID, err := adapter.GetUnfinishedTaskID(sessionID)
	if err != nil {
		m.log().Warn("failed to get unfinished task ID", "session", sessionID, "error", err)
		return false
	}
	if taskID == "" {
		return false
	}

	m.emitFunc(Event{
		SessionID: sessionID,
		Type:      "task_failed_resumable",
		Data: TaskFailedResumableData{
			Message: "Plan execution failed. You can resume to retry from where it left off.",
			TaskID:  taskID,
		},
	})
	return true
}

// resolveResumableTaskMessage marks the persisted "task_failed_resumable"
// message for the given task as resolved, so the Resume/Cancel banner does
// not reappear as pending on session reload after the user resumes or cancels.
// Best-effort: errors are logged only — the in-memory UI is already updated
// optimistically by the frontend, and a missed persist is self-healing via
// stale reconciliation on the next reload.
func (m *Manager) resolveResumableTaskMessage(sessionID, taskID, decision string) {
	m.mu.RLock()
	store := m.sessionStore
	m.mu.RUnlock()
	if store == nil || taskID == "" {
		return
	}
	extra := map[string]any{"resolved": true}
	if decision != "" {
		extra["decision"] = decision
	}
	if err := store.ResolvePendingMessage(context.Background(), sessionID, "task_failed_resumable", "task_id", taskID, extra); err != nil {
		m.log().Warn("failed to resolve persisted task_failed_resumable message",
			"session", sessionID, "task", taskID, "decision", decision, "error", err)
	}
}

// taskCompletionInfo derives the frontend-facing success contract from the
// typed execution status on a HandleResult.
func taskCompletionInfo(result *core.HandleResult) (success bool, completion string) {
	if result == nil {
		return true, "full"
	}
	switch result.Status {
	case orchestration.ExecutionStatusPartial, orchestration.ExecutionStatusCancelled:
		return false, "partial"
	case orchestration.ExecutionStatusFailed:
		return false, "failed"
	case orchestration.ExecutionStatusAborted:
		return false, "aborted"
	default:
		return true, "full"
	}
}

// emitTaskComplete emits the "task_complete" event with the typed success
// contract and guarantees the degraded-outcome surfacing: for non-successful
// completions it either emits "task_failed_resumable" or, when the resumable
// safety net cannot deliver (no task store, lookup failure, no unfinished
// record), a visible service warning — never a silent visual success.
func (m *Manager) emitTaskComplete(sessionID string, result *core.HandleResult, plan *orchestration.Plan) {
	success, completion := taskCompletionInfo(result)
	data := TaskCompleteData{
		SessionID:  sessionID,
		Success:    success,
		Completion: completion,
	}
	if result != nil {
		data.Output = result.Output
		data.RoutingDecision = result.RoutingDecision
		data.Plan = result.Plan
		data.Reflections = result.Reflections
	}
	if plan != nil {
		data.Plan = plan
	}
	m.emitFunc(Event{SessionID: sessionID, Type: "task_complete", Data: data})

	resumableEmitted := m.emitResumableIfUnfinished(sessionID)
	if !success && !resumableEmitted {
		m.log().Warn("degraded task completion without resumable safety net", "session", sessionID, "completion", completion)
		m.emitFunc(Event{
			SessionID: sessionID,
			Type:      "service",
			Data: map[string]any{
				"content": fmt.Sprintf("Task finished with %s execution, but it cannot be resumed. Review the output above.", completion),
				"phase":   "orchestration",
			},
		})
	}
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
