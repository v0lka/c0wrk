package workspace

import (
	"os"
	"path/filepath"
	"testing"
	"time"
)

// TestWatcher_RecreatedDirectoryIsRewatched pins review [71]: fsnotify
// watches inodes, so when a recursively-watched directory is deleted the
// kernel drops its watch while the watch-map entry stays behind — and the
// auto-add short-circuit then never re-registered a directory recreated at
// the same path, silently losing all change detection for it. The watcher
// must forget the watch on the Remove/Rename event so the recreated
// directory's Create re-registers it and writes inside it are detected.
//
// Synchronization is channel-based (no sleeps): every step waits for the
// debounced onChange batch carrying the path it produced, which also
// guarantees the event loop has finished the bookkeeping for that event
// (auto-add/forget run synchronously in the same loop iteration, before the
// debounce fires).
func TestWatcher_RecreatedDirectoryIsRewatched(t *testing.T) {
	root := t.TempDir()
	sub := filepath.Join(root, "recreated")

	events := make(chan []string, 64)
	w, err := NewWatcher(root, func(paths []string) {
		select {
		case events <- paths:
		default: // never block the event loop on a slow test
		}
	})
	if err != nil {
		t.Fatalf("NewWatcher: %v", err)
	}
	defer func() { _ = w.Close() }()
	if err := w.WatchTree(root); err != nil {
		t.Fatalf("WatchTree: %v", err)
	}

	waitForBatch := func(what string, match func([]string) bool) {
		t.Helper()
		deadline := time.After(5 * time.Second)
		for {
			select {
			case paths := <-events:
				if match(paths) {
					return
				}
			case <-deadline:
				t.Fatalf("timed out waiting for watcher event: %s", what)
			}
		}
	}
	hasPath := func(target string) func([]string) bool {
		return func(paths []string) bool {
			for _, p := range paths {
				if p == target {
					return true
				}
			}
			return false
		}
	}

	// 1. Create the directory under the recursive root: auto-add watches it.
	if err := os.Mkdir(sub, 0o755); err != nil {
		t.Fatalf("mkdir: %v", err)
	}
	waitForBatch("subdirectory create", hasPath(sub))

	// 2. Delete it: the stale watch entry must be forgotten.
	if err := os.RemoveAll(sub); err != nil {
		t.Fatalf("remove: %v", err)
	}
	waitForBatch("subdirectory remove", hasPath(sub))

	// 3. Recreate at the same path: the Create must re-register the watch.
	if err := os.Mkdir(sub, 0o755); err != nil {
		t.Fatalf("recreate: %v", err)
	}
	waitForBatch("subdirectory recreate", hasPath(sub))

	// 4. Write inside the recreated directory: the event that was lost
	// before the fix must now arrive.
	file := filepath.Join(sub, "late.txt")
	if err := os.WriteFile(file, []byte("data"), 0o644); err != nil {
		t.Fatalf("write: %v", err)
	}
	waitForBatch("write inside the recreated subdirectory", hasPath(file))
}

// TestWatcher_DeletedParentForgetsDescendants pins the subtree half of the
// forget contract: deleting a watched directory kills the kernel watches of
// every descendant too, but the Remove event fires only for the deleted
// directory itself — so forgetting must drop the watched-map entries of the
// WHOLE deleted subtree, not just the exact path. Otherwise stale descendant
// entries short-circuit re-registration (both auto-add and WatchTree skip
// paths already present in the map) and a recreated child inside the
// recreated parent stays silently unwatched.
func TestWatcher_DeletedParentForgetsDescendants(t *testing.T) {
	root := t.TempDir()
	parent := filepath.Join(root, "parent")
	child := filepath.Join(parent, "child")

	events := make(chan []string, 256)
	w, err := NewWatcher(root, func(paths []string) {
		select {
		case events <- paths:
		default: // never block the event loop on a slow test
		}
	})
	if err != nil {
		t.Fatalf("NewWatcher: %v", err)
	}
	defer func() { _ = w.Close() }()
	if err := w.WatchTree(root); err != nil {
		t.Fatalf("WatchTree: %v", err)
	}

	waitForBatch := func(what string, match func([]string) bool) {
		t.Helper()
		deadline := time.After(5 * time.Second)
		for {
			select {
			case paths := <-events:
				if match(paths) {
					return
				}
			case <-deadline:
				t.Fatalf("timed out waiting for watcher event: %s", what)
			}
		}
	}
	hasPath := func(target string) func([]string) bool {
		return func(paths []string) bool {
			for _, p := range paths {
				if p == target {
					return true
				}
			}
			return false
		}
	}

	// 1. Build parent/child one mkdir at a time — each parent must be
	// observed (and thus auto-added) before its child exists, or the child's
	// create is never seen by any watch.
	if err := os.Mkdir(parent, 0o755); err != nil {
		t.Fatalf("mkdir parent: %v", err)
	}
	waitForBatch("parent create", hasPath(parent))
	if err := os.Mkdir(child, 0o755); err != nil {
		t.Fatalf("mkdir child: %v", err)
	}
	waitForBatch("child create", hasPath(child))

	// 2. Delete the PARENT: only parent gets a Remove event; the child's
	// stale entry must be forgotten along with it.
	if err := os.RemoveAll(parent); err != nil {
		t.Fatalf("remove parent: %v", err)
	}
	waitForBatch("parent remove", hasPath(parent))

	// 3. Recreate step by step (same ordering discipline as step 1): the
	// recreated parent re-registers via its create, then the child — whose
	// stale map entry the parent's subtree-forget must have dropped —
	// re-registers via its own create.
	if err := os.Mkdir(parent, 0o755); err != nil {
		t.Fatalf("recreate parent: %v", err)
	}
	waitForBatch("recreated parent", hasPath(parent))
	if err := os.Mkdir(child, 0o755); err != nil {
		t.Fatalf("recreate child: %v", err)
	}
	waitForBatch("recreated child", hasPath(child))

	// 4. Write inside the recreated child: the event the stale entry used to
	// swallow must arrive.
	file := filepath.Join(child, "deep.txt")
	if err := os.WriteFile(file, []byte("data"), 0o644); err != nil {
		t.Fatalf("write: %v", err)
	}
	waitForBatch("write inside the recreated child", hasPath(file))
}

// TestWatcher_ForgetUnwatchedParentDropsWatchedDescendants pins the
// forgetWatchedDir contract for the partial-registration gap: a Remove event
// can name a directory whose own registration was skipped best-effort (a
// WatchTree walk hitting a transient error) while a descendant DID register.
// Forgetting only exact map entries would leave the descendant's stale entry
// behind, and a recreated descendant would then be short-circuited by both
// re-registration paths — silently unwatched (the review [71] loss class).
// The descendant scan must therefore run even when the deleted path itself is
// not in the watched map.
func TestWatcher_ForgetUnwatchedParentDropsWatchedDescendants(t *testing.T) {
	root := t.TempDir()
	child := filepath.Join(root, "gapchild")
	if err := os.Mkdir(child, 0o755); err != nil {
		t.Fatalf("mkdir child: %v", err)
	}

	events := make(chan []string, 64)
	w, err := NewWatcher(root, func(paths []string) {
		select {
		case events <- paths:
		default: // never block the event loop on a slow test
		}
	})
	if err != nil {
		t.Fatalf("NewWatcher: %v", err)
	}
	defer func() { _ = w.Close() }()

	// Simulate the gap deterministically: the deleted parent is NOT in the
	// watched map, its descendant is — the state a partially failed WatchTree
	// walk leaves behind.
	if err := w.watcher.Add(child); err != nil {
		t.Fatalf("watcher.Add(child): %v", err)
	}
	w.mu.Lock()
	delete(w.watched, root)
	w.watched[child] = true
	w.mu.Unlock()

	// The Remove event for the unwatched parent must still drop the watched
	// descendant: no early return on the missing parent entry.
	w.forgetWatchedDir(root)

	w.mu.Lock()
	_, childStillWatched := w.watched[child]
	w.mu.Unlock()
	if childStillWatched {
		t.Fatal("watched descendant survived forgetWatchedDir of an unwatched parent; a recreated subtree would stay unwatched")
	}
}

// TestWatcher_RenamedDirectoryDropsStaleWatch pins the Rename half of the
// forget contract: a rename MOVES the kernel watch with the inode (unlike a
// Remove, which kills it), but the watched-map entry for the old path used
// to stay behind — and since both re-registration paths short-circuit on
// that map, a directory recreated at the renamed-away path was never
// re-watched and its change detection silently died. The Rename event must
// drop the stale entries (without closing the live kernel watch) so the
// recreate's Create re-registers them.
func TestWatcher_RenamedDirectoryDropsStaleWatch(t *testing.T) {
	root := t.TempDir()
	sub := filepath.Join(root, "renamed-away")

	events := make(chan []string, 64)
	w, err := NewWatcher(root, func(paths []string) {
		select {
		case events <- paths:
		default: // never block the event loop on a slow test
		}
	})
	if err != nil {
		t.Fatalf("NewWatcher: %v", err)
	}
	defer func() { _ = w.Close() }()
	if err := w.WatchTree(root); err != nil {
		t.Fatalf("WatchTree: %v", err)
	}

	waitForBatch := func(what string, match func([]string) bool) {
		t.Helper()
		deadline := time.After(5 * time.Second)
		for {
			select {
			case paths := <-events:
				if match(paths) {
					return
				}
			case <-deadline:
				t.Fatalf("timed out waiting for watcher event: %s", what)
			}
		}
	}
	hasPath := func(target string) func([]string) bool {
		return func(paths []string) bool {
			for _, p := range paths {
				if p == target {
					return true
				}
			}
			return false
		}
	}

	// 1. Create the directory under the recursive root: auto-add watches it.
	if err := os.Mkdir(sub, 0o755); err != nil {
		t.Fatalf("mkdir: %v", err)
	}
	waitForBatch("subdirectory create", hasPath(sub))

	// 2. Rename it away: the stale watch entry for the old path must be
	// dropped (the watches themselves are deliberately not closed here —
	// fsnotify removes a moved directory's own watch via its IN_MOVE_SELF
	// handling, and racing that from the event loop is what the
	// destination-recovery design avoids).
	renamed := filepath.Join(root, "renamed-here")
	if err := os.Rename(sub, renamed); err != nil {
		t.Fatalf("rename: %v", err)
	}
	waitForBatch("subdirectory rename", hasPath(sub))

	// 3. Recreate a directory at the renamed-away path: the Create must
	// re-register the watch instead of short-circuiting on the stale entry.
	if err := os.Mkdir(sub, 0o755); err != nil {
		t.Fatalf("recreate: %v", err)
	}
	waitForBatch("subdirectory recreate at the renamed-away path", hasPath(sub))

	// 4. Write inside the recreated directory: the event that was lost
	// before the fix must now arrive.
	file := filepath.Join(sub, "after-rename.txt")
	if err := os.WriteFile(file, []byte("data"), 0o644); err != nil {
		t.Fatalf("write: %v", err)
	}
	waitForBatch("write inside the recreated-at-renamed-path directory", hasPath(file))
}
