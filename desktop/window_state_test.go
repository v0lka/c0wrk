package desktop

import (
	"context"
	"log/slog"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/v0lka/c0wrk/backend/config"
)

func TestLoadWindowBounds_MissingFileReturnsDefaults(t *testing.T) {
	agentDir := t.TempDir()
	got := LoadWindowBounds(agentDir)
	want := defaultWindowBounds()
	if got != want {
		t.Fatalf("expected defaults %+v, got %+v", want, got)
	}
}

func TestLoadWindowBounds_MalformedFileReturnsDefaults(t *testing.T) {
	agentDir := t.TempDir()
	if err := os.WriteFile(config.WindowStatePath(agentDir), []byte("{not json"), 0o600); err != nil {
		t.Fatalf("write: %v", err)
	}
	got := LoadWindowBounds(agentDir)
	if got != defaultWindowBounds() {
		t.Fatalf("expected defaults for malformed file, got %+v", got)
	}
}

func TestLoadWindowBounds_OutOfRangeFallsBackToDefaults(t *testing.T) {
	agentDir := t.TempDir()
	// Width/height below the minimum must be rejected in favor of defaults.
	if err := writeWindowBounds(agentDir, WindowBounds{Width: 100, Height: 100, Maximized: true}); err != nil {
		t.Fatalf("write: %v", err)
	}
	got := LoadWindowBounds(agentDir)
	if got.Width != defaultWindowWidth || got.Height != defaultWindowHeight {
		t.Fatalf("expected default dimensions, got width=%d height=%d", got.Width, got.Height)
	}
	// Maximized is adopted regardless of dimension validity.
	if !got.Maximized {
		t.Fatalf("expected maximized flag to be preserved")
	}
}

func TestLoadWriteWindowBounds_RoundTrip(t *testing.T) {
	agentDir := t.TempDir()
	want := WindowBounds{Width: 1600, Height: 1000, Maximized: false}
	if err := writeWindowBounds(agentDir, want); err != nil {
		t.Fatalf("write: %v", err)
	}
	got := LoadWindowBounds(agentDir)
	if got != want {
		t.Fatalf("round-trip mismatch: want %+v, got %+v", want, got)
	}
}

func TestWindowStatePath_UnderAgentDir(t *testing.T) {
	agentDir := filepath.Join("home", ".c0wrk")
	got := config.WindowStatePath(agentDir)
	want := filepath.Join(agentDir, "window_state.json")
	if got != want {
		t.Fatalf("expected %q, got %q", want, got)
	}
}

func TestDialogStatePath_UnderAgentDir(t *testing.T) {
	agentDir := filepath.Join("home", ".c0wrk")
	got := config.DialogStatePath(agentDir)
	want := filepath.Join(agentDir, "dialog_state.json")
	if got != want {
		t.Fatalf("expected %q, got %q", want, got)
	}
}

// TestWriteWindowBounds_ReplacesSymlinkInsteadOfTarget pins the #65 fix: a
// symlink planted at the fixed window_state.json path must be REPLACED by the
// atomic rename, never written through — the link target keeps its contents
// and the state file ends up a regular file at the expected path.
func TestWriteWindowBounds_ReplacesSymlinkInsteadOfTarget(t *testing.T) {
	agentDir := t.TempDir()
	victimDir := t.TempDir()
	victim := filepath.Join(victimDir, "authorized_keys")
	if err := os.WriteFile(victim, []byte("keep me"), 0o600); err != nil {
		t.Fatalf("seed victim: %v", err)
	}
	path := config.WindowStatePath(agentDir)
	if err := os.Symlink(victim, path); err != nil {
		t.Skipf("symlinks unavailable on this platform: %v", err)
	}

	if err := writeWindowBounds(agentDir, WindowBounds{Width: 1600, Height: 1000}); err != nil {
		t.Fatalf("writeWindowBounds: %v", err)
	}

	data, err := os.ReadFile(victim)
	if err != nil {
		t.Fatalf("read victim: %v", err)
	}
	if string(data) != "keep me" {
		t.Fatalf("symlink target was overwritten: %q", data)
	}
	fi, err := os.Lstat(path)
	if err != nil {
		t.Fatalf("lstat state file: %v", err)
	}
	if fi.Mode()&os.ModeSymlink != 0 {
		t.Fatal("state file is still a symlink; the rename did not replace it")
	}
	if !fi.Mode().IsRegular() {
		t.Fatalf("state file is not a regular file: %v", fi.Mode())
	}
}

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
	// The fresh temp home has no agent dir yet; writeWindowBounds stages the
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

	got := LoadWindowBounds(agentDir)
	if got.Width != 1600 || got.Height != 1000 {
		t.Fatalf("persisted bounds = %+v, want the live snapshot 1600x1000", got)
	}
}
