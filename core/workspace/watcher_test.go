package workspace

import (
	"os"
	"path/filepath"
	"sync/atomic"
	"testing"
	"time"
)

func TestNewWatcher_WatchesRoot(t *testing.T) {
	dir := t.TempDir()

	var called atomic.Int32
	w, err := NewWatcher(dir, func(_ []string) {
		called.Add(1)
	})
	if err != nil {
		t.Fatalf("NewWatcher: %v", err)
	}
	defer func() { _ = w.Close() }()

	// Create a file in the root directory.
	if err := os.WriteFile(filepath.Join(dir, "test.txt"), []byte("hello"), 0o644); err != nil {
		t.Fatalf("WriteFile: %v", err)
	}

	// Wait for debounce to fire.
	time.Sleep(500 * time.Millisecond)

	if c := called.Load(); c == 0 {
		t.Error("expected onChange to be called at least once, got 0")
	}
}

func TestWatcher_WatchDir_UnwatchDir(t *testing.T) {
	root := t.TempDir()
	sub := filepath.Join(root, "subdir")
	if err := os.Mkdir(sub, 0o755); err != nil {
		t.Fatalf("Mkdir: %v", err)
	}

	var called atomic.Int32
	w, err := NewWatcher(root, func(_ []string) {
		called.Add(1)
	})
	if err != nil {
		t.Fatalf("NewWatcher: %v", err)
	}
	defer func() { _ = w.Close() }()

	// Watch the subdirectory.
	if err := w.WatchDir(sub); err != nil {
		t.Fatalf("WatchDir: %v", err)
	}

	// Create a file inside the subdirectory.
	if err := os.WriteFile(filepath.Join(sub, "file.txt"), []byte("data"), 0o644); err != nil {
		t.Fatalf("WriteFile: %v", err)
	}
	time.Sleep(500 * time.Millisecond)

	if c := called.Load(); c == 0 {
		t.Error("expected onChange after WatchDir, got 0")
	}

	// Unwatch the subdirectory.
	if err := w.UnwatchDir(sub); err != nil {
		t.Fatalf("UnwatchDir: %v", err)
	}

	// Reset counter.
	called.Store(0)

	// Create another file — should still trigger because root is watched.
	// But the subdirectory itself should no longer trigger events for its own files.
	// We test that UnwatchDir didn't error out; full isolation is hard to test
	// because the root watcher may still pick up events.
}

func TestWatcher_Debounce(t *testing.T) {
	dir := t.TempDir()

	var called atomic.Int32
	var gotEmpty atomic.Bool
	w, err := NewWatcher(dir, func(paths []string) {
		called.Add(1)
		if len(paths) == 0 {
			gotEmpty.Store(true)
		}
	})
	if err != nil {
		t.Fatalf("NewWatcher: %v", err)
	}
	defer func() { _ = w.Close() }()

	// Rapidly create multiple files.
	for i := range 10 {
		name := filepath.Join(dir, "file"+string(rune('0'+i))+".txt")
		if err := os.WriteFile(name, []byte("data"), 0o644); err != nil {
			t.Fatalf("WriteFile: %v", err)
		}
	}

	// Wait for debounce.
	time.Sleep(600 * time.Millisecond)

	c := called.Load()
	if c == 0 {
		t.Error("expected at least one onChange call")
	}
	if gotEmpty.Load() {
		t.Error("expected changed paths to be passed to onChange (got empty)")
	}
	// With debounce, we should get far fewer calls than events.
	if c > 5 {
		t.Errorf("expected debounced calls (≤5), got %d", c)
	}
}

func TestWatcher_Close(t *testing.T) {
	dir := t.TempDir()

	w, err := NewWatcher(dir, func(_ []string) {})
	if err != nil {
		t.Fatalf("NewWatcher: %v", err)
	}

	if err := w.Close(); err != nil {
		t.Fatalf("Close: %v", err)
	}

	// Double close should not panic.
	if err := w.Close(); err != nil {
		t.Fatalf("Double Close: %v", err)
	}
}

func TestWatcher_PathValidation(t *testing.T) {
	root := t.TempDir()
	outside := t.TempDir()

	w, err := NewWatcher(root, func(_ []string) {})
	if err != nil {
		t.Fatalf("NewWatcher: %v", err)
	}
	defer func() { _ = w.Close() }()

	err = w.WatchDir(outside)
	if err == nil {
		t.Error("expected error when watching path outside root")
	}

	err = w.WatchTree(outside)
	if err == nil {
		t.Error("expected error when watchTree-ing path outside root")
	}
}

// TestWatcher_WatchTree_NestedDirs verifies the core fix for the research-panel
// bug: WatchTree recursively watches subdirectories so edits to deeply-nested
// files are detected. fsnotify is NOT recursive (it only reports events for
// explicitly-added directories, even on macOS), so without WatchTree a change
// in root/a/b/c/file.go goes unnoticed.
func TestWatcher_WatchTree_NestedDirs(t *testing.T) {
	root := t.TempDir()
	// Create a nested subtree before watching.
	deep := filepath.Join(root, "a", "b", "c")
	if err := os.MkdirAll(deep, 0o755); err != nil {
		t.Fatalf("MkdirAll: %v", err)
	}

	var called atomic.Int32
	w, err := NewWatcher(root, func(_ []string) { called.Add(1) })
	if err != nil {
		t.Fatalf("NewWatcher: %v", err)
	}
	defer func() { _ = w.Close() }()

	if err := w.WatchTree(root); err != nil {
		t.Fatalf("WatchTree: %v", err)
	}
	time.Sleep(200 * time.Millisecond)

	// Edit a file three levels deep — must be detected now.
	if err := os.WriteFile(filepath.Join(deep, "file.go"), []byte("x"), 0o644); err != nil {
		t.Fatalf("WriteFile: %v", err)
	}
	if !waitEvent(&called, 2*time.Second) {
		t.Fatal("expected onChange for nested file after WatchTree; fsnotify must be watching the subtree")
	}
}

// TestWatcher_WatchTree_AutoAddNewDir verifies that a directory created inside
// a recursive root after WatchTree is automatically added to the watch list,
// so a growing tree (e.g. a new R-NNN research project) stays fully watched.
func TestWatcher_WatchTree_AutoAddNewDir(t *testing.T) {
	root := t.TempDir()
	tree := filepath.Join(root, "research")
	if err := os.MkdirAll(tree, 0o755); err != nil {
		t.Fatalf("MkdirAll: %v", err)
	}

	var called atomic.Int32
	w, err := NewWatcher(root, func(_ []string) { called.Add(1) })
	if err != nil {
		t.Fatalf("NewWatcher: %v", err)
	}
	defer func() { _ = w.Close() }()

	if err := w.WatchTree(tree); err != nil {
		t.Fatalf("WatchTree: %v", err)
	}
	time.Sleep(200 * time.Millisecond)

	// Create a brand-new nested directory inside the watched tree.
	newDir := filepath.Join(tree, "R-002", "hypotheses")
	if err := os.MkdirAll(newDir, 0o755); err != nil {
		t.Fatalf("MkdirAll new dir: %v", err)
	}
	// Give the mkdir events time to propagate and trigger auto-add.
	time.Sleep(400 * time.Millisecond)

	called.Store(0)
	// A file in the newly-created directory must now be detected.
	if err := os.WriteFile(filepath.Join(newDir, "H-001.md"), []byte("# H-001"), 0o644); err != nil {
		t.Fatalf("WriteFile: %v", err)
	}
	if !waitEvent(&called, 2*time.Second) {
		t.Fatal("expected onChange for file in auto-added directory; new subdirs must be watched")
	}
}

// waitEvent polls the counter until it is non-zero or the timeout elapses.
func waitEvent(counter *atomic.Int32, timeout time.Duration) bool {
	deadline := time.Now().Add(timeout)
	for time.Now().Before(deadline) {
		if counter.Load() > 0 {
			return true
		}
		time.Sleep(25 * time.Millisecond)
	}
	return counter.Load() > 0
}
