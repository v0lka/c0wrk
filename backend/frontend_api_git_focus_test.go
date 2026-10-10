package backend

// Git-panel focus model tests (ADR-080's GitPanelTarget consumption):
// focus-target resolution, the worktree listing RPC with owning-session
// metadata, and the pinned-branch refusal. Everything runs against the REAL
// git binary through the production primitives (core/workspace →
// worktrees.Owner → the RPC flows under test), mirroring the lifecycle
// tests in frontend_api_worktrees_test.go.

import (
	"errors"
	"os"
	"path/filepath"
	"sort"
	"testing"

	"github.com/v0lka/c0wrk/core/workspace"
	"github.com/v0lka/c0wrk/internal/gittest"
)

// focusTestTree provisions one managed session and returns its tree path
// and pinned branch.
func focusTestTree(t *testing.T, h *wtHarness) (tree, pinned string) {
	t.Helper()
	info, err := h.api.CreateManagedSession("", true, "")
	if err != nil {
		t.Fatalf("CreateManagedSession: %v", err)
	}
	return info.WorkspaceBinding.WorkspacePath, info.WorkspaceBinding.Branch
}

func TestResolveGitRepoRoot_DefaultsToProjectCheckout(t *testing.T) {
	h := newWTHarness(t, wtFactory())

	root, err := h.api.resolveGitRepoRoot()
	if err != nil {
		t.Fatalf("resolveGitRepoRoot: %v", err)
	}
	if root != h.project.WorkspacePath {
		t.Fatalf("default focus = %q, want the project checkout %q", root, h.project.WorkspacePath)
	}

	// The resolved default focus reports as the main worktree.
	focus, err := h.api.GetGitPanelFocus()
	if err != nil {
		t.Fatalf("GetGitPanelFocus: %v", err)
	}
	if focus.Path != h.project.WorkspacePath || focus.Kind != string(workspace.WorktreeMain) || focus.Managed || focus.Pinned {
		t.Fatalf("default focus info = %+v", focus)
	}
}

func TestSetGitPanelFocus_SwitchesEveryGitRPC(t *testing.T) {
	h := newWTHarness(t, wtFactory())
	tree, pinned := focusTestTree(t, h)

	// A dirty file only the focused tree reports.
	if err := os.WriteFile(filepath.Join(tree, "focus-dirty.txt"), []byte("dirty"), 0o644); err != nil {
		t.Fatal(err)
	}

	if err := h.api.SetGitPanelFocus(tree); err != nil {
		t.Fatalf("SetGitPanelFocus(tree): %v", err)
	}

	root, err := h.api.resolveGitRepoRoot()
	if err != nil {
		t.Fatalf("resolveGitRepoRoot: %v", err)
	}
	if root != tree {
		t.Fatalf("focus root = %q, want the managed tree %q", root, tree)
	}

	// GetCurrentBranch follows the focus: the tree's pinned branch.
	bi, err := h.api.GetCurrentBranch()
	if err != nil {
		t.Fatalf("GetCurrentBranch: %v", err)
	}
	if bi.Name != pinned {
		t.Fatalf("focused branch = %q, want the pinned %q", bi.Name, pinned)
	}

	// GetGitStatus — the frontend passes the project workspace path —
	// reports the FOCUSED tree's status, not the checkout's.
	status, err := h.api.GetGitStatus(h.project.WorkspacePath)
	if err != nil {
		t.Fatalf("GetGitStatus: %v", err)
	}
	if _, ok := status[filepath.Join(tree, "focus-dirty.txt")]; !ok {
		t.Fatalf("focused tree's dirty file missing from status (keys: %v)", statusKeys(status))
	}

	// Staging operates inside the focused tree (relPath computed from it).
	if err := h.api.StageFile(filepath.Join(tree, "focus-dirty.txt")); err != nil {
		t.Fatalf("StageFile inside the focused tree: %v", err)
	}

	// A path that is not a worktree of the active project is refused.
	if err := h.api.SetGitPanelFocus(filepath.Join(t.TempDir(), "not-a-worktree")); !errors.Is(err, workspace.ErrWorktreeNotLinked) {
		t.Fatalf("foreign path error = %v, want ErrWorktreeNotLinked", err)
	}

	// Empty resets to the default focus (the checkout).
	if err := h.api.SetGitPanelFocus(""); err != nil {
		t.Fatalf("SetGitPanelFocus reset: %v", err)
	}
	root, err = h.api.resolveGitRepoRoot()
	if err != nil {
		t.Fatalf("resolveGitRepoRoot after reset: %v", err)
	}
	if root != h.project.WorkspacePath {
		t.Fatalf("focus after reset = %q, want the checkout", root)
	}
}

func TestResolveGitRepoRoot_StaleFocusFallsBackToCheckout(t *testing.T) {
	h := newWTHarness(t, wtFactory())
	tree, _ := focusTestTree(t, h)

	if err := h.api.SetGitPanelFocus(tree); err != nil {
		t.Fatalf("SetGitPanelFocus: %v", err)
	}

	// The tree is removed behind the panel's back (external removal).
	wtGit(t, h.repoRoot, "worktree", "remove", "--force", tree)

	root, err := h.api.resolveGitRepoRoot()
	if err != nil {
		t.Fatalf("resolveGitRepoRoot with a stale focus: %v", err)
	}
	if root != h.project.WorkspacePath {
		t.Fatalf("stale focus = %q, want fail-soft fallback to the checkout", root)
	}
	if got := h.api.gitFocusPathSnapshot(); got != "" {
		t.Fatalf("stale focus must be cleared, still set to %q", got)
	}
}

func TestListProjectWorktrees_MetadataAndFocus(t *testing.T) {
	h := newWTHarness(t, wtFactory())
	tree, pinned := focusTestTree(t, h)

	// An external linked tree outside the managed container.
	ext := filepath.Join(gittest.TempDir(t), "ext-tree")
	wtGit(t, h.repoRoot, "worktree", "add", "-b", "ext-branch", ext)

	if err := h.api.SetGitPanelFocus(tree); err != nil {
		t.Fatalf("SetGitPanelFocus(tree): %v", err)
	}

	trees, err := h.api.ListProjectWorktrees()
	if err != nil {
		t.Fatalf("ListProjectWorktrees: %v", err)
	}
	if len(trees) != 3 {
		t.Fatalf("worktree count = %d, want 3 (main + managed + external): %+v", len(trees), trees)
	}

	byKind := map[string]GitWorktree{}
	for _, w := range trees {
		byKind[w.Kind] = w
	}
	main, hasMain := byKind[string(workspace.WorktreeMain)]
	managed, hasManaged := byKind[string(workspace.WorktreeManaged)]
	external, hasExternal := byKind[string(workspace.WorktreeExternal)]
	if !hasMain || !hasManaged || !hasExternal {
		t.Fatalf("expected one entry per kind, got %+v", trees)
	}

	if main.Path != h.project.WorkspacePath || main.Managed || main.Pinned || main.IsFocus {
		t.Fatalf("main entry = %+v", main)
	}
	if managed.Path != tree || !managed.Managed || !managed.Pinned || !managed.IsFocus {
		t.Fatalf("managed entry = %+v", managed)
	}
	if managed.Branch != pinned {
		t.Fatalf("managed branch = %q, want the pinned %q", managed.Branch, pinned)
	}
	if external.Path != ext || external.Managed || external.Pinned || external.IsFocus {
		t.Fatalf("external entry = %+v", external)
	}

	// The owning session decorates the managed entry. Session IDs are not
	// asserted verbatim (CreateManagedSession generates them); ownership is
	// asserted through GetSessionWorkspace round-trip instead.
	if managed.SessionID == "" {
		t.Fatalf("managed entry lacks its owning session id: %+v", managed)
	}
	sessions, err := h.manager.ListSessionsByProject(h.project.ID)
	if err != nil {
		t.Fatalf("ListSessionsByProject: %v", err)
	}
	found := false
	for _, s := range sessions {
		if s.ID == managed.SessionID {
			found = true
			if s.WorkspaceBinding == nil || s.WorkspaceBinding.WorktreeName != managed.Name {
				t.Fatalf("owner binding does not match the tree name: %+v", s.WorkspaceBinding)
			}
		}
	}
	if !found {
		t.Fatalf("managed entry's session id %q owns no session of the project", managed.SessionID)
	}

	// The focus payload reports the same classification.
	focus, err := h.api.GetGitPanelFocus()
	if err != nil {
		t.Fatalf("GetGitPanelFocus: %v", err)
	}
	if focus.Path != tree || focus.Kind != string(workspace.WorktreeManaged) || !focus.Managed || !focus.Pinned {
		t.Fatalf("focus info = %+v", focus)
	}
	if focus.SessionID != managed.SessionID || focus.Branch != pinned {
		t.Fatalf("focus ownership = %+v, want session %q on %q", focus, managed.SessionID, pinned)
	}
}

func TestCheckoutBranch_PinnedRefusalOnManagedWorktree(t *testing.T) {
	h := newWTHarness(t, wtFactory())
	tree, pinned := focusTestTree(t, h)

	wtGit(t, h.repoRoot, "branch", "other-branch")
	wtGit(t, h.repoRoot, "branch", "free-branch")

	if err := h.api.SetGitPanelFocus(tree); err != nil {
		t.Fatalf("SetGitPanelFocus(tree): %v", err)
	}

	// Checking out another branch is refused for a managed session worktree.
	err := h.api.CheckoutBranch("other-branch")
	if !errors.Is(err, ErrPinnedWorktreeBranch) {
		t.Fatalf("CheckoutBranch(other) error = %v, want ErrPinnedWorktreeBranch", err)
	}
	if got := wtGit(t, tree, "rev-parse", "--abbrev-ref", "HEAD"); got != pinned {
		t.Fatalf("tree moved off its pin to %q", got)
	}

	// Checking out the pinned branch itself stays allowed (a no-op on the
	// pin — it cannot move the tree off its branch).
	if err := h.api.CheckoutBranch(pinned); err != nil {
		t.Fatalf("CheckoutBranch(pinned) = %v, want allowed", err)
	}

	// Creating a branch (checkout -b) always moves off the pin: refused.
	if err := h.api.CreateBranch("brand-new", ""); !errors.Is(err, ErrPinnedWorktreeBranch) {
		t.Fatalf("CreateBranch error = %v, want ErrPinnedWorktreeBranch", err)
	}

	// Renaming the pinned branch would change the identity the session's
	// immutable binding records: refused.
	if err := h.api.RenameBranch(pinned, "renamed"); !errors.Is(err, ErrPinnedWorktreeBranch) {
		t.Fatalf("RenameBranch(pinned) error = %v, want ErrPinnedWorktreeBranch", err)
	}

	// Renaming an unrelated branch from the managed focus stays allowed.
	if err := h.api.RenameBranch("free-branch", "free-branch-2"); err != nil {
		t.Fatalf("RenameBranch(free) = %v, want allowed", err)
	}

	// The local checkout keeps normal checkout.
	if err := h.api.SetGitPanelFocus(""); err != nil {
		t.Fatalf("SetGitPanelFocus reset: %v", err)
	}
	if err := h.api.CheckoutBranch("other-branch"); err != nil {
		t.Fatalf("CheckoutBranch on the checkout = %v, want allowed", err)
	}

	// An external linked tree keeps normal checkout too.
	ext := filepath.Join(gittest.TempDir(t), "ext-tree")
	wtGit(t, h.repoRoot, "worktree", "add", ext)
	if err := h.api.SetGitPanelFocus(ext); err != nil {
		t.Fatalf("SetGitPanelFocus(ext): %v", err)
	}
	if err := h.api.CheckoutBranch("free-branch-2"); err != nil {
		t.Fatalf("CheckoutBranch on an external tree = %v, want allowed", err)
	}
}

func TestGetGitStatus_FollowsFocusForExternalTree(t *testing.T) {
	h := newWTHarness(t, wtFactory())

	// An external linked tree with a dirty file: outside the project
	// workspace, so containment must accept the focus root.
	ext := filepath.Join(gittest.TempDir(t), "ext-tree")
	wtGit(t, h.repoRoot, "worktree", "add", ext)
	if err := os.WriteFile(filepath.Join(ext, "ext-dirty.txt"), []byte("dirty"), 0o644); err != nil {
		t.Fatal(err)
	}
	if err := h.api.SetGitPanelFocus(ext); err != nil {
		t.Fatalf("SetGitPanelFocus(ext): %v", err)
	}

	status, err := h.api.GetGitStatus(h.project.WorkspacePath)
	if err != nil {
		t.Fatalf("GetGitStatus with an external focus: %v", err)
	}
	if _, ok := status[filepath.Join(ext, "ext-dirty.txt")]; !ok {
		t.Fatalf("external focus's dirty file missing from status (keys: %v)", statusKeys(status))
	}

	// A path inside neither the workspace nor the focus root stays refused.
	if _, err := h.api.GetGitStatus(t.TempDir()); err == nil {
		t.Fatal("GetGitStatus outside workspace and focus must be refused")
	}
}

// statusKeys returns the sorted status map keys for failure messages.
func statusKeys(status map[string]GitStatusEntry) []string {
	keys := make([]string, 0, len(status))
	for k := range status {
		keys = append(keys, k)
	}
	sort.Strings(keys)
	return keys
}

// TestInvalidateAllGitCaches_ClearsGitPanelFocus pins review [78]: the
// Git-panel focus belongs to the previous project's repository, so the
// project-switch funnel (invalidateAllGitCaches, called from
// switchProjectActivate) must drop it — otherwise every pathless git RPC
// resolves into the previous project's repository until the frontend's
// asynchronous focus re-apply lands.
func TestInvalidateAllGitCaches_ClearsGitPanelFocus(t *testing.T) {
	h := newWTHarness(t, wtFactory())
	tree, _ := focusTestTree(t, h)

	if err := h.api.SetGitPanelFocus(tree); err != nil {
		t.Fatalf("SetGitPanelFocus(tree): %v", err)
	}
	if got, err := h.api.resolveGitRepoRoot(); err != nil || got != tree {
		t.Fatalf("focus resolution after SetGitPanelFocus = %q, %v; want %q", got, err, tree)
	}

	h.api.invalidateAllGitCaches()

	got, err := h.api.resolveGitRepoRoot()
	if err != nil {
		t.Fatalf("resolveGitRepoRoot after project-switch invalidation: %v", err)
	}
	if got != h.project.WorkspacePath {
		t.Fatalf("focus after project-switch invalidation = %q, want the (new) project checkout %q", got, h.project.WorkspacePath)
	}
}
