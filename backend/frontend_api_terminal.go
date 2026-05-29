package backend

import (
	"context"
	"errors"
	"fmt"

	"github.com/v0lka/c0wrk/backend/session"
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
