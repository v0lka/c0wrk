package worktrees

import (
	"context"
	"errors"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"

	"github.com/v0lka/c0wrk/core/workspace"
	"github.com/v0lka/c0wrk/internal/gittest"
)

func setupOwnerRepo(t *testing.T) (owner *Owner, repo *gittest.Repo) {
	t.Helper()
	gittest.RequireGit(t)
	root := filepath.Join(t.TempDir(), "repo")
	repo = gittest.InitRepo(t, root, "seed\n")
	return NewOwner(nil), repo
}

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

func TestOwner_ProvisionNewBranch(t *testing.T) {
	owner, repo := setupOwnerRepo(t)
	info, err := owner.ProvisionNewBranch(context.Background(), repo.Root, "s1", "wt-s1", "main")
	if err != nil {
		t.Fatalf("ProvisionNewBranch: %v", err)
	}
	want := filepath.Join(repo.Root, ".worktrees", "s1")
	if info.Path != want || info.Kind != workspace.WorktreeManaged {
		t.Errorf("info = %+v, want managed entry at %s", info, want)
	}
	if got := rawGitOut(t, want, "rev-parse", "--abbrev-ref", "HEAD"); got != "wt-s1" {
		t.Errorf("HEAD = %q, want wt-s1", got)
	}
	// Centralized exclusion install is part of provisioning.
	exclude, err := os.ReadFile(repo.GitDirFile("info/exclude"))
	if err != nil || !strings.Contains(string(exclude), "/.worktrees/") {
		t.Errorf("info/exclude missing /.worktrees/ (err=%v)", err)
	}
}

func TestOwner_ProvisionBranchExplicitFailures(t *testing.T) {
	owner, repo := setupOwnerRepo(t)
	ctx := context.Background()
	repo.Git(t, "branch", "feature")

	if _, err := owner.ProvisionBranch(ctx, repo.Root, "s1", "main"); !errors.Is(err, workspace.ErrBranchBusy) {
		t.Errorf("held branch err = %v, want ErrBranchBusy", err)
	}
	if _, err := owner.ProvisionBranch(ctx, repo.Root, "s2", "ghost"); !errors.Is(err, workspace.ErrBranchMissing) {
		t.Errorf("missing branch err = %v, want ErrBranchMissing", err)
	}
	if _, err := owner.ProvisionNewBranch(ctx, repo.Root, "s3", "feature", ""); !errors.Is(err, workspace.ErrBranchExists) {
		t.Errorf("create-over-existing err = %v, want ErrBranchExists", err)
	}
	if !IsExplicitFailure(errors.Join(workspace.ErrBranchBusy, errors.New("x"))) {
		t.Error("IsExplicitFailure(ErrBranchBusy) = false, want true")
	}
	if IsExplicitFailure(errors.New("boom")) {
		t.Error("IsExplicitFailure(generic) = true, want false")
	}
}

func TestOwner_ProvisionValidatesNamesThroughCentralPath(t *testing.T) {
	owner, repo := setupOwnerRepo(t)
	for _, name := range []string{"../escape", "sub/dir", ".hidden", ""} {
		if _, err := owner.ProvisionNewBranch(context.Background(), repo.Root, name, "wt-x", ""); err == nil {
			t.Errorf("name %q accepted, want rejection", name)
		}
	}
}

func TestOwner_ListAndInspectClassification(t *testing.T) {
	owner, repo := setupOwnerRepo(t)
	ctx := context.Background()
	repo.Git(t, "branch", "feature")
	ext := filepath.Join(gittest.TempDir(t), "ext")
	repo.Git(t, "worktree", "add", ext, "feature")
	if _, err := owner.ProvisionNewBranch(ctx, repo.Root, "s1", "wt-s1", ""); err != nil {
		t.Fatalf("ProvisionNewBranch: %v", err)
	}

	trees, err := owner.List(ctx, repo.Root)
	if err != nil {
		t.Fatalf("List: %v", err)
	}
	kinds := map[string]workspace.WorktreeKind{}
	for _, w := range trees {
		kinds[w.Path] = w.Kind
	}
	if kinds[repo.Root] != workspace.WorktreeMain {
		t.Errorf("main kind = %v", kinds[repo.Root])
	}
	if kinds[filepath.Join(repo.Root, ".worktrees", "s1")] != workspace.WorktreeManaged {
		t.Errorf("managed kind = %v", kinds[filepath.Join(repo.Root, ".worktrees", "s1")])
	}
	if kinds[ext] != workspace.WorktreeExternal {
		t.Errorf("external kind = %v", kinds[ext])
	}

	entry, err := owner.Inspect(ctx, repo.Root, "s1")
	if err != nil || entry == nil || entry.Branch != "wt-s1" {
		t.Errorf("Inspect = %v, %v", entry, err)
	}
	if _, err := owner.Inspect(ctx, repo.Root, "nope"); !errors.Is(err, workspace.ErrWorktreeNotLinked) {
		t.Errorf("Inspect missing err = %v, want ErrWorktreeNotLinked", err)
	}
}

// TestOwner_ReleaseKeepsBranch pins the session-deletion invariant: the
// ownership coordinator's release removes the tree and its metadata only —
// the branch survives clean, dirty and locked releases alike.
func TestOwner_ReleaseKeepsBranch(t *testing.T) {
	owner, repo := setupOwnerRepo(t)
	ctx := context.Background()

	// Clean release.
	if _, err := owner.ProvisionNewBranch(ctx, repo.Root, "s1", "wt-s1", ""); err != nil {
		t.Fatalf("provision s1: %v", err)
	}
	if err := owner.Release(ctx, repo.Root, "s1", workspace.RemoveWorktreeOptions{}); err != nil {
		t.Fatalf("Release s1: %v", err)
	}
	if got := rawGitOut(t, repo.Root, "rev-parse", "--verify", "refs/heads/wt-s1"); got == "" {
		t.Fatal("clean release deleted branch wt-s1")
	}

	// Dirty release: explicit failure first, forced release after.
	if _, err := owner.ProvisionNewBranch(ctx, repo.Root, "s2", "wt-s2", ""); err != nil {
		t.Fatalf("provision s2: %v", err)
	}
	if err := os.WriteFile(filepath.Join(repo.Root, ".worktrees", "s2", "file.txt"), []byte("dirty\n"), 0o644); err != nil {
		t.Fatalf("dirtying: %v", err)
	}
	if err := owner.Release(ctx, repo.Root, "s2", workspace.RemoveWorktreeOptions{}); !errors.Is(err, workspace.ErrWorktreeDirty) {
		t.Fatalf("dirty release err = %v, want ErrWorktreeDirty", err)
	}
	if err := owner.Release(ctx, repo.Root, "s2", workspace.RemoveWorktreeOptions{Force: true}); err != nil {
		t.Fatalf("forced release: %v", err)
	}
	if got := rawGitOut(t, repo.Root, "rev-parse", "--verify", "refs/heads/wt-s2"); got == "" {
		t.Fatal("forced release deleted branch wt-s2")
	}

	// Locked release: explicit failure first, unlocked release after.
	if _, err := owner.ProvisionNewBranch(ctx, repo.Root, "s3", "wt-s3", ""); err != nil {
		t.Fatalf("provision s3: %v", err)
	}
	repo.Git(t, "worktree", "lock", filepath.Join(repo.Root, ".worktrees", "s3"))
	if err := owner.Release(ctx, repo.Root, "s3", workspace.RemoveWorktreeOptions{}); !errors.Is(err, workspace.ErrWorktreeLocked) {
		t.Fatalf("locked release err = %v, want ErrWorktreeLocked", err)
	}
	if err := owner.Release(ctx, repo.Root, "s3", workspace.RemoveWorktreeOptions{Unlock: true}); err != nil {
		t.Fatalf("unlocked release: %v", err)
	}
	if got := rawGitOut(t, repo.Root, "rev-parse", "--verify", "refs/heads/wt-s3"); got == "" {
		t.Fatal("unlocked release deleted branch wt-s3")
	}

	trees, err := owner.List(ctx, repo.Root)
	if err != nil {
		t.Fatalf("List: %v", err)
	}
	if len(trees) != 1 {
		t.Errorf("worktrees after releases = %d, want 1 (main only): %+v", len(trees), trees)
	}
}

func TestOwner_ReleaseRefusesUnknownNames(t *testing.T) {
	owner, repo := setupOwnerRepo(t)
	ctx := context.Background()
	if err := owner.Release(ctx, repo.Root, "ghost", workspace.RemoveWorktreeOptions{Force: true}); !errors.Is(err, workspace.ErrWorktreeNotLinked) {
		t.Fatalf("unknown err = %v, want ErrWorktreeNotLinked", err)
	}
	// An external worktree exists in the repository, but the coordinator
	// can only ever operate on derived managed paths (<root>/.worktrees/*):
	// releasing any name resolves there, so external trees are structurally
	// out of reach.
	repo.Git(t, "branch", "feature")
	ext := filepath.Join(t.TempDir(), "ext")
	repo.Git(t, "worktree", "add", ext, "feature")
	trees, err := owner.List(ctx, repo.Root)
	if err != nil {
		t.Fatalf("List: %v", err)
	}
	var external int
	for _, w := range trees {
		if w.Kind == workspace.WorktreeExternal {
			external++
		}
	}
	if external != 1 {
		t.Fatalf("external worktrees = %d, want 1", external)
	}
	if err := owner.Release(ctx, repo.Root, filepath.Base(ext), workspace.RemoveWorktreeOptions{Force: true}); !errors.Is(err, workspace.ErrWorktreeNotLinked) {
		t.Fatalf("external-name release err = %v, want ErrWorktreeNotLinked (derived managed path only)", err)
	}
	// The external tree itself must be untouched.
	if _, err := os.Stat(filepath.Join(ext, "file.txt")); err != nil {
		t.Errorf("external worktree damaged by refused release: %v", err)
	}
}

func TestMain(m *testing.M) {
	gittest.SuppressGitAutoMaintenance()
	os.Exit(m.Run())
}

func TestOwner_RecreateRestoreFlow(t *testing.T) {
	owner, repo := setupOwnerRepo(t)
	ctx := context.Background()
	if _, err := owner.ProvisionNewBranch(ctx, repo.Root, "s1", "wt-s1", ""); err != nil {
		t.Fatalf("provision: %v", err)
	}
	if err := owner.Release(ctx, repo.Root, "s1", workspace.RemoveWorktreeOptions{}); err != nil {
		t.Fatalf("release: %v", err)
	}
	info, err := owner.Recreate(ctx, repo.Root, "s1", "wt-s1")
	if err != nil {
		t.Fatalf("Recreate: %v", err)
	}
	if info.Branch != "wt-s1" || info.Kind != workspace.WorktreeManaged {
		t.Errorf("recreated = %+v", info)
	}
	if _, err := owner.Recreate(ctx, repo.Root, "s1", "gone-branch"); !errors.Is(err, workspace.ErrWorktreeBranchMismatch) {
		t.Errorf("mismatched recreate err = %v, want ErrWorktreeBranchMismatch", err)
	}
	if _, err := owner.Recreate(ctx, repo.Root, "s2", "never"); !errors.Is(err, workspace.ErrBranchMissing) {
		t.Errorf("missing branch recreate err = %v, want ErrBranchMissing", err)
	}
}
