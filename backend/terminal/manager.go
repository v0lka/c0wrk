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
	"strings"
	"sync"

	"github.com/creack/pty"
)

// buildTermEnv returns the current process environment with terminal-specific
// variables injected. xterm.js is an xterm-compatible terminal with 256-color
// (and true-color) support, so we set TERM=xterm-256color and COLORTERM=truecolor.
func buildTermEnv() []string {
	env := os.Environ()
	hasTerm := false
	hasColorterm := false
	for _, e := range env {
		if strings.HasPrefix(e, "TERM=") {
			hasTerm = true
		}
		if strings.HasPrefix(e, "COLORTERM=") {
			hasColorterm = true
		}
	}
	if !hasTerm {
		env = append(env, "TERM=xterm-256color")
	}
	if !hasColorterm {
		env = append(env, "COLORTERM=truecolor")
	}
	return env
}

// Manager owns PTY instances keyed by session ID.
type Manager struct {
	mu       sync.Mutex
	sessions map[string]*Session
	logger   *slog.Logger
	emit     func(sessionID string, data []byte)
}

// Session represents a single PTY-backed shell session.
type Session struct {
	cmd  *exec.Cmd
	ptmx *os.File
}

// NewManager creates a new terminal manager.
func NewManager(logger *slog.Logger, emit func(sessionID string, data []byte)) *Manager {
	if logger == nil {
		logger = slog.Default()
	}
	return &Manager{
		sessions: make(map[string]*Session),
		logger:   logger,
		emit:     emit,
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

	shell := os.Getenv("SHELL")
	if shell == "" {
		shell = "/bin/bash"
	}

	cmd := exec.CommandContext(context.Background(), shell, "-l")
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
		return fmt.Errorf("failed to start pty: %w", err)
	}

	// Ensure initial terminal size is set — some shells wait for a valid size
	// before emitting a prompt.
	_ = pty.Setsize(ptmx, &pty.Winsize{Cols: 80, Rows: 24})

	m.sessions[sessionID] = &Session{cmd: cmd, ptmx: ptmx}

	// Start goroutine to read output and emit events.
	go m.readLoop(sessionID, ptmx)

	m.emit(sessionID, []byte("\r\n\x1b[32m[Terminal ready]\x1b[0m\r\n"))

	m.logger.Info("terminal started", "session_id", sessionID, "shell", shell, "workDir", workDir)
	return nil
}

// Write sends data to the PTY stdin of the given session.
func (m *Manager) Write(sessionID string, data []byte) error {
	m.mu.Lock()
	sess, exists := m.sessions[sessionID]
	m.mu.Unlock()

	if !exists {
		return fmt.Errorf("no terminal for session %s", sessionID)
	}

	if _, err := sess.ptmx.Write(data); err != nil {
		return fmt.Errorf("failed to write to pty: %w", err)
	}
	return nil
}

// Resize updates the PTY size for the given session.
func (m *Manager) Resize(sessionID string, cols, rows int) error {
	m.mu.Lock()
	sess, exists := m.sessions[sessionID]
	m.mu.Unlock()

	if !exists {
		return fmt.Errorf("no terminal for session %s", sessionID)
	}

	if err := pty.Setsize(sess.ptmx, &pty.Winsize{Cols: uint16(cols), Rows: uint16(rows)}); err != nil {
		return fmt.Errorf("failed to resize pty: %w", err)
	}
	return nil
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

	if err := sess.ptmx.Close(); err != nil {
		m.logger.Warn("failed to close pty", "session_id", sessionID, "error", err)
	}

	if sess.cmd.Process != nil {
		if err := sess.cmd.Process.Kill(); err != nil && !errors.Is(err, os.ErrProcessDone) {
			m.logger.Warn("failed to kill shell process", "session_id", sessionID, "error", err)
		}
	}

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
func (m *Manager) readLoop(sessionID string, ptmx *os.File) {
	buf := make([]byte, 4096)
	for {
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

	// Clean up the session when the process exits naturally.
	m.mu.Lock()
	if sess, exists := m.sessions[sessionID]; exists && sess.ptmx == ptmx {
		delete(m.sessions, sessionID)
	}
	m.mu.Unlock()

	m.emit(sessionID, []byte("\r\n\x1b[31m[Terminal session ended]\x1b[0m\r\n"))
	m.logger.Info("terminal process exited", "session_id", sessionID)
}
