package session

import (
	"context"
	"os"
	"path/filepath"
	"testing"

	"github.com/v0lka/c0wrk/backend/config"
	"github.com/v0lka/c0wrk/backend/project"
)

// seedEvictableSession inserts a settled (non-active, non-compacting) session
// directly into the manager map, mirroring the in-memory fixtures elsewhere in
// this package.
func seedEvictableSession(t *testing.T, m *Manager, id string) *Session {
	t.Helper()
	sess := &Session{
		ID:        id,
		ProjectID: project.NoProjectID,
		TempDir:   config.SessionTempDir(m.agentDir, project.NoProjectID, id),
	}
	m.mu.Lock()
	m.sessions[id] = sess
	m.mu.Unlock()
	return sess
}

func TestEvictSession_RemovesFromMemoryKeepsFiles(t *testing.T) {
	manager, _, agentDir := testManager(t)
	id := "evict-me"

	// On-disk state the eviction must NOT touch.
	logDir := config.SessionLogsDir(agentDir, project.NoProjectID, id)
	if err := os.MkdirAll(logDir, 0o755); err != nil {
		t.Fatalf("mkdir log dir: %v", err)
	}
	trackedFile := filepath.Join(logDir, "x.txt")
	if err := os.WriteFile(trackedFile, []byte("x"), 0o644); err != nil {
		t.Fatalf("write tracked file: %v", err)
	}
	seedEvictableSession(t, manager, id)

	// Tracking entries the eviction must purge alongside the session.
	manager.lastToolCallIDs.Store(id, toolCallIDEntry{id: "tc1", tool: "bash_exec"})
	seedCtx := ContextWithSessionID(context.Background(), id)
	manager.fileTracker.RecordWrite(seedCtx, trackedFile)
	if n := len(manager.fileTracker.snapshots[id]); n != 1 {
		t.Fatalf("test setup: expected 1 tracked snapshot, got %d", n)
	}

	if err := manager.EvictSession(id); err != nil {
		t.Fatalf("EvictSession failed: %v", err)
	}

	manager.mu.RLock()
	_, stillThere := manager.sessions[id]
	manager.mu.RUnlock()
	if stillThere {
		t.Fatal("session must be gone from the manager map")
	}
	if _, ok := manager.lastToolCallIDs.Load(id); ok {
		t.Fatal("lastToolCallIDs entry must be purged")
	}
	if n := len(manager.fileTracker.snapshots[id]); n != 0 {
		t.Fatalf("file-coherence state must be purged, %d entries remain", n)
	}
	if _, err := os.Stat(logDir); err != nil {
		t.Fatalf("on-disk files must survive eviction: %v", err)
	}

	// Idempotent: a second evict of the absent session is a no-op.
	if err := manager.EvictSession(id); err != nil {
		t.Fatalf("second EvictSession must be a no-op, got %v", err)
	}
	// An unknown session is equally a no-op.
	if err := manager.EvictSession("never-existed"); err != nil {
		t.Fatalf("evicting an unknown session must be a no-op, got %v", err)
	}
}

func TestEvictSession_RefusesActiveAndCompacting(t *testing.T) {
	manager, _, _ := testManager(t)

	sess := seedEvictableSession(t, manager, "busy")
	sess.mu.Lock()
	sess.active = true
	sess.mu.Unlock()
	if err := manager.EvictSession("busy"); err == nil {
		t.Fatal("evicting a session with an active task must be refused")
	}
	sess.mu.Lock()
	sess.active = false
	sess.compacting = true
	sess.mu.Unlock()
	if err := manager.EvictSession("busy"); err == nil {
		t.Fatal("evicting a session with an in-flight compaction must be refused")
	}
	// Refusals must not drop the session from the map.
	manager.mu.RLock()
	_, stillThere := manager.sessions["busy"]
	manager.mu.RUnlock()
	if !stillThere {
		t.Fatal("a refused eviction must keep the session in memory")
	}
}

func TestMoveSessionStorage_MovesSessionDirAndWorkspace(t *testing.T) {
	manager, _, agentDir := testManager(t)
	const sid = "move-me"
	const dst = "dst-project"

	// Source layout: session dir with infra + a per-session workspace holding
	// the user's files.
	srcDir := config.SessionDir(agentDir, project.NoProjectID, sid)
	srcWorkspace := filepath.Join(srcDir, config.NoProjectWorkspaceSegment)
	if err := os.MkdirAll(filepath.Join(srcWorkspace, "nested"), 0o755); err != nil {
		t.Fatalf("mkdir source workspace: %v", err)
	}
	if err := os.WriteFile(filepath.Join(srcWorkspace, "nested", "app.go"), []byte("x"), 0o644); err != nil {
		t.Fatalf("write source file: %v", err)
	}
	if err := os.MkdirAll(config.SessionLogsDir(agentDir, project.NoProjectID, sid), 0o755); err != nil {
		t.Fatalf("mkdir source logs: %v", err)
	}

	// Destination layout: the project dir with the freshly created, empty
	// Workspace slot (what Manager.CreateProject produces).
	dstWorkspace := config.ProjectWorkspacePath(agentDir, dst)
	if err := os.MkdirAll(dstWorkspace, 0o755); err != nil {
		t.Fatalf("mkdir destination workspace: %v", err)
	}

	if err := manager.MoveSessionStorage(sid, project.NoProjectID, dst); err != nil {
		t.Fatalf("MoveSessionStorage failed: %v", err)
	}

	// The workspace content now lives at the project's canonical Workspace.
	if _, err := os.Stat(filepath.Join(dstWorkspace, "nested", "app.go")); err != nil {
		t.Fatalf("workspace content must land at the project Workspace: %v", err)
	}
	// The session infra dir (logs, and the now-absent workspace segment)
	// moved under the destination project.
	if _, err := os.Stat(config.SessionLogsDir(agentDir, dst, sid)); err != nil {
		t.Fatalf("session infra must move with the session: %v", err)
	}
	// Nothing is left behind at the old locations.
	if _, err := os.Stat(srcDir); !os.IsNotExist(err) {
		t.Fatalf("source session dir must be gone (stat err=%v)", err)
	}
}

func TestMoveSessionStorage_MissingSourceIsNotAnError(t *testing.T) {
	manager, _, _ := testManager(t)
	if err := manager.MoveSessionStorage("ghost", project.NoProjectID, "dst-project"); err != nil {
		t.Fatalf("a session with no on-disk state must promote cleanly, got %v", err)
	}
}

func TestMoveSessionStorage_RefusesBadOwnersAndDestinations(t *testing.T) {
	manager, _, _ := testManager(t)

	if err := manager.MoveSessionStorage("s", "some-code-project", "dst"); err == nil {
		t.Fatal("a non-CHAT source project must be refused")
	}
	if err := manager.MoveSessionStorage("s", project.NoProjectID, ""); err == nil {
		t.Fatal("an empty destination must be refused")
	}
	if err := manager.MoveSessionStorage("s", project.NoProjectID, project.NoProjectID); err == nil {
		t.Fatal("No Project as the destination must be refused")
	}
	if err := manager.MoveSessionStorage("", project.NoProjectID, "dst"); err == nil {
		t.Fatal("an empty session id must be refused")
	}
}

func TestMoveSessionStorage_RefusesExistingDestinationAndDirtyWorkspace(t *testing.T) {
	manager, _, agentDir := testManager(t)
	const sid = "collide"

	srcDir := config.SessionDir(agentDir, project.NoProjectID, sid)
	if err := os.MkdirAll(filepath.Join(srcDir, config.NoProjectWorkspaceSegment), 0o755); err != nil {
		t.Fatalf("mkdir source: %v", err)
	}

	// Destination session dir already present → refuse without touching source.
	if err := os.MkdirAll(config.SessionDir(agentDir, "dst-project", sid), 0o755); err != nil {
		t.Fatalf("mkdir destination session dir: %v", err)
	}
	if err := manager.MoveSessionStorage(sid, project.NoProjectID, "dst-project"); err == nil {
		t.Fatal("an existing destination session dir must be refused")
	}
	if _, err := os.Stat(srcDir); err != nil {
		t.Fatalf("source must stay intact after the refusal: %v", err)
	}
	if err := os.RemoveAll(config.SessionDir(agentDir, "dst-project", sid)); err != nil {
		t.Fatalf("cleanup: %v", err)
	}

	// Destination Workspace non-empty → refuse; the pre-created empty slot is
	// the normal case, content in it means something went wrong upstream.
	dirtyWorkspace := config.ProjectWorkspacePath(agentDir, "dst-project")
	if err := os.MkdirAll(dirtyWorkspace, 0o755); err != nil {
		t.Fatalf("mkdir dirty workspace: %v", err)
	}
	if err := os.WriteFile(filepath.Join(dirtyWorkspace, "stowaway.txt"), []byte("x"), 0o644); err != nil {
		t.Fatalf("write stowaway: %v", err)
	}
	if err := manager.MoveSessionStorage(sid, project.NoProjectID, "dst-project"); err == nil {
		t.Fatal("a non-empty destination Workspace must be refused")
	}
	if _, err := os.Stat(filepath.Join(dirtyWorkspace, "stowaway.txt")); err != nil {
		t.Fatalf("the stowaway file must survive the refusal: %v", err)
	}
	if _, err := os.Stat(srcDir); err != nil {
		t.Fatalf("source must stay intact after the dirty-workspace refusal: %v", err)
	}
}

func TestMoveSessionStorage_RollsBackWhenSecondHopFails(t *testing.T) {
	manager, _, agentDir := testManager(t)
	const sid = "rollback"

	srcDir := config.SessionDir(agentDir, project.NoProjectID, sid)
	if err := os.MkdirAll(filepath.Join(srcDir, config.NoProjectWorkspaceSegment), 0o755); err != nil {
		t.Fatalf("mkdir source: %v", err)
	}
	dstWorkspace := config.ProjectWorkspacePath(agentDir, "dst-project")
	if err := os.MkdirAll(dstWorkspace, 0o755); err != nil {
		t.Fatalf("mkdir destination workspace: %v", err)
	}

	// Fail exactly the hop-2 rename (the second rename call); the rollback
	// rename (third call) must succeed so the source layout is restored.
	calls := 0
	realRename := os.Rename
	manager.promoteRename = func(oldpath, newpath string) error {
		calls++
		if calls == 2 {
			return os.ErrPermission
		}
		return realRename(oldpath, newpath)
	}
	t.Cleanup(func() { manager.promoteRename = nil })

	if err := manager.MoveSessionStorage(sid, project.NoProjectID, "dst-project"); err == nil {
		t.Fatal("hop-2 failure must surface as an error")
	}

	// The rollback put the session directory back where it was.
	if _, err := os.Stat(srcDir); err != nil {
		t.Fatalf("source session dir must be restored by the rollback: %v", err)
	}
	if _, err := os.Stat(filepath.Join(srcDir, config.NoProjectWorkspaceSegment)); err != nil {
		t.Fatalf("the source workspace must be back inside the session dir: %v", err)
	}
}
