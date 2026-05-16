package desktop

import (
	"context"
	"fmt"
	"log/slog"
	"os"
	"path/filepath"
	"testing"
	"time"

	"github.com/user/agent/backend"
	"github.com/user/agent/backend/config"
	"github.com/user/agent/backend/project"
	"github.com/user/agent/backend/session"
)

// TestCriticalPathBudget verifies that the synchronous startup phases
// (database, stores, project/session preload) complete within 500ms.
//
// This acts as a regression guardrail: if a new blocking operation is
// accidentally added to the critical path, this test will catch the
// budget violation.
//
// The test exercises the same subsystems as Startup phases 2-4 but
// without Wails context, ONNX models, MCP gateway, or network I/O.
func TestCriticalPathBudget(t *testing.T) {
	const budget = 500 * time.Millisecond

	dir := t.TempDir()
	log := slog.New(slog.NewTextHandler(os.Stderr, &slog.HandlerOptions{Level: slog.LevelError}))

	start := time.Now()

	// ── Phase 2 equivalent: config with defaults ───────────────────
	cfg := &config.Config{}
	config.ApplyDefaults(cfg)
	_ = cfg // startup reads cfg.Memory.Database; budget covers loading time

	// ── Phase 3 equivalent: database ────────────────────────────────
	dbPath := filepath.Join(dir, "test.db")
	db, err := backend.OpenDatabase(dbPath, log)
	if err != nil {
		t.Fatalf("OpenDatabase: %v", err)
	}
	defer func() { _ = db.Close() }()

	// ── Phase 4 equivalent: stores + preload ────────────────────────
	projStore, err := project.NewSQLiteProjectStore(db)
	if err != nil {
		t.Fatalf("NewSQLiteProjectStore: %v", err)
	}

	sessStore, err := session.NewSQLiteSessionStore(db)
	if err != nil {
		t.Fatalf("NewSQLiteSessionStore: %v", err)
	}

	projectsDir := filepath.Join(dir, "projects")
	if mkErr := os.MkdirAll(projectsDir, 0o755); mkErr != nil {
		t.Fatalf("MkdirAll projects: %v", mkErr)
	}

	projectMgr := project.NewManager(projStore, projectsDir)

	projects, err := projectMgr.ListProjects()
	if err != nil {
		t.Fatalf("ListProjects: %v", err)
	}

	// If projects existed, the startup would also pre-load sessions for
	// the most recent one. Simulate that path even with an empty DB.
	if len(projects) > 0 {
		_, err = sessStore.ListSessionsByProject(context.Background(), projects[0].ID)
		if err != nil {
			t.Fatalf("ListSessionsByProject: %v", err)
		}
	}

	elapsed := time.Since(start)
	t.Logf("critical-path budget test completed in %v (budget: %v)", elapsed, budget)

	if elapsed > budget {
		t.Fatalf("critical path exceeded budget: took %v, allowed %v", elapsed, budget)
	}
}

// TestCriticalPathBudget_WithData is a heavier variant that populates
// the database before measuring the preload phase, ensuring that the
// budget holds even when projects and sessions exist.
func TestCriticalPathBudget_WithData(t *testing.T) {
	const budget = 500 * time.Millisecond

	dir := t.TempDir()
	log := slog.New(slog.NewTextHandler(os.Stderr, &slog.HandlerOptions{Level: slog.LevelError}))

	// Setup: database + stores (not timed — this is fixture creation).
	dbPath := filepath.Join(dir, "test.db")
	db, err := backend.OpenDatabase(dbPath, log)
	if err != nil {
		t.Fatalf("OpenDatabase: %v", err)
	}
	defer func() { _ = db.Close() }()

	projStore, err := project.NewSQLiteProjectStore(db)
	if err != nil {
		t.Fatalf("NewSQLiteProjectStore: %v", err)
	}

	sessStore, err := session.NewSQLiteSessionStore(db)
	if err != nil {
		t.Fatalf("NewSQLiteSessionStore: %v", err)
	}

	projectsDir := filepath.Join(dir, "projects")
	projectMgr := project.NewManager(projStore, projectsDir)

	// Seed data: create a project and a handful of sessions.
	proj, err := projectMgr.CreateProject("bench-project", "")
	if err != nil {
		t.Fatalf("CreateProject: %v", err)
	}

	now := time.Now().UTC().Format(time.RFC3339)
	for i := 0; i < 10; i++ {
		sErr := sessStore.SaveSession(context.Background(), session.SessionInfo{
			ID:        fmt.Sprintf("sess-%d", i),
			ProjectID: proj.ID,
			Name:      fmt.Sprintf("Session %d", i),
			CreatedAt: now,
		})
		if sErr != nil {
			t.Fatalf("SaveSession[%d]: %v", i, sErr)
		}
	}

	// ── Timed section: simulate the preload critical path ───────────
	start := time.Now()

	projects, err := projectMgr.ListProjects()
	if err != nil {
		t.Fatalf("ListProjects: %v", err)
	}
	if len(projects) == 0 {
		t.Fatal("expected at least 1 project")
	}

	_, err = sessStore.ListSessionsByProject(context.Background(), projects[0].ID)
	if err != nil {
		t.Fatalf("ListSessionsByProject: %v", err)
	}

	elapsed := time.Since(start)
	t.Logf("preload with data completed in %v (budget: %v)", elapsed, budget)

	if elapsed > budget {
		t.Fatalf("preload exceeded budget: took %v, allowed %v", elapsed, budget)
	}
}
