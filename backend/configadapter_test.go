package backend

import (
	"path/filepath"
	"testing"
)

// TestAgentsMDSearchPaths verifies that agentsMDSearchPaths resolves the global
// and c0wrk-specific AGENTS.md paths relative to the user home directory in the
// documented priority order (global first, c0wrk second).
func TestAgentsMDSearchPaths(t *testing.T) {
	// os.UserHomeDir reads $HOME on Unix and %USERPROFILE% on Windows
	// (not %HOME%); override both to a temp dir for a deterministic result
	// on every platform Go supports.
	tmpHome := t.TempDir()
	t.Setenv("HOME", tmpHome)
	t.Setenv("USERPROFILE", tmpHome)

	got := agentsMDSearchPaths()
	want := []string{
		filepath.Join(tmpHome, ".agents", "AGENTS.md"),
		filepath.Join(tmpHome, ".c0wrk", ".agents", "AGENTS.md"),
	}
	if len(got) != len(want) {
		t.Fatalf("expected %d paths, got %d: %v", len(want), len(got), got)
	}
	for i, w := range want {
		if got[i] != w {
			t.Errorf("path[%d]: got %q, want %q", i, got[i], w)
		}
	}
}

// TestAgentsMDSearchPaths_NoHomeDir verifies that a failure to resolve the home
// directory yields nil (no search paths) rather than panicking.
func TestAgentsMDSearchPaths_NoHomeDir(t *testing.T) {
	// Unset HOME and USERPROFILE so os.UserHomeDir returns an error.
	t.Setenv("HOME", "")
	t.Setenv("USERPROFILE", "")

	got := agentsMDSearchPaths()
	if got != nil {
		t.Errorf("expected nil when home dir is unavailable, got %v", got)
	}
}
