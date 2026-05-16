package tools

import (
	"context"
	"time"
)

// FileSig is a lightweight file signature based on modification time and size.
// Used as a fast proxy for content change detection (no hashing required).
type FileSig struct {
	ModTime time.Time
	Size    int64
}

// CoherenceConflict describes a detected file conflict between sessions.
type CoherenceConflict struct {
	Path        string
	LastReadSig FileSig
	CurrentSig  FileSig
	ModifiedBy  string    // session display name, or "external"
	ModifiedAt  time.Time // when the conflicting write occurred
}

// FileCoherenceChecker detects cross-session file conflicts by tracking
// per-session file signatures and comparing them before read/write operations.
type FileCoherenceChecker interface {
	// CheckRead checks whether the file at path changed since this session last
	// read it. Always updates the session's snapshot to the current on-disk state.
	// Returns nil on first read or when the file has not changed.
	CheckRead(ctx context.Context, path string) *CoherenceConflict

	// CheckWrite checks whether the file at path changed since this session last
	// read it. Does NOT update the snapshot — the caller must call RecordWrite
	// after a successful write. Returns nil if no prior read exists (new file case).
	CheckWrite(ctx context.Context, path string) *CoherenceConflict

	// RecordWrite updates the session's snapshot to the current on-disk state
	// and logs the write in the activity buffer.
	RecordWrite(ctx context.Context, path string)

	// RecordDelete removes all session snapshots for the given path and logs
	// the deletion in the activity buffer.
	RecordDelete(ctx context.Context, path string)

	// PurgeSession removes all tracked state for a session (called on session delete).
	PurgeSession(sessionID string)

	// Lock acquires a per-file mutex for the given path. Callers must hold this
	// lock across the check-then-act window to eliminate TOCTOU races.
	Lock(path string)

	// Unlock releases the per-file mutex for the given path.
	Unlock(path string)
}

// coherenceKey is the context key for the file coherence checker.
type coherenceKey struct{}

// WithCoherence returns a new context with the given FileCoherenceChecker attached.
func WithCoherence(ctx context.Context, checker FileCoherenceChecker) context.Context {
	return context.WithValue(ctx, coherenceKey{}, checker)
}

// CoherenceFrom extracts the FileCoherenceChecker from the context.
// Returns nil if no checker is available (e.g., in CLI mode or tests).
func CoherenceFrom(ctx context.Context) FileCoherenceChecker {
	if v, ok := ctx.Value(coherenceKey{}).(FileCoherenceChecker); ok {
		return v
	}
	return nil
}
