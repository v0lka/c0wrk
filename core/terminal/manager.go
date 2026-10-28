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

// sameEnvKey reports whether two env-var names denote the same variable.
// Unix names are case-sensitive: inherited entries always carry the exact
// spelling of a configured or built-in key, so byte equality suffices.
func sameEnvKey(a, b string) bool {
	return a == b
}

// Session represents a single PTY-backed shell session.
type Session struct {
	cmd      *exec.Cmd
	ptmx     *os.File
	cancel   context.CancelFunc
	readDone chan struct{} // closed after readLoop and all its callbacks return
	mu       sync.Mutex    // per-session mutex for PTY I/O (avoids serializing writes across sessions)
}

// NewManager creates a Manager. rootCtx is the app lifecycle context
// (cancelling it tears down every session); emit, onExit and userEnv are
// optional (nil → no-op / none) with their contracts documented on Manager.
func NewManager(rootCtx context.Context, logger *slog.Logger, emit func(sessionID string, data []byte), onExit func(sessionID string), userEnv map[string]string) *Manager {
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
		userEnv:  userEnv,
	}
}

// Start creates a new PTY-backed shell for sessionID with workDir as the
// shell's working directory.
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
	cmd.Env = buildTermEnv(m.userEnv)

	ptmx, err := pty.Start(cmd)
	if err != nil {
		cancel()
		return fmt.Errorf("failed to start pty: %w", err)
	}

	// Set an initial size — some shells withhold the prompt until the PTY has one.
	_ = pty.Setsize(ptmx, &pty.Winsize{Cols: 80, Rows: 24})

	sess := &Session{cmd: cmd, ptmx: ptmx, cancel: cancel, readDone: make(chan struct{})}
	m.sessions[sessionID] = sess

	// readDone fires only after readLoop's callbacks return; Stop must never
	// join it — a callback can itself call Stop and deadlock.
	go func() {
		defer close(sess.readDone)
		m.readLoop(ctx, sessionID, ptmx)
	}()

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
	sess.mu.Lock()
	defer sess.mu.Unlock()
	_, err := sess.ptmx.Write(data)
	if err != nil {
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
	sess.mu.Lock()
	defer sess.mu.Unlock()
	err := pty.Setsize(sess.ptmx, &pty.Winsize{Cols: uint16(cols), Rows: uint16(rows)})
	if err != nil {
		return fmt.Errorf("failed to resize pty: %w", err)
	}
	return nil
}

// teardown releases all session resources: cancel (killing the process via
// exec.CommandContext), PTY close, child kill. Single-owner: only the
// goroutine that just removed the session from m.sessions may call it.
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
	// Reap the child: the Go runtime does not auto-reap on Unix, and teardown
	// being the single lifecycle owner makes this the one Wait that can never
	// race another. Wait is prompt (child just killed/exited) and uninteresting.
	_ = sess.cmd.Wait()
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

// readLoop reads the PTY until it closes or ctx is cancelled, emitting output.
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

	// Natural exit: readLoop mirrors Stop's teardown — Stop cannot reclaim
	// the fd/context for a session it did not remove itself. An explicit
	// Stop/StopAll removed the map entry first, so a missing or mismatched
	// session stays silent: marker and onExit would kill a healthy replacement.
	// See specs/domains/terminal.md (Natural-exit protocol).
	m.mu.Lock()
	sess, exists := m.sessions[sessionID]
	naturalExit := exists && sess.ptmx == ptmx
	if naturalExit {
		delete(m.sessions, sessionID)
	}
	m.mu.Unlock()

	if naturalExit {
		m.teardown(sess, sessionID)
		m.emit(sessionID, []byte("\r\n\x1b[31m[Terminal session ended]\x1b[0m\r\n"))
		m.logger.Info("terminal process exited", "session_id", sessionID)
		m.onExit(sessionID)
	}
}
