package backend

import (
	"context"
	"errors"
	"fmt"
	"time"

	"github.com/v0lka/c0wrk/backend/session"
	"github.com/v0lka/sp4rk/pathutil"
)

// terminalPathLookupTimeout bounds the session workspace lookup: read-only, but it
// shares the app's single SQLite connection with every active session's writes, so
// the bound turns connection-pool queuing into a prompt, retryable error.
const terminalPathLookupTimeout = 10 * time.Second

// StartTerminal starts a PTY-backed shell for the session; an already-active terminal
// makes this a reattach (returns nil). See specs/domains/session-lifecycle.md.
func (f *FrontendAPI) StartTerminal(sessionID string) error {
	if f.terminalManager == nil {
		return errors.New("terminal manager not initialized")
	}
	if f.appCell() == nil || f.app.Manager() == nil {
		return errors.New("session manager not initialized")
	}

	if f.terminalManager.IsActive(sessionID) {
		return nil
	}

	workDir, ok := f.workspacePathForTerminal(sessionID)
	if !ok {
		return errors.New("session not found")
	}

	if err := f.terminalManager.Start(sessionID, workDir); err != nil {
		return fmt.Errorf("failed to start terminal: %w", err)
	}
	return nil
}

// workspacePathForTerminal resolves the session workspace through the read-only
// Manager.WorkspacePathFor — NOT GetSessionWorkspacePath, whose lazy restore builds
// a full orchestrator per start and hung terminal starts behind the shared pool.
func (f *FrontendAPI) workspacePathForTerminal(sessionID string) (string, bool) {
	f.seedAcquire()
	ctx, cancel := context.WithTimeout(context.Background(), terminalPathLookupTimeout)
	defer cancel()
	return f.app.Manager().WorkspacePathFor(ctx, sessionID)
}

// StartTerminalInDir restarts the session's PTY in workDir (must be within the session
// workspace); an already-active terminal is stopped first. See specs/domains/session-lifecycle.md.
func (f *FrontendAPI) StartTerminalInDir(sessionID, workDir string) error {
	if f.terminalManager == nil {
		return errors.New("terminal manager not initialized")
	}
	if f.appCell() == nil || f.app.Manager() == nil {
		return errors.New("session manager not initialized")
	}

	wsPath, ok := f.workspacePathForTerminal(sessionID)
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
	f.seedAcquire()
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
	f.seedAcquire()
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
	f.seedAcquire()
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
	f.seedAcquire()
	if f.store == nil {
		return []session.TerminalCommand{}, nil
	}
	// Bounded like workspacePathForTerminal: the same shared pool must not park
	// the history load.
	ctx, cancel := context.WithTimeout(f.ctx(), terminalPathLookupTimeout)
	defer cancel()
	commands, err := f.store.LoadTerminalCommands(ctx, sessionID, 100)
	if err != nil {
		return nil, fmt.Errorf("failed to load terminal history: %w", err)
	}
	return commands, nil
}
