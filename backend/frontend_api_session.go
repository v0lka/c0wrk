package backend

import (
	"errors"
	"fmt"
	"time"

	"github.com/user/agent/backend/session"
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
		if err := f.store.SaveSession(*info); err != nil {
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
		if err := f.store.DeleteSession(id); err != nil {
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
		if err := f.store.RenameSession(id, name); err != nil {
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
		info, err := f.store.LoadSession(id)
		if err == nil && info != nil {
			if err := f.store.ArchiveSession(id, !info.Archived); err != nil {
				f.log().Error("failed to archive session in store", "error", err)
			}
		}
	}
	return nil
}

// SendMessage sends a user message to a session (async - results come via events).
// Always uses Plan&Execute mode.
func (f *FrontendAPI) SendMessage(id, text string) error {
	if f.app == nil || f.app.Manager() == nil {
		return errors.New("session manager not initialized - check startup logs for LLM router or configuration errors")
	}
	// Update session activity timestamp
	// Best-effort persistence: log and continue to avoid disrupting the user session.
	if f.store != nil {
		if err := f.store.UpdateSessionActivity(id); err != nil {
			f.log().Error("failed to update session activity", "error", err)
		}
	}
	// Save user message to store
	// Best-effort persistence: log and continue to avoid disrupting the user session.
	if f.store != nil {
		if err := f.store.SaveMessage(session.ChatMessage{
			SessionID: id,
			Role:      "user",
			Content:   text,
			CreatedAt: time.Now().Format(time.RFC3339),
		}); err != nil {
			f.log().Error("failed to save user message to store", "error", err)
		}
	}

	// Check if this is the first message (session has default name)
	// Title generation is handled by the backend session Manager.

	if err := f.app.Manager().SendMessage(f.appCtx(), id, text); err != nil {
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
	return f.app.Manager().ResumeTask(f.appCtx(), id)
}

// GetSessionHistory returns chat history for a session.
func (f *FrontendAPI) GetSessionHistory(id string) ([]session.ChatMessage, error) {
	if f.store != nil {
		return f.store.LoadMessages(id)
	}
	return []session.ChatMessage{}, nil
}
