package desktop

import (
	"context"
	"log/slog"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/v0lka/c0wrk/backend"

	"github.com/v0lka/c0wrk/backend/config"
)

// TestPersistWindowBoundsAtShutdown_PrefersSnapshot pins the #80 fix: the
// shutdown persist must use the geometry captured while the window was alive
// (the close-guard snapshot) and must not read the already-destroyed window —
// on Linux a post-destroy read returns the creation default and clobbers the
// value the debounced resize persist wrote.
func TestPersistWindowBoundsAtShutdown_PrefersSnapshotOverDestroyedWindow(t *testing.T) {
	tmp := t.TempDir()
	t.Setenv("HOME", tmp)
	t.Setenv("USERPROFILE", tmp)
	agentDir := config.AgentDir()
	if !filepath.IsAbs(agentDir) || !strings.HasPrefix(agentDir, tmp) {
		t.Skipf("agent dir not redirected to the temp home on this platform: %q", agentDir)
	}
	// The fresh temp home has no agent dir yet; the backend write stages the
	// temp file in the target directory, so it must exist.
	if err := os.MkdirAll(agentDir, 0o750); err != nil {
		t.Fatalf("mkdir agent dir: %v", err)
	}

	a := NewApp()
	a.setContext(context.Background())
	// Geometry reads while the window is alive: the user resized to 1600x1000.
	a.windowGetSizeFn = func(context.Context) (int, int) { return 1600, 1000 }
	a.windowIsMaximisedFn = func(context.Context) bool { return false }
	a.captureWindowGeometry()

	// The window is destroyed by the time OnShutdown runs (Linux): any live
	// read now would return the creation default — fail the test if one
	// happens, instead of silently asserting against a clobbered file.
	a.windowGetSizeFn = func(context.Context) (int, int) {
		t.Error("geometry read from the destroyed window at shutdown")
		return 1400, 900
	}
	a.windowIsMaximisedFn = func(context.Context) bool { return false }

	// testLogger lives in the linux-only notifications test file, so the
	// logger is built inline to keep this file platform-independent; its
	// records carry no signal for these assertions.
	a.persistWindowBoundsAtShutdown(slog.New(slog.DiscardHandler))

	got := backend.LoadWindowBounds(agentDir)
	if got.Width != 1600 || got.Height != 1000 {
		t.Fatalf("persisted bounds = %+v, want the live snapshot 1600x1000", got)
	}
}
