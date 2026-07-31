package backend

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"time"

	"github.com/v0lka/c0wrk/backend/config"
	"github.com/v0lka/c0wrk/backend/session"
	"github.com/v0lka/c0wrk/core"
)

// CreateSession creates a new agent session within the active project.
func (f *FrontendAPI) CreateSession() (*session.SessionInfo, error) {
	if f.app == nil || f.app.Manager() == nil {
		return nil, errors.New("session manager not initialized - check startup logs for LLM router or configuration errors")
	}

	f.activeProjectMu.RLock()
	projectID := f.activeProjectID
	projectPath := f.activeProjectPath
	f.activeProjectMu.RUnlock()

	if projectID == "" {
		return nil, errors.New("no active project — create or select a project first")
	}

	info, err := f.app.Manager().CreateSession(projectID, projectPath)
	if err != nil {
		return nil, fmt.Errorf("failed to create session: %w", err)
	}
	// Persist to SQLite
	// Best-effort persistence: log and continue to avoid disrupting the user session.
	if f.store != nil {
		if err := f.store.SaveSession(context.Background(), *info); err != nil {
			f.log().Error("failed to save session to store", "error", err)
		}
	}
	return info, nil
}

// ForkSession creates an independent deep copy of a session (messages, tasks,
// blackboard facts/plan/trajectory, terminal history, work directories, and
// code review) with freshly generated identifiers so the fork shares no rows
// with the original. Forking is only allowed when the session has no unfinished
// (in-progress or failed) task. On success the new session is returned; the
// caller switches the active session to it.
func (f *FrontendAPI) ForkSession(sessionID string) (*session.SessionInfo, error) {
	if f.app == nil || f.app.Manager() == nil {
		return nil, errors.New("session manager not initialized")
	}
	if f.store == nil {
		return nil, errors.New("session store not initialized")
	}

	ctx := context.Background()

	// Guard: a session with an unfinished task cannot be forked — forking would
	// duplicate a half-completed execution state.
	unfinished, err := f.store.GetUnfinishedTask(ctx, sessionID)
	if err != nil {
		return nil, fmt.Errorf("failed to check session tasks: %w", err)
	}
	if unfinished != nil {
		return nil, errors.New("cannot fork a session that has an unfinished task")
	}

	// Clone the review inside the same transaction as the fork so the whole
	// operation (session + tasks + review) commits atomically; a review-copy
	// failure rolls back the entire fork instead of leaving a review-less fork.
	var reviewCloner session.ForkReviewCloner
	if f.reviewStore != nil {
		reviewCloner = f.reviewStore.CloneReviewTx
	}

	info, err := f.store.ForkSession(ctx, sessionID, reviewCloner)
	if err != nil {
		return nil, fmt.Errorf("failed to fork session: %w", err)
	}

	return info, nil
}

// DeleteSession removes a session. All internal files that belong to the
// session (logs, dumps, temp, plans, and the No-Project workspace) are removed
// from ~/.c0wrk. Archiving a session (ArchiveSession) does NOT remove files so
// an archived session can be restored.
func (f *FrontendAPI) DeleteSession(id string) error {
	if f.app == nil || f.app.Manager() == nil {
		return errors.New("session manager not initialized")
	}
	manager := f.app.Manager()

	// The manager lazily restores and deletes in-memory/restorable sessions,
	// closing file handles and removing the entire per-session directory. When
	// the session cannot be restored (e.g. its project can no longer be
	// resolved), capture the project ID from the store so its on-disk files can
	// still be cleaned up below.
	var unrestorableProjectID string
	// Only delete from manager if session exists in memory
	if _, exists := manager.GetSession(id); exists {
		if err := manager.DeleteSession(id); err != nil {
			return fmt.Errorf("failed to delete session: %w", err)
		}
	} else if f.store != nil {
		if info, err := f.store.LoadSession(context.Background(), id); err == nil && info != nil {
			unrestorableProjectID = info.ProjectID
		}
	}
	// Always delete from store (handles store-only sessions from previous runs)
	// Best-effort persistence: log and continue to avoid disrupting the user session.
	if f.store != nil {
		if err := f.store.DeleteSession(context.Background(), id); err != nil {
			f.log().Error("failed to delete session from store", "error", err)
		}
	}
	// Stop any active terminal for this session.
	if f.terminalManager != nil && f.terminalManager.IsActive(id) {
		if err := f.terminalManager.Stop(id); err != nil {
			f.log().Warn("failed to stop terminal for deleted session", "session_id", id, "error", err)
		}
	}
	// Fallback: remove internal files for sessions the manager could not
	// restore. Restored/in-memory sessions are already cleaned up above.
	if unrestorableProjectID != "" {
		sessionDir := config.SessionDir(f.agentDir, unrestorableProjectID, id)
		if err := os.RemoveAll(sessionDir); err != nil {
			f.log().Warn("failed to remove session directory", "session_id", id, "dir", sessionDir, "error", err)
		}
	}
	return nil
}

// ListSessions returns sessions for the active project.
func (f *FrontendAPI) ListSessions() ([]session.SessionInfo, error) {
	f.activeProjectMu.RLock()
	projectID := f.activeProjectID
	f.activeProjectMu.RUnlock()

	if projectID == "" {
		return []session.SessionInfo{}, nil
	}

	if f.app == nil || f.app.Manager() == nil {
		return []session.SessionInfo{}, nil
	}

	return f.app.Manager().ListSessionsByProject(projectID)
}

// RenameSession changes session name.
func (f *FrontendAPI) RenameSession(id, name string) error {
	if f.app == nil || f.app.Manager() == nil {
		return errors.New("session manager not initialized")
	}
	manager := f.app.Manager()
	// Only rename in manager if session exists in memory
	if _, exists := manager.GetSession(id); exists {
		if err := manager.RenameSession(id, name); err != nil {
			return fmt.Errorf("failed to rename session: %w", err)
		}
	}
	// Always rename in store (handles store-only sessions from previous runs)
	// Best-effort persistence: log and continue to avoid disrupting the user session.
	if f.store != nil {
		if err := f.store.RenameSession(context.Background(), id, name); err != nil {
			f.log().Error("failed to rename session in store", "error", err)
		}
	}
	return nil
}

// ArchiveSession archives/unarchives a session.
func (f *FrontendAPI) ArchiveSession(id string) error {
	if f.app == nil || f.app.Manager() == nil {
		return errors.New("session manager not initialized")
	}
	manager := f.app.Manager()
	// Only archive in manager if session exists in memory
	if _, exists := manager.GetSession(id); exists {
		if err := manager.ArchiveSession(id); err != nil {
			return fmt.Errorf("failed to archive session: %w", err)
		}
	}
	// Toggle archive in store
	// Best-effort persistence: log and continue to avoid disrupting the user session.
	if f.store != nil {
		info, err := f.store.LoadSession(context.Background(), id)
		if err == nil && info != nil {
			if err := f.store.ArchiveSession(context.Background(), id, !info.Archived); err != nil {
				f.log().Error("failed to archive session in store", "error", err)
			}
		}
	}
	return nil
}

// PinSession pins/unpins a session.
func (f *FrontendAPI) PinSession(id string) error {
	if f.app == nil || f.app.Manager() == nil {
		return errors.New("session manager not initialized")
	}
	manager := f.app.Manager()
	// Only pin in manager if session exists in memory
	if _, exists := manager.GetSession(id); exists {
		if err := manager.PinSession(id); err != nil {
			return fmt.Errorf("failed to pin session: %w", err)
		}
	}
	// Toggle pin in store
	// Best-effort persistence: log and continue to avoid disrupting the user session.
	if f.store != nil {
		info, err := f.store.LoadSession(context.Background(), id)
		if err == nil && info != nil {
			if err := f.store.PinSession(context.Background(), id, !info.Pinned); err != nil {
				f.log().Error("failed to pin session in store", "error", err)
			}
		}
	}
	return nil
}

// SendMessage sends a user message to a session (async - results come via events).
// activeSkills contains skill names explicitly referenced by the user via /skill-name syntax.
// goal, when true, enables goal mode for the first message of a task (OR-ed with
// any /goal command prefix the message text may carry). goalBudget is an optional
// JSON budget override ({"max_turns":N}) tightening the goal's turn cap;
// empty = use defaults (unlimited).
// reviewMode, when true, marks the message as carrying code review feedback the
// agent must address (review status == "submitted"); the system prompt gains a
// Code Review section directing the agent to edit code.
func (f *FrontendAPI) SendMessage(id, text string, activeSkills, activeAgents []string, modelOverride, reasoningEffort string, goal bool, goalBudget string, reviewMode bool) error {
	if f.app == nil || f.app.Manager() == nil {
		return errors.New("session manager not initialized - check startup logs for LLM router or configuration errors")
	}
	// Update session activity timestamp
	// Best-effort persistence: log and continue to avoid disrupting the user session.
	if f.store != nil {
		if err := f.store.UpdateSessionActivity(context.Background(), id); err != nil {
			f.log().Error("failed to update session activity", "error", err)
		}
	}
	// Save user message to store (original text with /skill and @file markers for display on reload).
	// Best-effort persistence: log and continue to avoid disrupting the user session.
	if f.store != nil {
		// Persist image-attachment metadata (thumbnail + on-disk path, never the
		// full base64) so image attachments survive a backend restart and can be
		// reconstructed into ContentBlocks from the saved files. Read before
		// SendMessage snapshots and clears the pending image list.
		var imageMetadata json.RawMessage
		if mgr := f.app.Manager(); mgr != nil {
			if md, mdErr := mgr.PendingImageMetadata(id); mdErr == nil {
				imageMetadata = md
			} else {
				f.log().Warn("failed to read pending image metadata", "session_id", id, "error", mdErr)
			}
		}
		if err := f.store.SaveMessage(context.Background(), session.ChatMessage{
			SessionID: id,
			Role:      "user",
			Content:   text,
			Metadata:  imageMetadata,
			CreatedAt: time.Now().Format(time.RFC3339),
		}); err != nil {
			f.log().Error("failed to save user message to store", "error", err)
		}
	}

	// Preprocess text for the orchestrator: strip /skill refs and convert @file refs to fileref:// URIs.
	// Relative @file paths are resolved against the session's own workspace path (authoritative for
	// both project and No Project sessions) so the LLM receives unambiguous absolute paths. This
	// mirrors the preprocessing applied during history restore and avoids the project-level
	// activeProjectPath, which is empty for No Project sessions and may not match the session's project.
	workspacePath := ""
	if wp, ok := f.app.Manager().GetSessionWorkspacePath(id); ok {
		workspacePath = wp
	}
	processedText := core.PreprocessMessageText(text, activeSkills, activeAgents, workspacePath)

	if err := f.app.Manager().SendMessage(f.ctx(), id, processedText, activeSkills, activeAgents, modelOverride, reasoningEffort, goal, goalBudget, reviewMode); err != nil {
		return fmt.Errorf("failed to send message: %w", err)
	}
	return nil
}

// CancelTask cancels the running task in a session.
func (f *FrontendAPI) CancelTask(id string) error {
	if f.app == nil || f.app.Manager() == nil {
		return errors.New("session manager not initialized")
	}
	return f.app.Manager().CancelTask(id)
}

// ResumeTask resumes an interrupted task in the given session, if any.
// Returns nil if no unfinished task exists. This is safe to call on session load.
func (f *FrontendAPI) ResumeTask(id string) error {
	if f.app == nil || f.app.Manager() == nil {
		return errors.New("session manager not initialized")
	}
	return f.app.Manager().ResumeTask(f.ctx(), id)
}

// GetSessionTokens returns persisted token counts for a session.
func (f *FrontendAPI) GetSessionTokens(sessionID string) SessionTokensResponse {
	var result SessionTokensResponse
	if f.store == nil || sessionID == "" {
		return result
	}
	info, err := f.store.LoadSession(f.ctx(), sessionID)
	if err != nil || info == nil {
		return result
	}
	result.TotalInputTokens = info.TotalInputTokens
	result.TotalOutputTokens = info.TotalOutputTokens
	result.Model = info.Model
	result.Family = info.Family
	result.FillPercent = info.FillPercent
	return result
}

// CancelUnfinishedTask discards an unfinished task in the given session,
// preventing future resume prompts. Safe to call when no unfinished task
// exists; in that case it is a no-op.
func (f *FrontendAPI) CancelUnfinishedTask(id string) error {
	if f.app == nil || f.app.Manager() == nil {
		return errors.New("session manager not initialized")
	}
	return f.app.Manager().CancelUnfinishedTask(id)
}

// GetSessionRuntimeStatus returns whether a task is currently running in the
// session and whether the session has an unfinished (resumable) task persisted
// in the task store. The frontend calls this after loading history so the UI
// reflects real execution state instead of defaulting to "idle/completed".
func (f *FrontendAPI) GetSessionRuntimeStatus(id string) (session.SessionRuntimeStatus, error) {
	if f.app == nil || f.app.Manager() == nil {
		return session.SessionRuntimeStatus{}, errors.New("session manager not initialized")
	}
	return f.app.Manager().GetSessionRuntimeStatus(id)
}

// GetSessionHistory returns chat history for a session.
func (f *FrontendAPI) GetSessionHistory(id string) ([]session.ChatMessage, error) {
	if f.store != nil {
		return f.store.LoadMessages(context.Background(), id)
	}
	return []session.ChatMessage{}, nil
}

// GetBlackboardState returns the current blackboard state for a session.
// Returns nil if no task state is available.
func (f *FrontendAPI) GetBlackboardState(sessionID string) (*BlackboardStateResponse, error) {
	if f.app == nil || f.app.Manager() == nil {
		return nil, errors.New("session manager not initialized")
	}

	bbState, err := f.app.Manager().GetBlackboardState(sessionID)
	if err != nil {
		return nil, fmt.Errorf("failed to get blackboard state: %w", err)
	}
	if bbState == nil || bbState.TaskState == nil {
		return nil, nil
	}

	return convertBlackboardState(bbState.TaskState), nil
}

// convertBlackboardState maps a core.TaskState to the frontend DTO.
func convertBlackboardState(state *core.TaskState) *BlackboardStateResponse {
	resp := &BlackboardStateResponse{
		TaskID:          state.TaskID,
		SessionID:       state.SessionID,
		Status:          state.Status,
		OriginalRequest: state.OriginalRequest,
		FinalOutput:     state.FinalOutput,
		StepResults:     make(map[string]BlackboardStepResponse, len(state.StepResults)),
		Reflections:     make([]BlackboardReflectionResponse, 0, len(state.Reflections)),
		Facts:           make([]BlackboardFactResponse, 0, len(state.Facts)),
		Attachments:     make([]BlackboardAttachmentResponse, 0, len(state.Attachments)),
	}

	// Plan
	if state.Plan != nil && len(state.Plan.Steps) > 0 {
		planResp := &BlackboardPlanResponse{
			Steps: make([]BlackboardPlanStepResponse, len(state.Plan.Steps)),
		}
		for i, s := range state.Plan.Steps {
			planResp.Steps[i] = BlackboardPlanStepResponse{
				ID:          s.ID,
				Summary:     s.Summary,
				Description: s.Description,
				DependsOn:   s.DependsOn,
			}
		}
		resp.Plan = planResp
	}

	// Step results (summaries only, no full output)
	for stepID, sr := range state.StepResults {
		entry := BlackboardStepResponse{
			StepID:  stepID,
			Summary: sr.Summary,
		}
		if sr.Error != nil {
			entry.Error = sr.Error.Error()
		}
		resp.StepResults[stepID] = entry
	}

	// Reflections
	for _, r := range state.Reflections {
		resp.Reflections = append(resp.Reflections, BlackboardReflectionResponse{
			Summary:         r.Summary,
			Hypotheses:      r.Hypotheses,
			SuggestedAction: r.SuggestedAction,
			Reasoning:       r.Reasoning,
			FailureAnalysis: r.FailureAnalysis,
			RootCause:       r.RootCause,
			ActionPlan:      r.ActionPlan,
			Timestamp:       r.Timestamp.Format(time.RFC3339),
		})
	}

	// Facts
	for _, fact := range state.Facts {
		resp.Facts = append(resp.Facts, BlackboardFactResponse{
			Keywords: fact.Keywords,
			Content:  fact.Content,
			Author:   fact.Author,
		})
	}

	// Attachments (metadata only — markdown content is excluded)
	for _, att := range state.Attachments {
		resp.Attachments = append(resp.Attachments, BlackboardAttachmentResponse{
			ID:           att.ID,
			OriginalName: att.OriginalName,
			Format:       att.Format,
			SizeBytes:    att.SizeBytes,
			AttachedAt:   att.AttachedAt.Format(time.RFC3339),
		})
	}

	return resp
}

// ResolvePendingMessage patches the metadata of the most recent persisted
// message with the given role and matching field value, merging the extra
// fields. Used by desktop HITL response handlers to mark tool_confirm /
// ask_user / step_limit / plan_review messages as resolved in the DB so they
// don't reappear as pending on session reload.
func (f *FrontendAPI) ResolvePendingMessage(sessionID, role, matchField, matchValue string, extra map[string]any) error {
	if f.store == nil {
		return errors.New("session store not available")
	}
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	return f.store.ResolvePendingMessage(ctx, sessionID, role, matchField, matchValue, extra)
}
