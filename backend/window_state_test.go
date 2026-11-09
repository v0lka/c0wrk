package backend

import (
	"os"
	"path/filepath"
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

// TestWriteResolvedWindowBounds_MaximizedPreservesRect pins the maximized
// rule: while maximized, only the Maximized flag updates and the last
// non-maximized Width/Height is preserved, so un-maximizing restores the
// user's chosen rectangle.
func TestWriteResolvedWindowBounds_MaximizedPreservesRect(t *testing.T) {
	agentDir := t.TempDir()
	prev := WindowBounds{Width: 1600, Height: 1000}
	if err := writeWindowBounds(agentDir, prev); err != nil {
		t.Fatalf("seed prev: %v", err)
	}

	writeResolvedWindowBounds(agentDir, WindowBounds{Width: 2560, Height: 1440, Maximized: true}, testLogger())

	got := LoadWindowBounds(agentDir)
	if !got.Maximized {
		t.Fatal("expected the maximized flag to be persisted")
	}
	if got.Width != 1600 || got.Height != 1000 {
		t.Fatalf("persisted bounds = %+v, want the pre-maximize rectangle 1600x1000", got)
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
