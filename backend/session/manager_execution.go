package session

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"time"

	"github.com/v0lka/c0wrk/backend/config"
	"github.com/v0lka/c0wrk/backend/project"
	"github.com/v0lka/c0wrk/core"
	"github.com/v0lka/c0wrk/sdk/agent"
	"github.com/v0lka/c0wrk/sdk/orchestration"
	sdktools "github.com/v0lka/c0wrk/sdk/tools"
)

// SendMessage sends a user message to a session's orchestrator (async).
// Runs in a goroutine, results come via events.
func (m *Manager) SendMessage(ctx context.Context, id, text, mode string, activeSkills []string, modelOverride, reasoningEffort string, planReview bool) error {
	session, err := m.getOrRestoreSession(id)
	if err != nil {
		return fmt.Errorf("failed to restore session: %w", err)
	}
	if session == nil {
		return fmt.Errorf("session not found: %s", id)
	}

	session.mu.Lock()
	// Check if session is awaiting plan review feedback — treat incoming
	// message as replan feedback instead of a new request.
	if session.planReviewPhase == PlanReviewAwaitingFeedback {
		feedbackMsg := text
		// Create the cancellable context while holding the lock to prevent
		// TOCTOU with CancelTask which clears planReviewBB and planReviewCancel.
		replanCtx, replanCancel := context.WithCancel(context.Background())
		session.planReviewCancel = replanCancel
		session.mu.Unlock()
		go func() {
			defer func() {
				session.mu.Lock()
				session.planReviewCancel = nil
				session.mu.Unlock()
			}()
			if err := m.handleReplanWithFeedback(replanCtx, session, id, feedbackMsg); err != nil {
				m.log().Warn("replan with feedback failed", "session", id, "error", err)
				m.emitFunc(Event{SessionID: id, Type: "error", Data: ErrorData{SessionID: id, Error: err.Error()}})
			}
		}()
		return nil
	}

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
		taskCtx = sdktools.WithNoProject(taskCtx)
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
			ExecutionMode:   mode,
			UserSkills:      skills,
			ModelOverride:   modelOverride,
			ReasoningEffort: reasoningEffort,
			PlanReview:      planReview,
			SessionPlansDir: config.SessionPlansDir(m.agentDir, session.ProjectID, id),
		})

		// Plan review: if orchestrator returned a plan for review, store state
		// and emit plan_review_ready, then exit the goroutine (don't block).
		if result != nil && result.PlanReviewPhase != "" {
			planReviewPhase := PlanReviewPhase(result.PlanReviewPhase)
			session.mu.Lock()
			session.planReviewPhase = planReviewPhase
			session.planReviewPath = result.PlanReviewPath
			session.planReviewMsg = msg
			session.planReviewMode = mode
			session.planReviewSkills = skills
			session.planReviewBB = result.Blackboard
			session.planReviewRoute = result.RoutingDecision
			session.mu.Unlock()

			// Read plan content from disk for immediate frontend display.
			// Only emit if the file was read successfully; on failure, log
			// and skip the event — the frontend would render an empty viewer.
			planContent, readErr := os.ReadFile(result.PlanReviewPath)
			if readErr != nil {
				m.log().Warn("failed to read plan content for review", "session", id, "path", result.PlanReviewPath, "error", readErr)
			} else {
				m.emitFunc(Event{
					SessionID: id,
					Type:      "plan_review_ready",
					Data: PlanReviewReadyData{
						SessionID:   id,
						PlanPath:    result.PlanReviewPath,
						PlanContent: string(planContent),
					},
				})
			}

			// Persist plan review state for restart survival.
			m.mu.RLock()
			prs := m.planReviewStore
			m.mu.RUnlock()
			if prs != nil {
				contextJSON, err := json.Marshal(map[string]any{
					"msg":    msg,
					"mode":   mode,
					"skills": skills,
				})
				if err != nil {
					m.log().Warn("failed to marshal plan review context", "session", id, "error", err)
					contextJSON = []byte("{}")
				}
				if err := prs.UpdateSessionPlanReviewContext(context.Background(), id, string(planReviewPhase), result.PlanReviewPath, string(contextJSON)); err != nil {
					m.log().Warn("failed to persist plan review state", "session", id, "error", err)
				}
			}

			return
		}

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
				TaskID:          "",
				ExecutionMode:   mode,
				UserSkills:      skills,
				ModelOverride:   modelOverride,
				ReasoningEffort: reasoningEffort,
				PlanReview:      planReview,
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
	if session.ProjectID == project.NoProjectID {
		taskCtx = sdktools.WithNoProject(taskCtx)
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

// ApprovePlan validates and executes a plan that was awaiting user review.
func (m *Manager) ApprovePlan(ctx context.Context, sessionID, planPath string) error {
	session, err := m.getOrRestoreSession(sessionID)
	if err != nil {
		return fmt.Errorf("failed to restore session: %w", err)
	}
	if session == nil {
		return fmt.Errorf("session not found: %s", sessionID)
	}

	session.mu.Lock()
	if session.planReviewPhase != PlanReviewAwaitingAccept {
		session.mu.Unlock()
		return errors.New("session is not awaiting plan review")
	}
	if session.active {
		session.mu.Unlock()
		return errors.New("session is already processing a task")
	}
	session.mu.Unlock()

	// Read the plan markdown from disk (I/O — outside lock).
	planMD, err := os.ReadFile(planPath)
	if err != nil {
		return fmt.Errorf("failed to read plan file: %w", err)
	}

	// Parse and validate structure
	parsed, parseErrors := core.ParsePlanMarkdown(string(planMD))
	if len(parseErrors) > 0 {
		issues := make([]ValidationIssue, len(parseErrors))
		for i, pe := range parseErrors {
			issues[i] = ValidationIssue{
				StepIndex:   pe.StepNum,
				Field:       pe.Field,
				Severity:    "error",
				Description: pe.Detail,
			}
		}
		m.emitFunc(Event{
			SessionID: sessionID,
			Type:      "plan_validation_failed",
			Data: PlanValidationFailedData{
				SessionID: sessionID,
				Issues:    issues,
			},
		})
		return fmt.Errorf("plan structural validation failed: %d issues", len(parseErrors))
	}

	// Get original plan from blackboard
	session.mu.Lock()
	bb := session.planReviewBB
	session.mu.Unlock()
	if bb == nil {
		return errors.New("no blackboard found for plan review")
	}

	originalPlan := bb.GetPlan()
	if originalPlan == nil {
		return errors.New("no original plan found on blackboard")
	}

	// Merge user-edited content with original hidden fields
	mergedSteps := core.MergePlanSteps(parsed, originalPlan)
	mergedPlan := &orchestration.Plan{Steps: mergedSteps}

	// Run semantic validation (LLM-based) with enriched context.
	if session.orchestrator != nil {
		session.mu.Lock()
		originalMsg := session.planReviewMsg
		session.mu.Unlock()

		// Enrich context with session values for model/NoProject awareness.
		valCtx := ctx
		valCtx = sdktools.WithWorkspacePath(valCtx, session.WorkspacePath)
		valCtx = sdktools.WithTempDir(valCtx, session.TempDir)
		if session.ProjectID == project.NoProjectID {
			valCtx = sdktools.WithNoProject(valCtx)
		}

		semanticIssues, valErr := session.orchestrator.SemanticValidatePlan(valCtx, originalMsg, string(planMD))
		if valErr != nil {
			m.log().Warn("semantic validation failed", "session", sessionID, "error", valErr)
		} else if len(semanticIssues) > 0 {
			issues := make([]ValidationIssue, len(semanticIssues))
			for i, desc := range semanticIssues {
				issues[i] = ValidationIssue{
					Severity:    "warning",
					Description: desc,
				}
			}
			m.emitFunc(Event{
				SessionID: sessionID,
				Type:      "plan_validation_failed",
				Data: PlanValidationFailedData{
					SessionID: sessionID,
					Issues:    issues,
				},
			})
			return fmt.Errorf("plan semantic validation failed: %d issues", len(issues))
		}
	}

	// All validation passed. Now set active=true and launch execution.
	session.mu.Lock()
	if session.active {
		session.mu.Unlock()
		return errors.New("session is already processing a task")
	}
	bb.SetPlan(mergedPlan)
	route := session.planReviewRoute
	session.planReviewPhase = PlanReviewNone
	session.planReviewPath = ""
	session.planReviewRoute = nil
	session.planReviewMsg = ""
	session.planReviewMode = ""
	session.planReviewSkills = nil
	session.active = true
	resumeDoneCh := make(chan struct{})
	session.done = resumeDoneCh
	taskCtx, cancel := context.WithCancel(ContextWithSessionID(ctx, sessionID))
	taskCtx = sdktools.WithWorkspacePath(taskCtx, session.WorkspacePath)
	taskCtx = sdktools.WithTempDir(taskCtx, session.TempDir)
	taskCtx = sdktools.WithCoherence(taskCtx, m.fileTracker)
	if session.ProjectID == project.NoProjectID {
		taskCtx = sdktools.WithNoProject(taskCtx)
	}
	session.cancel = cancel
	session.mu.Unlock()

	// Emit plan accepted event
	m.emitFunc(Event{
		SessionID: sessionID,
		Type:      "plan_review_accepted",
		Data:      map[string]any{"session_id": sessionID},
	})

	// Clear persisted plan review state.
	m.mu.RLock()
	prs := m.planReviewStore
	m.mu.RUnlock()
	if prs != nil {
		if err := prs.UpdateSessionPlanReview(context.Background(), sessionID, "", ""); err != nil {
			m.log().Warn("failed to clear plan review state", "session", sessionID, "error", err)
		}
	}

	// Clean up superseded plan files (.md and .plan.json) now that the
	// plan is accepted and execution is starting. Previous plan files from
	// replanning iterations remain as history; only the accepted pair is
	// removed to avoid accumulating stale files.
	m.cleanupAcceptedPlanFiles(planPath)

	// Launch execution goroutine (same pattern as ResumeTask)
	go func() {
		defer close(resumeDoneCh)
		defer func() {
			session.mu.Lock()
			session.active = false
			session.cancel = nil
			session.done = nil
			session.mu.Unlock()
		}()

		result, execErr := session.orchestrator.Resume(taskCtx, bb, route)

		if execErr != nil && errors.Is(execErr, orchestration.ErrExecutionIncomplete) && result != nil {
			m.log().Warn("approved plan execution completed with incomplete execution", "session_id", sessionID, "error", execErr)
			execErr = nil
		}

		if execErr != nil {
			if taskCtx.Err() == context.Canceled {
				m.emitFunc(Event{SessionID: sessionID, Type: "task_cancelled", Data: TaskCancelledData{SessionID: sessionID}})
				return
			}
			m.emitFunc(Event{SessionID: sessionID, Type: "error", Data: ErrorData{SessionID: sessionID, Error: execErr.Error()}})
			m.emitResumableIfUnfinished(sessionID)
			return
		}

		// Store task ID for potential continuations
		if result != nil {
			if pbb, ok := result.Blackboard.(*PersistentBlackboard); ok {
				session.mu.Lock()
				session.lastCompletedTaskID = pbb.TaskID()
				session.mu.Unlock()
			}
		}

		output := ""
		if result != nil {
			output = result.Output
		}
		m.emitFunc(Event{SessionID: sessionID, Type: "task_complete", Data: TaskCompleteData{SessionID: sessionID, Output: output, Plan: mergedPlan}})
		m.emitResumableIfUnfinished(sessionID)
	}()

	return nil
}

// RejectPlan rejects a plan that was awaiting user review, optionally with
// feedback for replanning.
func (m *Manager) RejectPlan(ctx context.Context, sessionID, feedback string) error {
	session, err := m.getOrRestoreSession(sessionID)
	if err != nil {
		return fmt.Errorf("failed to restore session: %w", err)
	}
	if session == nil {
		return fmt.Errorf("session not found: %s", sessionID)
	}

	session.mu.Lock()
	if session.planReviewPhase != PlanReviewAwaitingAccept {
		session.mu.Unlock()
		return errors.New("session is not awaiting plan review")
	}
	session.mu.Unlock()

	if feedback == "" {
		// No feedback: wait for user to provide feedback as a message
		session.mu.Lock()
		prevPlanPath := session.planReviewPath
		session.planReviewPhase = PlanReviewAwaitingFeedback
		session.mu.Unlock()

		// Persist the awaiting_feedback state with context for restart survival.
		// Keep the previous plan path so handleReplanWithFeedback can read the
		// original plan after a restart (awaiting_feedback has no path in the
		// normal sense, but needs the previous plan for replanning).
		m.mu.RLock()
		prs := m.planReviewStore
		m.mu.RUnlock()
		if prs != nil {
			session.mu.Lock()
			contextJSON, err := json.Marshal(map[string]any{
				"msg":    session.planReviewMsg,
				"mode":   session.planReviewMode,
				"skills": session.planReviewSkills,
			})
			session.mu.Unlock()
			if err != nil {
				m.log().Warn("failed to marshal plan review context", "session", sessionID, "error", err)
				contextJSON = []byte("{}")
			}
			if err := prs.UpdateSessionPlanReviewContext(context.Background(), sessionID, string(PlanReviewAwaitingFeedback), prevPlanPath, string(contextJSON)); err != nil {
				m.log().Warn("failed to persist plan review state", "session", sessionID, "error", err)
			}
		}

		// Emit rejection resolution so it survives app restart (like plan_review_accepted).
		m.emitFunc(Event{
			SessionID: sessionID,
			Type:      "plan_review_rejected",
			Data:      map[string]any{"session_id": sessionID},
		})
		m.emitFunc(Event{
			SessionID: sessionID,
			Type:      "plan_review_awaiting_feedback",
			Data:      map[string]string{"session_id": sessionID},
		})
		return nil
	}

	// Emit rejection resolution so it survives app restart (like plan_review_accepted).
	m.emitFunc(Event{
		SessionID: sessionID,
		Type:      "plan_review_rejected",
		Data:      map[string]any{"session_id": sessionID},
	})

	// Feedback provided: replan with feedback in a goroutine so the Wails RPC
	// does not block on the LLM call.
	// Create a cancellable context so CancelTask can interrupt the LLM call.
	replanCtx, replanCancel := context.WithCancel(context.Background())
	session.mu.Lock()
	session.planReviewCancel = replanCancel
	session.mu.Unlock()
	go func() {
		defer func() {
			session.mu.Lock()
			session.planReviewCancel = nil
			session.mu.Unlock()
		}()
		if err := m.handleReplanWithFeedback(replanCtx, session, sessionID, feedback); err != nil {
			m.log().Warn("replan with feedback failed", "session", sessionID, "error", err)
			m.emitFunc(Event{SessionID: sessionID, Type: "error", Data: ErrorData{SessionID: sessionID, Error: err.Error()}})
		}
	}()
	return nil
}

// handleReplanWithFeedback implements the replan-with-feedback flow shared by
// RejectPlan (with feedback) and SendMessage (awaiting_feedback message).

func (m *Manager) handleReplanWithFeedback(ctx context.Context, session *Session, sessionID, feedback string) error {
	session.mu.Lock()
	originalMsg := session.planReviewMsg
	prevPlanPath := session.planReviewPath
	planReviewSkills := session.planReviewSkills
	session.mu.Unlock()

	// Read previous plan markdown
	prevPlanMD, err := os.ReadFile(prevPlanPath)
	if err != nil {
		return fmt.Errorf("failed to read previous plan: %w", err)
	}

	if session.orchestrator == nil {
		return errors.New("no orchestrator available for replanning")
	}

	// Determine single-step mode
	singleStep := false
	session.mu.Lock()
	if session.planReviewMode == "normal" {
		singleStep = true
	}
	session.mu.Unlock()

	// Build enriched context mirroring the SendMessage path (EnvInfo, TempDir,
	// Coherence, NoProject). Derived from the caller context so CancelTask
	// can interrupt an in-flight LLM call.
	ctx2 := ctx
	if ctx2 == nil {
		ctx2 = context.Background()
	}
	ctx2 = sdktools.WithWorkspacePath(ctx2, session.WorkspacePath)
	ctx2 = sdktools.WithTempDir(ctx2, session.TempDir)
	ctx2 = sdktools.WithCoherence(ctx2, m.fileTracker)
	if session.ProjectID == project.NoProjectID {
		ctx2 = sdktools.WithNoProject(ctx2)
	}
	m.mu.RLock()
	envInfo := m.envInfo
	m.mu.RUnlock()
	if envInfo != nil {
		ctx2 = sdktools.WithEnvInfo(ctx2, envInfo)
	}

	newPlan, err := session.orchestrator.PlanWithFeedback(
		ctx2,
		originalMsg,
		string(prevPlanMD),
		feedback,
		nil, // tools — orchestrator handles this internally
		session.orchestrator.LookupSkillDescriptors(planReviewSkills),
		singleStep,
	)
	if err != nil {
		return fmt.Errorf("replanning failed: %w", err)
	}

	// Serialize new plan
	newMD := core.SerializePlan(newPlan)

	// Save to new .md file
	plansDir := config.SessionPlansDir(m.agentDir, session.ProjectID, sessionID)
	if mkErr := os.MkdirAll(plansDir, 0o755); mkErr != nil {
		return fmt.Errorf("failed to create plans directory: %w", mkErr)
	}

	sessionPrefix := "session"
	if len(sessionID) > 8 {
		sessionPrefix = sessionID[:8]
	} else if sessionID != "" {
		sessionPrefix = sessionID
	}

	newPlanPath := filepath.Join(plansDir, fmt.Sprintf("%s_%s.md", sessionPrefix, core.RandomSuffix()))
	if writeErr := os.WriteFile(newPlanPath, []byte(newMD), 0o644); writeErr != nil {
		return fmt.Errorf("failed to write new plan: %w", writeErr)
	}

	// Write .plan.json sidecar so hidden fields (DependsOn, Profile, etc.)
	// and routing decision survive app restart. Mirrors HandlePlanReview in
	// core/plan_review.go.
	jsonPath := strings.TrimSuffix(newPlanPath, ".md") + ".plan.json"
	session.mu.Lock()
	route := session.planReviewRoute
	session.mu.Unlock()
	planJSON, jErr := json.Marshal(core.PlanReviewSidecar{
		Plan:  newPlan,
		Route: route,
	})
	if jErr != nil {
		m.log().Warn("failed to marshal plan JSON for sidecar", "session", sessionID, "error", jErr)
	} else if wErr := os.WriteFile(jsonPath, planJSON, 0o644); wErr != nil {
		m.log().Warn("failed to write plan JSON sidecar", "session", sessionID, "path", jsonPath, "error", wErr)
	}

	// Emit new plan_review_ready (PlanWithFeedback already emitted PlanGenerated
	// events internally; session manager only needs to emit the review-ready event).
	session.mu.Lock()
	bb := session.planReviewBB
	if bb != nil {
		bb.SetPlan(newPlan)
		session.planReviewBB = bb
	}
	session.planReviewPhase = PlanReviewAwaitingAccept
	session.planReviewPath = newPlanPath
	session.mu.Unlock()

	// Persist updated plan review state.
	m.mu.RLock()
	prs := m.planReviewStore
	m.mu.RUnlock()
	if prs != nil {
		session.mu.Lock()
		contextJSON, err := json.Marshal(map[string]any{
			"msg":    originalMsg,
			"mode":   session.planReviewMode,
			"skills": session.planReviewSkills,
		})
		session.mu.Unlock()
		if err != nil {
			m.log().Warn("failed to marshal plan review context", "session", sessionID, "error", err)
			contextJSON = []byte("{}")
		}
		if err := prs.UpdateSessionPlanReviewContext(context.Background(), sessionID, string(PlanReviewAwaitingAccept), newPlanPath, string(contextJSON)); err != nil {
			m.log().Warn("failed to persist plan review state after replan", "session", sessionID, "error", err)
		}
	}

	// Emit new plan_review_ready
	m.emitFunc(Event{
		SessionID: sessionID,
		Type:      "plan_review_ready",
		Data: PlanReviewReadyData{
			SessionID:   sessionID,
			PlanPath:    newPlanPath,
			PlanContent: newMD,
		},
	})

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

	// Handle plan review cancellation: clear review state and persist.
	if session.planReviewPhase != PlanReviewNone {
		bb := session.planReviewBB // capture before clearing
		planReviewCancel := session.planReviewCancel
		planPath := session.planReviewPath // capture for cleanup
		session.planReviewPhase = PlanReviewNone
		session.planReviewPath = ""
		session.planReviewMsg = ""
		session.planReviewMode = ""
		session.planReviewSkills = nil
		session.planReviewBB = nil
		session.planReviewRoute = nil
		session.planReviewCancel = nil
		session.mu.Unlock()

		// Cancel any in-flight replan LLM call.
		if planReviewCancel != nil {
			planReviewCancel()
		}

		// Persist cleared state.
		m.mu.RLock()
		prs := m.planReviewStore
		m.mu.RUnlock()
		if prs != nil {
			if err := prs.UpdateSessionPlanReview(context.Background(), id, "", ""); err != nil {
				m.log().Warn("failed to clear plan review state on cancel", "session", id, "error", err)
			}
		}

		// Cancel the blackboard task so it's not left resumable.
		if bb != nil {
			if pbb, ok := bb.(*PersistentBlackboard); ok {
				pbb.CancelTask()
			}
		}

		// Clean up plan files left behind by the cancelled review.
		if planPath != "" {
			m.cleanupAcceptedPlanFiles(planPath)
		}

		m.emitFunc(Event{SessionID: id, Type: "task_cancelled", Data: TaskCancelledData{SessionID: id}})
		return nil
	}

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

// cleanupAcceptedPlanFiles removes the .md and .plan.json files for an
// accepted plan. Only the accepted file pair is removed; previous plan
// files from replanning iterations remain as history.
func (m *Manager) cleanupAcceptedPlanFiles(planPath string) {
	// Remove the .md file (best-effort; non-critical).
	if err := os.Remove(planPath); err != nil && !os.IsNotExist(err) {
		m.log().Warn("failed to remove accepted plan .md file", "path", planPath, "error", err)
	}
	// Remove the .plan.json sidecar.
	jsonPath := strings.TrimSuffix(planPath, ".md") + ".plan.json"
	if err := os.Remove(jsonPath); err != nil && !os.IsNotExist(err) {
		m.log().Warn("failed to remove accepted plan .plan.json file", "path", jsonPath, "error", err)
	}
}

// BlackboardState wraps a core.TaskState for the GetBlackboardState API.
type BlackboardState struct {
	TaskState *core.TaskState
}