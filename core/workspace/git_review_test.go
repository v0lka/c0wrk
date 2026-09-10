package workspace

import (
	"context"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"sync/atomic"
	"testing"
)

// gitRun runs a git sub-command inside dir.
func gitRun(t *testing.T, dir string, args ...string) {
	t.Helper()
	cmd := exec.CommandContext(context.Background(), "git", args...)
	cmd.Dir = dir
	if out, err := cmd.CombinedOutput(); err != nil {
		t.Fatalf("git %s in %s: %v\n%s", strings.Join(args, " "), dir, err, out)
	}
}

// gitWrite writes content to name inside dir.
func gitWrite(t *testing.T, dir, name, content string) {
	t.Helper()
	if err := os.WriteFile(filepath.Join(dir, name), []byte(content), 0o644); err != nil {
		t.Fatalf("write %s: %v", name, err)
	}
}

// initGitRepo creates a git repository in dir with an initial commit so HEAD
// exists, providing a baseline for diff operations.
func initGitRepo(t *testing.T, dir string) {
	t.Helper()
	gitRun(t, dir, "init")
	gitRun(t, dir, "config", "user.email", "t@t.t")
	gitRun(t, dir, "config", "user.name", "tester")
	gitWrite(t, dir, "committed.txt", "v1\n")
	gitRun(t, dir, "add", "committed.txt")
	gitRun(t, dir, "commit", "-m", "init")
}

// TestBuildReviewDiff_IncludesUntracked verifies that the combined diff
// covers both tracked changes (via `git diff HEAD`) and untracked files
// (emitted per-file via `git diff --no-index`), while excluding git-ignored
// files. `git diff HEAD` alone omits untracked files, so they must be added
// separately for the review page to see a complete changeset.
func TestBuildReviewDiff_IncludesUntracked(t *testing.T) {
	dir := t.TempDir()
	initGitRepo(t, dir)

	// Tracked modification: appears in `git diff HEAD`.
	gitWrite(t, dir, "committed.txt", "v2\n")
	// Untracked file: only surfaces via `git diff --no-index`.
	gitWrite(t, dir, "untracked.txt", "hello\nworld\n")
	// Git-ignored untracked file: must be excluded.
	gitWrite(t, dir, ".gitignore", "ignored.txt\n")
	gitWrite(t, dir, "ignored.txt", "nope\n")

	diff, err := BuildReviewDiff(context.Background(), dir, 5)
	if err != nil {
		t.Fatalf("BuildReviewDiff: %v", err)
	}

	// Both the tracked change and the untracked addition must be present.
	if !strings.Contains(diff, "diff --git a/committed.txt b/committed.txt") {
		t.Errorf("tracked change missing from diff:\n%s", diff)
	}
	if !strings.Contains(diff, "diff --git a/untracked.txt b/untracked.txt") {
		t.Errorf("untracked addition missing from diff:\n%s", diff)
	}
	if !strings.Contains(diff, "+++ b/untracked.txt") {
		t.Errorf("untracked file new-file marker missing from diff:\n%s", diff)
	}
	// The ignored file must never appear as its own diff block. (The
	// .gitignore file itself is untracked and legitimately appears, and its
	// content references "ignored.txt", so we check for the diff header
	// rather than the bare substring.)
	if strings.Contains(diff, "diff --git a/ignored.txt b/ignored.txt") {
		t.Errorf("git-ignored file leaked into diff as its own block:\n%s", diff)
	}

	// The combined output must be parseable by ParseReviewDiff, grouping the
	// tracked change and the untracked addition as separate files.
	files := ParseReviewDiff(diff)
	paths := map[string]bool{}
	for _, f := range files {
		paths[f.Path] = true
	}
	if !paths["committed.txt"] {
		t.Errorf("ParseReviewDiff: committed.txt missing, got %v", paths)
	}
	if !paths["untracked.txt"] {
		t.Errorf("ParseReviewDiff: untracked.txt missing, got %v", paths)
	}
}

// TestBuildReviewDiff_CleanTree verifies that a clean working tree (no
// tracked changes and no untracked files) yields an empty diff.
func TestBuildReviewDiff_CleanTree(t *testing.T) {
	dir := t.TempDir()
	initGitRepo(t, dir)

	diff, err := BuildReviewDiff(context.Background(), dir, 5)
	if err != nil {
		t.Fatalf("BuildReviewDiff: %v", err)
	}
	if diff != "" {
		t.Errorf("expected empty diff for clean tree, got:\n%s", diff)
	}
}

// TestBuildReviewDiff_ScansOncePerOperation pins review [15]: one
// BuildReviewDiff operation (diff HEAD + ls-files + one --no-index diff per
// untracked file — formerly N+2 rescans) shares a single config scan taken
// before its first git invocation, while a SECOND operation rescans (no
// cross-operation cache: freshness per user operation is the security
// property).
func TestBuildReviewDiff_ScansOncePerOperation(t *testing.T) {
	dir := t.TempDir()
	initGitRepo(t, dir)
	gitWrite(t, dir, "committed.txt", "v2\n") // tracked change
	gitWrite(t, dir, "u1.txt", "one\n")
	gitWrite(t, dir, "u2.txt", "two\n")
	gitWrite(t, dir, "u3.txt", "three\n")

	var scans atomic.Int64
	orig := scanGitConfigFn
	scanGitConfigFn = func(repoRoot string) (*GitConfigInfo, error) {
		scans.Add(1)
		return orig(repoRoot)
	}
	t.Cleanup(func() { scanGitConfigFn = orig })

	diff, err := BuildReviewDiff(context.Background(), dir, 3)
	if err != nil {
		t.Fatalf("BuildReviewDiff: %v", err)
	}
	if !strings.Contains(diff, "+v2") || !strings.Contains(diff, "u1.txt") || !strings.Contains(diff, "u3.txt") {
		t.Fatalf("diff lost tracked or untracked content:\n%s", diff)
	}
	if got := scans.Load(); got != 1 {
		t.Errorf("config scans for one BuildReviewDiff = %d, want exactly 1 (operation-local memo, review [15])", got)
	}

	if _, err := BuildReviewDiff(context.Background(), dir, 3); err != nil {
		t.Fatalf("second BuildReviewDiff: %v", err)
	}
	if got := scans.Load(); got != 2 {
		t.Errorf("config scans after second BuildReviewDiff = %d, want 2 (no cross-operation cache)", got)
	}
}

// TestGetFileDiff_ScanFailureErrorsFailClosed pins the reworded contract of
// review [53]: when a repository's config cannot be scanned, the boolean
// predicates report not-a-repo and the caller takes its non-repo path — but
// that fallback is degraded, not silently git-free. The --no-index diff
// runs inside the repository directory and still consults repo config, so
// it spawns through the same fresh scan and the failure resurfaces as an
// error instead of producing output. git never runs un-neutralized.
func TestGetFileDiff_ScanFailureErrorsFailClosed(t *testing.T) {
	dir := t.TempDir()
	// .git/config as a directory: readCapped refuses non-regular files on
	// every platform, so the scan fails.
	if err := os.MkdirAll(filepath.Join(dir, ".git", "config"), 0o755); err != nil {
		t.Fatal(err)
	}
	gitWrite(t, dir, "file.txt", "content\n")

	_, err := GetFileDiff(context.Background(), dir, "file.txt")
	if err == nil {
		t.Fatal("expected a fail-closed error from the --no-index fallback, got a diff")
	}
	if !strings.Contains(err.Error(), "fail closed") {
		t.Errorf("error should surface the fail-closed scan failure, got: %v", err)
	}
}
