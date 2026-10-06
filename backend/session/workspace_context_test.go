package session

import (
	"context"
	"reflect"
	"testing"
)

func TestManagedSessionDraftAndRestore(t *testing.T) {
	s, cleanup := setupTestStore(t)
	t.Cleanup(cleanup)
	root := bindingTestProject(t, s)
	m, events, _ := testManager(t)
	binding := managedTestBinding(t, root, "managed", "topic")
	draft := NewSessionDraft(testProjectID, binding)
	if len(m.sessions) != 0 || len(events) != 0 {
		t.Fatal("draft created runtime state")
	}
	binding.Branch = "mutated-after-draft"
	info, err := m.CreateSessionFromDraft(draft, root)
	if err != nil {
		t.Fatal(err)
	}
	runtime, ok := m.GetSession(info.ID)
	if !ok || runtime.WorkspacePath != draft.WorkspaceBinding.WorkspacePath || runtime.ProjectPath != root {
		t.Fatalf("execution/project not separated: %+v", runtime)
	}
	info.WorkspaceBinding.Branch = "mutated-response"
	if runtime.WorkspaceBinding().Branch != "topic" {
		t.Fatal("DTO mutated runtime binding")
	}
	info.WorkspaceBinding = runtime.WorkspaceBinding()
	if err := s.SaveSession(context.Background(), *info); err != nil {
		t.Fatal(err)
	}
	m.Shutdown()
	restored, _, _ := testManager(t)
	restored.SetSessionStore(s)
	restored.SetProjectResolver(func(string) (string, error) { return root, nil })
	// Managed restores require a workspace ensurer (fail-closed without one);
	// this stub stands in for the backend worktree owner.
	restored.SetWorkspaceEnsurer(func(context.Context, string, *WorkspaceBinding) (bool, error) {
		return false, nil
	})
	path, ok := restored.WorkspacePathFor(context.Background(), info.ID)
	if !ok || path != draft.WorkspaceBinding.WorkspacePath || len(restored.sessions) != 0 {
		t.Fatalf("lightweight lookup: %q %v", path, ok)
	}
	sess, ok := restored.GetSession(info.ID)
	if !ok || sess.WorkspacePath != path || sess.ProjectPath != root || !reflect.DeepEqual(sess.WorkspaceBinding(), info.WorkspaceBinding) {
		t.Fatalf("restore: %+v", sess)
	}
	listed := restored.ListSessions()
	if len(listed) != 1 || !reflect.DeepEqual(listed[0].WorkspaceBinding, info.WorkspaceBinding) {
		t.Fatalf("runtime list: %+v", listed)
	}
}

func TestSessionContextsSeparateGitFocus(t *testing.T) {
	root := t.TempDir()
	binding := managedTestBinding(t, root, "managed", "topic")
	info := SessionInfo{ProjectID: testProjectID, WorkspaceBinding: binding}
	target := GitPanelTarget{ProjectID: testProjectID, WorkspacePath: root}
	contexts, err := ResolveSessionContexts(info, root, "", &target)
	if err != nil {
		t.Fatal(err)
	}
	if contexts.Project.RepositoryPath != root || contexts.ExecutionWorkspace != binding.WorkspacePath || contexts.GitPanel.WorkspacePath != root {
		t.Fatalf("contexts collapsed: %+v", contexts)
	}
	target.WorkspacePath = t.TempDir()
	if _, err := ResolveSessionContexts(info, root, "", &target); err == nil {
		t.Fatal("accepted outside Git focus")
	}
	target = GitPanelTarget{ProjectID: "other", WorkspacePath: root}
	if _, err := ResolveSessionContexts(info, root, "", &target); err == nil {
		t.Fatal("accepted cross-project Git focus")
	}
	if binding.WorkspacePath == root {
		t.Fatal("focus changed execution binding")
	}
}
