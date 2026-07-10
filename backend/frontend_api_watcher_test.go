package backend

import (
	"os"
	"path/filepath"
	"sync/atomic"
	"testing"
	"time"

	"github.com/v0lka/c0wrk/backend/project"
	"github.com/v0lka/c0wrk/core/workspace"
)

// markNoProjectActive sets the active project to No Project on a FrontendAPI.
func markNoProjectActive(f *FrontendAPI) {
	f.activeProjectMu.Lock()
	f.activeProjectID = project.NoProjectID
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

// TestWatchDirectory_NoProject_ReScopesAcrossSessions verifies the core fix:
// switching chat sessions re-scopes the watcher to the new session's workspace,
// even though that workspace is outside the previous session's watcher root.
// Previously WatchDirectory rejected the new path with "path is outside
// workspace root" and file watching silently broke for every session after the
// first.
func TestWatchDirectory_NoProject_ReScopesAcrossSessions(t *testing.T) {
	base := t.TempDir()
	// Two isolated session workspaces, siblings (neither contains the other).
	wsA := filepath.Join(base, "sess-a", "workspace")
	wsB := filepath.Join(base, "sess-b", "workspace")

	var emitted atomic.Int32
	f := &FrontendAPI{
		agentDir:  base,
		emitEvent: func(string, ...any) { emitted.Add(1) },
	}
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
	ws := filepath.Join(base, "sess-new", "workspace") // does not exist yet

	f := &FrontendAPI{
		agentDir:  base,
		emitEvent: func(string, ...any) {},
	}
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
	ws := filepath.Join(base, "sess-a", "workspace")

	f := &FrontendAPI{
		agentDir:  base,
		emitEvent: func(string, ...any) {},
	}
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
	p := &project.ProjectInfo{
		ID:            project.NoProjectID,
		IsNoProject:   true,
		WorkspacePath: filepath.Join(t.TempDir(), "does-not-exist"),
	}

	f.switchProjectSetupWatcher(p)
	if f.watcher != nil {
		t.Fatal("expected watcher to be nil when no session workspace can be determined")
	}
}
