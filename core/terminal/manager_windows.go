//go:build windows

// Package terminal provides PTY-based shell session management for the desktop UI.
//
// On Windows the manager is backed by the ConPTY (Pseudo Console) API, which
// exposes a Unix-PTY-like read/write/resize interface around a child process.
package terminal

import (
	"context"
	"errors"
	"fmt"
	"io"
	"log/slog"
	"os"
	"os/exec"
	"strings"
	"sync"

	"github.com/UserExistsError/conpty"
)

// resolveWindowsShell picks the shell binary to spawn under the pseudo console.
// It prefers Windows PowerShell (powershell.exe, ships with every Windows
// install) and falls back to cmd.exe. The returned args string may be empty.
func resolveWindowsShell() (path, args string, err error) {
	if p, lookErr := exec.LookPath("powershell.exe"); lookErr == nil {
		return p, "-NoLogo", nil
	}
	if p, lookErr := exec.LookPath("cmd.exe"); lookErr == nil {
		return p, "", nil
	}
	return "", "", errors.New("no suitable Windows shell found (powershell.exe or cmd.exe)")
}

// quoteWindowsCommandLine quotes an executable path for inclusion as the first
// token of a ConPTY command line. ConPTY parses its command line via the
// standard argv splitting rules, so a path containing spaces must be quoted.
func quoteWindowsCommandLine(path string) string {
	if strings.ContainsAny(path, " \t") && !strings.HasPrefix(path, `"`) {
		return `"` + path + `"`
	}
	return path
}

// Manager owns ConPTY-backed shell instances keyed by session ID.
//
// The Manager struct itself is declared in manager_common.go; this file
// extends the platform-specific Session and provides the ConPTY wrappers.

// Session represents a single ConPTY-backed shell session.
type Session struct {
	cpty   *conpty.ConPty
	shell  string
	cancel context.CancelFunc
	mu     sync.Mutex // per-session mutex for ConPTY I/O (avoids serializing writes across sessions)
}

// NewManager creates a new terminal manager.
// The rootCtx is the application lifecycle context — cancelling it triggers
// cleanup of all active terminal sessions.
func NewManager(rootCtx context.Context, logger *slog.Logger, emit func(sessionID string, data []byte)) *Manager {
	if logger == nil {
		logger = slog.Default()
	}
	if emit == nil {
		emit = func(string, []byte) {} // no-op fallback
	}
	return &Manager{
		rootCtx:  rootCtx,
		sessions: make(map[string]*Session),
		logger:   logger,
		emit:     emit,
	}
}

// Start creates a new ConPTY-backed shell for the given session ID.
// The workDir is used as the shell's working directory.
func (m *Manager) Start(sessionID, workDir string) error {
	m.mu.Lock()
	defer m.mu.Unlock()

	if _, exists := m.sessions[sessionID]; exists {
		return fmt.Errorf("terminal already active for session %s", sessionID)
	}

	shellPath, shellArgs, err := resolveWindowsShell()
	if err != nil {
		return fmt.Errorf("failed to resolve shell: %w", err)
	}

	cmdLine := quoteWindowsCommandLine(shellPath)
	if shellArgs != "" {
		cmdLine += " " + shellArgs
	}

	opts := []conpty.ConPtyOption{
		conpty.ConPtyDimensions(80, 24),
		conpty.ConPtyEnv(buildTermEnv()),
	}
	if workDir != "" {
		opts = append(opts, conpty.ConPtyWorkDir(workDir))
	}

	// ConPTY availability depends on Windows 10+; surface a clear error when
	// the host is too old rather than letting Start return an opaque failure.
	if !conpty.IsConPtyAvailable() {
		return errors.New("ConPTY is not available on this version of Windows")
	}

	cpty, err := conpty.Start(cmdLine, opts...)
	if err != nil {
		return fmt.Errorf("failed to start conpty: %w", err)
	}

	ctx, cancel := context.WithCancel(m.rootCtx)

	m.sessions[sessionID] = &Session{cpty: cpty, shell: shellPath, cancel: cancel}

	// Start goroutine to read output and emit events.
	go m.readLoop(ctx, sessionID, cpty)

	m.logger.Info("terminal started", "session_id", sessionID, "shell", shellPath, "workDir", workDir)
	return nil
}

// Write sends data to the ConPTY stdin of the given session.
// Uses a per-session mutex so writes across different sessions don't serialize.
func (m *Manager) Write(sessionID string, data []byte) error {
	m.mu.Lock()
	sess, exists := m.sessions[sessionID]
	m.mu.Unlock()
	if !exists {
		return fmt.Errorf("no terminal for session %s", sessionID)
	}
	sess.mu.Lock()
	defer sess.mu.Unlock()
	_, err := sess.cpty.Write(data)
	if err != nil {
		return fmt.Errorf("failed to write to conpty: %w", err)
	}
	return nil
}

// Resize updates the ConPTY size for the given session.
// Uses a per-session mutex so resizes across different sessions don't serialize.
func (m *Manager) Resize(sessionID string, cols, rows int) error {
	m.mu.Lock()
	sess, exists := m.sessions[sessionID]
	m.mu.Unlock()
	if !exists {
		return fmt.Errorf("no terminal for session %s", sessionID)
	}
	sess.mu.Lock()
	defer sess.mu.Unlock()
	err := sess.cpty.Resize(cols, rows)
	if err != nil {
		return fmt.Errorf("failed to resize conpty: %w", err)
	}
	return nil
}

// teardown releases all resources owned by a session: it cancels the context
// and closes the ConPTY (which terminates the child process and releases all
// handles). It must only be called by the goroutine that just removed the
// session from m.sessions, so cleanup is never performed twice.
func (m *Manager) teardown(sess *Session, sessionID string) {
	sess.cancel()
	if err := sess.cpty.Close(); err != nil {
		m.logger.Warn("failed to close conpty", "session_id", sessionID, "error", err)
	}
}

// Stop terminates the shell and closes the ConPTY for the given session.
func (m *Manager) Stop(sessionID string) error {
	m.mu.Lock()
	sess, exists := m.sessions[sessionID]
	if !exists {
		m.mu.Unlock()
		return fmt.Errorf("no terminal for session %s", sessionID)
	}
	delete(m.sessions, sessionID)
	m.mu.Unlock()

	m.teardown(sess, sessionID)

	m.logger.Info("terminal stopped", "session_id", sessionID)
	return nil
}

// StopAll terminates all active terminal sessions.
func (m *Manager) StopAll() {
	m.mu.Lock()
	ids := make([]string, 0, len(m.sessions))
	for id := range m.sessions {
		ids = append(ids, id)
	}
	m.mu.Unlock()

	for _, id := range ids {
		if err := m.Stop(id); err != nil {
			m.logger.Warn("failed to stop terminal during shutdown", "session_id", id, "error", err)
		}
	}
}

// IsActive returns true if a terminal is active for the given session.
func (m *Manager) IsActive(sessionID string) bool {
	m.mu.Lock()
	defer m.mu.Unlock()
	_, exists := m.sessions[sessionID]
	return exists
}

// readLoop continuously reads from the ConPTY and emits output events.
// It exits when the ConPTY is closed or the context is cancelled.
func (m *Manager) readLoop(ctx context.Context, sessionID string, cpty *conpty.ConPty) {
	buf := make([]byte, 4096)
	for {
		select {
		case <-ctx.Done():
			return
		default:
		}

		n, err := cpty.Read(buf)
		if n > 0 {
			data := make([]byte, n)
			copy(data, buf[:n])
			m.emit(sessionID, data)
		}
		if err != nil {
			if !errors.Is(err, io.EOF) && !errors.Is(err, os.ErrClosed) {
				m.logger.Debug("conpty read error", "session_id", sessionID, "error", err)
			}
			break
		}
	}

	// Clean up the session when the process exits naturally. Stop() cannot
	// reclaim the context/handles once the session is gone from the map, so
	// readLoop must mirror its teardown to avoid leaking the per-session
	// context and ConPTY handles.
	m.mu.Lock()
	if sess, exists := m.sessions[sessionID]; exists && sess.cpty == cpty {
		delete(m.sessions, sessionID)
		m.mu.Unlock()
		m.teardown(sess, sessionID)
	} else {
		m.mu.Unlock()
	}

	m.emit(sessionID, []byte("\r\n\x1b[31m[Terminal session ended]\x1b[0m\r\n"))
	m.logger.Info("terminal process exited", "session_id", sessionID)
}
