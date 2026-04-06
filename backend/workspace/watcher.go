package workspace

import (
	"errors"
	"fmt"
	"path/filepath"
	"strings"
	"sync"
	"time"

	"github.com/fsnotify/fsnotify"
)

// Watcher watches directories for file system changes and calls onChange when changes occur.
type Watcher struct {
	root     string
	watcher  *fsnotify.Watcher
	onChange func()
	mu       sync.Mutex
	watched  map[string]bool
	done     chan struct{}
}

// NewWatcher creates a Watcher that watches the root directory and calls onChange
// (debounced at 200ms) on any change.
func NewWatcher(root string, onChange func()) (*Watcher, error) {
	absRoot, err := filepath.Abs(root)
	if err != nil {
		return nil, fmt.Errorf("failed to resolve root path: %w", err)
	}

	fsw, err := fsnotify.NewWatcher()
	if err != nil {
		return nil, fmt.Errorf("failed to create fsnotify watcher: %w", err)
	}

	w := &Watcher{
		root:     absRoot,
		watcher:  fsw,
		onChange: onChange,
		watched:  make(map[string]bool),
		done:     make(chan struct{}),
	}

	// Watch the root directory itself.
	if err := fsw.Add(absRoot); err != nil {
		_ = fsw.Close()
		return nil, fmt.Errorf("failed to watch root directory: %w", err)
	}
	w.watched[absRoot] = true

	go w.eventLoop()
	return w, nil
}

// eventLoop reads fsnotify events and debounces onChange calls.
func (w *Watcher) eventLoop() {
	var timer *time.Timer
	const debounce = 200 * time.Millisecond

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
			timer = time.AfterFunc(debounce, w.onChange)
		case _, ok := <-w.watcher.Errors:
			if !ok {
				return
			}
			// Errors are silently ignored; callers rely on onChange notifications.
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

// Close stops watching and releases resources.
func (w *Watcher) Close() error {
	select {
	case <-w.done:
		// already closed
		return nil
	default:
		close(w.done)
	}
	return w.watcher.Close()
}

// isUnderRoot checks whether absPath is under (or equal to) the root directory.
func (w *Watcher) isUnderRoot(absPath string) bool {
	return absPath == w.root || strings.HasPrefix(absPath, w.root+string(filepath.Separator))
}
