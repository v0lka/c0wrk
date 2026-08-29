package terminal

import (
	"context"
	"log/slog"
	"os"
	"strings"
	"sync"

	"github.com/v0lka/c0wrk/core/version"
)

// Manager owns shell session instances keyed by session ID. The concrete
// struct fields shared across platforms (Unix PTY and Windows ConPTY) are
// defined here; the platform-specific Session types are declared in the
// respective manager_{unix,windows}.go files.
type Manager struct {
	mu       sync.Mutex
	rootCtx  context.Context // app lifecycle context; cancelled on shutdown
	sessions map[string]*Session
	logger   *slog.Logger
	emit     func(sessionID string, data []byte)
	// onExit, when set, is invoked when a session's shell process exits on
	// its own (user types `exit`, shell crash). It is NOT invoked for explicit
	// Stop/StopAll teardown (session deletion, app shutdown, StartTerminalInDir
	// restarts) — those paths already have a defined follow-up state.
	onExit func(sessionID string)
	// userEnv holds user-configured extra environment variables (config
	// `terminal.env`) set on every shell process. Values win over inherited
	// environment entries and over the built-in defaults in buildTermEnv.
	userEnv map[string]string
}

// buildTermEnv returns the current process environment with terminal-specific
// variables injected. xterm.js is an xterm-compatible terminal with 256-color
// (and true-color) support, so we set TERM=xterm-256color and COLORTERM=truecolor.
//
// TERM_PROGRAM and TERM_PROGRAM_VERSION are force-set to c0wrk, overriding any
// inherited values: they identify the terminal emulator the shell actually runs
// in — xterm.js inside c0wrk — not the terminal the app happened to be launched
// from. This is the de-facto TERM_PROGRAM convention (vscode, WezTerm, ghostty
// set it the same way): shell rc files use it to detect embedded terminals and
// skip "own the terminal" behaviors such as tmux auto-attach.
//
// extra (config `terminal.env`) is applied last and wins over both the
// inherited environment and the built-in defaults, letting users override
// anything (e.g. their own terminal marker for rc-file guards).
func buildTermEnv(extra map[string]string) []string {
	// Copy os.Environ(), dropping entries that are force-set below, so the
	// backing array is never shared with the process environment and no
	// duplicate keys remain in the child env.
	raw := os.Environ()
	env := make([]string, 0, len(raw)+4+2*len(extra))
	hasTerm := false
	hasColorterm := false
	for _, e := range raw {
		switch {
		case strings.HasPrefix(e, "TERM_PROGRAM="), strings.HasPrefix(e, "TERM_PROGRAM_VERSION="):
			continue // force-set below, never inherited
		case strings.HasPrefix(e, "TERM="):
			hasTerm = true
		case strings.HasPrefix(e, "COLORTERM="):
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

	// User configuration wins: replace any existing entry for each key
	// (inherited or built-in default) with the configured value.
	for k, v := range extra {
		if k == "" {
			continue
		}
		prefix := k + "="
		next := make([]string, 0, len(env)+1)
		for _, e := range env {
			if !strings.HasPrefix(e, prefix) {
				next = append(next, e)
			}
		}
		next = append(next, prefix+v)
		env = next
	}
	return env
}
