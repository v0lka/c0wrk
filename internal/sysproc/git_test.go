package sysproc

import (
	"context"
	"os"
	"path/filepath"
	"slices"
	"strings"
	"sync"
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
	cmd, err := GitCmd(context.Background(), "status", "--porcelain", "-uall")
	if err != nil {
		t.Fatalf("GitCmd: %v", err)
	}
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
	cmd, err := GitCmd(context.Background(), layerArgs...)
	if err != nil {
		t.Fatalf("GitCmd: %v", err)
	}

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
	overrides, err := gitSafetyOverrides()
	if err != nil {
		t.Fatalf("gitSafetyOverrides: %v", err)
	}
	if want := len(layerArgs) + len(overrides) + 1; len(cmd.Args) != want {
		t.Errorf("hardened argv length = %d, want %d (nothing dropped or duplicated)", len(cmd.Args), want)
	}
}

func TestGitCmdEnvPreservesParentAndForcesEditor(t *testing.T) {
	t.Setenv("C0WRK_SYSPROC_TEST_SENTINEL", "1")
	// An inherited GIT_EDITOR must be REPLACED, not appended after: with
	// duplicate entries glibc's getenv (Linux) resolves to the first
	// occurrence, so appending on top would void the pin.
	t.Setenv("GIT_EDITOR", "inherited-editor")
	cmd, err := GitCmd(context.Background(), "log", "--oneline")
	if err != nil {
		t.Fatalf("GitCmd: %v", err)
	}

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

func TestGitCmdEnvStripsAttributeEnvVars(t *testing.T) {
	// GIT_ATTR_SOURCE (documented, git 2.50.1) redirects where git reads
	// attributes from — the same knob as attr.tree, so an inherited value
	// could reroute the neutralizing empty tree; GIT_ATTR_SYSTEM and
	// GIT_ATTR_GLOBAL name additional attributes files. The whole GIT_ATTR_*
	// family must be stripped from every spawned git process.
	t.Setenv("GIT_ATTR_SOURCE", "HEAD~1")
	t.Setenv("GIT_ATTR_SYSTEM", "/tmp/evil-attrs")
	t.Setenv("GIT_ATTR_GLOBAL", "/tmp/evil-attrs")
	t.Setenv("GIT_ATTR_FUTURE_KNOB", "1") // prefix strip covers additions
	cmd, err := GitCmd(context.Background(), "check-attr", "filter", "--", "file.txt")
	if err != nil {
		t.Fatalf("GitCmd: %v", err)
	}
	for _, e := range cmd.Env {
		if strings.HasPrefix(e, "GIT_ATTR_") {
			t.Errorf("cmd.Env still carries an attribute environment entry: %q", e)
		}
	}
	// The rest of the hardening (GIT_EDITOR pin) is untouched.
	if !slices.Contains(cmd.Env, gitEditorEnv) {
		t.Errorf("cmd.Env lost the %s pin", gitEditorEnv)
	}
}

func TestGitCmdCreatesSafeHooksDir(t *testing.T) {
	if _, err := GitCmd(context.Background()); err != nil { // trigger the sync.Once resolution
		t.Fatalf("GitCmd: %v", err)
	}
	dir := wantSafeHooksDir(t)
	st, err := os.Stat(dir)
	if err != nil {
		t.Fatalf("safe hooks dir %s not created: %v", dir, err)
	}
	if !st.IsDir() {
		t.Errorf("safe hooks path %s is not a directory", dir)
	}
}

// resetSafeHooksCacheForTest drops the process-wide sync.Once resolution
// cache so a test can observe a fresh resolution under a manipulated
// environment, and restores a clean cache afterwards.
func resetSafeHooksCacheForTest(t *testing.T) {
	t.Helper()
	reset := func() {
		gitSafeHooksOnce = sync.Once{}
		gitSafeHooksDir = ""
		gitSafeHooksErr = nil
	}
	reset()
	t.Cleanup(reset)
}

// TestGitCmdRefusesSpawnOnUnresolvableHome pins review [42]: when the home
// directory cannot be resolved there is no safe absolute core.hooksPath —
// the old "." fallback produced a RELATIVE hooksPath that git resolved
// inside the repository, letting a planted .c0wrk/git/safe-hooks/pre-commit
// become the "safe" hook. GitCmd must fail closed: no command, an error.
func TestGitCmdRefusesSpawnOnUnresolvableHome(t *testing.T) {
	// Empty (not just unset) env vars make os.UserHomeDir fail on every
	// platform (unix: $HOME; windows: USERPROFILE / HOMEDRIVE+HOMEPATH).
	t.Setenv("HOME", "")
	t.Setenv("USERPROFILE", "")
	t.Setenv("HOMEDRIVE", "")
	t.Setenv("HOMEPATH", "")
	resetSafeHooksCacheForTest(t)

	cmd, err := GitCmd(context.Background(), "status")
	if err == nil {
		t.Fatal("expected GitCmd to refuse the spawn when home is unresolvable")
	}
	if cmd != nil {
		t.Errorf("expected a nil cmd on refusal, got %+v", cmd)
	}
	if !strings.Contains(err.Error(), "fail closed") {
		t.Errorf("error should state the fail-closed direction, got: %v", err)
	}
}

// TestGitCmdRawSkipsOverridesAndHardening pins the raw escape hatch: argv is
// exactly [git, <args...>] (no -c core.fsmonitor/hooksPath/gpgsign baseline,
// no NeutralizingArgv) and cmd.Env is nil (inherit the parent environment —
// no GIT_EDITOR pin, no GIT_ATTR_* strip), the opposite of GitCmd.
func TestGitCmdRawSkipsOverridesAndHardening(t *testing.T) {
	layerArgs := []string{"-C", filepath.Join(t.TempDir(), "repo"), "diff", "HEAD"}
	cmd := GitCmdRaw(context.Background(), layerArgs...)

	want := append([]string{gitBinary}, layerArgs...)
	if !slices.Equal(cmd.Args, want) {
		t.Errorf("GitCmdRaw argv = %q, want %q (no -c overrides)", cmd.Args, want)
	}

	// The raw path must leave cmd.Env nil: nil inherits the parent
	// environment untouched, so any inherited GIT_EDITOR / GIT_ATTR_* stays
	// effective and no GIT_EDITOR=true pin is appended — the inverse of
	// GitCmd's hardenedGitEnv. A non-nil Env here would prove hardening
	// leaked into the raw path.
	if cmd.Env != nil {
		t.Errorf("GitCmdRaw must leave cmd.Env nil (inherit parent env), got %d entries: %q", len(cmd.Env), cmd.Env)
	}
}
