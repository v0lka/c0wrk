package backend

// Session-root coherence tests (ADR-080 step: workspace-dependent mechanisms
// key off the session's execution root, not the global active-project path).
// Reuses the wtHarness (real git repository + real FrontendAPI/manager) from
// frontend_api_worktrees_test.go.

import (
	"context"
	"os"
	"path/filepath"
	"testing"

	"github.com/v0lka/c0wrk/backend/config"
)

// TestGetSessionWorkspace_ManagedSessionOfActiveProjectReturnsOwnTree pins
// the explorer/@-file contract: a managed-worktree session of the ACTIVE
// project resolves to its OWN tree even though its WorkspacePath differs
// from the project checkout (the old path-equality membership test fell back
// to the checkout, pointing the explorer at the wrong root). A local session
// of the same project keeps resolving to the checkout, so concurrently
// selectable A/B sessions each keep their own root.
func TestGetSessionWorkspace_ManagedSessionOfActiveProjectReturnsOwnTree(t *testing.T) {
	h := newWTHarness(t, wtFactory())

	managed, err := h.api.CreateManagedSession("", true, "")
	if err != nil {
		t.Fatalf("CreateManagedSession: %v", err)
	}
	local, err := h.manager.CreateSession(h.project.ID, h.project.WorkspacePath)
	if err != nil {
		t.Fatalf("CreateSession (local): %v", err)
	}

	gotManaged, err := h.api.GetSessionWorkspace(managed.ID)
	if err != nil {
		t.Fatalf("GetSessionWorkspace(managed): %v", err)
	}
	if gotManaged != managed.WorkspaceBinding.WorkspacePath {
		t.Fatalf("managed session workspace = %q, want its own tree %q (not the checkout)",
			gotManaged, managed.WorkspaceBinding.WorkspacePath)
	}
	if gotManaged == h.project.WorkspacePath {
		t.Fatalf("managed session workspace resolved to the project checkout %q", gotManaged)
	}

	gotLocal, err := h.api.GetSessionWorkspace(local.ID)
	if err != nil {
		t.Fatalf("GetSessionWorkspace(local): %v", err)
	}
	if gotLocal != h.project.WorkspacePath {
		t.Fatalf("local session workspace = %q, want the checkout %q", gotLocal, h.project.WorkspacePath)
	}
}

// TestGetSessionWorkspace_ForeignProjectSessionDoesNotLeak: when the ACTIVE
// project is a different one, a managed session of the inactive project must
// NOT resolve to its tree — the fallback is the active project's workspace,
// exactly like the CHAT-mode foreign-session guard.
func TestGetSessionWorkspace_ForeignProjectSessionDoesNotLeak(t *testing.T) {
	h := newWTHarness(t, wtFactory())

	managed, err := h.api.CreateManagedSession("", true, "")
	if err != nil {
		t.Fatalf("CreateManagedSession: %v", err)
	}

	foreignPath := filepath.Join(t.TempDir(), "other-project")
	if err := os.MkdirAll(foreignPath, 0o755); err != nil {
		t.Fatal(err)
	}
	h.api.activeProjectMu.Lock()
	h.api.activeProjectID = "foreign-project"
	h.api.activeProjectPath = foreignPath
	h.api.activeProjectMu.Unlock()

	got, err := h.api.GetSessionWorkspace(managed.ID)
	if err != nil {
		t.Fatalf("GetSessionWorkspace: %v", err)
	}
	if got != foreignPath {
		t.Fatalf("foreign session leaked its tree: got %q, want the active project workspace %q", got, foreignPath)
	}
}

// TestGetFileDiff_WorktreeFileDiffsInItsOwnRepo pins the viewer-diff
// contract: an uncommitted change inside a session's managed worktree must
// produce a real diff. Before the fix the diff ran from the project
// checkout, where the .worktrees container is excluded (info/exclude), so
// worktree files silently reported no changes.
func TestGetFileDiff_WorktreeFileDiffsInItsOwnRepo(t *testing.T) {
	h := newWTHarness(t, wtFactory())

	managed, err := h.api.CreateManagedSession("", true, "")
	if err != nil {
		t.Fatalf("CreateManagedSession: %v", err)
	}
	tree := managed.WorkspaceBinding.WorkspacePath

	// Modify a tracked file (the seed commit's file.txt) inside the tree.
	target := filepath.Join(tree, "file.txt")
	if err := os.WriteFile(target, []byte("changed by the session\n"), 0o644); err != nil {
		t.Fatalf("modify tracked file: %v", err)
	}

	diff, err := h.api.GetFileDiff(target)
	if err != nil {
		t.Fatalf("GetFileDiff: %v", err)
	}
	if diff == "" {
		t.Fatal("GetFileDiff for a modified worktree file = empty, want the worktree's own diff")
	}

	// Containment is still enforced against the ACTIVE project root: the
	// worktree lives inside the checkout, so the RPC must not reject it.
	if _, err := h.api.GetFileDiff(filepath.Join(tree, "definitely", "relative", "no", "such", "file.txt")); err != nil {
		t.Fatalf("containment must accept in-project worktree paths: %v", err)
	}
}

// TestResolveWorkspacePath_AcceptsWorktreePaths pins the write-containment
// contract for session trees: ListDirectory/WriteFile resolve paths inside
// the managed container (the container is inside the project root by
// design), so the explorer and the editor keep working for managed sessions.
func TestResolveWorkspacePath_AcceptsWorktreePaths(t *testing.T) {
	h := newWTHarness(t, wtFactory())

	managed, err := h.api.CreateManagedSession("", true, "")
	if err != nil {
		t.Fatalf("CreateManagedSession: %v", err)
	}
	tree := managed.WorkspaceBinding.WorkspacePath
	inner := filepath.Join(tree, "src", "app.go")

	absPath, absRoot, err := h.api.resolveWorkspacePath(inner)
	if err != nil {
		t.Fatalf("resolveWorkspacePath(worktree file): %v", err)
	}
	wantAbs, absErr := filepath.Abs(inner)
	if absErr != nil || absPath != wantAbs {
		t.Fatalf("absPath = %q, want %q", absPath, wantAbs)
	}
	if absRoot != h.project.WorkspacePath {
		t.Fatalf("absRoot = %q, want the project checkout %q", absRoot, h.project.WorkspacePath)
	}

	// A path outside the project (and outside session infra) stays rejected.
	if _, _, err := h.api.resolveWorkspacePath(filepath.Join(t.TempDir(), "outside.txt")); err == nil {
		t.Fatal("resolveWorkspacePath accepted a path outside the project workspace")
	}
}

// TestManagedWorktreesDirInsideRepo documents the structural fact the
// traversal gate depends on: the managed container is nested inside the
// repository checkout, so root containment alone cannot isolate sibling
// session trees (the gate in core/tools restores that isolation).
func TestManagedWorktreesDirInsideRepo(t *testing.T) {
	h := newWTHarness(t, wtFactory())
	managed, err := h.api.CreateManagedSession("", true, "")
	if err != nil {
		t.Fatalf("CreateManagedSession: %v", err)
	}
	container := config.ManagedWorktreesDir(h.project.WorkspacePath)
	ok, err := config.IsWithinPath(h.project.WorkspacePath, container)
	if err != nil {
		t.Fatal(err)
	}
	if !ok {
		t.Fatalf("container %q must live inside the checkout %q", container, h.project.WorkspacePath)
	}
	ok, err = config.IsWithinPath(container, managed.WorkspaceBinding.WorkspacePath)
	if err != nil {
		t.Fatal(err)
	}
	if !ok {
		t.Fatalf("tree %q must live inside the container %q", managed.WorkspaceBinding.WorkspacePath, container)
	}
}

// TestGetSessionWorkspace_ResidentSessionWithoutStoreRowFallsBackToMemory
// pins the membership fallback: when a RESIDENT session's store row is
// missing (session-row persistence is asynchronous), its in-memory ProjectID
// must decide membership. A MANAGED session is the discriminating shape: its
// workspace lives inside <checkout>/.worktrees and differs from the project
// checkout, so a regressed fallback (empty project ID → membership false)
// would resolve to the checkout while the correct path resolves to the
// session's own tree.
func TestGetSessionWorkspace_ResidentSessionWithoutStoreRowFallsBackToMemory(t *testing.T) {
	h := newWTHarness(t, wtFactory())

	managed, err := h.api.CreateManagedSession("", true, "")
	if err != nil {
		t.Fatalf("CreateManagedSession: %v", err)
	}
	want := managed.WorkspaceBinding.WorkspacePath
	if want == h.project.WorkspacePath {
		t.Fatalf("precondition broken: managed workspace equals the checkout")
	}

	// Remove the persisted row: the managed session stays RESIDENT (in-memory
	// identity intact) while the store row is gone — exactly the state the
	// fallback exists for.
	if err := h.store.DeleteSession(context.Background(), managed.ID); err != nil {
		t.Fatalf("delete store row: %v", err)
	}
	if info, err := h.store.LoadSession(context.Background(), managed.ID); err != nil || info != nil {
		t.Fatalf("precondition broken: expected no store row, got info=%v err=%v", info != nil, err)
	}

	got, err := h.api.GetSessionWorkspace(managed.ID)
	if err != nil {
		t.Fatalf("GetSessionWorkspace: %v", err)
	}
	if got != want {
		t.Fatalf("resident managed session without a store row resolved to %q, want its own tree %q (a regression resolves to the checkout)", got, want)
	}
}
