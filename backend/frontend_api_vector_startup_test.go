package backend

import (
	"context"
	"os"
	"path/filepath"
	"sync"
	"testing"
	"time"

	"github.com/v0lka/c0wrk/backend/project"
	"github.com/v0lka/c0wrk/core/vectorindex"
)

// newStartupVectorAPI builds a FrontendAPI whose vector registry has NO
// factory wired yet (the background ONNX init has not finished), mirroring
// the startup race: the frontend's first SwitchProject arrives before
// SetVectorRootsFactory. A project store + manager are wired so
// resolveRootPlan can resolve registered checkouts once the factory lands.
func newStartupVectorAPI(t *testing.T) *FrontendAPI {
	t.Helper()

	db := openProjectSwitchTestDB(t)
	projectStore, err := project.NewSQLiteProjectStore(db)
	if err != nil {
		_ = db.Close()
		t.Fatalf("project store: %v", err)
	}
	agentDir := t.TempDir()
	t.Cleanup(func() { _ = db.Close() })

	f := &FrontendAPI{
		appCtx:         context.Background,
		agentDir:       agentDir,
		projStore:      projectStore,
		projectManager: project.NewManager(projectStore, agentDir, nil),
		emitEvent:      func(string, ...any) {},
	}
	return f
}

// wireFactory simulates the background ONNX goroutine finishing its
// construction and handing the registry a manager factory. Every manager the
// factory builds is tracked and shut down with the registry on cleanup.
func wireFactory(t *testing.T, f *FrontendAPI) {
	t.Helper()
	var mu sync.Mutex
	var created []*vectorindex.Manager
	ready := make(chan struct{})
	close(ready)
	f.vectorRootsRegistry().SetFactory(func() (*vectorindex.Manager, error) {
		mgr, err := vectorindex.NewManager(vectorindex.ManagerConfig{
			EmbeddingFunc: func(ctx context.Context, text string) ([]float32, error) {
				return make([]float32, 4), nil
			},
		})
		if err != nil {
			return nil, err
		}
		mu.Lock()
		created = append(created, mgr)
		mu.Unlock()
		return mgr, nil
	}, ready, func() error { return nil })
	t.Cleanup(func() {
		f.vectorRootsRegistry().ShutdownAll()
	})
}

// setActiveProject simulates SwitchProject committing the activation.
func setActiveProject(f *FrontendAPI, id string) {
	f.activeProjectMu.Lock()
	f.activeProjectID = id
	f.activeProjectMu.Unlock()
}

// waitForVectorReady blocks until the manager's per-project init has fully
// settled (branch detected, collection opened, indexing done) or fails after a
// deadline. It observes readiness through the lock-protected accessors —
// Service.GetCollection is intentionally lock-free for the Indexer and must not
// be polled concurrently with init.
func waitForVectorReady(t *testing.T, mgr *vectorindex.Manager) {
	t.Helper()
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	if err := mgr.Service().WaitReady(ctx); err != nil {
		t.Fatalf("vector service never became ready: %v", err)
	}
	if branch := mgr.GetIndexStatus().Branch; branch == "" {
		t.Fatal("expected the manager to have detected a branch for the active project")
	}
}

// seedDeferred records a deferred setup by driving switchProjectSetupVector
// while the factory is still unwired — reproducing the startup race in which
// the frontend's first SwitchProject arrives before the background ONNX init.
func seedDeferred(t *testing.T, f *FrontendAPI, p *project.ProjectInfo) {
	t.Helper()
	if f.vectorRootsRegistry().FactoryReady() {
		t.Fatal("precondition: factory must be unwired to reproduce the startup race")
	}
	if err := f.switchProjectSetupVector(p); err != nil {
		t.Fatalf("switchProjectSetupVector (deferral): %v", err)
	}
	if f.deferredVectorProject != p {
		t.Fatalf("expected setup to be deferred, got %+v", f.deferredVectorProject)
	}
}

// TestSwitchProjectSetupVector_DefersWhenFactoryUnavailable pins the core of
// the startup bug: when the background vector factory is not yet ready,
// switchProjectSetupVector must NOT silently drop the setup — it records the
// project so InitVectorIndexForActiveProject can apply it later.
func TestSwitchProjectSetupVector_DefersWhenFactoryUnavailable(t *testing.T) {
	f := newStartupVectorAPI(t)
	p := &project.ProjectInfo{ID: "proj-a", WorkspacePath: t.TempDir()}

	seedDeferred(t, f, p)
}

// TestSwitchProjectSetupVector_NoProjectDropsDeferred pins that a No Project
// (CHAT mode) switch clears any pending setup: CHAT mode never indexes.
func TestSwitchProjectSetupVector_NoProjectDropsDeferred(t *testing.T) {
	f := newStartupVectorAPI(t)
	f.deferredVectorProject = &project.ProjectInfo{ID: "proj-a"}

	np := &project.ProjectInfo{ID: project.NoProjectID, IsNoProject: true}
	if err := f.switchProjectSetupVector(np); err != nil {
		t.Fatalf("switchProjectSetupVector(No Project): %v", err)
	}
	if f.deferredVectorProject != nil {
		t.Fatalf("No Project switch must drop the deferred setup, got %+v", f.deferredVectorProject)
	}
}

// TestSwitchProjectSetupVector_FactoryReadyClearsDeferred pins the supersede
// path: once the factory is wired, a switch clears any stale deferred setup
// (so it is applied exactly once) and runs the real setup — the focus root's
// manager is created and initialized.
func TestSwitchProjectSetupVector_FactoryReadyClearsDeferred(t *testing.T) {
	// Workspace root before newStartupVectorAPI: the helper registers
	// registry shutdown on t.Cleanup, so LIFO ordering must let Shutdown
	// release the vector-store handles before this TempDir's RemoveAll runs
	// — an open chromem handle fails the unlink on Windows.
	ws := t.TempDir()

	f := newStartupVectorAPI(t)
	if _, err := f.projectManager.CreateProject("proj-a", ws); err != nil {
		t.Fatalf("register project: %v", err)
	}
	wireFactory(t, f)

	f.deferredVectorProject = &project.ProjectInfo{ID: "proj-a"}

	p := &project.ProjectInfo{ID: "proj-a", WorkspacePath: ws}
	if err := f.switchProjectSetupVector(p); err != nil {
		t.Fatalf("switchProjectSetupVector (ready factory): %v", err)
	}
	if f.deferredVectorProject != nil {
		t.Fatalf("a ready factory must clear the deferred setup, got %+v", f.deferredVectorProject)
	}

	mgr, err := f.vectorRootsRegistry().FocusManager()
	if err != nil {
		t.Fatalf("focus manager: %v", err)
	}
	waitForVectorReady(t, mgr)
}

// TestInitVectorIndexForActiveProject_AppliesDeferredSetup pins the fix: a
// setup deferred while the factory was unwired is applied once the factory is
// wired in and the project is the active one — without a manual project switch.
func TestInitVectorIndexForActiveProject_AppliesDeferredSetup(t *testing.T) {
	// Workspace root before newStartupVectorAPI (see the LIFO note above).
	ws := t.TempDir()
	if err := os.WriteFile(filepath.Join(ws, "a.go"), []byte("package a\n"), 0o644); err != nil {
		t.Fatalf("write file: %v", err)
	}

	f := newStartupVectorAPI(t)
	if _, err := f.projectManager.CreateProject("proj-a", ws); err != nil {
		t.Fatalf("register project: %v", err)
	}

	p := &project.ProjectInfo{ID: "proj-a", WorkspacePath: ws}

	// Startup race: the switch runs while the factory is still being built.
	seedDeferred(t, f, p)
	if got := f.vectorRootsRegistry().liveRootsForTest(); got != 0 {
		t.Fatalf("no manager may exist before the deferred setup runs, got %d live roots", got)
	}

	// Background init finishes: wire the factory, then mark the project
	// active (SwitchProject commits activation after the vector step).
	wireFactory(t, f)
	setActiveProject(f, p.ID)

	f.Lifecycle().InitVectorIndexForActiveProject()

	if f.deferredVectorProject != nil {
		t.Fatal("the drain must clear the deferred setup")
	}
	mgr, err := f.vectorRootsRegistry().FocusManager()
	if err != nil {
		t.Fatalf("focus manager: %v", err)
	}
	waitForVectorReady(t, mgr)
}

// TestInitVectorIndexForActiveProject_NoOpWhenNoProjectActive pins that the
// drain is a no-op while no project is active: the frontend simply has not
// switched yet, and its later switch (now seeing a wired factory) initializes
// the index itself.
func TestInitVectorIndexForActiveProject_NoOpWhenNoProjectActive(t *testing.T) {
	f := newStartupVectorAPI(t)
	wireFactory(t, f)

	f.Lifecycle().InitVectorIndexForActiveProject()

	if got := f.vectorRootsRegistry().liveRootsForTest(); got != 0 {
		t.Fatalf("no active project: the drain must not create any manager, got %d live roots", got)
	}
}

// TestInitVectorIndexForActiveProject_SkipsStaleDeferred pins the guard: a
// deferred setup for a project that is no longer active must not be applied
// (a superseding switch owns the focus). The drain is synchronous, so the
// no-application assertion needs no timing grace.
func TestInitVectorIndexForActiveProject_SkipsStaleDeferred(t *testing.T) {
	f := newStartupVectorAPI(t)
	wireFactory(t, f)

	f.deferredVectorProject = &project.ProjectInfo{ID: "proj-a", WorkspacePath: t.TempDir()}
	setActiveProject(f, "proj-b")

	f.Lifecycle().InitVectorIndexForActiveProject()

	if f.deferredVectorProject != nil {
		t.Fatal("the drain must clear the deferred slot even when it skips applying it")
	}
	if got := f.vectorRootsRegistry().liveRootsForTest(); got != 0 {
		t.Fatalf("a deferred setup for a non-active project must not create any manager, got %d live roots", got)
	}
}
