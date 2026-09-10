package backend

import (
	"context"
	"errors"
	"fmt"
	"time"

	"github.com/v0lka/c0wrk/backend/session"
	"github.com/v0lka/sp4rk/pathutil"
)

// terminalPathLookupTimeout bounds the session workspace lookup in
// StartTerminal/StartTerminalInDir. The lookup is read-only (see
// Manager.WorkspacePathFor) but still shares the app's single SQLite
// connection with every write of every active session; the bound turns
// connection-pool queuing into a prompt, retryable error instead of an
// indefinitely spinning terminal loader.
const terminalPathLookupTimeout = 10 * time.Second

// StartTerminal starts a new PTY-backed shell for the given session.
// Terminals are kept alive per-session for the whole app lifetime (the UI
// persists one xterm.js instance per session and never stops the PTY on
// session/project switches), so a start request for a session that already
// has an active terminal is treated as a reattach: it returns nil and the
// caller's terminal attaches to the live output stream.
func (f *FrontendAPI) StartTerminal(sessionID string) error {
	if f.terminalManager == nil {
		return errors.New("terminal manager not initialized")
	}
	if f.app == nil || f.app.Manager() == nil {
		return errors.New("session manager not initialized")
	}

	// Reattach: the PTY is already running for this session.
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

// workspacePathForTerminal resolves the session's workspace path through the
// read-only Manager.WorkspacePathFor lookup. Deliberately NOT
// GetSessionWorkspacePath: that helper lazily restores the session, building
// a full orchestrator per start — and its unbounded store reads, serialized
// behind active sessions' writes on the single SQLite connection, are what
// made terminal starts hang for minutes while bash_exec storms ran. On
// timeout or store failure the bool is false and the caller reports a
// retryable error.
func (f *FrontendAPI) workspacePathForTerminal(sessionID string) (string, bool) {
	ctx, cancel := context.WithTimeout(context.Background(), terminalPathLookupTimeout)
	defer cancel()
	return f.app.Manager().WorkspacePathFor(ctx, sessionID)
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
