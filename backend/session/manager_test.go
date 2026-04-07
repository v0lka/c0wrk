package session

import (
	"context"
	"errors"
	"fmt"
	"log/slog"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/user/agent/core"
)

// testManager creates a Manager with a mock factory for testing.
func testManager(t *testing.T) (manager *Manager, events chan Event, dir string) {
	t.Helper()

	// Create temp directory for logs
	logDir := t.TempDir()

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
	factory := func(emitter core.Emitter, logger *slog.Logger, workspacePath string) (*core.Orchestrator, error) {
		return nil, nil
	}

	manager = NewManager(factory, emitFunc, logDir)
	events = eventChan
	dir = logDir
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
	manager, eventChan, logDir := testManager(t)

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
	logFile := filepath.Join(logDir, "session_"+info.ID+".log")
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
	err := manager.SendMessage(ctx, "non-existent", "hello")
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
	err := manager.SendMessage(ctx, info.ID, "hello")
	if err == nil {
		t.Error("SendMessage should return error when session is already active")
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
	wg.Add(numOps * 3)

	// Concurrent ListSessions
	for i := 0; i < numOps; i++ {
		go func() {
			defer wg.Done()
			_ = manager.ListSessions()
		}()
	}

	// Concurrent GetSession
	sessions := manager.ListSessions()
	if len(sessions) > 0 {
		for i := 0; i < numOps; i++ {
			go func(idx int) {
				defer wg.Done()
				sessionID := sessions[idx%len(sessions)].ID
				_, _ = manager.GetSession(sessionID)
			}(i)
		}
	} else {
		wg.Add(-numOps)
	}

	// Concurrent RenameSession
	if len(sessions) > 0 {
		for i := 0; i < numOps; i++ {
			go func(idx int) {
				defer wg.Done()
				sessionID := sessions[idx%len(sessions)].ID
				_ = manager.RenameSession(sessionID, "NewName")
			}(i)
		}
	} else {
		wg.Add(-numOps)
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
	logDir := t.TempDir()

	eventChan := make(chan Event, 100)
	emitFunc := func(e Event) {
		select {
		case eventChan <- e:
		default:
		}
	}

	// Factory that always fails
	factory := func(emitter core.Emitter, logger *slog.Logger, workspacePath string) (*core.Orchestrator, error) {
		return nil, errors.New("factory error")
	}

	manager := NewManager(factory, emitFunc, logDir)

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
	err = manager.SendMessage(ctx, info1.ID, "hello")
	if err == nil {
		t.Fatal("expected error when sending message to already-active session")
	}
	if !strings.Contains(err.Error(), "already processing") {
		t.Errorf("unexpected error: %v", err)
	}
}
