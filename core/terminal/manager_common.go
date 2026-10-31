package terminal

import (
	"context"
	"log/slog"
	"os"
	"strings"
	"sync"

	"github.com/v0lka/c0wrk/core/version"
)

// Manager owns shell session instances keyed by session ID. Fields shared by
// both platform builds live here; the platform-specific Session types are
// declared in manager.go (Unix PTY) and manager_windows.go (ConPTY).
type Manager struct {
	mu       sync.Mutex
	rootCtx  context.Context // app lifecycle context; cancelled on shutdown
	sessions map[string]*Session
	logger   *slog.Logger
	emit     func(sessionID string, data []byte)
	// onExit, when set, fires only on natural shell exit (user `exit`, crash);
	// explicit Stop/StopAll teardown (deletion, shutdown, StartTerminalInDir
	// restarts) must not fire it — those paths own their follow-up state.
	onExit func(sessionID string)
	// userEnv holds user-configured extra env (config `terminal.env`) set on
	// every shell process; values win over inherited entries and the built-in
	// defaults in buildTermEnv.
	userEnv map[string]string
}

// buildTermEnv returns the process environment with the embedded-terminal
// variables applied: TERM/COLORTERM fill-only-if-missing, TERM_PROGRAM
// force-set to c0wrk (shell rc files detect the in-app terminal and skip
// own-the-terminal behaviors like tmux auto-attach), and extra (config
// `terminal.env`) winning over everything else.
// See specs/decisions/029-terminal-env-conventions.md.
func buildTermEnv(extra map[string]string) []string {
	// Copy os.Environ() minus the force-set entries: the backing array must
	// not alias the process environment, and the child env stays duplicate-free.
	raw := os.Environ()
	env := make([]string, 0, len(raw)+4+2*len(extra))
	hasTerm := false
	hasColorterm := false
	for _, e := range raw {
		name, _, _ := strings.Cut(e, "=")
		switch {
		case sameEnvKey(name, "TERM_PROGRAM"), sameEnvKey(name, "TERM_PROGRAM_VERSION"):
			continue // force-set below, never inherited
		case sameEnvKey(name, "TERM"):
			hasTerm = true
		case sameEnvKey(name, "COLORTERM"):
			hasColorterm = true
		}
		env = append(env, e)
	}
	if !hasTerm {
		env = append(env, "TERM=xterm-256color")
	}
	if !hasColorterm {
		env = append(env, "COLORTERM=truecolor")
	}
	env = append(env, "TERM_PROGRAM=c0wrk", "TERM_PROGRAM_VERSION="+version.Version)

	for k, v := range extra {
		if k == "" {
			continue
		}
		next := make([]string, 0, len(env)+1)
		for _, e := range env {
			name, _, _ := strings.Cut(e, "=")
			if sameEnvKey(name, k) {
				continue
			}
			next = append(next, e)
		}
		next = append(next, k+"="+v)
		env = next
	}
	return env
}
