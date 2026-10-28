package session

import (
	"context"
	"os"
	"sync"
	"time"

	sdktools "github.com/v0lka/sp4rk/tools"
)

const defaultActivityCap = 200

type writeRecord struct {
	Path      string
	SessionID string
	At        time.Time
	Sig       sdktools.FileSig
}

// FileCoherenceTracker detects cross-session file conflicts by comparing
// per-session file signatures before read/write operations
// (specs/domains/session-lifecycle.md); it implements
// sdktools.FileCoherenceChecker. Lock ordering: t.mu BEFORE t.fileMu —
// pruneOrphanFileMutexesLocked relies on it (holds t.mu, takes t.fileMu).
type FileCoherenceTracker struct {
	mu           sync.RWMutex
	snapshots    map[string]map[string]sdktools.FileSig // sessionID -> path -> sig
	activity     []writeRecord                          // ring buffer of recent writes
	activityCap  int
	fileMutexes  map[string]*fileLockEntry
	fileMu       sync.Mutex // protects fileMutexes map
	nameResolver func(string) string
}

// fileLockEntry is a per-path mutex plus a refcount of every Lock (holder or
// waiter) that resolved it. A nonzero refcount covers the whole hold/wait
// window: pruning must never delete a pinned entry, because Unlock
// re-resolves the entry by path — a deleted entry would no-op the Unlock and
// strand the mutex locked (or break same-path exclusivity once a fresh entry
// is created).
type fileLockEntry struct {
	mu   sync.Mutex
	refs int
}

// NewFileCoherenceTracker creates a new tracker instance.
// nameResolver should return a human-readable session name given a session ID.
func NewFileCoherenceTracker(nameResolver func(string) string) *FileCoherenceTracker {
	return &FileCoherenceTracker{
		snapshots:    make(map[string]map[string]sdktools.FileSig),
		activity:     make([]writeRecord, 0, defaultActivityCap),
		activityCap:  defaultActivityCap,
		fileMutexes:  make(map[string]*fileLockEntry),
		nameResolver: nameResolver,
	}
}

// Lock acquires the per-file mutex for the given path.
func (t *FileCoherenceTracker) Lock(path string) {
	t.fileMu.Lock()
	e, ok := t.fileMutexes[path]
	if !ok {
		e = &fileLockEntry{}
		t.fileMutexes[path] = e
	}
	e.refs++
	t.fileMu.Unlock()
	e.mu.Lock()
}

// Unlock releases the per-file mutex for the given path; unmatched Unlock
// calls (no prior Lock) are ignored.
func (t *FileCoherenceTracker) Unlock(path string) {
	t.fileMu.Lock()
	e, ok := t.fileMutexes[path]
	if ok {
		if e.refs > 0 {
			e.refs--
		}
		e.mu.Unlock()
	}
	t.fileMu.Unlock()
}

// CheckRead checks whether the file changed since this session last read it,
// updating the session's snapshot; nil on first read or unchanged.
func (t *FileCoherenceTracker) CheckRead(ctx context.Context, path string) *sdktools.CoherenceConflict {
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

	modifiedBy, modifiedAt := t.findLastWriter(path, sessionID)

	return &sdktools.CoherenceConflict{
		Path:        path,
		LastReadSig: prevSig,
		CurrentSig:  currentSig,
		ModifiedBy:  modifiedBy,
		ModifiedAt:  modifiedAt,
	}
}

// CheckWrite checks if the file at path changed since this session last read it.
// Does NOT update the snapshot. Returns nil if no prior read exists.
func (t *FileCoherenceTracker) CheckWrite(ctx context.Context, path string) *sdktools.CoherenceConflict {
	sessionID := SessionIDFromContext(ctx)
	if sessionID == "" {
		return nil
	}

	t.mu.RLock()
	prevSig, hasPrev := t.getSnapshot(sessionID, path)
	t.mu.RUnlock()

	if !hasPrev {
		return nil
	}

	currentSig, err := statFile(path)
	if err != nil {
		// File was deleted externally since session last read it.
		modifiedBy, modifiedAt := t.findLastWriterLocked(path, sessionID)
		return &sdktools.CoherenceConflict{
			Path:        path,
			LastReadSig: prevSig,
			CurrentSig:  sdktools.FileSig{},
			ModifiedBy:  modifiedBy,
			ModifiedAt:  modifiedAt,
		}
	}

	if sigEqual(prevSig, currentSig) {
		return nil
	}

	modifiedBy, modifiedAt := t.findLastWriterLocked(path, sessionID)
	return &sdktools.CoherenceConflict{
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

	// Drop per-path mutex entries that no surviving session references. Keeps
	// fileMutexes from growing without bound across long-lived runs (W-25).
	t.pruneOrphanFileMutexesLocked()
}

// pruneOrphanFileMutexesLocked removes per-path mutexes referenced by no
// session's snapshot map or activity entry; pinned entries (refs > 0 — see
// fileLockEntry) survive. The caller must hold t.mu.
func (t *FileCoherenceTracker) pruneOrphanFileMutexesLocked() {
	t.fileMu.Lock()
	defer t.fileMu.Unlock()
	if len(t.fileMutexes) == 0 {
		return
	}
	referenced := make(map[string]struct{}, len(t.fileMutexes))
	for _, snap := range t.snapshots {
		for path := range snap {
			referenced[path] = struct{}{}
		}
	}
	for _, r := range t.activity {
		referenced[r.Path] = struct{}{}
	}
	for path, e := range t.fileMutexes {
		if _, ok := referenced[path]; ok {
			continue
		}
		if e.refs > 0 {
			continue
		}
		delete(t.fileMutexes, path)
	}
}

// --- internal helpers ---

func (t *FileCoherenceTracker) getSnapshot(sessionID, path string) (sdktools.FileSig, bool) {
	m, ok := t.snapshots[sessionID]
	if !ok {
		return sdktools.FileSig{}, false
	}
	sig, ok := m[path]
	return sig, ok
}

func (t *FileCoherenceTracker) setSnapshot(sessionID, path string, sig sdktools.FileSig) {
	m, ok := t.snapshots[sessionID]
	if !ok {
		m = make(map[string]sdktools.FileSig)
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

func statFile(path string) (sdktools.FileSig, error) {
	info, err := os.Stat(path)
	if err != nil {
		return sdktools.FileSig{}, err
	}
	return sdktools.FileSig{
		ModTime: info.ModTime(),
		Size:    info.Size(),
	}, nil
}

func sigEqual(a, b sdktools.FileSig) bool {
	return a.Size == b.Size && a.ModTime.Equal(b.ModTime)
}
