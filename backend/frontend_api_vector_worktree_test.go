package backend

// Session-workspace routing for the vector index (ADR-080): the per-root
// registry gives every workspace root its own manager with its own persisted
// storage, so a session's semantic_search and RAG hints resolve against ITS
// tree (the root carried by the executor context) while the user-facing RPCs
// follow the Git-panel focus — regardless of which session is active.
// Fixtures use real linked worktrees via internal/gittest; async index init
// is awaited with bounded poll watchdogs, never sleeps.

import (
	"context"
	"database/sql"
	"errors"
	"io"
	"log/slog"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/v0lka/c0wrk/backend/config"
	"github.com/v0lka/c0wrk/backend/project"
	"github.com/v0lka/c0wrk/backend/session"
	"github.com/v0lka/c0wrk/core"
	"github.com/v0lka/c0wrk/core/vectorindex"
	"github.com/v0lka/c0wrk/internal/gittest"
	"github.com/v0lka/sp4rk/orchestration"
	sdktools "github.com/v0lka/sp4rk/tools"
	"github.com/v0lka/sp4rk/tools/builtins"
	_ "modernc.org/sqlite"
)

// vectorWorktreeHarness wires a FrontendAPI against in-memory stores, a
// per-root vector registry whose factory builds real (fake-embedded)
// managers, and a real git repository whose managed worktrees live under
// <repo>/.worktrees.
type vectorWorktreeHarness struct {
	api      *FrontendAPI
	db       *sql.DB
	mgr      *session.Manager
	roots    *VectorRoots
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
	manager.SetProjectResolver(func(string) (string, error) { return created.WorkspacePath, nil })
	manager.SetWorkspaceEnsurer(func(context.Context, string, *session.WorkspaceBinding) (bool, error) {
		return false, nil // trees exist in the fixture; no recreation
	})

	api := &FrontendAPI{
		app:               &Application{manager: manager},
		store:             sessionStore,
		projStore:         projectStore,
		projectManager:    projectManager,
		agentDir:          agentDir,
		appCtx:            func() context.Context { return ctx },
		activeProjectID:   created.ID,
		activeProjectPath: created.WorkspacePath,
		emitEvent:         func(string, ...any) {},
	}
	api.seedPublished.Store(true)
	api.seedPublished.Store(true)

	// The registry builds a real manager per routed root through the factory
	// (fake 4-dim embeddings, same as the pre-registry fixtures). The ready
	// channel is pre-closed: there is no background ONNX init in the test.
	ready := make(chan struct{})
	close(ready)
	roots := newVectorRoots(api)
	roots.SetFactory(func() (*vectorindex.Manager, error) {
		return vectorindex.NewManager(vectorindex.ManagerConfig{
			EmbeddingFunc: func(context.Context, string) ([]float32, error) {
				return make([]float32, 4), nil
			},
		})
	}, ready, nil)
	api.vectorRoots = roots

	h := &vectorWorktreeHarness{
		api: api, db: db, mgr: manager, roots: roots,
		store: sessionStore, proj: created, repo: repo, agentDir: agentDir,
	}
	t.Cleanup(func() {
		roots.ShutdownAll()
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

// waitForVectorBranch awaits the manager's async init via the service's
// readiness barrier (bounded by a watchdog context — no sleeps), then asserts
// the live branch identity: SwitchProject returns before initProject detects
// the branch, so readiness is the lifecycle signal that the branch is settled.
func waitForVectorBranch(t *testing.T, mgr *vectorindex.Manager, want string) {
	t.Helper()
	ctx, cancel := context.WithTimeout(context.Background(), 20*time.Second)
	defer cancel()
	if err := mgr.Service().WaitReady(ctx); err != nil {
		t.Fatalf("vector index never became ready (watchdog): %v (status: %+v)", err, mgr.GetIndexStatus())
	}
	if got := mgr.GetIndexStatus().Branch; got != want {
		t.Fatalf("vector index branch = %q, want %q", got, want)
	}
}

// TestResolveVectorIndexTarget_ManagedSessionTargetsOwnTree pins focus-target
// resolution: the project's saved managed session moves the Git-panel focus
// (and with it the user-facing index target) to its own tree root with a
// worktree-scoped storage dir; no session and local sessions keep the
// checkout + project storage — the unchanged default flow.
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

// TestManagerForContext_RoutesSessionTreePerSession is the AC test for
// agent-side search routing: the executor context carries the session's own
// workspace root (sdktools.WithWorkspacePathNoProbe, set by the session
// manager before HandleMessage), so ManagerForContext resolves THAT tree's
// manager — branch detected from the root, storage disjoint per tree — and
// concurrent sessions on different trees get simultaneously live, isolated
// managers. A repeat request returns the SAME manager instance: two managers
// must never open the same chromem persistent storage.
func TestManagerForContext_RoutesSessionTreePerSession(t *testing.T) {
	h := newVectorWorktreeHarness(t)

	treeA := h.addManagedWorktree(t, "s-alpha", "feature-alpha")
	treeB := h.addManagedWorktree(t, "s-beta", "feature-beta")
	h.saveManagedSession(t, "sess-alpha", "s-alpha", "feature-alpha", treeA)
	h.saveManagedSession(t, "sess-beta", "s-beta", "feature-beta", treeB)

	// Session A's task context: the index routes to ITS tree.
	ctxA := sdktools.WithWorkspacePathNoProbe(context.Background(), treeA)
	mgrA, err := h.roots.ManagerForContext(ctxA)
	if err != nil {
		t.Fatalf("manager for tree A: %v", err)
	}
	waitForVectorBranch(t, mgrA, "feature-alpha")

	storageA, err := config.WorktreeVectorIndexPath(h.agentDir, h.proj.ID, "s-alpha")
	if err != nil {
		t.Fatal(err)
	}
	if fi, statErr := os.Stat(filepath.Join(storageA, "branches")); statErr != nil || !fi.IsDir() {
		t.Errorf("tree A storage layout missing under %s (err=%v)", storageA, statErr)
	}

	// Session B on the other tree: its OWN manager, branch resolved from that
	// root, storage disjoint from A's. A's manager stays live — B's routing
	// must not disturb it.
	ctxB := sdktools.WithWorkspacePathNoProbe(context.Background(), treeB)
	mgrB, err := h.roots.ManagerForContext(ctxB)
	if err != nil {
		t.Fatalf("manager for tree B: %v", err)
	}
	waitForVectorBranch(t, mgrB, "feature-beta")

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
	if _, statErr := os.Stat(storageA); statErr != nil {
		t.Errorf("tree A storage removed by tree B routing: %v", statErr)
	}
	if mgrA == mgrB {
		t.Fatal("tree A and tree B resolved to the same manager instance")
	}

	// Re-routing A returns the SAME live manager (idempotent per root) and
	// its branch identity is intact — concurrent searches stay isolated.
	mgrA2, err := h.roots.ManagerForContext(ctxA)
	if err != nil {
		t.Fatalf("re-route tree A: %v", err)
	}
	if mgrA2 != mgrA {
		t.Fatal("re-routing tree A built a second manager over the same storage")
	}
	waitForVectorBranch(t, mgrA2, "feature-alpha")

	// Both managers are simultaneously live in the registry.
	if got := h.roots.liveRootsForTest(); got != 2 {
		t.Errorf("live roots = %d, want 2 (both session trees)", got)
	}
}

// TestManagerForContext_Fallbacks pins the routing guards: a local session's
// context (checkout root) resolves the checkout manager with the project
// storage, and an unknown root (neither a registered checkout nor a managed
// worktree) is rejected instead of silently indexed.
func TestManagerForContext_Fallbacks(t *testing.T) {
	h := newVectorWorktreeHarness(t)

	// Local session: workspace == checkout → project storage.
	ctxLocal := sdktools.WithWorkspacePathNoProbe(context.Background(), h.proj.WorkspacePath)
	mgrLocal, err := h.roots.ManagerForContext(ctxLocal)
	if err != nil {
		t.Fatalf("manager for checkout: %v", err)
	}
	// WaitReady first: SwitchProject returns before the async init
	// materializes the branch layout, so the storage assertion must follow
	// readiness.
	waitForVectorBranch(t, mgrLocal, "main")
	checkoutStorage := config.ProjectVectorIndexPath(h.agentDir, h.proj.ID)
	if fi, statErr := os.Stat(filepath.Join(checkoutStorage, "branches")); statErr != nil || !fi.IsDir() {
		t.Errorf("checkout storage layout missing under %s (err=%v)", checkoutStorage, statErr)
	}

	// Unknown root: rejected, nothing registered.
	foreign := filepath.Join(t.TempDir(), "foreign")
	if err := os.MkdirAll(foreign, 0o755); err != nil {
		t.Fatal(err)
	}
	ctxForeign := sdktools.WithWorkspacePathNoProbe(context.Background(), foreign)
	if _, err := h.roots.ManagerForContext(ctxForeign); !errors.Is(err, errVectorNoTarget) {
		t.Errorf("foreign root error = %v, want errVectorNoTarget", err)
	}
	if got := h.roots.liveRootsForTest(); got != 1 {
		t.Errorf("live roots = %d, want 1 (checkout only)", got)
	}
}

// TestApplyFocus_StatusFollowsGitPanelFocus is the user-side AC: the
// user-facing RPCs (status here; SearchVectorStore/Reindex resolve the same
// FocusManager) follow the Git-panel focus — moving the focus to another
// tree switches the visible index identity WITHOUT touching the outgoing
// tree's manager (a background session there keeps its own live manager).
func TestApplyFocus_StatusFollowsGitPanelFocus(t *testing.T) {
	h := newVectorWorktreeHarness(t)

	treeA := h.addManagedWorktree(t, "s-focus-a", "focus-alpha")
	treeB := h.addManagedWorktree(t, "s-focus-b", "focus-beta")
	h.saveManagedSession(t, "sess-focus-a", "s-focus-a", "focus-alpha", treeA)
	h.saveManagedSession(t, "sess-focus-b", "s-focus-b", "focus-beta", treeB)

	mgrA, err := h.roots.ManagerForContext(sdktools.WithWorkspacePathNoProbe(context.Background(), treeA))
	if err != nil {
		t.Fatalf("manager for tree A: %v", err)
	}
	mgrB, err := h.roots.ManagerForContext(sdktools.WithWorkspacePathNoProbe(context.Background(), treeB))
	if err != nil {
		t.Fatalf("manager for tree B: %v", err)
	}
	waitForVectorBranch(t, mgrA, "focus-alpha")
	waitForVectorBranch(t, mgrB, "focus-beta")

	// Focus B: the status RPC reports B's branch...
	if err := h.roots.ApplyFocus(canonicalRoot(treeB)); err != nil {
		t.Fatalf("focus tree B: %v", err)
	}
	if got := h.api.GetVectorIndexStatus().Branch; got != "focus-beta" {
		t.Errorf("status branch after focusing B = %q, want focus-beta", got)
	}
	// ...while A's manager stays live and untouched for session A.
	waitForVectorBranch(t, mgrA, "focus-alpha")

	// Focus back to A: the visible identity flips, B's manager survives.
	if err := h.roots.ApplyFocus(canonicalRoot(treeA)); err != nil {
		t.Fatalf("focus tree A: %v", err)
	}
	if got := h.api.GetVectorIndexStatus().Branch; got != "focus-alpha" {
		t.Errorf("status branch after refocusing A = %q, want focus-alpha", got)
	}
	waitForVectorBranch(t, mgrB, "focus-beta")
}

// TestRouter_SearchFuncResolvesSessionContext pins the closure-level routing:
// the router built by the Application resolves the manager from the executor
// context's workspace root, so two sessions on different trees querying
// through the SAME registered closure get results from their own trees.
func TestRouter_SearchFuncResolvesSessionContext(t *testing.T) {
	h := newVectorWorktreeHarness(t)

	treeA := h.addManagedWorktree(t, "s-rt-a", "router-alpha")
	treeB := h.addManagedWorktree(t, "s-rt-b", "router-beta")
	// Distinct marker content per tree, written BEFORE the first manager is
	// built for the root, so the initial index pass ingests it: the search
	// isolation below is then provable by CONTENT, not just by call success.
	markerA := filepath.Join(treeA, "alpha-marker.txt")
	if err := os.WriteFile(markerA, []byte("unique token zzz-alpha-marker"), 0o644); err != nil {
		t.Fatal(err)
	}
	markerB := filepath.Join(treeB, "beta-marker.txt")
	if err := os.WriteFile(markerB, []byte("unique token zzz-beta-marker"), 0o644); err != nil {
		t.Fatal(err)
	}

	app := h.api.app
	searchFunc, waitFunc := app.buildVectorRouter(0) // explicit fail-fast sentinel
	app.SetVectorRoots(h.roots)

	// Both managers live before querying (the fail-fast sentinel refuses to
	// build anything through the still-loading/unknown path).
	mgrA, err := h.roots.ManagerForContext(sdktools.WithWorkspacePathNoProbe(context.Background(), treeA))
	if err != nil {
		t.Fatalf("manager for tree A: %v", err)
	}
	mgrB, err := h.roots.ManagerForContext(sdktools.WithWorkspacePathNoProbe(context.Background(), treeB))
	if err != nil {
		t.Fatalf("manager for tree B: %v", err)
	}
	waitForVectorBranch(t, mgrA, "router-alpha")
	waitForVectorBranch(t, mgrB, "router-beta")

	// Session A's closure resolves A's manager: the marker token from A's
	// tree is found, B's is not.
	ctxA := sdktools.WithWorkspacePathNoProbe(context.Background(), treeA)
	if err := waitFunc(ctxA); err != nil {
		t.Fatalf("waitFunc for tree A: %v", err)
	}
	resA, err := searchFunc(ctxA, builtins.VectorSearchOptions{Query: "zzz-alpha-marker", TopK: 5})
	if err != nil {
		t.Fatalf("searchFunc for tree A: %v", err)
	}
	if len(resA) == 0 {
		t.Fatal("tree A search found none of its own marker")
	}
	foundOwn := false
	for _, r := range resA {
		if strings.Contains(r.FilePath, "alpha-marker") {
			foundOwn = true
		}
		if strings.Contains(r.FilePath, "beta-marker") {
			t.Errorf("tree A search leaked tree B content: %s", r.FilePath)
		}
	}
	if !foundOwn {
		t.Errorf("tree A results missing alpha-marker: %+v", resA)
	}

	// Session B's closure resolves B's manager: symmetric isolation.
	ctxB := sdktools.WithWorkspacePathNoProbe(context.Background(), treeB)
	if err := waitFunc(ctxB); err != nil {
		t.Fatalf("waitFunc for tree B: %v", err)
	}
	resB, err := searchFunc(ctxB, builtins.VectorSearchOptions{Query: "zzz-beta-marker", TopK: 5})
	if err != nil {
		t.Fatalf("searchFunc for tree B: %v", err)
	}
	if len(resB) == 0 {
		t.Fatal("tree B search found none of its own marker")
	}
	for _, r := range resB {
		if strings.Contains(r.FilePath, "alpha-marker") {
			t.Errorf("tree B search leaked tree A content: %s", r.FilePath)
		}
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

	h.api.deleteWorktreeVectorIndex(h.proj.WorkspacePath, h.proj.ID, "s-gone")

	if _, statErr := os.Stat(storage); !os.IsNotExist(statErr) {
		t.Errorf("released worktree storage still present (err=%v)", statErr)
	}
}
