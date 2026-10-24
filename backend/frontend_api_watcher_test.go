package backend

import (
	"context"
	"os"
	"path/filepath"
	"sync/atomic"
	"testing"
	"time"

	"github.com/v0lka/c0wrk/backend/config"
	"github.com/v0lka/c0wrk/backend/project"
	"github.com/v0lka/c0wrk/core/workspace"
)

// markNoProjectActive sets the active project to No Project on a FrontendAPI,
// mirroring the state switchProjectActivate leaves behind: both the project ID
// and the project path (the No Project project dir — the containment root the
// WatchDirectory gate validates renderer-supplied paths against).
func markNoProjectActive(f *FrontendAPI) {
	f.activeProjectMu.Lock()
	f.activeProjectID = project.NoProjectID
	f.activeProjectPath = config.ProjectDir(f.agentDir, project.NoProjectID)
	f.activeProjectMu.Unlock()
}

// waitForEmission polls counter until it reaches target or timeout elapses.
// fsnotify/FSEvents on macOS has latency, so polling is more robust than a
// fixed sleep.
func waitForEmission(counter *atomic.Int32, target int32, timeout time.Duration) bool {
	deadline := time.Now().Add(timeout)
	for time.Now().Before(deadline) {
		if counter.Load() >= target {
			return true
		}
		time.Sleep(25 * time.Millisecond)
	}
	return counter.Load() >= target
}

// writeUntilEmitted writes path and waits for the watcher's emission; if the
// event does not arrive within a bounded window it rewrites and re-waits
// until the overall deadline. That covers watch-registration latency on
// every fsnotify backend (the write is only meaningful once the watch is
// live) with an event-driven bounded retry instead of sleep debt.
func writeUntilEmitted(t *testing.T, path string, data []byte, events *atomic.Int32, target int32) {
	t.Helper()
	deadline := time.Now().Add(5 * time.Second)
	for {
		if err := os.WriteFile(path, data, 0o644); err != nil {
			t.Fatalf("write %s: %v", path, err)
		}
		if waitForEmission(events, target, 750*time.Millisecond) {
			return
		}
		if time.Now().After(deadline) {
			t.Fatalf("watcher event for %s not emitted", path)
		}
	}
}

// TestWatchDirectory_NoProject_ReScopesAcrossSessions verifies the core fix:
// switching chat sessions re-scopes the watcher to the new session's workspace,
// even though that workspace is outside the previous session's watcher root.
// Previously WatchDirectory rejected the new path with "path is outside
// workspace root" and file watching silently broke for every session after the
// first.
func TestWatchDirectory_NoProject_ReScopesAcrossSessions(t *testing.T) {
	base := t.TempDir()
	// Two isolated session workspaces, siblings (neither contains the other),
	// under the production No Project project dir.
	noProjectDir := config.ProjectDir(base, project.NoProjectID)
	wsA := filepath.Join(noProjectDir, "sess-a", "workspace")
	wsB := filepath.Join(noProjectDir, "sess-b", "workspace")

	var emitted atomic.Int32
	f := &FrontendAPI{
		agentDir:  base,
		emitEvent: func(string, ...any) { emitted.Add(1) },
	}
	f.seedPublished.Store(true)
	f.seedPublished.Store(true)
	markNoProjectActive(f)
	t.Cleanup(func() {
		if f.watcher != nil {
			_ = f.watcher.Close()
		}
	})

	// Activate session A — watcher is created scoped to A's workspace.
	if err := f.WatchDirectory(wsA); err != nil {
		t.Fatalf("WatchDirectory A: %v", err)
	}
	if f.watcher == nil {
		t.Fatal("expected watcher to be created for session A")
	}

	// A file change in A's workspace must emit workspace:tree_changed.
	if err := os.WriteFile(filepath.Join(wsA, "a.txt"), []byte("a"), 0o644); err != nil {
		t.Fatalf("WriteFile A: %v", err)
	}
	if !waitForEmission(&emitted, 1, 2*time.Second) {
		t.Fatal("expected workspace:tree_changed emission after file change in session A")
	}

	// Switch to session B — watcher must re-scope to B's workspace, even
	// though B is outside A's root (the previous bug).
	emitted.Store(0)
	if err := f.WatchDirectory(wsB); err != nil {
		t.Fatalf("WatchDirectory B: %v", err)
	}
	if f.watcher == nil {
		t.Fatal("expected watcher to be re-scoped to session B")
	}

	// A file change in B's workspace must emit workspace:tree_changed.
	if err := os.WriteFile(filepath.Join(wsB, "b.txt"), []byte("b"), 0o644); err != nil {
		t.Fatalf("WriteFile B: %v", err)
	}
	if !waitForEmission(&emitted, 1, 2*time.Second) {
		t.Fatal("expected workspace:tree_changed emission after file change in session B")
	}
}

// TestWatchDirectory_NoProject_CreatesMissingWorkspace ensures the watcher can
// be set up before the session workspace exists on disk (fresh start), which
// previously caused NewWatcher to fail and left f.watcher permanently nil.
func TestWatchDirectory_NoProject_CreatesMissingWorkspace(t *testing.T) {
	base := t.TempDir()
	ws := filepath.Join(config.ProjectDir(base, project.NoProjectID), "sess-new", "workspace") // does not exist yet

	f := &FrontendAPI{
		agentDir:  base,
		emitEvent: func(string, ...any) {},
	}
	f.seedPublished.Store(true)
	f.seedPublished.Store(true)
	markNoProjectActive(f)
	t.Cleanup(func() {
		if f.watcher != nil {
			_ = f.watcher.Close()
		}
	})

	if err := f.WatchDirectory(ws); err != nil {
		t.Fatalf("WatchDirectory on missing dir: %v", err)
	}
	if f.watcher == nil {
		t.Fatal("expected watcher to be created even when workspace dir was missing")
	}
	if info, err := os.Stat(ws); err != nil || !info.IsDir() {
		t.Errorf("expected session workspace to be created, got err=%v", err)
	}
}

// TestUnwatchDirectory_NoProject_IsNoOp verifies that UnwatchDirectory does not
// tear down the watcher in No Project mode. The watcher is re-scoped by
// WatchDirectory on session switch; unwatching mid-session would otherwise
// remove the active session's workspace root and break change detection.
func TestUnwatchDirectory_NoProject_IsNoOp(t *testing.T) {
	base := t.TempDir()
	ws := filepath.Join(config.ProjectDir(base, project.NoProjectID), "sess-a", "workspace")

	f := &FrontendAPI{
		agentDir:  base,
		emitEvent: func(string, ...any) {},
	}
	f.seedPublished.Store(true)
	f.seedPublished.Store(true)
	markNoProjectActive(f)
	t.Cleanup(func() {
		if f.watcher != nil {
			_ = f.watcher.Close()
		}
	})

	if err := f.WatchDirectory(ws); err != nil {
		t.Fatalf("WatchDirectory: %v", err)
	}
	if f.watcher == nil {
		t.Fatal("expected watcher to be created")
	}

	if err := f.UnwatchDirectory(ws); err != nil {
		t.Fatalf("UnwatchDirectory: %v", err)
	}
	if f.watcher == nil {
		t.Error("UnwatchDirectory should not tear down the watcher in No Project mode")
	}
}

// TestUnwatchDirectory_CodeMode_IsNoOp verifies that UnwatchDirectory does not
// remove the workspace root from the watcher in CODE mode. The frontend's
// FileTreePanel calls UnwatchDirectory from its effect cleanup, which runs on
// unmount — and FileTreePanel unmounts when the user switches the sidebar tab
// (Explorer → Git/Search), collapses the sidebar, or during React StrictMode's
// mount/unmount/remount cycle. Removing the root there would tear down the
// backend-managed watcher and break file-change detection, which is exactly the
// regression that stopped the file tree from auto-updating in CODE mode.
func TestUnwatchDirectory_CodeMode_IsNoOp(t *testing.T) {
	base := t.TempDir()
	ws := filepath.Join(base, "project", "workspace")
	if err := os.MkdirAll(ws, 0o755); err != nil {
		t.Fatalf("MkdirAll: %v", err)
	}

	var emitted atomic.Int32
	f := &FrontendAPI{
		agentDir:  base,
		emitEvent: func(string, ...any) { emitted.Add(1) },
	}
	f.seedPublished.Store(true)
	f.seedPublished.Store(true)
	// CODE mode: the active project is a real (non-No-Project) project.
	f.activeProjectMu.Lock()
	f.activeProjectID = "real-project"
	f.activeProjectPath = ws
	f.activeProjectMu.Unlock()
	t.Cleanup(func() {
		if f.watcher != nil {
			_ = f.watcher.Close()
		}
	})

	// Create the watcher scoped to the project workspace, mirroring what
	// switchProjectSetupWatcher does in CODE mode (without the vector-manager
	// callback to keep the test isolated).
	watcher, err := workspace.NewWatcher(ws, func(_ []string) { f.emitEvent(EventWorkspaceTreeChanged, nil) })
	if err != nil {
		t.Fatalf("NewWatcher: %v", err)
	}
	f.watcherMu.Lock()
	f.watcher = watcher
	f.watcherMu.Unlock()

	// Sanity check: a file change emits workspace:tree_changed before unwatch.
	if err := os.WriteFile(filepath.Join(ws, "a.txt"), []byte("a"), 0o644); err != nil {
		t.Fatalf("WriteFile a: %v", err)
	}
	if !waitForEmission(&emitted, 1, 2*time.Second) {
		t.Fatal("expected workspace:tree_changed emission before unwatch")
	}

	// UnwatchDirectory must NOT remove the root in CODE mode.
	emitted.Store(0)
	if err := f.UnwatchDirectory(ws); err != nil {
		t.Fatalf("UnwatchDirectory: %v", err)
	}

	// A file change must still emit after unwatch — this is the regression
	// test: previously UnwatchDirectory removed the root and the tree stopped
	// updating.
	if err := os.WriteFile(filepath.Join(ws, "b.txt"), []byte("b"), 0o644); err != nil {
		t.Fatalf("WriteFile b: %v", err)
	}
	if !waitForEmission(&emitted, 1, 2*time.Second) {
		t.Fatal("expected workspace:tree_changed emission after unwatch; UnwatchDirectory must not remove the root in CODE mode")
	}
}

// TestSwitchProjectSetupWatcher_NoProject_NoSession_DeferCreation verifies that
// on a fresh start (no sessions), the watcher is not created at project-switch
// time and no error is raised. Previously it tried to watch the non-existent
// project directory, failed, and left f.watcher nil with a warning — and
// because nothing retried, file watching never worked in chat mode.
func TestSwitchProjectSetupWatcher_NoProject_NoSession_DeferCreation(t *testing.T) {
	f := &FrontendAPI{
		agentDir:  t.TempDir(),
		emitEvent: func(string, ...any) {},
		// app is nil => resolveNoProjectSessionWorkspace returns "" (no sessions).
	}
	f.seedPublished.Store(true)
	f.seedPublished.Store(true)
	p := &project.ProjectInfo{
		ID:            project.NoProjectID,
		IsNoProject:   true,
		WorkspacePath: filepath.Join(t.TempDir(), "does-not-exist"),
	}

	f.switchProjectSetupWatcher(context.Background(), p)
	if f.watcher != nil {
		t.Fatal("expected watcher to be nil when no session workspace can be determined")
	}
}

// codeWatcherRoot returns the root of the currently live watcher under
// watcherMu, failing the test when there is none.
func codeWatcherRoot(t *testing.T, f *FrontendAPI) string {
	t.Helper()
	f.watcherMu.Lock()
	defer f.watcherMu.Unlock()
	if f.watcher == nil {
		t.Fatal("expected a live workspace watcher")
	}
	return f.watcher.Root()
}

// closeAPIWatcher returns the cleanup closing whatever watcher is current —
// re-scopes replace the instance, so the cleanup must not hold a stale one.
func closeAPIWatcher(f *FrontendAPI) func() {
	return func() {
		f.watcherMu.Lock()
		defer f.watcherMu.Unlock()
		if f.watcher != nil {
			_ = f.watcher.Close()
		}
	}
}

// TestSwitchProjectSetupWatcher_CodeWatcherRootFollowsActiveSession pins the
// CODE-mode root contract: the watcher root follows the ACTIVE SESSION — a
// managed-worktree session roots the watcher at ITS tree; without a session
// (or with a newer local session) the checkout fallback holds. The switch
// must also materialize nothing (Issue 52): a missing research tree is only
// marked recursive, never created on disk.
func TestSwitchProjectSetupWatcher_CodeWatcherRootFollowsActiveSession(t *testing.T) {
	h := newWTHarness(t, wtFactory())
	f := h.api
	checkout := h.project.WorkspacePath
	t.Cleanup(closeAPIWatcher(f))

	// No sessions yet: the checkout fallback.
	f.switchProjectSetupWatcher(context.Background(), h.project)
	if root := codeWatcherRoot(t, f); root != checkout {
		t.Fatalf("no-session watcher root = %q, want the checkout %q", root, checkout)
	}

	// A managed session of the project flips the root to its tree.
	managed := f.mustManagedSession(t, "", true, "")
	tree := managed.WorkspaceBinding.WorkspacePath
	if tree == checkout {
		t.Fatal("precondition broken: managed tree equals the checkout")
	}
	f.switchProjectSetupWatcher(context.Background(), h.project)
	if root := codeWatcherRoot(t, f); root != tree {
		t.Fatalf("managed-session watcher root = %q, want the session tree %q", root, tree)
	}

	// Issue 52: watching the tree must not materialize research subtrees in
	// it or in the checkout.
	if _, err := os.Stat(config.ProjectResearchPath(tree)); !os.IsNotExist(err) {
		t.Fatalf("switchProjectSetupWatcher materialized the session tree's .research: err=%v", err)
	}
	if _, err := os.Stat(config.ProjectResearchPath(checkout)); !os.IsNotExist(err) {
		t.Fatalf("switchProjectSetupWatcher materialized the checkout's .research: err=%v", err)
	}

	// A newer LOCAL session (checkout workspace) flips the root back. The
	// activity timestamp is stamped an hour ahead explicitly: wall-clock
	// creation times share second-level granularity, and a tie would make
	// the "which session is latest" outcome depend on the store's tie-break.
	local, err := h.manager.CreateSession(h.project.ID, checkout)
	if err != nil {
		t.Fatalf("CreateSession (local): %v", err)
	}
	local.LastActiveAt = time.Now().Add(time.Hour).UTC().Format(time.RFC3339)
	local.CreatedAt = local.LastActiveAt
	if err := h.store.SaveSession(context.Background(), *local); err != nil {
		t.Fatalf("SaveSession (local): %v", err)
	}
	f.switchProjectSetupWatcher(context.Background(), h.project)
	if root := codeWatcherRoot(t, f); root != checkout {
		t.Fatalf("local-session watcher root = %q, want the checkout %q", root, checkout)
	}
}

// TestSwitchProjectSetupWatcher_CodeSessionTreeEventsEmit: with a managed
// session active, a file change inside the session tree emits
// workspace:tree_changed through the production switchProjectSetupWatcher
// wiring — the watcher root IS the tree, so the tree's own changes surface
// without any panel-driven WatchDirectory call.
func TestSwitchProjectSetupWatcher_CodeSessionTreeEventsEmit(t *testing.T) {
	h := newWTHarness(t, wtFactory())
	f := h.api
	managed := f.mustManagedSession(t, "", true, "")
	tree := managed.WorkspaceBinding.WorkspacePath
	t.Cleanup(closeAPIWatcher(f))

	var treeEvents atomic.Int32
	f.emitEvent = func(name string, _ ...any) {
		if name == EventWorkspaceTreeChanged {
			treeEvents.Add(1)
		}
	}

	f.switchProjectSetupWatcher(context.Background(), h.project)
	if root := codeWatcherRoot(t, f); root != tree {
		t.Fatalf("watcher root = %q, want the session tree %q", root, tree)
	}

	// The write retries until the watcher reports it (writeUntilEmitted),
	// so watch-registration latency cannot race the first event.
	writeUntilEmitted(t, filepath.Join(tree, "notes.txt"), []byte("session work\n"), &treeEvents, 1)
}

// TestWatchDirectory_CodeRescopesToSessionTree pins the session-switch path:
// the panel re-roots the file tree via WatchDirectory and the watcher follows
// — from the checkout to an outside-checkout managed tree, tracked emit roots
// moving along — while a repeated call with the same root keeps the live
// watcher instead of tearing it down.
func TestWatchDirectory_CodeRescopesToSessionTree(t *testing.T) {
	h := newWTHarness(t, wtFactory())
	f := h.api
	checkout := h.project.WorkspacePath
	t.Cleanup(closeAPIWatcher(f))
	f.emitEvent = func(string, ...any) {}

	// Fresh project: the watcher starts checkout-rooted (no sessions).
	f.switchProjectSetupWatcher(context.Background(), h.project)
	if root := codeWatcherRoot(t, f); root != checkout {
		t.Fatalf("initial watcher root = %q, want the checkout %q", root, checkout)
	}

	managed := f.mustManagedSession(t, "", true, "")
	tree := managed.WorkspaceBinding.WorkspacePath

	// Session switch: the panel re-roots at the session workspace.
	if err := f.WatchDirectory(tree); err != nil {
		t.Fatalf("WatchDirectory(session tree): %v", err)
	}
	if root := codeWatcherRoot(t, f); root != tree {
		t.Fatalf("watcher root after re-scope = %q, want the session tree %q", root, tree)
	}
	f.activeProjectMu.RLock()
	tracked := f.activeResearchRoot
	f.activeProjectMu.RUnlock()
	if want := config.ProjectResearchPath(tree); tracked != want {
		t.Fatalf("tracked research root = %q, want the session tree's %q", tracked, want)
	}

	// A repeat call with the same root keeps the live watcher (no teardown).
	f.watcherMu.Lock()
	live := f.watcher
	f.watcherMu.Unlock()
	if err := f.WatchDirectory(tree); err != nil {
		t.Fatalf("WatchDirectory(same tree): %v", err)
	}
	f.watcherMu.Lock()
	kept := f.watcher == live
	f.watcherMu.Unlock()
	if !kept {
		t.Fatal("repeated WatchDirectory with an unchanged root replaced the watcher")
	}
}
