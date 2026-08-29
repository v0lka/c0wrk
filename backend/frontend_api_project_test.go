package backend

import (
	"context"
	"database/sql"
	"io"
	"log/slog"
	"os"
	"path/filepath"
	"sync"
	"testing"
	"time"

	"github.com/v0lka/c0wrk/backend/config"
	"github.com/v0lka/c0wrk/backend/project"
	"github.com/v0lka/c0wrk/backend/session"
	"github.com/v0lka/c0wrk/core"
	"github.com/v0lka/sp4rk/orchestration"
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
	projectManager := project.NewManager(projectStore, agentDir, nil)
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
	// Shut down the session manager first so it closes every session's
	// log/dump file handle. On Windows TempDir cleanup fails (unlinkat
	// EBUSY) if any handle to the dumps/*.jsonl file is still open.
	if h.api != nil && h.api.app != nil && h.api.app.manager != nil {
		h.api.app.manager.Shutdown()
	}
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

// TestDeleteProject_RemovesInMemorySessionsAndFiles verifies that deleting a
// project cleans up its in-memory sessions (closing file handles, cancelling
// active tasks) and removes each session's internal files, then removes the
// whole project directory tree from ~/.c0wrk.
func TestDeleteProject_RemovesInMemorySessionsAndFiles(t *testing.T) {
	h := newProjectSwitchHarness(t)
	defer h.close(t)
	// DeleteProject emits a frontend event; wire a no-op emitter.
	h.api.emitEvent = func(string, ...any) {}

	// Create a live (in-memory) session in the project.
	info, err := h.api.app.Manager().CreateSession(h.projectID, h.workspace)
	if err != nil {
		t.Fatalf("CreateSession failed: %v", err)
	}

	// The session dir (logs, etc.) must exist before deletion.
	sessionDir := config.SessionDir(h.api.agentDir, h.projectID, info.ID)
	logFile := config.SessionLogPath(h.api.agentDir, h.projectID, info.ID)
	if _, err := os.Stat(logFile); err != nil {
		t.Fatalf("session log file should exist before project deletion: %v", err)
	}

	projectDir := config.ProjectDir(h.api.agentDir, h.projectID)

	if err := h.api.DeleteProject(h.projectID); err != nil {
		t.Fatalf("DeleteProject failed: %v", err)
	}

	// The in-memory session must be gone.
	if _, exists := h.api.app.Manager().GetSession(info.ID); exists {
		t.Error("in-memory session should be removed on project deletion")
	}

	// The per-session directory (logs/dumps/plans/temp) must be removed.
	if _, err := os.Stat(sessionDir); !os.IsNotExist(err) {
		t.Errorf("session directory should be removed: %s (stat err=%v)", sessionDir, err)
	}

	// The entire project directory tree must be removed.
	if _, err := os.Stat(projectDir); !os.IsNotExist(err) {
		t.Errorf("project directory should be removed: %s (stat err=%v)", projectDir, err)
	}
}

// TestDeleteProject_RemovesStoreOnlySessionFiles verifies that deleting a
// project removes internal files for sessions that exist only in the store
// (never restored into memory). These files are removed as part of the project
// directory tree removal.
func TestDeleteProject_RemovesStoreOnlySessionFiles(t *testing.T) {
	h := newProjectSwitchHarness(t)
	defer h.close(t)
	h.api.emitEvent = func(string, ...any) {}

	storeSessionID := "store-only-session"
	now := time.Now().UTC().Format(time.RFC3339)
	h.seedSession(t, storeSessionID, now, now)

	// Create the session's internal files on disk (as if a previous run had
	// created them) but do NOT restore the session into memory.
	sessionDir := config.SessionDir(h.api.agentDir, h.projectID, storeSessionID)
	logFile := config.SessionLogPath(h.api.agentDir, h.projectID, storeSessionID)
	if err := os.MkdirAll(filepath.Dir(logFile), 0o755); err != nil {
		t.Fatalf("mkdir log dir: %v", err)
	}
	if err := os.WriteFile(logFile, []byte("old log"), 0o644); err != nil {
		t.Fatalf("write log file: %v", err)
	}

	if err := h.api.DeleteProject(h.projectID); err != nil {
		t.Fatalf("DeleteProject failed: %v", err)
	}

	// The store-only session's files are removed with the project dir tree.
	if _, err := os.Stat(sessionDir); !os.IsNotExist(err) {
		t.Errorf("store-only session directory should be removed: %s (stat err=%v)", sessionDir, err)
	}
}

// TestSwitchProject_AlreadyActive_EmitsSwitchedEvent pins the reconciliation
// contract for the idempotent path: SwitchProject(same id) must STILL emit
// project:switched. A frontend whose local activeProjectId went stale (e.g.
// after a failed or interleaved toggle) reconciles exclusively via its
// project:switched subscription — without the event, every later toggle that
// reaches the backend takes the early return, the backend never re-syncs the
// frontend, ListDirectory keeps rejecting the frontend's rootPath ("path
// outside project workspace") and @-file completions in the chat input stay
// empty until an app restart.
func TestSwitchProject_AlreadyActive_EmitsSwitchedEvent(t *testing.T) {
	h := newProjectSwitchHarness(t)
	defer h.close(t)

	// Seed a session so applySavedProjectSwitchState resolves a fallback
	// without creating one through the nil-orchestrator factory.
	now := time.Now().UTC().Format(time.RFC3339)
	h.seedSession(t, "session-a", now, now)

	switched := 0
	h.api.emitEvent = func(name string, _ ...any) {
		if name == EventProjectSwitched {
			switched++
		}
	}
	// The harness Application has no real builder; switchProjectActivate
	// calls SetMCPWorkDir on it. Without an override, builder() returns an
	// interface holding a typed-nil *core.OrchestratorBuilder which passes
	// the nil check and panics on the nil receiver.
	h.api.builderOverride = &mockBuilder{}
	t.Cleanup(func() {
		h.api.watcherMu.Lock()
		defer h.api.watcherMu.Unlock()
		if h.api.watcher != nil {
			_ = h.api.watcher.Close()
			h.api.watcher = nil
		}
	})

	// First switch: full activation path, emits once.
	if err := h.api.SwitchProject(h.projectID); err != nil {
		t.Fatalf("SwitchProject: %v", err)
	}
	if switched != 1 {
		t.Fatalf("expected 1 project:switched after full switch, got %d", switched)
	}

	// Idempotent re-switch: must still emit so a desynced frontend reconciles.
	if err := h.api.SwitchProject(h.projectID); err != nil {
		t.Fatalf("SwitchProject (already active): %v", err)
	}
	if switched != 2 {
		t.Fatalf("expected project:switched on the already-active path, got %d events", switched)
	}
}

// TestSwitchProject_ConcurrentSwitchesSerialize pins the backend-side switch
// serialization contract. Wails runs each binding call in its own goroutine,
// so two rapid CHAT↔CODE toggles arrive on two goroutines. Without serializing
// the SwitchProject body, the switches interleave inside the backend and a
// slower EARLIER switch can overwrite activeProjectID after a later switch has
// already completed — the backend ends on the older project while the
// frontend's serialized switch chain believes the newer one. Every subsequent
// ListDirectory against the frontend's rootPath then fails containment
// ("path outside project workspace") and @-file completions stay empty until
// an app restart.
func TestSwitchProject_ConcurrentSwitchesSerialize(t *testing.T) {
	h := newProjectSwitchHarness(t)
	defer h.close(t)

	second, err := h.api.projectManager.CreateProject("Switch Target 2", "")
	if err != nil {
		t.Fatalf("failed to create second test project: %v", err)
	}

	h.api.emitEvent = func(string, ...any) {}
	h.api.builderOverride = &mockBuilder{}
	t.Cleanup(func() {
		h.api.watcherMu.Lock()
		defer h.api.watcherMu.Unlock()
		if h.api.watcher != nil {
			_ = h.api.watcher.Close()
			h.api.watcher = nil
		}
	})

	// Block the FIRST switch mid-body (inside switchMu) and only let it finish
	// after the second switch has been dispatched. The hook runs once; later
	// switches pass through unblocked.
	inFirst := make(chan struct{})
	releaseFirst := make(chan struct{})
	var hookOnce sync.Once
	h.api.switchInProgressHook = func(string) {
		hookOnce.Do(func() {
			close(inFirst)
			<-releaseFirst
		})
	}

	errFirst := make(chan error, 1)
	go func() { errFirst <- h.api.SwitchProject(h.projectID) }()
	<-inFirst // first switch is mid-body, holding switchMu

	errSecond := make(chan error, 1)
	go func() { errSecond <- h.api.SwitchProject(second.ID) }()

	// While the first switch is in progress the second must NOT be able to
	// complete: switchMu must exclude it. Sample for 150ms.
	select {
	case err := <-errSecond:
		t.Fatalf("second switch completed while first was still in progress (switch serialization broken): %v", err)
	case <-time.After(150 * time.Millisecond):
		// Expected: second switch is blocked on switchMu.
	}

	close(releaseFirst)
	if err := <-errFirst; err != nil {
		t.Fatalf("first SwitchProject: %v", err)
	}
	if err := <-errSecond; err != nil {
		t.Fatalf("second SwitchProject: %v", err)
	}

	// The LAST-arrived switch must always win. Without serialization, the
	// interleaved first switch's activate step could land after the second
	// switch completed, leaving the backend on h.projectID.
	if got := activeProjectIDForTest(h.api); got != second.ID {
		t.Fatalf("backend ended on project %q, want the last-arrived switch target %q", got, second.ID)
	}
}

func activeProjectIDForTest(f *FrontendAPI) string {
	f.activeProjectMu.RLock()
	defer f.activeProjectMu.RUnlock()
	return f.activeProjectID
}


// TestGetSessionWorkspace_NoProject_ForeignSessionDoesNotLeak pins the CHAT
// mode isolation guard: while No Project is active, GetSessionWorkspace must
// never return the registered workspace of a session that belongs to a REAL
// project. The old unconditional short-circuit (activeProjectID == NoProjectID
// ⇒ return wsPath) let a stale cross-project activeSessionId set the file-tree
// root outside the No Project tree — ListDirectory then rejected that root on
// every call and @-file completions died until an app restart.
func TestGetSessionWorkspace_NoProject_ForeignSessionDoesNotLeak(t *testing.T) {
	base := t.TempDir()
	realWS := filepath.Join(base, "real", "workspace")
	if err := os.MkdirAll(realWS, 0o755); err != nil {
		t.Fatalf("mkdir real workspace: %v", err)
	}

	factory := func(core.Emitter, *slog.Logger, string, core.BlackboardFactory, io.Writer, *orchestration.StepDumpTracker) (*core.Orchestrator, error) {
		return nil, nil
	}
	manager := session.NewManager(factory, func(session.Event) {}, base)
	t.Cleanup(manager.Shutdown)

	// A session registered under a real project with a real workspace.
	created, err := manager.CreateSession("real-project", realWS)
	if err != nil {
		t.Fatalf("CreateSession: %v", err)
	}

	f := &FrontendAPI{
		app:       &Application{manager: manager},
		agentDir:  base,
		emitEvent: func(string, ...any) {},
	}
	f.activeProjectMu.Lock()
	f.activeProjectID = project.NoProjectID
	f.activeProjectPath = filepath.Join(base, "__no_project__")
	f.activeProjectMu.Unlock()

	got, err := f.GetSessionWorkspace(created.ID)
	if err != nil {
		t.Fatalf("GetSessionWorkspace: %v", err)
	}
	want, absErr := filepath.Abs(config.NoProjectSessionWorkspace(base, created.ID))
	if absErr != nil {
		t.Fatalf("abs: %v", absErr)
	}
	if got != want {
		t.Fatalf("foreign session leaked real workspace into CHAT mode: got %q, want derived No Project path %q", got, want)
	}
}
