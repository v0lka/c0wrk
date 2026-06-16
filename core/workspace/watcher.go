package workspace

import (
	"errors"
	"fmt"
	"log/slog"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"time"

	"github.com/fsnotify/fsnotify"
)

// defaultDebounce is the debounce interval for file system events.
// 200ms balances responsiveness with avoiding excessive re-processing
// of rapid consecutive file changes (e.g., editor save + format).
const defaultDebounce = 200 * time.Millisecond

// Watcher watches directories for file system changes and calls onChange when changes occur.
type Watcher struct {
	root      string
	watcher   *fsnotify.Watcher
	onChange  func()
	mu        sync.Mutex
	watched   map[string]bool
	done      chan struct{}
	closeOnce sync.Once
	logger    *slog.Logger
}

// log returns the logger, falling back to slog.Default() if nil.
func (w *Watcher) log() *slog.Logger {
	if w.logger != nil {
		return w.logger
	}
	return slog.Default()
}

// NewWatcher creates a Watcher that watches the root directory and calls onChange
// (debounced at 200ms) on any change.
func NewWatcher(root string, onChange func(), loggers ...*slog.Logger) (*Watcher, error) {
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
		root:     absRoot,
		watcher:  fsw,
		onChange: onChange,
		watched:  make(map[string]bool),
		done:     make(chan struct{}),
		logger:   logger,
	}

	// Watch the root directory itself.
	if err := fsw.Add(absRoot); err != nil {
		_ = fsw.Close()
		return nil, fmt.Errorf("failed to watch root directory: %w", err)
	}
	w.watched[absRoot] = true

	// Also watch the .git directory so that index changes (staging/unstaging)
	// trigger the same onChange callback used by the file tree.
	gitDir := filepath.Join(absRoot, ".git")
	if stat, err := os.Stat(gitDir); err == nil && stat.IsDir() {
		if err := fsw.Add(gitDir); err != nil {
			w.log().Debug("failed to watch .git directory", "error", err)
		}
	}

	go w.eventLoop()
	return w, nil
}

// eventLoop reads fsnotify events and debounces onChange calls.
func (w *Watcher) eventLoop() {
	var timer *time.Timer

	for {
		select {
		case <-w.done:
			if timer != nil {
				timer.Stop()
			}
			return
		case _, ok := <-w.watcher.Events:
			if !ok {
				return
			}
			if timer != nil {
				timer.Stop()
			}
			timer = time.AfterFunc(defaultDebounce, w.onChange)
		case watchErr, ok := <-w.watcher.Errors:
			if !ok {
				return
			}
			w.log().Debug("fsnotify watcher error", "error", watchErr)
		}
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
func (w *Watcher) isUnderRoot(absPath string) bool {
	return absPath == w.root || strings.HasPrefix(absPath, w.root+string(filepath.Separator))
}
