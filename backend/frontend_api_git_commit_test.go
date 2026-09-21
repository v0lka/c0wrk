package backend

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/v0lka/c0wrk/core/gittrust"
	"github.com/v0lka/c0wrk/internal/gittest"
)

// Suppression-aware Commit tests ────────────────────────────────────────────
//
// The Commit RPC withholds a commit in an UNTRUSTED repository that arms
// commit hooks or signing (CommitResult.Suppressed describes what would
// have run), commits hardened under an explicit force, and goes straight
// to raw git (hooks execute, output captured) for trusted repositories.
//
// HOME is isolated in every test that asserts the clean/suppressed
// baseline: DetectCommitSuppression also reads the developer's global git
// config, and a machine with commit.gpgsign=true globally would otherwise
// flip every "clean" case into a suppression.

// isolateHomeForCommit redirects HOME to a temp dir so the developer's real
// ~/.gitconfig cannot arm global signing during the test.
func isolateHomeForCommit(t *testing.T) string {
	t.Helper()
	home := t.TempDir()
	t.Setenv("HOME", home)
	return home
}

// plantCommitHook installs an executable pre-commit hook in the repository's
// default hooks dir. The hook prints hookLine to stdout/stderr (so the
// commit's combined Output can be asserted) and records its execution in
// markerPath. Returns the marker path.
func plantCommitHook(t *testing.T, repoRoot, hookLine, markerPath string) {
	t.Helper()
	hookPath := filepath.Join(repoRoot, ".git", "hooks", "pre-commit")
	if err := os.MkdirAll(filepath.Dir(hookPath), 0o755); err != nil {
		t.Fatalf("mkdir hooks: %v", err)
	}
	body := "#!/bin/sh\necho " + hookLine + "\nprintf 'FIRED\\n' >> " + markerPath + "\nexit 0\n"
	if err := os.WriteFile(hookPath, []byte(body), 0o755); err != nil {
		t.Fatalf("write pre-commit hook: %v", err)
	}
}

// commitCount returns the repository's commit count via rev-list.
func commitCount(t *testing.T, repoRoot string) string {
	t.Helper()
	return gitOut(t, repoRoot, "rev-list", "--count", "HEAD")
}

// TestCommit_SuppressedUntrustedHook pins the core suppression contract: an
// untrusted repository with an armed pre-commit hook and force=false gets NO
// commit (nothing spawned at all — the hook cannot run), a nil error, and a
// Suppressed payload naming the hook. No status event fires for a withheld
// commit.
func TestCommit_SuppressedUntrustedHook(t *testing.T) {
	gittest.RequirePOSIXShell(t)
	isolateHomeForCommit(t)

	root := filepath.Join(t.TempDir(), "repo")
	repo := gittest.InitRepo(t, root, "hello\n")
	marker := filepath.Join(t.TempDir(), "hook-fired")
	plantCommitHook(t, root, "hook-line", marker)

	repo.Write(t, "file.txt", "hello\nchanged\n")
	repo.Git(t, "add", ".")

	f := &FrontendAPI{activeProjectPath: root} // no config → untrusted (fail-closed)
	events := 0
	f.emitEvent = func(name string, _ ...any) {
		if name == EventGitStatusChanged {
			events++
		}
	}

	res, err := f.Commit("suppressed commit", false)
	if err != nil {
		t.Fatalf("Commit (suppressed): unexpected error: %v", err)
	}
	if res.Suppressed == nil {
		t.Fatalf("Commit: expected Suppressed for an untrusted repo with an armed hook, got %+v", res)
	}
	if len(res.Suppressed.Hooks) != 1 || res.Suppressed.Hooks[0] != "pre-commit" {
		t.Errorf("Suppressed.Hooks = %v, want [pre-commit]", res.Suppressed.Hooks)
	}
	if res.Suppressed.SigningRepo || res.Suppressed.SigningGlobal {
		t.Errorf("Suppressed signing flags = repo:%v global:%v, want both false for a clean config", res.Suppressed.SigningRepo, res.Suppressed.SigningGlobal)
	}
	if res.Sha != "" || res.Output != "" {
		t.Errorf("withheld commit must return empty Sha/Output, got %q/%q", res.Sha, res.Output)
	}

	// No commit was created and the hook never executed.
	if got := commitCount(t, root); got != "1" {
		t.Errorf("rev-list count = %s, want 1 (no commit created)", got)
	}
	if _, err := os.Stat(marker); !os.IsNotExist(err) {
		t.Error("pre-commit hook executed during a withheld commit")
	}
	if events != 0 {
		t.Errorf("git:status_changed fired %d times for a withheld commit, want 0", events)
	}
}

// TestCommit_ForceCommitsHardened pins the force override: force=true commits
// through the hardened baseline — the commit EXISTS, the armed hook still
// does NOT run (neutralized), and Suppressed stays nil.
func TestCommit_ForceCommitsHardened(t *testing.T) {
	gittest.RequirePOSIXShell(t)
	isolateHomeForCommit(t)

	root := filepath.Join(t.TempDir(), "repo")
	repo := gittest.InitRepo(t, root, "hello\n")
	marker := filepath.Join(t.TempDir(), "hook-fired")
	plantCommitHook(t, root, "hook-line", marker)

	repo.Write(t, "file.txt", "hello\nchanged\n")
	repo.Git(t, "add", ".")

	f := &FrontendAPI{activeProjectPath: root} // untrusted

	res, err := f.Commit("forced commit", true)
	if err != nil {
		t.Fatalf("Commit (force): %v", err)
	}
	if res.Suppressed != nil {
		t.Fatalf("Commit (force): Suppressed must be nil, got %+v", res.Suppressed)
	}
	if res.Sha == "" {
		t.Fatal("Commit (force): expected a commit SHA")
	}
	if got := commitCount(t, root); got != "2" {
		t.Errorf("rev-list count = %s, want 2 (forced commit created)", got)
	}
	if _, err := os.Stat(marker); !os.IsNotExist(err) {
		t.Error("forced hardened commit must still neutralize the armed pre-commit hook")
	}
}

// TestCommit_TrustedRepoRunsHooks is the integration proof of the trusted
// path (the backend twin of core's git_canary_trust_test.go): after
// TrustGitRepo, a commit EXECUTES the repository's own pre-commit hook
// (marker fires), the hook's output lands in CommitResult.Output, the commit
// completes, and Suppressed stays nil. POSIX-only (shell hook).
func TestCommit_TrustedRepoRunsHooks(t *testing.T) {
	gittest.RequirePOSIXShell(t)
	gittrust.Clear()
	t.Cleanup(gittrust.Clear)
	isolateHomeForCommit(t)

	root := filepath.Join(t.TempDir(), "repo")
	repo := gittest.InitRepo(t, root, "hello\n")
	marker := filepath.Join(t.TempDir(), "hook-fired")
	plantCommitHook(t, root, "HOOK-STDOUT-LINE", marker)

	f, _, _ := newTestAPI(t)
	f.activeProjectPath = root
	if err := f.TrustGitRepo(root); err != nil {
		t.Fatalf("TrustGitRepo: %v", err)
	}
	if !gittrust.IsTrusted(root) {
		t.Fatal("TrustGitRepo did not register the root in the trust registry")
	}

	repo.Write(t, "file.txt", "hello\nchanged\n")
	repo.Git(t, "add", ".")

	res, err := f.Commit("trusted commit", false)
	if err != nil {
		t.Fatalf("Commit (trusted): %v", err)
	}
	if res.Suppressed != nil {
		t.Fatalf("Commit (trusted): Suppressed must be nil, got %+v", res.Suppressed)
	}
	if res.Sha == "" {
		t.Fatal("Commit (trusted): expected a commit SHA")
	}
	if got := commitCount(t, root); got != "2" {
		t.Errorf("rev-list count = %s, want 2 (trusted commit created)", got)
	}

	// The hook executed (raw git for a trusted repo) and its output was
	// captured into the combined Output.
	if _, err := os.Stat(marker); err != nil {
		t.Fatal("trusted repository must run its own pre-commit hook (trust = raw git)")
	}
	if !strings.Contains(res.Output, "HOOK-STDOUT-LINE") {
		t.Errorf("Commit Output must contain the hook's output, got: %q", res.Output)
	}
}

// TestCommit_CleanUntrustedUnchanged pins the passthrough: a clean untrusted
// repository (no hooks, no signing — HOME isolated so the developer's global
// config cannot interfere) commits exactly as before, with Suppressed nil
// and a valid SHA.
func TestCommit_CleanUntrustedUnchanged(t *testing.T) {
	gittest.RequirePOSIXShell(t)
	isolateHomeForCommit(t)

	withGitRepo(t, func(f *FrontendAPI, dir string) {
		path := filepath.Join(dir, "clean.txt")
		if err := os.WriteFile(path, []byte("content\n"), 0o644); err != nil {
			t.Fatalf("write: %v", err)
		}
		if err := f.StageFile(path); err != nil {
			t.Fatalf("StageFile: %v", err)
		}

		res, err := f.Commit("clean commit", false)
		if err != nil {
			t.Fatalf("Commit: %v", err)
		}
		if res.Suppressed != nil {
			t.Fatalf("clean untrusted repo must not be suppressed, got %+v", res.Suppressed)
		}
		if res.Sha == "" {
			t.Fatal("Commit: expected a commit SHA")
		}
		if got := commitCount(t, dir); got != "2" {
			t.Errorf("rev-list count = %s, want 2", got)
		}
	})
}

// TestGitCommitTimeout_ConfigAndDefault pins the timeout resolution used by
// the commit spawn: the configured timeouts.gitCommitTimeout (seconds) when
// positive, the 300s default otherwise (nil config or zero value).
func TestGitCommitTimeout_ConfigAndDefault(t *testing.T) {
	f := &FrontendAPI{}
	if got := f.gitCommitTimeout(); got != 300*time.Second {
		t.Errorf("gitCommitTimeout (nil config) = %v, want 300s", got)
	}

	f2, _, _ := newTestAPI(t)
	if got := f2.gitCommitTimeout(); got != 300*time.Second {
		t.Errorf("gitCommitTimeout (defaults) = %v, want 300s", got)
	}

	f2.config.Timeouts.GitCommitTimeout = 900
	if got := f2.gitCommitTimeout(); got != 900*time.Second {
		t.Errorf("gitCommitTimeout (override) = %v, want 900s", got)
	}
}

// TestCommit_TimeoutApplied proves the commit spawn actually runs under the
// configured GitCommitTimeout: a trusted repository (raw git, so its hook
// really executes) with a sleeping pre-commit hook and a 1s budget must fail
// fast — well before the hook's 10s sleep — and leave no commit behind.
func TestCommit_TimeoutApplied(t *testing.T) {
	gittest.RequirePOSIXShell(t)
	gittrust.Clear()
	t.Cleanup(gittrust.Clear)
	isolateHomeForCommit(t)

	root := filepath.Join(t.TempDir(), "repo")
	repo := gittest.InitRepo(t, root, "hello\n")

	hookPath := filepath.Join(root, ".git", "hooks", "pre-commit")
	if err := os.MkdirAll(filepath.Dir(hookPath), 0o755); err != nil {
		t.Fatalf("mkdir hooks: %v", err)
	}
	if err := os.WriteFile(hookPath, []byte("#!/bin/sh\nsleep 10\nexit 0\n"), 0o755); err != nil {
		t.Fatalf("write sleeping hook: %v", err)
	}

	f, _, _ := newTestAPI(t)
	f.activeProjectPath = root
	f.config.Timeouts.GitCommitTimeout = 1 // 1 second budget
	if err := f.TrustGitRepo(root); err != nil {
		t.Fatalf("TrustGitRepo: %v", err)
	}

	repo.Write(t, "file.txt", "hello\nchanged\n")
	repo.Git(t, "add", ".")

	start := time.Now()
	_, err := f.Commit("slow commit", false)
	elapsed := time.Since(start)
	if err == nil {
		t.Fatal("expected an error from a commit spawn that exceeds its 1s budget")
	}
	if elapsed >= 5*time.Second {
		t.Errorf("commit took %v to fail; the 1s GitCommitTimeout was not applied (default 300s?)", elapsed)
	}
	if got := commitCount(t, root); got != "1" {
		t.Errorf("rev-list count = %s, want 1 (timed-out commit must not land)", got)
	}
}

// --- limitedBuffer tests ---

func TestLimitedBuffer_CapsAt64KiBWithMarker(t *testing.T) {
	var b limitedBuffer
	chunk := strings.Repeat("a", 16*1024)
	for i := 0; i < 5; i++ { // 80 KiB total — crosses the 64 KiB cap mid-write
		if n, err := b.Write([]byte(chunk)); err != nil || n != len(chunk) {
			t.Fatalf("Write: n=%d err=%v", n, err)
		}
	}
	got := b.String()
	if !strings.HasSuffix(got, commitOutputTruncationMarker) {
		t.Error("expected the truncation marker at the end of the capped output")
	}
	if l := len(got); l > commitOutputLimit+len(commitOutputTruncationMarker) {
		t.Errorf("capped output length = %d, want <= %d", l, commitOutputLimit+len(commitOutputTruncationMarker))
	}
	if !strings.HasPrefix(got, strings.Repeat("a", 64*1024)) {
		t.Error("the first 64 KiB must be preserved verbatim")
	}
}

func TestLimitedBuffer_UnderCapNoMarker(t *testing.T) {
	var b limitedBuffer
	if _, err := b.Write([]byte("short output\n")); err != nil {
		t.Fatalf("Write: %v", err)
	}
	got := b.String()
	if got != "short output" {
		t.Errorf("String() = %q, want %q (trimmed, no marker)", got, "short output")
	}
}

// TestCommit_TrustedSubdirectoryWorkspace pins the work-tree-root trust
// attribution (review finding 1): a workspace opened at a SUBDIRECTORY of a
// trusted repository must commit as trusted — the gate must not withhold
// (the "Trust & commit" button would loop forever, since trust is stored
// for the root), and force must not silently run raw git through the gate's
// blind side. Both paths go through the same root-keyed decision.
func TestCommit_TrustedSubdirectoryWorkspace(t *testing.T) {
	gittest.RequirePOSIXShell(t)
	gittrust.Clear()
	t.Cleanup(gittrust.Clear)
	isolateHomeForCommit(t)

	root := filepath.Join(t.TempDir(), "repo")
	repo := gittest.InitRepo(t, root, "hello\n")
	sub := filepath.Join(root, "sub", "dir")
	if err := os.MkdirAll(sub, 0o755); err != nil {
		t.Fatalf("mkdir subdirectory: %v", err)
	}
	marker := filepath.Join(t.TempDir(), "hook-fired")
	plantCommitHook(t, root, "HOOK-STDOUT-LINE", marker)

	f, _, _ := newTestAPI(t)
	f.activeProjectPath = sub // workspace = subdirectory of the repository
	if err := f.TrustGitRepo(sub); err != nil {
		t.Fatalf("TrustGitRepo(subdirectory): %v", err)
	}
	// TrustGitRepo normalizes to the work-tree root; the commit must key on
	// the same form.
	if got := commitCount(t, root); got != "1" {
		t.Fatalf("setup: rev-list count = %s, want 1", got)
	}

	repo.Write(t, "file.txt", "hello\nchanged\n")
	repo.Git(t, "add", ".")

	// force=false on the trusted subdirectory workspace: the gate must NOT
	// withhold (Suppressed nil) — the commit runs raw through the trust.
	res, err := f.Commit("trusted subdir commit", false)
	if err != nil {
		t.Fatalf("Commit (trusted subdirectory): %v", err)
	}
	if res.Suppressed != nil {
		t.Fatalf("Commit (trusted subdirectory): Suppressed = %+v, want nil — the gate must honor root-keyed trust", res.Suppressed)
	}
	if res.Sha == "" {
		t.Fatal("Commit (trusted subdirectory): expected a commit SHA")
	}
	if _, err := os.Stat(marker); err != nil {
		t.Error("trusted subdirectory commit must run the repository's hook (raw git)")
	}
}

// TestCommit_UntrustedSubdirectorySuppresses is the negative twin: the same
// subdirectory workspace WITHOUT trust must still hit the gate (detection
// walks up to the root's hooks) and withhold.
func TestCommit_UntrustedSubdirectorySuppresses(t *testing.T) {
	gittest.RequirePOSIXShell(t)
	gittrust.Clear()
	t.Cleanup(gittrust.Clear)
	isolateHomeForCommit(t)

	root := filepath.Join(t.TempDir(), "repo")
	repo := gittest.InitRepo(t, root, "hello\n")
	sub := filepath.Join(root, "sub")
	if err := os.MkdirAll(sub, 0o755); err != nil {
		t.Fatalf("mkdir subdirectory: %v", err)
	}
	marker := filepath.Join(t.TempDir(), "hook-fired")
	plantCommitHook(t, root, "hook-line", marker)

	f := &FrontendAPI{activeProjectPath: sub} // untrusted, subdirectory workspace
	repo.Write(t, "file.txt", "hello\nchanged\n")
	repo.Git(t, "add", ".")

	res, err := f.Commit("untrusted subdir commit", false)
	if err != nil {
		t.Fatalf("Commit (untrusted subdirectory): %v", err)
	}
	if res.Suppressed == nil {
		t.Fatal("Commit (untrusted subdirectory): expected Suppressed — detection must see the root's hooks")
	}
	if got := commitCount(t, root); got != "1" {
		t.Errorf("rev-list count = %s, want 1 (withheld commit must not land)", got)
	}
}

// TestCommit_WaitDelayOrphanStillSucceeds pins the ErrWaitDelay tolerance
// (review finding 6): a trusted repository whose pre-commit hook launches a
// background process holding the pipes must still REPORT success — the
// commit lands, the SHA resolves, the status event fires — instead of
// surfacing exec.ErrWaitDelay as a commit failure.
func TestCommit_WaitDelayOrphanStillSucceeds(t *testing.T) {
	gittest.RequirePOSIXShell(t)
	gittrust.Clear()
	t.Cleanup(gittrust.Clear)
	isolateHomeForCommit(t)

	root := filepath.Join(t.TempDir(), "repo")
	repo := gittest.InitRepo(t, root, "hello\n")

	// The hook backgrounds a process that inherits stdout/stderr and holds
	// them past cmd.WaitDelay (6s sleep vs the 1s WaitDelay).
	hookPath := filepath.Join(root, ".git", "hooks", "pre-commit")
	if err := os.MkdirAll(filepath.Dir(hookPath), 0o755); err != nil {
		t.Fatalf("mkdir hooks: %v", err)
	}
	orphan := filepath.Join(t.TempDir(), "orphan-exit")
	body := "#!/bin/sh\n( sleep 6; echo orphan-done >> " + orphan + " ) &\nexit 0\n"
	if err := os.WriteFile(hookPath, []byte(body), 0o755); err != nil {
		t.Fatalf("write orphan-spawning hook: %v", err)
	}

	f, _, _ := newTestAPI(t)
	f.activeProjectPath = root
	if err := f.TrustGitRepo(root); err != nil {
		t.Fatalf("TrustGitRepo: %v", err)
	}

	repo.Write(t, "file.txt", "hello\nchanged\n")
	repo.Git(t, "add", ".")

	res, err := f.Commit("orphan hook commit", false)
	if err != nil {
		t.Fatalf("Commit (orphan-holding hook): %v — ErrWaitDelay must not fail a landed commit", err)
	}
	if res.Sha == "" {
		t.Fatal("Commit (orphan-holding hook): expected a commit SHA")
	}
	if got := commitCount(t, root); got != "2" {
		t.Errorf("rev-list count = %s, want 2 (commit landed)", got)
	}
}
