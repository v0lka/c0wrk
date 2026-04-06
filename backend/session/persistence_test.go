package session

import (
	"context"
	"database/sql"
	"encoding/json"
	"testing"
	"time"

	_ "modernc.org/sqlite"
)

// openTestDB opens an in-memory SQLite database with required pragmas.
func openTestDB(t *testing.T) *sql.DB {
	t.Helper()
	db, err := sql.Open("sqlite", ":memory:")
	if err != nil {
		t.Fatalf("failed to open test db: %v", err)
	}
	if _, err := db.ExecContext(context.Background(), "PRAGMA journal_mode=WAL"); err != nil {
		_ = db.Close()
		t.Fatalf("failed to enable WAL: %v", err)
	}
	if _, err := db.ExecContext(context.Background(), "PRAGMA foreign_keys=ON"); err != nil {
		_ = db.Close()
		t.Fatalf("failed to enable foreign keys: %v", err)
	}
	return db
}

// createProjectsTable creates the projects table needed for FK references.
func createProjectsTable(t *testing.T, db *sql.DB) {
	t.Helper()
	_, err := db.ExecContext(context.Background(), `
		CREATE TABLE IF NOT EXISTS projects (
			id TEXT PRIMARY KEY,
			name TEXT NOT NULL,
			workspace_path TEXT NOT NULL,
			is_external BOOLEAN NOT NULL DEFAULT 0,
			created_at TIMESTAMP NOT NULL,
			last_active_at TIMESTAMP
		);
	`)
	if err != nil {
		t.Fatalf("failed to create projects table: %v", err)
	}
}

// insertTestProject inserts a test project and returns its ID.
func insertTestProject(t *testing.T, db *sql.DB, id string) string {
	t.Helper()
	_, err := db.ExecContext(context.Background(), `
		INSERT INTO projects (id, name, workspace_path, created_at)
		VALUES (?, ?, ?, ?)`,
		id, "Test Project", "/tmp/test", time.Now().Format(time.RFC3339),
	)
	if err != nil {
		t.Fatalf("failed to insert test project: %v", err)
	}
	return id
}

const testProjectID = "test-project-1"

func setupTestStore(t *testing.T) (store *SQLiteSessionStore, cleanup func()) {
	t.Helper()

	db := openTestDB(t)
	createProjectsTable(t, db)
	insertTestProject(t, db, testProjectID)

	var err error
	store, err = NewSQLiteSessionStore(db)
	if err != nil {
		_ = db.Close()
		t.Fatalf("failed to create test store: %v", err)
	}

	cleanup = func() {
		_ = db.Close()
	}

	return
}

func TestSaveAndLoadSession(t *testing.T) {
	store, cleanup := setupTestStore(t)
	defer cleanup()

	session := SessionInfo{
		ID:        "test-session-1",
		ProjectID: testProjectID,
		Name:      "Test Session",
		CreatedAt: "2024-01-15T10:30:00Z",
		Archived:  false,
	}

	// Save session
	if err := store.SaveSession(session); err != nil {
		t.Fatalf("failed to save session: %v", err)
	}

	// Load session
	loaded, err := store.LoadSession(session.ID)
	if err != nil {
		t.Fatalf("failed to load session: %v", err)
	}
	if loaded == nil {
		t.Fatal("loaded session should not be nil")
	}

	if loaded.ID != session.ID {
		t.Errorf("session ID mismatch: got %q, want %q", loaded.ID, session.ID)
	}
	if loaded.ProjectID != session.ProjectID {
		t.Errorf("session ProjectID mismatch: got %q, want %q", loaded.ProjectID, session.ProjectID)
	}
	if loaded.Name != session.Name {
		t.Errorf("session name mismatch: got %q, want %q", loaded.Name, session.Name)
	}
	if loaded.Archived != session.Archived {
		t.Errorf("session archived status mismatch: got %v, want %v", loaded.Archived, session.Archived)
	}

	// Load non-existent session
	notFound, err := store.LoadSession("non-existent")
	if err != nil {
		t.Fatalf("error loading non-existent session: %v", err)
	}
	if notFound != nil {
		t.Error("non-existent session should return nil")
	}
}

func TestListSessions(t *testing.T) {
	store, cleanup := setupTestStore(t)
	defer cleanup()

	// Create multiple sessions
	sessions := []SessionInfo{
		{
			ID:        "session-1",
			ProjectID: testProjectID,
			Name:      "First Session",
			CreatedAt: "2024-01-15T10:00:00Z",
			Archived:  false,
		},
		{
			ID:        "session-2",
			ProjectID: testProjectID,
			Name:      "Second Session",
			CreatedAt: "2024-01-15T11:00:00Z",
			Archived:  true,
		},
		{
			ID:        "session-3",
			ProjectID: testProjectID,
			Name:      "Third Session",
			CreatedAt: "2024-01-15T12:00:00Z",
			Archived:  false,
		},
	}

	for _, s := range sessions {
		if err := store.SaveSession(s); err != nil {
			t.Fatalf("failed to save session: %v", err)
		}
	}

	// List sessions - should be ordered by created_at DESC (newest first)
	listed, err := store.ListSessions()
	if err != nil {
		t.Fatalf("failed to list sessions: %v", err)
	}
	if len(listed) != 3 {
		t.Errorf("expected 3 sessions, got %d", len(listed))
	}

	// Verify ordering (newest first)
	if listed[0].ID != "session-3" {
		t.Errorf("first session should be newest (session-3), got %s", listed[0].ID)
	}
	if listed[1].ID != "session-2" {
		t.Errorf("second session should be session-2, got %s", listed[1].ID)
	}
	if listed[2].ID != "session-1" {
		t.Errorf("third session should be oldest (session-1), got %s", listed[2].ID)
	}
}

func TestDeleteSession(t *testing.T) {
	store, cleanup := setupTestStore(t)
	defer cleanup()

	// Create session with messages
	session := SessionInfo{
		ID:        "delete-test",
		ProjectID: testProjectID,
		Name:      "Delete Test",
		CreatedAt: time.Now().Format(time.RFC3339),
	}
	if err := store.SaveSession(session); err != nil {
		t.Fatalf("failed to save session: %v", err)
	}

	// Add messages
	msg1 := ChatMessage{
		SessionID: session.ID,
		Role:      "user",
		Content:   "Hello",
		CreatedAt: time.Now().Format(time.RFC3339),
	}
	msg2 := ChatMessage{
		SessionID: session.ID,
		Role:      "assistant",
		Content:   "Hi there",
		CreatedAt: time.Now().Format(time.RFC3339),
	}
	if err := store.SaveMessage(msg1); err != nil {
		t.Fatalf("failed to save message 1: %v", err)
	}
	if err := store.SaveMessage(msg2); err != nil {
		t.Fatalf("failed to save message 2: %v", err)
	}

	// Verify messages exist
	messages, err := store.LoadMessages(session.ID)
	if err != nil {
		t.Fatalf("failed to load messages: %v", err)
	}
	if len(messages) != 2 {
		t.Errorf("expected 2 messages before delete, got %d", len(messages))
	}

	// Delete session (should cascade delete messages)
	if err := store.DeleteSession(session.ID); err != nil {
		t.Fatalf("failed to delete session: %v", err)
	}

	// Verify session is gone
	loaded, err := store.LoadSession(session.ID)
	if err != nil {
		t.Fatalf("error loading deleted session: %v", err)
	}
	if loaded != nil {
		t.Error("deleted session should be nil")
	}

	// Verify messages are also deleted (cascade)
	messages, err = store.LoadMessages(session.ID)
	if err != nil {
		t.Fatalf("error loading messages after delete: %v", err)
	}
	if len(messages) != 0 {
		t.Errorf("expected 0 messages after cascade delete, got %d", len(messages))
	}
}

func TestArchiveSession(t *testing.T) {
	store, cleanup := setupTestStore(t)
	defer cleanup()

	session := SessionInfo{
		ID:        "archive-test",
		ProjectID: testProjectID,
		Name:      "Archive Test",
		CreatedAt: time.Now().Format(time.RFC3339),
		Archived:  false,
	}
	if err := store.SaveSession(session); err != nil {
		t.Fatalf("failed to save session: %v", err)
	}

	// Archive the session
	if err := store.ArchiveSession(session.ID, true); err != nil {
		t.Fatalf("failed to archive session: %v", err)
	}

	// Verify archived
	loaded, err := store.LoadSession(session.ID)
	if err != nil {
		t.Fatalf("failed to load session: %v", err)
	}
	if loaded == nil {
		t.Fatal("loaded session should not be nil")
	}
	if !loaded.Archived {
		t.Error("session should be archived")
	}

	// Unarchive the session
	if err := store.ArchiveSession(session.ID, false); err != nil {
		t.Fatalf("failed to unarchive session: %v", err)
	}

	// Verify unarchived
	loaded, err = store.LoadSession(session.ID)
	if err != nil {
		t.Fatalf("failed to load session: %v", err)
	}
	if loaded == nil {
		t.Fatal("loaded session should not be nil")
	}
	if loaded.Archived {
		t.Error("session should not be archived")
	}
}

func TestRenameSession(t *testing.T) {
	store, cleanup := setupTestStore(t)
	defer cleanup()

	session := SessionInfo{
		ID:        "rename-test",
		ProjectID: testProjectID,
		Name:      "Original Name",
		CreatedAt: time.Now().Format(time.RFC3339),
	}
	if err := store.SaveSession(session); err != nil {
		t.Fatalf("failed to save session: %v", err)
	}

	// Rename the session
	newName := "Renamed Session"
	if err := store.RenameSession(session.ID, newName); err != nil {
		t.Fatalf("failed to rename session: %v", err)
	}

	// Verify renamed
	loaded, err := store.LoadSession(session.ID)
	if err != nil {
		t.Fatalf("failed to load session: %v", err)
	}
	if loaded == nil {
		t.Fatal("loaded session should not be nil")
	}
	if loaded.Name != newName {
		t.Errorf("session name should be updated: got %q, want %q", loaded.Name, newName)
	}
}

func TestSaveAndLoadMessages(t *testing.T) {
	store, cleanup := setupTestStore(t)
	defer cleanup()

	// Create session first
	session := SessionInfo{
		ID:        "msg-test",
		ProjectID: testProjectID,
		Name:      "Message Test",
		CreatedAt: time.Now().Format(time.RFC3339),
	}
	if err := store.SaveSession(session); err != nil {
		t.Fatalf("failed to save session: %v", err)
	}

	// Create messages with specific timestamps for ordering test
	messages := []ChatMessage{
		{
			SessionID: session.ID,
			Role:      "user",
			Content:   "First message",
			Metadata:  json.RawMessage(`{"key":"value1"}`),
			CreatedAt: "2024-01-15T10:00:00Z",
		},
		{
			SessionID: session.ID,
			Role:      "assistant",
			Content:   "Second message",
			Metadata:  json.RawMessage(`{"key":"value2"}`),
			CreatedAt: "2024-01-15T10:01:00Z",
		},
		{
			SessionID: session.ID,
			Role:      "tool_call",
			Content:   "Third message",
			Metadata:  json.RawMessage(`{"tool":"search"}`),
			CreatedAt: "2024-01-15T10:02:00Z",
		},
	}

	for _, msg := range messages {
		if err := store.SaveMessage(msg); err != nil {
			t.Fatalf("failed to save message: %v", err)
		}
	}

	// Load messages
	loaded, err := store.LoadMessages(session.ID)
	if err != nil {
		t.Fatalf("failed to load messages: %v", err)
	}
	if len(loaded) != 3 {
		t.Errorf("expected 3 messages, got %d", len(loaded))
	}

	// Verify ordering (by created_at ASC)
	if loaded[0].Role != "user" {
		t.Errorf("first message role: got %q, want %q", loaded[0].Role, "user")
	}
	if loaded[0].Content != "First message" {
		t.Errorf("first message content: got %q, want %q", loaded[0].Content, "First message")
	}
	if string(loaded[0].Metadata) != `{"key":"value1"}` {
		t.Errorf("first message metadata: got %q, want %q", string(loaded[0].Metadata), `{"key":"value1"}`)
	}

	if loaded[1].Role != "assistant" {
		t.Errorf("second message role: got %q, want %q", loaded[1].Role, "assistant")
	}
	if loaded[1].Content != "Second message" {
		t.Errorf("second message content: got %q, want %q", loaded[1].Content, "Second message")
	}

	if loaded[2].Role != "tool_call" {
		t.Errorf("third message role: got %q, want %q", loaded[2].Role, "tool_call")
	}
	if loaded[2].Content != "Third message" {
		t.Errorf("third message content: got %q, want %q", loaded[2].Content, "Third message")
	}

	// Verify auto-increment IDs
	if loaded[0].ID != 1 {
		t.Errorf("first message ID: got %d, want %d", loaded[0].ID, 1)
	}
	if loaded[1].ID != 2 {
		t.Errorf("second message ID: got %d, want %d", loaded[1].ID, 2)
	}
	if loaded[2].ID != 3 {
		t.Errorf("third message ID: got %d, want %d", loaded[2].ID, 3)
	}
}

func TestDeleteMessages(t *testing.T) {
	store, cleanup := setupTestStore(t)
	defer cleanup()

	// Create session
	session := SessionInfo{
		ID:        "delete-msg-test",
		ProjectID: testProjectID,
		Name:      "Delete Messages Test",
		CreatedAt: time.Now().Format(time.RFC3339),
	}
	if err := store.SaveSession(session); err != nil {
		t.Fatalf("failed to save session: %v", err)
	}

	// Add messages
	for i := 0; i < 5; i++ {
		msg := ChatMessage{
			SessionID: session.ID,
			Role:      "user",
			Content:   "Message",
			CreatedAt: time.Now().Format(time.RFC3339),
		}
		if err := store.SaveMessage(msg); err != nil {
			t.Fatalf("failed to save message: %v", err)
		}
	}

	// Verify messages exist
	messages, err := store.LoadMessages(session.ID)
	if err != nil {
		t.Fatalf("failed to load messages: %v", err)
	}
	if len(messages) != 5 {
		t.Errorf("expected 5 messages, got %d", len(messages))
	}

	// Delete messages
	if err := store.DeleteMessages(session.ID); err != nil {
		t.Fatalf("failed to delete messages: %v", err)
	}

	// Verify messages are deleted but session remains
	messages, err = store.LoadMessages(session.ID)
	if err != nil {
		t.Fatalf("error loading messages after delete: %v", err)
	}
	if len(messages) != 0 {
		t.Errorf("expected 0 messages after delete, got %d", len(messages))
	}

	// Verify session still exists
	loaded, err := store.LoadSession(session.ID)
	if err != nil {
		t.Fatalf("session should still exist: %v", err)
	}
	if loaded == nil {
		t.Fatal("session should not be nil")
	}
}

func TestSessionStoreClose(t *testing.T) {
	db := openTestDB(t)
	createProjectsTable(t, db)
	insertTestProject(t, db, testProjectID)

	store, err := NewSQLiteSessionStore(db)
	if err != nil {
		t.Fatalf("failed to create store: %v", err)
	}

	// Save some data
	session := SessionInfo{
		ID:        "close-test",
		ProjectID: testProjectID,
		Name:      "Close Test",
		CreatedAt: time.Now().Format(time.RFC3339),
	}
	if err := store.SaveSession(session); err != nil {
		t.Fatalf("failed to save session: %v", err)
	}

	// Close the store (no-op since DB lifecycle is external)
	if err := store.Close(); err != nil {
		t.Fatalf("failed to close store: %v", err)
	}

	// DB should still be usable (Close is a no-op)
	loaded, err := store.LoadSession(session.ID)
	if err != nil {
		t.Fatalf("failed to load after close: %v", err)
	}
	if loaded == nil || loaded.ID != session.ID {
		t.Error("expected to still load session after Close (no-op)")
	}

	_ = db.Close()
}

func TestEmptyListSessions(t *testing.T) {
	store, cleanup := setupTestStore(t)
	defer cleanup()

	// List sessions when none exist
	sessions, err := store.ListSessions()
	if err != nil {
		t.Fatalf("failed to list empty sessions: %v", err)
	}
	if len(sessions) != 0 {
		t.Errorf("expected empty list, got %d sessions", len(sessions))
	}
	if sessions == nil {
		t.Error("sessions should not be nil")
	}
}

func TestEmptyLoadMessages(t *testing.T) {
	store, cleanup := setupTestStore(t)
	defer cleanup()

	// Create session without messages
	session := SessionInfo{
		ID:        "empty-msg-test",
		ProjectID: testProjectID,
		Name:      "Empty Messages Test",
		CreatedAt: time.Now().Format(time.RFC3339),
	}
	if err := store.SaveSession(session); err != nil {
		t.Fatalf("failed to save session: %v", err)
	}

	// Load messages when none exist
	messages, err := store.LoadMessages(session.ID)
	if err != nil {
		t.Fatalf("failed to load empty messages: %v", err)
	}
	if len(messages) != 0 {
		t.Errorf("expected empty list, got %d messages", len(messages))
	}
	if messages == nil {
		t.Error("messages should not be nil")
	}
}

func TestSaveSessionUpdate(t *testing.T) {
	store, cleanup := setupTestStore(t)
	defer cleanup()

	// Create initial session
	session := SessionInfo{
		ID:        "update-test",
		ProjectID: testProjectID,
		Name:      "Original Name",
		CreatedAt: "2024-01-15T10:00:00Z",
		Archived:  false,
	}
	if err := store.SaveSession(session); err != nil {
		t.Fatalf("failed to save initial session: %v", err)
	}

	// Update the session
	updated := SessionInfo{
		ID:        session.ID,
		ProjectID: testProjectID,
		Name:      "Updated Name",
		CreatedAt: session.CreatedAt,
		Archived:  true,
	}
	if err := store.SaveSession(updated); err != nil {
		t.Fatalf("failed to update session: %v", err)
	}

	// Verify update
	loaded, err := store.LoadSession(session.ID)
	if err != nil {
		t.Fatalf("failed to load updated session: %v", err)
	}
	if loaded == nil {
		t.Fatal("loaded session should not be nil")
	}
	if loaded.Name != "Updated Name" {
		t.Errorf("name should be updated: got %q, want %q", loaded.Name, "Updated Name")
	}
	if !loaded.Archived {
		t.Error("archived should be updated to true")
	}
}

func TestMultipleSessionsMessagesIsolation(t *testing.T) {
	store, cleanup := setupTestStore(t)
	defer cleanup()

	// Create two sessions
	session1 := SessionInfo{
		ID:        "session-1",
		ProjectID: testProjectID,
		Name:      "Session 1",
		CreatedAt: time.Now().Format(time.RFC3339),
	}
	session2 := SessionInfo{
		ID:        "session-2",
		ProjectID: testProjectID,
		Name:      "Session 2",
		CreatedAt: time.Now().Format(time.RFC3339),
	}

	if err := store.SaveSession(session1); err != nil {
		t.Fatalf("failed to save session 1: %v", err)
	}
	if err := store.SaveSession(session2); err != nil {
		t.Fatalf("failed to save session 2: %v", err)
	}

	// Add messages to each session
	msg1 := ChatMessage{
		SessionID: session1.ID,
		Role:      "user",
		Content:   "Session 1 message",
		CreatedAt: time.Now().Format(time.RFC3339),
	}
	msg2 := ChatMessage{
		SessionID: session2.ID,
		Role:      "user",
		Content:   "Session 2 message",
		CreatedAt: time.Now().Format(time.RFC3339),
	}

	if err := store.SaveMessage(msg1); err != nil {
		t.Fatalf("failed to save message 1: %v", err)
	}
	if err := store.SaveMessage(msg2); err != nil {
		t.Fatalf("failed to save message 2: %v", err)
	}

	// Verify message isolation
	messages1, err := store.LoadMessages(session1.ID)
	if err != nil {
		t.Fatalf("failed to load messages for session 1: %v", err)
	}
	if len(messages1) != 1 {
		t.Errorf("session 1 should have 1 message, got %d", len(messages1))
	}
	if messages1[0].Content != "Session 1 message" {
		t.Errorf("session 1 message content: got %q, want %q", messages1[0].Content, "Session 1 message")
	}

	messages2, err := store.LoadMessages(session2.ID)
	if err != nil {
		t.Fatalf("failed to load messages for session 2: %v", err)
	}
	if len(messages2) != 1 {
		t.Errorf("session 2 should have 1 message, got %d", len(messages2))
	}
	if messages2[0].Content != "Session 2 message" {
		t.Errorf("session 2 message content: got %q, want %q", messages2[0].Content, "Session 2 message")
	}

	// Delete messages for session 1 only
	if err := store.DeleteMessages(session1.ID); err != nil {
		t.Fatalf("failed to delete messages for session 1: %v", err)
	}

	// Verify session 2 messages still exist
	messages2, err = store.LoadMessages(session2.ID)
	if err != nil {
		t.Fatalf("failed to load messages for session 2 after delete: %v", err)
	}
	if len(messages2) != 1 {
		t.Errorf("session 2 should still have 1 message, got %d", len(messages2))
	}
}

func TestSaveAndLoadSessionTokens(t *testing.T) {
	store, cleanup := setupTestStore(t)
	defer cleanup()

	session := SessionInfo{
		ID:                "token-test",
		ProjectID:         testProjectID,
		Name:              "Token Test",
		CreatedAt:         time.Now().Format(time.RFC3339),
		TotalInputTokens:  5000,
		TotalOutputTokens: 3000,
	}

	if err := store.SaveSession(session); err != nil {
		t.Fatalf("failed to save session: %v", err)
	}

	loaded, err := store.LoadSession(session.ID)
	if err != nil {
		t.Fatalf("failed to load session: %v", err)
	}
	if loaded == nil {
		t.Fatal("loaded session should not be nil")
	}
	if loaded.TotalInputTokens != 5000 {
		t.Errorf("input tokens mismatch: got %d, want %d", loaded.TotalInputTokens, 5000)
	}
	if loaded.TotalOutputTokens != 3000 {
		t.Errorf("output tokens mismatch: got %d, want %d", loaded.TotalOutputTokens, 3000)
	}
}

func TestUpdateSessionTokens(t *testing.T) {
	store, cleanup := setupTestStore(t)
	defer cleanup()

	// Create session with zero tokens
	session := SessionInfo{
		ID:        "update-tokens-test",
		ProjectID: testProjectID,
		Name:      "Update Tokens Test",
		CreatedAt: time.Now().Format(time.RFC3339),
	}
	if err := store.SaveSession(session); err != nil {
		t.Fatalf("failed to save session: %v", err)
	}

	// Update tokens
	if err := store.UpdateSessionTokens(session.ID, 10000, 7500); err != nil {
		t.Fatalf("failed to update session tokens: %v", err)
	}

	// Verify tokens were updated
	loaded, err := store.LoadSession(session.ID)
	if err != nil {
		t.Fatalf("failed to load session: %v", err)
	}
	if loaded == nil {
		t.Fatal("loaded session should not be nil")
	}
	if loaded.TotalInputTokens != 10000 {
		t.Errorf("input tokens mismatch: got %d, want %d", loaded.TotalInputTokens, 10000)
	}
	if loaded.TotalOutputTokens != 7500 {
		t.Errorf("output tokens mismatch: got %d, want %d", loaded.TotalOutputTokens, 7500)
	}

	// Update again (overwrite)
	if err := store.UpdateSessionTokens(session.ID, 20000, 15000); err != nil {
		t.Fatalf("failed to update session tokens again: %v", err)
	}

	loaded, err = store.LoadSession(session.ID)
	if err != nil {
		t.Fatalf("failed to load session after second update: %v", err)
	}
	if loaded.TotalInputTokens != 20000 {
		t.Errorf("input tokens after update: got %d, want %d", loaded.TotalInputTokens, 20000)
	}
	if loaded.TotalOutputTokens != 15000 {
		t.Errorf("output tokens after update: got %d, want %d", loaded.TotalOutputTokens, 15000)
	}
}

func TestListSessionsWithTokens(t *testing.T) {
	store, cleanup := setupTestStore(t)
	defer cleanup()

	sessions := []SessionInfo{
		{
			ID:                "list-token-1",
			ProjectID:         testProjectID,
			Name:              "Session 1",
			CreatedAt:         "2024-01-15T10:00:00Z",
			TotalInputTokens:  1000,
			TotalOutputTokens: 500,
		},
		{
			ID:                "list-token-2",
			ProjectID:         testProjectID,
			Name:              "Session 2",
			CreatedAt:         "2024-01-15T11:00:00Z",
			TotalInputTokens:  2000,
			TotalOutputTokens: 1500,
		},
	}

	for _, s := range sessions {
		if err := store.SaveSession(s); err != nil {
			t.Fatalf("failed to save session: %v", err)
		}
	}

	listed, err := store.ListSessions()
	if err != nil {
		t.Fatalf("failed to list sessions: %v", err)
	}
	if len(listed) != 2 {
		t.Fatalf("expected 2 sessions, got %d", len(listed))
	}

	// Listed in DESC order by created_at, so session-2 first
	if listed[0].TotalInputTokens != 2000 {
		t.Errorf("session 2 input tokens: got %d, want %d", listed[0].TotalInputTokens, 2000)
	}
	if listed[1].TotalOutputTokens != 500 {
		t.Errorf("session 1 output tokens: got %d, want %d", listed[1].TotalOutputTokens, 500)
	}
}

// TestUpdateSessionActivity verifies last_active_at timestamp is updated.
func TestUpdateSessionActivity(t *testing.T) {
	store, cleanup := setupTestStore(t)
	defer cleanup()

	session := SessionInfo{
		ID:        "activity-test",
		ProjectID: testProjectID,
		Name:      "Activity Test",
		CreatedAt: "2024-01-15T10:00:00Z",
	}
	if err := store.SaveSession(session); err != nil {
		t.Fatalf("failed to save session: %v", err)
	}

	// Load before update
	before, err := store.LoadSession(session.ID)
	if err != nil {
		t.Fatalf("failed to load session: %v", err)
	}
	originalLastActive := before.LastActiveAt

	// Wait a tiny bit so timestamps differ
	time.Sleep(10 * time.Millisecond)

	// Update activity
	if err := store.UpdateSessionActivity(session.ID); err != nil {
		t.Fatalf("failed to update session activity: %v", err)
	}

	// Load after update
	after, err := store.LoadSession(session.ID)
	if err != nil {
		t.Fatalf("failed to load session after update: %v", err)
	}
	if after.LastActiveAt == originalLastActive {
		t.Error("last_active_at should have changed after UpdateSessionActivity")
	}
}

// TestSaveSessionWithLastActiveAt verifies LastActiveAt is stored and retrieved.
func TestSaveSessionWithLastActiveAt(t *testing.T) {
	store, cleanup := setupTestStore(t)
	defer cleanup()

	session := SessionInfo{
		ID:           "last-active-test",
		ProjectID:    testProjectID,
		Name:         "Last Active Test",
		CreatedAt:    "2024-01-15T10:00:00Z",
		LastActiveAt: "2024-06-01T15:30:00Z",
	}
	if err := store.SaveSession(session); err != nil {
		t.Fatalf("failed to save session: %v", err)
	}

	loaded, err := store.LoadSession(session.ID)
	if err != nil {
		t.Fatalf("failed to load session: %v", err)
	}
	if loaded.LastActiveAt != "2024-06-01T15:30:00Z" {
		t.Errorf("expected last_active_at '2024-06-01T15:30:00Z', got %q", loaded.LastActiveAt)
	}
}

// TestSaveSessionLastActiveAtFallback verifies LastActiveAt falls back to CreatedAt when empty.
func TestSaveSessionLastActiveAtFallback(t *testing.T) {
	store, cleanup := setupTestStore(t)
	defer cleanup()

	session := SessionInfo{
		ID:           "fallback-test",
		ProjectID:    testProjectID,
		Name:         "Fallback Test",
		CreatedAt:    "2024-01-15T10:00:00Z",
		LastActiveAt: "", // empty
	}
	if err := store.SaveSession(session); err != nil {
		t.Fatalf("failed to save session: %v", err)
	}

	loaded, err := store.LoadSession(session.ID)
	if err != nil {
		t.Fatalf("failed to load session: %v", err)
	}
	// Should fall back to created_at
	if loaded.LastActiveAt != "2024-01-15T10:00:00Z" {
		t.Errorf("expected last_active_at to fall back to created_at, got %q", loaded.LastActiveAt)
	}
}

// TestNewSQLiteSessionStore_InMemory verifies in-memory database works with shared DB.
func TestNewSQLiteSessionStore_InMemory(t *testing.T) {
	db := openTestDB(t)
	defer func() { _ = db.Close() }()
	createProjectsTable(t, db)
	insertTestProject(t, db, testProjectID)

	store, err := NewSQLiteSessionStore(db)
	if err != nil {
		t.Fatalf("failed to create in-memory store: %v", err)
	}

	// Basic operation on in-memory store
	session := SessionInfo{
		ID:        "mem-test",
		ProjectID: testProjectID,
		Name:      "Memory Test",
		CreatedAt: time.Now().Format(time.RFC3339),
	}
	if err := store.SaveSession(session); err != nil {
		t.Fatalf("failed to save: %v", err)
	}

	loaded, err := store.LoadSession("mem-test")
	if err != nil {
		t.Fatalf("failed to load: %v", err)
	}
	if loaded == nil || loaded.ID != "mem-test" {
		t.Error("expected to load session from in-memory store")
	}
}

// TestListSessionsOrderedByActivity verifies sessions ordered by last_active_at DESC.
func TestListSessionsOrderedByActivity(t *testing.T) {
	store, cleanup := setupTestStore(t)
	defer cleanup()

	// Create sessions with different last_active_at
	sessions := []SessionInfo{
		{
			ID:           "old",
			ProjectID:    testProjectID,
			Name:         "Old Session",
			CreatedAt:    "2024-01-01T10:00:00Z",
			LastActiveAt: "2024-01-01T10:00:00Z",
		},
		{
			ID:           "newest",
			ProjectID:    testProjectID,
			Name:         "Newest Activity",
			CreatedAt:    "2024-01-01T09:00:00Z",
			LastActiveAt: "2024-06-15T20:00:00Z",
		},
		{
			ID:           "mid",
			ProjectID:    testProjectID,
			Name:         "Mid Session",
			CreatedAt:    "2024-03-01T10:00:00Z",
			LastActiveAt: "2024-03-01T10:00:00Z",
		},
	}

	for _, s := range sessions {
		if err := store.SaveSession(s); err != nil {
			t.Fatalf("failed to save session: %v", err)
		}
	}

	listed, err := store.ListSessions()
	if err != nil {
		t.Fatalf("failed to list sessions: %v", err)
	}
	if len(listed) != 3 {
		t.Fatalf("expected 3 sessions, got %d", len(listed))
	}

	// Should be ordered by last_active_at DESC: newest, mid, old
	if listed[0].ID != "newest" {
		t.Errorf("first session should be 'newest', got %q", listed[0].ID)
	}
	if listed[1].ID != "mid" {
		t.Errorf("second session should be 'mid', got %q", listed[1].ID)
	}
	if listed[2].ID != "old" {
		t.Errorf("third session should be 'old', got %q", listed[2].ID)
	}
}

// TestListSessionsByProject verifies filtering sessions by project ID.
func TestListSessionsByProject(t *testing.T) {
	db := openTestDB(t)
	defer func() { _ = db.Close() }()
	createProjectsTable(t, db)
	insertTestProject(t, db, "project-a")
	insertTestProject(t, db, "project-b")

	store, err := NewSQLiteSessionStore(db)
	if err != nil {
		t.Fatalf("failed to create store: %v", err)
	}

	// Create sessions across two projects
	sessions := []SessionInfo{
		{ID: "s1", ProjectID: "project-a", Name: "A1", CreatedAt: "2024-01-15T10:00:00Z"},
		{ID: "s2", ProjectID: "project-a", Name: "A2", CreatedAt: "2024-01-15T11:00:00Z"},
		{ID: "s3", ProjectID: "project-b", Name: "B1", CreatedAt: "2024-01-15T12:00:00Z"},
	}
	for _, s := range sessions {
		if err := store.SaveSession(s); err != nil {
			t.Fatalf("failed to save session: %v", err)
		}
	}

	// List project-a sessions
	projectASessions, err := store.ListSessionsByProject("project-a")
	if err != nil {
		t.Fatalf("failed to list sessions by project: %v", err)
	}
	if len(projectASessions) != 2 {
		t.Fatalf("expected 2 sessions for project-a, got %d", len(projectASessions))
	}
	// Should be ordered by last_active_at DESC
	if projectASessions[0].ID != "s2" {
		t.Errorf("first session should be s2 (newest), got %q", projectASessions[0].ID)
	}
	if projectASessions[1].ID != "s1" {
		t.Errorf("second session should be s1 (oldest), got %q", projectASessions[1].ID)
	}

	// List project-b sessions
	projectBSessions, err := store.ListSessionsByProject("project-b")
	if err != nil {
		t.Fatalf("failed to list sessions by project: %v", err)
	}
	if len(projectBSessions) != 1 {
		t.Fatalf("expected 1 session for project-b, got %d", len(projectBSessions))
	}
	if projectBSessions[0].ID != "s3" {
		t.Errorf("expected session s3, got %q", projectBSessions[0].ID)
	}

	// List nonexistent project
	emptySessions, err := store.ListSessionsByProject("nonexistent")
	if err != nil {
		t.Fatalf("failed to list sessions for nonexistent project: %v", err)
	}
	if len(emptySessions) != 0 {
		t.Errorf("expected 0 sessions, got %d", len(emptySessions))
	}
	if emptySessions == nil {
		t.Error("sessions should not be nil")
	}
}
