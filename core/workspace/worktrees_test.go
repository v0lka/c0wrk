package workspace

import (
	"bytes"
	"context"
	"errors"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"sync"
	"testing"

	"github.com/v0lka/c0wrk/internal/gittest"
)

// Worktree primitive tests. Everything exercises the real `git` binary
// through the production path (GitCmdInRepo's scan pipeline) unless a test
// explicitly swaps in a fake git to pin argv or malformed output. Shell
// shims and canaries are POSIX-only (gittest.RequirePOSIXShell).

func setupWorktreeRepo(t *testing.T) *gittest.Repo {
	t.Helper()
	gittest.RequireGit(t)
	root := filepath.Join(t.TempDir(), "repo")
	return gittest.InitRepo(t, root, "seed\n")
}

func managedPath(root, name string) string {
	return filepath.Join(root, ".worktrees", name)
}

// rawGitOut runs a setup git command (before any hostile config exists)
// and returns trimmed stdout.
func rawGitOut(t *testing.T, dir string, args ...string) string {
	t.Helper()
	cmd := exec.CommandContext(context.Background(), "git", args...)
	cmd.Dir = dir
	out, err := cmd.Output()
	if err != nil {
		t.Fatalf("raw git %s (dir %s): %v", strings.Join(args, " "), dir, err)
	}
	return strings.TrimSpace(string(out))
}

// ---------------------------------------------------------------------------
// Listing: linked/local/external discovery, states
// ---------------------------------------------------------------------------

func TestListWorktrees_Classification(t *testing.T) {
	repo := setupWorktreeRepo(t)
	repo.Git(t, "branch", "feature")
	ext := filepath.Join(gittest.TempDir(t), "ext")
	repo.Git(t, "worktree", "add", ext, "feature")
	managed := managedPath(repo.Root, "s1")
	info, err := AddWorktree(context.Background(), repo.Root, managed, AddWorktreeOptions{
		Branch:       "wt-s1",
		CreateBranch: true,
	})
	if err != nil {
		t.Fatalf("AddWorktree: %v", err)
	}
	if info.Kind != WorktreeManaged {
		t.Errorf("added worktree kind = %q, want managed", info.Kind)
	}

	trees, err := ListWorktrees(context.Background(), repo.Root)
	if err != nil {
		t.Fatalf("ListWorktrees: %v", err)
	}
	byPath := make(map[string]WorktreeInfo, len(trees))
	for _, w := range trees {
		byPath[w.Path] = w
	}
	if len(trees) != 3 {
		t.Fatalf("worktree count = %d, want 3 (%v)", len(trees), trees)
	}
	main, ok := byPath[repo.Root]
	if !ok || main.Kind != WorktreeMain {
		t.Errorf("main checkout entry = %+v, want kind main at %s", main, repo.Root)
	}
	if main.Branch != "main" {
		t.Errorf("main branch = %q, want main", main.Branch)
	}
	m, ok := byPath[managed]
	if !ok || m.Kind != WorktreeManaged {
		t.Errorf("managed entry = %+v, want kind managed at %s", m, managed)
	}
	if m.Branch != "wt-s1" {
		t.Errorf("managed branch = %q, want wt-s1", m.Branch)
	}
	e, ok := byPath[ext]
	if !ok || e.Kind != WorktreeExternal {
		t.Errorf("external entry = %+v, want kind external at %s", e, ext)
	}
	if e.Branch != "feature" {
		t.Errorf("external branch = %q, want feature", e.Branch)
	}
	holders := BranchHolders(trees)
	if holders["feature"] != ext {
		t.Errorf("BranchHolders[feature] = %q, want %q", holders["feature"], ext)
	}
}

func TestListWorktrees_LockedWithReason(t *testing.T) {
	repo := setupWorktreeRepo(t)
	managed := managedPath(repo.Root, "s1")
	if _, err := AddWorktree(context.Background(), repo.Root, managed, AddWorktreeOptions{
		Branch:       "wt-s1",
		CreateBranch: true,
	}); err != nil {
		t.Fatalf("AddWorktree: %v", err)
	}
	repo.Git(t, "worktree", "lock", "--reason", "held by tooling", managed)

	trees, err := ListWorktrees(context.Background(), repo.Root)
	if err != nil {
		t.Fatalf("ListWorktrees: %v", err)
	}
	entry := FindWorktree(trees, managed)
	if entry == nil {
		t.Fatal("locked worktree missing from list")
	}
	if !entry.Locked {
		t.Error("entry.Locked = false, want true")
	}
	if entry.LockedReason != "held by tooling" {
		t.Errorf("LockedReason = %q, want %q", entry.LockedReason, "held by tooling")
	}
}

func TestListWorktrees_PrunableAfterDirectoryLoss(t *testing.T) {
	repo := setupWorktreeRepo(t)
	managed := managedPath(repo.Root, "s1")
	if _, err := AddWorktree(context.Background(), repo.Root, managed, AddWorktreeOptions{
		Branch:       "wt-s1",
		CreateBranch: true,
	}); err != nil {
		t.Fatalf("AddWorktree: %v", err)
	}
	if err := os.RemoveAll(managed); err != nil {
		t.Fatalf("removing worktree dir: %v", err)
	}
	trees, err := ListWorktrees(context.Background(), repo.Root)
	if err != nil {
		t.Fatalf("ListWorktrees: %v", err)
	}
	entry := FindWorktree(trees, managed)
	if entry == nil {
		t.Fatal("prunable worktree missing from list")
	}
	if !entry.Prunable {
		t.Errorf("entry.Prunable = false, want true (%+v)", entry)
	}
	if entry.PrunableReason == "" {
		t.Error("PrunableReason empty, want git's reason")
	}
}

func TestListWorktrees_MalformedEmptyOutput(t *testing.T) {
	gittest.InstallFakeGit(t)
	if _, err := ListWorktrees(context.Background(), t.TempDir()); !errors.Is(err, ErrWorktreeMalformed) {
		t.Fatalf("ListWorktrees with empty output err = %v, want ErrWorktreeMalformed", err)
	}
}

// installScriptGit puts a shell-script git stand-in on PATH that prints the
// fixed stdout regardless of argv (POSIX only).
func installScriptGit(t *testing.T, stdout string) {
	t.Helper()
	gittest.RequirePOSIXShell(t)
	gittest.RequireGit(t)
	binDir := filepath.Join(t.TempDir(), "bin")
	if err := os.MkdirAll(binDir, 0o755); err != nil {
		t.Fatalf("mkdir bin: %v", err)
	}
	script := fmt.Sprintf("#!/bin/sh\ncat <<'PORCELAIN_EOF'\n%s\nPORCELAIN_EOF\n", stdout)
	if err := os.WriteFile(filepath.Join(binDir, "git"), []byte(script), 0o755); err != nil {
		t.Fatalf("writing fake git: %v", err)
	}
	t.Setenv("PATH", binDir+string(os.PathListSeparator)+os.Getenv("PATH"))
}

func TestListWorktrees_MalformedVariants(t *testing.T) {
	cases := []struct {
		name   string
		output string
	}{
		{"unknown attribute", "worktree /tmp/repo\nHEAD abc\nbogusattr x\n"},
		{"missing HEAD", "worktree /tmp/repo\nbranch refs/heads/main\n"},
		{"attribute before header", "HEAD abc\nworktree /tmp/repo\n"},
		{"relative worktree path", "worktree repo\nHEAD abc\n"},
		{"duplicate HEAD", "worktree /tmp/repo\nHEAD abc\nHEAD def\n"},
		{"trailing attribute without entry", "\n\nHEAD abc\n"},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			installScriptGit(t, tc.output)
			if _, err := ListWorktrees(context.Background(), t.TempDir()); !errors.Is(err, ErrWorktreeMalformed) {
				t.Fatalf("err = %v, want ErrWorktreeMalformed", err)
			}
		})
	}
}

// ---------------------------------------------------------------------------
// Creation
// ---------------------------------------------------------------------------

func TestAddWorktree_SuccessManaged(t *testing.T) {
	repo := setupWorktreeRepo(t)
	managed := managedPath(repo.Root, "s1")
	info, err := AddWorktree(context.Background(), repo.Root, managed, AddWorktreeOptions{
		Branch:       "wt-s1",
		CreateBranch: true,
		StartPoint:   "main",
	})
	if err != nil {
		t.Fatalf("AddWorktree: %v", err)
	}
	if info.Branch != "wt-s1" || info.Detached {
		t.Errorf("info = %+v, want branch wt-s1, not detached", info)
	}
	if got := rawGitOut(t, managed, "rev-parse", "--abbrev-ref", "HEAD"); got != "wt-s1" {
		t.Errorf("worktree HEAD = %q, want wt-s1", got)
	}
	// The container must be invisible to the main checkout's status.
	if out := rawGitOut(t, repo.Root, "status", "--porcelain"); strings.Contains(out, ".worktrees") {
		t.Errorf("git status of main checkout lists .worktrees:\n%s", out)
	}
	// No .gitignore is ever created or required.
	if _, err := os.Stat(filepath.Join(repo.Root, ".gitignore")); !os.IsNotExist(err) {
		t.Errorf(".gitignore exists after managed add (err=%v), want untouched", err)
	}
}

func TestAddWorktree_CheckoutExistingBranch(t *testing.T) {
	repo := setupWorktreeRepo(t)
	repo.Git(t, "branch", "feature")
	managed := managedPath(repo.Root, "s1")
	info, err := AddWorktree(context.Background(), repo.Root, managed, AddWorktreeOptions{Branch: "feature"})
	if err != nil {
		t.Fatalf("AddWorktree: %v", err)
	}
	if info.Branch != "feature" {
		t.Errorf("branch = %q, want feature", info.Branch)
	}
}

func TestAddWorktree_RefusesForeignDir(t *testing.T) {
	repo := setupWorktreeRepo(t)
	managed := managedPath(repo.Root, "s1")
	if err := os.MkdirAll(managed, 0o755); err != nil {
		t.Fatalf("mkdir foreign dir: %v", err)
	}
	foreign := filepath.Join(managed, "keep.txt")
	if err := os.WriteFile(foreign, []byte("precious\n"), 0o644); err != nil {
		t.Fatalf("write foreign file: %v", err)
	}
	_, err := AddWorktree(context.Background(), repo.Root, managed, AddWorktreeOptions{
		Branch:       "wt-s1",
		CreateBranch: true,
	})
	if !errors.Is(err, ErrWorktreeExists) {
		t.Fatalf("err = %v, want ErrWorktreeExists", err)
	}
	got, rerr := os.ReadFile(foreign)
	if rerr != nil || string(got) != "precious\n" {
		t.Errorf("foreign dir overwritten or unreadable: %q, %v", got, rerr)
	}
	if out := rawGitOut(t, repo.Root, "worktree", "list", "--porcelain"); strings.Count(out, "worktree ") != 1 {
		t.Errorf("worktree list changed after refused add:\n%s", out)
	}
}

func TestAddWorktree_BranchOccupancyFailures(t *testing.T) {
	repo := setupWorktreeRepo(t)
	repo.Git(t, "branch", "feature")

	// Held by the main checkout.
	_, err := AddWorktree(context.Background(), repo.Root, managedPath(repo.Root, "s1"), AddWorktreeOptions{Branch: "main"})
	if !errors.Is(err, ErrBranchBusy) {
		t.Fatalf("checkout of main err = %v, want ErrBranchBusy", err)
	}

	// Creating a branch that already exists.
	_, err = AddWorktree(context.Background(), repo.Root, managedPath(repo.Root, "s2"), AddWorktreeOptions{
		Branch:       "feature",
		CreateBranch: true,
	})
	if !errors.Is(err, ErrBranchExists) {
		t.Fatalf("create over existing err = %v, want ErrBranchExists", err)
	}

	// Checking out a branch that does not exist.
	_, err = AddWorktree(context.Background(), repo.Root, managedPath(repo.Root, "s3"), AddWorktreeOptions{Branch: "nope"})
	if !errors.Is(err, ErrBranchMissing) {
		t.Fatalf("checkout missing err = %v, want ErrBranchMissing", err)
	}

	// Occupied by another linked worktree.
	occupied := managedPath(repo.Root, "s4")
	if _, err := AddWorktree(context.Background(), repo.Root, occupied, AddWorktreeOptions{Branch: "feature"}); err != nil {
		t.Fatalf("first checkout of feature: %v", err)
	}
	_, err = AddWorktree(context.Background(), repo.Root, managedPath(repo.Root, "s5"), AddWorktreeOptions{Branch: "feature"})
	if !errors.Is(err, ErrBranchBusy) {
		t.Fatalf("second checkout of feature err = %v, want ErrBranchBusy", err)
	}
}

func TestAddWorktree_PathValidation(t *testing.T) {
	repo := setupWorktreeRepo(t)
	linkTarget := gittest.TempDir(t)
	link := filepath.Join(repo.Root, "link")
	if err := os.Symlink(linkTarget, link); err != nil {
		t.Skipf("symlink unsupported: %v", err)
	}
	cases := []struct {
		name   string
		target string
		want   error
	}{
		{"relative path", "somewhere/rel", ErrWorktreePathInvalid},
		{"repo root itself", repo.Root, ErrWorktreePathInvalid},
		{"ancestor of repo root", filepath.Dir(repo.Root), ErrWorktreePathInvalid},
		{"managed container itself", filepath.Join(repo.Root, ".worktrees"), ErrWorktreePathInvalid},
		{"inside .git", filepath.Join(repo.Root, ".git", "wt"), ErrWorktreePathInvalid},
		{"symlinked ancestor", filepath.Join(link, "s1"), ErrWorktreePathInvalid},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			_, err := AddWorktree(context.Background(), repo.Root, tc.target, AddWorktreeOptions{
				Branch:       "wt-x",
				CreateBranch: true,
			})
			if !errors.Is(err, tc.want) {
				t.Fatalf("err = %v, want %v", err, tc.want)
			}
		})
	}
}

func TestAddWorktree_RefusesOptionLikeRefs(t *testing.T) {
	repo := setupWorktreeRepo(t)
	_, err := AddWorktree(context.Background(), repo.Root, managedPath(repo.Root, "s1"), AddWorktreeOptions{
		Branch:       "--force",
		CreateBranch: true,
	})
	if !errors.Is(err, ErrRefInvalid) {
		t.Fatalf("err = %v, want ErrRefInvalid", err)
	}
	_, err = AddWorktree(context.Background(), repo.Root, managedPath(repo.Root, "s2"), AddWorktreeOptions{
		Branch:       "wt-s2",
		CreateBranch: true,
		StartPoint:   "--exec=evil",
	})
	if !errors.Is(err, ErrRefInvalid) {
		t.Fatalf("start point err = %v, want ErrRefInvalid", err)
	}
}

func TestAddWorktree_RefusesTargetInsideLinkedWorktree(t *testing.T) {
	repo := setupWorktreeRepo(t)
	managed := managedPath(repo.Root, "s1")
	if _, err := AddWorktree(context.Background(), repo.Root, managed, AddWorktreeOptions{
		Branch:       "wt-s1",
		CreateBranch: true,
	}); err != nil {
		t.Fatalf("AddWorktree: %v", err)
	}
	nested := filepath.Join(managed, "nested")
	_, err := AddWorktree(context.Background(), repo.Root, nested, AddWorktreeOptions{
		Branch:       "wt-nested",
		CreateBranch: true,
	})
	if !errors.Is(err, ErrWorktreePathInvalid) {
		t.Fatalf("err = %v, want ErrWorktreePathInvalid", err)
	}
}

func TestAddWorktree_ExternalTargetSkipsExcludeInstall(t *testing.T) {
	repo := setupWorktreeRepo(t)
	repo.Git(t, "branch", "feature")
	ext := filepath.Join(gittest.TempDir(t), "ext-tree")
	if _, err := AddWorktree(context.Background(), repo.Root, ext, AddWorktreeOptions{Branch: "feature"}); err != nil {
		t.Fatalf("external AddWorktree: %v", err)
	}
	exclude, err := os.ReadFile(repo.GitDirFile("info/exclude"))
	if err != nil && !os.IsNotExist(err) {
		t.Fatalf("read exclude: %v", err)
	}
	if strings.Contains(string(exclude), worktreesExcludePattern) {
		t.Errorf("external add installed %q into info/exclude:\n%s", worktreesExcludePattern, exclude)
	}
}

// ---------------------------------------------------------------------------
// Concurrency: same-branch creates cannot both succeed
// ---------------------------------------------------------------------------

func TestAddWorktree_ConcurrentSameBranchSingleWinner(t *testing.T) {
	repo := setupWorktreeRepo(t)
	const n = 4
	var wg sync.WaitGroup
	results := make([]error, n)
	start := make(chan struct{})
	for i := 0; i < n; i++ {
		wg.Add(1)
		go func(i int) {
			defer wg.Done()
			<-start
			_, err := AddWorktree(context.Background(), repo.Root, managedPath(repo.Root, fmt.Sprintf("s%d", i)), AddWorktreeOptions{
				Branch:       "wt-race",
				CreateBranch: true,
			})
			results[i] = err
		}(i)
	}
	close(start)
	wg.Wait()

	succeeded := 0
	for i, err := range results {
		switch {
		case err == nil:
			succeeded++
		case errors.Is(err, ErrBranchBusy), errors.Is(err, ErrBranchExists):
			// explicit loser verdicts
		default:
			t.Errorf("goroutine %d: err = %v, want success or explicit busy/exists", i, err)
		}
	}
	if succeeded != 1 {
		t.Fatalf("succeeded = %d, want exactly 1 (results: %v)", succeeded, results)
	}
	trees, err := ListWorktrees(context.Background(), repo.Root)
	if err != nil {
		t.Fatalf("ListWorktrees: %v", err)
	}
	if holders := BranchHolders(trees); holders["wt-race"] == "" {
		t.Errorf("wt-race holder = %q, want exactly one path", holders["wt-race"])
	}
}

func TestAddWorktree_ConcurrentSamePathSingleWinner(t *testing.T) {
	repo := setupWorktreeRepo(t)
	const n = 4
	target := managedPath(repo.Root, "same")
	var wg sync.WaitGroup
	results := make([]error, n)
	start := make(chan struct{})
	for i := 0; i < n; i++ {
		wg.Add(1)
		go func(i int) {
			defer wg.Done()
			<-start
			_, err := AddWorktree(context.Background(), repo.Root, target, AddWorktreeOptions{
				Branch:       fmt.Sprintf("wt-race-%d", i),
				CreateBranch: true,
			})
			results[i] = err
		}(i)
	}
	close(start)
	wg.Wait()

	succeeded := 0
	for i, err := range results {
		switch {
		case err == nil:
			succeeded++
		case errors.Is(err, ErrWorktreeExists), errors.Is(err, ErrBranchBusy), errors.Is(err, ErrBranchExists):
			// explicit loser verdicts
		default:
			t.Errorf("goroutine %d: err = %v, want success or explicit exists/busy", i, err)
		}
	}
	if succeeded != 1 {
		t.Fatalf("succeeded = %d, want exactly 1 (results: %v)", succeeded, results)
	}
}

// ---------------------------------------------------------------------------
// Removal — and the never-delete-branch invariant
// ---------------------------------------------------------------------------

func TestRemoveWorktree_Clean_KeepsBranch(t *testing.T) {
	repo := setupWorktreeRepo(t)
	managed := managedPath(repo.Root, "s1")
	if _, err := AddWorktree(context.Background(), repo.Root, managed, AddWorktreeOptions{
		Branch:       "wt-s1",
		CreateBranch: true,
	}); err != nil {
		t.Fatalf("AddWorktree: %v", err)
	}
	if err := RemoveWorktree(context.Background(), repo.Root, managed, RemoveWorktreeOptions{}); err != nil {
		t.Fatalf("RemoveWorktree: %v", err)
	}
	if _, err := os.Stat(managed); !os.IsNotExist(err) {
		t.Errorf("worktree dir still present (err=%v)", err)
	}
	// The branch must survive tree removal: session deletion can never
	// destroy unmerged work.
	if got := rawGitOut(t, repo.Root, "rev-parse", "--verify", "refs/heads/wt-s1"); got == "" {
		t.Error("branch wt-s1 deleted by worktree removal")
	}
}

func TestRemoveWorktree_DirtyRequiresForce(t *testing.T) {
	repo := setupWorktreeRepo(t)
	managed := managedPath(repo.Root, "s1")
	if _, err := AddWorktree(context.Background(), repo.Root, managed, AddWorktreeOptions{
		Branch:       "wt-s1",
		CreateBranch: true,
	}); err != nil {
		t.Fatalf("AddWorktree: %v", err)
	}
	if err := os.WriteFile(filepath.Join(managed, "file.txt"), []byte("changed\n"), 0o644); err != nil {
		t.Fatalf("dirtying worktree: %v", err)
	}
	err := RemoveWorktree(context.Background(), repo.Root, managed, RemoveWorktreeOptions{})
	if !errors.Is(err, ErrWorktreeDirty) {
		t.Fatalf("err = %v, want ErrWorktreeDirty", err)
	}
	var state *StateError
	if !errors.As(err, &state) {
		t.Errorf("err does not expose StateError reason")
	}
	if err := RemoveWorktree(context.Background(), repo.Root, managed, RemoveWorktreeOptions{Force: true}); err != nil {
		t.Fatalf("forced RemoveWorktree: %v", err)
	}
	if got := rawGitOut(t, repo.Root, "rev-parse", "--verify", "refs/heads/wt-s1"); got == "" {
		t.Error("branch wt-s1 deleted by forced removal")
	}
}

func TestRemoveWorktree_LockedRequiresUnlock(t *testing.T) {
	repo := setupWorktreeRepo(t)
	managed := managedPath(repo.Root, "s1")
	if _, err := AddWorktree(context.Background(), repo.Root, managed, AddWorktreeOptions{
		Branch:       "wt-s1",
		CreateBranch: true,
	}); err != nil {
		t.Fatalf("AddWorktree: %v", err)
	}
	repo.Git(t, "worktree", "lock", "--reason", "session tree locked", managed)

	err := RemoveWorktree(context.Background(), repo.Root, managed, RemoveWorktreeOptions{})
	if !errors.Is(err, ErrWorktreeLocked) {
		t.Fatalf("err = %v, want ErrWorktreeLocked", err)
	}
	var state *StateError
	if !errors.As(err, &state) {
		t.Fatal("err does not expose StateError reason")
	}
	if state.Reason != "session tree locked" {
		t.Errorf("StateError reason = %q, want git's lock reason %q", state.Reason, "session tree locked")
	}
	if err := RemoveWorktree(context.Background(), repo.Root, managed, RemoveWorktreeOptions{Unlock: true}); err != nil {
		t.Fatalf("RemoveWorktree after unlock: %v", err)
	}
}

func TestRemoveWorktree_RefusesMainAndForeignPaths(t *testing.T) {
	repo := setupWorktreeRepo(t)
	if err := RemoveWorktree(context.Background(), repo.Root, repo.Root, RemoveWorktreeOptions{Force: true}); !errors.Is(err, ErrWorktreeMain) {
		t.Fatalf("main err = %v, want ErrWorktreeMain", err)
	}
	foreign := filepath.Join(gittest.TempDir(t), "foreign")
	if err := os.MkdirAll(foreign, 0o755); err != nil {
		t.Fatalf("mkdir foreign: %v", err)
	}
	if err := RemoveWorktree(context.Background(), repo.Root, foreign, RemoveWorktreeOptions{Force: true}); !errors.Is(err, ErrWorktreeNotLinked) {
		t.Fatalf("foreign err = %v, want ErrWorktreeNotLinked", err)
	}
}

func TestRemoveWorktree_CleansStaleMetadataAndKeepsBranch(t *testing.T) {
	repo := setupWorktreeRepo(t)
	managed := managedPath(repo.Root, "s1")
	if _, err := AddWorktree(context.Background(), repo.Root, managed, AddWorktreeOptions{
		Branch:       "wt-s1",
		CreateBranch: true,
	}); err != nil {
		t.Fatalf("AddWorktree: %v", err)
	}
	if err := os.RemoveAll(managed); err != nil {
		t.Fatalf("removing dir: %v", err)
	}
	// git removes a worktree whose directory is already gone by deleting
	// its administrative metadata — the right release semantics for a tree
	// lost out-of-band, and the branch survives regardless.
	if err := RemoveWorktree(context.Background(), repo.Root, managed, RemoveWorktreeOptions{}); err != nil {
		t.Fatalf("RemoveWorktree over missing dir: %v", err)
	}
	trees, err := ListWorktrees(context.Background(), repo.Root)
	if err != nil {
		t.Fatalf("ListWorktrees: %v", err)
	}
	if FindWorktree(trees, managed) != nil {
		t.Error("stale metadata survived remove")
	}
	if got := rawGitOut(t, repo.Root, "rev-parse", "--verify", "refs/heads/wt-s1"); got == "" {
		t.Error("branch wt-s1 deleted by stale-metadata removal")
	}
	if err := PruneWorktrees(context.Background(), repo.Root); err != nil {
		t.Fatalf("PruneWorktrees: %v", err)
	}
}

// ---------------------------------------------------------------------------
// Recreate (restore)
// ---------------------------------------------------------------------------

func TestRecreateWorktree_NoOpWhenPresentOnPinnedBranch(t *testing.T) {
	repo := setupWorktreeRepo(t)
	managed := managedPath(repo.Root, "s1")
	added, err := AddWorktree(context.Background(), repo.Root, managed, AddWorktreeOptions{
		Branch:       "wt-s1",
		CreateBranch: true,
	})
	if err != nil {
		t.Fatalf("AddWorktree: %v", err)
	}
	again, err := RecreateWorktree(context.Background(), repo.Root, managed, "wt-s1")
	if err != nil {
		t.Fatalf("RecreateWorktree: %v", err)
	}
	if again.Path != added.Path || again.Branch != "wt-s1" {
		t.Errorf("recreate returned %+v, want same entry as %+v", again, added)
	}
}

func TestRecreateWorktree_MismatchRefused(t *testing.T) {
	repo := setupWorktreeRepo(t)
	managed := managedPath(repo.Root, "s1")
	if _, err := AddWorktree(context.Background(), repo.Root, managed, AddWorktreeOptions{
		Branch:       "wt-s1",
		CreateBranch: true,
	}); err != nil {
		t.Fatalf("AddWorktree: %v", err)
	}
	_, err := RecreateWorktree(context.Background(), repo.Root, managed, "main")
	if !errors.Is(err, ErrWorktreeBranchMismatch) {
		t.Fatalf("err = %v, want ErrWorktreeBranchMismatch", err)
	}
}

func TestRecreateWorktree_AfterRemovalFromKeptBranch(t *testing.T) {
	repo := setupWorktreeRepo(t)
	managed := managedPath(repo.Root, "s1")
	if _, err := AddWorktree(context.Background(), repo.Root, managed, AddWorktreeOptions{
		Branch:       "wt-s1",
		CreateBranch: true,
	}); err != nil {
		t.Fatalf("AddWorktree: %v", err)
	}
	if err := RemoveWorktree(context.Background(), repo.Root, managed, RemoveWorktreeOptions{}); err != nil {
		t.Fatalf("RemoveWorktree: %v", err)
	}
	recreated, err := RecreateWorktree(context.Background(), repo.Root, managed, "wt-s1")
	if err != nil {
		t.Fatalf("RecreateWorktree: %v", err)
	}
	if recreated.Branch != "wt-s1" {
		t.Errorf("recreated branch = %q, want wt-s1", recreated.Branch)
	}
	if got := rawGitOut(t, managed, "rev-parse", "--abbrev-ref", "HEAD"); got != "wt-s1" {
		t.Errorf("recreated HEAD = %q, want wt-s1", got)
	}
}

func TestRecreateWorktree_PrunesStaleMetadata(t *testing.T) {
	repo := setupWorktreeRepo(t)
	managed := managedPath(repo.Root, "s1")
	if _, err := AddWorktree(context.Background(), repo.Root, managed, AddWorktreeOptions{
		Branch:       "wt-s1",
		CreateBranch: true,
	}); err != nil {
		t.Fatalf("AddWorktree: %v", err)
	}
	if err := os.RemoveAll(managed); err != nil {
		t.Fatalf("removing dir: %v", err)
	}
	recreated, err := RecreateWorktree(context.Background(), repo.Root, managed, "wt-s1")
	if err != nil {
		t.Fatalf("RecreateWorktree over stale metadata: %v", err)
	}
	if recreated.Branch != "wt-s1" {
		t.Errorf("recreated branch = %q, want wt-s1", recreated.Branch)
	}
}

func TestRecreateWorktree_MissingBranchExplicit(t *testing.T) {
	repo := setupWorktreeRepo(t)
	_, err := RecreateWorktree(context.Background(), repo.Root, managedPath(repo.Root, "s1"), "never-existed")
	if !errors.Is(err, ErrBranchMissing) {
		t.Fatalf("err = %v, want ErrBranchMissing", err)
	}
}

func TestRecreateWorktree_RefusesForeignDir(t *testing.T) {
	repo := setupWorktreeRepo(t)
	managed := managedPath(repo.Root, "s1")
	repo.Git(t, "branch", "feature")
	if err := os.MkdirAll(managed, 0o755); err != nil {
		t.Fatalf("mkdir foreign: %v", err)
	}
	_, err := RecreateWorktree(context.Background(), repo.Root, managed, "feature")
	if !errors.Is(err, ErrWorktreeExists) {
		t.Fatalf("err = %v, want ErrWorktreeExists", err)
	}
}

// ---------------------------------------------------------------------------
// info/exclude hardening: idempotence, .gitignore immutability
// ---------------------------------------------------------------------------

func TestManagedExclude_IdempotentAcrossAdds(t *testing.T) {
	repo := setupWorktreeRepo(t)
	for _, name := range []string{"s1", "s2"} {
		if _, err := AddWorktree(context.Background(), repo.Root, managedPath(repo.Root, name), AddWorktreeOptions{
			Branch:       "wt-" + name,
			CreateBranch: true,
		}); err != nil {
			t.Fatalf("AddWorktree %s: %v", name, err)
		}
	}
	exclude, err := os.ReadFile(repo.GitDirFile("info/exclude"))
	if err != nil {
		t.Fatalf("read exclude: %v", err)
	}
	count := strings.Count(string(exclude), worktreesExcludePattern)
	if count != 1 {
		t.Errorf("exclude contains %d copies of %q, want 1:\n%s", count, worktreesExcludePattern, exclude)
	}
}

func TestManagedExclude_GitignoreUntouched(t *testing.T) {
	repo := setupWorktreeRepo(t)
	ignorePath := filepath.Join(repo.Root, ".gitignore")
	before := []byte("build/\n*.log\n")
	if err := os.WriteFile(ignorePath, before, 0o644); err != nil {
		t.Fatalf("write .gitignore: %v", err)
	}
	if _, err := AddWorktree(context.Background(), repo.Root, managedPath(repo.Root, "s1"), AddWorktreeOptions{
		Branch:       "wt-s1",
		CreateBranch: true,
	}); err != nil {
		t.Fatalf("AddWorktree: %v", err)
	}
	after, err := os.ReadFile(ignorePath)
	if err != nil {
		t.Fatalf("read .gitignore: %v", err)
	}
	if !bytes.Equal(after, before) {
		t.Errorf(".gitignore modified:\nbefore %q\nafter  %q", before, after)
	}
	exclude, err := os.ReadFile(repo.GitDirFile("info/exclude"))
	if err != nil || !strings.Contains(string(exclude), worktreesExcludePattern) {
		t.Errorf("exclude missing pattern (err=%v):\n%s", err, exclude)
	}
}

// ---------------------------------------------------------------------------
// Hardening canaries: common config + per-worktree config overlay
// ---------------------------------------------------------------------------

// TestWorktreeOps_CommonConfigCanary arms clean/smudge filters in the
// repository's common .git/config and drives the full primitive lifecycle
// (list → add → recreate → remove). `git worktree add` performs a
// checkout, which is exactly where an unarmed smudge filter would execute;
// the canary must never fire and every operation must still succeed.
func TestWorktreeOps_CommonConfigCanary(t *testing.T) {
	gittest.RequirePOSIXShell(t)
	canary := gittest.NewCanary(t)
	repo := setupWorktreeRepo(t)

	// Arm the filter only AFTER the fixture commit: the tracked file.txt is
	// stored unfiltered, and any checkout after this point (worktree add)
	// would smudge through the canary if neutralization failed.
	smudge := canary.Plant(t, "smudge", gittest.FilterBody)
	clean := canary.Plant(t, "clean", gittest.FilterBody)
	repo.AppendConfig(t, fmt.Sprintf(
		"[filter \"canary\"]\n\tclean = %s\n\tsmudge = %s\n\trequired = true\n", clean, smudge))

	managed := managedPath(repo.Root, "s1")
	if _, err := AddWorktree(context.Background(), repo.Root, managed, AddWorktreeOptions{
		Branch:       "wt-s1",
		CreateBranch: true,
	}); err != nil {
		t.Fatalf("AddWorktree with armed filter: %v", err)
	}
	canary.RequireNotFired(t)
	if got, err := os.ReadFile(filepath.Join(managed, "file.txt")); err != nil || string(got) != "seed\n" {
		t.Errorf("checked-out file not passed through unchanged: %q (%v)", got, err)
	}

	if _, err := RecreateWorktree(context.Background(), repo.Root, managed, "wt-s1"); err != nil {
		t.Fatalf("RecreateWorktree with armed filter: %v", err)
	}
	if err := RemoveWorktree(context.Background(), repo.Root, managed, RemoveWorktreeOptions{}); err != nil {
		t.Fatalf("RemoveWorktree with armed filter: %v", err)
	}
	if _, err := ListWorktrees(context.Background(), repo.Root); err != nil {
		t.Fatalf("ListWorktrees with armed filter: %v", err)
	}
	canary.RequireNotFired(t)
}

// TestWorktreeOps_WorktreeConfigOverlayCanary pins that git invocations
// rooted INSIDE a linked worktree neutralize command-bearing keys from the
// per-worktree config.worktree overlay: the scan merges the overlay (core
// .git/config + config.worktree) and the neutralizing -c pins for the
// overlay's filter appear in the spawned argv.
func TestWorktreeOps_WorktreeConfigOverlayCanary(t *testing.T) {
	gittest.RequirePOSIXShell(t)
	canary := gittest.NewCanary(t)
	repo := setupWorktreeRepo(t)
	managed := managedPath(repo.Root, "s1")
	if _, err := AddWorktree(context.Background(), repo.Root, managed, AddWorktreeOptions{
		Branch:       "wt-s1",
		CreateBranch: true,
	}); err != nil {
		t.Fatalf("AddWorktree: %v", err)
	}

	// Arm the filter in the per-worktree overlay only (not the common
	// config): .git/worktrees/s1/config.worktree. git reads the overlay
	// only when extensions.worktreeConfig is enabled — enabling it in the
	// common config is what makes the overlay live for both git and the
	// scanner.
	repo.Git(t, "config", "extensions.worktreeConfig", "true")
	smudge := canary.Plant(t, "smudge", gittest.FilterBody)
	overlay := repo.GitDirFile(filepath.FromSlash("worktrees/s1/config.worktree"))
	overlayContent := fmt.Sprintf("[filter \"canary\"]\n\tsmudge = %s\n\trequired = true\n", smudge)
	if err := os.MkdirAll(filepath.Dir(overlay), 0o755); err != nil {
		t.Fatalf("mkdir overlay dir: %v", err)
	}
	if err := os.WriteFile(overlay, []byte(overlayContent), 0o644); err != nil {
		t.Fatalf("write overlay config: %v", err)
	}

	// Dirty-check runs `git status --porcelain` rooted at the worktree;
	// with the recording fake git on PATH its argv shows the neutralizing
	// pin derived from the overlay scan.
	readLog := gittest.InstallFakeGit(t)
	dirty, err := worktreeDirty(context.Background(), managed)
	if err != nil {
		t.Fatalf("worktreeDirty with overlay config: %v", err)
	}
	if dirty {
		t.Error("worktreeDirty = true against fake git, want false")
	}
	argv := gittest.SectionLines(readLog(), "ARGV")
	pinned := false
	for i := 0; i+1 < len(argv); i++ {
		if argv[i] == "-c" && strings.HasPrefix(argv[i+1], "filter.canary.") {
			pinned = true
		}
	}
	if !pinned {
		t.Errorf("no neutralizing -c pin for overlay filter in argv: %v", argv)
	}
	canary.RequireNotFired(t)
}
