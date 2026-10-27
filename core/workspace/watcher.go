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

// defaultDebounce balances responsiveness against re-processing rapid
// consecutive changes (e.g. editor save + format).
const defaultDebounce = 200 * time.Millisecond

// ChangeHandler is invoked (debounced) with the changed file/directory paths
// accumulated during the debounce window; may be empty for pathless events.
type ChangeHandler func(changedPaths []string)

// Watcher watches directories for file system changes and calls onChange when changes occur.
type Watcher struct {
	root     string
	watcher  *fsnotify.Watcher
	onChange ChangeHandler
	mu       sync.Mutex
	watched  map[string]bool
	// extraRoots are additional admitted roots for the under-root gate:
	// WatchDir/WatchTree accept the primary root OR any extra root — for subtrees
	// legitimately outside it (a managed worktree's .research tree at checkout root).
	extraRoots map[string]bool
	// recursiveRoots are roots whose entire subtree must be watched: the event
	// loop auto-adds directories created inside them (fsnotify is NOT recursive,
	// even on macOS) — new subdirectories are detected without a WatchDir call.
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

// NewWatcher creates a Watcher on root that calls onChange (debounced) with
// the accumulated changed paths, so consumers can decide relevance.
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
		extraRoots:     make(map[string]bool),
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

	// Also watch the .git entry so staging/HEAD churn reaches the same onChange
	// (the backend filters .git paths before re-indexing — file-tree refresh
	// only). For a LINKED worktree <root>/.git is a "gitdir:" pointer file and
	// the real metadata lives in the named per-worktree directory outside the
	// watched root, so the resolved directory is watched instead — the same
	// single-level shape as a checkout's own <checkout>/.git. Best-effort — see
	// specs/domains/workspace.md (Filesystem Watcher).
	gitDir, err := resolveGitDirAt(absRoot)
	if err != nil {
		w.log().Debug("failed to resolve .git entry for watching", "root", absRoot, "error", err)
	} else if gitDir != "" {
		if err := fsw.Add(gitDir); err != nil {
			w.log().Warn("failed to watch .git directory", "error", err)
		}
	}

	go w.eventLoop()
	return w, nil
}

// Root returns the watcher's primary root (absolute). Fixed at construction (no
// locking needed); callers compare it to a re-scope target to keep the watcher.
func (w *Watcher) Root() string {
	return w.root
}

// eventLoop reads fsnotify events, accumulates paths during a debounce window,
// and flushes them to onChange; all state is confined to this one goroutine.
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
			w.log().Debug("fsnotify event", "op", event.Op.String(), "path", event.Name)
			// Pure Chmod events carry no content change but every git index read
			// emits one on macOS — without this filter a read-only git refresh
			// loops the watcher forever. Skip entirely (no pending entry, no
			// debounce restart); a Chmod ORed with a write-ish op still passes, and
			// Windows never emits Chmod. See specs/domains/workspace.md.
			if event.Op&fsnotify.Chmod != 0 &&
				event.Op&(fsnotify.Write|fsnotify.Create|fsnotify.Remove|fsnotify.Rename) == 0 {
				continue
			}
			// A Write whose path IS a directory is directory-mtime churn (NTFS
			// FILE_ACTION_MODIFIED; kqueue/inotify similar), not a file write —
			// suppress it. The check covers EVERY directory: a child's change bumps
			// an unwatched subdirectory's mtime, reported by the parent watch as a
			// Write that can LAG the child events by seconds (re-armed the git
			// refresh loop on Windows CI). Nothing is lost — real changes arrive as child-path events. See specs/domains/workspace.md.
			if event.Has(fsnotify.Write) &&
				!event.Has(fsnotify.Create|fsnotify.Remove|fsnotify.Rename) &&
				w.pathIsDirectory(event.Name) {
				w.log().Debug("suppressing Write on directory", "path", event.Name)
				continue
			}
			if event.Name != "" {
				if pending == nil {
					pending = make(map[string]struct{})
				}
				pending[event.Name] = struct{}{}
			}
			// Auto-expand: a Create inside a recursive root adds the new directory
			// (and subtree), keeping a growing recursively-watched tree complete.
			if event.Has(fsnotify.Create) && event.Name != "" {
				w.maybeAutoAddDir(event.Name)
			}
			// Forget watches on deleted directories (review [71]): the kernel drops
			// an unlinked directory's watch while watched[path] stays true, short-
			// circuiting the auto-add — dropping the entry lets the recreate's
			// Create re-register it.
			if event.Has(fsnotify.Remove) && event.Name != "" {
				w.forgetWatchedDir(event.Name)
			} else if event.Has(fsnotify.Rename) && event.Name != "" {
				// Drop stale watched entries for the old path (reviews [71]): they
				// short-circuit every re-watch path. Unlike Remove, no fsnotify
				// closes here — on fsnotify v1 a moved watched directory has its
				// watch removed by fsnotify's own IN_MOVE_SELF handling at an
				// unobservable ordering, so re-registration raced it and
				// intermittently lost all detection. Recovery is recreate-at-old-
				// path or recursive-root auto-add; WatchDir watches are re-issued
				// by their owner.
				w.forgetRenamedWatched(event.Name)
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

// pathIsDirectory reports whether path currently refers to a directory; watched
// paths answer from the watch list without a syscall, a missing path is a file.
func (w *Watcher) pathIsDirectory(path string) bool {
	if path == "" {
		return false
	}
	w.mu.Lock()
	watched := w.watched[path]
	w.mu.Unlock()
	if watched {
		return true
	}
	info, err := os.Stat(path)
	return err == nil && info.IsDir()
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

// WatchTree recursively watches dir, marking it a "recursive root" whose
// later-created subdirectories are auto-added (fsnotify is not recursive, even
// on macOS) — for subtrees watched as a whole (e.g. .research) vs WatchDir's
// single non-recursive directory. Best-effort, idempotent, under-root only.
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
// watch list. The caller must hold w.mu; missing dirs are skipped (best-effort).
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

// maybeAutoAddDir adds a directory created inside a recursive root to the
// watch list (event loop, on Create) so a growing tree stays fully watched.
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
	// The new directory may already contain subdirectories; add them too.
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

// forgetRenamedWatched drops the watched entries a RENAMED directory left
// behind (itself plus watched descendants) — they short-circuit every
// re-registration path, so a directory recreated at the old path would never
// be re-watched (the Remove-side failure shape, review [71]). Unlike
// forgetWatchedDir it does NOT close fsnotify watches: fsnotify removes a
// moved directory's watch itself (IN_MOVE_SELF) at an unobservable ordering,
// and closing here would race that (see the event loop). No early return — a
// watched descendant can exist when the path itself never registered.
func (w *Watcher) forgetRenamedWatched(path string) {
	w.mu.Lock()
	defer w.mu.Unlock()
	for watched := range w.watched {
		within, withinErr := pathutil.IsWithinPath(path, watched)
		if withinErr != nil || !within {
			continue
		}
		delete(w.watched, watched)
	}
}

// forgetWatchedDir drops a DELETED directory and every watched descendant from
// the watch list so a tree recreated at the path is re-registered by the event
// loop's auto-add (review [71]): the Remove event names only the deleted
// directory, and stale descendant entries would short-circuit both registration
// paths forever. Best-effort — the kernel already dropped the watches, so the
// fsnotify Removes typically error (Debug, skipped); renames take
// forgetRenamedWatched (its doc explains the split).
func (w *Watcher) forgetWatchedDir(path string) {
	w.mu.Lock()
	defer w.mu.Unlock()
	// No early return when path itself never registered: descendants may have,
	// and their stale entries short-circuit re-registration too (review [71]).
	for watched := range w.watched {
		within, withinErr := pathutil.IsWithinPath(path, watched)
		if withinErr != nil || !within {
			continue
		}
		if rmErr := w.watcher.Remove(watched); rmErr != nil {
			w.log().Debug("watcher: failed to remove watch for deleted directory", "dir", watched, "error", rmErr)
		}
		delete(w.watched, watched)
	}
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

// UnwatchTree is the inverse of [Watcher.WatchTree]: it drops the root from
// recursiveRoots and unwatches every watched directory at or beneath it.
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

// AllowRoot admits an additional root for the under-root gate — for watched
// subtrees legitimately outside the primary root (a managed session worktree's
// .research tree while the watcher roots at the checkout). Idempotent.
func (w *Watcher) AllowRoot(path string) error {
	absPath, err := filepath.Abs(path)
	if err != nil {
		return fmt.Errorf("invalid path: %w", err)
	}
	w.mu.Lock()
	defer w.mu.Unlock()
	w.extraRoots[absPath] = true
	return nil
}

// isUnderRoot checks absPath against the root and any admitted extra root via
// the centralized containment primitive (symlink-escaped paths resolve
// consistently). It takes w.mu itself, so callers must not hold it.
func (w *Watcher) isUnderRoot(absPath string) bool {
	w.mu.Lock()
	defer w.mu.Unlock()
	if within, err := pathutil.IsWithinPath(w.root, absPath); err == nil && within {
		return true
	}
	for extra := range w.extraRoots {
		if within, err := pathutil.IsWithinPath(extra, absPath); err == nil && within {
			return true
		}
	}
	return false
}
