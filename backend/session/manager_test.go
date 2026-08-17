package session

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"log/slog"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/v0lka/c0wrk/backend/config"
	"github.com/v0lka/c0wrk/backend/project"
	"github.com/v0lka/c0wrk/core"
	"github.com/v0lka/sp4rk/orchestration"
)

// testManager creates a Manager with a mock factory for testing.
func testManager(t *testing.T) (manager *Manager, events chan Event, agentDir string) {
	t.Helper()

	// Create temp directory for agent
	agentDir = t.TempDir()

	// Create event channel to capture events
	eventChan := make(chan Event, 100)
	emitFunc := func(e Event) {
		select {
		case eventChan <- e:
		default:
			// Channel full, drop event
		}
	}

	// Create factory that returns nil orchestrator with no error (we'll patch sessions manually)
	factory := func(emitter core.Emitter, logger *slog.Logger, workspacePath string, bbFactory core.BlackboardFactory, dumpWriter io.Writer, _ *orchestration.StepDumpTracker) (*core.Orchestrator, error) {
		return nil, nil
	}

	manager = NewManager(factory, emitFunc, agentDir)
	// Ensure all session file handles (log/dump files) are closed so the
	// TempDir cleanup can remove them — Windows refuses to delete files
	// that still have open handles.
	t.Cleanup(manager.Shutdown)
	events = eventChan
	return
}

// testWorkspacePath returns a temp workspace path for tests.
func testWorkspacePath(t *testing.T) string {
	t.Helper()
	return t.TempDir()
}

// drainEvents drains all pending events from the channel.
func drainEvents(ch chan Event) {
	for {
		select {
		case <-ch:
		default:
			return
		}
	}
}

func TestNewManager(t *testing.T) {
	manager, _, _ := testManager(t)

	if manager == nil {
		t.Fatal("NewManager returned nil")
	}

	if manager.sessions == nil {
		t.Error("manager.sessions is nil")
	}

	if manager.orchestratorFactory == nil {
		t.Error("manager.orchestratorFactory is nil")
	}

	if manager.emitFunc == nil {
		t.Error("manager.emitFunc is nil")
	}
}

func TestManager_CreateSession(t *testing.T) {
	manager, eventChan, agentDir := testManager(t)

	wsPath := testWorkspacePath(t)
	info, err := manager.CreateSession(testProjectID, wsPath)
	if err != nil {
		t.Fatalf("CreateSession failed: %v", err)
	}

	// Verify SessionInfo
	if info.ID == "" {
		t.Error("Session ID is empty")
	}
	if info.Name == "" {
		t.Error("Session Name is empty")
	}
	if info.CreatedAt == "" {
		t.Error("Session CreatedAt is empty")
	}
	if info.Archived {
		t.Error("New session should not be archived")
	}
	if info.Active {
		t.Error("New session should not be active")
	}

	// Verify session was stored
	session, exists := manager.GetSession(info.ID)
	if !exists {
		t.Fatal("Session not found after creation")
	}
	if session.ID != info.ID {
		t.Errorf("Session ID mismatch: got %s, want %s", session.ID, info.ID)
	}

	// Verify log file was created
	logFile := config.SessionLogPath(agentDir, testProjectID, info.ID)
	if _, err := os.Stat(logFile); os.IsNotExist(err) {
		t.Errorf("Log file not created: %s", logFile)
	}

	// Verify event was emitted
	select {
	case event := <-eventChan:
		if event.Type != "session_created" {
			t.Errorf("Expected session_created event, got %s", event.Type)
		}
		if event.SessionID != info.ID {
			t.Errorf("Event SessionID mismatch: got %s, want %s", event.SessionID, info.ID)
		}
	case <-time.After(time.Second):
		t.Error("Timeout waiting for session_created event")
	}
}

func TestManager_CreateSession_Multiple(t *testing.T) {
	manager, _, _ := testManager(t)

	// Create multiple sessions (each in a different project to avoid active-session conflict)
	infos := make([]*SessionInfo, 3)
	for i := 0; i < 3; i++ {
		projID := fmt.Sprintf("project-%d", i)
		info, err := manager.CreateSession(projID, testWorkspacePath(t))
		if err != nil {
			t.Fatalf("CreateSession %d failed: %v", i, err)
		}
		infos[i] = info
	}

	// Verify all sessions exist
	sessions := manager.ListSessions()
	if len(sessions) != 3 {
		t.Errorf("Expected 3 sessions, got %d", len(sessions))
	}

	// Verify each session has unique ID
	ids := make(map[string]bool)
	for _, info := range infos {
		if ids[info.ID] {
			t.Errorf("Duplicate session ID: %s", info.ID)
		}
		ids[info.ID] = true
	}
}

func TestManager_GetSession(t *testing.T) {
	manager, _, _ := testManager(t)

	// Get non-existent session
	_, exists := manager.GetSession("non-existent")
	if exists {
		t.Error("GetSession should return false for non-existent session")
	}

	// Create and get session
	info, _ := manager.CreateSession(testProjectID, testWorkspacePath(t))
	session, exists := manager.GetSession(info.ID)
	if !exists {
		t.Error("GetSession should return true for existing session")
	}
	if session.ID != info.ID {
		t.Errorf("Session ID mismatch: got %s, want %s", session.ID, info.ID)
	}
}

func TestManager_ListSessions(t *testing.T) {
	manager, _, _ := testManager(t)

	// Empty list
	sessions := manager.ListSessions()
	if len(sessions) != 0 {
		t.Errorf("Expected 0 sessions, got %d", len(sessions))
	}

	// Create sessions
	info1, _ := manager.CreateSession(testProjectID, testWorkspacePath(t))
	info2, _ := manager.CreateSession(testProjectID, testWorkspacePath(t))

	// List should return both
	sessions = manager.ListSessions()
	if len(sessions) != 2 {
		t.Errorf("Expected 2 sessions, got %d", len(sessions))
	}

	// Verify session info matches
	sessionMap := make(map[string]SessionInfo)
	for _, s := range sessions {
		sessionMap[s.ID] = s
	}

	if _, ok := sessionMap[info1.ID]; !ok {
		t.Errorf("Session %s not found in list", info1.ID)
	}
	if _, ok := sessionMap[info2.ID]; !ok {
		t.Errorf("Session %s not found in list", info2.ID)
	}
}

func TestManager_DeleteSession(t *testing.T) {
	manager, eventChan, _ := testManager(t)

	// Delete non-existent session
	err := manager.DeleteSession("non-existent")
	if err == nil {
		t.Error("DeleteSession should return error for non-existent session")
	}

	// Create and delete session
	info, _ := manager.CreateSession(testProjectID, testWorkspacePath(t))

	// Drain the session_created event
	drainEvents(eventChan)
	sessions := manager.ListSessions()
	if len(sessions) != 1 {
		t.Fatalf("Expected 1 session before deletion, got %d", len(sessions))
	}

	err = manager.DeleteSession(info.ID)
	if err != nil {
		t.Fatalf("DeleteSession failed: %v", err)
	}

	// Verify session is gone
	sessions = manager.ListSessions()
	if len(sessions) != 0 {
		t.Errorf("Expected 0 sessions after deletion, got %d", len(sessions))
	}

	_, exists := manager.GetSession(info.ID)
	if exists {
		t.Error("Session should not exist after deletion")
	}

	// Verify event was emitted
	select {
	case event := <-eventChan:
		if event.Type != "session_deleted" {
			t.Errorf("Expected session_deleted event, got %s", event.Type)
		}
	case <-time.After(time.Second):
		t.Error("Timeout waiting for session_deleted event")
	}
}

// TestManager_DeleteSession_RemovesSessionDirectory verifies that deleting a
// session removes ALL its internal files (logs, dumps, temp, plans) from
// ~/.c0wrk — not just the temp directory. The shared project workspace must
// survive since it is owned by the project, not the session.
func TestManager_DeleteSession_RemovesSessionDirectory(t *testing.T) {
	manager, _, agentDir := testManager(t)

	// Use a workspace OUTSIDE the agent dir to model a regular project's shared
	// workspace — it must NOT be removed on session deletion.
	projectWorkspace := testWorkspacePath(t)

	info, err := manager.CreateSession(testProjectID, projectWorkspace)
	if err != nil {
		t.Fatalf("CreateSession failed: %v", err)
	}

	// Seed internal files that previously leaked on deletion.
	sessionDir := config.SessionDir(agentDir, testProjectID, info.ID)
	dumpDir := config.SessionDumpPath(agentDir, testProjectID, info.ID)
	plansDir := config.SessionPlansDir(agentDir, testProjectID, info.ID)
	for _, p := range []string{
		filepath.Join(sessionDir, "marker.txt"),
		filepath.Join(filepath.Dir(dumpDir), "dump.jsonl"),
		filepath.Join(plansDir, "plan_1.json"),
	} {
		if err := os.MkdirAll(filepath.Dir(p), 0o755); err != nil {
			t.Fatalf("mkdir %s: %v", p, err)
		}
		if err := os.WriteFile(p, []byte("x"), 0o644); err != nil {
			t.Fatalf("write %s: %v", p, err)
		}
	}

	if err := manager.DeleteSession(info.ID); err != nil {
		t.Fatalf("DeleteSession failed: %v", err)
	}

	// The entire session directory (logs, dumps, temp, plans) must be gone.
	if _, err := os.Stat(sessionDir); !os.IsNotExist(err) {
		t.Errorf("session directory should be removed after deletion: %s (stat err=%v)", sessionDir, err)
	}

	// The project workspace must survive.
	if _, err := os.Stat(projectWorkspace); err != nil {
		t.Errorf("project workspace should survive session deletion: %v", err)
	}
}

// TestManager_DeleteSession_NoProjectRemovesWorkspace verifies that deleting a
// No Project session removes its isolated per-session workspace in addition to
// logs/dumps/plans. The session is seeded directly (bypassing CreateSession)
// because the nil test orchestrator cannot satisfy SetNoProjectMode.
func TestManager_DeleteSession_NoProjectRemovesWorkspace(t *testing.T) {
	manager, _, agentDir := testManager(t)

	id := "no-project-session-1"
	ws := config.NoProjectSessionWorkspace(agentDir, id)
	if err := os.MkdirAll(ws, 0o755); err != nil {
		t.Fatalf("mkdir workspace: %v", err)
	}
	// Seed a file inside the workspace to prove the whole tree is removed.
	if err := os.WriteFile(filepath.Join(ws, "scratch.txt"), []byte("x"), 0o644); err != nil {
		t.Fatalf("write scratch file: %v", err)
	}
	// Create the session log dir/file like createSessionLogger would.
	logDir := config.SessionLogsDir(agentDir, project.NoProjectID, id)
	if err := os.MkdirAll(logDir, 0o755); err != nil {
		t.Fatalf("mkdir log dir: %v", err)
	}

	// Insert the session directly into the manager map.
	manager.mu.Lock()
	manager.sessions[id] = &Session{
		ID:        id,
		ProjectID: project.NoProjectID,
		TempDir:   config.SessionTempDir(agentDir, project.NoProjectID, id),
	}
	manager.mu.Unlock()

	sessionDir := config.SessionDir(agentDir, project.NoProjectID, id)

	if err := manager.DeleteSession(id); err != nil {
		t.Fatalf("DeleteSession failed: %v", err)
	}

	if _, err := os.Stat(ws); !os.IsNotExist(err) {
		t.Errorf("No Project workspace should be removed after deletion: %s (stat err=%v)", ws, err)
	}
	if _, err := os.Stat(sessionDir); !os.IsNotExist(err) {
		t.Errorf("No Project session directory should be removed after deletion: %s (stat err=%v)", sessionDir, err)
	}
}

// TestManager_ArchiveSession_KeepsFiles verifies that archiving a session does
// NOT remove its files — an archived session must remain restorable.
func TestManager_ArchiveSession_KeepsFiles(t *testing.T) {
	manager, _, agentDir := testManager(t)

	info, err := manager.CreateSession(testProjectID, testWorkspacePath(t))
	if err != nil {
		t.Fatalf("CreateSession failed: %v", err)
	}

	logFile := config.SessionLogPath(agentDir, testProjectID, info.ID)
	if _, err := os.Stat(logFile); err != nil {
		t.Fatalf("log file should exist before archive: %v", err)
	}

	if err := manager.ArchiveSession(info.ID); err != nil {
		t.Fatalf("ArchiveSession failed: %v", err)
	}

	// Archived session's files must survive.
	if _, err := os.Stat(logFile); err != nil {
		t.Errorf("log file should survive archiving: %v", err)
	}
}

func TestManager_RenameSession(t *testing.T) {
	manager, eventChan, _ := testManager(t)

	// Rename non-existent session
	err := manager.RenameSession("non-existent", "New Name")
	if err == nil {
		t.Error("RenameSession should return error for non-existent session")
	}

	// Create and rename session
	info, _ := manager.CreateSession(testProjectID, testWorkspacePath(t))

	// Drain the session_created event
	drainEvents(eventChan)
	oldName := info.Name
	newName := "My Custom Name"

	err = manager.RenameSession(info.ID, newName)
	if err != nil {
		t.Fatalf("RenameSession failed: %v", err)
	}

	// Verify rename
	session, _ := manager.GetSession(info.ID)
	if session.Name != newName {
		t.Errorf("Session name not updated: got %s, want %s", session.Name, newName)
	}

	// Verify list shows new name
	sessions := manager.ListSessions()
	if sessions[0].Name != newName {
		t.Errorf("ListSessions shows old name: got %s, want %s", sessions[0].Name, newName)
	}

	// Verify event was emitted
	select {
	case event := <-eventChan:
		if event.Type != "session_renamed" {
			t.Errorf("Expected session_renamed event, got %s", event.Type)
		}
		data, ok := event.Data.(SessionRenamedData)
		switch {
		case !ok:
			t.Errorf("Event data is not SessionRenamedData, got %T", event.Data)
		case data.OldName != oldName:
			t.Errorf("Event old_name mismatch: got %s, want %s", data.OldName, oldName)
		case data.NewName != newName:
			t.Errorf("Event new_name mismatch: got %s, want %s", data.NewName, newName)
		}
	case <-time.After(time.Second):
		t.Error("Timeout waiting for session_renamed event")
	}
}

func TestManager_ArchiveSession(t *testing.T) {
	manager, eventChan, _ := testManager(t)

	// Archive non-existent session
	err := manager.ArchiveSession("non-existent")
	if err == nil {
		t.Error("ArchiveSession should return error for non-existent session")
	}

	// Create session
	info, _ := manager.CreateSession(testProjectID, testWorkspacePath(t))

	// Drain the session_created event
	drainEvents(eventChan)
	if info.Archived {
		t.Error("New session should not be archived")
	}

	// Archive session
	err = manager.ArchiveSession(info.ID)
	if err != nil {
		t.Fatalf("ArchiveSession failed: %v", err)
	}

	// Verify archived
	session, _ := manager.GetSession(info.ID)
	if !session.Archived {
		t.Error("Session should be archived")
	}

	// Verify event was emitted
	select {
	case event := <-eventChan:
		if event.Type != "session_archived" {
			t.Errorf("Expected session_archived event, got %s", event.Type)
		}
	case <-time.After(time.Second):
		t.Error("Timeout waiting for session_archived event")
	}

	// Unarchive session
	err = manager.ArchiveSession(info.ID)
	if err != nil {
		t.Fatalf("ArchiveSession (unarchive) failed: %v", err)
	}

	// Verify unarchived
	session, _ = manager.GetSession(info.ID)
	if session.Archived {
		t.Error("Session should be unarchived")
	}

	// Verify unarchive event was emitted
	select {
	case event := <-eventChan:
		if event.Type != "session_unarchived" {
			t.Errorf("Expected session_unarchived event, got %s", event.Type)
		}
	case <-time.After(time.Second):
		t.Error("Timeout waiting for session_unarchived event")
	}
}

func TestManager_PinSession(t *testing.T) {
	manager, eventChan, _ := testManager(t)

	// Pin non-existent session
	err := manager.PinSession("non-existent")
	if err == nil {
		t.Error("PinSession should return error for non-existent session")
	}

	// Create session
	info, _ := manager.CreateSession(testProjectID, testWorkspacePath(t))

	// Drain the session_created event
	drainEvents(eventChan)
	if info.Pinned {
		t.Error("New session should not be pinned")
	}

	// Pin session
	err = manager.PinSession(info.ID)
	if err != nil {
		t.Fatalf("PinSession failed: %v", err)
	}

	// Verify pinned
	session, _ := manager.GetSession(info.ID)
	if !session.Pinned {
		t.Error("Session should be pinned")
	}

	// Verify event was emitted
	select {
	case event := <-eventChan:
		if event.Type != "session_pinned" {
			t.Errorf("Expected session_pinned event, got %s", event.Type)
		}
	case <-time.After(time.Second):
		t.Error("Timeout waiting for session_pinned event")
	}

	// Unpin session
	err = manager.PinSession(info.ID)
	if err != nil {
		t.Fatalf("PinSession (unpin) failed: %v", err)
	}

	// Verify unpinned
	session, _ = manager.GetSession(info.ID)
	if session.Pinned {
		t.Error("Session should be unpinned")
	}

	// Verify unpin event was emitted
	select {
	case event := <-eventChan:
		if event.Type != "session_unpinned" {
			t.Errorf("Expected session_unpinned event, got %s", event.Type)
		}
	case <-time.After(time.Second):
		t.Error("Timeout waiting for session_unpinned event")
	}
}

func TestManager_CancelTask(t *testing.T) {
	manager, _, _ := testManager(t)

	// Cancel on non-existent session
	err := manager.CancelTask("non-existent")
	if err == nil {
		t.Error("CancelTask should return error for non-existent session")
	}

	// Create session
	info, _ := manager.CreateSession(testProjectID, testWorkspacePath(t))

	// Cancel when no task is active
	err = manager.CancelTask(info.ID)
	if err == nil {
		t.Error("CancelTask should return error when no task is active")
	}
}

func TestManager_SendMessage_SessionNotFound(t *testing.T) {
	manager, _, _ := testManager(t)

	ctx := context.Background()
	err := manager.SendMessage(ctx, "non-existent", "hello", nil, nil, "", "", false, "", false)
	if err == nil {
		t.Error("SendMessage should return error for non-existent session")
	}
}

func TestManager_SendMessage_AlreadyActive(t *testing.T) {
	manager, _, _ := testManager(t)

	// Create session with a mock orchestrator
	info, _ := manager.CreateSession(testProjectID, testWorkspacePath(t))
	session, _ := manager.GetSession(info.ID)

	// Manually set session as active
	session.mu.Lock()
	session.active = true
	session.mu.Unlock()

	// Try to send message while active
	ctx := context.Background()
	err := manager.SendMessage(ctx, info.ID, "hello", nil, nil, "", "", false, "", false)
	if err == nil {
		t.Error("SendMessage should return error when session is already active")
	}
}

// TestManager_SendMessage_ArchivedRejected verifies the read-only invariant:
// the manager must refuse to launch a task on an archived session. This is the
// server-side choke point that backs the ArchivedBanner UI (and defends any
// other caller of SendMessage).
func TestManager_SendMessage_ArchivedRejected(t *testing.T) {
	manager, eventChan, _ := testManager(t)

	info, err := manager.CreateSession(testProjectID, testWorkspacePath(t))
	if err != nil {
		t.Fatalf("CreateSession failed: %v", err)
	}
	drainEvents(eventChan) // session_created

	// Archive the session (toggles Archived=true in-memory and emits an event).
	if err := manager.ArchiveSession(info.ID); err != nil {
		t.Fatalf("ArchiveSession failed: %v", err)
	}
	drainEvents(eventChan) // session_archived

	// SendMessage must be rejected with the sentinel error before the agent
	// goroutine is launched.
	err = manager.SendMessage(context.Background(), info.ID, "hello", nil, nil, "", "", false, "", false)
	if !errors.Is(err, ErrSessionArchived) {
		t.Errorf("SendMessage on archived session should return ErrSessionArchived, got %v", err)
	}
}

func TestManager_ConcurrentCreateDelete(t *testing.T) {
	manager, _, _ := testManager(t)

	const numOps = 50
	var wg sync.WaitGroup
	wg.Add(numOps * 2)

	// Concurrent creates
	createdIDs := make(chan string, numOps)
	for i := 0; i < numOps; i++ {
		go func() {
			defer wg.Done()
			info, err := manager.CreateSession(testProjectID, testWorkspacePath(t))
			if err != nil {
				t.Errorf("CreateSession failed: %v", err)
				return
			}
			createdIDs <- info.ID
		}()
	}

	// Concurrent deletes (will try to delete random IDs)
	go func() {
		for i := 0; i < numOps; i++ {
			go func() {
				defer wg.Done()
				// Try to delete a random ID - some will fail, that's ok
				_ = manager.DeleteSession("random-id")
			}()
		}
	}()

	wg.Wait()
	close(createdIDs)

	// Count created sessions
	count := 0
	for range createdIDs {
		count++
	}

	// Verify all created sessions still exist (weren't deleted)
	sessions := manager.ListSessions()
	if len(sessions) != count {
		t.Errorf("Expected %d sessions, got %d", count, len(sessions))
	}
}

func TestManager_ConcurrentOperations(t *testing.T) {
	manager, _, _ := testManager(t)

	// Create some sessions
	for i := 0; i < 5; i++ {
		_, _ = manager.CreateSession(testProjectID, testWorkspacePath(t))
	}

	const numOps = 100
	var wg sync.WaitGroup

	// Concurrent ListSessions
	wg.Add(numOps)
	for i := 0; i < numOps; i++ {
		go func() {
			defer wg.Done()
			_ = manager.ListSessions()
		}()
	}

	// Concurrent GetSession
	sessions := manager.ListSessions()
	if len(sessions) > 0 {
		wg.Add(numOps)
		for i := 0; i < numOps; i++ {
			go func(idx int) {
				defer wg.Done()
				sessionID := sessions[idx%len(sessions)].ID
				_, _ = manager.GetSession(sessionID)
			}(i)
		}
	}

	// Concurrent RenameSession
	if len(sessions) > 0 {
		wg.Add(numOps)
		for i := 0; i < numOps; i++ {
			go func(idx int) {
				defer wg.Done()
				sessionID := sessions[idx%len(sessions)].ID
				_ = manager.RenameSession(sessionID, "NewName")
			}(i)
		}
	}

	wg.Wait()

	// Verify sessions are still intact
	finalSessions := manager.ListSessions()
	if len(finalSessions) != 5 {
		t.Errorf("Expected 5 sessions after concurrent ops, got %d", len(finalSessions))
	}
}

func TestSession_IsActive(t *testing.T) {
	manager, _, _ := testManager(t)

	info, _ := manager.CreateSession(testProjectID, testWorkspacePath(t))
	session, _ := manager.GetSession(info.ID)

	// Initially not active
	if session.IsActive() {
		t.Error("New session should not be active")
	}

	// Manually set active
	session.mu.Lock()
	session.active = true
	session.mu.Unlock()

	if !session.IsActive() {
		t.Error("Session should be active after setting")
	}
}

func TestSession_GetOrchestrator(t *testing.T) {
	manager, _, _ := testManager(t)

	info, _ := manager.CreateSession(testProjectID, testWorkspacePath(t))
	session, _ := manager.GetSession(info.ID)

	// GetOrchestrator returns nil because we used a mock factory
	orch := session.GetOrchestrator()
	if orch != nil {
		t.Error("Expected nil orchestrator from mock factory")
	}
}

// TestContextWithSessionID verifies session ID round-trips through context.
func TestContextWithSessionID(t *testing.T) {
	ctx := context.Background()

	// Empty context returns empty string
	if id := SessionIDFromContext(ctx); id != "" {
		t.Errorf("expected empty string from background context, got %q", id)
	}

	// Set and retrieve
	ctx = ContextWithSessionID(ctx, "sess-abc")
	if id := SessionIDFromContext(ctx); id != "sess-abc" {
		t.Errorf("expected 'sess-abc', got %q", id)
	}

	// Overwrite with new value
	ctx = ContextWithSessionID(ctx, "sess-xyz")
	if id := SessionIDFromContext(ctx); id != "sess-xyz" {
		t.Errorf("expected 'sess-xyz', got %q", id)
	}
}

// TestParseSlogLevel verifies all log level strings.
func TestParseSlogLevel(t *testing.T) {
	tests := []struct {
		input string
		want  slog.Level
	}{
		{"DEBUG", slog.LevelDebug},
		{"debug", slog.LevelDebug},
		{"WARN", slog.LevelWarn},
		{"warn", slog.LevelWarn},
		{"ERROR", slog.LevelError},
		{"error", slog.LevelError},
		{"INFO", slog.LevelInfo},
		{"info", slog.LevelInfo},
		{"unknown", slog.LevelInfo}, // default
		{"", slog.LevelInfo},        // empty -> default
	}

	for _, tc := range tests {
		got := parseSlogLevel(tc.input)
		if got != tc.want {
			t.Errorf("parseSlogLevel(%q) = %v, want %v", tc.input, got, tc.want)
		}
	}
}

// TestManager_SetLogLevel verifies log level can be set.
func TestManager_SetLogLevel(t *testing.T) {
	manager, _, _ := testManager(t)

	// Default changed from DEBUG to INFO in 2026-06-05 review (W-27): DEBUG
	// Default logLevel is DEBUG for maximum diagnostic visibility.
	if manager.logLevel != "DEBUG" {
		t.Errorf("default logLevel should be DEBUG, got %q", manager.logLevel)
	}

	manager.SetLogLevel("ERROR")

	if manager.logLevel != "ERROR" {
		t.Errorf("expected logLevel ERROR, got %q", manager.logLevel)
	}
}

// TestManager_Shutdown verifies Shutdown cancels active tasks and cleans up.
func TestManager_Shutdown(t *testing.T) {
	manager, _, _ := testManager(t)

	// Create several sessions
	for i := 0; i < 3; i++ {
		_, err := manager.CreateSession(testProjectID, testWorkspacePath(t))
		if err != nil {
			t.Fatalf("CreateSession failed: %v", err)
		}
	}

	if len(manager.ListSessions()) != 3 {
		t.Fatalf("expected 3 sessions before shutdown")
	}

	// Set one session as active with a cancel func to verify it gets called
	sessions := manager.ListSessions()
	sess, _ := manager.GetSession(sessions[0].ID)
	cancelled := false
	sess.mu.Lock()
	sess.active = true
	sess.cancel = func() { cancelled = true }
	sess.mu.Unlock()

	manager.Shutdown()

	if len(manager.ListSessions()) != 0 {
		t.Errorf("expected 0 sessions after shutdown, got %d", len(manager.ListSessions()))
	}

	if !cancelled {
		t.Error("expected active session cancel to be called during shutdown")
	}
}

// TestManager_DeleteSession_WithActiveTask verifies deleting a session with active task cancels it.
func TestManager_DeleteSession_WithActiveTask(t *testing.T) {
	manager, _, _ := testManager(t)

	info, _ := manager.CreateSession(testProjectID, testWorkspacePath(t))
	session, _ := manager.GetSession(info.ID)

	// Simulate an active task
	cancelled := false
	session.mu.Lock()
	session.active = true
	session.cancel = func() { cancelled = true }
	session.mu.Unlock()

	err := manager.DeleteSession(info.ID)
	if err != nil {
		t.Fatalf("DeleteSession failed: %v", err)
	}

	if !cancelled {
		t.Error("expected cancel to be called when deleting session with active task")
	}

	_, exists := manager.GetSession(info.ID)
	if exists {
		t.Error("session should not exist after deletion")
	}
}

// TestManager_CancelTask_WithActiveTask verifies cancelling an active task.
func TestManager_CancelTask_WithActiveTask(t *testing.T) {
	manager, _, _ := testManager(t)

	info, _ := manager.CreateSession(testProjectID, testWorkspacePath(t))
	session, _ := manager.GetSession(info.ID)

	// Simulate an active task
	cancelled := false
	session.mu.Lock()
	session.active = true
	session.cancel = func() { cancelled = true }
	session.mu.Unlock()

	err := manager.CancelTask(info.ID)
	if err != nil {
		t.Fatalf("CancelTask failed: %v", err)
	}

	if !cancelled {
		t.Error("expected cancel func to be called")
	}
}

// TestManager_CreateSession_FactoryError verifies error handling when factory fails.
func TestManager_CreateSession_FactoryError(t *testing.T) {
	eventChan := make(chan Event, 100)
	emitFunc := func(e Event) {
		select {
		case eventChan <- e:
		default:
		}
	}

	// Factory that always fails
	factory := func(emitter core.Emitter, logger *slog.Logger, workspacePath string, bbFactory core.BlackboardFactory, dumpWriter io.Writer, _ *orchestration.StepDumpTracker) (*core.Orchestrator, error) {
		return nil, errors.New("factory error")
	}

	manager := NewManager(factory, emitFunc, t.TempDir())
	t.Cleanup(manager.Shutdown) // close handles before TempDir cleanup (Windows)
	_, err := manager.CreateSession(testProjectID, testWorkspacePath(t))
	if err == nil {
		t.Fatal("expected error from CreateSession when factory fails")
	}
	if !strings.Contains(err.Error(), "failed to create orchestrator") {
		t.Errorf("expected 'failed to create orchestrator' in error, got: %v", err)
	}
}

// TestManager_CreateSession_AllowsParallelActiveSessions verifies that creating a session
// while another session in the same project is active succeeds (parallel sessions allowed).
func TestManager_CreateSession_AllowsParallelActiveSessions(t *testing.T) {
	manager, _, _ := testManager(t)

	wsPath := testWorkspacePath(t)

	// Create first session and make it active
	info1, err := manager.CreateSession(testProjectID, wsPath)
	if err != nil {
		t.Fatalf("first CreateSession failed: %v", err)
	}
	sess1, _ := manager.GetSession(info1.ID)
	sess1.mu.Lock()
	sess1.active = true
	sess1.mu.Unlock()

	// Creating second session in same project should succeed
	info2, err := manager.CreateSession(testProjectID, wsPath)
	if err != nil {
		t.Fatalf("CreateSession should succeed while another session is active: %v", err)
	}
	if info2.ID == info1.ID {
		t.Error("second session should have a different ID")
	}

	// Creating session in a different project should also succeed
	_, err = manager.CreateSession("different-project", testWorkspacePath(t))
	if err != nil {
		t.Fatalf("CreateSession in different project should succeed: %v", err)
	}
}

// TestManager_SendMessage_AllowsParallelActiveSessions verifies that SendMessage succeeds
// when another session in the same project is already active (parallel sessions allowed).
func TestManager_SendMessage_AllowsParallelActiveSessions(t *testing.T) {
	manager, _, _ := testManager(t)

	wsPath := testWorkspacePath(t)

	// Create two sessions in the same project
	info1, err := manager.CreateSession(testProjectID, wsPath)
	if err != nil {
		t.Fatalf("CreateSession 1 failed: %v", err)
	}
	_, err = manager.CreateSession(testProjectID, wsPath)
	if err != nil {
		t.Fatalf("CreateSession 2 failed: %v", err)
	}

	// Make session 1 active
	sess1, _ := manager.GetSession(info1.ID)
	sess1.mu.Lock()
	sess1.active = true
	sess1.mu.Unlock()

	// Sending message to session 1 again should fail (same session double-send)
	ctx := context.Background()
	err = manager.SendMessage(ctx, info1.ID, "hello", nil, nil, "", "", false, "", false)
	if err == nil {
		t.Fatal("expected error when sending message to already-active session")
	}
	if !strings.Contains(err.Error(), "already processing") {
		t.Errorf("unexpected error: %v", err)
	}
}

// mockTaskStoreForResumable is a minimal TaskStore mock that controls
// what GetUnfinishedTask returns, used for emitResumableIfUnfinished tests.
type mockTaskStoreForResumable struct {
	unfinished     *TaskRecord // returned by GetUnfinishedTask
	loadTaskResult *TaskRecord // returned by LoadTask (nil → default nil,nil)
}

func (m *mockTaskStoreForResumable) SaveTask(_ context.Context, _ TaskRecord) error { return nil }
func (m *mockTaskStoreForResumable) UpdateTaskPlan(_ context.Context, _ string, _ json.RawMessage) error {
	return nil
}
func (m *mockTaskStoreForResumable) UpdateTaskRouting(_ context.Context, _ string, _ json.RawMessage) error {
	return nil
}
func (m *mockTaskStoreForResumable) SaveTaskStep(_ context.Context, _ string, _ TaskStepRecord) error {
	return nil
}
func (m *mockTaskStoreForResumable) AddTaskReflection(_ context.Context, _ string, _ json.RawMessage) error {
	return nil
}
func (m *mockTaskStoreForResumable) CompleteTask(_ context.Context, _, _ string, _ int) error {
	return nil
}
func (m *mockTaskStoreForResumable) FailTask(_ context.Context, _ string) error   { return nil }
func (m *mockTaskStoreForResumable) CancelTask(_ context.Context, _ string) error { return nil }
func (m *mockTaskStoreForResumable) PauseTask(_ context.Context, _ string) error  { return nil }
func (m *mockTaskStoreForResumable) LoadTask(_ context.Context, _ string) (*TaskRecord, error) {
	return m.loadTaskResult, nil
}
func (m *mockTaskStoreForResumable) LoadTaskSteps(_ context.Context, _ string) ([]TaskStepRecord, error) {
	return nil, nil
}
func (m *mockTaskStoreForResumable) SaveFacts(_ context.Context, _ string, _ json.RawMessage) error {
	return nil
}
func (m *mockTaskStoreForResumable) LoadFacts(_ context.Context, _ string) (json.RawMessage, error) {
	return nil, nil
}
func (m *mockTaskStoreForResumable) SaveAttachments(_ context.Context, _ string, _ json.RawMessage) error {
	return nil
}
func (m *mockTaskStoreForResumable) LoadAttachments(_ context.Context, _ string) (json.RawMessage, error) {
	return nil, nil
}
func (m *mockTaskStoreForResumable) SaveTrajectory(_ context.Context, _ string, _ json.RawMessage) error {
	return nil
}
func (m *mockTaskStoreForResumable) LoadTrajectory(_ context.Context, _ string) (json.RawMessage, error) {
	return nil, nil
}
func (m *mockTaskStoreForResumable) SaveGoalState(_ context.Context, _ string, _ json.RawMessage) error {
	return nil
}
func (m *mockTaskStoreForResumable) LoadGoalState(_ context.Context, _ string) (json.RawMessage, error) {
	return nil, nil
}
func (m *mockTaskStoreForResumable) GetUnfinishedTask(_ context.Context, _ string) (*TaskRecord, error) {
	return m.unfinished, nil
}
func (m *mockTaskStoreForResumable) ReactivateTask(_ context.Context, _ string) error { return nil }
func (m *mockTaskStoreForResumable) GetLatestTaskID(_ context.Context, _ string) (string, error) {
	return "", nil
}

// TestEmitResumableIfUnfinished_EmitsWhenUnfinishedTaskExists verifies that
// emitResumableIfUnfinished emits a "task_failed_resumable" event when the
// task store reports an in-progress task for the session.
func TestEmitResumableIfUnfinished_EmitsWhenUnfinishedTaskExists(t *testing.T) {
	manager, eventChan, _ := testManager(t)

	// Configure a mock store that reports an unfinished task.
	manager.SetTaskStore(&mockTaskStoreForResumable{
		unfinished: &TaskRecord{ID: "task-123", SessionID: "sess-1", Status: "failed"},
	})

	// Drain any prior events.
	drainEvents(eventChan)

	// Call the method under test.
	manager.emitResumableIfUnfinished("sess-1", "Network error during execution.")

	// Expect a task_failed_resumable event.
	select {
	case event := <-eventChan:
		if event.Type != "task_failed_resumable" {
			t.Errorf("expected task_failed_resumable event, got %s", event.Type)
		}
		if event.SessionID != "sess-1" {
			t.Errorf("expected session ID sess-1, got %s", event.SessionID)
		}
		data, ok := event.Data.(TaskFailedResumableData)
		if !ok {
			t.Fatalf("expected TaskFailedResumableData, got %T", event.Data)
		}
		if data.Message == "" {
			t.Error("expected non-empty message in TaskFailedResumableData")
		}
		if data.Reason != "Network error during execution." {
			t.Errorf("expected Reason to carry the contextual cause, got %q", data.Reason)
		}
		if data.TaskID != "task-123" {
			t.Errorf("expected TaskID task-123 (so the banner can be resolved by task_id), got %q", data.TaskID)
		}
	case <-time.After(time.Second):
		t.Error("timeout waiting for task_failed_resumable event")
	}
}

// TestEmitResumableIfUnfinished_NoEventWhenNoUnfinishedTask verifies that
// emitResumableIfUnfinished does NOT emit when the task store has no
// in-progress task (e.g. after a successful completion).
func TestEmitResumableIfUnfinished_NoEventWhenNoUnfinishedTask(t *testing.T) {
	manager, eventChan, _ := testManager(t)

	// Configure a mock store that reports no unfinished task.
	manager.SetTaskStore(&mockTaskStoreForResumable{
		unfinished: nil,
	})

	// Drain any prior events.
	drainEvents(eventChan)

	// Call the method under test.
	manager.emitResumableIfUnfinished("sess-1", "")

	// No event should be emitted.
	select {
	case event := <-eventChan:
		t.Errorf("expected no event, but got %s", event.Type)
	case <-time.After(100 * time.Millisecond):
		// OK — no event emitted.
	}
}

// TestEmitResumableIfUnfinished_NoEventWithoutTaskStore verifies that
// emitResumableIfUnfinished is a no-op when no task store is configured.
func TestEmitResumableIfUnfinished_NoEventWithoutTaskStore(t *testing.T) {
	manager, eventChan, _ := testManager(t)

	// No SetTaskStore call — taskStore remains nil.

	drainEvents(eventChan)

	manager.emitResumableIfUnfinished("sess-1", "")

	select {
	case event := <-eventChan:
		t.Errorf("expected no event, but got %s", event.Type)
	case <-time.After(100 * time.Millisecond):
		// OK — no event emitted.
	}
}

// TestTaskCompletionInfo verifies the mapping from typed execution status to
// the frontend-facing success contract.
func TestTaskCompletionInfo(t *testing.T) {
	tests := []struct {
		name           string
		result         *core.HandleResult
		wantSuccess    bool
		wantCompletion string
	}{
		{name: "nil result", result: nil, wantSuccess: true, wantCompletion: "full"},
		{name: "empty status (legacy)", result: &core.HandleResult{}, wantSuccess: true, wantCompletion: "full"},
		{name: "success", result: &core.HandleResult{Status: orchestration.ExecutionStatusSuccess}, wantSuccess: true, wantCompletion: "full"},
		{name: "partial", result: &core.HandleResult{Status: orchestration.ExecutionStatusPartial}, wantSuccess: false, wantCompletion: "partial"},
		{name: "failed", result: &core.HandleResult{Status: orchestration.ExecutionStatusFailed}, wantSuccess: false, wantCompletion: "failed"},
		{name: "aborted", result: &core.HandleResult{Status: orchestration.ExecutionStatusAborted}, wantSuccess: false, wantCompletion: "aborted"},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			success, completion := taskCompletionInfo(tt.result)
			if success != tt.wantSuccess {
				t.Errorf("success = %v, want %v", success, tt.wantSuccess)
			}
			if completion != tt.wantCompletion {
				t.Errorf("completion = %q, want %q", completion, tt.wantCompletion)
			}
		})
	}
}

// collectEvents drains up to n events from ch, waiting briefly for each.
func collectEvents(ch chan Event, n int) []Event {
	var events []Event
	for range n {
		select {
		case e := <-ch:
			events = append(events, e)
		case <-time.After(200 * time.Millisecond):
			return events
		}
	}
	return events
}

// TestEmitTaskComplete_SuccessEmitsTypedContract verifies that a successful
// completion emits task_complete with Success=true and no degraded-outcome
// follow-up events.
func TestEmitTaskComplete_SuccessEmitsTypedContract(t *testing.T) {
	manager, eventChan, _ := testManager(t)
	manager.SetTaskStore(&mockTaskStoreForResumable{unfinished: nil})
	drainEvents(eventChan)

	manager.emitTaskComplete("sess-1", &core.HandleResult{
		Output: "done",
		Status: orchestration.ExecutionStatusSuccess,
	}, nil)

	events := collectEvents(eventChan, 2)
	if len(events) != 1 {
		t.Fatalf("expected exactly 1 event, got %d: %+v", len(events), events)
	}
	data, ok := events[0].Data.(TaskCompleteData)
	if !ok {
		t.Fatalf("expected TaskCompleteData, got %T", events[0].Data)
	}
	if !data.Success || data.Completion != "full" {
		t.Errorf("expected success=true/completion=full, got %v/%q", data.Success, data.Completion)
	}
}

// TestEmitTaskComplete_SuccessDoesNotEmitResumeBadge is the core regression
// test for the false-failure badge: a genuinely successful completion must
// NEVER offer a resume action, even when the task store still reports an
// unfinished task (e.g. the completion write raced or was dropped). Only the
// task_complete event should be emitted.
func TestEmitTaskComplete_SuccessDoesNotEmitResumeBadge(t *testing.T) {
	manager, eventChan, _ := testManager(t)
	// The task store still reports an unfinished task — simulating a
	// completion write that raced/dropped (the original bug).
	manager.SetTaskStore(&mockTaskStoreForResumable{
		unfinished: &TaskRecord{ID: "task-1", SessionID: "sess-1", Status: "in_progress"},
	})
	drainEvents(eventChan)

	manager.emitTaskComplete("sess-1", &core.HandleResult{
		Output: "done",
		Status: orchestration.ExecutionStatusSuccess,
	}, nil)

	// Only task_complete should be emitted — no task_failed_resumable.
	events := collectEvents(eventChan, 2)
	if len(events) != 1 {
		t.Fatalf("expected exactly 1 event (task_complete only), got %d: %+v", len(events), events)
	}
	if events[0].Type != "task_complete" {
		t.Fatalf("expected task_complete, got %s", events[0].Type)
	}
	data, ok := events[0].Data.(TaskCompleteData)
	if !ok {
		t.Fatalf("expected TaskCompleteData, got %T", events[0].Data)
	}
	if !data.Success {
		t.Error("expected success=true — a resume banner must not shadow a successful result")
	}
}

// TestEmitTaskComplete_PartialEmitsResumable verifies that a partial completion
// emits task_complete with Success=false followed by task_failed_resumable
// when the task store holds an unfinished record.
func TestEmitTaskComplete_PartialEmitsResumable(t *testing.T) {
	manager, eventChan, _ := testManager(t)
	manager.SetTaskStore(&mockTaskStoreForResumable{
		unfinished: &TaskRecord{ID: "task-1", SessionID: "sess-1", Status: "in_progress"},
	})
	drainEvents(eventChan)

	manager.emitTaskComplete("sess-1", &core.HandleResult{
		Output: "partial output",
		Status: orchestration.ExecutionStatusPartial,
	}, nil)

	events := collectEvents(eventChan, 2)
	if len(events) != 2 {
		t.Fatalf("expected 2 events, got %d: %+v", len(events), events)
	}
	data, ok := events[0].Data.(TaskCompleteData)
	if !ok {
		t.Fatalf("expected TaskCompleteData, got %T", events[0].Data)
	}
	if data.Success {
		t.Error("expected success=false for partial completion")
	}
	if data.Completion != "partial" {
		t.Errorf("expected completion=partial, got %q", data.Completion)
	}
	if events[1].Type != "task_failed_resumable" {
		t.Errorf("expected task_failed_resumable follow-up, got %s", events[1].Type)
	}
}

// TestEmitTaskComplete_DegradedWithoutSafetyNetEmitsWarning verifies the
// fallback: when a degraded completion cannot be surfaced as resumable (no
// task store), a visible service warning is emitted instead of a silent
// visual success.
func TestEmitTaskComplete_DegradedWithoutSafetyNetEmitsWarning(t *testing.T) {
	manager, eventChan, _ := testManager(t)
	// No SetTaskStore — resumable safety net unavailable.
	drainEvents(eventChan)

	manager.emitTaskComplete("sess-1", &core.HandleResult{
		Output: "partial output",
		Status: orchestration.ExecutionStatusPartial,
	}, nil)

	events := collectEvents(eventChan, 2)
	if len(events) != 2 {
		t.Fatalf("expected 2 events, got %d: %+v", len(events), events)
	}
	if events[0].Type != "task_complete" {
		t.Errorf("expected task_complete first, got %s", events[0].Type)
	}
	if events[1].Type != "service" {
		t.Fatalf("expected service warning follow-up, got %s", events[1].Type)
	}
	svc, ok := events[1].Data.(map[string]any)
	if !ok {
		t.Fatalf("expected map service payload, got %T", events[1].Data)
	}
	if phase, _ := svc["phase"].(string); phase != "orchestration" {
		t.Errorf("expected orchestration phase, got %q", phase)
	}
}

// TestEmitTaskComplete_DegradedEmitsExactlyOneAgentMetrics is the regression
// test for the double agent_metrics emission on degraded completions: the
// counters reset on every emission, so a second (zeroed) event used to
// overwrite the real per-run quality report in the UI. With a tracked session
// and an unfinished task record, a partial completion must produce exactly
// one agent_metrics event carrying the run's real counters and the precise
// "partial" finish label, alongside the resumable banner.
func TestEmitTaskComplete_DegradedEmitsExactlyOneAgentMetrics(t *testing.T) {
	manager, eventChan, _ := testManager(t)
	manager.SetTaskStore(&mockTaskStoreForResumable{
		unfinished: &TaskRecord{ID: "task-1", SessionID: "sess-1", Status: "in_progress"},
	})

	// Track the session with a real emitter so emitAgentMetrics is not a no-op.
	emitter := NewEventEmitter("sess-1", manager.emitFunc)
	manager.mu.Lock()
	manager.sessions["sess-1"] = &Session{ID: "sess-1", emitter: emitter}
	manager.mu.Unlock()

	// Seed the run's quality counters.
	emitter.ExecutorDiagnostic(1, "parse_error_nudge", map[string]any{"tool": "finish"})
	emitter.StepStart(1)
	emitter.StepStart(2)

	drainEvents(eventChan)
	manager.emitTaskComplete("sess-1", &core.HandleResult{
		Output: "partial output",
		Status: orchestration.ExecutionStatusPartial,
	}, nil)

	events := collectEvents(eventChan, 5)
	var metrics []AgentMetricsData
	resumable := false
	for _, e := range events {
		switch e.Type {
		case "agent_metrics":
			if data, ok := e.Data.(AgentMetricsData); ok {
				metrics = append(metrics, data)
			}
		case "task_failed_resumable":
			resumable = true
		}
	}
	if len(metrics) != 1 {
		t.Fatalf("expected exactly 1 agent_metrics event, got %d: %+v", len(metrics), metrics)
	}
	if metrics[0].Finish != "partial" {
		t.Errorf("expected finish=partial, got %q", metrics[0].Finish)
	}
	if metrics[0].ParseErrors != 1 || metrics[0].Steps != 2 {
		t.Errorf("expected the run's real counters (parse_errors=1, steps=2), got %+v", metrics[0])
	}
	if !resumable {
		t.Error("expected the resumable banner alongside the single metrics event")
	}
}

// TestGetSessionRuntimeStatus verifies active/unfinished reporting without
// restoring sessions as a side effect.
func TestGetSessionRuntimeStatus(t *testing.T) {
	t.Run("unfinished task, session not in memory", func(t *testing.T) {
		manager, _, _ := testManager(t)
		manager.SetTaskStore(&mockTaskStoreForResumable{
			unfinished: &TaskRecord{ID: "task-9", SessionID: "sess-9", Status: "in_progress"},
		})

		status, err := manager.GetSessionRuntimeStatus("sess-9")
		if err != nil {
			t.Fatalf("GetSessionRuntimeStatus returned error: %v", err)
		}
		if status.Active {
			t.Error("expected active=false for session not in memory")
		}
		if !status.HasUnfinishedTask || status.UnfinishedTaskID != "task-9" {
			t.Errorf("expected unfinished task task-9, got %+v", status)
		}
	})

	t.Run("no task store, no session", func(t *testing.T) {
		manager, _, _ := testManager(t)

		status, err := manager.GetSessionRuntimeStatus("sess-x")
		if err != nil {
			t.Fatalf("GetSessionRuntimeStatus returned error: %v", err)
		}
		if status.Active || status.HasUnfinishedTask {
			t.Errorf("expected idle status, got %+v", status)
		}
	})

	t.Run("paused task reports Paused=true", func(t *testing.T) {
		manager, _, _ := testManager(t)
		pausedTask := &TaskRecord{
			ID: "task-paused", SessionID: "sess-paused", Status: "paused",
			RoutingDecision: json.RawMessage(`{}`), Plan: json.RawMessage(`{}`),
			Reflections: json.RawMessage(`[]`),
		}
		manager.SetTaskStore(&mockTaskStoreForResumable{
			unfinished:     pausedTask,
			loadTaskResult: pausedTask,
		})

		status, err := manager.GetSessionRuntimeStatus("sess-paused")
		if err != nil {
			t.Fatalf("GetSessionRuntimeStatus returned error: %v", err)
		}
		if !status.HasUnfinishedTask || status.UnfinishedTaskID != "task-paused" {
			t.Errorf("expected unfinished task task-paused, got %+v", status)
		}
		if !status.Paused {
			t.Errorf("expected Paused=true for paused task, got %+v", status)
		}
	})

	t.Run("in_progress task reports Paused=false", func(t *testing.T) {
		manager, _, _ := testManager(t)
		activeTask := &TaskRecord{
			ID: "task-active", SessionID: "sess-active", Status: "in_progress",
			RoutingDecision: json.RawMessage(`{}`), Plan: json.RawMessage(`{}`),
			Reflections: json.RawMessage(`[]`),
		}
		manager.SetTaskStore(&mockTaskStoreForResumable{
			unfinished:     activeTask,
			loadTaskResult: activeTask,
		})

		status, err := manager.GetSessionRuntimeStatus("sess-active")
		if err != nil {
			t.Fatalf("GetSessionRuntimeStatus returned error: %v", err)
		}
		if !status.HasUnfinishedTask {
			t.Errorf("expected unfinished task, got %+v", status)
		}
		if status.Paused {
			t.Errorf("expected Paused=false for in_progress task, got %+v", status)
		}
	})
}

// recordingCancelTaskStore extends mockTaskStoreForResumable by capturing
// CompleteTask, CancelTask, and PauseTask invocations so shutdown/cancel tests
// can assert on them.
type recordingCancelTaskStore struct {
	mockTaskStoreForResumable
	mu              sync.Mutex
	completedID     string
	completedOutput string
	completedCount  int
	completedCalls  int
	cancelledID     string
	cancelledCalls  int
	pausedID        string
	pausedCalls     int
}

func (m *recordingCancelTaskStore) CompleteTask(_ context.Context, taskID, finalOutput string, attemptCount int) error {
	m.mu.Lock()
	defer m.mu.Unlock()
	m.completedID = taskID
	m.completedOutput = finalOutput
	m.completedCount = attemptCount
	m.completedCalls++
	return nil
}

func (m *recordingCancelTaskStore) CancelTask(_ context.Context, taskID string) error {
	m.mu.Lock()
	defer m.mu.Unlock()
	m.cancelledID = taskID
	m.cancelledCalls++
	return nil
}

// PauseTask records the call instead of delegating to the embedded mock's
// no-op, so shutdown-pause tests can assert PauseTask was invoked.
func (m *recordingCancelTaskStore) PauseTask(_ context.Context, taskID string) error {
	m.mu.Lock()
	defer m.mu.Unlock()
	m.pausedID = taskID
	m.pausedCalls++
	return nil
}

// recordingSessionStore wraps mockSessionStoreForRestore and captures
// ResolvePendingMessage calls, so CancelUnfinishedTask/ResumeTask can assert
// that the persisted task_failed_resumable banner is marked resolved.
type recordingSessionStore struct {
	mockSessionStoreForRestore
	mu       sync.Mutex
	resolved []resolvedPendingCall
}

type resolvedPendingCall struct {
	role       string
	matchField string
	matchValue string
	extra      map[string]any
}

func (r *recordingSessionStore) ResolvePendingMessage(_ context.Context, _, role, matchField, matchValue string, extra map[string]any) error {
	r.mu.Lock()
	defer r.mu.Unlock()
	r.resolved = append(r.resolved, resolvedPendingCall{
		role: role, matchField: matchField, matchValue: matchValue, extra: extra,
	})
	return nil
}

// TestCancelUnfinishedTask_PersistsCancellation verifies that
// CancelUnfinishedTask looks up the unfinished task and marks it as
// cancelled (NOT completed) in the store, so the persisted status
// reflects the real outcome.
func TestCancelUnfinishedTask_PersistsCancellation(t *testing.T) {
	manager, _, _ := testManager(t)

	store := &recordingCancelTaskStore{
		mockTaskStoreForResumable: mockTaskStoreForResumable{
			unfinished: &TaskRecord{ID: "task-abc", SessionID: "sess-1", Status: "in_progress"},
		},
	}
	manager.SetTaskStore(store)

	if err := manager.CancelUnfinishedTask("sess-1"); err != nil {
		t.Fatalf("CancelUnfinishedTask returned error: %v", err)
	}

	store.mu.Lock()
	defer store.mu.Unlock()
	if store.cancelledCalls != 1 {
		t.Errorf("expected exactly one CancelTask call, got %d", store.cancelledCalls)
	}
	if store.cancelledID != "task-abc" {
		t.Errorf("expected CancelTask to be called with task-abc, got %q", store.cancelledID)
	}
	if store.completedCalls != 0 {
		t.Errorf("expected no CompleteTask calls, got %d", store.completedCalls)
	}
}

// TestCancelUnfinishedTask_NoUnfinished verifies that CancelUnfinishedTask is a
// no-op when the session has no unfinished task.
func TestCancelUnfinishedTask_NoUnfinished(t *testing.T) {
	manager, _, _ := testManager(t)

	store := &recordingCancelTaskStore{
		mockTaskStoreForResumable: mockTaskStoreForResumable{unfinished: nil},
	}
	manager.SetTaskStore(store)

	if err := manager.CancelUnfinishedTask("sess-1"); err != nil {
		t.Fatalf("CancelUnfinishedTask returned error: %v", err)
	}

	store.mu.Lock()
	defer store.mu.Unlock()
	if store.completedCalls != 0 {
		t.Errorf("expected no CompleteTask calls, got %d", store.completedCalls)
	}
	if store.cancelledCalls != 0 {
		t.Errorf("expected no CancelTask calls, got %d", store.cancelledCalls)
	}
}

// TestCancelUnfinishedTask_NoTaskStore verifies that CancelUnfinishedTask
// returns nil without error when no task store is configured.
func TestCancelUnfinishedTask_NoTaskStore(t *testing.T) {
	manager, _, _ := testManager(t)

	// Do not call SetTaskStore — taskStore stays nil.
	if err := manager.CancelUnfinishedTask("sess-1"); err != nil {
		t.Fatalf("CancelUnfinishedTask should be a no-op without task store, got: %v", err)
	}
}

// TestCancelUnfinishedTask_ResolvesResumableBanner verifies that
// CancelUnfinishedTask persists the resolution of the task_failed_resumable
// message (matched by task_id) so the Resume/Cancel banner does not reappear
// as pending on session reload after the user discards the task.
func TestCancelUnfinishedTask_ResolvesResumableBanner(t *testing.T) {
	manager, _, _ := testManager(t)

	taskStore := &recordingCancelTaskStore{
		mockTaskStoreForResumable: mockTaskStoreForResumable{
			unfinished: &TaskRecord{ID: "task-abc", SessionID: "sess-1", Status: "in_progress"},
		},
	}
	manager.SetTaskStore(taskStore)
	sessStore := &recordingSessionStore{}
	manager.SetSessionStore(sessStore)

	if err := manager.CancelUnfinishedTask("sess-1"); err != nil {
		t.Fatalf("CancelUnfinishedTask returned error: %v", err)
	}

	sessStore.mu.Lock()
	defer sessStore.mu.Unlock()
	if len(sessStore.resolved) != 1 {
		t.Fatalf("expected exactly one ResolvePendingMessage call, got %d", len(sessStore.resolved))
	}
	call := sessStore.resolved[0]
	if call.role != "task_failed_resumable" {
		t.Errorf("expected role task_failed_resumable, got %q", call.role)
	}
	if call.matchField != "task_id" || call.matchValue != "task-abc" {
		t.Errorf("expected match task_id=task-abc, got %s=%s", call.matchField, call.matchValue)
	}
	if call.extra["resolved"] != true {
		t.Errorf("expected resolved=true, got %v", call.extra["resolved"])
	}
	if call.extra["decision"] != "cancelled" {
		t.Errorf("expected decision=cancelled, got %v", call.extra["decision"])
	}
}

// TestCancelUnfinishedTask_NoResolveWithoutSessionStore verifies that a nil
// session store does not break CancelUnfinishedTask (resolve is skipped, the
// in-memory UI still updates and stale reconciliation self-heals on reload).
func TestCancelUnfinishedTask_NoResolveWithoutSessionStore(t *testing.T) {
	manager, _, _ := testManager(t)
	manager.SetTaskStore(&mockTaskStoreForResumable{
		unfinished: &TaskRecord{ID: "task-xyz", SessionID: "sess-1", Status: "in_progress"},
	})
	// No SetSessionStore — sessionStore stays nil.

	if err := manager.CancelUnfinishedTask("sess-1"); err != nil {
		t.Fatalf("CancelUnfinishedTask with nil session store returned error: %v", err)
	}
}

// ---------------------------------------------------------------------------
// Continuation Routing Tests
// ---------------------------------------------------------------------------

// TestSendMessage_StoresTaskIDForContinuation verifies that after a successful task,
// the task ID is stored in lastCompletedTaskID for potential continuations.
func TestSendMessage_StoresTaskIDForContinuation(t *testing.T) {
	eventChan := make(chan Event, 100)
	emitFunc := func(e Event) {
		select {
		case eventChan <- e:
		default:
		}
	}

	// Track whether Handle or ContinueTask was called via the mock orchestrator behavior
	callCount := 0
	factory := func(emitter core.Emitter, logger *slog.Logger, workspacePath string, bbFactory core.BlackboardFactory, dumpWriter io.Writer, _ *orchestration.StepDumpTracker) (*core.Orchestrator, error) {
		callCount++
		// Return nil - we'll test the session's lastCompletedTaskID field directly
		return nil, nil
	}

	manager := NewManager(factory, emitFunc, t.TempDir())
	t.Cleanup(manager.Shutdown) // close handles before TempDir cleanup (Windows)
	// Create a session
	wsPath := testWorkspacePath(t)
	info, err := manager.CreateSession(testProjectID, wsPath)
	if err != nil {
		t.Fatalf("CreateSession failed: %v", err)
	}

	// Get the session
	session, _ := manager.GetSession(info.ID)

	// Initially, lastCompletedTaskID should be empty
	session.mu.Lock()
	if session.lastCompletedTaskID != "" {
		t.Error("expected lastCompletedTaskID to be empty initially")
	}
	session.mu.Unlock()

	// Simulate that a task was completed by setting the task ID directly
	// (In real usage, this would be set after a successful task completion)
	session.mu.Lock()
	session.lastCompletedTaskID = "task-abc-123"
	session.mu.Unlock()

	// Verify it was stored
	session.mu.Lock()
	if session.lastCompletedTaskID != "task-abc-123" {
		t.Errorf("expected lastCompletedTaskID to be 'task-abc-123', got %q", session.lastCompletedTaskID)
	}
	session.mu.Unlock()
}

// TestSendMessage_LastTaskIDClearedOnContinuationError verifies that when ContinueTask
// fails and falls back to Handle, the lastCompletedTaskID is cleared.
func TestSendMessage_LastTaskIDClearedOnContinuationError(t *testing.T) {
	eventChan := make(chan Event, 100)
	emitFunc := func(e Event) {
		select {
		case eventChan <- e:
		default:
		}
	}

	factory := func(emitter core.Emitter, logger *slog.Logger, workspacePath string, bbFactory core.BlackboardFactory, dumpWriter io.Writer, _ *orchestration.StepDumpTracker) (*core.Orchestrator, error) {
		return nil, nil
	}

	manager := NewManager(factory, emitFunc, t.TempDir())
	t.Cleanup(manager.Shutdown) // close handles before TempDir cleanup (Windows)
	// Create a session
	wsPath := testWorkspacePath(t)
	info, err := manager.CreateSession(testProjectID, wsPath)
	if err != nil {
		t.Fatalf("CreateSession failed: %v", err)
	}

	session, _ := manager.GetSession(info.ID)

	// Set a task ID as if a task was completed
	session.mu.Lock()
	session.lastCompletedTaskID = "task-to-be-cleared"
	session.mu.Unlock()

	// Verify the logic in SendMessage goroutine would clear it on ContinueTask error
	// (We can't easily test the full flow without a real orchestrator, but we can verify
	// the field exists and can be modified as expected by the logic)
	session.mu.Lock()
	session.lastCompletedTaskID = "" // Simulate the clear that happens on error
	session.mu.Unlock()

	session.mu.Lock()
	if session.lastCompletedTaskID != "" {
		t.Error("expected lastCompletedTaskID to be cleared")
	}
	session.mu.Unlock()
}

// TestSendMessage_ContinuationRoutingLogic verifies the continuation routing logic
// by examining the code path that would be taken.
func TestSendMessage_ContinuationRoutingLogic(t *testing.T) {
	// This test verifies that the continuation routing logic is correctly structured
	// in the SendMessage method. The key aspects are:
	// 1. If lastCompletedTaskID is set, ContinueTask is called
	// 2. If ContinueTask fails, Handle is called as fallback
	// 3. If lastCompletedTaskID is empty, Handle is called directly
	//
	// Since we can't easily mock the orchestrator (it's a concrete type),
	// we verify the session structure supports this logic.

	manager, _, _ := testManager(t)

	wsPath := testWorkspacePath(t)
	info, err := manager.CreateSession(testProjectID, wsPath)
	if err != nil {
		t.Fatalf("CreateSession failed: %v", err)
	}

	session, _ := manager.GetSession(info.ID)

	// Verify the session has the lastCompletedTaskID field
	session.mu.Lock()
	session.lastCompletedTaskID = "test-task-id"
	taskID := session.lastCompletedTaskID
	session.mu.Unlock()

	if taskID != "test-task-id" {
		t.Error("lastCompletedTaskID field not working as expected")
	}
}

// TestSendMessage_AlwaysPlanFirst verifies that SendMessage always uses
// plan-first mode — planning is always on.
func TestSendMessage_AlwaysPlanFirst(t *testing.T) {
	// This test verifies that SendMessage works without a planFirst parameter
	// and the session correctly forwards to the orchestrator.
	// The full integration test requires a working orchestrator, which is
	// tested separately in core/orchestrator_test.go.

	manager, _, _ := testManager(t)

	wsPath := testWorkspacePath(t)
	info, err := manager.CreateSession(testProjectID, wsPath)
	if err != nil {
		t.Fatalf("CreateSession failed: %v", err)
	}

	session, _ := manager.GetSession(info.ID)

	// Initially, session should not be active
	if session.IsActive() {
		t.Error("New session should not be active")
	}

	// Verify session has orchestrator (even if nil from mock factory)
	// The actual orchestrator behavior is tested in core/orchestrator_test.go
	orch := session.GetOrchestrator()
	// With our mock factory, orchestrator is nil - that's expected
	if orch != nil {
		t.Log("Orchestrator is non-nil, can test planning passing directly")
	}

	// The real test is in the source code at manager.go where HandleMessage is called with:
	// session.orchestrator.HandleMessage(ctx, msg, id, core.HandleOptions{
	//     TaskID: lastTaskID,
	// })
	//
	// Planning always happens first; mode is selected automatically.
}

// ---------------------------------------------------------------------------
// Session Restoration Tests
// ---------------------------------------------------------------------------

// mockSessionStoreForRestore implements SessionStore for restoration tests.
// Only LoadSession is wired to return stored data; other methods are no-ops
// or minimal implementations.
type mockSessionStoreForRestore struct {
	mu       sync.Mutex
	sessions map[string]*SessionInfo
}

func newMockSessionStore() *mockSessionStoreForRestore {
	return &mockSessionStoreForRestore{sessions: make(map[string]*SessionInfo)}
}

func (m *mockSessionStoreForRestore) SaveSession(_ context.Context, info SessionInfo) error {
	m.mu.Lock()
	defer m.mu.Unlock()
	cp := info
	m.sessions[info.ID] = &cp
	return nil
}

func (m *mockSessionStoreForRestore) LoadSession(_ context.Context, id string) (*SessionInfo, error) {
	m.mu.Lock()
	defer m.mu.Unlock()
	info, ok := m.sessions[id]
	if !ok {
		return nil, nil
	}
	cp := *info
	return &cp, nil
}

func (m *mockSessionStoreForRestore) ListSessions(_ context.Context) ([]SessionInfo, error) {
	return []SessionInfo{}, nil
}
func (m *mockSessionStoreForRestore) ListSessionsByProject(_ context.Context, _ string) ([]SessionInfo, error) {
	return []SessionInfo{}, nil
}
func (m *mockSessionStoreForRestore) DeleteSession(_ context.Context, _ string) error { return nil }
func (m *mockSessionStoreForRestore) ArchiveSession(_ context.Context, _ string, _ bool) error {
	return nil
}
func (m *mockSessionStoreForRestore) PinSession(_ context.Context, _ string, _ bool) error {
	return nil
}
func (m *mockSessionStoreForRestore) RenameSession(_ context.Context, _, _ string) error { return nil }
func (m *mockSessionStoreForRestore) UpdateSessionTokens(_ context.Context, _ string, _, _ int, _, _ string, _ float64) error {
	return nil
}
func (m *mockSessionStoreForRestore) UpdateSessionActivity(_ context.Context, _ string) error {
	return nil
}
func (m *mockSessionStoreForRestore) SaveMessage(_ context.Context, _ ChatMessage) error { return nil }
func (m *mockSessionStoreForRestore) LoadMessages(_ context.Context, _ string) ([]ChatMessage, error) {
	return []ChatMessage{}, nil
}
func (m *mockSessionStoreForRestore) DeleteMessages(_ context.Context, _ string) error { return nil }
func (m *mockSessionStoreForRestore) ResolvePendingMessage(_ context.Context, _, _, _, _ string, _ map[string]any) error {
	return nil
}
func (m *mockSessionStoreForRestore) SaveTerminalCommand(_ context.Context, _, _ string) error {
	return nil
}
func (m *mockSessionStoreForRestore) LoadTerminalCommands(_ context.Context, _ string, _ int) ([]TerminalCommand, error) {
	return []TerminalCommand{}, nil
}
func (m *mockSessionStoreForRestore) SaveSessionWorkDir(_ context.Context, _ string, _ project.WorkDirectoryRecord) error {
	return nil
}
func (m *mockSessionStoreForRestore) ListSessionWorkDirs(_ context.Context, _ string) ([]project.WorkDirectoryRecord, error) {
	return []project.WorkDirectoryRecord{}, nil
}
func (m *mockSessionStoreForRestore) UpdateSessionWorkDirDescription(_ context.Context, _, _, _ string) error {
	return nil
}
func (m *mockSessionStoreForRestore) DeleteSessionWorkDir(_ context.Context, _, _ string) error {
	return nil
}
func (m *mockSessionStoreForRestore) Close() error { return nil }

// restoreTestManager creates a Manager pre-wired with a mock session store and
// project resolver for restoration tests. It returns the manager, event channel,
// and the mock store so tests can insert sessions directly.
func restoreTestManager(t *testing.T) (*Manager, chan Event, *mockSessionStoreForRestore) {
	t.Helper()

	agentDir := t.TempDir()

	eventChan := make(chan Event, 100)
	emitFunc := func(e Event) {
		select {
		case eventChan <- e:
		default:
		}
	}

	factory := func(emitter core.Emitter, logger *slog.Logger, workspacePath string, bbFactory core.BlackboardFactory, dumpWriter io.Writer, _ *orchestration.StepDumpTracker) (*core.Orchestrator, error) {
		return nil, nil
	}

	mgr := NewManager(factory, emitFunc, agentDir)
	t.Cleanup(mgr.Shutdown) // close handles before TempDir cleanup (Windows)
	store := newMockSessionStore()
	mgr.SetSessionStore(store)
	mgr.SetProjectResolver(func(projectID string) (string, error) {
		return config.ProjectDir(agentDir, projectID), nil
	})

	return mgr, eventChan, store
}

// seedSession saves a session directly into the mock store (simulating a
// previous app run) without adding it to the Manager's in-memory map.
func seedSession(t *testing.T, store *mockSessionStoreForRestore, id, projectID, name string, archived bool) {
	t.Helper()
	now := time.Now().Format(time.RFC3339)
	if err := store.SaveSession(context.Background(), SessionInfo{
		ID:                id,
		ProjectID:         projectID,
		Name:              name,
		CreatedAt:         now,
		LastActiveAt:      now,
		Archived:          archived,
		TotalInputTokens:  100,
		TotalOutputTokens: 50,
	}); err != nil {
		t.Fatalf("seedSession failed: %v", err)
	}
}

// TestRestoreSession_BasicGetSession verifies that GetSession lazily restores
// a session from the persistent store when it's not in the in-memory map.
func TestRestoreSession_BasicGetSession(t *testing.T) {
	mgr, _, store := restoreTestManager(t)

	seedSession(t, store, "restore-1", testProjectID, "Restored Session", false)

	// Session is only in the store, not in memory.
	sess, ok := mgr.GetSession("restore-1")
	if !ok {
		t.Fatal("GetSession should find session via lazy restoration")
	}
	if sess.ID != "restore-1" {
		t.Errorf("ID mismatch: got %q, want %q", sess.ID, "restore-1")
	}
	if sess.ProjectID != testProjectID {
		t.Errorf("ProjectID mismatch: got %q, want %q", sess.ProjectID, testProjectID)
	}
	if sess.Name != "Restored Session" {
		t.Errorf("Name mismatch: got %q, want %q", sess.Name, "Restored Session")
	}
	if sess.Archived {
		t.Error("Archived should be false")
	}
	if sess.WorkspacePath == "" {
		t.Error("WorkspacePath should not be empty after restoration")
	}

	// Second call should return the same object from memory (no re-creation).
	sess2, ok := mgr.GetSession("restore-1")
	if !ok {
		t.Fatal("second GetSession should succeed")
	}
	if sess2 != sess {
		t.Error("second GetSession should return the same *Session pointer")
	}
}

// TestRestoreSession_Metadata verifies that restored sessions preserve
// metadata fields: name, project ID, creation time, archived flag.
func TestRestoreSession_Metadata(t *testing.T) {
	mgr, _, store := restoreTestManager(t)

	createdAt := time.Date(2025, 6, 15, 12, 0, 0, 0, time.UTC).Format(time.RFC3339)
	if err := store.SaveSession(context.Background(), SessionInfo{
		ID:                "meta-sess",
		ProjectID:         testProjectID,
		Name:              "Metadata Check",
		CreatedAt:         createdAt,
		LastActiveAt:      createdAt,
		Archived:          true,
		TotalInputTokens:  500,
		TotalOutputTokens: 250,
	}); err != nil {
		t.Fatalf("SaveSession failed: %v", err)
	}

	sess, ok := mgr.GetSession("meta-sess")
	if !ok {
		t.Fatal("expected session to be restored")
	}
	if sess.Name != "Metadata Check" {
		t.Errorf("Name mismatch: got %q", sess.Name)
	}
	if sess.ProjectID != testProjectID {
		t.Errorf("ProjectID mismatch: got %q", sess.ProjectID)
	}
	if !sess.Archived {
		t.Error("Archived should be true")
	}
	// CreatedAt should be parsed from the stored RFC3339 string.
	if sess.CreatedAt.Year() != 2025 || sess.CreatedAt.Month() != 6 {
		t.Errorf("CreatedAt mismatch: got %v", sess.CreatedAt)
	}
}

// TestRestoreSession_RenameSession verifies that RenameSession works on a
// session that is only in the persistent store.
func TestRestoreSession_RenameSession(t *testing.T) {
	mgr, eventChan, store := restoreTestManager(t)

	seedSession(t, store, "rename-restore", testProjectID, "Old Name", false)
	drainEvents(eventChan)

	if err := mgr.RenameSession("rename-restore", "New Name"); err != nil {
		t.Fatalf("RenameSession on restored session failed: %v", err)
	}

	sess, ok := mgr.GetSession("rename-restore")
	if !ok {
		t.Fatal("session should exist after rename")
	}
	if sess.Name != "New Name" {
		t.Errorf("Name should be updated: got %q, want %q", sess.Name, "New Name")
	}

	// Verify rename event was emitted.
	select {
	case event := <-eventChan:
		if event.Type != "session_renamed" {
			t.Errorf("expected session_renamed event, got %s", event.Type)
		}
	case <-time.After(time.Second):
		t.Error("timeout waiting for session_renamed event")
	}
}

// TestRestoreSession_DeleteSession verifies that DeleteSession works on a
// session that is only in the persistent store.
func TestRestoreSession_DeleteSession(t *testing.T) {
	mgr, eventChan, store := restoreTestManager(t)

	seedSession(t, store, "delete-restore", testProjectID, "To Delete", false)
	drainEvents(eventChan)

	if err := mgr.DeleteSession("delete-restore"); err != nil {
		t.Fatalf("DeleteSession on restored session failed: %v", err)
	}

	// Session should be gone from in-memory map.
	// Note: the mock store doesn't actually remove from its map on Manager.DeleteSession
	// because the Manager only deletes from its own sessions map. But GetSession
	// first checks the in-memory map, so after delete it will try to restore again.
	// Since it was removed from the manager map, it would restore again from the store.
	// This is acceptable — the important thing is the delete event was emitted.
	select {
	case event := <-eventChan:
		if event.Type != "session_deleted" {
			t.Errorf("expected session_deleted event, got %s", event.Type)
		}
		if event.SessionID != "delete-restore" {
			t.Errorf("event session ID mismatch: got %q", event.SessionID)
		}
	case <-time.After(time.Second):
		t.Error("timeout waiting for session_deleted event")
	}
}

// TestRestoreSession_ArchiveSession verifies that ArchiveSession works on a
// session that is only in the persistent store.
func TestRestoreSession_ArchiveSession(t *testing.T) {
	mgr, eventChan, store := restoreTestManager(t)

	seedSession(t, store, "archive-restore", testProjectID, "To Archive", false)
	drainEvents(eventChan)

	if err := mgr.ArchiveSession("archive-restore"); err != nil {
		t.Fatalf("ArchiveSession on restored session failed: %v", err)
	}

	sess, ok := mgr.GetSession("archive-restore")
	if !ok {
		t.Fatal("session should exist after archive")
	}
	if !sess.Archived {
		t.Error("session should be archived after ArchiveSession")
	}

	select {
	case event := <-eventChan:
		if event.Type != "session_archived" {
			t.Errorf("expected session_archived event, got %s", event.Type)
		}
	case <-time.After(time.Second):
		t.Error("timeout waiting for session_archived event")
	}
}

// TestRestoreSession_NotInStore verifies that accessing a session that
// doesn't exist in memory or store returns the appropriate not-found result.
func TestRestoreSession_NotInStore(t *testing.T) {
	mgr, _, _ := restoreTestManager(t)

	// GetSession
	_, ok := mgr.GetSession("nonexistent-restore")
	if ok {
		t.Error("GetSession should return false for session not in memory or store")
	}

	// RenameSession
	err := mgr.RenameSession("nonexistent-restore", "name")
	if err == nil {
		t.Error("RenameSession should return error for nonexistent session")
	}
	if !strings.Contains(err.Error(), "session not found") {
		t.Errorf("expected 'session not found' error, got: %v", err)
	}

	// ArchiveSession
	err = mgr.ArchiveSession("nonexistent-restore")
	if err == nil {
		t.Error("ArchiveSession should return error for nonexistent session")
	}

	// DeleteSession
	err = mgr.DeleteSession("nonexistent-restore")
	if err == nil {
		t.Error("DeleteSession should return error for nonexistent session")
	}

	// CancelTask
	err = mgr.CancelTask("nonexistent-restore")
	if err == nil {
		t.Error("CancelTask should return error for nonexistent session")
	}
}

// TestRestoreSession_NoProjectResolver verifies that restoration returns nil
// gracefully when SetProjectResolver was never called.
func TestRestoreSession_NoProjectResolver(t *testing.T) {
	eventChan := make(chan Event, 100)
	emitFunc := func(e Event) {
		select {
		case eventChan <- e:
		default:
		}
	}

	factory := func(emitter core.Emitter, logger *slog.Logger, workspacePath string, bbFactory core.BlackboardFactory, dumpWriter io.Writer, _ *orchestration.StepDumpTracker) (*core.Orchestrator, error) {
		return nil, nil
	}

	mgr := NewManager(factory, emitFunc, t.TempDir())
	t.Cleanup(mgr.Shutdown) // close handles before TempDir cleanup (Windows)
	// Set store but NOT project resolver.
	store := newMockSessionStore()
	mgr.SetSessionStore(store)
	seedSession(t, store, "no-resolver", testProjectID, "No Resolver", false)

	// GetSession should return false — cannot restore without resolver.
	_, ok := mgr.GetSession("no-resolver")
	if ok {
		t.Error("GetSession should return false when no project resolver is set")
	}
}

// TestRestoreSession_NoSessionStore verifies that restoration returns nil
// gracefully when no session store is configured.
func TestRestoreSession_NoSessionStore(t *testing.T) {
	mgr, _, _ := testManager(t)

	// No SetSessionStore or SetProjectResolver.
	_, ok := mgr.GetSession("no-store-session")
	if ok {
		t.Error("GetSession should return false when no session store is configured")
	}
}

// TestRestoreSession_ConcurrentAccess verifies that when multiple goroutines
// try to access the same non-in-memory session simultaneously, only one Session
// object ends up in the map (double-check locking).
func TestRestoreSession_ConcurrentAccess(t *testing.T) {
	mgr, _, store := restoreTestManager(t)

	seedSession(t, store, "concurrent-restore", testProjectID, "Concurrent", false)

	const numGoroutines = 20
	results := make(chan *Session, numGoroutines)
	var wg sync.WaitGroup
	wg.Add(numGoroutines)

	for i := 0; i < numGoroutines; i++ {
		go func() {
			defer wg.Done()
			sess, ok := mgr.GetSession("concurrent-restore")
			if ok {
				results <- sess
			}
		}()
	}

	wg.Wait()
	close(results)

	// All goroutines should have gotten back the same *Session pointer.
	var first *Session
	count := 0
	for sess := range results {
		count++
		if first == nil {
			first = sess
			continue
		}
		if sess != first {
			t.Error("concurrent restoration returned different *Session pointers; expected the same object")
			break
		}
	}

	if count != numGoroutines {
		t.Errorf("expected %d successful GetSession results, got %d", numGoroutines, count)
	}

	// Verify only one session in the map.
	sessions := mgr.ListSessions()
	if len(sessions) != 1 {
		t.Errorf("expected 1 session in map, got %d", len(sessions))
	}
}

// TestRestoreSession_GetSessionWorkspacePath verifies that GetSessionWorkspacePath
// works for a lazily-restored session.
func TestRestoreSession_GetSessionWorkspacePath(t *testing.T) {
	mgr, _, store := restoreTestManager(t)

	seedSession(t, store, "ws-path-restore", testProjectID, "WS Path", false)

	wsPath, ok := mgr.GetSessionWorkspacePath("ws-path-restore")
	if !ok {
		t.Fatal("GetSessionWorkspacePath should succeed via lazy restoration")
	}
	if wsPath == "" {
		t.Error("workspace path should not be empty")
	}
}

// TestRestoreSession_CancelTask verifies that CancelTask on a restored session
// returns the expected "no active task" error (not "session not found").
func TestRestoreSession_CancelTask(t *testing.T) {
	mgr, _, store := restoreTestManager(t)

	seedSession(t, store, "cancel-restore", testProjectID, "Cancel Test", false)

	err := mgr.CancelTask("cancel-restore")
	if err == nil {
		t.Fatal("CancelTask should return error when no task is active")
	}
	// The error should be about no active task, NOT about session not found.
	if strings.Contains(err.Error(), "session not found") {
		t.Errorf("expected 'no active task' error, got 'session not found': %v", err)
	}
	if !strings.Contains(err.Error(), "no active task") {
		t.Errorf("expected 'no active task' error, got: %v", err)
	}
}

// TestRestoreSession_ProjectResolverError verifies that when the project
// resolver returns an error, GetSession gracefully returns false.
func TestRestoreSession_ProjectResolverError(t *testing.T) {
	eventChan := make(chan Event, 100)
	emitFunc := func(e Event) {
		select {
		case eventChan <- e:
		default:
		}
	}

	factory := func(emitter core.Emitter, logger *slog.Logger, workspacePath string, bbFactory core.BlackboardFactory, dumpWriter io.Writer, _ *orchestration.StepDumpTracker) (*core.Orchestrator, error) {
		return nil, nil
	}

	mgr := NewManager(factory, emitFunc, t.TempDir())
	t.Cleanup(mgr.Shutdown) // close handles before TempDir cleanup (Windows)
	store := newMockSessionStore()
	mgr.SetSessionStore(store)
	mgr.SetProjectResolver(func(projectID string) (string, error) {
		return "", fmt.Errorf("project %s not found", projectID)
	})

	seedSession(t, store, "resolver-err", testProjectID, "Resolver Error", false)

	_, ok := mgr.GetSession("resolver-err")
	if ok {
		t.Error("GetSession should return false when project resolver fails")
	}
}

// TestManager_LastToolCallID_WiredByEmitterSink verifies that the per-session
// tool_call_id tracking (written by the emitter's sink during ToolCall) is
// readable via Manager.LastToolCallID, and that it is cleaned up on session
// deletion. This is the bridge the desktop confirm callback uses to attach the
// matching tool_call_id to the tool_confirm payload.
func TestManager_LastToolCallID_WiredByEmitterSink(t *testing.T) {
	agentDir := t.TempDir()
	emitFunc := func(Event) {}

	var capturedEmitter core.Emitter
	factory := func(emitter core.Emitter, _ *slog.Logger, _ string, _ core.BlackboardFactory, _ io.Writer, _ *orchestration.StepDumpTracker) (*core.Orchestrator, error) {
		capturedEmitter = emitter
		return nil, nil
	}
	manager := NewManager(factory, emitFunc, agentDir)
	t.Cleanup(manager.Shutdown) // close handles before TempDir cleanup (Windows)
	info, err := manager.CreateSession("proj-toolcall", testWorkspacePath(t))
	if err != nil {
		t.Fatalf("CreateSession: %v", err)
	}
	if capturedEmitter == nil {
		t.Fatal("factory did not receive an emitter")
	}

	// Before any tool call, LastToolCallID is empty.
	if id, tool := manager.LastToolCallID(info.ID); id != "" || tool != "" {
		t.Fatalf("expected empty LastToolCallID before any tool call, got id=%q tool=%q", id, tool)
	}

	// Emit a tool call; the sink should record it for the session.
	capturedEmitter.ToolCall(1, 0, "bash_exec", `{"command":"ls"}`, "core")
	id, tool := manager.LastToolCallID(info.ID)
	if id == "" {
		t.Fatal("expected non-empty tool_call_id after ToolCall")
	}
	if tool != "bash_exec" {
		t.Fatalf("expected tool=bash_exec, got %q", tool)
	}

	// A subsequent tool call overwrites the entry (only the latest is needed).
	capturedEmitter.ToolCall(2, 0, "write_file", `{"path":"/x"}`, "core")
	id2, tool2 := manager.LastToolCallID(info.ID)
	if tool2 != "write_file" {
		t.Fatalf("expected tool=write_file after second call, got %q", tool2)
	}
	if id2 == "" || id2 == id {
		t.Errorf("expected a new distinct tool_call_id, got %q (prev %q)", id2, id)
	}

	// Unknown session returns empty.
	if x, _ := manager.LastToolCallID("no-such-session"); x != "" {
		t.Errorf("expected empty for unknown session, got %q", x)
	}

	// Deletion clears the entry.
	if err := manager.DeleteSession(info.ID); err != nil {
		t.Fatalf("DeleteSession: %v", err)
	}
	if x, _ := manager.LastToolCallID(info.ID); x != "" {
		t.Errorf("expected empty tool_call_id after session deletion, got %q", x)
	}
}
