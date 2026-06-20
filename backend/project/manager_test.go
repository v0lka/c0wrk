package project

import (
	"context"
	"database/sql"
	"os"
	"path/filepath"
	"testing"

	_ "modernc.org/sqlite"
)

// setupTestManager creates a Manager backed by an in-memory SQLite store and a temp agent dir.
func setupTestManager(t *testing.T) (mgr *Manager, agentDir string) {
	t.Helper()

	db, err := sql.Open("sqlite", ":memory:")
	if err != nil {
		t.Fatalf("failed to open test db: %v", err)
	}
	t.Cleanup(func() { _ = db.Close() })

	if _, err := db.ExecContext(context.Background(), "PRAGMA journal_mode=WAL"); err != nil {
		t.Fatalf("failed to enable WAL: %v", err)
	}
	if _, err := db.ExecContext(context.Background(), "PRAGMA foreign_keys=ON"); err != nil {
		t.Fatalf("failed to enable foreign keys: %v", err)
	}

	store, err := NewSQLiteProjectStore(db)
	if err != nil {
		t.Fatalf("failed to create project store: %v", err)
	}

	agentDir = t.TempDir()
	mgr = NewManager(store, agentDir)
	return mgr, agentDir
}

func TestManager_CreateProject_Internal(t *testing.T) {
	mgr, agentDir := setupTestManager(t)

	proj, err := mgr.CreateProject("My Project", "")
	if err != nil {
		t.Fatalf("CreateProject failed: %v", err)
	}

	if proj.ID == "" {
		t.Error("ID should not be empty")
	}
	if proj.Name != "My Project" {
		t.Errorf("Name = %q, want %q", proj.Name, "My Project")
	}
	if proj.IsExternal {
		t.Error("internal project should have IsExternal=false")
	}
	if proj.CreatedAt == "" {
		t.Error("CreatedAt should not be empty")
	}
	if proj.LastActiveAt == "" {
		t.Error("LastActiveAt should not be empty")
	}

	// Verify workspace directory was created (under projects/<id>/Workspace)
	expectedWorkspace := filepath.Join(agentDir, "projects", proj.ID, "Workspace")
	if proj.WorkspacePath != expectedWorkspace {
		t.Errorf("WorkspacePath = %q, want %q", proj.WorkspacePath, expectedWorkspace)
	}
	info, err := os.Stat(expectedWorkspace)
	if err != nil {
		t.Fatalf("workspace directory does not exist: %v", err)
	}
	if !info.IsDir() {
		t.Error("workspace path should be a directory")
	}

	// Verify project is persisted
	loaded, err := mgr.GetProject(proj.ID)
	if err != nil {
		t.Fatalf("GetProject failed: %v", err)
	}
	if loaded == nil {
		t.Fatal("persisted project should not be nil")
	}
	if loaded.ID != proj.ID {
		t.Errorf("ID mismatch: got %q, want %q", loaded.ID, proj.ID)
	}
}

func TestManager_CreateProject_External(t *testing.T) {
	mgr, agentDir := setupTestManager(t)

	// Create an external directory to point to
	externalDir := t.TempDir()

	proj, err := mgr.CreateProject("External", externalDir)
	if err != nil {
		t.Fatalf("CreateProject failed: %v", err)
	}

	if !proj.IsExternal {
		t.Error("external project should have IsExternal=true")
	}
	if proj.WorkspacePath != externalDir {
		t.Errorf("WorkspacePath = %q, want %q", proj.WorkspacePath, externalDir)
	}

	// Verify NO directory was created under agentDir for external projects
	projectDir := filepath.Join(agentDir, "projects", proj.ID)
	if _, err := os.Stat(projectDir); !os.IsNotExist(err) {
		t.Errorf("no directory should be created under agentDir for external projects, but %q exists", projectDir)
	}
}

func TestManager_CreateProject_ExternalPathNotExist(t *testing.T) {
	mgr, _ := setupTestManager(t)

	_, err := mgr.CreateProject("Bad External", "/non/existent/path/that/does/not/exist")
	if err == nil {
		t.Fatal("expected error for non-existent external path")
	}
}

func TestManager_DeleteProject_Internal(t *testing.T) {
	mgr, agentDir := setupTestManager(t)

	proj, err := mgr.CreateProject("ToDelete", "")
	if err != nil {
		t.Fatalf("CreateProject failed: %v", err)
	}

	// Verify directory exists before delete
	projectDir := filepath.Join(agentDir, "projects", proj.ID)
	if _, err := os.Stat(projectDir); err != nil {
		t.Fatalf("project directory should exist before delete: %v", err)
	}

	if err := mgr.DeleteProject(proj.ID); err != nil {
		t.Fatalf("DeleteProject failed: %v", err)
	}

	// Verify directory is removed
	if _, err := os.Stat(projectDir); !os.IsNotExist(err) {
		t.Error("project directory should be removed after deleting internal project")
	}

	// Verify project is removed from store
	loaded, err := mgr.GetProject(proj.ID)
	if err != nil {
		t.Fatalf("GetProject failed: %v", err)
	}
	if loaded != nil {
		t.Error("deleted project should not be loadable")
	}
}

func TestManager_DeleteProject_External(t *testing.T) {
	mgr, _ := setupTestManager(t)

	externalDir := t.TempDir()

	// Put a file in the external dir to verify it survives
	testFile := filepath.Join(externalDir, "important.txt")
	if err := os.WriteFile(testFile, []byte("keep me"), 0o644); err != nil {
		t.Fatalf("failed to create test file: %v", err)
	}

	proj, err := mgr.CreateProject("External", externalDir)
	if err != nil {
		t.Fatalf("CreateProject failed: %v", err)
	}

	if err := mgr.DeleteProject(proj.ID); err != nil {
		t.Fatalf("DeleteProject failed: %v", err)
	}

	// Verify external directory is untouched
	if _, err := os.Stat(testFile); err != nil {
		t.Errorf("external workspace file should survive deletion: %v", err)
	}
}

func TestManager_DeleteProject_NotFound(t *testing.T) {
	mgr, _ := setupTestManager(t)

	err := mgr.DeleteProject("non-existent-id")
	if err == nil {
		t.Error("expected error when deleting non-existent project")
	}
}

func TestManager_RenameProject(t *testing.T) {
	mgr, _ := setupTestManager(t)

	proj, err := mgr.CreateProject("Original", "")
	if err != nil {
		t.Fatalf("CreateProject failed: %v", err)
	}

	if err := mgr.RenameProject(proj.ID, "Renamed"); err != nil {
		t.Fatalf("RenameProject failed: %v", err)
	}

	loaded, err := mgr.GetProject(proj.ID)
	if err != nil {
		t.Fatalf("GetProject failed: %v", err)
	}
	if loaded.Name != "Renamed" {
		t.Errorf("Name = %q, want %q", loaded.Name, "Renamed")
	}
}

func TestManager_ListProjects(t *testing.T) {
	mgr, _ := setupTestManager(t)

	// Empty list
	projects, err := mgr.ListProjects()
	if err != nil {
		t.Fatalf("ListProjects failed: %v", err)
	}
	if len(projects) != 0 {
		t.Errorf("expected 0 projects, got %d", len(projects))
	}

	// Create some projects
	_, err = mgr.CreateProject("First", "")
	if err != nil {
		t.Fatalf("CreateProject failed: %v", err)
	}
	_, err = mgr.CreateProject("Second", "")
	if err != nil {
		t.Fatalf("CreateProject failed: %v", err)
	}

	projects, err = mgr.ListProjects()
	if err != nil {
		t.Fatalf("ListProjects failed: %v", err)
	}
	if len(projects) != 2 {
		t.Errorf("expected 2 projects, got %d", len(projects))
	}
}

func TestManager_GetProject(t *testing.T) {
	mgr, _ := setupTestManager(t)

	// Non-existent
	proj, err := mgr.GetProject("nope")
	if err != nil {
		t.Fatalf("GetProject failed: %v", err)
	}
	if proj != nil {
		t.Error("non-existent project should return nil")
	}

	// Create and retrieve
	created, err := mgr.CreateProject("Get Test", "")
	if err != nil {
		t.Fatalf("CreateProject failed: %v", err)
	}

	proj, err = mgr.GetProject(created.ID)
	if err != nil {
		t.Fatalf("GetProject failed: %v", err)
	}
	if proj == nil {
		t.Fatal("project should not be nil")
	}
	if proj.ID != created.ID {
		t.Errorf("ID mismatch: got %q, want %q", proj.ID, created.ID)
	}
}
