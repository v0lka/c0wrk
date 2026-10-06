package session

// Manager-level contract tests for the managed-workspace restore hook
// (ADR-080): a managed session restores only through a WorkspaceEnsurer, and
// an ensurer failure aborts the restore instead of silently falling back to
// the project checkout. The full Git-side behavior (recreate-from-branch,
// missing-branch refusal, warning emission) is covered end-to-end by the
// backend lifecycle tests against the real worktrees.Owner.

import (
	"context"
	"errors"
	"strings"
	"testing"
)

func TestGetOrRestore_ManagedSessionRequiresEnsurer(t *testing.T) {
	s, cleanup := setupTestStore(t)
	t.Cleanup(cleanup)
	root := bindingTestProject(t, s)
	binding := managedTestBinding(t, root, "managed", "topic")
	info := SessionInfo{ID: "needs-ensurer", ProjectID: testProjectID, Name: "Managed", WorkspaceBinding: binding}
	if err := s.SaveSession(context.Background(), info); err != nil {
		t.Fatal(err)
	}

	mgr, _, _ := testManager(t)
	mgr.SetSessionStore(s)
	mgr.SetProjectResolver(func(string) (string, error) { return root, nil })
	// Deliberately NO SetWorkspaceEnsurer.

	if sess, ok := mgr.GetSession(info.ID); ok {
		t.Fatalf("managed session must not restore without an ensurer, got %+v", sess)
	}
	if mgr.HasSession(info.ID) {
		t.Fatal("failed restore must not leave an in-memory session behind")
	}
}

func TestGetOrRestore_ManagedSessionEnsurerFailureAbortsRestore(t *testing.T) {
	s, cleanup := setupTestStore(t)
	t.Cleanup(cleanup)
	root := bindingTestProject(t, s)
	binding := managedTestBinding(t, root, "managed", "topic")
	info := SessionInfo{ID: "ensurer-fails", ProjectID: testProjectID, Name: "Managed", WorkspaceBinding: binding}
	if err := s.SaveSession(context.Background(), info); err != nil {
		t.Fatal(err)
	}

	mgr, _, _ := testManager(t)
	mgr.SetSessionStore(s)
	mgr.SetProjectResolver(func(string) (string, error) { return root, nil })
	mgr.SetWorkspaceEnsurer(func(context.Context, string, *WorkspaceBinding) (bool, error) {
		return false, errors.New("branch gone")
	})

	if sess, ok := mgr.GetSession(info.ID); ok {
		t.Fatalf("ensurer failure must abort the restore, got %+v", sess)
	}
	if mgr.HasSession(info.ID) {
		t.Fatal("aborted restore must not leave an in-memory session behind")
	}
}

func TestGetOrRestore_EnsurerReceivesDerivedManagedBinding(t *testing.T) {
	s, cleanup := setupTestStore(t)
	t.Cleanup(cleanup)
	root := bindingTestProject(t, s)
	binding := managedTestBinding(t, root, "managed", "topic")
	info := SessionInfo{ID: "ensurer-seen", ProjectID: testProjectID, Name: "Managed", WorkspaceBinding: binding}
	if err := s.SaveSession(context.Background(), info); err != nil {
		t.Fatal(err)
	}

	mgr, _, _ := testManager(t)
	mgr.SetSessionStore(s)
	mgr.SetProjectResolver(func(string) (string, error) { return root, nil })

	var gotRoot string
	var gotBinding *WorkspaceBinding
	var calls int
	mgr.SetWorkspaceEnsurer(func(_ context.Context, repoRoot string, b *WorkspaceBinding) (bool, error) {
		calls++
		gotRoot, gotBinding = repoRoot, b
		return false, nil
	})

	sess, ok := mgr.GetSession(info.ID)
	if !ok {
		t.Fatal("managed session should restore through the ensurer")
	}
	if calls != 1 {
		t.Fatalf("ensurer must run exactly once per restore, ran %d", calls)
	}
	if gotRoot != root {
		t.Fatalf("ensurer repo root %q != project root %q", gotRoot, root)
	}
	if gotBinding == nil || gotBinding.Kind != WorkspaceManagedWorktree || gotBinding.Branch != "topic" || gotBinding.WorkspacePath != binding.WorkspacePath {
		t.Fatalf("ensurer binding mismatch: %+v", gotBinding)
	}
	if sess.WorkspacePath != binding.WorkspacePath {
		t.Fatalf("restored workspace %q != tree %q", sess.WorkspacePath, binding.WorkspacePath)
	}
}

func TestGetOrRestore_EnsurerNeverCalledForLocalSessions(t *testing.T) {
	s, cleanup := setupTestStore(t)
	t.Cleanup(cleanup)
	root := bindingTestProject(t, s)
	info := SessionInfo{ID: "local-session", ProjectID: testProjectID, Name: "Local"}
	if err := s.SaveSession(context.Background(), info); err != nil {
		t.Fatal(err)
	}

	mgr, _, _ := testManager(t)
	mgr.SetSessionStore(s)
	mgr.SetProjectResolver(func(string) (string, error) { return root, nil })
	mgr.SetWorkspaceEnsurer(func(context.Context, string, *WorkspaceBinding) (bool, error) {
		t.Fatal("ensurer must not run for local sessions")
		return false, nil
	})

	sess, ok := mgr.GetSession(info.ID)
	if !ok || sess.WorkspacePath != root {
		t.Fatalf("local session must restore to the checkout: ok=%v ws=%q", ok, sess.WorkspacePath)
	}
	if mgr.HasSession(info.ID) && !strings.HasPrefix(sess.WorkspacePath, root) {
		t.Fatal("local restore must stay within the checkout")
	}
}
