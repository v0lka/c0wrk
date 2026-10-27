package session

import (
	"errors"
	"fmt"
	"os"
	"path/filepath"

	"github.com/v0lka/c0wrk/backend/config"
	"github.com/v0lka/c0wrk/backend/project"
	"github.com/v0lka/sp4rk/orchestration"
)

// Drops the in-memory session without touching on-disk state (handles
// closed, trackers purged, active task/compaction refused, restore waited
// out); the next GetSession re-restores, picking up the caller's project
// change. See specs/domains/session-lifecycle.md; no-capture wrapper:
// EvictSessionCarryingAttachments.
func (m *Manager) EvictSession(id string) error {
	_, err := m.EvictSessionCarryingAttachments(id)
	return err
}

// Evicts like EvictSession AND returns the staged attachments, captured
// under the same critical section that deletes the session: a racing
// staging append either commits before the capture or re-stages on the
// restored session — no interleaving loses the file.
func (m *Manager) EvictSessionCarryingAttachments(id string) (*PendingAttachmentSnapshot, error) {
	if id == "" {
		return nil, errors.New("session id is required")
	}
	// Wait out any in-flight restore, then evict under the write lock; the
	// loop re-checks (a new restore may start the moment the lock releases).
	for {
		m.mu.Lock()
		waitCh, inflight := m.restoreInFlight[id]
		if !inflight {
			break // still holding m.mu — proceed to the eviction below
		}
		m.mu.Unlock()
		<-waitCh
	}
	defer m.mu.Unlock()

	sess, ok := m.sessions[id]
	if !ok {
		return nil, nil // not in memory — nothing staged, nothing to evict
	}

	sess.mu.Lock()
	if sess.active {
		sess.mu.Unlock()
		return nil, fmt.Errorf("session %s has an active task; it cannot be evicted", id)
	}
	if sess.compacting {
		sess.mu.Unlock()
		return nil, fmt.Errorf("session %s has a manual compaction in flight; it cannot be evicted", id)
	}
	// Capture staged attachments BEFORE any teardown, under the same m.mu
	// hold that deletes the session (loss-proof ordering above).
	snap := &PendingAttachmentSnapshot{
		Docs:   make([]orchestration.Attachment, len(sess.pendingAttachments)),
		Images: make([]ImageAttachment, len(sess.pendingImageAttachments)),
	}
	copy(snap.Docs, sess.pendingAttachments)
	copy(snap.Images, sess.pendingImageAttachments)
	// Per-step dump cleanup via the orchestrator, then the plain file handles.
	if sess.orchestrator != nil {
		sess.orchestrator.Cleanup()
	}
	if sess.logFile != nil {
		if err := sess.logFile.Close(); err != nil {
			m.log().Warn("failed to close session log file during eviction", "session_id", id, "error", err)
		}
	}
	if sess.dumpFile != nil {
		if err := sess.dumpFile.Close(); err != nil {
			m.log().Warn("failed to close session dump file during eviction", "session_id", id, "error", err)
		}
	}
	sess.mu.Unlock()

	delete(m.sessions, id)
	m.fileTracker.PurgeSession(id)
	m.lastToolCallIDs.Delete(id)
	return snap, nil
}

// Parks new lazy restores until release: one starting between the eviction
// and the store commit would rebuild from pre-commit state (and recreate
// the undo's source directory). In-flight restores are waited out; caller
// MUST release (defer). See specs/domains/session-lifecycle.md (§ Promotion).
func (m *Manager) ReserveRestores(id string) (func(), error) {
	if id == "" {
		return nil, errors.New("session id is required")
	}
	// Wait out in-flight restores first (same loop as EvictSession): the
	// reservation must start from a quiesced session.
	for {
		m.mu.Lock()
		waitCh, inflight := m.restoreInFlight[id]
		if !inflight {
			break // still holding m.mu — proceed to the reservation below
		}
		m.mu.Unlock()
		<-waitCh
	}
	parkCh := make(chan struct{})
	m.restoreParked[id] = parkCh
	m.mu.Unlock()

	return func() {
		m.mu.Lock()
		delete(m.restoreParked, id)
		m.mu.Unlock()
		close(parkCh)
	}, nil
}

// Moves a CHAT session's on-disk state into the CODE layout: the session
// directory projects/__no_project__/<sid> → projects/<dst>/<sid>, then its
// workspace → the project's canonical Workspace slot (two renames, one
// filesystem — atomic). The destination session dir must not exist; the
// destination Workspace must be empty or absent (nothing non-empty is ever
// deleted); a missing source is not an error; other failures roll back
// best-effort. Caller must have evicted the session and stopped its
// terminal first. See specs/domains/session-lifecycle.md (§ Promotion).
func (m *Manager) MoveSessionStorage(id, srcProjectID, dstProjectID string) error {
	if id == "" {
		return errors.New("session id is required")
	}
	if srcProjectID != project.NoProjectID {
		return fmt.Errorf("session %s is not a No Project session (owner %q); nothing to promote", id, srcProjectID)
	}
	if dstProjectID == "" || dstProjectID == project.NoProjectID {
		return errors.New("destination project must be a real project")
	}

	srcDir := config.SessionDir(m.agentDir, srcProjectID, id)
	dstSessionDir := config.SessionDir(m.agentDir, dstProjectID, id)
	// No Project workspace uses NoProjectWorkspaceSegment, CODE uses
	// WorkspaceSegment; hop 2 renames from the DESTINATION dir.
	dstWorkspace := config.ProjectWorkspacePath(m.agentDir, dstProjectID)

	if _, err := os.Lstat(dstSessionDir); err == nil {
		return fmt.Errorf("destination session directory %s already exists", dstSessionDir)
	} else if !errors.Is(err, os.ErrNotExist) {
		return fmt.Errorf("cannot inspect destination session directory: %w", err)
	}

	rename := m.renameOr(os.Rename)

	if _, err := os.Lstat(srcDir); errors.Is(err, os.ErrNotExist) {
		m.log().Debug("promoting a session with no on-disk state; nothing to move",
			"session_id", id, "expected_dir", srcDir)
		return nil
	} else if err != nil {
		return fmt.Errorf("cannot inspect source session directory: %w", err)
	}

	// Hop 1: the whole session directory (workspace, logs, dumps, plans,
	// temp, images) moves under the destination project.
	if err := rename(srcDir, dstSessionDir); err != nil {
		return fmt.Errorf("failed to move session directory into the project: %w", err)
	}
	rollbackHop1 := func() {
		if rbErr := rename(dstSessionDir, srcDir); rbErr != nil {
			m.log().Error("failed to roll back the session-directory move",
				"session_id", id, "dir", dstSessionDir, "error", rbErr)
		}
	}

	// Hop 2: lift the per-session workspace into the project's canonical
	// Workspace slot; os.Remove refuses non-empty directories, so content
	// there aborts the move instead of being deleted.
	innerWorkspace := filepath.Join(dstSessionDir, config.NoProjectWorkspaceSegment)
	if err := os.Remove(dstWorkspace); err != nil {
		if !errors.Is(err, os.ErrNotExist) {
			rollbackHop1()
			return fmt.Errorf("destination workspace %s is not an empty directory: %w", dstWorkspace, err)
		}
	}
	if err := rename(innerWorkspace, dstWorkspace); err != nil {
		rollbackHop1()
		return fmt.Errorf("failed to lift the workspace into the project: %w", err)
	}
	return nil
}

// The injected rename seam (promoteRename, assigned by in-package tests for
// fault injection) or the real os.Rename.
func (m *Manager) renameOr(fallback func(oldpath, newpath string) error) func(oldpath, newpath string) error {
	if m.promoteRename != nil {
		return m.promoteRename
	}
	return fallback
}

// Reverses a completed MoveSessionStorage — the file-level compensation
// half. Settled layouts return nil (both directories missing; source
// already home); only both PRESENT — an ambiguous half-move — errors. See
// specs/domains/session-lifecycle.md (§ Promotion).
func (m *Manager) UndoMoveSessionStorage(id, dstProjectID string) error {
	if id == "" {
		return errors.New("session id is required")
	}
	if dstProjectID == "" || dstProjectID == project.NoProjectID {
		return errors.New("destination project must be a real project")
	}
	srcDir := config.SessionDir(m.agentDir, project.NoProjectID, id)
	dstSessionDir := config.SessionDir(m.agentDir, dstProjectID, id)
	dstWorkspace := config.ProjectWorkspacePath(m.agentDir, dstProjectID)
	innerWorkspace := filepath.Join(dstSessionDir, config.NoProjectWorkspaceSegment)

	srcStat, srcErr := os.Lstat(srcDir)
	if srcErr != nil && !errors.Is(srcErr, os.ErrNotExist) {
		return fmt.Errorf("cannot inspect source session directory: %w", srcErr)
	}
	dstStat, dstErr := os.Lstat(dstSessionDir)
	if dstErr != nil && !errors.Is(dstErr, os.ErrNotExist) {
		return fmt.Errorf("cannot inspect destination session directory: %w", dstErr)
	}
	if srcStat != nil && dstStat == nil {
		// Already home (the move never happened, or a previous undo
		// completed): nothing to move back.
		return nil
	}
	if srcStat == nil && dstStat == nil {
		return nil
	}
	if srcStat != nil {
		// Both directories exist: ambiguous half-moved layout (e.g. a lazy
		// restore recreated the source mid-promotion).
		return fmt.Errorf("source session directory %s already exists; cannot undo into it", srcDir)
	}

	rename := m.renameOr(os.Rename)

	// Hop 1 (reverse): drop the project Workspace back into the session dir.
	if err := rename(dstWorkspace, innerWorkspace); err != nil {
		return fmt.Errorf("failed to lower the workspace back into the session directory: %w", err)
	}
	rollback := func() {
		if rbErr := rename(innerWorkspace, dstWorkspace); rbErr != nil {
			m.log().Error("failed to roll back the workspace undo",
				"session_id", id, "workspace", innerWorkspace, "error", rbErr)
		}
	}

	// Hop 2 (reverse): the session directory returns under __no_project__.
	if err := rename(dstSessionDir, srcDir); err != nil {
		rollback()
		return fmt.Errorf("failed to move the session directory back: %w", err)
	}
	return nil
}
