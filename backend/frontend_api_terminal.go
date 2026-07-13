package backend

import (
	"context"
	"errors"
	"fmt"

	"github.com/v0lka/c0wrk/backend/session"
	"github.com/v0lka/sp4rk/pathutil"
)

// StartTerminal starts a new PTY-backed shell for the given session.
func (f *FrontendAPI) StartTerminal(sessionID string) error {
	if f.terminalManager == nil {
		return errors.New("terminal manager not initialized")
	}
	if f.app == nil || f.app.Manager() == nil {
		return errors.New("session manager not initialized")
	}

	workDir, ok := f.app.Manager().GetSessionWorkspacePath(sessionID)
	if !ok {
		return errors.New("session not found")
	}

	if err := f.terminalManager.Start(sessionID, workDir); err != nil {
		return fmt.Errorf("failed to start terminal: %w", err)
	}
	return nil
}

// StartTerminalInDir starts a PTY-backed shell for the given session in the
// specified working directory. The workDir must be within the session's
// workspace path (path containment check). If a terminal is already active
// for the session it is stopped first so the new shell can start in the
// requested directory.
func (f *FrontendAPI) StartTerminalInDir(sessionID, workDir string) error {
	if f.terminalManager == nil {
		return errors.New("terminal manager not initialized")
	}
	if f.app == nil || f.app.Manager() == nil {
		return errors.New("session manager not initialized")
	}

	wsPath, ok := f.app.Manager().GetSessionWorkspacePath(sessionID)
	if !ok {
		return errors.New("session not found")
	}

	within, err := pathutil.IsWithinPath(wsPath, workDir)
	if err != nil {
		return fmt.Errorf("failed to validate working directory: %w", err)
	}
	if !within {
		return errors.New("working directory is outside the session workspace")
	}

	// Stop any existing terminal so the manager can start a fresh one in the
	// new directory. Stop is a no-op when no terminal is active.
	if f.terminalManager.IsActive(sessionID) {
		if err := f.terminalManager.Stop(sessionID); err != nil {
			return fmt.Errorf("failed to stop existing terminal: %w", err)
		}
	}

	if err := f.terminalManager.Start(sessionID, workDir); err != nil {
		return fmt.Errorf("failed to start terminal: %w", err)
	}
	return nil
}

// TerminalInput sends user input to the terminal PTY.
func (f *FrontendAPI) TerminalInput(sessionID, data string) error {
	if f.terminalManager == nil {
		return errors.New("terminal manager not initialized")
	}
	if err := f.terminalManager.Write(sessionID, []byte(data)); err != nil {
		return fmt.Errorf("failed to write to terminal: %w", err)
	}
	return nil
}

// TerminalResize updates the terminal dimensions.
func (f *FrontendAPI) TerminalResize(sessionID string, cols, rows int) error {
	if f.terminalManager == nil {
		return errors.New("terminal manager not initialized")
	}
	if err := f.terminalManager.Resize(sessionID, cols, rows); err != nil {
		return fmt.Errorf("failed to resize terminal: %w", err)
	}
	return nil
}

// StopTerminal stops the terminal for the given session.
func (f *FrontendAPI) StopTerminal(sessionID string) error {
	if f.terminalManager == nil {
		return errors.New("terminal manager not initialized")
	}
	if err := f.terminalManager.Stop(sessionID); err != nil {
		return fmt.Errorf("failed to stop terminal: %w", err)
	}
	return nil
}

// GetTerminalHistory returns the command history for a session.
func (f *FrontendAPI) GetTerminalHistory(sessionID string) ([]session.TerminalCommand, error) {
	if f.store == nil {
		return []session.TerminalCommand{}, nil
	}
	commands, err := f.store.LoadTerminalCommands(context.Background(), sessionID, 100)
	if err != nil {
		return nil, fmt.Errorf("failed to load terminal history: %w", err)
	}
	return commands, nil
}
