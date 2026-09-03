package sysproc

import (
	"context"
	"os"
	"path/filepath"
	"slices"
	"strings"
	"testing"
)

// wantSafeHooksDir computes the hooksPath value GitCmd is expected to pass,
// derived the same way resolveGitSafeHooksDir derives it (process-wide, so
// the sync.Once cache is consistent with this computation as long as tests
// do not touch HOME).
func wantSafeHooksDir(t *testing.T) string {
	t.Helper()
	home, err := os.UserHomeDir()
	if err != nil {
		t.Fatalf("resolving home dir: %v", err)
	}
	return filepath.Join(home, DefaultAgentDirName, GitSafeHooksSegment)
}

func TestGitCmdPrependsSafetyOverrides(t *testing.T) {
	cmd := GitCmd(context.Background(), "status", "--porcelain", "-uall")
	want := []string{
		gitBinary,
		"-c", "core.fsmonitor=false",
		"-c", "core.hooksPath=" + wantSafeHooksDir(t),
		"-c", "commit.gpgsign=false",
		"status", "--porcelain", "-uall",
	}
	if !slices.Equal(cmd.Args, want) {
		t.Errorf("GitCmd argv = %q, want %q", cmd.Args, want)
	}
}

func TestGitCmdTailMatchesUnhardenedArgv(t *testing.T) {
	layerArgs := []string{"-C", filepath.Join(t.TempDir(), "repo"), "diff", "HEAD"}
	cmd := GitCmd(context.Background(), layerArgs...)

	// The layer's arguments survive unmodified after the override block,
	// matching the unhardened argv minus its leading binary name.
	unhardened := UnhardenedGitArgv(layerArgs...)
	gotTail := cmd.Args[len(cmd.Args)-len(layerArgs):]
	if !slices.Equal(gotTail, unhardened[1:]) {
		t.Errorf("hardened argv tail = %q, want layer args %q (from unhardened argv %q)", gotTail, unhardened[1:], unhardened)
	}
	if cmd.Args[0] != gitBinary {
		t.Errorf("argv[0] = %q, want %q", cmd.Args[0], gitBinary)
	}
	if want := len(layerArgs) + len(gitSafetyOverrides()) + 1; len(cmd.Args) != want {
		t.Errorf("hardened argv length = %d, want %d (nothing dropped or duplicated)", len(cmd.Args), want)
	}
}

func TestGitCmdEnvPreservesParentAndForcesEditor(t *testing.T) {
	t.Setenv("C0WRK_SYSPROC_TEST_SENTINEL", "1")
	// An inherited GIT_EDITOR must be REPLACED, not appended after: with
	// duplicate entries glibc's getenv (Linux) resolves to the first
	// occurrence, so appending on top would void the pin.
	t.Setenv("GIT_EDITOR", "inherited-editor")
	cmd := GitCmd(context.Background(), "log", "--oneline")

	foundSentinel, editorEntries := false, 0
	for _, e := range cmd.Env {
		switch {
		case e == "C0WRK_SYSPROC_TEST_SENTINEL=1":
			foundSentinel = true
		case e == gitEditorEnv:
			editorEntries++
		case strings.HasPrefix(e, gitEditorEnvVar+"="):
			t.Errorf("cmd.Env still carries an inherited %s entry: %q", gitEditorEnvVar, e)
		}
	}
	if !foundSentinel {
		t.Error("cmd.Env lost the parent environment (os.Environ not preserved)")
	}
	if editorEntries != 1 {
		t.Errorf("cmd.Env carries %d %s entries, want exactly 1", editorEntries, gitEditorEnv)
	}
}

func TestGitCmdCreatesSafeHooksDir(t *testing.T) {
	GitCmd(context.Background()) // trigger the sync.Once resolution
	dir := wantSafeHooksDir(t)
	st, err := os.Stat(dir)
	if err != nil {
		t.Fatalf("safe hooks dir %s not created: %v", dir, err)
	}
	if !st.IsDir() {
		t.Errorf("safe hooks path %s is not a directory", dir)
	}
}
