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

func TestSaveAndLoadProjectUIState(t *testing.T) {
	store, _, cleanup := setupTestStore(t)
	defer cleanup()

	proj := ProjectInfo{
		ID:            "proj-ui-state",
		Name:          "UI State",
		WorkspacePath: "/tmp/ui-state",
		CreatedAt:     "2024-01-15T10:00:00Z",
	}
	if err := store.SaveProject(context.Background(), proj); err != nil {
		t.Fatalf("failed to save project: %v", err)
	}

	state := ProjectUIState{
		ProjectID:      proj.ID,
		SavedSessionID: "session-123",
		OpenTabs:       []string{"a.go", "dir/b.go"},
		ActiveFile:     "dir/b.go",
		UpdatedAt:      "2024-06-01T12:00:00Z",
	}
	if err := store.SaveUIState(context.Background(), state); err != nil {
		t.Fatalf("failed to save UI state: %v", err)
	}

	loaded, err := store.LoadUIState(context.Background(), proj.ID)
	if err != nil {
		t.Fatalf("failed to load UI state: %v", err)
	}
	if loaded == nil {
		t.Fatal("loaded UI state should not be nil")
	}
	if loaded.ProjectID != proj.ID {
		t.Errorf("ProjectID mismatch: got %q, want %q", loaded.ProjectID, proj.ID)
	}
	if loaded.SavedSessionID != "session-123" {
		t.Errorf("SavedSessionID mismatch: got %q", loaded.SavedSessionID)
	}
	if len(loaded.OpenTabs) != 2 || loaded.OpenTabs[0] != "a.go" || loaded.OpenTabs[1] != "dir/b.go" {
		t.Errorf("OpenTabs mismatch: got %#v", loaded.OpenTabs)
	}
	if loaded.ActiveFile != "dir/b.go" {
		t.Errorf("ActiveFile mismatch: got %q", loaded.ActiveFile)
	}
	if loaded.UpdatedAt != "2024-06-01T12:00:00Z" {
		t.Errorf("UpdatedAt mismatch: got %q", loaded.UpdatedAt)
	}
}

func TestSaveProjectUIState_UpsertAndDefaults(t *testing.T) {
	store, _, cleanup := setupTestStore(t)
	defer cleanup()

	proj := ProjectInfo{
		ID:            "proj-ui-upsert",
		Name:          "UI Upsert",
		WorkspacePath: "/tmp/ui-upsert",
		CreatedAt:     time.Now().Format(time.RFC3339),
	}
	if err := store.SaveProject(context.Background(), proj); err != nil {
		t.Fatalf("failed to save project: %v", err)
	}

	if err := store.SaveUIState(context.Background(), ProjectUIState{
		ProjectID:      proj.ID,
		SavedSessionID: "first-session",
		OpenTabs:       []string{"first.go"},
		ActiveFile:     "first.go",
	}); err != nil {
		t.Fatalf("failed to save initial UI state: %v", err)
	}

	if err := store.SaveUIState(context.Background(), ProjectUIState{
		ProjectID:      proj.ID,
		SavedSessionID: "",
		OpenTabs:       []string{},
		ActiveFile:     "",
	}); err != nil {
		t.Fatalf("failed to upsert UI state: %v", err)
	}

	loaded, err := store.LoadUIState(context.Background(), proj.ID)
	if err != nil {
		t.Fatalf("failed to load UI state: %v", err)
	}
	if loaded == nil {
		t.Fatal("loaded UI state should not be nil")
	}
	if loaded.SavedSessionID != "" {
		t.Errorf("SavedSessionID should be empty after overwrite, got %q", loaded.SavedSessionID)
	}
	if len(loaded.OpenTabs) != 0 {
		t.Errorf("OpenTabs should be empty after overwrite, got %#v", loaded.OpenTabs)
	}
	if loaded.ActiveFile != "" {
		t.Errorf("ActiveFile should be empty after overwrite, got %q", loaded.ActiveFile)
	}
	if loaded.UpdatedAt == "" {
		t.Error("UpdatedAt should be auto-populated when omitted")
	}
}

func TestLoadProjectUIState_NotFound(t *testing.T) {
	store, _, cleanup := setupTestStore(t)
	defer cleanup()

	loaded, err := store.LoadUIState(context.Background(), "missing-project")
	if err != nil {
		t.Fatalf("failed to load missing UI state: %v", err)
	}
	if loaded != nil {
		t.Error("missing UI state should return nil")
	}
}

// ---------------------------------------------------------------------------
// Project work directories
// ---------------------------------------------------------------------------

// saveTestProject saves and returns a minimal project for work-directory tests.
func saveTestProject(t *testing.T, store *SQLiteProjectStore, id string) ProjectInfo {
	t.Helper()
	proj := ProjectInfo{
		ID:            id,
		Name:          "Work Dir Project",
		WorkspacePath: "/tmp/workdirs",
		CreatedAt:     "2024-01-15T10:00:00Z",
	}
	if err := store.SaveProject(context.Background(), proj); err != nil {
		t.Fatalf("failed to save project: %v", err)
	}
	return proj
}

func TestProjectWorkDir_SaveListUpdateDelete(t *testing.T) {
	store, _, cleanup := setupTestStore(t)
	defer cleanup()

	proj := saveTestProject(t, store, "proj-workdir-1")

	// rec1 has an explicit older timestamp; rec2 auto-generates id + created_at
	// (now). Ordered ASC by created_at, rec1 comes first.
	rec1 := WorkDirectoryRecord{
		ID:          "explicit-id",
		Path:        "/tmp/dir1",
		Description: "build output",
		CreatedAt:   "2024-06-01T12:00:00Z",
	}
	rec2 := WorkDirectoryRecord{Path: "/tmp/dir2", Description: "logs"}
	for _, rec := range []WorkDirectoryRecord{rec1, rec2} {
		if err := store.SaveProjectWorkDir(context.Background(), proj.ID, rec); err != nil {
			t.Fatalf("failed to save work dir: %v", err)
		}
	}

	// List returns both, ordered oldest-first.
	listed, err := store.ListProjectWorkDirs(context.Background(), proj.ID)
	if err != nil {
		t.Fatalf("failed to list work dirs: %v", err)
	}
	if len(listed) != 2 {
		t.Fatalf("expected 2 work dirs, got %d", len(listed))
	}
	// Explicit id + created_at preserved on the oldest row.
	if listed[0].ID != "explicit-id" {
		t.Errorf("ID mismatch: got %q, want explicit-id", listed[0].ID)
	}
	if listed[0].CreatedAt != "2024-06-01T12:00:00Z" {
		t.Errorf("CreatedAt mismatch: got %q", listed[0].CreatedAt)
	}
	if listed[0].Path != "/tmp/dir1" {
		t.Errorf("Path mismatch on oldest: got %q, want /tmp/dir1", listed[0].Path)
	}
	// Auto-generated id + created_at should be populated on the newer row.
	if listed[1].ID == "" {
		t.Error("auto-generated ID should not be empty")
	}
	if listed[1].CreatedAt == "" {
		t.Error("auto-generated CreatedAt should not be empty")
	}

	// Update description on the explicit-id row.
	if err := store.UpdateProjectWorkDirDescription(context.Background(), proj.ID, "explicit-id", "updated description"); err != nil {
		t.Fatalf("failed to update description: %v", err)
	}
	listed, err = store.ListProjectWorkDirs(context.Background(), proj.ID)
	if err != nil {
		t.Fatalf("failed to list after update: %v", err)
	}
	if listed[0].Description != "updated description" {
		t.Errorf("description should be updated, got %q", listed[0].Description)
	}

	// Delete the explicit-id row.
	if err := store.DeleteProjectWorkDir(context.Background(), proj.ID, "explicit-id"); err != nil {
		t.Fatalf("failed to delete work dir: %v", err)
	}
	listed, err = store.ListProjectWorkDirs(context.Background(), proj.ID)
	if err != nil {
		t.Fatalf("failed to list after delete: %v", err)
	}
	if len(listed) != 1 || listed[0].ID == "explicit-id" {
		t.Errorf("expected only the auto-generated row to remain, got %#v", listed)
	}
}

func TestProjectWorkDir_ListEmptyReturnsNonNilSlice(t *testing.T) {
	store, _, cleanup := setupTestStore(t)
	defer cleanup()

	proj := saveTestProject(t, store, "proj-workdir-empty")

	listed, err := store.ListProjectWorkDirs(context.Background(), proj.ID)
	if err != nil {
		t.Fatalf("failed to list empty work dirs: %v", err)
	}
	if listed == nil {
		t.Fatal("expected non-nil slice for empty result")
	}
	if len(listed) != 0 {
		t.Errorf("expected empty slice, got %d items", len(listed))
	}
}

func TestProjectWorkDir_CascadeOnProjectDelete(t *testing.T) {
	store, _, cleanup := setupTestStore(t)
	defer cleanup()

	proj := saveTestProject(t, store, "proj-workdir-cascade")
	if err := store.SaveProjectWorkDir(context.Background(), proj.ID, WorkDirectoryRecord{
		Path:        "/tmp/cascade",
		Description: "should be removed",
	}); err != nil {
		t.Fatalf("failed to save work dir: %v", err)
	}

	// Confirm it exists.
	listed, err := store.ListProjectWorkDirs(context.Background(), proj.ID)
	if err != nil {
		t.Fatalf("failed to list before delete: %v", err)
	}
	if len(listed) != 1 {
		t.Fatalf("expected 1 work dir before cascade, got %d", len(listed))
	}

	// Delete the project — FK cascade must remove the work dir row.
	if err := store.DeleteProject(context.Background(), proj.ID); err != nil {
		t.Fatalf("failed to delete project: %v", err)
	}

	listed, err = store.ListProjectWorkDirs(context.Background(), proj.ID)
	if err != nil {
		t.Fatalf("failed to list after project delete: %v", err)
	}
	if len(listed) != 0 {
		t.Errorf("expected work dirs to cascade-delete with project, got %d", len(listed))
	}
}

func TestProjectWorkDir_IsolationByProject(t *testing.T) {
	store, _, cleanup := setupTestStore(t)
	defer cleanup()

	projA := saveTestProject(t, store, "proj-workdir-a")
	projB := saveTestProject(t, store, "proj-workdir-b")

	if err := store.SaveProjectWorkDir(context.Background(), projA.ID, WorkDirectoryRecord{Path: "/tmp/a", Description: "a"}); err != nil {
		t.Fatalf("failed to save work dir for A: %v", err)
	}
	if err := store.SaveProjectWorkDir(context.Background(), projB.ID, WorkDirectoryRecord{Path: "/tmp/b", Description: "b"}); err != nil {
		t.Fatalf("failed to save work dir for B: %v", err)
	}

	aDirs, err := store.ListProjectWorkDirs(context.Background(), projA.ID)
	if err != nil {
		t.Fatalf("failed to list A work dirs: %v", err)
	}
	if len(aDirs) != 1 || aDirs[0].Path != "/tmp/a" {
		t.Errorf("project A should only see its own work dir, got %#v", aDirs)
	}
	bDirs, err := store.ListProjectWorkDirs(context.Background(), projB.ID)
	if err != nil {
		t.Fatalf("failed to list B work dirs: %v", err)
	}
	if len(bDirs) != 1 || bDirs[0].Path != "/tmp/b" {
		t.Errorf("project B should only see its own work dir, got %#v", bDirs)
	}
}
