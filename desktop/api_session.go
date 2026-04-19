package desktop

import (
	"errors"
	"fmt"
	"time"

	"github.com/user/agent/backend/session"
)

// CreateSession creates a new agent session within the active project.
func (a *App) CreateSession() (*session.SessionInfo, error) {
	if a.manager == nil {
		return nil, errors.New("session manager not initialized - check startup logs for LLM router or configuration errors")
	}

	a.activeProjectMu.RLock()
	projectID := a.activeProjectID
	projectPath := a.activeProjectPath
	a.activeProjectMu.RUnlock()

	if projectID == "" {
		return nil, errors.New("no active project — create or select a project first")
	}

	info, err := a.manager.CreateSession(projectID, projectPath)
	if err != nil {
		return nil, fmt.Errorf("failed to create session: %w", err)
	}
	// Persist to SQLite
	// Best-effort persistence: log and continue to avoid disrupting the user session.
	if a.store != nil {
		if err := a.store.SaveSession(*info); err != nil {
			a.log().Error("failed to save session to store", "error", err)
		}
	}
	return info, nil
}

// DeleteSession removes a session.
func (a *App) DeleteSession(id string) error {
	if a.manager == nil {
		return errors.New("session manager not initialized")
	}
	// Only delete from manager if session exists in memory
	if _, exists := a.manager.GetSession(id); exists {
		if err := a.manager.DeleteSession(id); err != nil {
			return fmt.Errorf("failed to delete session: %w", err)
		}
	}
	// Always delete from store (handles store-only sessions from previous runs)
	// Best-effort persistence: log and continue to avoid disrupting the user session.
	if a.store != nil {
		if err := a.store.DeleteSession(id); err != nil {
			a.log().Error("failed to delete session from store", "error", err)
		}
	}
	return nil
}

// ListSessions returns sessions for the active project.
func (a *App) ListSessions() ([]session.SessionInfo, error) {
	a.activeProjectMu.RLock()
	projectID := a.activeProjectID
	a.activeProjectMu.RUnlock()

	if projectID == "" {
		return []session.SessionInfo{}, nil
	}

	if a.manager == nil {
		return []session.SessionInfo{}, nil
	}

	return a.manager.ListSessionsByProject(projectID)
}

// RenameSession changes session name.
func (a *App) RenameSession(id, name string) error {
	if a.manager == nil {
		return errors.New("session manager not initialized")
	}
	// Only rename in manager if session exists in memory
	if _, exists := a.manager.GetSession(id); exists {
		if err := a.manager.RenameSession(id, name); err != nil {
			return fmt.Errorf("failed to rename session: %w", err)
		}
	}
	// Always rename in store (handles store-only sessions from previous runs)
	// Best-effort persistence: log and continue to avoid disrupting the user session.
	if a.store != nil {
		if err := a.store.RenameSession(id, name); err != nil {
			a.log().Error("failed to rename session in store", "error", err)
		}
	}
	return nil
}

// ArchiveSession archives/unarchives a session.
func (a *App) ArchiveSession(id string) error {
	if a.manager == nil {
		return errors.New("session manager not initialized")
	}
	// Only archive in manager if session exists in memory
	if _, exists := a.manager.GetSession(id); exists {
		if err := a.manager.ArchiveSession(id); err != nil {
			return fmt.Errorf("failed to archive session: %w", err)
		}
	}
	// Toggle archive in store
	// Best-effort persistence: log and continue to avoid disrupting the user session.
	if a.store != nil {
		info, err := a.store.LoadSession(id)
		if err == nil && info != nil {
			if err := a.store.ArchiveSession(id, !info.Archived); err != nil {
				a.log().Error("failed to archive session in store", "error", err)
			}
		}
	}
	return nil
}

// SendMessage sends a user message to a session (async - results come via events).
// Always uses Plan&Execute mode.
func (a *App) SendMessage(id, text string) error {
	if a.manager == nil {
		return errors.New("session manager not initialized - check startup logs for LLM router or configuration errors")
	}
	// Update session activity timestamp
	// Best-effort persistence: log and continue to avoid disrupting the user session.
	if a.store != nil {
		if err := a.store.UpdateSessionActivity(id); err != nil {
			a.log().Error("failed to update session activity", "error", err)
		}
	}
	// Save user message to store
	// Best-effort persistence: log and continue to avoid disrupting the user session.
	if a.store != nil {
		if err := a.store.SaveMessage(session.ChatMessage{
			SessionID: id,
			Role:      "user",
			Content:   text,
			CreatedAt: time.Now().Format(time.RFC3339),
		}); err != nil {
			a.log().Error("failed to save user message to store", "error", err)
		}
	}

	// Check if this is the first message (session has default name)
	// Title generation is handled by the backend session Manager.

	if err := a.manager.SendMessage(a.ctx, id, text); err != nil {
		return fmt.Errorf("failed to send message: %w", err)
	}
	return nil
}

// CancelTask cancels the running task in a session.
func (a *App) CancelTask(id string) error {
	if a.manager == nil {
		return errors.New("session manager not initialized")
	}
	return a.manager.CancelTask(id)
}

// ResumeTask resumes an interrupted task in the given session, if any.
// Returns nil if no unfinished task exists. This is safe to call on session load.
func (a *App) ResumeTask(id string) error {
	if a.manager == nil {
		return errors.New("session manager not initialized")
	}
	return a.manager.ResumeTask(a.ctx, id)
}

// GetSessionHistory returns chat history for a session.
func (a *App) GetSessionHistory(id string) ([]session.ChatMessage, error) {
	if a.store != nil {
		return a.store.LoadMessages(id)
	}
	return []session.ChatMessage{}, nil
}
