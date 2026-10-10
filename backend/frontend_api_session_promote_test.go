package backend

import (
	"context"
	"database/sql"
	"encoding/json"
	"log/slog"
	"os"
	"path/filepath"
	"reflect"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/v0lka/c0wrk/backend/config"
	"github.com/v0lka/c0wrk/backend/project"
	"github.com/v0lka/c0wrk/backend/session"
)

// promoteEventCapture records emitted event names for assertions.
type promoteEventCapture struct {
	mu    sync.Mutex
	names []string
}

func (c *promoteEventCapture) emit(name string, _ ...any) {
	c.mu.Lock()
	defer c.mu.Unlock()
	c.names = append(c.names, name)
}

func (c *promoteEventCapture) has(name string) bool {
	c.mu.Lock()
	defer c.mu.Unlock()
	for _, n := range c.names {
		if n == name {
			return true
		}
	}
	return false
}

// promoteHarness bundles everything a promotion test drives: the RPC surface,
// both stores, the isolated agent dir, the event capture, and the raw DB (for
// failure injection).
type promoteHarness struct {
	api      *FrontendAPI
	sessions *session.SQLiteSessionStore
	projects *project.Manager
	agentDir string
	events   *promoteEventCapture
	db       *sql.DB
}

// newPromoteTestAPI builds a minimal FrontendAPI wired to real session and
// project stores over one in-memory SQLite database, with the No Project
// pseudo-project ensured and a capture for emitted events. agentDir is an
// isolated temp dir so the file moves run against a disposable layout.
func newPromoteTestAPI(t *testing.T) *promoteHarness {
	t.Helper()

	dbConn := openProjectSwitchTestDB(t)
	t.Cleanup(func() { _ = dbConn.Close() })

	projectStore, err := project.NewSQLiteProjectStore(dbConn)
	if err != nil {
		t.Fatalf("project store: %v", err)
	}
	sessionStore, err := session.NewSQLiteSessionStore(dbConn)
	if err != nil {
		t.Fatalf("session store: %v", err)
	}

	agentDir := t.TempDir()
	projectManager := project.NewManager(projectStore, agentDir, nil)
	if created, err := projectManager.EnsureNoProject(); err != nil || !created {
		t.Fatalf("EnsureNoProject: created=%v err=%v", created, err)
	}

	events := &promoteEventCapture{}
	// seedPublishedAPI performs Init's last publication step on this
	// hand-built instance: the promotion path reads seed fields through
	// guardless entries (stopSessionTerminal's seedAcquire), which block
	// forever until the seed is published.
	api := seedPublishedAPI(&FrontendAPI{
		app:            &Application{manager: session.NewManager(nil, func(session.Event) {}, agentDir)},
		store:          sessionStore,
		projStore:      projectStore,
		projectManager: projectManager,
		agentDir:       agentDir,
		emitEvent:      events.emit,
	})
	return &promoteHarness{
		api:      api,
		sessions: sessionStore,
		projects: projectManager,
		agentDir: agentDir,
		events:   events,
		db:       dbConn,
	}
}

// seedPromotableChatSession persists a CHAT session with on-disk workspace
// content (a file inside the per-session workspace) and one message whose
// metadata embeds an image path under the session's images dir.
func seedPromotableChatSession(t *testing.T, store *session.SQLiteSessionStore, agentDir, sessionID string) (srcDir, srcWorkspace string) {
	t.Helper()
	ctx := context.Background()
	if err := store.SaveSession(ctx, session.SessionInfo{
		ID:        sessionID,
		ProjectID: project.NoProjectID,
		Name:      "Chat " + sessionID,
		CreatedAt: time.Now().Format(time.RFC3339),
		Archived:  true,
	}); err != nil {
		t.Fatalf("save session: %v", err)
	}

	srcDir = config.SessionDir(agentDir, project.NoProjectID, sessionID)
	srcWorkspace = filepath.Join(srcDir, config.NoProjectWorkspaceSegment)
	if err := os.MkdirAll(srcWorkspace, 0o755); err != nil {
		t.Fatalf("mkdir workspace: %v", err)
	}
	if err := os.WriteFile(filepath.Join(srcWorkspace, "scratch.py"), []byte("print(1)"), 0o644); err != nil {
		t.Fatalf("write workspace file: %v", err)
	}

	oldImage := filepath.Join(srcDir, "images", "img_1.jpg")
	metadata, err := json.Marshal(map[string]any{
		"images": []any{map[string]any{"path": oldImage, "media_type": "image/jpeg"}},
	})
	if err != nil {
		t.Fatalf("marshal metadata: %v", err)
	}
	if err := store.SaveMessage(ctx, session.ChatMessage{
		SessionID: sessionID,
		Role:      "user",
		Content:   "look at the screenshot",
		Metadata:  metadata,
		CreatedAt: time.Now().Format(time.RFC3339),
	}); err != nil {
		t.Fatalf("save message: %v", err)
	}
	return srcDir, srcWorkspace
}

func TestPromoteSessionToProject_HappyPath(t *testing.T) {
	h := newPromoteTestAPI(t)
	api, sessionStore, agentDir, events := h.api, h.sessions, h.agentDir, h.events
	srcDir, _ := seedPromotableChatSession(t, sessionStore, agentDir, "s-happy")
	ctx := context.Background()

	proj, err := api.PromoteSessionToProject("s-happy", "My Promoted Project")
	if err != nil {
		t.Fatalf("PromoteSessionToProject failed: %v", err)
	}

	if proj.Name != "My Promoted Project" {
		t.Fatalf("project name = %q", proj.Name)
	}
	if proj.IsExternal || proj.IsNoProject {
		t.Fatalf("promoted project must be internal, got external=%v nopProject=%v", proj.IsExternal, proj.IsNoProject)
	}
	wantWorkspace := filepath.Join(agentDir, "projects", proj.ID, "Workspace")
	if proj.WorkspacePath != wantWorkspace {
		t.Fatalf("workspace path = %q, want %q", proj.WorkspacePath, wantWorkspace)
	}

	// The session row moved and unarchived.
	loaded, err := sessionStore.LoadSession(ctx, "s-happy")
	if err != nil {
		t.Fatalf("load session: %v", err)
	}
	if loaded.ProjectID != proj.ID || loaded.Archived {
		t.Fatalf("session row not re-parented: project=%q archived=%v", loaded.ProjectID, loaded.Archived)
	}

	// The files moved: workspace content at the project Workspace, session
	// infra under the project, nothing left in __no_project__.
	if _, err := os.Stat(filepath.Join(wantWorkspace, "scratch.py")); err != nil {
		t.Fatalf("workspace content must live at the project Workspace: %v", err)
	}
	if _, err := os.Stat(srcDir); !os.IsNotExist(err) {
		t.Fatalf("old session dir must be gone (stat err=%v)", err)
	}

	// The structured image path followed the files.
	msgs, err := sessionStore.LoadMessages(ctx, "s-happy")
	if err != nil {
		t.Fatalf("load messages: %v", err)
	}
	var meta struct {
		Images []struct {
			Path string `json:"path"`
		} `json:"images"`
	}
	if err := json.Unmarshal(msgs[0].Metadata, &meta); err != nil {
		t.Fatalf("unmarshal metadata: %v", err)
	}
	wantImage := filepath.Join(config.SessionDir(agentDir, proj.ID, "s-happy"), "images", "img_1.jpg")
	if meta.Images[0].Path != wantImage {
		t.Fatalf("image path = %q, want %q", meta.Images[0].Path, wantImage)
	}

	// Saved-session pointers: the new project starts on the promoted session;
	// No Project drops its stale pointer.
	if ui, err := api.projStore.LoadUIState(ctx, proj.ID); err != nil || ui == nil || ui.SavedSessionID != "s-happy" {
		t.Fatalf("new project saved session pointer wrong: ui=%+v err=%v", ui, err)
	}
	if ui, err := api.projStore.LoadUIState(ctx, project.NoProjectID); err != nil || (ui != nil && ui.SavedSessionID != "") {
		t.Fatalf("No Project pointer must be cleared: ui=%+v err=%v", ui, err)
	}

	if !events.has(EventProjectCreated) {
		t.Fatal("project:created event must be emitted")
	}

	// git init is fail-soft; assert the repository only when git is usable.
	if gitOnPath() {
		if _, err := os.Stat(filepath.Join(wantWorkspace, ".git")); err != nil {
			t.Fatalf("promoted workspace must carry a git repository: %v", err)
		}
	}
}

func TestPromoteSessionToProject_Guards(t *testing.T) {
	h := newPromoteTestAPI(t)
	api, sessionStore, projectManager, agentDir := h.api, h.sessions, h.projects, h.agentDir
	srcDir, _ := seedPromotableChatSession(t, sessionStore, agentDir, "s-guard")
	ctx := context.Background()

	// Non-CHAT session → refused.
	realProj, err := projectManager.CreateProject("Real Project", "")
	if err != nil {
		t.Fatalf("create real project: %v", err)
	}
	if err := sessionStore.SaveSession(ctx, session.SessionInfo{
		ID: "code-s", ProjectID: realProj.ID, Name: "code",
		CreatedAt: time.Now().Format(time.RFC3339),
	}); err != nil {
		t.Fatalf("save code session: %v", err)
	}

	projectsBefore, err := projectManager.ListProjects()
	if err != nil {
		t.Fatalf("list projects: %v", err)
	}
	beforeCount := len(projectsBefore)

	// Unfinished task → refused, nothing changes.
	if err := sessionStore.SaveTask(ctx, session.TaskRecord{
		ID: "task-run", SessionID: "s-guard", OriginalRequest: "running",
		RoutingDecision: json.RawMessage(`{}`), Plan: json.RawMessage(`{}`),
		Reflections: json.RawMessage(`[]`), Status: "in_progress", CreatedAt: time.Now(),
	}); err != nil {
		t.Fatalf("save task: %v", err)
	}
	if _, err := api.PromoteSessionToProject("s-guard", "X"); err == nil {
		t.Fatal("promoting a session with an unfinished task must be refused")
	} else if !strings.Contains(err.Error(), "unfinished task") {
		t.Fatalf("unexpected guard error: %v", err)
	}

	if _, err := api.PromoteSessionToProject("code-s", "Y"); err == nil {
		t.Fatal("promoting a CODE session must be refused")
	}

	// Unknown session and empty name → refused.
	if _, err := api.PromoteSessionToProject("missing", "Z"); err == nil {
		t.Fatal("promoting an unknown session must be refused")
	}
	if _, err := api.PromoteSessionToProject("s-guard", "   "); err == nil {
		t.Fatal("an empty project name must be refused")
	}

	projectsAfter, err := projectManager.ListProjects()
	if err != nil {
		t.Fatalf("list projects: %v", err)
	}
	if len(projectsAfter) != beforeCount {
		t.Fatalf("refused promotions must not create projects: before=%d after=%d", beforeCount, len(projectsAfter))
	}
	if _, err := os.Stat(srcDir); err != nil {
		t.Fatalf("refused promotions must not touch the files: %v", err)
	}
	loaded, err := sessionStore.LoadSession(ctx, "s-guard")
	if err != nil {
		t.Fatalf("load session: %v", err)
	}
	if loaded.ProjectID != project.NoProjectID {
		t.Fatalf("refused promotion must keep the session in CHAT, got %q", loaded.ProjectID)
	}
}

func TestPromoteSessionToProject_CompensatesWhenStoreFails(t *testing.T) {
	h := newPromoteTestAPI(t)
	api, sessionStore, projectManager, agentDir, dbConn := h.api, h.sessions, h.projects, h.agentDir, h.db
	srcDir, _ := seedPromotableChatSession(t, sessionStore, agentDir, "s-comp")
	ctx := context.Background()

	// Force the store-level promotion to fail mid-transaction: the metadata
	// rewrite scans session_messages, which no longer exists. The sessions
	// UPDATE rolls back with it (the store method's transactional guarantee).
	if _, err := dbConn.ExecContext(ctx, "DROP TABLE session_messages"); err != nil {
		t.Fatalf("drop session_messages: %v", err)
	}

	if _, err := api.PromoteSessionToProject("s-comp", "Doomed Project"); err == nil {
		t.Fatal("the store failure must fail the promotion")
	}

	// The session row rolled back to CHAT.
	loaded, err := sessionStore.LoadSession(ctx, "s-comp")
	if err != nil {
		t.Fatalf("load session: %v", err)
	}
	if loaded.ProjectID != project.NoProjectID || !loaded.Archived {
		t.Fatalf("failed promotion must leave the session untouched: project=%q archived=%v", loaded.ProjectID, loaded.Archived)
	}

	// The files returned to the No Project layout.
	if _, err := os.Stat(filepath.Join(srcDir, config.NoProjectWorkspaceSegment, "scratch.py")); err != nil {
		t.Fatalf("workspace must be back inside the session dir: %v", err)
	}

	// The compensation project is gone.
	projects, err := projectManager.ListProjects()
	if err != nil {
		t.Fatalf("list projects: %v", err)
	}
	for _, p := range projects {
		if p.Name == "Doomed Project" {
			t.Fatalf("the compensation project must be deleted, projects=%+v", projects)
		}
	}
}

func TestPromoteSessionToProject_CompensatesWhenMoveFails(t *testing.T) {
	h := newPromoteTestAPI(t)
	api, sessionStore, projectManager, agentDir := h.api, h.sessions, h.projects, h.agentDir
	ctx := context.Background()

	// A session dir WITHOUT the workspace segment: hop 1 (session dir rename)
	// succeeds, hop 2 (workspace lift) fails and rolls itself back inside the
	// manager; the RPC must then delete the fresh project.
	srcDir := config.SessionDir(agentDir, project.NoProjectID, "s-mvfail")
	if err := os.MkdirAll(filepath.Join(srcDir, "logs"), 0o755); err != nil {
		t.Fatalf("mkdir source: %v", err)
	}
	if err := sessionStore.SaveSession(ctx, session.SessionInfo{
		ID: "s-mvfail", ProjectID: project.NoProjectID, Name: "no workspace",
		CreatedAt: time.Now().Format(time.RFC3339),
	}); err != nil {
		t.Fatalf("save session: %v", err)
	}

	if _, err := api.PromoteSessionToProject("s-mvfail", "Doomed Move"); err == nil {
		t.Fatal("the move failure must fail the promotion")
	}

	if _, err := os.Stat(srcDir); err != nil {
		t.Fatalf("the session dir must be restored by the rollback: %v", err)
	}
	loaded, err := sessionStore.LoadSession(ctx, "s-mvfail")
	if err != nil {
		t.Fatalf("load session: %v", err)
	}
	if loaded.ProjectID != project.NoProjectID {
		t.Fatalf("failed promotion must leave the session in CHAT, got %q", loaded.ProjectID)
	}
	projects, err := projectManager.ListProjects()
	if err != nil {
		t.Fatalf("list projects: %v", err)
	}
	for _, p := range projects {
		if p.Name == "Doomed Move" {
			t.Fatalf("the compensation project must be deleted, projects=%+v", projects)
		}
	}
}

// promoteDiagnosticSink captures the Warn+ diagnostics of one FrontendAPI so
// the compensation failure paths assert their exact messages, levels and call
// counts instead of letting them leak into the test output.
type promoteDiagnosticSink struct {
	mu       sync.Mutex
	messages []string
	levels   []slog.Level
}

type promoteDiagnosticHandler struct {
	next slog.Handler
	sink *promoteDiagnosticSink
}

func (h *promoteDiagnosticHandler) Enabled(_ context.Context, level slog.Level) bool {
	return level >= slog.LevelWarn
}

func (h *promoteDiagnosticHandler) Handle(_ context.Context, r slog.Record) error {
	h.sink.mu.Lock()
	defer h.sink.mu.Unlock()
	h.sink.messages = append(h.sink.messages, r.Message)
	h.sink.levels = append(h.sink.levels, r.Level)
	return nil
}

func (h *promoteDiagnosticHandler) WithAttrs(attrs []slog.Attr) slog.Handler {
	return &promoteDiagnosticHandler{next: h.next.WithAttrs(attrs), sink: h.sink}
}

func (h *promoteDiagnosticHandler) WithGroup(name string) slog.Handler {
	return &promoteDiagnosticHandler{next: h.next.WithGroup(name), sink: h.sink}
}

// capturePromoteDiagnostics routes the API's logger through the sink and, on
// cleanup, requires exactly the expected diagnostics at Error level.
func capturePromoteDiagnostics(t *testing.T, api *FrontendAPI, wantMessages ...string) {
	t.Helper()
	previous := api.logger
	base := previous
	if base == nil {
		base = slog.Default() // the harness leaves the logger unset
	}
	sink := &promoteDiagnosticSink{}
	api.logger = slog.New(&promoteDiagnosticHandler{next: base.Handler(), sink: sink})
	t.Cleanup(func() {
		api.logger = previous
		sink.mu.Lock()
		defer sink.mu.Unlock()
		if !reflect.DeepEqual(sink.messages, wantMessages) {
			t.Errorf("promotion diagnostics(%s) = %#v, want %#v", t.Name(), sink.messages, wantMessages)
		}
		for i, level := range sink.levels {
			if level != slog.LevelError {
				t.Errorf("promotion diagnostics(%s) record %d level = %v, want Error", t.Name(), i, level)
			}
		}
	})
}

func TestPromoteSessionToProject_KeepsProjectWhenUndoFails(t *testing.T) {
	h := newPromoteTestAPI(t)
	api, projectManager, agentDir := h.api, h.projects, h.agentDir

	proj, err := projectManager.CreateProject("Stranded Project", "")
	if err != nil {
		t.Fatalf("create project: %v", err)
	}

	// Craft the ambiguous layout the compensation must refuse to touch: both
	// session directories exist (e.g. a lazy restore recreated the source
	// mid-promotion), so the undo refuses and the project tree still holds
	// the moved files.
	dstSessionDir := config.SessionDir(agentDir, proj.ID, "s-undofail")
	if err := os.MkdirAll(filepath.Join(dstSessionDir, config.NoProjectWorkspaceSegment), 0o755); err != nil {
		t.Fatalf("mkdir destination layout: %v", err)
	}
	keptFile := filepath.Join(dstSessionDir, config.NoProjectWorkspaceSegment, "precious.txt")
	if err := os.WriteFile(keptFile, []byte("session data"), 0o644); err != nil {
		t.Fatalf("write stranded file: %v", err)
	}
	srcDir := config.SessionDir(agentDir, project.NoProjectID, "s-undofail")
	if err := os.MkdirAll(srcDir, 0o755); err != nil {
		t.Fatalf("mkdir source: %v", err)
	}

	capturePromoteDiagnostics(t, api,
		"failed to undo the promotion file moves; keeping the promotion project and its directory for manual recovery")
	api.compensateFailedPromotion("s-undofail", proj, true)

	// Nothing was deleted: the stranded files survive and the project row
	// stays for manual recovery.
	content, err := os.ReadFile(keptFile)
	if err != nil || string(content) != "session data" {
		t.Fatalf("stranded file must survive the failed compensation: content=%q err=%v", content, err)
	}
	projects, err := projectManager.ListProjects()
	if err != nil {
		t.Fatalf("list projects: %v", err)
	}
	found := false
	for _, p := range projects {
		if p.ID == proj.ID {
			found = true
		}
	}
	if !found {
		t.Fatalf("a failed undo must keep the project row, projects=%+v", projects)
	}
}

func TestPromoteSessionToProject_KeepsStrandedTreeWithoutCompletedMoves(t *testing.T) {
	h := newPromoteTestAPI(t)
	api, projectManager, agentDir := h.api, h.projects, h.agentDir

	proj, err := projectManager.CreateProject("Half-Moved Project", "")
	if err != nil {
		t.Fatalf("create project: %v", err)
	}

	// A failed in-move rollback strands the session directory inside the
	// fresh project while the forward move "never completed" — the defensive
	// gate must keep the tree exactly as it is.
	dstSessionDir := config.SessionDir(agentDir, proj.ID, "s-stranded")
	if err := os.MkdirAll(dstSessionDir, 0o755); err != nil {
		t.Fatalf("mkdir stranded layout: %v", err)
	}
	keptFile := filepath.Join(dstSessionDir, "logs", "session.log")
	if err := os.MkdirAll(filepath.Dir(keptFile), 0o755); err != nil {
		t.Fatalf("mkdir logs: %v", err)
	}
	if err := os.WriteFile(keptFile, []byte("logs"), 0o644); err != nil {
		t.Fatalf("write stranded log: %v", err)
	}

	capturePromoteDiagnostics(t, api,
		"the promotion project directory still contains the session directory; keeping everything for manual recovery")
	api.compensateFailedPromotion("s-stranded", proj, false)

	if _, err := os.ReadFile(keptFile); err != nil {
		t.Fatalf("stranded file must survive: %v", err)
	}
	projects, err := projectManager.ListProjects()
	if err != nil {
		t.Fatalf("list projects: %v", err)
	}
	for _, p := range projects {
		if p.ID == proj.ID {
			return
		}
	}
	t.Fatalf("a stranded project tree must keep its project row, projects=%+v", projects)
}

func TestPromoteSessionToProject_CleanCompensationStillDeletesProject(t *testing.T) {
	h := newPromoteTestAPI(t)
	api, projectManager, agentDir := h.api, h.projects, h.agentDir

	proj, err := projectManager.CreateProject("Clean Compensation", "")
	if err != nil {
		t.Fatalf("create project: %v", err)
	}

	// A session that never materialized: nothing moved, no diagnostics, the
	// fresh project is removed as before.
	capturePromoteDiagnostics(t, api)
	api.compensateFailedPromotion("s-ghost", proj, false)

	projects, err := projectManager.ListProjects()
	if err != nil {
		t.Fatalf("list projects: %v", err)
	}
	for _, p := range projects {
		if p.ID == proj.ID {
			t.Fatalf("a clean compensation must delete the project, projects=%+v", projects)
		}
	}
	if _, err := os.Stat(config.ProjectDir(agentDir, proj.ID)); !os.IsNotExist(err) {
		t.Fatalf("a clean compensation must remove the project tree (stat err=%v)", err)
	}
}
