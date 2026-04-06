// Package session provides session-scoped event emission and persistence for the desktop UI.
package session

import (
	"context"
	"database/sql"
	"encoding/json"
	"fmt"
	"time"

	_ "modernc.org/sqlite" // register SQLite driver
)

// SessionInfo is the public-facing session metadata.
type SessionInfo struct {
	ID                string `json:"id"`
	Name              string `json:"name"`
	CreatedAt         string `json:"created_at"`     // RFC 3339 formatted timestamp
	LastActiveAt      string `json:"last_active_at"` // RFC 3339 formatted timestamp
	Archived          bool   `json:"archived"`
	Active            bool   `json:"active"`
	TotalInputTokens  int    `json:"total_input_tokens"`
	TotalOutputTokens int    `json:"total_output_tokens"`
}

// ChatMessage represents a stored chat message.
type ChatMessage struct {
	ID        int64           `json:"id"`
	SessionID string          `json:"session_id"`
	Role      string          `json:"role"` // "user", "assistant", "tool_call", "tool_result", "routing", "eval", "reflection", "error"
	Content   string          `json:"content"`
	Metadata  json.RawMessage `json:"metadata"`   // JSON blob for extra data
	CreatedAt string          `json:"created_at"` // RFC 3339 formatted timestamp
}

// SessionStore provides persistent storage for sessions and messages.
type SessionStore interface {
	// Session CRUD
	SaveSession(info SessionInfo) error
	LoadSession(id string) (*SessionInfo, error)
	ListSessions() ([]SessionInfo, error)
	DeleteSession(id string) error
	ArchiveSession(id string, archived bool) error
	RenameSession(id, name string) error

	// Token tracking
	UpdateSessionTokens(id string, inputTokens, outputTokens int) error

	// Activity tracking
	UpdateSessionActivity(id string) error

	// Message operations
	SaveMessage(msg ChatMessage) error
	LoadMessages(sessionID string) ([]ChatMessage, error)
	DeleteMessages(sessionID string) error

	// Lifecycle
	Close() error
}

// SQLiteSessionStore implements SessionStore using SQLite.
type SQLiteSessionStore struct {
	db *sql.DB
}

// NewSQLiteSessionStore opens SQLite at dbPath and auto-creates tables.
// Use ":memory:" for in-memory testing.
func NewSQLiteSessionStore(dbPath string) (*SQLiteSessionStore, error) {
	db, err := sql.Open("sqlite", dbPath)
	if err != nil {
		return nil, fmt.Errorf("failed to open sqlite database: %w", err)
	}

	// Enable WAL mode for better performance
	if _, err := db.ExecContext(context.Background(), "PRAGMA journal_mode=WAL"); err != nil {
		_ = db.Close()
		return nil, fmt.Errorf("failed to enable WAL mode: %w", err)
	}

	// Enable foreign keys for cascade deletes
	if _, err := db.ExecContext(context.Background(), "PRAGMA foreign_keys=ON"); err != nil {
		_ = db.Close()
		return nil, fmt.Errorf("failed to enable foreign keys: %w", err)
	}

	store := &SQLiteSessionStore{db: db}

	if err := store.createTables(); err != nil {
		_ = db.Close()
		return nil, fmt.Errorf("failed to create tables: %w", err)
	}

	return store, nil
}

// createTables creates the necessary tables and indexes.
func (s *SQLiteSessionStore) createTables() error {
	schema := `
	CREATE TABLE IF NOT EXISTS sessions (
		id TEXT PRIMARY KEY,
		name TEXT NOT NULL,
		created_at TIMESTAMP NOT NULL,
		archived BOOLEAN DEFAULT FALSE
	);

	CREATE TABLE IF NOT EXISTS session_messages (
		id INTEGER PRIMARY KEY AUTOINCREMENT,
		session_id TEXT NOT NULL REFERENCES sessions(id) ON DELETE CASCADE,
		role TEXT NOT NULL,
		content TEXT NOT NULL,
		metadata TEXT DEFAULT '',
		created_at TIMESTAMP NOT NULL
	);

	CREATE INDEX IF NOT EXISTS idx_session_messages_session_id ON session_messages(session_id);
	`
	if _, err := s.db.ExecContext(context.Background(), schema); err != nil {
		return err
	}

	// Migrate: add token columns to sessions (ignore "duplicate column" errors)
	migrations := []string{
		"ALTER TABLE sessions ADD COLUMN total_input_tokens INTEGER DEFAULT 0",
		"ALTER TABLE sessions ADD COLUMN total_output_tokens INTEGER DEFAULT 0",
	}
	for _, m := range migrations {
		_, _ = s.db.ExecContext(context.Background(), m)
	}

	// Migrate: add last_active_at column (ignore "duplicate column" errors)
	_, _ = s.db.ExecContext(context.Background(), "ALTER TABLE sessions ADD COLUMN last_active_at TIMESTAMP")
	// Backfill: set last_active_at to created_at for existing rows where it's NULL
	_, _ = s.db.ExecContext(context.Background(), "UPDATE sessions SET last_active_at = created_at WHERE last_active_at IS NULL")

	return nil
}

// SaveSession saves or updates a session.
func (s *SQLiteSessionStore) SaveSession(info SessionInfo) error {
	// Use created_at as fallback for last_active_at if not set
	lastActiveAt := info.LastActiveAt
	if lastActiveAt == "" {
		lastActiveAt = info.CreatedAt
	}
	_, err := s.db.ExecContext(context.Background(), `
		INSERT INTO sessions (id, name, created_at, last_active_at, archived, total_input_tokens, total_output_tokens)
		VALUES (?, ?, ?, ?, ?, ?, ?)
		ON CONFLICT(id) DO UPDATE SET
			name = excluded.name,
			last_active_at = excluded.last_active_at,
			archived = excluded.archived,
			total_input_tokens = excluded.total_input_tokens,
			total_output_tokens = excluded.total_output_tokens`,
		info.ID, info.Name, info.CreatedAt, lastActiveAt, info.Archived, info.TotalInputTokens, info.TotalOutputTokens,
	)
	if err != nil {
		return fmt.Errorf("failed to save session: %w", err)
	}
	return nil
}

// LoadSession loads a session by ID.
func (s *SQLiteSessionStore) LoadSession(id string) (*SessionInfo, error) {
	var info SessionInfo
	err := s.db.QueryRowContext(context.Background(), `
		SELECT id, name, created_at, COALESCE(last_active_at, created_at), archived, COALESCE(total_input_tokens, 0), COALESCE(total_output_tokens, 0) FROM sessions WHERE id = ?`,
		id,
	).Scan(&info.ID, &info.Name, &info.CreatedAt, &info.LastActiveAt, &info.Archived, &info.TotalInputTokens, &info.TotalOutputTokens)

	if err == sql.ErrNoRows {
		return nil, nil
	}
	if err != nil {
		return nil, fmt.Errorf("failed to load session: %w", err)
	}

	return &info, nil
}

// ListSessions returns all sessions ordered by last activity time (newest first).
func (s *SQLiteSessionStore) ListSessions() ([]SessionInfo, error) {
	rows, err := s.db.QueryContext(context.Background(), `
		SELECT id, name, created_at, COALESCE(last_active_at, created_at), archived, COALESCE(total_input_tokens, 0), COALESCE(total_output_tokens, 0) FROM sessions
		ORDER BY last_active_at DESC`)
	if err != nil {
		return nil, fmt.Errorf("failed to list sessions: %w", err)
	}
	defer func() { _ = rows.Close() }()

	var sessions []SessionInfo
	for rows.Next() {
		var info SessionInfo
		if err := rows.Scan(&info.ID, &info.Name, &info.CreatedAt, &info.LastActiveAt, &info.Archived, &info.TotalInputTokens, &info.TotalOutputTokens); err != nil {
			return nil, fmt.Errorf("failed to scan session: %w", err)
		}
		sessions = append(sessions, info)
	}

	if err := rows.Err(); err != nil {
		return nil, fmt.Errorf("error iterating sessions: %w", err)
	}

	// Return empty slice instead of nil for consistency
	if sessions == nil {
		sessions = []SessionInfo{}
	}

	return sessions, nil
}

// DeleteSession deletes a session and all its messages (cascade).
func (s *SQLiteSessionStore) DeleteSession(id string) error {
	_, err := s.db.ExecContext(context.Background(), `DELETE FROM sessions WHERE id = ?`, id)
	if err != nil {
		return fmt.Errorf("failed to delete session: %w", err)
	}
	return nil
}

// ArchiveSession sets the archived flag on a session.
func (s *SQLiteSessionStore) ArchiveSession(id string, archived bool) error {
	_, err := s.db.ExecContext(context.Background(), `UPDATE sessions SET archived = ? WHERE id = ?`, archived, id)
	if err != nil {
		return fmt.Errorf("failed to archive session: %w", err)
	}
	return nil
}

// RenameSession updates a session's name.
func (s *SQLiteSessionStore) RenameSession(id, name string) error {
	_, err := s.db.ExecContext(context.Background(), `UPDATE sessions SET name = ? WHERE id = ?`, name, id)
	if err != nil {
		return fmt.Errorf("failed to rename session: %w", err)
	}
	return nil
}

// UpdateSessionTokens updates the accumulated token counts for a session.
func (s *SQLiteSessionStore) UpdateSessionTokens(id string, inputTokens, outputTokens int) error {
	_, err := s.db.ExecContext(context.Background(), `
		UPDATE sessions SET total_input_tokens = ?, total_output_tokens = ? WHERE id = ?`,
		inputTokens, outputTokens, id,
	)
	if err != nil {
		return fmt.Errorf("failed to update session tokens: %w", err)
	}
	return nil
}

// UpdateSessionActivity updates the last_active_at timestamp for a session.
func (s *SQLiteSessionStore) UpdateSessionActivity(id string) error {
	now := time.Now().Format(time.RFC3339)
	_, err := s.db.ExecContext(context.Background(), `
		UPDATE sessions SET last_active_at = ? WHERE id = ?`,
		now, id,
	)
	if err != nil {
		return fmt.Errorf("failed to update session activity: %w", err)
	}
	return nil
}

// SaveMessage saves a chat message.
func (s *SQLiteSessionStore) SaveMessage(msg ChatMessage) error {
	_, err := s.db.ExecContext(context.Background(), `
		INSERT INTO session_messages (session_id, role, content, metadata, created_at)
		VALUES (?, ?, ?, ?, ?)`,
		msg.SessionID, msg.Role, msg.Content, string(msg.Metadata), msg.CreatedAt,
	)
	if err != nil {
		return fmt.Errorf("failed to save message: %w", err)
	}
	return nil
}

// LoadMessages loads all messages for a session ordered by creation time.
func (s *SQLiteSessionStore) LoadMessages(sessionID string) ([]ChatMessage, error) {
	rows, err := s.db.QueryContext(context.Background(), `
		SELECT id, session_id, role, content, metadata, created_at
		FROM session_messages
		WHERE session_id = ?
		ORDER BY created_at ASC`,
		sessionID,
	)
	if err != nil {
		return nil, fmt.Errorf("failed to load messages: %w", err)
	}
	defer func() { _ = rows.Close() }()

	var messages []ChatMessage
	for rows.Next() {
		var msg ChatMessage
		var metadataStr string
		if err := rows.Scan(&msg.ID, &msg.SessionID, &msg.Role, &msg.Content, &metadataStr, &msg.CreatedAt); err != nil {
			return nil, fmt.Errorf("failed to scan message: %w", err)
		}
		if metadataStr != "" {
			msg.Metadata = json.RawMessage(metadataStr)
		} else {
			msg.Metadata = json.RawMessage("{}")
		}
		messages = append(messages, msg)
	}

	if err := rows.Err(); err != nil {
		return nil, fmt.Errorf("error iterating messages: %w", err)
	}

	// Return empty slice instead of nil for consistency
	if messages == nil {
		messages = []ChatMessage{}
	}

	return messages, nil
}

// DeleteMessages deletes all messages for a session.
func (s *SQLiteSessionStore) DeleteMessages(sessionID string) error {
	_, err := s.db.ExecContext(context.Background(), `DELETE FROM session_messages WHERE session_id = ?`, sessionID)
	if err != nil {
		return fmt.Errorf("failed to delete messages: %w", err)
	}
	return nil
}

// Close closes the database connection.
func (s *SQLiteSessionStore) Close() error {
	return s.db.Close()
}
