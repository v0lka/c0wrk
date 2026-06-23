package backend

import (
	"context"
	"database/sql"
	"io"
	"log/slog"
	"testing"
	"time"

	"github.com/v0lka/c0wrk/backend/project"
	"github.com/v0lka/c0wrk/backend/session"
	"github.com/v0lka/c0wrk/core"
	"github.com/v0lka/c0wrk/sdk/orchestration"
	_ "modernc.org/sqlite"
)

type projectSwitchTestHarness struct {
	api          *FrontendAPI
	ctx          context.Context
	db           *sql.DB
	projectStore *project.SQLiteProjectStore
	sessionStore *session.SQLiteSessionStore
	projectID    string
	workspace    string
}

func openProjectSwitchTestDB(t *testing.T) *sql.DB {
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

func newProjectSwitchHarness(t *testing.T) *projectSwitchTestHarness {
	t.Helper()

	ctx := context.Background()
	db := openProjectSwitchTestDB(t)

	projectStore, err := project.NewSQLiteProjectStore(db)
	if err != nil {
		_ = db.Close()
		t.Fatalf("failed to create project store: %v", err)
	}

	sessionStore, err := session.NewSQLiteSessionStore(db)
	if err != nil {
		_ = db.Close()
		t.Fatalf("failed to create session store: %v", err)
	}

	agentDir := t.TempDir()
	projectManager := project.NewManager(projectStore, agentDir)
	createdProject, err := projectManager.CreateProject("Switch Target", "")
	if err != nil {
		_ = db.Close()
		t.Fatalf("failed to create test project: %v", err)
	}

	factory := func(emitter core.Emitter, logger *slog.Logger, workspacePath string, bbFactory core.BlackboardFactory, dumpWriter io.Writer, _ *orchestration.StepDumpTracker) (*core.Orchestrator, error) {
		return nil, nil
	}
	manager := session.NewManager(factory, func(session.Event) {}, agentDir)
	manager.SetSessionStore(sessionStore)
	manager.SetProjectResolver(func(projectID string) (string, error) {
		p, err := projectManager.GetProject(projectID)
		if err != nil || p == nil {
			return "", err
		}
		return p.WorkspacePath, nil
	})

	api := &FrontendAPI{
		app:            &Application{manager: manager},
		store:          sessionStore,
		projStore:      projectStore,
		projectManager: projectManager,
		agentDir:       agentDir,
		appCtx:         func() context.Context { return ctx },
	}

	return &projectSwitchTestHarness{
		api:          api,
		ctx:          ctx,
		db:           db,
		projectStore: projectStore,
		sessionStore: sessionStore,
		projectID:    createdProject.ID,
		workspace:    createdProject.WorkspacePath,
	}
}

func (h *projectSwitchTestHarness) close(t *testing.T) {
	t.Helper()
	if err := h.db.Close(); err != nil {
		t.Fatalf("failed to close db: %v", err)
	}
}

func (h *projectSwitchTestHarness) saveUIState(t *testing.T, savedSessionID string) {
	t.Helper()
	err := h.projectStore.SaveUIState(h.ctx, project.ProjectUIState{
		ProjectID:      h.projectID,
		SavedSessionID: savedSessionID,
		OpenTabs:       []string{"README.md"},
		ActiveFile:     "README.md",
	})
	if err != nil {
		t.Fatalf("failed to save UI state: %v", err)
	}
}

func (h *projectSwitchTestHarness) loadUIState(t *testing.T) *project.ProjectUIState {
	t.Helper()
	state, err := h.projectStore.LoadUIState(h.ctx, h.projectID)
	if err != nil {
		t.Fatalf("failed to load UI state: %v", err)
	}
	if state == nil {
		t.Fatal("expected UI state to be present")
	}
	return state
}

func (h *projectSwitchTestHarness) seedSessionForProject(t *testing.T, projectID, id, createdAt, lastActiveAt string) {
	t.Helper()
	err := h.sessionStore.SaveSession(h.ctx, session.SessionInfo{
		ID:           id,
		ProjectID:    projectID,
		Name:         "Session " + id,
		CreatedAt:    createdAt,
		LastActiveAt: lastActiveAt,
	})
	if err != nil {
		t.Fatalf("failed to seed session %q: %v", id, err)
	}
}

func (h *projectSwitchTestHarness) seedSession(t *testing.T, id, createdAt, lastActiveAt string) {
	t.Helper()
	h.seedSessionForProject(t, h.projectID, id, createdAt, lastActiveAt)
}

func TestSaveAndGetProjectUIState_RoundTripOpenFilesAndSavedSession(t *testing.T) {
	h := newProjectSwitchHarness(t)
	defer h.close(t)

	now := time.Now().UTC()
	h.seedSession(t, "session-a", now.Add(-time.Hour).Format(time.RFC3339), now.Add(-time.Minute).Format(time.RFC3339))

	err := h.api.SaveProjectUIState(ProjectUIStateRequest{
		ProjectID:      h.projectID,
		SavedSessionID: "session-a",
		OpenTabs:       []string{"README.md", "src/main.go"},
		ActiveFile:     "src/main.go",
	})
	if err != nil {
		t.Fatalf("SaveProjectUIState failed: %v", err)
	}

	restored, err := h.api.GetProjectUIState(h.projectID)
	if err != nil {
		t.Fatalf("GetProjectUIState failed: %v", err)
	}
	if restored == nil {
		t.Fatal("expected persisted project UI state")
	}
	if restored.ProjectID != h.projectID {
		t.Fatalf("project mismatch: got %q want %q", restored.ProjectID, h.projectID)
	}
	if restored.SavedSessionID != "session-a" {
		t.Fatalf("saved session mismatch: got %q", restored.SavedSessionID)
	}
	if len(restored.OpenTabs) != 2 || restored.OpenTabs[0] != "README.md" || restored.OpenTabs[1] != "src/main.go" {
		t.Fatalf("open tabs mismatch: %#v", restored.OpenTabs)
	}
	if restored.ActiveFile != "src/main.go" {
		t.Fatalf("active file mismatch: got %q", restored.ActiveFile)
	}
	if restored.UpdatedAt == "" {
		t.Fatal("expected updated_at to be populated")
	}
}

func TestSaveAndGetProjectUIState_NormalizesOpenTabsAndActiveFile(t *testing.T) {
	h := newProjectSwitchHarness(t)
	defer h.close(t)

	err := h.api.SaveProjectUIState(ProjectUIStateRequest{
		ProjectID:      h.projectID,
		SavedSessionID: "",
		OpenTabs:       []string{"", " README.md ", "README.md", "src/main.go", "src/main.go", "   "},
		ActiveFile:     "missing.go",
	})
	if err != nil {
		t.Fatalf("SaveProjectUIState failed: %v", err)
	}

	restored, err := h.api.GetProjectUIState(h.projectID)
	if err != nil {
		t.Fatalf("GetProjectUIState failed: %v", err)
	}
	if restored == nil {
		t.Fatal("expected persisted project UI state")
	}
	if len(restored.OpenTabs) != 2 || restored.OpenTabs[0] != "README.md" || restored.OpenTabs[1] != "src/main.go" {
		t.Fatalf("expected normalized unique open tabs, got %#v", restored.OpenTabs)
	}
	if restored.ActiveFile != "" {
		t.Fatalf("expected invalid active file to be cleared, got %q", restored.ActiveFile)
	}
}

func TestSaveAndGetProjectUIState_ProjectScopedIsolation(t *testing.T) {
	h := newProjectSwitchHarness(t)
	defer h.close(t)

	otherProject, err := h.api.projectManager.CreateProject("Other", "")
	if err != nil {
		t.Fatalf("failed to create second project: %v", err)
	}

	now := time.Now().UTC()
	h.seedSessionForProject(t, h.projectID, "p1-session", now.Add(-2*time.Hour).Format(time.RFC3339), now.Add(-90*time.Minute).Format(time.RFC3339))
	h.seedSessionForProject(t, otherProject.ID, "p2-session", now.Add(-time.Hour).Format(time.RFC3339), now.Add(-30*time.Minute).Format(time.RFC3339))

	if err := h.api.SaveProjectUIState(ProjectUIStateRequest{
		ProjectID:      h.projectID,
		SavedSessionID: "p1-session",
		OpenTabs:       []string{"project1.md"},
		ActiveFile:     "project1.md",
	}); err != nil {
		t.Fatalf("failed to save project one UI state: %v", err)
	}

	if err := h.api.SaveProjectUIState(ProjectUIStateRequest{
		ProjectID:      otherProject.ID,
		SavedSessionID: "p2-session",
		OpenTabs:       []string{"project2.md"},
		ActiveFile:     "project2.md",
	}); err != nil {
		t.Fatalf("failed to save project two UI state: %v", err)
	}

	stateOne, err := h.api.GetProjectUIState(h.projectID)
	if err != nil {
		t.Fatalf("failed to get project one UI state: %v", err)
	}
	stateTwo, err := h.api.GetProjectUIState(otherProject.ID)
	if err != nil {
		t.Fatalf("failed to get project two UI state: %v", err)
	}

	if stateOne == nil || stateTwo == nil {
		t.Fatalf("expected both project UI states to be present, got stateOne=%v stateTwo=%v", stateOne, stateTwo)
	}
	if stateOne.ProjectID == stateTwo.ProjectID {
		t.Fatalf("expected distinct projects, got %q", stateOne.ProjectID)
	}
	if stateOne.ActiveFile != "project1.md" || len(stateOne.OpenTabs) != 1 || stateOne.OpenTabs[0] != "project1.md" {
		t.Fatalf("unexpected project one restore payload: %+v", *stateOne)
	}
	if stateTwo.ActiveFile != "project2.md" || len(stateTwo.OpenTabs) != 1 || stateTwo.OpenTabs[0] != "project2.md" {
		t.Fatalf("unexpected project two restore payload: %+v", *stateTwo)
	}
}

func TestApplySavedProjectSwitchState_UsesSavedSessionWhenValid(t *testing.T) {
	h := newProjectSwitchHarness(t)
	defer h.close(t)

	now := time.Now().UTC()
	h.seedSession(t, "saved-valid", now.Add(-2*time.Hour).Format(time.RFC3339), now.Add(-2*time.Hour).Format(time.RFC3339))
	h.seedSession(t, "latest-other", now.Add(-time.Hour).Format(time.RFC3339), now.Add(-time.Minute).Format(time.RFC3339))
	h.saveUIState(t, "saved-valid")

	h.api.applySavedProjectSwitchState(h.projectID)

	state := h.loadUIState(t)
	if state.SavedSessionID != "saved-valid" {
		t.Fatalf("expected valid saved session to win fallback order, got %q", state.SavedSessionID)
	}
}

func TestApplySavedProjectSwitchState_FallsBackToLatestWhenSavedMissing(t *testing.T) {
	h := newProjectSwitchHarness(t)
	defer h.close(t)

	now := time.Now().UTC()
	h.seedSession(t, "older", now.Add(-3*time.Hour).Format(time.RFC3339), now.Add(-3*time.Hour).Format(time.RFC3339))
	h.seedSession(t, "latest", now.Add(-2*time.Hour).Format(time.RFC3339), now.Add(-5*time.Minute).Format(time.RFC3339))
	h.saveUIState(t, "missing-session")

	h.api.applySavedProjectSwitchState(h.projectID)

	state := h.loadUIState(t)
	if state.SavedSessionID != "latest" {
		t.Fatalf("expected latest session fallback, got %q", state.SavedSessionID)
	}

	sessions, err := h.sessionStore.ListSessionsByProject(h.ctx, h.projectID)
	if err != nil {
		t.Fatalf("failed to list sessions: %v", err)
	}
	if len(sessions) != 2 {
		t.Fatalf("expected no new session creation when latest exists, got %d sessions", len(sessions))
	}
}

func TestApplySavedProjectSwitchState_CreatesSessionWhenProjectHasNone(t *testing.T) {
	h := newProjectSwitchHarness(t)
	defer h.close(t)

	h.saveUIState(t, "")

	h.api.applySavedProjectSwitchState(h.projectID)

	state := h.loadUIState(t)
	if state.SavedSessionID == "" {
		t.Fatal("expected fallback to create a new session and persist saved_session_id")
	}

	persisted, err := h.sessionStore.LoadSession(h.ctx, state.SavedSessionID)
	if err != nil {
		t.Fatalf("failed to load created fallback session: %v", err)
	}
	if persisted == nil {
		t.Fatal("expected created fallback session to be persisted")
	}
	if persisted.ProjectID != h.projectID {
		t.Fatalf("expected created session project_id %q, got %q", h.projectID, persisted.ProjectID)
	}
}
