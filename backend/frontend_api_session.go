package backend

import (
	"context"
	"errors"
	"fmt"
	"regexp"
	"strings"
	"time"

	"github.com/user/agent/backend/session"
	"github.com/user/agent/core"
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

// DeleteSession removes a session.
func (f *FrontendAPI) DeleteSession(id string) error {
	if f.app == nil || f.app.Manager() == nil {
		return errors.New("session manager not initialized")
	}
	manager := f.app.Manager()
	// Only delete from manager if session exists in memory
	if _, exists := manager.GetSession(id); exists {
		if err := manager.DeleteSession(id); err != nil {
			return fmt.Errorf("failed to delete session: %w", err)
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

// SendMessage sends a user message to a session (async - results come via events).
// mode controls execution strategy: "normal" = single-step plan, "advanced" = full multi-step DAG.
// activeSkills contains skill names explicitly referenced by the user via /skill-name syntax.
func (f *FrontendAPI) SendMessage(id, text, mode string, activeSkills []string) error {
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
		if err := f.store.SaveMessage(context.Background(), session.ChatMessage{
			SessionID: id,
			Role:      "user",
			Content:   text,
			CreatedAt: time.Now().Format(time.RFC3339),
		}); err != nil {
			f.log().Error("failed to save user message to store", "error", err)
		}
	}

	// Preprocess text for the orchestrator: strip /skill refs and convert @file refs to fileref:// URIs.
	processedText := preprocessMessageText(text, activeSkills)

	if err := f.app.Manager().SendMessage(f.ctx(), id, processedText, mode, activeSkills); err != nil {
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

	return resp
}

// fileRefPattern matches @path references (with optional backslash-escaped spaces and #LN or #LN-M suffix).
var fileRefPattern = regexp.MustCompile(`(?:^|\s)@((?:[^\s\\]|\\.)+(?:#\d+(?:-\d+)?)?)`)

// preprocessMessageText transforms a user message for the orchestrator:
// 1. Strips /skill-name references for each skill in activeSkills.
// 2. Converts @file-path references to fileref:// URIs.
func preprocessMessageText(text string, activeSkills []string) string {
	result := text

	// Strip skill references.
	for _, name := range activeSkills {
		pattern := regexp.MustCompile(`(?:^|\s)/` + regexp.QuoteMeta(name) + `(?:\s|$)`)
		result = pattern.ReplaceAllStringFunc(result, func(match string) string {
			// Preserve surrounding whitespace boundaries: if the match had leading/trailing space,
			// collapse to a single space; if it was at start/end, remove entirely.
			leading := match != "" && match[0] == ' '
			trailing := match != "" && match[len(match)-1] == ' '
			if leading || trailing {
				return " "
			}
			return ""
		})
	}

	// Convert @file references to fileref:// URIs.
	result = fileRefPattern.ReplaceAllStringFunc(result, func(match string) string {
		// Preserve leading whitespace.
		prefix := ""
		trimmed := match
		if trimmed != "" && (trimmed[0] == ' ' || trimmed[0] == '\t' || trimmed[0] == '\n') {
			prefix = trimmed[:1]
			trimmed = trimmed[1:]
		}
		// Remove the @ prefix.
		path := strings.TrimPrefix(trimmed, "@")
		// Unescape backslash-escaped spaces.
		path = strings.ReplaceAll(path, `\ `, " ")
		return prefix + "fileref://" + path
	})

	// Collapse multiple spaces into one.
	result = regexp.MustCompile(`  +`).ReplaceAllString(result, " ")
	return strings.TrimSpace(result)
}
