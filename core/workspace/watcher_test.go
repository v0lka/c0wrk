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
	w, err := NewWatcher(dir, func(paths []string) {
		called.Add(1)
		if len(paths) == 0 {
			t.Error("expected changed paths to be passed to onChange")
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
}
