package agent

import (
	"context"
	"fmt"
	"io/fs"
	"log/slog"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"sync"
	"time"

	"github.com/sergi/go-diff/diffmatchpatch"
)

// fileBaseline stores the original content of a file before any modifications.
type fileBaseline struct {
	content []byte
	existed bool // false if file didn't exist before session
}

// trackedOp records a single file operation within a step.
type trackedOp struct {
	path      string
	operation string // CREATE, MODIFY, DELETE
	timestamp time.Time
}

// fileInfo holds metadata about a file for snapshot comparison.
type fileInfo struct {
	modTime time.Time
	size    int64
}

// FileChangeTracker tracks file modifications made by agent steps,
// supports per-step rollback and session-level aggregation.
type FileChangeTracker struct {
	mu            sync.RWMutex
	workspaceRoot string
	fileLocks     map[string]*sync.Mutex
	baselines     map[string]*fileBaseline
	ops           map[string][]trackedOp // keyed by stepID
	logger        *slog.Logger
}

// NewFileChangeTracker creates a new FileChangeTracker for the given workspace root.
func NewFileChangeTracker(workspaceRoot string) *FileChangeTracker {
	return &FileChangeTracker{
		workspaceRoot: workspaceRoot,
		fileLocks:     make(map[string]*sync.Mutex),
		baselines:     make(map[string]*fileBaseline),
		ops:           make(map[string][]trackedOp),
	}
}

// SetLogger sets the logger for the file change tracker.
func (t *FileChangeTracker) SetLogger(l *slog.Logger) { t.logger = l }

func (t *FileChangeTracker) log() *slog.Logger {
	if t.logger != nil {
		return t.logger
	}
	return slog.Default()
}

// relPath converts an absolute path to a relative path from the workspace root.
func (t *FileChangeTracker) relPath(absPath string) string {
	rel, err := filepath.Rel(t.workspaceRoot, absPath)
	if err != nil {
		return absPath
	}
	return rel
}

// getFileLock returns (or creates) a per-file mutex for the given relative path.
func (t *FileChangeTracker) getFileLock(rel string) *sync.Mutex {
	t.mu.Lock()
	defer t.mu.Unlock()
	m, ok := t.fileLocks[rel]
	if !ok {
		m = &sync.Mutex{}
		t.fileLocks[rel] = m
	}
	return m
}

// AcquireFileLock locks the per-file mutex for absPath.
func (t *FileChangeTracker) AcquireFileLock(absPath string) {
	rel := t.relPath(absPath)
	t.getFileLock(rel).Lock()
}

// ReleaseFileLock unlocks the per-file mutex for absPath.
func (t *FileChangeTracker) ReleaseFileLock(absPath string) {
	rel := t.relPath(absPath)
	t.getFileLock(rel).Unlock()
}

// RecordBeforeWrite captures the baseline content of a file before modification.
// If the file has already been baselined, this is a no-op.
func (t *FileChangeTracker) RecordBeforeWrite(ctx context.Context, absPath string) {
	rel := t.relPath(absPath)

	t.mu.RLock()
	_, exists := t.baselines[rel]
	t.mu.RUnlock()
	if exists {
		return
	}

	bl := &fileBaseline{}
	data, err := os.ReadFile(absPath)
	if err != nil {
		bl.existed = false
	} else {
		bl.existed = true
		bl.content = data
	}

	t.mu.Lock()
	// Double-check after acquiring write lock.
	if _, exists := t.baselines[rel]; !exists {
		t.baselines[rel] = bl
	}
	t.mu.Unlock()
}

// RecordAfterWrite records a write operation (CREATE or MODIFY) for the current step.
func (t *FileChangeTracker) RecordAfterWrite(ctx context.Context, absPath string) {
	rel := t.relPath(absPath)
	stepID := StepIDFromContext(ctx)

	t.mu.RLock()
	bl := t.baselines[rel]
	t.mu.RUnlock()

	op := "MODIFY"
	if bl != nil && !bl.existed {
		op = "CREATE"
	}

	t.mu.Lock()
	t.ops[stepID] = append(t.ops[stepID], trackedOp{
		path:      rel,
		operation: op,
		timestamp: time.Now(),
	})
	t.mu.Unlock()
}

// RecordDelete captures the baseline (if not already captured) and records a DELETE operation.
func (t *FileChangeTracker) RecordDelete(ctx context.Context, absPath string) {
	t.RecordBeforeWrite(ctx, absPath)
	rel := t.relPath(absPath)
	stepID := StepIDFromContext(ctx)

	t.mu.Lock()
	t.ops[stepID] = append(t.ops[stepID], trackedOp{
		path:      rel,
		operation: "DELETE",
		timestamp: time.Now(),
	})
	t.mu.Unlock()
}

// GetStepChanges returns all file changes for a given step.
func (t *FileChangeTracker) GetStepChanges(stepID string) []FileChange {
	t.mu.RLock()
	stepOps := t.ops[stepID]
	t.mu.RUnlock()

	changes := make([]FileChange, 0, len(stepOps))
	for _, op := range stepOps {
		fc := FileChange{
			Path:      op.path,
			Operation: op.operation,
		}
		absPath := filepath.Join(t.workspaceRoot, op.path)
		switch op.operation {
		case "CREATE":
			if info, err := os.Stat(absPath); err == nil {
				fc.SizeBytes = info.Size()
			}
		case "MODIFY":
			t.mu.RLock()
			bl := t.baselines[op.path]
			t.mu.RUnlock()
			if bl != nil {
				current, err := os.ReadFile(absPath)
				if err == nil {
					fc.Diff = computeDiff(bl.content, current, op.path)
					fc.SizeBytes = int64(len(current))
				}
			}
		case "DELETE":
			fc.SizeBytes = 0
		}
		changes = append(changes, fc)
	}
	return changes
}

// GetSessionChanges returns an aggregated view of all file changes across all steps.
// Each unique path produces at most one FileChange, comparing baseline to current state.
func (t *FileChangeTracker) GetSessionChanges() []FileChange {
	t.mu.RLock()
	// Collect all unique paths that had operations.
	pathSet := make(map[string]struct{})
	for _, ops := range t.ops {
		for _, op := range ops {
			pathSet[op.path] = struct{}{}
		}
	}

	// Snapshot baselines we need.
	baselinesCopy := make(map[string]*fileBaseline, len(pathSet))
	for p := range pathSet {
		if bl, ok := t.baselines[p]; ok {
			baselinesCopy[p] = bl
		}
	}
	t.mu.RUnlock()

	// Sort paths for deterministic output.
	paths := make([]string, 0, len(pathSet))
	for p := range pathSet {
		paths = append(paths, p)
	}
	sort.Strings(paths)

	var changes []FileChange
	for _, rel := range paths {
		bl := baselinesCopy[rel]
		absPath := filepath.Join(t.workspaceRoot, rel)

		currentExists := false
		var currentSize int64
		var currentContent []byte
		if info, err := os.Stat(absPath); err == nil {
			currentExists = true
			currentSize = info.Size()
			currentContent, _ = os.ReadFile(absPath)
		}

		existed := bl != nil && bl.existed

		switch {
		case !existed && !currentExists:
			// Created then deleted — omit.
			continue
		case !existed && currentExists:
			changes = append(changes, FileChange{
				Path:      rel,
				Operation: "CREATE",
				SizeBytes: currentSize,
			})
		case existed && !currentExists:
			changes = append(changes, FileChange{
				Path:      rel,
				Operation: "DELETE",
				SizeBytes: 0,
			})
		case existed && currentExists:
			diff := computeDiff(bl.content, currentContent, rel)
			if diff == "" {
				// No effective change.
				continue
			}
			changes = append(changes, FileChange{
				Path:      rel,
				Operation: "MODIFY",
				Diff:      diff,
				SizeBytes: currentSize,
			})
		}
	}
	return changes
}

// RollbackStep reverts all file operations from the given step.
func (t *FileChangeTracker) RollbackStep(stepID string) error {
	t.mu.RLock()
	stepOps := make([]trackedOp, len(t.ops[stepID]))
	copy(stepOps, t.ops[stepID])
	t.mu.RUnlock()

	var errs []string
	// Rollback in reverse order.
	for i := len(stepOps) - 1; i >= 0; i-- {
		op := stepOps[i]
		absPath := filepath.Join(t.workspaceRoot, op.path)

		t.mu.RLock()
		bl := t.baselines[op.path]
		t.mu.RUnlock()

		switch op.operation {
		case "CREATE":
			if err := os.Remove(absPath); err != nil && !os.IsNotExist(err) {
				errs = append(errs, fmt.Sprintf("rollback CREATE %s: %v", op.path, err))
			}
		case "MODIFY":
			if bl != nil {
				if err := os.WriteFile(absPath, bl.content, 0o644); err != nil {
					errs = append(errs, fmt.Sprintf("rollback MODIFY %s: %v", op.path, err))
				}
			}
		case "DELETE":
			if bl != nil && bl.existed {
				dir := filepath.Dir(absPath)
				if err := os.MkdirAll(dir, 0o755); err != nil {
					errs = append(errs, fmt.Sprintf("rollback DELETE mkdir %s: %v", op.path, err))
					continue
				}
				if err := os.WriteFile(absPath, bl.content, 0o644); err != nil {
					errs = append(errs, fmt.Sprintf("rollback DELETE %s: %v", op.path, err))
				}
			}
		}
	}

	// Remove the step from ops.
	t.mu.Lock()
	delete(t.ops, stepID)
	t.mu.Unlock()

	if len(errs) > 0 {
		return fmt.Errorf("rollback errors: %s", strings.Join(errs, "; "))
	}
	return nil
}

// RollbackAll restores all files to their baseline state.
func (t *FileChangeTracker) RollbackAll() error {
	t.mu.RLock()
	baselinesCopy := make(map[string]*fileBaseline, len(t.baselines))
	for k, v := range t.baselines {
		baselinesCopy[k] = v
	}
	t.mu.RUnlock()

	var errs []string
	for rel, bl := range baselinesCopy {
		absPath := filepath.Join(t.workspaceRoot, rel)
		if bl.existed {
			dir := filepath.Dir(absPath)
			if err := os.MkdirAll(dir, 0o755); err != nil {
				errs = append(errs, fmt.Sprintf("rollback mkdir %s: %v", rel, err))
				continue
			}
			if err := os.WriteFile(absPath, bl.content, 0o644); err != nil {
				errs = append(errs, fmt.Sprintf("rollback %s: %v", rel, err))
			}
		} else {
			// File was created during session — remove it.
			if err := os.Remove(absPath); err != nil && !os.IsNotExist(err) {
				errs = append(errs, fmt.Sprintf("rollback remove %s: %v", rel, err))
			}
		}
	}

	// Clear ops.
	t.mu.Lock()
	t.ops = make(map[string][]trackedOp)
	t.mu.Unlock()

	if len(errs) > 0 {
		return fmt.Errorf("rollback errors: %s", strings.Join(errs, "; "))
	}
	return nil
}

// skipDirs is the set of directory names to skip during workspace snapshot.
var skipDirs = map[string]bool{
	".git":         true,
	"node_modules": true,
	".cache":       true,
	"__pycache__":  true,
	"vendor":       true,
}

// skipFiles is the set of file names to skip during workspace snapshot.
var skipFiles = map[string]bool{
	".DS_Store": true,
}

// WorkspaceSnapshot is an opaque snapshot of the workspace file tree.
// It is produced by TakeSnapshot and consumed by DetectChangesFrom.
type WorkspaceSnapshot struct {
	files map[string]fileInfo
}

// TakeSnapshot captures the current workspace file tree for later comparison.
func (t *FileChangeTracker) TakeSnapshot() *WorkspaceSnapshot {
	return &WorkspaceSnapshot{files: t.SnapshotWorkspace()}
}

// DetectChangesFrom compares the current workspace state against a previous
// snapshot and records any detected file operations for the current step.
func (t *FileChangeTracker) DetectChangesFrom(ctx context.Context, snap *WorkspaceSnapshot) {
	if snap == nil {
		return
	}
	t.DetectChanges(ctx, snap.files)
}

// SnapshotWorkspace walks the workspace and returns a map of relative path to file metadata.
func (t *FileChangeTracker) SnapshotWorkspace() map[string]fileInfo {
	snapshot := make(map[string]fileInfo)
	_ = filepath.WalkDir(t.workspaceRoot, func(path string, d fs.DirEntry, err error) error {
		if err != nil {
			t.log().Debug("file walk: skipping entry", "path", path, "error", err)
			return nil //nolint:nilerr // intentionally skip unreadable files/dirs during walk
		}
		if d.IsDir() {
			if skipDirs[d.Name()] {
				return filepath.SkipDir
			}
			return nil
		}
		if !d.Type().IsRegular() {
			return nil
		}
		if skipFiles[d.Name()] {
			return nil
		}
		rel, err := filepath.Rel(t.workspaceRoot, path)
		if err != nil {
			t.log().Debug("file walk: skipping entry", "path", path, "error", err)
			return nil //nolint:nilerr // intentionally skip unreadable files/dirs during walk
		}
		info, err := d.Info()
		if err != nil {
			t.log().Debug("file walk: skipping entry", "path", path, "error", err)
			return nil //nolint:nilerr // intentionally skip unreadable files/dirs during walk
		}
		snapshot[rel] = fileInfo{
			modTime: info.ModTime(),
			size:    info.Size(),
		}
		return nil
	})
	return snapshot
}

// DetectChanges compares the current workspace state against a previous snapshot
// and records any detected file operations for the current step.
func (t *FileChangeTracker) DetectChanges(ctx context.Context, before map[string]fileInfo) {
	after := t.SnapshotWorkspace()

	// Detect new or changed files.
	for rel, afterInfo := range after {
		absPath := filepath.Join(t.workspaceRoot, rel)
		beforeInfo, existed := before[rel]
		if !existed {
			// New file — force a non-existent baseline so it's recorded as CREATE.
			t.mu.Lock()
			if _, has := t.baselines[rel]; !has {
				t.baselines[rel] = &fileBaseline{existed: false}
			}
			t.mu.Unlock()
			t.RecordAfterWrite(ctx, absPath)
		} else if afterInfo.modTime != beforeInfo.modTime || afterInfo.size != beforeInfo.size {
			// Changed file — MODIFY.
			t.RecordBeforeWrite(ctx, absPath)
			t.RecordAfterWrite(ctx, absPath)
		}
	}

	// Detect deleted files.
	for rel := range before {
		if _, exists := after[rel]; !exists {
			absPath := filepath.Join(t.workspaceRoot, rel)
			t.RecordDelete(ctx, absPath)
		}
	}
}

// computeDiff produces a unified diff between old and new content.
func computeDiff(oldContent, newContent []byte, path string) string {
	oldStr := string(oldContent)
	newStr := string(newContent)

	if oldStr == newStr {
		return ""
	}

	dmp := diffmatchpatch.New()
	a, b, c := dmp.DiffLinesToChars(oldStr, newStr)
	diffs := dmp.DiffMain(a, b, false)
	diffs = dmp.DiffCharsToLines(diffs, c)
	diffs = dmp.DiffCleanupSemantic(diffs)

	var buf strings.Builder
	fmt.Fprintf(&buf, "--- a/%s\n", path)
	fmt.Fprintf(&buf, "+++ b/%s\n", path)

	for _, d := range diffs {
		lines := strings.Split(d.Text, "\n")
		// Remove trailing empty string from split.
		if len(lines) > 0 && lines[len(lines)-1] == "" {
			lines = lines[:len(lines)-1]
		}
		switch d.Type {
		case diffmatchpatch.DiffDelete:
			for _, l := range lines {
				buf.WriteString("-" + l + "\n")
			}
		case diffmatchpatch.DiffInsert:
			for _, l := range lines {
				buf.WriteString("+" + l + "\n")
			}
		case diffmatchpatch.DiffEqual:
			for _, l := range lines {
				buf.WriteString(" " + l + "\n")
			}
		}
	}
	return buf.String()
}

// --- Context injection helpers ---

type fileTrackerKey struct{}

// WithFileTracker returns a new context with the FileChangeTracker attached.
func WithFileTracker(ctx context.Context, tracker *FileChangeTracker) context.Context {
	return context.WithValue(ctx, fileTrackerKey{}, tracker)
}

// FileTrackerFromContext extracts the FileChangeTracker from the context.
// Returns nil if not found.
func FileTrackerFromContext(ctx context.Context) *FileChangeTracker {
	if t, ok := ctx.Value(fileTrackerKey{}).(*FileChangeTracker); ok {
		return t
	}
	return nil
}

type stepIDKey struct{}

// WithStepID returns a new context with the step ID attached.
func WithStepID(ctx context.Context, id string) context.Context {
	return context.WithValue(ctx, stepIDKey{}, id)
}

// StepIDFromContext extracts the step ID from the context.
// Returns empty string if not found.
func StepIDFromContext(ctx context.Context) string {
	if id, ok := ctx.Value(stepIDKey{}).(string); ok {
		return id
	}
	return ""
}
