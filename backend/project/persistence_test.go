package project

import (
	"context"
	"database/sql"
	"testing"
	"time"

	_ "modernc.org/sqlite"
)

// openTestDB opens an in-memory SQLite database with required pragmas.
func openTestDB(t *testing.T) *sql.DB {
	t.Helper()
	db, err := sql.Open("sqlite", ":memory:")
	if err != nil {
		t.Fatalf("failed to open test db: %v", err)
	}
	if _, err := db.ExecContext(context.Background(), "PRAGMA journal_mode=WAL"); err != nil {
		_ = db.Close()
		t.Fatalf("failed to enable WAL: %v", err)
	}
	if _, err := db.ExecContext(context.Background(), "PRAGMA foreign_keys=ON"); err != nil {
		_ = db.Close()
		t.Fatalf("failed to enable foreign keys: %v", err)
	}
	return db
}

func setupTestStore(t *testing.T) (store *SQLiteProjectStore, db *sql.DB, cleanup func()) {
	t.Helper()
	db = openTestDB(t)

	var err error
	store, err = NewSQLiteProjectStore(db)
	if err != nil {
		_ = db.Close()
		t.Fatalf("failed to create project store: %v", err)
	}

	cleanup = func() {
		_ = db.Close()
	}
	return
}

func TestSaveAndLoadProject(t *testing.T) {
	store, _, cleanup := setupTestStore(t)
	defer cleanup()

	proj := ProjectInfo{
		ID:            "proj-1",
		Name:          "Test Project",
		WorkspacePath: "/tmp/test",
		IsExternal:    false,
		CreatedAt:     "2024-01-15T10:30:00Z",
	}

	if err := store.SaveProject(context.Background(), proj); err != nil {
		t.Fatalf("failed to save project: %v", err)
	}

	loaded, err := store.LoadProject(context.Background(), proj.ID)
	if err != nil {
		t.Fatalf("failed to load project: %v", err)
	}
	if loaded == nil {
		t.Fatal("loaded project should not be nil")
	}
	if loaded.ID != proj.ID {
		t.Errorf("ID mismatch: got %q, want %q", loaded.ID, proj.ID)
	}
	if loaded.Name != proj.Name {
		t.Errorf("Name mismatch: got %q, want %q", loaded.Name, proj.Name)
	}
	if loaded.WorkspacePath != proj.WorkspacePath {
		t.Errorf("WorkspacePath mismatch: got %q, want %q", loaded.WorkspacePath, proj.WorkspacePath)
	}
	if loaded.IsExternal != proj.IsExternal {
		t.Errorf("IsExternal mismatch: got %v, want %v", loaded.IsExternal, proj.IsExternal)
	}
	// LastActiveAt should fall back to CreatedAt
	if loaded.LastActiveAt != proj.CreatedAt {
		t.Errorf("LastActiveAt should fall back to CreatedAt: got %q, want %q", loaded.LastActiveAt, proj.CreatedAt)
	}

	// Load non-existent project
	notFound, err := store.LoadProject(context.Background(), "non-existent")
	if err != nil {
		t.Fatalf("error loading non-existent project: %v", err)
	}
	if notFound != nil {
		t.Error("non-existent project should return nil")
	}
}

func TestSaveProjectUpsert(t *testing.T) {
	store, _, cleanup := setupTestStore(t)
	defer cleanup()

	proj := ProjectInfo{
		ID:            "upsert-proj",
		Name:          "Original",
		WorkspacePath: "/tmp/original",
		CreatedAt:     "2024-01-15T10:00:00Z",
	}
	if err := store.SaveProject(context.Background(), proj); err != nil {
		t.Fatalf("failed to save project: %v", err)
	}

	// Update via upsert
	proj.Name = "Updated"
	proj.WorkspacePath = "/tmp/updated"
	proj.LastActiveAt = "2024-06-01T15:00:00Z"
	if err := store.SaveProject(context.Background(), proj); err != nil {
		t.Fatalf("failed to upsert project: %v", err)
	}

	loaded, err := store.LoadProject(context.Background(), proj.ID)
	if err != nil {
		t.Fatalf("failed to load project: %v", err)
	}
	if loaded.Name != "Updated" {
		t.Errorf("name should be updated: got %q", loaded.Name)
	}
	if loaded.WorkspacePath != "/tmp/updated" {
		t.Errorf("workspace_path should be updated: got %q", loaded.WorkspacePath)
	}
	if loaded.LastActiveAt != "2024-06-01T15:00:00Z" {
		t.Errorf("last_active_at should be updated: got %q", loaded.LastActiveAt)
	}
}

func TestListProjects(t *testing.T) {
	store, _, cleanup := setupTestStore(t)
	defer cleanup()

	projects := []ProjectInfo{
		{ID: "old", Name: "Old", WorkspacePath: "/old", CreatedAt: "2024-01-01T10:00:00Z", LastActiveAt: "2024-01-01T10:00:00Z"},
		{ID: "newest", Name: "Newest", WorkspacePath: "/newest", CreatedAt: "2024-01-01T09:00:00Z", LastActiveAt: "2024-06-15T20:00:00Z"},
		{ID: "mid", Name: "Mid", WorkspacePath: "/mid", CreatedAt: "2024-03-01T10:00:00Z", LastActiveAt: "2024-03-01T10:00:00Z"},
	}
	for _, p := range projects {
		if err := store.SaveProject(context.Background(), p); err != nil {
			t.Fatalf("failed to save project: %v", err)
		}
	}

	listed, err := store.ListProjects(context.Background())
	if err != nil {
		t.Fatalf("failed to list projects: %v", err)
	}
	if len(listed) != 3 {
		t.Fatalf("expected 3 projects, got %d", len(listed))
	}

	// Should be ordered by last_active_at DESC
	if listed[0].ID != "newest" {
		t.Errorf("first should be 'newest', got %q", listed[0].ID)
	}
	if listed[1].ID != "mid" {
		t.Errorf("second should be 'mid', got %q", listed[1].ID)
	}
	if listed[2].ID != "old" {
		t.Errorf("third should be 'old', got %q", listed[2].ID)
	}
}

func TestEmptyListProjects(t *testing.T) {
	store, _, cleanup := setupTestStore(t)
	defer cleanup()

	projects, err := store.ListProjects(context.Background())
	if err != nil {
		t.Fatalf("failed to list empty projects: %v", err)
	}
	if len(projects) != 0 {
		t.Errorf("expected empty list, got %d", len(projects))
	}
	if projects == nil {
		t.Error("projects should not be nil")
	}
}

func TestDeleteProject(t *testing.T) {
	store, _, cleanup := setupTestStore(t)
	defer cleanup()

	proj := ProjectInfo{
		ID:            "delete-test",
		Name:          "Delete Test",
		WorkspacePath: "/tmp/delete",
		CreatedAt:     time.Now().Format(time.RFC3339),
	}
	if err := store.SaveProject(context.Background(), proj); err != nil {
		t.Fatalf("failed to save project: %v", err)
	}

	if err := store.DeleteProject(context.Background(), proj.ID); err != nil {
		t.Fatalf("failed to delete project: %v", err)
	}

	loaded, err := store.LoadProject(context.Background(), proj.ID)
	if err != nil {
		t.Fatalf("error loading deleted project: %v", err)
	}
	if loaded != nil {
		t.Error("deleted project should be nil")
	}
}

func TestRenameProject(t *testing.T) {
	store, _, cleanup := setupTestStore(t)
	defer cleanup()

	proj := ProjectInfo{
		ID:            "rename-test",
		Name:          "Original",
		WorkspacePath: "/tmp/rename",
		CreatedAt:     time.Now().Format(time.RFC3339),
	}
	if err := store.SaveProject(context.Background(), proj); err != nil {
		t.Fatalf("failed to save project: %v", err)
	}

	if err := store.RenameProject(context.Background(), proj.ID, "Renamed"); err != nil {
		t.Fatalf("failed to rename project: %v", err)
	}

	loaded, err := store.LoadProject(context.Background(), proj.ID)
	if err != nil {
		t.Fatalf("failed to load project: %v", err)
	}
	if loaded.Name != "Renamed" {
		t.Errorf("name should be 'Renamed', got %q", loaded.Name)
	}
}

func TestUpdateProjectActivity(t *testing.T) {
	store, _, cleanup := setupTestStore(t)
	defer cleanup()

	proj := ProjectInfo{
		ID:            "activity-test",
		Name:          "Activity Test",
		WorkspacePath: "/tmp/activity",
		CreatedAt:     "2024-01-15T10:00:00Z",
	}
	if err := store.SaveProject(context.Background(), proj); err != nil {
		t.Fatalf("failed to save project: %v", err)
	}

	before, err := store.LoadProject(context.Background(), proj.ID)
	if err != nil {
		t.Fatalf("failed to load project: %v", err)
	}
	originalLastActive := before.LastActiveAt

	time.Sleep(10 * time.Millisecond)

	if err := store.UpdateProjectActivity(context.Background(), proj.ID); err != nil {
		t.Fatalf("failed to update project activity: %v", err)
	}

	after, err := store.LoadProject(context.Background(), proj.ID)
	if err != nil {
		t.Fatalf("failed to load project after update: %v", err)
	}
	if after.LastActiveAt == originalLastActive {
		t.Error("last_active_at should have changed after UpdateProjectActivity")
	}
}

func TestCloseIsNoOp(t *testing.T) {
	store, db, cleanup := setupTestStore(t)
	defer cleanup()

	proj := ProjectInfo{
		ID:            "close-test",
		Name:          "Close Test",
		WorkspacePath: "/tmp/close",
		CreatedAt:     time.Now().Format(time.RFC3339),
	}
	if err := store.SaveProject(context.Background(), proj); err != nil {
		t.Fatalf("failed to save project: %v", err)
	}

	// Close should be a no-op
	if err := store.Close(); err != nil {
		t.Fatalf("Close returned error: %v", err)
	}

	// DB should still be usable
	loaded, err := store.LoadProject(context.Background(), proj.ID)
	if err != nil {
		t.Fatalf("DB should still work after Close: %v", err)
	}
	if loaded == nil || loaded.ID != proj.ID {
		t.Error("expected to still load project after Close")
	}
	_ = db // referenced via cleanup
}

func TestSaveProjectWithExternalFlag(t *testing.T) {
	store, _, cleanup := setupTestStore(t)
	defer cleanup()

	proj := ProjectInfo{
		ID:            "external-test",
		Name:          "External Project",
		WorkspacePath: "/external/path",
		IsExternal:    true,
		CreatedAt:     time.Now().Format(time.RFC3339),
	}
	if err := store.SaveProject(context.Background(), proj); err != nil {
		t.Fatalf("failed to save project: %v", err)
	}

	loaded, err := store.LoadProject(context.Background(), proj.ID)
	if err != nil {
		t.Fatalf("failed to load project: %v", err)
	}
	if !loaded.IsExternal {
		t.Error("IsExternal should be true")
	}
}

func TestSaveProjectLastActiveAtFallback(t *testing.T) {
	store, _, cleanup := setupTestStore(t)
	defer cleanup()

	proj := ProjectInfo{
		ID:            "fallback-test",
		Name:          "Fallback Test",
		WorkspacePath: "/tmp/fallback",
		CreatedAt:     "2024-01-15T10:00:00Z",
		LastActiveAt:  "", // empty
	}
	if err := store.SaveProject(context.Background(), proj); err != nil {
		t.Fatalf("failed to save project: %v", err)
	}

	loaded, err := store.LoadProject(context.Background(), proj.ID)
	if err != nil {
		t.Fatalf("failed to load project: %v", err)
	}
	if loaded.LastActiveAt != "2024-01-15T10:00:00Z" {
		t.Errorf("expected last_active_at to fall back to created_at, got %q", loaded.LastActiveAt)
	}
}
