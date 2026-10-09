package session

import (
	"errors"
	"fmt"
	"os"
	"path/filepath"

	"github.com/v0lka/c0wrk/backend/config"
	"github.com/v0lka/c0wrk/backend/project"
)

// EvictSession drops an in-memory session WITHOUT touching any on-disk state:
// per-step dump files are closed through the orchestrator's idempotent
// cleanup, the session log/LLM-dump file handles are closed, and the session
// plus its tracking entries (file-coherence, last tool_call_id) are purged
// from the manager. The next GetSession lazily re-restores the session from
// the store — at that point any project change already committed by the caller
// takes effect (NoProjectMode is derived from project_id at orchestrator-build
// time), which is exactly what the session-promotion flow needs.
//
// Deletion is NOT performed here: logs, dumps, workspace — everything on disk
// survives. A session with a running task or an in-flight manual compaction is
// refused (its goroutines still hold open state); a session that is not in
// memory returns nil — eviction is idempotent.
//
// A concurrently starting lazy restore is waited out first (single-flight
// reservation), so a restore that began before the eviction can never
// re-insert the session after it is dropped.
func (m *Manager) EvictSession(id string) error {
	if id == "" {
		return errors.New("session id is required")
	}
	// Wait out any in-flight restore of this session, then evict under the
	// write lock. The loop re-checks because another goroutine may start a new
	// restore the moment we release the lock to wait.
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
		return nil // not in memory — nothing to evict
	}

	sess.mu.Lock()
	if sess.active {
		sess.mu.Unlock()
		return fmt.Errorf("session %s has an active task; it cannot be evicted", id)
	}
	if sess.compacting {
		sess.mu.Unlock()
		return fmt.Errorf("session %s has a manual compaction in flight; it cannot be evicted", id)
	}
	// Idempotent per-step dump cleanup via the orchestrator (same call
	// DeleteSession makes), then the plain file handles.
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
	// Mirror DeleteSession's tracking purges so a stale entry cannot outlive
	// the evicted identity.
	m.fileTracker.PurgeSession(id)
	m.lastToolCallIDs.Delete(id)
	return nil
}

// ReserveRestores parks new lazy restores of the session until the returned
// release function is called. It closes the gap EvictSession cannot: a
// restore that STARTS after the eviction but before the caller's store
// commit would rebuild the session from the pre-commit state (CHAT owner,
// stale workspace paths — and, worse, it would recreate the source session
// directory a later file-level undo needs to rename back into). The
// reservation waits out any restore already in flight (the same single-
// flight loop EvictSession uses), then blocks every new attempt; a blocked
// GetSession restarts its restore from scratch once the window closes, so it
// observes the post-mutation store state. The caller MUST invoke release —
// defer it — otherwise lazy restores of the session park forever.
//
// Used by the session-promotion flow; not part of the general lifecycle.
func (m *Manager) ReserveRestores(id string) (func(), error) {
	if id == "" {
		return nil, errors.New("session id is required")
	}
	// Wait out any in-flight restore first (same loop as EvictSession): the
	// reservation must start from a quiesced session — nothing mid-restore,
	// and nothing allowed to start until release.
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

// MoveSessionStorage physically relocates a CHAT session's on-disk state into
// a CODE project's layout, using the same derived paths every other subsystem
// (DeleteSession, restore, dump tracker) computes from (agentDir, projectID,
// sessionID):
//
//	<agentDir>/projects/__no_project__/<sid>   → <agentDir>/projects/<dst>/<sid>
//	<agentDir>/projects/<dst>/<sid>/workspace  → <agentDir>/projects/<dst>/Workspace
//
// Both hops are renames within ~/.c0wrk/projects (one filesystem — atomic on
// the platforms this app ships on). The destination session directory must not
// exist; the destination Workspace must be empty or absent (a freshly created
// internal project's Workspace is exactly that) — a non-empty destination
// workspace is refused rather than merged. The pre-created empty Workspace is
// removed only via os.Remove, which fails on any non-empty directory, so a
// surprise can never be deleted.
//
// A missing SOURCE directory is not an error: a session that was created but
// never materialized (never opened, never sent a message) simply has nothing
// to move — the destination project's Workspace already exists and the store
// update carries the rest. Any other failure rolls the completed hops back
// best-effort so the caller's own compensation (project-row removal) finds the
// original layout intact.
//
// The caller must have evicted the session and stopped its terminal first:
// open handles and a PTY cwd inside the moved tree are the failure modes this
// cannot fix.
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
	// config.NoProjectWorkspaceSegment is the "workspace" segment the No
	// Project session manager creates; the CODE project workspace uses
	// config.WorkspaceSegment ("Workspace") via ProjectWorkspacePath. After
	// hop 1 the workspace lives at <dstSessionDir>/workspace, so hop 2 renames
	// from the DESTINATION session dir, not the old No Project location.
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
	// Workspace slot (it now sits at <dstSessionDir>/workspace). os.Remove
	// refuses a non-empty directory, so a destination Workspace that somehow
	// has content aborts the move instead of being deleted.
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

// renameOr returns the injected rename seam (promoteRename, assigned directly
// by in-package tests for fault injection) or the real os.Rename.
func (m *Manager) renameOr(fallback func(oldpath, newpath string) error) func(oldpath, newpath string) error {
	if m.promoteRename != nil {
		return m.promoteRename
	}
	return fallback
}

// UndoMoveSessionStorage reverses a completed MoveSessionStorage: the lifted
// project Workspace returns to <session dir>/workspace and the session
// directory returns under __no_project__. It is the file-level half of the
// promotion compensation — the caller runs it when the store update failed so
// the files and the database agree again, before deleting the (still rowless
// or to-be-deleted) destination project. The No Project project row itself is
// never deleted, so the restored layout is immediately valid again.
//
// A session that never materialized on disk has nothing to undo: when BOTH
// the source and the destination session directories are missing, the undo
// is trivially successful and returns nil — the same settled answer
// MoveSessionStorage gives for the missing source (a source that is already
// home with the destination gone is equally settled). Only an ambiguous,
// half-moved layout — both directories present — is an error.
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
		// The session directory is already home (the move never happened,
		// or a previous undo already completed): there is nothing left to
		// move back.
		return nil
	}
	if srcStat == nil && dstStat == nil {
		// A session that never materialized on disk: nothing to undo — the
		// same settled answer MoveSessionStorage gives for a missing source.
		return nil
	}
	if srcStat != nil {
		// Both directories exist: an ambiguous, half-moved layout (e.g. a
		// lazy restore recreated the source mid-promotion) that no blind
		// rename may resolve.
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
