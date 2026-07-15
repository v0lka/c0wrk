package session

import (
	"context"
	"database/sql"
	"encoding/json"
	"fmt"
	"testing"
	"time"

	"github.com/v0lka/c0wrk/backend/project"

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
	if err := store.SaveSession(context.Background(), session); err != nil {
		t.Fatalf("failed to save session: %v", err)
	}

	// Load session
	loaded, err := store.LoadSession(context.Background(), session.ID)
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
	notFound, err := store.LoadSession(context.Background(), "non-existent")
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
		if err := store.SaveSession(context.Background(), s); err != nil {
			t.Fatalf("failed to save session: %v", err)
		}
	}

	// List sessions - should be ordered by created_at DESC (newest first)
	listed, err := store.ListSessions(context.Background())
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
	if err := store.SaveSession(context.Background(), session); err != nil {
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
	if err := store.SaveMessage(context.Background(), msg1); err != nil {
		t.Fatalf("failed to save message 1: %v", err)
	}
	if err := store.SaveMessage(context.Background(), msg2); err != nil {
		t.Fatalf("failed to save message 2: %v", err)
	}

	// Verify messages exist
	messages, err := store.LoadMessages(context.Background(), session.ID)
	if err != nil {
		t.Fatalf("failed to load messages: %v", err)
	}
	if len(messages) != 2 {
		t.Errorf("expected 2 messages before delete, got %d", len(messages))
	}

	// Delete session (should cascade delete messages)
	if err := store.DeleteSession(context.Background(), session.ID); err != nil {
		t.Fatalf("failed to delete session: %v", err)
	}

	// Verify session is gone
	loaded, err := store.LoadSession(context.Background(), session.ID)
	if err != nil {
		t.Fatalf("error loading deleted session: %v", err)
	}
	if loaded != nil {
		t.Error("deleted session should be nil")
	}

	// Verify messages are also deleted (cascade)
	messages, err = store.LoadMessages(context.Background(), session.ID)
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
	if err := store.SaveSession(context.Background(), session); err != nil {
		t.Fatalf("failed to save session: %v", err)
	}

	// Archive the session
	if err := store.ArchiveSession(context.Background(), session.ID, true); err != nil {
		t.Fatalf("failed to archive session: %v", err)
	}

	// Verify archived
	loaded, err := store.LoadSession(context.Background(), session.ID)
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
	if err := store.ArchiveSession(context.Background(), session.ID, false); err != nil {
		t.Fatalf("failed to unarchive session: %v", err)
	}

	// Verify unarchived
	loaded, err = store.LoadSession(context.Background(), session.ID)
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
	if err := store.SaveSession(context.Background(), session); err != nil {
		t.Fatalf("failed to save session: %v", err)
	}

	// Rename the session
	newName := "Renamed Session"
	if err := store.RenameSession(context.Background(), session.ID, newName); err != nil {
		t.Fatalf("failed to rename session: %v", err)
	}

	// Verify renamed
	loaded, err := store.LoadSession(context.Background(), session.ID)
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
	if err := store.SaveSession(context.Background(), session); err != nil {
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
		if err := store.SaveMessage(context.Background(), msg); err != nil {
			t.Fatalf("failed to save message: %v", err)
		}
	}

	// Load messages
	loaded, err := store.LoadMessages(context.Background(), session.ID)
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
	if err := store.SaveSession(context.Background(), session); err != nil {
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
		if err := store.SaveMessage(context.Background(), msg); err != nil {
			t.Fatalf("failed to save message: %v", err)
		}
	}

	// Verify messages exist
	messages, err := store.LoadMessages(context.Background(), session.ID)
	if err != nil {
		t.Fatalf("failed to load messages: %v", err)
	}
	if len(messages) != 5 {
		t.Errorf("expected 5 messages, got %d", len(messages))
	}

	// Delete messages
	if err := store.DeleteMessages(context.Background(), session.ID); err != nil {
		t.Fatalf("failed to delete messages: %v", err)
	}

	// Verify messages are deleted but session remains
	messages, err = store.LoadMessages(context.Background(), session.ID)
	if err != nil {
		t.Fatalf("error loading messages after delete: %v", err)
	}
	if len(messages) != 0 {
		t.Errorf("expected 0 messages after delete, got %d", len(messages))
	}

	// Verify session still exists
	loaded, err := store.LoadSession(context.Background(), session.ID)
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
	if err := store.SaveSession(context.Background(), session); err != nil {
		t.Fatalf("failed to save session: %v", err)
	}

	// Close the store (no-op since DB lifecycle is external)
	if err := store.Close(); err != nil {
		t.Fatalf("failed to close store: %v", err)
	}

	// DB should still be usable (Close is a no-op)
	loaded, err := store.LoadSession(context.Background(), session.ID)
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
	sessions, err := store.ListSessions(context.Background())
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
	if err := store.SaveSession(context.Background(), session); err != nil {
		t.Fatalf("failed to save session: %v", err)
	}

	// Load messages when none exist
	messages, err := store.LoadMessages(context.Background(), session.ID)
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
	if err := store.SaveSession(context.Background(), session); err != nil {
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
	if err := store.SaveSession(context.Background(), updated); err != nil {
		t.Fatalf("failed to update session: %v", err)
	}

	// Verify update
	loaded, err := store.LoadSession(context.Background(), session.ID)
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

	if err := store.SaveSession(context.Background(), session1); err != nil {
		t.Fatalf("failed to save session 1: %v", err)
	}
	if err := store.SaveSession(context.Background(), session2); err != nil {
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

	if err := store.SaveMessage(context.Background(), msg1); err != nil {
		t.Fatalf("failed to save message 1: %v", err)
	}
	if err := store.SaveMessage(context.Background(), msg2); err != nil {
		t.Fatalf("failed to save message 2: %v", err)
	}

	// Verify message isolation
	messages1, err := store.LoadMessages(context.Background(), session1.ID)
	if err != nil {
		t.Fatalf("failed to load messages for session 1: %v", err)
	}
	if len(messages1) != 1 {
		t.Errorf("session 1 should have 1 message, got %d", len(messages1))
	}
	if messages1[0].Content != "Session 1 message" {
		t.Errorf("session 1 message content: got %q, want %q", messages1[0].Content, "Session 1 message")
	}

	messages2, err := store.LoadMessages(context.Background(), session2.ID)
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
	if err := store.DeleteMessages(context.Background(), session1.ID); err != nil {
		t.Fatalf("failed to delete messages for session 1: %v", err)
	}

	// Verify session 2 messages still exist
	messages2, err = store.LoadMessages(context.Background(), session2.ID)
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

	if err := store.SaveSession(context.Background(), session); err != nil {
		t.Fatalf("failed to save session: %v", err)
	}

	loaded, err := store.LoadSession(context.Background(), session.ID)
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
	if err := store.SaveSession(context.Background(), session); err != nil {
		t.Fatalf("failed to save session: %v", err)
	}

	// Update tokens with model info
	if err := store.UpdateSessionTokens(context.Background(), session.ID, 10000, 7500, "claude-3-opus", "anthropic", 42.5); err != nil {
		t.Fatalf("failed to update session tokens: %v", err)
	}

	// Verify tokens and model info were updated
	loaded, err := store.LoadSession(context.Background(), session.ID)
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
	if loaded.Model != "claude-3-opus" {
		t.Errorf("model mismatch: got %q, want %q", loaded.Model, "claude-3-opus")
	}
	if loaded.Family != "anthropic" {
		t.Errorf("family mismatch: got %q, want %q", loaded.Family, "anthropic")
	}
	if loaded.FillPercent != 42.5 {
		t.Errorf("fill percent mismatch: got %f, want %f", loaded.FillPercent, 42.5)
	}

	// Update again (overwrite)
	if err := store.UpdateSessionTokens(context.Background(), session.ID, 20000, 15000, "gpt-4o", "openai", 88); err != nil {
		t.Fatalf("failed to update session tokens again: %v", err)
	}

	loaded, err = store.LoadSession(context.Background(), session.ID)
	if err != nil {
		t.Fatalf("failed to load session after second update: %v", err)
	}
	if loaded.TotalInputTokens != 20000 {
		t.Errorf("input tokens after update: got %d, want %d", loaded.TotalInputTokens, 20000)
	}
	if loaded.TotalOutputTokens != 15000 {
		t.Errorf("output tokens after update: got %d, want %d", loaded.TotalOutputTokens, 15000)
	}
	if loaded.Model != "gpt-4o" {
		t.Errorf("model after update: got %q, want %q", loaded.Model, "gpt-4o")
	}
	if loaded.Family != "openai" {
		t.Errorf("family after update: got %q, want %q", loaded.Family, "openai")
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
		if err := store.SaveSession(context.Background(), s); err != nil {
			t.Fatalf("failed to save session: %v", err)
		}
	}

	listed, err := store.ListSessions(context.Background())
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
	if err := store.SaveSession(context.Background(), session); err != nil {
		t.Fatalf("failed to save session: %v", err)
	}

	// Load before update
	before, err := store.LoadSession(context.Background(), session.ID)
	if err != nil {
		t.Fatalf("failed to load session: %v", err)
	}
	originalLastActive := before.LastActiveAt

	// Wait a tiny bit so timestamps differ
	time.Sleep(10 * time.Millisecond)

	// Update activity
	if err := store.UpdateSessionActivity(context.Background(), session.ID); err != nil {
		t.Fatalf("failed to update session activity: %v", err)
	}

	// Load after update
	after, err := store.LoadSession(context.Background(), session.ID)
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
	if err := store.SaveSession(context.Background(), session); err != nil {
		t.Fatalf("failed to save session: %v", err)
	}

	loaded, err := store.LoadSession(context.Background(), session.ID)
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
	if err := store.SaveSession(context.Background(), session); err != nil {
		t.Fatalf("failed to save session: %v", err)
	}

	loaded, err := store.LoadSession(context.Background(), session.ID)
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
	if err := store.SaveSession(context.Background(), session); err != nil {
		t.Fatalf("failed to save: %v", err)
	}

	loaded, err := store.LoadSession(context.Background(), "mem-test")
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
		if err := store.SaveSession(context.Background(), s); err != nil {
			t.Fatalf("failed to save session: %v", err)
		}
	}

	listed, err := store.ListSessions(context.Background())
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
		if err := store.SaveSession(context.Background(), s); err != nil {
			t.Fatalf("failed to save session: %v", err)
		}
	}

	// List project-a sessions
	projectASessions, err := store.ListSessionsByProject(context.Background(), "project-a")
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
	projectBSessions, err := store.ListSessionsByProject(context.Background(), "project-b")
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
	emptySessions, err := store.ListSessionsByProject(context.Background(), "nonexistent")
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

// ---------------------------------------------------------------------------
// TaskStore tests
// ---------------------------------------------------------------------------

// setupTestStoreWithSession creates a store with a project and session for task tests.
func setupTestStoreWithSession(t *testing.T) (store *SQLiteSessionStore, sessionID string, cleanup func()) {
	t.Helper()
	store, cleanup = setupTestStore(t)

	sessionID = "task-test-session"
	if err := store.SaveSession(context.Background(), SessionInfo{
		ID:        sessionID,
		ProjectID: testProjectID,
		Name:      "Task Test Session",
		CreatedAt: time.Now().Format(time.RFC3339),
	}); err != nil {
		t.Fatalf("failed to create test session: %v", err)
	}
	return
}

func TestSaveTask(t *testing.T) {
	store, sessionID, cleanup := setupTestStoreWithSession(t)
	defer cleanup()

	now := time.Now().Truncate(time.Second)
	task := TaskRecord{
		ID:              "task-1",
		SessionID:       sessionID,
		OriginalRequest: "build a CLI tool",
		RoutingDecision: json.RawMessage(`{"domain":"code"}`),
		Plan:            json.RawMessage(`{"steps":[{"id":"step_1"}]}`),
		Reflections:     json.RawMessage(`[]`),
		FinalOutput:     "",
		AttemptCount:    0,
		Status:          "in_progress",
		CreatedAt:       now,
	}

	if err := store.SaveTask(context.Background(), task); err != nil {
		t.Fatalf("SaveTask failed: %v", err)
	}

	loaded, err := store.LoadTask(context.Background(), "task-1")
	if err != nil {
		t.Fatalf("LoadTask failed: %v", err)
	}
	if loaded == nil {
		t.Fatal("loaded task should not be nil")
	}

	if loaded.ID != task.ID {
		t.Errorf("ID mismatch: got %q, want %q", loaded.ID, task.ID)
	}
	if loaded.SessionID != task.SessionID {
		t.Errorf("SessionID mismatch: got %q, want %q", loaded.SessionID, task.SessionID)
	}
	if loaded.OriginalRequest != task.OriginalRequest {
		t.Errorf("OriginalRequest mismatch: got %q, want %q", loaded.OriginalRequest, task.OriginalRequest)
	}
	if loaded.Status != "in_progress" {
		t.Errorf("Status mismatch: got %q, want %q", loaded.Status, "in_progress")
	}
	if string(loaded.RoutingDecision) != `{"domain":"code"}` {
		t.Errorf("RoutingDecision mismatch: got %s", loaded.RoutingDecision)
	}
	if string(loaded.Plan) != `{"steps":[{"id":"step_1"}]}` {
		t.Errorf("Plan mismatch: got %s", loaded.Plan)
	}
}

func TestUpdateTaskPlan(t *testing.T) {
	store, sessionID, cleanup := setupTestStoreWithSession(t)
	defer cleanup()

	// Save initial task
	if err := store.SaveTask(context.Background(), TaskRecord{
		ID:              "task-plan",
		SessionID:       sessionID,
		OriginalRequest: "test",
		RoutingDecision: json.RawMessage(`{}`),
		Plan:            json.RawMessage(`{}`),
		Reflections:     json.RawMessage(`[]`),
		Status:          "in_progress",
		CreatedAt:       time.Now(),
	}); err != nil {
		t.Fatalf("SaveTask failed: %v", err)
	}

	newPlan := json.RawMessage(`{"steps":[{"id":"step_1","description":"write code"}]}`)
	if err := store.UpdateTaskPlan(context.Background(), "task-plan", newPlan); err != nil {
		t.Fatalf("UpdateTaskPlan failed: %v", err)
	}

	loaded, err := store.LoadTask(context.Background(), "task-plan")
	if err != nil {
		t.Fatalf("LoadTask failed: %v", err)
	}
	if string(loaded.Plan) != string(newPlan) {
		t.Errorf("Plan mismatch: got %s, want %s", loaded.Plan, newPlan)
	}
}

func TestUpdateTaskRouting(t *testing.T) {
	store, sessionID, cleanup := setupTestStoreWithSession(t)
	defer cleanup()

	if err := store.SaveTask(context.Background(), TaskRecord{
		ID: "task-routing", SessionID: sessionID, OriginalRequest: "test",
		RoutingDecision: json.RawMessage(`{}`), Plan: json.RawMessage(`{}`),
		Reflections: json.RawMessage(`[]`), Status: "in_progress", CreatedAt: time.Now(),
	}); err != nil {
		t.Fatalf("SaveTask failed: %v", err)
	}

	newRouting := json.RawMessage(`{"domain":"research","complexity":3}`)
	if err := store.UpdateTaskRouting(context.Background(), "task-routing", newRouting); err != nil {
		t.Fatalf("UpdateTaskRouting failed: %v", err)
	}

	loaded, err := store.LoadTask(context.Background(), "task-routing")
	if err != nil {
		t.Fatalf("LoadTask failed: %v", err)
	}
	if string(loaded.RoutingDecision) != string(newRouting) {
		t.Errorf("RoutingDecision mismatch: got %s, want %s", loaded.RoutingDecision, newRouting)
	}
}

func TestSaveTaskStep(t *testing.T) {
	store, sessionID, cleanup := setupTestStoreWithSession(t)
	defer cleanup()

	if err := store.SaveTask(context.Background(), TaskRecord{
		ID: "task-step", SessionID: sessionID, OriginalRequest: "test",
		RoutingDecision: json.RawMessage(`{}`), Plan: json.RawMessage(`{}`),
		Reflections: json.RawMessage(`[]`), Status: "in_progress", CreatedAt: time.Now(),
	}); err != nil {
		t.Fatalf("SaveTask failed: %v", err)
	}

	now := time.Now().Truncate(time.Second)
	step := TaskStepRecord{
		StepID:     "step_1",
		TaskID:     "task-step",
		Summary:    "wrote code",
		FullOutput: "full output of step 1",
		ErrorText:  "",
		Steps:      json.RawMessage(`[{"thought":"thinking"}]`),
		CreatedAt:  now,
	}

	if err := store.SaveTaskStep(context.Background(), "task-step", step); err != nil {
		t.Fatalf("SaveTaskStep failed: %v", err)
	}

	steps, err := store.LoadTaskSteps(context.Background(), "task-step")
	if err != nil {
		t.Fatalf("LoadTaskSteps failed: %v", err)
	}
	if len(steps) != 1 {
		t.Fatalf("expected 1 step, got %d", len(steps))
	}
	if steps[0].StepID != "step_1" {
		t.Errorf("StepID mismatch: got %q", steps[0].StepID)
	}
	if steps[0].Summary != "wrote code" {
		t.Errorf("Summary mismatch: got %q", steps[0].Summary)
	}
	if steps[0].FullOutput != "full output of step 1" {
		t.Errorf("FullOutput mismatch: got %q", steps[0].FullOutput)
	}
	if string(steps[0].Steps) != `[{"thought":"thinking"}]` {
		t.Errorf("Steps JSON mismatch: got %s", steps[0].Steps)
	}
}

func TestSaveTaskStep_Multiple(t *testing.T) {
	store, sessionID, cleanup := setupTestStoreWithSession(t)
	defer cleanup()

	if err := store.SaveTask(context.Background(), TaskRecord{
		ID: "task-multi-step", SessionID: sessionID, OriginalRequest: "test",
		RoutingDecision: json.RawMessage(`{}`), Plan: json.RawMessage(`{}`),
		Reflections: json.RawMessage(`[]`), Status: "in_progress", CreatedAt: time.Now(),
	}); err != nil {
		t.Fatalf("SaveTask failed: %v", err)
	}

	base := time.Now().Truncate(time.Second)
	for i := 0; i < 3; i++ {
		step := TaskStepRecord{
			StepID:     fmt.Sprintf("step_%d", i+1),
			TaskID:     "task-multi-step",
			Summary:    fmt.Sprintf("summary %d", i+1),
			FullOutput: fmt.Sprintf("output %d", i+1),
			Steps:      json.RawMessage(`[]`),
			CreatedAt:  base.Add(time.Duration(i) * time.Second),
		}
		if err := store.SaveTaskStep(context.Background(), "task-multi-step", step); err != nil {
			t.Fatalf("SaveTaskStep %d failed: %v", i, err)
		}
	}

	steps, err := store.LoadTaskSteps(context.Background(), "task-multi-step")
	if err != nil {
		t.Fatalf("LoadTaskSteps failed: %v", err)
	}
	if len(steps) != 3 {
		t.Fatalf("expected 3 steps, got %d", len(steps))
	}
	// Verify ordering by created_at ASC
	for i, s := range steps {
		expected := fmt.Sprintf("step_%d", i+1)
		if s.StepID != expected {
			t.Errorf("step %d: expected ID %q, got %q", i, expected, s.StepID)
		}
	}
}

func TestAddTaskReflection(t *testing.T) {
	store, sessionID, cleanup := setupTestStoreWithSession(t)
	defer cleanup()

	if err := store.SaveTask(context.Background(), TaskRecord{
		ID: "task-reflect", SessionID: sessionID, OriginalRequest: "test",
		RoutingDecision: json.RawMessage(`{}`), Plan: json.RawMessage(`{}`),
		Reflections: json.RawMessage(`[]`), Status: "in_progress", CreatedAt: time.Now(),
	}); err != nil {
		t.Fatalf("SaveTask failed: %v", err)
	}

	// Add first reflection
	r1 := json.RawMessage(`{"summary":"first"}`)
	if err := store.AddTaskReflection(context.Background(), "task-reflect", r1); err != nil {
		t.Fatalf("AddTaskReflection 1 failed: %v", err)
	}

	// Add second reflection
	r2 := json.RawMessage(`{"summary":"second"}`)
	if err := store.AddTaskReflection(context.Background(), "task-reflect", r2); err != nil {
		t.Fatalf("AddTaskReflection 2 failed: %v", err)
	}

	loaded, err := store.LoadTask(context.Background(), "task-reflect")
	if err != nil {
		t.Fatalf("LoadTask failed: %v", err)
	}

	var reflections []json.RawMessage
	if err := json.Unmarshal(loaded.Reflections, &reflections); err != nil {
		t.Fatalf("failed to unmarshal reflections: %v", err)
	}
	if len(reflections) != 2 {
		t.Fatalf("expected 2 reflections, got %d", len(reflections))
	}
	if string(reflections[0]) != `{"summary":"first"}` {
		t.Errorf("first reflection mismatch: got %s", reflections[0])
	}
	if string(reflections[1]) != `{"summary":"second"}` {
		t.Errorf("second reflection mismatch: got %s", reflections[1])
	}
}

func TestCompleteTask(t *testing.T) {
	store, sessionID, cleanup := setupTestStoreWithSession(t)
	defer cleanup()

	if err := store.SaveTask(context.Background(), TaskRecord{
		ID: "task-complete", SessionID: sessionID, OriginalRequest: "test",
		RoutingDecision: json.RawMessage(`{}`), Plan: json.RawMessage(`{}`),
		Reflections: json.RawMessage(`[]`), Status: "in_progress", CreatedAt: time.Now(),
	}); err != nil {
		t.Fatalf("SaveTask failed: %v", err)
	}

	if err := store.CompleteTask(context.Background(), "task-complete", "task done", 2); err != nil {
		t.Fatalf("CompleteTask failed: %v", err)
	}

	loaded, err := store.LoadTask(context.Background(), "task-complete")
	if err != nil {
		t.Fatalf("LoadTask failed: %v", err)
	}
	if loaded.Status != "completed" {
		t.Errorf("Status mismatch: got %q, want %q", loaded.Status, "completed")
	}
	if loaded.FinalOutput != "task done" {
		t.Errorf("FinalOutput mismatch: got %q", loaded.FinalOutput)
	}
	if loaded.AttemptCount != 2 {
		t.Errorf("AttemptCount mismatch: got %d, want %d", loaded.AttemptCount, 2)
	}
	if loaded.CompletedAt == nil {
		t.Error("CompletedAt should not be nil")
	}
}

func TestFailTask(t *testing.T) {
	store, sessionID, cleanup := setupTestStoreWithSession(t)
	defer cleanup()

	if err := store.SaveTask(context.Background(), TaskRecord{
		ID: "task-fail", SessionID: sessionID, OriginalRequest: "test",
		RoutingDecision: json.RawMessage(`{}`), Plan: json.RawMessage(`{}`),
		Reflections: json.RawMessage(`[]`), Status: "in_progress", CreatedAt: time.Now(),
	}); err != nil {
		t.Fatalf("SaveTask failed: %v", err)
	}

	if err := store.FailTask(context.Background(), "task-fail"); err != nil {
		t.Fatalf("FailTask failed: %v", err)
	}

	loaded, err := store.LoadTask(context.Background(), "task-fail")
	if err != nil {
		t.Fatalf("LoadTask failed: %v", err)
	}
	if loaded.Status != "failed" {
		t.Errorf("Status mismatch: got %q, want %q", loaded.Status, "failed")
	}
	if loaded.CompletedAt == nil {
		t.Error("CompletedAt should not be nil for failed task")
	}
}

func TestGetUnfinishedTask(t *testing.T) {
	store, sessionID, cleanup := setupTestStoreWithSession(t)
	defer cleanup()

	base := time.Now().Truncate(time.Second)

	// Create a completed task
	if err := store.SaveTask(context.Background(), TaskRecord{
		ID: "task-done", SessionID: sessionID, OriginalRequest: "old",
		RoutingDecision: json.RawMessage(`{}`), Plan: json.RawMessage(`{}`),
		Reflections: json.RawMessage(`[]`), Status: "completed", CreatedAt: base,
	}); err != nil {
		t.Fatalf("SaveTask failed: %v", err)
	}

	// Create an in_progress task
	if err := store.SaveTask(context.Background(), TaskRecord{
		ID: "task-active", SessionID: sessionID, OriginalRequest: "current",
		RoutingDecision: json.RawMessage(`{}`), Plan: json.RawMessage(`{}`),
		Reflections: json.RawMessage(`[]`), Status: "in_progress", CreatedAt: base.Add(time.Second),
	}); err != nil {
		t.Fatalf("SaveTask failed: %v", err)
	}

	unfinished, err := store.GetUnfinishedTask(context.Background(), sessionID)
	if err != nil {
		t.Fatalf("GetUnfinishedTask failed: %v", err)
	}
	if unfinished == nil {
		t.Fatal("expected unfinished task")
	}
	if unfinished.ID != "task-active" {
		t.Errorf("expected task-active, got %q", unfinished.ID)
	}
}

func TestGetUnfinishedTask_None(t *testing.T) {
	store, sessionID, cleanup := setupTestStoreWithSession(t)
	defer cleanup()

	// Create only completed tasks
	if err := store.SaveTask(context.Background(), TaskRecord{
		ID: "task-done-1", SessionID: sessionID, OriginalRequest: "done",
		RoutingDecision: json.RawMessage(`{}`), Plan: json.RawMessage(`{}`),
		Reflections: json.RawMessage(`[]`), Status: "completed", CreatedAt: time.Now(),
	}); err != nil {
		t.Fatalf("SaveTask failed: %v", err)
	}

	unfinished, err := store.GetUnfinishedTask(context.Background(), sessionID)
	if err != nil {
		t.Fatalf("GetUnfinishedTask failed: %v", err)
	}
	if unfinished != nil {
		t.Error("expected nil for no unfinished tasks")
	}
}

func TestTaskCascadeDelete(t *testing.T) {
	store, sessionID, cleanup := setupTestStoreWithSession(t)
	defer cleanup()

	// Create task with steps
	if err := store.SaveTask(context.Background(), TaskRecord{
		ID: "task-cascade", SessionID: sessionID, OriginalRequest: "test",
		RoutingDecision: json.RawMessage(`{}`), Plan: json.RawMessage(`{}`),
		Reflections: json.RawMessage(`[]`), Status: "in_progress", CreatedAt: time.Now(),
	}); err != nil {
		t.Fatalf("SaveTask failed: %v", err)
	}

	if err := store.SaveTaskStep(context.Background(), "task-cascade", TaskStepRecord{
		StepID: "step_1", TaskID: "task-cascade", Summary: "s",
		Steps: json.RawMessage(`[]`), CreatedAt: time.Now(),
	}); err != nil {
		t.Fatalf("SaveTaskStep failed: %v", err)
	}

	// Delete session — should cascade to tasks and steps
	if err := store.DeleteSession(context.Background(), sessionID); err != nil {
		t.Fatalf("DeleteSession failed: %v", err)
	}

	// Verify task is gone
	loaded, err := store.LoadTask(context.Background(), "task-cascade")
	if err != nil {
		t.Fatalf("LoadTask after cascade: %v", err)
	}
	if loaded != nil {
		t.Error("task should be deleted by cascade")
	}

	// Verify steps are gone
	steps, err := store.LoadTaskSteps(context.Background(), "task-cascade")
	if err != nil {
		t.Fatalf("LoadTaskSteps after cascade: %v", err)
	}
	if len(steps) != 0 {
		t.Errorf("expected 0 steps after cascade, got %d", len(steps))
	}
}

func TestSaveAndLoadTrajectory(t *testing.T) {
	store, sessionID, cleanup := setupTestStoreWithSession(t)
	defer cleanup()

	// Parent task must exist (trajectory FK references tasks).
	if err := store.SaveTask(context.Background(), TaskRecord{
		ID: "task-traj", SessionID: sessionID, OriginalRequest: "test",
		RoutingDecision: json.RawMessage(`{}`), Plan: json.RawMessage(`{}`),
		Reflections: json.RawMessage(`[]`), Status: "in_progress", CreatedAt: time.Now(),
	}); err != nil {
		t.Fatalf("SaveTask failed: %v", err)
	}

	payload := json.RawMessage(`[{"thought":"plan","observation":"done"}]`)

	// Save
	if err := store.SaveTrajectory(context.Background(), "task-traj", payload); err != nil {
		t.Fatalf("SaveTrajectory failed: %v", err)
	}

	// Load — should round-trip the exact bytes
	loaded, err := store.LoadTrajectory(context.Background(), "task-traj")
	if err != nil {
		t.Fatalf("LoadTrajectory failed: %v", err)
	}
	if loaded == nil {
		t.Fatal("expected non-nil trajectory")
	}

	var got []map[string]any
	if err := json.Unmarshal(loaded, &got); err != nil {
		t.Fatalf("unmarshal loaded trajectory: %v", err)
	}
	if len(got) != 1 {
		t.Fatalf("expected 1 step, got %d", len(got))
	}
	if got[0]["thought"] != "plan" {
		t.Errorf("thought: got %v", got[0]["thought"])
	}
	if got[0]["observation"] != "done" {
		t.Errorf("observation: got %v", got[0]["observation"])
	}

	// Save again (replace) — should overwrite, not duplicate.
	updated := json.RawMessage(`[{"thought":"new"}]`)
	if err := store.SaveTrajectory(context.Background(), "task-traj", updated); err != nil {
		t.Fatalf("SaveTrajectory (replace) failed: %v", err)
	}
	loaded2, err := store.LoadTrajectory(context.Background(), "task-traj")
	if err != nil {
		t.Fatalf("LoadTrajectory (replace) failed: %v", err)
	}
	if string(loaded2) != string(updated) {
		t.Errorf("expected replaced trajectory %s, got %s", updated, loaded2)
	}
}

func TestLoadTrajectory_NotFound(t *testing.T) {
	store, _, cleanup := setupTestStoreWithSession(t)
	defer cleanup()

	// No trajectory persisted for this task → nil, nil (not an error).
	loaded, err := store.LoadTrajectory(context.Background(), "missing-task")
	if err != nil {
		t.Fatalf("LoadTrajectory should not error on missing, got: %v", err)
	}
	if loaded != nil {
		t.Errorf("expected nil trajectory for missing task, got %s", loaded)
	}
}

func TestTaskTrajectory_CascadeDelete(t *testing.T) {
	store, sessionID, cleanup := setupTestStoreWithSession(t)
	defer cleanup()

	if err := store.SaveTask(context.Background(), TaskRecord{
		ID: "task-traj-cascade", SessionID: sessionID, OriginalRequest: "test",
		RoutingDecision: json.RawMessage(`{}`), Plan: json.RawMessage(`{}`),
		Reflections: json.RawMessage(`[]`), Status: "in_progress", CreatedAt: time.Now(),
	}); err != nil {
		t.Fatalf("SaveTask failed: %v", err)
	}
	if err := store.SaveTrajectory(context.Background(), "task-traj-cascade", json.RawMessage(`[]`)); err != nil {
		t.Fatalf("SaveTrajectory failed: %v", err)
	}

	// Confirm present.
	if loaded, err := store.LoadTrajectory(context.Background(), "task-traj-cascade"); err != nil || loaded == nil {
		t.Fatalf("expected trajectory present before cascade (loaded=%v, err=%v)", loaded, err)
	}

	// Deleting the task must cascade to the trajectory (FK ON DELETE CASCADE).
	if _, err := store.db.ExecContext(context.Background(), `DELETE FROM tasks WHERE id = ?`, "task-traj-cascade"); err != nil {
		t.Fatalf("delete task failed: %v", err)
	}

	loaded, err := store.LoadTrajectory(context.Background(), "task-traj-cascade")
	if err != nil {
		t.Fatalf("LoadTrajectory after cascade: %v", err)
	}
	if loaded != nil {
		t.Error("trajectory should be deleted by task cascade")
	}
}

func TestLoadTask_NotFound(t *testing.T) {
	store, _, cleanup := setupTestStoreWithSession(t)
	defer cleanup()

	loaded, err := store.LoadTask(context.Background(), "nonexistent")
	if err != nil {
		t.Fatalf("LoadTask error: %v", err)
	}
	if loaded != nil {
		t.Error("expected nil for missing task")
	}
}

func TestLoadTaskSteps_Empty(t *testing.T) {
	store, sessionID, cleanup := setupTestStoreWithSession(t)
	defer cleanup()

	if err := store.SaveTask(context.Background(), TaskRecord{
		ID: "task-empty-steps", SessionID: sessionID, OriginalRequest: "test",
		RoutingDecision: json.RawMessage(`{}`), Plan: json.RawMessage(`{}`),
		Reflections: json.RawMessage(`[]`), Status: "in_progress", CreatedAt: time.Now(),
	}); err != nil {
		t.Fatalf("SaveTask failed: %v", err)
	}

	steps, err := store.LoadTaskSteps(context.Background(), "task-empty-steps")
	if err != nil {
		t.Fatalf("LoadTaskSteps error: %v", err)
	}
	if len(steps) != 0 {
		t.Errorf("expected empty steps, got %d", len(steps))
	}
	if steps == nil {
		t.Error("steps should not be nil")
	}
}

// TestReactivateTask verifies that ReactivateTask changes status to in_progress
// and clears the completed_at timestamp.
func TestReactivateTask(t *testing.T) {
	store, sessionID, cleanup := setupTestStoreWithSession(t)
	defer cleanup()

	// Create a completed task
	now := time.Now().Truncate(time.Second)
	completedAt := now.Add(5 * time.Minute)
	if err := store.SaveTask(context.Background(), TaskRecord{
		ID:              "task-reactivate",
		SessionID:       sessionID,
		OriginalRequest: "test task",
		RoutingDecision: json.RawMessage(`{}`),
		Plan:            json.RawMessage(`{}`),
		Reflections:     json.RawMessage(`[]`),
		Status:          "completed",
		CreatedAt:       now,
		CompletedAt:     &completedAt,
	}); err != nil {
		t.Fatalf("SaveTask failed: %v", err)
	}

	// Verify task is completed
	loaded, err := store.LoadTask(context.Background(), "task-reactivate")
	if err != nil {
		t.Fatalf("LoadTask failed: %v", err)
	}
	if loaded.Status != "completed" {
		t.Fatalf("expected status 'completed', got %q", loaded.Status)
	}
	if loaded.CompletedAt == nil {
		t.Fatal("expected CompletedAt to be set before reactivation")
	}

	// Reactivate the task
	if err := store.ReactivateTask(context.Background(), "task-reactivate"); err != nil {
		t.Fatalf("ReactivateTask failed: %v", err)
	}

	// Verify task is now in_progress and completed_at is cleared
	reactivated, err := store.LoadTask(context.Background(), "task-reactivate")
	if err != nil {
		t.Fatalf("LoadTask after reactivation failed: %v", err)
	}
	if reactivated.Status != "in_progress" {
		t.Errorf("expected status 'in_progress', got %q", reactivated.Status)
	}
	if reactivated.CompletedAt != nil {
		t.Errorf("expected CompletedAt to be nil after reactivation, got %v", reactivated.CompletedAt)
	}
}

// TestSaveAndLoadSessionModelFamily verifies model and family are persisted and loaded.
func TestSaveAndLoadSessionModelFamily(t *testing.T) {
	store, cleanup := setupTestStore(t)
	defer cleanup()

	session := SessionInfo{
		ID:        "model-family-test",
		ProjectID: testProjectID,
		Name:      "Model Family Test",
		CreatedAt: time.Now().Format(time.RFC3339),
		Model:     "claude-3-sonnet",
		Family:    "anthropic",
	}

	if err := store.SaveSession(context.Background(), session); err != nil {
		t.Fatalf("failed to save session: %v", err)
	}

	loaded, err := store.LoadSession(context.Background(), session.ID)
	if err != nil {
		t.Fatalf("failed to load session: %v", err)
	}
	if loaded == nil {
		t.Fatal("loaded session should not be nil")
	}
	if loaded.Model != "claude-3-sonnet" {
		t.Errorf("model mismatch: got %q, want %q", loaded.Model, "claude-3-sonnet")
	}
	if loaded.Family != "anthropic" {
		t.Errorf("family mismatch: got %q, want %q", loaded.Family, "anthropic")
	}
}

// TestModelFamilyDefaultsEmpty verifies model and family default to empty strings.
func TestModelFamilyDefaultsEmpty(t *testing.T) {
	store, cleanup := setupTestStore(t)
	defer cleanup()

	session := SessionInfo{
		ID:        "model-default-test",
		ProjectID: testProjectID,
		Name:      "Default Model Test",
		CreatedAt: time.Now().Format(time.RFC3339),
	}

	if err := store.SaveSession(context.Background(), session); err != nil {
		t.Fatalf("failed to save session: %v", err)
	}

	loaded, err := store.LoadSession(context.Background(), session.ID)
	if err != nil {
		t.Fatalf("failed to load session: %v", err)
	}
	if loaded == nil {
		t.Fatal("loaded session should not be nil")
	}
	if loaded.Model != "" {
		t.Errorf("model should default to empty: got %q", loaded.Model)
	}
	if loaded.Family != "" {
		t.Errorf("family should default to empty: got %q", loaded.Family)
	}
}

// TestListSessionsIncludesModelFamily verifies list queries return model and family.
func TestListSessionsIncludesModelFamily(t *testing.T) {
	store, cleanup := setupTestStore(t)
	defer cleanup()

	session := SessionInfo{
		ID:        "list-model-test",
		ProjectID: testProjectID,
		Name:      "List Model Test",
		CreatedAt: time.Now().Format(time.RFC3339),
		Model:     "gpt-4o",
		Family:    "openai",
	}
	if err := store.SaveSession(context.Background(), session); err != nil {
		t.Fatalf("failed to save session: %v", err)
	}

	// Test ListSessions
	listed, err := store.ListSessions(context.Background())
	if err != nil {
		t.Fatalf("failed to list sessions: %v", err)
	}
	if len(listed) != 1 {
		t.Fatalf("expected 1 session, got %d", len(listed))
	}
	if listed[0].Model != "gpt-4o" {
		t.Errorf("listed model mismatch: got %q, want %q", listed[0].Model, "gpt-4o")
	}
	if listed[0].Family != "openai" {
		t.Errorf("listed family mismatch: got %q, want %q", listed[0].Family, "openai")
	}

	// Test ListSessionsByProject
	byProject, err := store.ListSessionsByProject(context.Background(), testProjectID)
	if err != nil {
		t.Fatalf("failed to list sessions by project: %v", err)
	}
	if len(byProject) != 1 {
		t.Fatalf("expected 1 session, got %d", len(byProject))
	}
	if byProject[0].Model != "gpt-4o" {
		t.Errorf("by-project model mismatch: got %q, want %q", byProject[0].Model, "gpt-4o")
	}
	if byProject[0].Family != "openai" {
		t.Errorf("by-project family mismatch: got %q, want %q", byProject[0].Family, "openai")
	}
}

func TestGetLatestTaskID(t *testing.T) {
	store, sessionID, cleanup := setupTestStoreWithSession(t)
	defer cleanup()

	base := time.Now().Truncate(time.Second)

	// Create tasks with known ordering.
	if err := store.SaveTask(context.Background(), TaskRecord{
		ID: "task-old", SessionID: sessionID, OriginalRequest: "old task",
		RoutingDecision: json.RawMessage(`{}`), Plan: json.RawMessage(`{}`),
		Reflections: json.RawMessage(`[]`), Status: "completed", CreatedAt: base,
	}); err != nil {
		t.Fatalf("SaveTask failed: %v", err)
	}
	if err := store.SaveTask(context.Background(), TaskRecord{
		ID: "task-new", SessionID: sessionID, OriginalRequest: "new task",
		RoutingDecision: json.RawMessage(`{}`), Plan: json.RawMessage(`{}`),
		Reflections: json.RawMessage(`[]`), Status: "in_progress", CreatedAt: base.Add(time.Second),
	}); err != nil {
		t.Fatalf("SaveTask failed: %v", err)
	}

	got, err := store.GetLatestTaskID(context.Background(), sessionID)
	if err != nil {
		t.Fatalf("GetLatestTaskID failed: %v", err)
	}
	if got != "task-new" {
		t.Errorf("expected task-new, got %q", got)
	}
}

func TestGetLatestTaskID_NoTasks(t *testing.T) {
	store, sessionID, cleanup := setupTestStoreWithSession(t)
	defer cleanup()

	got, err := store.GetLatestTaskID(context.Background(), sessionID)
	if err != nil {
		t.Fatalf("GetLatestTaskID failed: %v", err)
	}
	if got != "" {
		t.Errorf("expected empty string, got %q", got)
	}
}

// ---------------------------------------------------------------------------
// Session work directories
// ---------------------------------------------------------------------------

func TestSessionWorkDir_SaveListUpdateDelete(t *testing.T) {
	store, sessionID, cleanup := setupTestStoreWithSession(t)
	defer cleanup()

	// rec1 has an explicit older timestamp; rec2 auto-generates id + created_at
	// (now). Ordered ASC by created_at, rec1 comes first.
	rec1 := project.WorkDirectoryRecord{
		ID:          "session-explicit-id",
		Path:        "/tmp/sdir1",
		Description: "build output",
		CreatedAt:   "2024-06-01T12:00:00Z",
	}
	rec2 := project.WorkDirectoryRecord{Path: "/tmp/sdir2", Description: "logs"}
	for _, rec := range []project.WorkDirectoryRecord{rec1, rec2} {
		if err := store.SaveSessionWorkDir(context.Background(), sessionID, rec); err != nil {
			t.Fatalf("failed to save work dir: %v", err)
		}
	}

	// List returns both, ordered oldest-first.
	listed, err := store.ListSessionWorkDirs(context.Background(), sessionID)
	if err != nil {
		t.Fatalf("failed to list work dirs: %v", err)
	}
	if len(listed) != 2 {
		t.Fatalf("expected 2 work dirs, got %d", len(listed))
	}
	// Explicit id + created_at preserved on the oldest row.
	if listed[0].ID != "session-explicit-id" {
		t.Errorf("ID mismatch: got %q, want session-explicit-id", listed[0].ID)
	}
	if listed[0].CreatedAt != "2024-06-01T12:00:00Z" {
		t.Errorf("CreatedAt mismatch: got %q", listed[0].CreatedAt)
	}
	if listed[0].Path != "/tmp/sdir1" {
		t.Errorf("Path mismatch on oldest: got %q, want /tmp/sdir1", listed[0].Path)
	}
	// Auto-generated id + created_at should be populated on the newer row.
	if listed[1].ID == "" {
		t.Error("auto-generated ID should not be empty")
	}
	if listed[1].CreatedAt == "" {
		t.Error("auto-generated CreatedAt should not be empty")
	}

	// Update description on the explicit-id row.
	if err := store.UpdateSessionWorkDirDescription(context.Background(), sessionID, "session-explicit-id", "updated description"); err != nil {
		t.Fatalf("failed to update description: %v", err)
	}
	listed, err = store.ListSessionWorkDirs(context.Background(), sessionID)
	if err != nil {
		t.Fatalf("failed to list after update: %v", err)
	}
	if listed[0].Description != "updated description" {
		t.Errorf("description should be updated, got %q", listed[0].Description)
	}

	// Delete the explicit-id row.
	if err := store.DeleteSessionWorkDir(context.Background(), sessionID, "session-explicit-id"); err != nil {
		t.Fatalf("failed to delete work dir: %v", err)
	}
	listed, err = store.ListSessionWorkDirs(context.Background(), sessionID)
	if err != nil {
		t.Fatalf("failed to list after delete: %v", err)
	}
	if len(listed) != 1 || listed[0].ID == "session-explicit-id" {
		t.Errorf("expected only the auto-generated row to remain, got %#v", listed)
	}
}

func TestSessionWorkDir_ListEmptyReturnsNonNilSlice(t *testing.T) {
	store, sessionID, cleanup := setupTestStoreWithSession(t)
	defer cleanup()

	listed, err := store.ListSessionWorkDirs(context.Background(), sessionID)
	if err != nil {
		t.Fatalf("failed to list empty work dirs: %v", err)
	}
	if listed == nil {
		t.Fatal("expected non-nil slice for empty result")
	}
	if len(listed) != 0 {
		t.Errorf("expected empty slice, got %d items", len(listed))
	}
}

func TestSessionWorkDir_CascadeOnSessionDelete(t *testing.T) {
	store, sessionID, cleanup := setupTestStoreWithSession(t)
	defer cleanup()

	if err := store.SaveSessionWorkDir(context.Background(), sessionID, project.WorkDirectoryRecord{
		Path:        "/tmp/scascade",
		Description: "should be removed",
	}); err != nil {
		t.Fatalf("failed to save work dir: %v", err)
	}

	// Confirm it exists.
	listed, err := store.ListSessionWorkDirs(context.Background(), sessionID)
	if err != nil {
		t.Fatalf("failed to list before delete: %v", err)
	}
	if len(listed) != 1 {
		t.Fatalf("expected 1 work dir before cascade, got %d", len(listed))
	}

	// Delete the session — FK cascade must remove the work dir row.
	if err := store.DeleteSession(context.Background(), sessionID); err != nil {
		t.Fatalf("failed to delete session: %v", err)
	}

	listed, err = store.ListSessionWorkDirs(context.Background(), sessionID)
	if err != nil {
		t.Fatalf("failed to list after session delete: %v", err)
	}
	if len(listed) != 0 {
		t.Errorf("expected work dirs to cascade-delete with session, got %d", len(listed))
	}
}

func TestSessionWorkDir_IsolationBySession(t *testing.T) {
	store, _, cleanup := setupTestStoreWithSession(t)
	defer cleanup()

	// Create a second session under the same project.
	const sessionB = "session-workdir-b"
	if err := store.SaveSession(context.Background(), SessionInfo{
		ID:        sessionB,
		ProjectID: testProjectID,
		Name:      "Session B",
		CreatedAt: time.Now().Format(time.RFC3339),
	}); err != nil {
		t.Fatalf("failed to create session B: %v", err)
	}
	const sessionA = "task-test-session"
	if err := store.SaveSessionWorkDir(context.Background(), sessionA, project.WorkDirectoryRecord{Path: "/tmp/a", Description: "a"}); err != nil {
		t.Fatalf("failed to save work dir for A: %v", err)
	}
	if err := store.SaveSessionWorkDir(context.Background(), sessionB, project.WorkDirectoryRecord{Path: "/tmp/b", Description: "b"}); err != nil {
		t.Fatalf("failed to save work dir for B: %v", err)
	}

	aDirs, err := store.ListSessionWorkDirs(context.Background(), sessionA)
	if err != nil {
		t.Fatalf("failed to list A work dirs: %v", err)
	}
	if len(aDirs) != 1 || aDirs[0].Path != "/tmp/a" {
		t.Errorf("session A should only see its own work dir, got %#v", aDirs)
	}
	bDirs, err := store.ListSessionWorkDirs(context.Background(), sessionB)
	if err != nil {
		t.Fatalf("failed to list B work dirs: %v", err)
	}
	if len(bDirs) != 1 || bDirs[0].Path != "/tmp/b" {
		t.Errorf("session B should only see its own work dir, got %#v", bDirs)
	}
}
