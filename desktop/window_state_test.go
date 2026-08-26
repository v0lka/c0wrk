package desktop

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
