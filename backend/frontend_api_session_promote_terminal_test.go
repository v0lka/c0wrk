//go:build !windows

package backend

import (
	"context"
	"log/slog"
	"os"
	"testing"

	"github.com/v0lka/c0wrk/core/terminal"
)

// promoteTerminalManager builds a real PTY-backed terminal manager with the
// same logger discipline core/terminal's tests use: routine lifecycle records
// are below the level, so only genuine errors reach the test output.
func promoteTerminalManager(t *testing.T) *terminal.Manager {
	t.Helper()
	logger := slog.New(slog.NewTextHandler(os.Stderr, &slog.HandlerOptions{Level: slog.LevelError}))
	ctx, cancel := context.WithCancel(context.Background())
	t.Cleanup(cancel)
	tm := terminal.NewManager(ctx, logger, func(string, []byte) {}, nil, nil)
	t.Cleanup(tm.StopAll)
	return tm
}

func TestPromoteSessionToProject_EmitsTerminalExitedWhenTerminalStopped(t *testing.T) {
	h := newPromoteTestAPI(t)
	api, sessionStore, agentDir, events := h.api, h.sessions, h.agentDir, h.events
	seedPromotableChatSession(t, sessionStore, agentDir, "s-term")

	// A live PTY for the session: the promotion stops it (its cwd sits inside
	// the moved tree) and must tell the frontend — the xterm instance
	// survives the promotion, and terminal_exited is the one explicit-stop
	// exception that arms the lazy resurrection in the new workspace.
	tm := promoteTerminalManager(t)
	api.terminalManager = tm
	if err := tm.Start("s-term", agentDir); err != nil {
		t.Fatalf("start terminal: %v", err)
	}

	if _, err := api.PromoteSessionToProject("s-term", "Terminal Project"); err != nil {
		t.Fatalf("PromoteSessionToProject failed: %v", err)
	}
	if !events.has("session:s-term:terminal_exited") {
		t.Fatal("promotion of a session with a live terminal must emit terminal_exited")
	}
	if tm.IsActive("s-term") {
		t.Fatal("the promotion must have stopped the session terminal")
	}
}

func TestPromoteSessionToProject_NoTerminalNoTerminalExitedEvent(t *testing.T) {
	h := newPromoteTestAPI(t)
	api, sessionStore, agentDir, events := h.api, h.sessions, h.agentDir, h.events
	seedPromotableChatSession(t, sessionStore, agentDir, "s-noterm")

	if _, err := api.PromoteSessionToProject("s-noterm", "Quiet Project"); err != nil {
		t.Fatalf("PromoteSessionToProject failed: %v", err)
	}
	if events.has("session:s-noterm:terminal_exited") {
		t.Fatal("a session without a terminal must not emit terminal_exited")
	}
}
