package session

import (
	"context"
	"errors"
	"reflect"
	"testing"
	"time"

	"golang.org/x/sync/errgroup"
)

func TestMCPMentions_SQLiteAdapterUnionRollbackForkAndCascade(t *testing.T) {
	store, sessionID, cleanup := setupTestStoreWithSession(t)
	t.Cleanup(cleanup)
	// :memory: databases are connection-local; concurrent calls share this one.
	store.db.SetMaxOpenConns(1)
	ctx := context.Background()
	if err := store.SaveTask(ctx, TaskRecord{ID: "task", SessionID: sessionID, OriginalRequest: "work", Status: "paused", CreatedAt: time.Now()}); err != nil {
		t.Fatal(err)
	}
	adapter := NewTaskStoreAdapter(store)
	assertNames := func(taskID string, want []string) {
		t.Helper()
		got, err := adapter.LoadMCPMentions(ctx, taskID)
		if err != nil || !reflect.DeepEqual(got, want) {
			t.Errorf("LoadMCPMentions(%q) = (%v,%v), want %v,nil", taskID, got, err, want)
		}
	}
	assertNames("task", nil)
	if err := adapter.PersistMCPMentions(ctx, "task", []string{"b", "a", "b"}); err != nil {
		t.Fatal(err)
	}
	assertNames("task", []string{"a", "b"})
	var group errgroup.Group
	for _, names := range [][]string{{"c"}, {"d", "a"}, {"e"}} {
		group.Go(func() error { return adapter.PersistMCPMentions(ctx, "task", names) })
	}
	if err := group.Wait(); err != nil {
		t.Fatalf("PersistMCPMentions(concurrent) = %v, want nil", err)
	}
	assertNames("task", []string{"a", "b", "c", "d", "e"})
	if _, err := store.db.ExecContext(ctx, `CREATE TRIGGER reject_mcp_mention BEFORE INSERT ON task_mcp_mentions WHEN NEW.server_name = 'reject' BEGIN SELECT RAISE(ABORT, 'test failure'); END`); err != nil {
		t.Fatal(err)
	}
	if err := adapter.PersistMCPMentions(ctx, "task", []string{"rolled-back", "reject"}); err == nil {
		t.Error("PersistMCPMentions(trigger failure) = nil, want error")
	}
	assertNames("task", []string{"a", "b", "c", "d", "e"})
	if err := adapter.PersistMCPMentions(ctx, "missing", []string{"a"}); err == nil {
		t.Error("PersistMCPMentions(missing parent) = nil, want FK error")
	}
	fork, err := store.ForkSession(ctx, sessionID, nil)
	if err != nil {
		t.Fatalf("ForkSession(%q) = %v, want nil", sessionID, err)
	}
	forkTask, err := store.GetLatestTaskID(ctx, fork.ID)
	if err != nil || forkTask == "" || forkTask == "task" {
		t.Fatalf("GetLatestTaskID(fork) = (%q,%v), want remapped task", forkTask, err)
	}
	assertNames(forkTask, []string{"a", "b", "c", "d", "e"})
	if err := adapter.PersistMCPMentions(ctx, forkTask, []string{"fork-only"}); err != nil {
		t.Fatal(err)
	}
	assertNames("task", []string{"a", "b", "c", "d", "e"})
	if _, err := store.db.ExecContext(ctx, `DELETE FROM tasks WHERE id = ?`, forkTask); err != nil {
		t.Fatal(err)
	}
	assertNames(forkTask, nil)
	if _, err := store.db.ExecContext(ctx, `DELETE FROM sessions WHERE id = ?`, sessionID); err != nil {
		t.Fatal(err)
	}
	assertNames("task", nil)
}

func TestMCPMentions_AdditiveMigrationAndContextErrors(t *testing.T) {
	store, sessionID, cleanup := setupTestStoreWithSession(t)
	t.Cleanup(cleanup)
	ctx := context.Background()
	if err := store.SaveTask(ctx, TaskRecord{ID: "legacy", SessionID: sessionID, OriginalRequest: "legacy work", Status: "paused", CreatedAt: time.Now()}); err != nil {
		t.Fatal(err)
	}
	// Reopen the old-database shape; initialization must recreate only the additive table.
	if _, err := store.db.ExecContext(ctx, `DROP TABLE task_mcp_mentions`); err != nil {
		t.Fatal(err)
	}
	reopened, err := NewSQLiteSessionStore(store.db)
	if err != nil {
		t.Fatalf("NewSQLiteSessionStore(legacy) = %v, want nil", err)
	}
	got, err := reopened.LoadMCPMentions(ctx, "legacy")
	if err != nil || len(got) != 0 {
		t.Fatalf("LoadMCPMentions(legacy) = (%v,%v), want empty,nil", got, err)
	}
	task, err := reopened.LoadTask(ctx, "legacy")
	if err != nil || task == nil || task.OriginalRequest != "legacy work" {
		t.Fatalf("LoadTask(legacy) = (%v,%v), want preserved task", task, err)
	}
	cancelled, cancel := context.WithCancel(ctx)
	cancel()
	adapter := NewTaskStoreAdapter(reopened)
	if _, err := adapter.LoadMCPMentions(cancelled, "legacy"); !errors.Is(err, context.Canceled) {
		t.Errorf("LoadMCPMentions(cancelled) = %v, want context.Canceled", err)
	}
	if err := adapter.PersistMCPMentions(cancelled, "legacy", []string{"x"}); !errors.Is(err, context.Canceled) {
		t.Errorf("PersistMCPMentions(cancelled) = %v, want context.Canceled", err)
	}
	if _, err := reopened.db.ExecContext(ctx, `DROP TABLE task_mcp_mentions`); err != nil {
		t.Fatal(err)
	}
	if _, err := adapter.LoadMCPMentions(ctx, "legacy"); err == nil {
		t.Error("LoadMCPMentions(unavailable table) = nil error, want error")
	}
}
