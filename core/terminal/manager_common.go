package terminal

import (
	"context"
	"log/slog"
	"os"
	"strings"
	"sync"
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
}

// buildTermEnv returns the current process environment with terminal-specific
// variables injected. xterm.js is an xterm-compatible terminal with 256-color
// (and true-color) support, so we set TERM=xterm-256color and COLORTERM=truecolor.
func buildTermEnv() []string {
	// Force a copy to avoid sharing the backing array with os.Environ().
	raw := os.Environ()
	env := make([]string, 0, len(raw)+2)
	env = append(env, raw...)
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
