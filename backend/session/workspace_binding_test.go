package session

import (
	"context"
	"encoding/json"
	"os"
	"path/filepath"
	"reflect"
	"testing"

	"github.com/v0lka/c0wrk/backend/config"
	"github.com/v0lka/c0wrk/backend/project"
)

func bindingTestProject(t *testing.T, s *SQLiteSessionStore) string {
	t.Helper()
	root := t.TempDir()
	if _, err := s.db.ExecContext(context.Background(), `UPDATE projects SET workspace_path = ? WHERE id = ?`, root, testProjectID); err != nil {
		t.Fatal(err)
	}
	return root
}

func managedTestBinding(t *testing.T, root, name, branch string) *WorkspaceBinding {
	t.Helper()
	path, err := config.ManagedWorktreePath(root, name)
	if err != nil {
		t.Fatal(err)
	}
	return &WorkspaceBinding{Kind: WorkspaceManagedWorktree, WorkspacePath: path, WorktreeName: name, Branch: branch}
}

func TestWorkspaceBindingRoundTripAndFork(t *testing.T) {
	s, cleanup := setupTestStore(t)
	defer cleanup()
	root := bindingTestProject(t, s)
	ctx := context.Background()
	binding := managedTestBinding(t, root, "source", "feature/topic")
	info := SessionInfo{ID: "source", ProjectID: testProjectID, Name: "Source", CreatedAt: "2026-01-01T00:00:00Z", WorkspaceBinding: binding}
	if err := s.SaveSession(ctx, info); err != nil {
		t.Fatal(err)
	}
	loaded, err := s.LoadSession(ctx, info.ID)
	if err != nil || !reflect.DeepEqual(loaded.WorkspaceBinding, binding) {
		t.Fatalf("load: %+v %v", loaded, err)
	}
	for _, list := range []func(context.Context) ([]SessionInfo, error){s.ListSessions, func(ctx context.Context) ([]SessionInfo, error) { return s.ListSessionsByProject(ctx, testProjectID) }} {
		rows, err := list(ctx)
		if err != nil || len(rows) != 1 || !reflect.DeepEqual(rows[0].WorkspaceBinding, binding) {
			t.Fatalf("list: %+v %v", rows, err)
		}
	}
	if _, err := s.ForkSession(ctx, info.ID, nil); err == nil {
		t.Fatal("managed fork shared source binding")
	}
	forkBinding := managedTestBinding(t, root, "source-fork-12345678", "source-fork-12345678")
	fork, err := s.ForkSessionWithBinding(ctx, info.ID, "fork", forkBinding, nil)
	if err != nil || !reflect.DeepEqual(fork.WorkspaceBinding, forkBinding) {
		t.Fatalf("fork: %+v %v", fork, err)
	}
	forkLoaded, err := s.LoadSession(ctx, fork.ID)
	if err != nil || !reflect.DeepEqual(forkLoaded.WorkspaceBinding, forkBinding) {
		t.Fatalf("reload fork: %+v %v", forkLoaded, err)
	}
	info.Name = "Renamed"
	if err := s.SaveSession(ctx, info); err != nil {
		t.Fatal(err)
	}
	if err := s.ArchiveSession(ctx, info.ID, true); err != nil {
		t.Fatal(err)
	}
	loaded, err = s.LoadSession(ctx, info.ID)
	if err != nil || !loaded.Archived || !reflect.DeepEqual(loaded.WorkspaceBinding, binding) {
		t.Fatalf("archive: %+v %v", loaded, err)
	}
}

func TestWorkspaceBindingRejectsInvalidAndMutation(t *testing.T) {
	s, cleanup := setupTestStore(t)
	defer cleanup()
	root := bindingTestProject(t, s)
	ctx := context.Background()
	base := SessionInfo{ID: "session", ProjectID: testProjectID, CreatedAt: "2026-01-01T00:00:00Z", WorkspaceBinding: managedTestBinding(t, root, "owned", "topic")}
	if err := s.SaveSession(ctx, base); err != nil {
		t.Fatal(err)
	}
	for _, change := range []func(*SessionInfo){
		func(i *SessionInfo) { i.WorkspaceBinding.Branch = "other" },
		func(i *SessionInfo) { i.WorkspaceBinding.WorktreeName = "../escape" },
		func(i *SessionInfo) { i.WorkspaceBinding.Kind = "unknown" },
		func(i *SessionInfo) { i.ProjectID = "missing" },
		func(i *SessionInfo) { i.ID = "" },
		func(i *SessionInfo) { i.WorkspaceBinding = nil },
	} {
		i := base
		i.WorkspaceBinding = cloneWorkspaceBinding(base.WorkspaceBinding)
		change(&i)
		if err := s.SaveSession(ctx, i); err == nil {
			t.Fatalf("accepted invalid mutation: %+v", i)
		}
	}
	// A tampered stored path is not a binding change: the execution path is
	// derived, so the save succeeds and rewrites the canonical value.
	tampered := base
	tampered.WorkspaceBinding = cloneWorkspaceBinding(base.WorkspaceBinding)
	tampered.WorkspaceBinding.WorkspacePath = root
	if err := s.SaveSession(ctx, tampered); err != nil {
		t.Fatalf("rejected path re-derivation: %v", err)
	}
	canonical, err := s.LoadSession(ctx, base.ID)
	if err != nil || !reflect.DeepEqual(canonical.WorkspaceBinding, base.WorkspaceBinding) {
		t.Fatalf("canonical path not restored: %+v %v", canonical, err)
	}
	duplicate := base
	duplicate.ID = "other-session"
	if err := s.SaveSession(ctx, duplicate); err == nil {
		t.Fatal("two sessions acquired same managed tree")
	}
	insertTestProject(t, s.db, "other-project")
	changed := base
	changed.ProjectID = "other-project"
	changed.WorkspaceBinding = nil
	if err := s.SaveSession(ctx, changed); err == nil {
		t.Fatal("changed owning project")
	}
	loaded, err := s.LoadSession(ctx, base.ID)
	if err != nil || !reflect.DeepEqual(loaded.WorkspaceBinding, base.WorkspaceBinding) {
		t.Fatalf("mutation changed source: %+v %v", loaded, err)
	}
}

func TestWorkspaceBindingMigrationPreservesData(t *testing.T) {
	s, cleanup := setupTestStore(t)
	defer cleanup()
	root := bindingTestProject(t, s)
	ctx := context.Background()
	insertTestProject(t, s.db, project.NoProjectID)
	for _, info := range []SessionInfo{
		{ID: "legacy-code", ProjectID: testProjectID, Name: "Code", CreatedAt: "2026-01-01T00:00:00Z", TotalInputTokens: 42, Archived: true, Pinned: true},
		{ID: "legacy-chat", ProjectID: project.NoProjectID, Name: "Chat", CreatedAt: "2026-01-02T00:00:00Z"},
	} {
		if err := s.SaveSession(ctx, info); err != nil {
			t.Fatal(err)
		}
	}
	msg := ChatMessage{SessionID: "legacy-code", Role: "user", Content: "preserved", Metadata: json.RawMessage(`{"marker":true}`), CreatedAt: "2026-01-01T00:00:00Z"}
	if err := s.SaveMessage(ctx, msg); err != nil {
		t.Fatal(err)
	}
	if err := s.SaveTerminalCommand(ctx, "legacy-code", "pwd"); err != nil {
		t.Fatal(err)
	}
	before, err := s.LoadMessages(ctx, "legacy-code")
	if err != nil {
		t.Fatal(err)
	}
	commands, err := s.LoadTerminalCommands(ctx, "legacy-code", 10)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := s.db.ExecContext(ctx, `DROP INDEX idx_session_managed_workspace; ALTER TABLE sessions DROP COLUMN workspace_binding`); err != nil {
		t.Fatal(err)
	}
	upgraded, err := NewSQLiteSessionStore(s.db)
	if err != nil {
		t.Fatal(err)
	}
	code, err := upgraded.LoadSession(ctx, "legacy-code")
	if err != nil {
		t.Fatal(err)
	}
	if code.WorkspaceBinding == nil || code.WorkspaceBinding.Kind != WorkspaceLocal || code.WorkspaceBinding.WorkspacePath != root || code.TotalInputTokens != 42 || !code.Archived || !code.Pinned {
		t.Fatalf("migration lost metadata: %+v", code)
	}
	chat, err := upgraded.LoadSession(ctx, "legacy-chat")
	if err != nil || chat.WorkspaceBinding != nil {
		t.Fatalf("CHAT changed: %+v %v", chat, err)
	}
	after, err := upgraded.LoadMessages(ctx, "legacy-code")
	if err != nil || !reflect.DeepEqual(before, after) {
		t.Fatalf("messages changed: %+v %v", after, err)
	}
	afterCommands, err := upgraded.LoadTerminalCommands(ctx, "legacy-code", 10)
	if err != nil || !reflect.DeepEqual(commands, afterCommands) {
		t.Fatalf("terminal commands changed: %+v %v", afterCommands, err)
	}
	if _, err := NewSQLiteSessionStore(s.db); err != nil {
		t.Fatalf("migration not idempotent: %v", err)
	}
	fork, err := upgraded.ForkSession(ctx, "legacy-code", nil)
	if err != nil || !reflect.DeepEqual(code.WorkspaceBinding, fork.WorkspaceBinding) {
		t.Fatalf("local fork: %+v %v", fork, err)
	}
}

func TestWorkspaceBindingValidation(t *testing.T) {
	root := t.TempDir()
	for _, branch := range []string{"", "HEAD", "-option", "a..b", "a.lock", "a/.hidden", "a@{b", "a b", "a\\b"} {
		b := managedTestBinding(t, root, "safe", "valid")
		b.Branch = branch
		if _, err := NormalizeWorkspaceBinding(testProjectID, root, b); err == nil {
			t.Fatalf("accepted branch %q", branch)
		}
	}
	for _, name := range []string{"", "..", "../escape", "a/b", "a\\b", "-option", "NUL", "COM1.txt", ".hidden"} {
		if _, err := config.ManagedWorktreePath(root, name); err == nil {
			t.Fatalf("accepted name %q", name)
		}
	}
	b := managedTestBinding(t, root, "safe", "valid")
	if _, err := NormalizeWorkspaceBinding(project.NoProjectID, root, b); err == nil {
		t.Fatal("CHAT accepted binding")
	}
	if err := os.MkdirAll(config.ManagedWorktreesDir(root), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.Symlink(t.TempDir(), filepath.Join(config.ManagedWorktreesDir(root), "escape")); err != nil {
		t.Skipf("symlinks unavailable: %v", err)
	}
	if _, err := config.ManagedWorktreePath(root, "escape"); err == nil {
		t.Fatal("accepted escaping symlink")
	}
}
