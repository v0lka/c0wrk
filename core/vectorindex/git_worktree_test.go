package vectorindex

import (
	"context"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/v0lka/c0wrk/internal/gittest"
)

// Real-git coverage for linked worktrees (git worktree add), the shape a
// managed session workspace takes (ADR-080). In a linked worktree <root>/.git
// is a FILE carrying a "gitdir: <path>" pointer to the worktree's private git
// directory — everything that watches or resolves HEAD-level state must follow
// the pointer. GitMonitor.Start watching <root>/.git directly would watch a
// pointer file that never changes while branches are switched or commits land
// in that tree, so the monitor would never fire.
//
// Timing discipline: no sleeps. Branch-switch/commit delivery is awaited on a
// buffered channel under a bounded watchdog (fsnotify latency + the monitor's
// 300ms debounce are both well inside the window; the watchdog only fails a
// wedged monitor).

// runTestGit runs a setup-side git command in dir and fails the test on
// error. Test-fixture plumbing only — it never touches the production
// runGit neutralization path under test.
func runTestGit(t *testing.T, dir string, args ...string) string {
	t.Helper()
	cmd := exec.CommandContext(context.Background(), "git", args...)
	cmd.Dir = dir
	out, err := cmd.CombinedOutput()
	if err != nil {
		t.Fatalf("git -C %s %s: %v\n%s", dir, strings.Join(args, " "), err, out)
	}
	return strings.TrimSpace(string(out))
}

// addWorktree creates a linked worktree of repo at path on a new branch born
// from main and returns the worktree root path.
func addWorktree(t *testing.T, repo *gittest.Repo, path, branch string) string {
	t.Helper()
	repo.Git(t, "worktree", "add", "-b", branch, path, "main")
	return path
}

// TestResolveGitDir covers the pointer resolution for every .git shape the
// monitor can meet: a normal checkout's .git directory, a linked worktree's
// pointer file, a non-repository, and broken pointers that must surface as
// errors instead of a silently wrong watch target.
func TestResolveGitDir(t *testing.T) {
	gittest.RequireGit(t)
	root := filepath.Join(t.TempDir(), "repo")
	repo := gittest.InitRepo(t, root, "hello\n")
	wt := addWorktree(t, repo, filepath.Join(t.TempDir(), "wt"), "topic")

	// Normal checkout: .git is a directory.
	got, err := resolveGitDir(root)
	if err != nil {
		t.Fatalf("resolveGitDir(checkout): %v", err)
	}
	if got != repo.GitDir() {
		t.Errorf("resolveGitDir(checkout) = %q, want %q", got, repo.GitDir())
	}

	// Linked worktree: .git is a file; the resolved gitdir is the worktree's
	// private directory under the main repo's .git/worktrees/.
	got, err = resolveGitDir(wt)
	if err != nil {
		t.Fatalf("resolveGitDir(worktree): %v", err)
	}
	if got != repo.GitDirFile(filepath.Join("worktrees", "wt")) {
		t.Errorf("resolveGitDir(worktree) = %q, want %q", got, repo.GitDirFile(filepath.Join("worktrees", "wt")))
	}
	if fi, statErr := os.Stat(filepath.Join(wt, ".git")); statErr != nil || fi.IsDir() {
		t.Fatalf("fixture sanity: %s/.git must be a pointer file", wt)
	}

	// Not a repository.
	plain := t.TempDir()
	got, err = resolveGitDir(plain)
	if err != nil || got != "" {
		t.Errorf("resolveGitDir(non-repo) = %q, %v; want empty, nil", got, err)
	}

	// Pointer file without a gitdir: prefix → error.
	broken := t.TempDir()
	if err := os.WriteFile(filepath.Join(broken, ".git"), []byte("garbage\n"), 0o644); err != nil {
		t.Fatalf("write broken pointer: %v", err)
	}
	if _, err := resolveGitDir(broken); err == nil {
		t.Error("resolveGitDir(garbage pointer) = nil error; want error")
	}

	// Pointer file naming a missing directory → error.
	dangling := t.TempDir()
	if err := os.WriteFile(filepath.Join(dangling, ".git"), []byte("gitdir: /nonexistent/gitdir\n"), 0o644); err != nil {
		t.Fatalf("write dangling pointer: %v", err)
	}
	if _, err := resolveGitDir(dangling); err == nil {
		t.Error("resolveGitDir(dangling pointer) = nil error; want error")
	}
}

// TestCurrentBranch_LinkedWorktree pins branch resolution inside a linked
// worktree: git resolves through the .git pointer, so the vector index's
// branch detection keys off the worktree's OWN checked-out branch.
func TestCurrentBranch_LinkedWorktree(t *testing.T) {
	gittest.RequireGit(t)
	root := filepath.Join(t.TempDir(), "repo")
	repo := gittest.InitRepo(t, root, "hello\n")
	wt := addWorktree(t, repo, filepath.Join(t.TempDir(), "wt"), "topic")

	branch, err := CurrentBranch(context.Background(), wt)
	if err != nil {
		t.Fatalf("CurrentBranch(worktree): %v", err)
	}
	if branch != "topic" {
		t.Errorf("CurrentBranch(worktree) = %q, want %q", branch, "topic")
	}

	// The main checkout independently resolves its own branch.
	branch, err = CurrentBranch(context.Background(), root)
	if err != nil {
		t.Fatalf("CurrentBranch(checkout): %v", err)
	}
	if branch != "main" {
		t.Errorf("CurrentBranch(checkout) = %q, want %q", branch, "main")
	}
}

// TestGitMonitor_LinkedWorktree_BranchSwitch is the AC test for GitMonitor in
// a managed-worktree-shaped repository: starting the monitor on a linked
// worktree and switching THAT worktree to another branch must fire onChange
// with the new branch name. The HEAD rewrite lands in the worktree's private
// git directory — only a monitor that resolved the .git pointer sees it.
func TestGitMonitor_LinkedWorktree_BranchSwitch(t *testing.T) {
	gittest.RequireGit(t)
	root := filepath.Join(t.TempDir(), "repo")
	repo := gittest.InitRepo(t, root, "hello\n")
	repo.Git(t, "branch", "other")
	wt := addWorktree(t, repo, filepath.Join(t.TempDir(), "wt"), "topic")

	branchCh := make(chan string, 4)
	mon, err := NewGitMonitor(wt, func(newBranch string) { branchCh <- newBranch }, nil)
	if err != nil {
		t.Fatalf("NewGitMonitor(worktree): %v", err)
	}
	t.Cleanup(func() { _ = mon.Stop() })
	if err := mon.Start(); err != nil {
		t.Fatalf("monitor start on linked worktree: %v", err)
	}
	if got := mon.CurrentBranchName(); got != "topic" {
		t.Fatalf("initial branch = %q, want %q", got, "topic")
	}

	// Switch the worktree (not the checkout) to another branch. This must
	// NOT touch the .git pointer file — the event the monitor needs happens
	// in <main>/.git/worktrees/wt/HEAD.
	runTestGit(t, wt, "checkout", "other")

	select {
	case got := <-branchCh:
		if got != "other" {
			t.Fatalf("onChange branch = %q, want %q", got, "other")
		}
	case <-time.After(10 * time.Second):
		t.Fatal("git monitor did not detect the branch switch inside the linked worktree (watchdog)")
	}
	if got := mon.CurrentBranchName(); got != "other" {
		t.Errorf("CurrentBranchName after switch = %q, want %q", got, "other")
	}
}

// TestGitMonitor_LinkedWorktree_CommitDetection pins commit detection via
// HEAD in a linked worktree: on a detached worktree every commit rewrites
// HEAD itself, so the monitor must report the new short-hash identity — the
// same signal branch switches produce on attached trees.
func TestGitMonitor_LinkedWorktree_CommitDetection(t *testing.T) {
	gittest.RequireGit(t)
	root := filepath.Join(t.TempDir(), "repo")
	repo := gittest.InitRepo(t, root, "hello\n")
	wt := filepath.Join(t.TempDir(), "wt")
	repo.Git(t, "worktree", "add", "--detach", wt, "main")

	branchCh := make(chan string, 4)
	mon, err := NewGitMonitor(wt, func(newBranch string) { branchCh <- newBranch }, nil)
	if err != nil {
		t.Fatalf("NewGitMonitor(detached worktree): %v", err)
	}
	t.Cleanup(func() { _ = mon.Stop() })
	if err := mon.Start(); err != nil {
		t.Fatalf("monitor start on detached worktree: %v", err)
	}
	before := mon.CurrentBranchName()
	if before == "" || len(before) > 12 {
		t.Fatalf("initial detached identity = %q, want a short commit hash", before)
	}

	// Commit inside the worktree: HEAD (the hash) is rewritten.
	if err := os.WriteFile(filepath.Join(wt, "new.txt"), []byte("content\n"), 0o644); err != nil {
		t.Fatalf("write new.txt: %v", err)
	}
	runTestGit(t, wt, "add", ".")
	runTestGit(t, wt, "commit", "-m", "worktree commit")

	select {
	case got := <-branchCh:
		if got == before || len(got) > 12 {
			t.Fatalf("onChange identity after commit = %q, want a new short hash != %q", got, before)
		}
	case <-time.After(10 * time.Second):
		t.Fatal("git monitor did not detect the commit inside the linked worktree (watchdog)")
	}
}

// TestGitMonitor_CheckoutUnaffectedByWorktreeEvents pins the inverse
// isolation structurally: a monitor on the main checkout watches the
// checkout's OWN .git directory and never the linked worktree's private git
// directory, so branch churn inside the worktree cannot surface as checkout
// events (fsnotify watches are per-directory, not recursive).
func TestGitMonitor_CheckoutUnaffectedByWorktreeEvents(t *testing.T) {
	gittest.RequireGit(t)
	root := filepath.Join(t.TempDir(), "repo")
	repo := gittest.InitRepo(t, root, "hello\n")
	repo.Git(t, "branch", "other")
	wt := addWorktree(t, repo, filepath.Join(t.TempDir(), "wt"), "topic")

	checkoutCh := make(chan string, 4)
	mon, err := NewGitMonitor(root, func(newBranch string) { checkoutCh <- newBranch }, nil)
	if err != nil {
		t.Fatalf("NewGitMonitor(checkout): %v", err)
	}
	t.Cleanup(func() { _ = mon.Stop() })
	if err := mon.Start(); err != nil {
		t.Fatalf("monitor start on checkout: %v", err)
	}

	// The watch target is the checkout's .git, never the worktree's private
	// gitdir — the structural reason worktree events cannot leak.
	watched := mon.watcher.WatchList()
	wtGitDir := repo.GitDirFile(filepath.Join("worktrees", "wt"))
	for _, w := range watched {
		if w == wtGitDir {
			t.Fatalf("checkout monitor watches the worktree gitdir %q (watch list: %v)", wtGitDir, watched)
		}
	}
	foundCheckout := false
	for _, w := range watched {
		if w == repo.GitDir() {
			foundCheckout = true
		}
	}
	if !foundCheckout {
		t.Fatalf("checkout monitor does not watch its own .git (watch list: %v)", watched)
	}

	// Branch churn entirely inside the linked worktree: the checkout monitor
	// must neither fire (drained without waiting — the watch list already
	// proves the events are unreachable) nor change its branch state.
	runTestGit(t, wt, "checkout", "other")
	select {
	case got := <-checkoutCh:
		t.Fatalf("checkout monitor fired for worktree-internal branch switch: %q", got)
	default:
	}
	if got := mon.CurrentBranchName(); got != "main" {
		t.Errorf("checkout branch = %q after worktree switch, want main", got)
	}
}
