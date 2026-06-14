package tools

import (
	"context"
	"os"
	"sync"
)

// MapFileCoherenceChecker is an in-memory implementation of FileCoherenceChecker
// backed by sync.Mutex-protected maps. It tracks per-session file signatures
// (modification time + size) to detect cross-session and external file conflicts.
//
// Concurrency: all methods are safe for concurrent use.
type MapFileCoherenceChecker struct {
	mu       sync.Mutex
	sessions map[string]*sessionCoherence // sessionID → per-session state
	locks    map[string]*sync.Mutex       // per-file mutexes for TOCTOU guards
}

// sessionCoherence holds per-session file signature tracking.
type sessionCoherence struct {
	snapshots map[string]FileSig // path → last-read signature
}

// NewMapFileCoherenceChecker creates a new in-memory coherence checker.
func NewMapFileCoherenceChecker() *MapFileCoherenceChecker {
	return &MapFileCoherenceChecker{
		sessions: make(map[string]*sessionCoherence),
		locks:    make(map[string]*sync.Mutex),
	}
}

// CheckRead checks whether the file at path changed since this session last
// read it. Always updates the session's snapshot to the current on-disk state.
// Returns nil on first read or when the file has not changed.
func (c *MapFileCoherenceChecker) CheckRead(ctx context.Context, path string) *CoherenceConflict {
	sessionID := sessionIDFrom(ctx)
	if sessionID == "" {
		return nil
	}

	currSig, err := fileSig(path)
	if err != nil {
		return nil // file doesn't exist or is inaccessible — nothing to check
	}

	c.mu.Lock()
	sc, ok := c.sessions[sessionID]
	if !ok {
		sc = &sessionCoherence{snapshots: make(map[string]FileSig)}
		c.sessions[sessionID] = sc
	}
	prevSig, had := sc.snapshots[path]
	// Always update to current state
	sc.snapshots[path] = currSig
	c.mu.Unlock()

	if !had {
		return nil // first read — no conflict possible
	}
	if prevSig.ModTime.Equal(currSig.ModTime) && prevSig.Size == currSig.Size {
		return nil // unchanged
	}
	return &CoherenceConflict{
		Path:        path,
		LastReadSig: prevSig,
		CurrentSig:  currSig,
		ModifiedBy:  "external",
		ModifiedAt:  currSig.ModTime,
	}
}

// CheckWrite checks whether the file at path changed since this session last
// read it. Does NOT update the snapshot — the caller must call RecordWrite
// after a successful write. Returns nil if no prior read exists (new file case).
func (c *MapFileCoherenceChecker) CheckWrite(ctx context.Context, path string) *CoherenceConflict {
	sessionID := sessionIDFrom(ctx)
	if sessionID == "" {
		return nil
	}

	currSig, err := fileSig(path)
	if err != nil {
		return nil // file doesn't exist (yet) — new file, no conflict
	}

	c.mu.Lock()
	sc, ok := c.sessions[sessionID]
	if !ok {
		c.mu.Unlock()
		return nil // no prior reads in this session — new file, no conflict
	}
	prevSig, had := sc.snapshots[path]
	c.mu.Unlock()

	if !had {
		return nil // not previously read — no conflict
	}
	if prevSig.ModTime.Equal(currSig.ModTime) && prevSig.Size == currSig.Size {
		return nil // unchanged
	}
	return &CoherenceConflict{
		Path:        path,
		LastReadSig: prevSig,
		CurrentSig:  currSig,
		ModifiedBy:  "external",
		ModifiedAt:  currSig.ModTime,
	}
}

// RecordWrite updates the session's snapshot to the current on-disk state.
func (c *MapFileCoherenceChecker) RecordWrite(ctx context.Context, path string) {
	sessionID := sessionIDFrom(ctx)
	if sessionID == "" {
		return
	}

	currSig, err := fileSig(path)
	if err != nil {
		return
	}

	c.mu.Lock()
	defer c.mu.Unlock()
	sc, ok := c.sessions[sessionID]
	if !ok {
		sc = &sessionCoherence{snapshots: make(map[string]FileSig)}
		c.sessions[sessionID] = sc
	}
	sc.snapshots[path] = currSig
}

// RecordDelete removes all session snapshots for the given path.
func (c *MapFileCoherenceChecker) RecordDelete(ctx context.Context, path string) {
	c.mu.Lock()
	defer c.mu.Unlock()
	for _, sc := range c.sessions {
		delete(sc.snapshots, path)
	}
}

// PurgeSession removes all tracked state for a session.
func (c *MapFileCoherenceChecker) PurgeSession(sessionID string) {
	c.mu.Lock()
	defer c.mu.Unlock()
	delete(c.sessions, sessionID)
}

// Lock acquires a per-file mutex for the given path.
func (c *MapFileCoherenceChecker) Lock(path string) {
	c.mu.Lock()
	mu, ok := c.locks[path]
	if !ok {
		mu = &sync.Mutex{}
		c.locks[path] = mu
	}
	c.mu.Unlock()
	mu.Lock()
}

// Unlock releases the per-file mutex for the given path.
func (c *MapFileCoherenceChecker) Unlock(path string) {
	c.mu.Lock()
	mu, ok := c.locks[path]
	c.mu.Unlock()
	if ok {
		mu.Unlock()
	}
}

// sessionIDKey is the context key for the session ID used by coherence tracking.
type sessionIDKey struct{}

// WithCoherenceSessionID returns a context with the given session ID attached.
// FileCoherenceChecker methods use this to identify the current session.
func WithCoherenceSessionID(ctx context.Context, sessionID string) context.Context {
	return context.WithValue(ctx, sessionIDKey{}, sessionID)
}

// sessionIDFrom extracts the session ID from the context.
func sessionIDFrom(ctx context.Context) string {
	if v, ok := ctx.Value(sessionIDKey{}).(string); ok {
		return v
	}
	return ""
}

// fileSig reads the file at path and returns its modification time and size.
func fileSig(path string) (FileSig, error) {
	fi, err := os.Stat(path)
	if err != nil {
		return FileSig{}, err
	}
	return FileSig{
		ModTime: fi.ModTime(),
		Size:    fi.Size(),
	}, nil
}

// compile-time check
var _ FileCoherenceChecker = (*MapFileCoherenceChecker)(nil)
