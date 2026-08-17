//go:build !windows

// Package terminal provides PTY-based shell session management for the desktop UI.
package terminal

import (
	"context"
	"errors"
	"fmt"
	"io"
	"log/slog"
	"os"
	"os/exec"
	"sync"

	"github.com/creack/pty"
	"github.com/v0lka/c0wrk/internal/shellresolver"
)

// Session represents a single PTY-backed shell session.
type Session struct {
	cmd    *exec.Cmd
	ptmx   *os.File
	cancel context.CancelFunc
	mu     sync.Mutex // per-session mutex for PTY I/O (avoids serializing writes across sessions)
}

// NewManager creates a new terminal manager.
// The rootCtx is the application lifecycle context — cancelling it triggers
// cleanup of all active terminal sessions.
// The emit callback streams raw PTY output; onExit is fired when a shell
// process exits on its own (not on explicit Stop/StopAll). Both are optional
// (nil → no-op).
func NewManager(rootCtx context.Context, logger *slog.Logger, emit func(sessionID string, data []byte), onExit func(sessionID string)) *Manager {
	if logger == nil {
		logger = slog.Default()
	}
	if emit == nil {
		emit = func(string, []byte) {} // no-op fallback
	}
	if onExit == nil {
		onExit = func(string) {} // no-op fallback
	}
	return &Manager{
		rootCtx:  rootCtx,
		sessions: make(map[string]*Session),
		logger:   logger,
		emit:     emit,
		onExit:   onExit,
	}
}

// Start creates a new PTY-backed shell for the given session ID.
// The workDir is used as the shell's working directory.
func (m *Manager) Start(sessionID, workDir string) error {
	m.mu.Lock()
	defer m.mu.Unlock()

	if _, exists := m.sessions[sessionID]; exists {
		return fmt.Errorf("terminal already active for session %s", sessionID)
	}

	shell := shellresolver.Resolve()

	ctx, cancel := context.WithCancel(m.rootCtx)
	cmd := exec.CommandContext(ctx, shell, "-l")
	if workDir != "" {
		cmd.Dir = workDir
	}

	// Set terminal-specific environment variables so the shell knows its
	// capabilities (cursor movement, colors, completion menus, etc.).
	// Without TERM the shell defaults to "dumb" and advanced features like
	// zsh autosuggestions and tab-completion menus render incorrectly.
	cmd.Env = buildTermEnv()

	ptmx, err := pty.Start(cmd)
	if err != nil {
		cancel()
		return fmt.Errorf("failed to start pty: %w", err)
	}

	// Ensure initial terminal size is set — some shells wait for a valid size
	// before emitting a prompt.
	_ = pty.Setsize(ptmx, &pty.Winsize{Cols: 80, Rows: 24})

	m.sessions[sessionID] = &Session{cmd: cmd, ptmx: ptmx, cancel: cancel}

	// Start goroutine to read output and emit events.
	go m.readLoop(ctx, sessionID, ptmx)

	m.logger.Info("terminal started", "session_id", sessionID, "shell", shell, "workDir", workDir)
	return nil
}

// Write sends data to the PTY stdin of the given session.
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
	_, err := sess.ptmx.Write(data)
	if err != nil {
		return fmt.Errorf("failed to write to pty: %w", err)
	}
	return nil
}

// Resize updates the PTY size for the given session.
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
	err := pty.Setsize(sess.ptmx, &pty.Winsize{Cols: uint16(cols), Rows: uint16(rows)})
	if err != nil {
		return fmt.Errorf("failed to resize pty: %w", err)
	}
	return nil
}

// teardown releases all resources owned by a session: it cancels the context
// (which kills the process via exec.CommandContext), closes the PTY fd, and
// signals the shell to terminate. It must only be called by the goroutine that
// just removed the session from m.sessions, so cleanup is never performed twice.
func (m *Manager) teardown(sess *Session, sessionID string) {
	sess.cancel()
	if err := sess.ptmx.Close(); err != nil {
		m.logger.Warn("failed to close pty", "session_id", sessionID, "error", err)
	}
	if sess.cmd.Process != nil {
		if err := sess.cmd.Process.Kill(); err != nil && !errors.Is(err, os.ErrProcessDone) {
			m.logger.Warn("failed to kill shell process", "session_id", sessionID, "error", err)
		}
	}
}

// Stop terminates the shell and closes the PTY for the given session.
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

// readLoop continuously reads from the PTY and emits output events.
// It exits when the PTY is closed or the context is cancelled.
func (m *Manager) readLoop(ctx context.Context, sessionID string, ptmx *os.File) {
	buf := make([]byte, 4096)
	for {
		select {
		case <-ctx.Done():
			return
		default:
		}

		n, err := ptmx.Read(buf)
		if n > 0 {
			data := make([]byte, n)
			copy(data, buf[:n])
			m.emit(sessionID, data)
		}
		if err != nil {
			if !errors.Is(err, io.EOF) && !errors.Is(err, os.ErrClosed) {
				m.logger.Debug("pty read error", "session_id", sessionID, "error", err)
			}
			break
		}
	}

	// Clean up the session when the process exits naturally. Stop() cannot
	// reclaim the fd/context once the session is gone from the map, so
	// readLoop must mirror its teardown to avoid leaking the PTY fd and the
	// per-session context.
	m.mu.Lock()
	if sess, exists := m.sessions[sessionID]; exists && sess.ptmx == ptmx {
		delete(m.sessions, sessionID)
		m.mu.Unlock()
		m.teardown(sess, sessionID)
	} else {
		m.mu.Unlock()
	}

	m.emit(sessionID, []byte("\r\n\x1b[31m[Terminal session ended]\x1b[0m\r\n"))
	m.logger.Info("terminal process exited", "session_id", sessionID)

	// Signal natural exit so the UI can resurrect the shell lazily (on next
	// activation). Explicit Stop/StopAll paths do not fire this — they either
	// remove the terminal for good (session delete, app shutdown) or are
	// immediately followed by a fresh Start (StartTerminalInDir).
	m.onExit(sessionID)
}
