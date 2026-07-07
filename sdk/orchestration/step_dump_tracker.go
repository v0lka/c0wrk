package orchestration

import (
	"errors"
	"io"
	"log/slog"
	"os"
	"path/filepath"
	"sync"
)

// StepDumpTracker manages per-step LLM dump files. It is safe for concurrent use.
type StepDumpTracker struct {
	mu     sync.Mutex
	dir    string
	files  map[string]*os.File
	closed bool
}

// NewStepDumpTracker creates a tracker for per-step dump files.
// Creates dir via os.MkdirAll; logs a warning on failure but does not
// return an error (dumps are best-effort debugging aids).
func NewStepDumpTracker(dir string) *StepDumpTracker {
	if err := os.MkdirAll(dir, 0o755); err != nil {
		slog.Default().Warn("step_dump_tracker: failed to create directory",
			"dir", dir, "error", err)
	}
	return &StepDumpTracker{
		dir:   dir,
		files: make(map[string]*os.File),
	}
}

// OpenStepDump returns an io.Writer for the step's dump file.
// The file is created/opened on first call and cached for subsequent calls
// (idempotent — retries append to the same file).
// Returns nil if the tracker is disabled (empty dir), closed, or file creation fails.
func (t *StepDumpTracker) OpenStepDump(stepID string) io.Writer {
	if t.dir == "" {
		return nil
	}
	// Sanitize stepID to prevent path traversal: strip any directory
	// components so the filename stays within t.dir.
	filename := "step_" + filepath.Base(stepID) + ".jsonl"

	t.mu.Lock()
	defer t.mu.Unlock()

	if t.closed {
		return nil
	}
	if f, ok := t.files[filename]; ok {
		return f
	}
	f, err := os.OpenFile(filepath.Join(t.dir, filename),
		os.O_CREATE|os.O_WRONLY|os.O_APPEND, 0o644)
	if err != nil {
		slog.Default().Warn("step_dump_tracker: failed to open dump file",
			"filename", filename, "error", err)
		return nil
	}
	t.files[filename] = f
	return f
}

// CloseAll closes all tracked dump files. Idempotent — safe to call multiple times.
func (t *StepDumpTracker) CloseAll() error {
	t.mu.Lock()
	defer t.mu.Unlock()

	t.closed = true
	var errs []error
	for name, f := range t.files {
		if err := f.Close(); err != nil {
			errs = append(errs, err)
		}
		delete(t.files, name)
	}
	return errors.Join(errs...)
}
