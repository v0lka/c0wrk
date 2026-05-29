package session

import (
	"context"
	"os"
	"sync"
	"time"

	"github.com/v0lka/c0wrk/core/tools"
)

const defaultActivityCap = 200

// writeRecord tracks a single write operation for the activity log.
type writeRecord struct {
	Path      string
	SessionID string
	At        time.Time
	Sig       tools.FileSig
}

// FileCoherenceTracker detects cross-session file conflicts by tracking
// per-session file signatures and comparing them before read/write operations.
// It implements tools.FileCoherenceChecker.
type FileCoherenceTracker struct {
	mu           sync.RWMutex
	snapshots    map[string]map[string]tools.FileSig // sessionID -> path -> sig
	activity     []writeRecord                       // ring buffer of recent writes
	activityCap  int
	fileMutexes  map[string]*sync.Mutex
	fileMu       sync.Mutex // protects fileMutexes map
	nameResolver func(string) string
}

// NewFileCoherenceTracker creates a new tracker instance.
// nameResolver should return a human-readable session name given a session ID.
func NewFileCoherenceTracker(nameResolver func(string) string) *FileCoherenceTracker {
	return &FileCoherenceTracker{
		snapshots:    make(map[string]map[string]tools.FileSig),
		activity:     make([]writeRecord, 0, defaultActivityCap),
		activityCap:  defaultActivityCap,
		fileMutexes:  make(map[string]*sync.Mutex),
		nameResolver: nameResolver,
	}
}

// Lock acquires a per-file mutex for the given path.
func (t *FileCoherenceTracker) Lock(path string) {
	t.fileMu.Lock()
	mu, ok := t.fileMutexes[path]
	if !ok {
		mu = &sync.Mutex{}
		t.fileMutexes[path] = mu
	}
	t.fileMu.Unlock()
	mu.Lock()
}

// Unlock releases the per-file mutex for the given path.
func (t *FileCoherenceTracker) Unlock(path string) {
	t.fileMu.Lock()
	mu, ok := t.fileMutexes[path]
	t.fileMu.Unlock()
	if ok {
		mu.Unlock()
	}
}

// CheckRead checks if the file at path changed since this session last read it.
// Always updates the session's snapshot to the current on-disk state.
// Returns nil on first read or when the file has not changed.
func (t *FileCoherenceTracker) CheckRead(ctx context.Context, path string) *tools.CoherenceConflict {
	sessionID := SessionIDFromContext(ctx)
	if sessionID == "" {
		return nil
	}

	currentSig, err := statFile(path)
	if err != nil {
		// File doesn't exist or can't be stat'd — no conflict on read
		// (read_file will handle the error itself)
		t.mu.Lock()
		t.removeSnapshot(sessionID, path)
		t.mu.Unlock()
		return nil
	}

	t.mu.Lock()
	defer t.mu.Unlock()

	prevSig, hasPrev := t.getSnapshot(sessionID, path)
	t.setSnapshot(sessionID, path, currentSig)

	if !hasPrev || sigEqual(prevSig, currentSig) {
		return nil
	}

	// File changed since last read — find who modified it
	modifiedBy, modifiedAt := t.findLastWriter(path, sessionID)

	return &tools.CoherenceConflict{
		Path:        path,
		LastReadSig: prevSig,
		CurrentSig:  currentSig,
		ModifiedBy:  modifiedBy,
		ModifiedAt:  modifiedAt,
	}
}

// CheckWrite checks if the file at path changed since this session last read it.
// Does NOT update the snapshot. Returns nil if no prior read exists.
func (t *FileCoherenceTracker) CheckWrite(ctx context.Context, path string) *tools.CoherenceConflict {
	sessionID := SessionIDFromContext(ctx)
	if sessionID == "" {
		return nil
	}

	t.mu.RLock()
	prevSig, hasPrev := t.getSnapshot(sessionID, path)
	t.mu.RUnlock()

	if !hasPrev {
		// No prior read — session has no stale knowledge to conflict on.
		return nil
	}

	currentSig, err := statFile(path)
	if err != nil {
		// File was deleted externally since session last read it.
		modifiedBy, modifiedAt := t.findLastWriterLocked(path, sessionID)
		return &tools.CoherenceConflict{
			Path:        path,
			LastReadSig: prevSig,
			CurrentSig:  tools.FileSig{},
			ModifiedBy:  modifiedBy,
			ModifiedAt:  modifiedAt,
		}
	}

	if sigEqual(prevSig, currentSig) {
		return nil
	}

	modifiedBy, modifiedAt := t.findLastWriterLocked(path, sessionID)
	return &tools.CoherenceConflict{
		Path:        path,
		LastReadSig: prevSig,
		CurrentSig:  currentSig,
		ModifiedBy:  modifiedBy,
		ModifiedAt:  modifiedAt,
	}
}

// RecordWrite updates the session's snapshot and logs the write in the activity buffer.
func (t *FileCoherenceTracker) RecordWrite(ctx context.Context, path string) {
	sessionID := SessionIDFromContext(ctx)
	if sessionID == "" {
		return
	}

	currentSig, err := statFile(path)
	if err != nil {
		// Write may have failed or file was immediately removed; clear snapshot.
		t.mu.Lock()
		t.removeSnapshot(sessionID, path)
		t.mu.Unlock()
		return
	}

	t.mu.Lock()
	defer t.mu.Unlock()

	t.setSnapshot(sessionID, path, currentSig)
	t.appendActivity(writeRecord{
		Path:      path,
		SessionID: sessionID,
		At:        time.Now(),
		Sig:       currentSig,
	})
}

// RecordDelete removes all session snapshots for the given path and logs the deletion.
func (t *FileCoherenceTracker) RecordDelete(ctx context.Context, path string) {
	sessionID := SessionIDFromContext(ctx)
	if sessionID == "" {
		return
	}

	t.mu.Lock()
	defer t.mu.Unlock()

	// Remove path from all sessions' snapshots.
	for sid := range t.snapshots {
		delete(t.snapshots[sid], path)
	}

	t.appendActivity(writeRecord{
		Path:      path,
		SessionID: sessionID,
		At:        time.Now(),
	})
}

// PurgeSession removes all tracked state for a session.
func (t *FileCoherenceTracker) PurgeSession(sessionID string) {
	t.mu.Lock()
	defer t.mu.Unlock()
	delete(t.snapshots, sessionID)

	// Remove activity entries for this session.
	filtered := t.activity[:0]
	for _, r := range t.activity {
		if r.SessionID != sessionID {
			filtered = append(filtered, r)
		}
	}
	t.activity = filtered
}

// --- internal helpers ---

func (t *FileCoherenceTracker) getSnapshot(sessionID, path string) (tools.FileSig, bool) {
	m, ok := t.snapshots[sessionID]
	if !ok {
		return tools.FileSig{}, false
	}
	sig, ok := m[path]
	return sig, ok
}

func (t *FileCoherenceTracker) setSnapshot(sessionID, path string, sig tools.FileSig) {
	m, ok := t.snapshots[sessionID]
	if !ok {
		m = make(map[string]tools.FileSig)
		t.snapshots[sessionID] = m
	}
	m[path] = sig
}

func (t *FileCoherenceTracker) removeSnapshot(sessionID, path string) {
	if m, ok := t.snapshots[sessionID]; ok {
		delete(m, path)
	}
}

func (t *FileCoherenceTracker) appendActivity(r writeRecord) {
	if len(t.activity) >= t.activityCap {
		// Drop oldest entry.
		copy(t.activity, t.activity[1:])
		t.activity = t.activity[:len(t.activity)-1]
	}
	t.activity = append(t.activity, r)
}

// findLastWriter searches the activity log for the most recent write to path
// by a session other than excludeID. Caller must hold t.mu (any lock level).
func (t *FileCoherenceTracker) findLastWriter(path, excludeID string) (name string, at time.Time) {
	for i := len(t.activity) - 1; i >= 0; i-- {
		r := t.activity[i]
		if r.Path == path && r.SessionID != excludeID {
			resolved := r.SessionID
			if t.nameResolver != nil {
				resolved = t.nameResolver(r.SessionID)
			}
			return resolved, r.At
		}
	}
	return "external", time.Now()
}

// findLastWriterLocked acquires a read lock and searches activity.
func (t *FileCoherenceTracker) findLastWriterLocked(path, excludeID string) (string, time.Time) {
	t.mu.RLock()
	defer t.mu.RUnlock()
	return t.findLastWriter(path, excludeID)
}

func statFile(path string) (tools.FileSig, error) {
	info, err := os.Stat(path)
	if err != nil {
		return tools.FileSig{}, err
	}
	return tools.FileSig{
		ModTime: info.ModTime(),
		Size:    info.Size(),
	}, nil
}

func sigEqual(a, b tools.FileSig) bool {
	return a.Size == b.Size && a.ModTime.Equal(b.ModTime)
}
