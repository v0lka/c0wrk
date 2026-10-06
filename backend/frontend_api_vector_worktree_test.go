package backend

// Session-workspace routing for the vector index (ADR-080): a managed
// session's index/search targets its OWN worktree root — branch resolved from
// that root, storage scoped per tree — while the default local-project flow
// stays unchanged. Fixtures use real linked worktrees via internal/gittest;
// async index init is awaited with bounded poll watchdogs, never sleeps.

import (
	"context"
	"database/sql"
	"io"
	"log/slog"
	"os"
	"path/filepath"
	"testing"
	"time"

	"github.com/v0lka/c0wrk/backend/config"
	"github.com/v0lka/c0wrk/backend/project"
	"github.com/v0lka/c0wrk/backend/session"
	"github.com/v0lka/c0wrk/core"
	"github.com/v0lka/c0wrk/core/vectorindex"
	"github.com/v0lka/c0wrk/internal/gittest"
	"github.com/v0lka/sp4rk/orchestration"
	_ "modernc.org/sqlite"
)

// vectorWorktreeHarness wires a FrontendAPI against in-memory stores, a real
// (fake-embedded) vector manager, and a real git repository whose managed
// worktrees live under <repo>/.worktrees.
type vectorWorktreeHarness struct {
	api      *FrontendAPI
	db       *sql.DB
	mgr      *session.Manager
	vectors  *vectorindex.Manager
	store    *session.SQLiteSessionStore
	proj     *project.ProjectInfo
	repo     *gittest.Repo
	agentDir string
}

func newVectorWorktreeHarness(t *testing.T) *vectorWorktreeHarness {
	t.Helper()
	gittest.RequireGit(t)

	ctx := context.Background()
	db := openProjectSwitchTestDB(t)

	projectStore, err := project.NewSQLiteProjectStore(db)
	if err != nil {
		_ = db.Close()
		t.Fatalf("project store: %v", err)
	}
	sessionStore, err := session.NewSQLiteSessionStore(db)
	if err != nil {
		_ = db.Close()
		t.Fatalf("session store: %v", err)
	}

	// Real git repo registered as the project workspace: managed worktrees
	// are genuine linked worktrees under <repo>/.worktrees/<name>.
	repoRoot := filepath.Join(t.TempDir(), "repo")
	repo := gittest.InitRepo(t, repoRoot, "hello\n")

	agentDir := t.TempDir()
	projectManager := project.NewManager(projectStore, agentDir, nil)
	created, err := projectManager.CreateProject("vector-worktree-fixture", repoRoot)
	if err != nil {
		_ = db.Close()
		t.Fatalf("register project: %v", err)
	}

	factory := session.OrchestratorFactory(func(core.Emitter, *slog.Logger, string, core.BlackboardFactory, io.Writer, *orchestration.StepDumpTracker) (*core.Orchestrator, error) {
		return core.NewOrchestrator(core.OrchestratorConfig{}, core.OrchestratorDeps{}), nil
	})
	manager := session.NewManager(factory, func(session.Event) {}, agentDir)
	manager.SetSessionStore(sessionStore)
	manager.SetProjectResolver(func(string) (string, error) { return repoRoot, nil })
	manager.SetWorkspaceEnsurer(func(context.Context, string, *session.WorkspaceBinding) (bool, error) {
		return false, nil // trees exist in the fixture; no recreation
	})

	vectors, err := vectorindex.NewManager(vectorindex.ManagerConfig{
		EmbeddingFunc: func(context.Context, string) ([]float32, error) {
			return make([]float32, 4), nil
		},
	})
	if err != nil {
		_ = db.Close()
		t.Fatalf("vector manager: %v", err)
	}

	api := &FrontendAPI{
		app:               &Application{manager: manager},
		store:             sessionStore,
		projStore:         projectStore,
		projectManager:    projectManager,
		agentDir:          agentDir,
		appCtx:            func() context.Context { return ctx },
		vectorManager:     vectors,
		activeProjectID:   created.ID,
		activeProjectPath: created.WorkspacePath,
		emitEvent:         func(string, ...any) {},
	}

	h := &vectorWorktreeHarness{
		api: api, db: db, mgr: manager, vectors: vectors,
		store: sessionStore, proj: created, repo: repo, agentDir: agentDir,
	}
	t.Cleanup(func() {
		vectors.Shutdown()
		manager.Shutdown()
		_ = db.Close()
	})
	return h
}

// addManagedWorktree provisions a genuine linked worktree at
// <repo>/.worktrees/<name> on a fresh branch and returns its root.
func (h *vectorWorktreeHarness) addManagedWorktree(t *testing.T, name, branch string) string {
	t.Helper()
	wt := filepath.Join(config.ManagedWorktreesDir(h.proj.WorkspacePath), name)
	h.repo.Git(t, "worktree", "add", "-b", branch, wt, "main")
	return wt
}

// saveManagedSession persists a managed-worktree session row and makes it the
// project's saved selection.
func (h *vectorWorktreeHarness) saveManagedSession(t *testing.T, id, name, branch, treePath string) {
	t.Helper()
	binding := &session.WorkspaceBinding{
		Kind:          session.WorkspaceManagedWorktree,
		WorkspacePath: treePath,
		WorktreeName:  name,
		Branch:        branch,
	}
	info := session.SessionInfo{ID: id, ProjectID: h.proj.ID, Name: id, WorkspaceBinding: binding}
	if err := h.store.SaveSession(context.Background(), info); err != nil {
		t.Fatalf("save managed session: %v", err)
	}
	if err := h.projStoreSaveSession(t, id); err != nil {
		t.Fatalf("save UI state: %v", err)
	}
}

// saveLocalSession persists a local (unbound) session row and makes it the
// project's saved selection.
func (h *vectorWorktreeHarness) saveLocalSession(t *testing.T, id string) {
	t.Helper()
	info := session.SessionInfo{ID: id, ProjectID: h.proj.ID, Name: id}
	if err := h.store.SaveSession(context.Background(), info); err != nil {
		t.Fatalf("save local session: %v", err)
	}
	if err := h.projStoreSaveSession(t, id); err != nil {
		t.Fatalf("save UI state: %v", err)
	}
}

// projStoreSaveSession records the session as the project's saved selection.
func (h *vectorWorktreeHarness) projStoreSaveSession(t *testing.T, sessionID string) error {
	t.Helper()
	return h.api.projStore.SaveUIState(context.Background(), project.ProjectUIState{
		ProjectID:      h.proj.ID,
		SavedSessionID: sessionID,
	})
}

// waitForVectorBranch awaits the async init via the service's readiness
// barrier (bounded by a watchdog context — no sleeps), then asserts the live
// branch identity: SwitchProject returns before initProject detects the
// branch, so readiness is the lifecycle signal that the branch is settled.
func (h *vectorWorktreeHarness) waitForVectorBranch(t *testing.T, want string) {
	t.Helper()
	ctx, cancel := context.WithTimeout(context.Background(), 20*time.Second)
	defer cancel()
	if err := h.vectors.Service().WaitReady(ctx); err != nil {
		t.Fatalf("vector index never became ready (watchdog): %v (status: %+v)", err, h.vectors.GetIndexStatus())
	}
	if got := h.vectors.GetIndexStatus().Branch; got != want {
		t.Fatalf("vector index branch = %q, want %q", got, want)
	}
}

// TestResolveVectorIndexTarget_ManagedSessionTargetsOwnTree pins target
// resolution: the project's saved managed session routes the index to its own
// tree root with a worktree-scoped storage dir; no session and local sessions
// keep the checkout + project storage — the unchanged default flow.
func TestResolveVectorIndexTarget_ManagedSessionTargetsOwnTree(t *testing.T) {
	h := newVectorWorktreeHarness(t)
	p := h.proj

	// No saved session: checkout defaults.
	got := h.api.resolveVectorIndexTarget(p)
	if got.workspacePath != p.WorkspacePath || got.storagePath != config.ProjectVectorIndexPath(h.agentDir, p.ID) {
		t.Fatalf("default target = %+v; want checkout + project storage", got)
	}

	tree := h.addManagedWorktree(t, "s-1", "topic")
	h.saveManagedSession(t, "sess-managed", "s-1", "topic", tree)

	got = h.api.resolveVectorIndexTarget(p)
	wantStorage, err := config.WorktreeVectorIndexPath(h.agentDir, p.ID, "s-1")
	if err != nil {
		t.Fatalf("worktree vector path: %v", err)
	}
	if got.workspacePath != tree {
		t.Errorf("workspace = %q, want the managed tree %q", got.workspacePath, tree)
	}
	if got.storagePath != wantStorage {
		t.Errorf("storage = %q, want %q", got.storagePath, wantStorage)
	}

	// A local saved session keeps the defaults.
	h.saveLocalSession(t, "sess-local")
	got = h.api.resolveVectorIndexTarget(p)
	if got.workspacePath != p.WorkspacePath {
		t.Errorf("local-session target workspace = %q, want checkout", got.workspacePath)
	}
	if got.storagePath != config.ProjectVectorIndexPath(h.agentDir, p.ID) {
		t.Errorf("local-session target storage = %q, want project storage", got.storagePath)
	}
}

// TestMaybeReScopeVectorIndexToSession_WorktreeIndexIsRoutedPerSession is the
// AC test for index/search routing: activating (sending to) a managed session
// re-scopes the vector index to that session's tree — the branch is resolved
// from the tree root — and a second managed session on another tree gets its
// own storage root, so the two trees never share or clobber index state.
func TestMaybeReScopeVectorIndexToSession_WorktreeIndexIsRoutedPerSession(t *testing.T) {
	h := newVectorWorktreeHarness(t)

	treeA := h.addManagedWorktree(t, "s-alpha", "feature-alpha")
	treeB := h.addManagedWorktree(t, "s-beta", "feature-beta")
	h.saveManagedSession(t, "sess-alpha", "s-alpha", "feature-alpha", treeA)
	h.saveManagedSession(t, "sess-beta", "s-beta", "feature-beta", treeB)

	// Activate the first managed session: the index re-scopes to ITS tree.
	h.api.maybeReScopeVectorIndexToSession("sess-alpha")
	h.waitForVectorBranch(t, "feature-alpha")

	storageA, err := config.WorktreeVectorIndexPath(h.agentDir, h.proj.ID, "s-alpha")
	if err != nil {
		t.Fatal(err)
	}
	if fi, statErr := os.Stat(filepath.Join(storageA, "branches")); statErr != nil || !fi.IsDir() {
		t.Errorf("tree A storage layout missing under %s (err=%v)", storageA, statErr)
	}

	// Activate the second managed session on the other tree: re-scope again,
	// branch resolved from THAT root, storage disjoint from A's.
	h.api.maybeReScopeVectorIndexToSession("sess-beta")
	h.waitForVectorBranch(t, "feature-beta")

	storageB, err := config.WorktreeVectorIndexPath(h.agentDir, h.proj.ID, "s-beta")
	if err != nil {
		t.Fatal(err)
	}
	if _, statErr := os.Stat(storageB); statErr != nil {
		t.Errorf("tree B storage missing: %v", statErr)
	}
	if storageA == storageB {
		t.Fatalf("storage roots collide: %q", storageA)
	}
	// A's persisted state survives B's activation (no clobber).
	if _, statErr := os.Stat(storageA); statErr != nil {
		t.Errorf("tree A storage removed by tree B activation: %v", statErr)
	}

	// Re-activating A round-trips back to its branch.
	h.api.maybeReScopeVectorIndexToSession("sess-alpha")
	h.waitForVectorBranch(t, "feature-alpha")
}

// TestMaybeReScopeVectorIndexToSession_NoOps pins the guards: a local session
// (workspace == checkout) and a session of another project never re-scope.
func TestMaybeReScopeVectorIndexToSession_NoOps(t *testing.T) {
	h := newVectorWorktreeHarness(t)

	h.saveLocalSession(t, "sess-local")
	h.api.maybeReScopeVectorIndexToSession("sess-local")
	if h.api.vectorTargetWorkspace != "" && h.api.vectorTargetWorkspace != h.proj.WorkspacePath {
		t.Errorf("local session re-scoped the vector target to %q", h.api.vectorTargetWorkspace)
	}

	// Foreign-project managed session: workspace outside this project's
	// container must be ignored.
	foreignRoot := filepath.Join(t.TempDir(), "foreign")
	frepo := gittest.InitRepo(t, foreignRoot, "x\n")
	fproj, err := h.api.projectManager.CreateProject("foreign", foreignRoot)
	if err != nil {
		t.Fatalf("foreign project: %v", err)
	}
	fwt := filepath.Join(config.ManagedWorktreesDir(foreignRoot), "s-x")
	frepo.Git(t, "worktree", "add", "-b", "fx", fwt, "main")
	fbinding := &session.WorkspaceBinding{
		Kind: session.WorkspaceManagedWorktree, WorkspacePath: fwt, WorktreeName: "s-x", Branch: "fx",
	}
	finfo := session.SessionInfo{ID: "sess-foreign", ProjectID: fproj.ID, Name: "foreign", WorkspaceBinding: fbinding}
	if err := h.store.SaveSession(context.Background(), finfo); err != nil {
		t.Fatal(err)
	}
	h.api.maybeReScopeVectorIndexToSession("sess-foreign")
	if h.api.vectorTargetWorkspace == fwt {
		t.Error("foreign-project session re-scoped the vector index to its tree")
	}
}

// TestDeleteProject_RemovesWorktreeVectorIndexes pins project-deletion
// cleanup: every managed tree's worktree-scoped index storage under
// <projectVI>/worktrees/ is removed together with the project's own index.
func TestDeleteProject_RemovesWorktreeVectorIndexes(t *testing.T) {
	h := newVectorWorktreeHarness(t)

	viRoot := config.ProjectVectorIndexPath(h.agentDir, h.proj.ID)
	trees := []string{"s-one", "s-two"}
	for _, name := range trees {
		storage, err := config.WorktreeVectorIndexPath(h.agentDir, h.proj.ID, name)
		if err != nil {
			t.Fatal(err)
		}
		if err := os.MkdirAll(filepath.Join(storage, "branches"), 0o755); err != nil {
			t.Fatal(err)
		}
	}
	if err := os.MkdirAll(filepath.Join(viRoot, "branches"), 0o755); err != nil {
		t.Fatal(err)
	}

	if err := h.api.DeleteProject(h.proj.ID); err != nil {
		t.Fatalf("DeleteProject: %v", err)
	}

	if _, statErr := os.Stat(viRoot); !os.IsNotExist(statErr) {
		t.Errorf("project vector dir still present after project delete (err=%v)", statErr)
	}
	for _, name := range trees {
		storage, err := config.WorktreeVectorIndexPath(h.agentDir, h.proj.ID, name)
		if err != nil {
			t.Fatal(err)
		}
		if _, statErr := os.Stat(storage); !os.IsNotExist(statErr) {
			t.Errorf("worktree %s vector storage still present after project delete (err=%v)", name, statErr)
		}
	}
}

// TestDeleteWorktreeVectorIndex_RemovesReleasedTreeStorage pins release-path
// cleanup: after a session's tree is released, its worktree-scoped index
// storage is dropped from disk.
func TestDeleteWorktreeVectorIndex_RemovesReleasedTreeStorage(t *testing.T) {
	h := newVectorWorktreeHarness(t)

	storage, err := config.WorktreeVectorIndexPath(h.agentDir, h.proj.ID, "s-gone")
	if err != nil {
		t.Fatal(err)
	}
	if err := os.MkdirAll(filepath.Join(storage, "branches"), 0o755); err != nil {
		t.Fatal(err)
	}

	h.api.deleteWorktreeVectorIndex(h.proj.ID, "s-gone")

	if _, statErr := os.Stat(storage); !os.IsNotExist(statErr) {
		t.Errorf("released worktree storage still present (err=%v)", statErr)
	}
}
