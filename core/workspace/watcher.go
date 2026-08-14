package workspace

import (
	"errors"
	"fmt"
	"log/slog"
	"os"
	"path/filepath"
	"sync"
	"time"

	"github.com/fsnotify/fsnotify"
	"github.com/v0lka/sp4rk/pathutil"
)

// defaultDebounce is the debounce interval for file system events.
// 200ms balances responsiveness with avoiding excessive re-processing
// of rapid consecutive file changes (e.g., editor save + format).
const defaultDebounce = 200 * time.Millisecond

// ChangeHandler is invoked (debounced) with the set of changed file/directory
// paths accumulated during the debounce window. The slice may be empty when
// events carried no usable path (e.g. some Chmod/Rename events).
type ChangeHandler func(changedPaths []string)

// Watcher watches directories for file system changes and calls onChange when changes occur.
type Watcher struct {
	root     string
	watcher  *fsnotify.Watcher
	onChange ChangeHandler
	mu       sync.Mutex
	watched  map[string]bool
	// recursiveRoots are directory roots whose entire subtree must be watched.
	// The event loop auto-adds any directory created within a recursive root
	// so newly-created subdirectories (e.g. a new R-NNN research project) are
	// detected without a manual WatchDir call. fsnotify is NOT recursive, even
	// on macOS (the FSEvents backend filters to explicitly-added directories),
	// so recursive watching must be implemented by adding every directory.
	recursiveRoots map[string]bool
	done           chan struct{}
	closeOnce      sync.Once
	logger         *slog.Logger
}

// log returns the logger, falling back to slog.Default() if nil.
func (w *Watcher) log() *slog.Logger {
	if w.logger != nil {
		return w.logger
	}
	return slog.Default()
}

// NewWatcher creates a Watcher that watches the root directory and calls onChange
// (debounced at 200ms) on any change. The accumulated changed paths are passed
// to onChange so consumers can decide whether a change is relevant to them
// (e.g. skip re-indexing for .git-internal churn).
func NewWatcher(root string, onChange ChangeHandler, loggers ...*slog.Logger) (*Watcher, error) {
	if onChange == nil {
		return nil, errors.New("onChange callback must not be nil")
	}

	absRoot, err := filepath.Abs(root)
	if err != nil {
		return nil, fmt.Errorf("failed to resolve root path: %w", err)
	}

	fsw, err := fsnotify.NewWatcher()
	if err != nil {
		return nil, fmt.Errorf("failed to create fsnotify watcher: %w", err)
	}

	var logger *slog.Logger
	if len(loggers) > 0 {
		logger = loggers[0]
	}

	w := &Watcher{
		root:           absRoot,
		watcher:        fsw,
		onChange:       onChange,
		watched:        make(map[string]bool),
		recursiveRoots: make(map[string]bool),
		done:           make(chan struct{}),
		logger:         logger,
	}

	// Watch the root directory itself.
	if err := fsw.Add(absRoot); err != nil {
		_ = fsw.Close()
		return nil, fmt.Errorf("failed to watch root directory: %w", err)
	}
	w.watched[absRoot] = true

	// Also watch the .git directory so that index changes (staging/unstaging)
	// trigger the same onChange callback used by the file tree. The vector
	// indexer ignores .git, so the consumer (backend) filters .git paths before
	// triggering re-indexing — keeping this watch for file-tree refresh only.
	gitDir := filepath.Join(absRoot, ".git")
	if stat, err := os.Stat(gitDir); err == nil && stat.IsDir() {
		if err := fsw.Add(gitDir); err != nil {
			w.log().Warn("failed to watch .git directory", "error", err)
		}
	}

	go w.eventLoop()
	return w, nil
}

// eventLoop reads fsnotify events, accumulates their paths during a debounce
// window, and flushes them to onChange. All state (pending paths + timer) is
// confined to this single goroutine, so no extra synchronization is needed.
func (w *Watcher) eventLoop() {
	var pending map[string]struct{}
	var timer *time.Timer
	var timerC <-chan time.Time

	for {
		select {
		case <-w.done:
			if timer != nil {
				timer.Stop()
			}
			return
		case event, ok := <-w.watcher.Events:
			if !ok {
				return
			}
			if event.Name != "" {
				if pending == nil {
					pending = make(map[string]struct{})
				}
				pending[event.Name] = struct{}{}
			}
			// Auto-expand: when a directory is created inside a recursive
			// root, add it (and its subtree) to the watch list so changes in
			// newly-created subdirectories are detected. fsnotify is not
			// recursive, so this is required to keep a recursively-watched
			// tree complete as it grows.
			if event.Has(fsnotify.Create) && event.Name != "" {
				w.maybeAutoAddDir(event.Name)
			}
			// (Re)start the debounce window.
			if timer == nil {
				timer = time.NewTimer(defaultDebounce)
				timerC = timer.C
			} else {
				if !timer.Stop() {
					// Timer already fired; drain its channel so Reset is safe.
					select {
					case <-timer.C:
					default:
					}
				}
				timer.Reset(defaultDebounce)
			}
		case <-timerC:
			paths := keysOf(pending)
			pending = nil
			timer = nil
			timerC = nil
			w.onChange(paths)
		case watchErr, ok := <-w.watcher.Errors:
			if !ok {
				return
			}
			w.log().Debug("fsnotify watcher error", "error", watchErr)
		}
	}
}

// keysOf returns the keys of m as a slice, or nil if m is empty/nil.
func keysOf(m map[string]struct{}) []string {
	if len(m) == 0 {
		return nil
	}
	out := make([]string, 0, len(m))
	for k := range m {
		out = append(out, k)
	}
	return out
}

// WatchTree recursively watches a directory and all of its subdirectories,
// marking it as a "recursive root" so that directories created inside it
// later are automatically added (see maybeAutoAddDir). This is required
// because fsnotify is not recursive — even on macOS the FSEvents backend only
// reports events for explicitly-added directories. Use this for subtrees that
// must be watched as a whole (e.g. the .research artifact tree), in contrast
// to WatchDir which watches a single directory non-recursively.
//
// Best-effort: unreadable or missing subdirectories are skipped rather than
// failing the whole call. Idempotent: calling it twice on the same root is a
// no-op. The path must be under the workspace root.
func (w *Watcher) WatchTree(path string) error {
	absPath, err := filepath.Abs(path)
	if err != nil {
		return fmt.Errorf("invalid path: %w", err)
	}
	if !w.isUnderRoot(absPath) {
		return errors.New("path is outside workspace root")
	}

	w.mu.Lock()
	defer w.mu.Unlock()
	w.recursiveRoots[absPath] = true
	w.addTreeLocked(absPath)
	return nil
}

// addTreeLocked adds dir and every subdirectory beneath it to the fsnotify
// watch list. The caller must hold w.mu. Missing or unreadable directories
// are skipped (best-effort).
func (w *Watcher) addTreeLocked(dir string) {
	if !w.watched[dir] {
		if err := w.watcher.Add(dir); err != nil {
			w.log().Debug("watchTree: failed to watch directory", "dir", dir, "error", err)
		} else {
			w.watched[dir] = true
		}
	}
	_ = filepath.WalkDir(dir, func(p string, d os.DirEntry, _ error) error {
		// On a read error WalkDir passes a nil DirEntry; skip it rather
		// than aborting the whole walk (best-effort).
		if d == nil || !d.IsDir() || p == dir {
			return nil
		}
		if !w.watched[p] {
			if err := w.watcher.Add(p); err != nil {
				w.log().Debug("watchTree: failed to watch subdirectory", "dir", p, "error", err)
			} else {
				w.watched[p] = true
			}
		}
		return nil
	})
}

// maybeAutoAddDir adds path to the watch list when it is a directory created
// inside a recursive root. Called from the event loop on fsnotify.Create
// events so a growing recursively-watched tree (e.g. a new R-NNN research
// project) stays fully watched.
func (w *Watcher) maybeAutoAddDir(path string) {
	w.mu.Lock()
	defer w.mu.Unlock()
	if !w.isWithinRecursiveRootLocked(path) {
		return
	}
	info, err := os.Stat(path)
	if err != nil || !info.IsDir() {
		return
	}
	if w.watched[path] {
		return
	}
	if err := w.watcher.Add(path); err != nil {
		w.log().Debug("auto-add: failed to watch new directory", "dir", path, "error", err)
		return
	}
	w.watched[path] = true
	// The new directory may already contain subdirectories (e.g. a research
	// project created with its hypotheses/ folder in one step); add them too.
	w.addTreeLocked(path)
}

// isWithinRecursiveRootLocked reports whether path falls inside any recursive
// root. The caller must hold w.mu.
func (w *Watcher) isWithinRecursiveRootLocked(path string) bool {
	for root := range w.recursiveRoots {
		if within, err := pathutil.IsWithinPath(root, path); err == nil && within {
			return true
		}
	}
	return false
}

// WatchDir adds a directory to the watch list. The path must be under the root.
func (w *Watcher) WatchDir(path string) error {
	absPath, err := filepath.Abs(path)
	if err != nil {
		return fmt.Errorf("invalid path: %w", err)
	}
	if !w.isUnderRoot(absPath) {
		return errors.New("path is outside workspace root")
	}

	w.mu.Lock()
	defer w.mu.Unlock()

	if w.watched[absPath] {
		return nil // already watched
	}
	if err := w.watcher.Add(absPath); err != nil {
		return fmt.Errorf("failed to watch directory: %w", err)
	}
	w.watched[absPath] = true
	return nil
}

// UnwatchTree removes a recursively-watched directory tree from the watch
// list. It removes the root from recursiveRoots and unwatches every directory
// that was added as part of the recursive watch (the root itself and all
// subdirectories beneath it). This is the inverse of [Watcher.WatchTree].
//
// Best-effort: fsnotify Remove errors are logged at Debug level and skipped
// so a partially-removed tree does not abort the call. Idempotent: calling it
// on a root that was never watched (or already unwatched) is a no-op.
func (w *Watcher) UnwatchTree(path string) error {
	absPath, err := filepath.Abs(path)
	if err != nil {
		return fmt.Errorf("invalid path: %w", err)
	}

	w.mu.Lock()
	defer w.mu.Unlock()

	delete(w.recursiveRoots, absPath)

	// Remove every watched directory at or beneath the unwatched root so
	// future changes inside it no longer trigger callbacks.
	for watched := range w.watched {
		within, withinErr := pathutil.IsWithinPath(absPath, watched)
		if withinErr != nil || !within {
			continue
		}
		if rmErr := w.watcher.Remove(watched); rmErr != nil {
			w.log().Debug("unwatchTree: failed to unwatch directory",
				"dir", watched, "error", rmErr)
		}
		delete(w.watched, watched)
	}
	return nil
}

// UnwatchDir removes a directory from the watch list.
func (w *Watcher) UnwatchDir(path string) error {
	absPath, err := filepath.Abs(path)
	if err != nil {
		return fmt.Errorf("invalid path: %w", err)
	}

	w.mu.Lock()
	defer w.mu.Unlock()

	if !w.watched[absPath] {
		return nil // not watched
	}
	if err := w.watcher.Remove(absPath); err != nil {
		return fmt.Errorf("failed to unwatch directory: %w", err)
	}
	delete(w.watched, absPath)
	return nil
}

// Close stops watching and releases resources. Safe to call multiple times.
func (w *Watcher) Close() error {
	var err error
	w.closeOnce.Do(func() {
		close(w.done)
		err = w.watcher.Close()
	})
	return err
}

// isUnderRoot checks whether absPath is under (or equal to) the root directory.
// Uses the centralized containment primitive so symlink-escaped paths (e.g.
// macOS /var → /private/var) are resolved consistently with the rest of the
// codebase, per the path-centralization convention.
func (w *Watcher) isUnderRoot(absPath string) bool {
	within, err := pathutil.IsWithinPath(w.root, absPath)
	if err != nil {
		return false
	}
	return within
}
