// Package session provides session-scoped event emission and persistence for the desktop UI.
package session

import (
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"fmt"
	"log/slog"
	"strings"
	"time"
)

// SessionInfo is the public-facing session metadata.
type SessionInfo struct {
	ID                string `json:"id"`
	ProjectID         string `json:"project_id"`
	Name              string `json:"name"`
	CreatedAt         string `json:"created_at"`     // RFC 3339 formatted timestamp
	LastActiveAt      string `json:"last_active_at"` // RFC 3339 formatted timestamp
	Archived          bool   `json:"archived"`
	Active            bool   `json:"active"`
	TotalInputTokens  int    `json:"total_input_tokens"`
	TotalOutputTokens int    `json:"total_output_tokens"`
	Model             string `json:"model"`
	Family            string `json:"family"`
	PlanReviewPhase   string `json:"plan_review_phase"` // "" | "awaiting_accept" | "awaiting_feedback"
	PlanReviewPath    string `json:"plan_review_path"`  // path to .md plan file awaiting review
	PlanReviewContext string `json:"plan_review_context"` // JSON with {msg, mode, skills} for restart survival
}

// ChatMessage represents a stored chat message.
type ChatMessage struct {
	ID               int64            `json:"id"`
	SessionID        string           `json:"session_id"`
	Role             string           `json:"role"`              // "user", "assistant", "tool_call", "tool_result", "routing", "eval", "reflection", "error"
	Content          string           `json:"content"`
	ReasoningContent *string          `json:"reasoning_content,omitempty"` // chain-of-thought / reasoning content (DeepSeek)
	ToolCalls        *json.RawMessage `json:"tool_calls,omitempty"`        // JSON-encoded tool calls (for assistant)
	Metadata         json.RawMessage  `json:"metadata"`                    // JSON blob for extra data
	CreatedAt        string           `json:"created_at"`                  // RFC 3339 formatted timestamp
}

// TerminalCommand represents a stored terminal command.
type TerminalCommand struct {
	ID        int64  `json:"id"`
	SessionID string `json:"session_id"`
	Command   string `json:"command"`
	CreatedAt string `json:"created_at"` // RFC 3339 formatted timestamp
}

// SessionStore provides persistent storage for sessions and messages.
type SessionStore interface {
	// Session CRUD
	SaveSession(ctx context.Context, info SessionInfo) error
	LoadSession(ctx context.Context, id string) (*SessionInfo, error)
	ListSessions(ctx context.Context) ([]SessionInfo, error)
	ListSessionsByProject(ctx context.Context, projectID string) ([]SessionInfo, error)
	DeleteSession(ctx context.Context, id string) error
	ArchiveSession(ctx context.Context, id string, archived bool) error
	RenameSession(ctx context.Context, id, name string) error

	// Token tracking
	UpdateSessionTokens(ctx context.Context, id string, inputTokens, outputTokens int, model, family string) error

	// Activity tracking
	UpdateSessionActivity(ctx context.Context, id string) error

	// Message operations
	SaveMessage(ctx context.Context, msg ChatMessage) error
	LoadMessages(ctx context.Context, sessionID string) ([]ChatMessage, error)
	DeleteMessages(ctx context.Context, sessionID string) error

	// Terminal command history
	SaveTerminalCommand(ctx context.Context, sessionID, command string) error
	LoadTerminalCommands(ctx context.Context, sessionID string, limit int) ([]TerminalCommand, error)

	// Lifecycle
	Close() error
}

// PlanReviewStore provides persistence for plan review workflow state.
// It is separate from SessionStore so that implementations that don't need
// plan review support do not have to implement these methods.
type PlanReviewStore interface {
	UpdateSessionPlanReview(ctx context.Context, id, phase, planPath string) error
	UpdateSessionPlanReviewContext(ctx context.Context, id, phase, planPath, contextJSON string) error
	GetSessionsInPlanReview(ctx context.Context, projectID string) ([]SessionInfo, error)
}

// SQLiteSessionStore implements SessionStore using SQLite.
type SQLiteSessionStore struct {
	db     *sql.DB
	logger *slog.Logger
}

// log returns the store's logger, falling back to slog.Default().
func (s *SQLiteSessionStore) log() *slog.Logger {
	if s.logger != nil {
		return s.logger
	}
	return slog.Default()
}

// SetLogger sets the logger for the session store.
func (s *SQLiteSessionStore) SetLogger(l *slog.Logger) {
	s.logger = l
}

// NewSQLiteSessionStore wraps an existing *sql.DB and auto-creates tables.
// The caller is responsible for opening the DB and applying pragmas (WAL, foreign_keys).
// The projects table must be created before calling this (sessions has FK to projects).
func NewSQLiteSessionStore(db *sql.DB) (*SQLiteSessionStore, error) {
	store := &SQLiteSessionStore{db: db}

	if err := store.createTables(); err != nil {
		return nil, fmt.Errorf("failed to create tables: %w", err)
	}

	return store, nil
}

// createTables creates the necessary tables and indexes.
func (s *SQLiteSessionStore) createTables() error {
	schema := `
	CREATE TABLE IF NOT EXISTS sessions (
		id TEXT PRIMARY KEY,
		project_id TEXT NOT NULL REFERENCES projects(id) ON DELETE CASCADE,
		name TEXT NOT NULL,
		created_at TIMESTAMP NOT NULL,
		last_active_at TIMESTAMP,
		archived BOOLEAN DEFAULT FALSE,
		total_input_tokens INTEGER DEFAULT 0,
		total_output_tokens INTEGER DEFAULT 0,
		model TEXT DEFAULT '',
		family TEXT DEFAULT '',
		plan_review_phase TEXT DEFAULT '',
		plan_review_path TEXT DEFAULT '',
		plan_review_context TEXT DEFAULT ''
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
	CREATE INDEX IF NOT EXISTS idx_session_messages_session_role ON session_messages(session_id, role);
	CREATE INDEX IF NOT EXISTS idx_sessions_project_id ON sessions(project_id);

	CREATE TABLE IF NOT EXISTS tasks (
		id TEXT PRIMARY KEY,
		session_id TEXT NOT NULL REFERENCES sessions(id) ON DELETE CASCADE,
		original_request TEXT NOT NULL,
		routing_decision TEXT DEFAULT '{}',
		plan TEXT DEFAULT '{}',
		reflections TEXT DEFAULT '[]',
		final_output TEXT DEFAULT '',
		attempt_count INTEGER DEFAULT 0,
		status TEXT NOT NULL DEFAULT 'in_progress',
		created_at TIMESTAMP NOT NULL,
		completed_at TIMESTAMP
	);
	CREATE INDEX IF NOT EXISTS idx_tasks_session_id ON tasks(session_id);

	CREATE TABLE IF NOT EXISTS task_steps (
		step_id TEXT NOT NULL,
		task_id TEXT NOT NULL REFERENCES tasks(id) ON DELETE CASCADE,
		summary TEXT DEFAULT '',
		full_output TEXT DEFAULT '',
		error_text TEXT DEFAULT '',
		steps TEXT DEFAULT '[]',
		created_at TIMESTAMP NOT NULL,
		PRIMARY KEY (task_id, step_id)
	);

	CREATE TABLE IF NOT EXISTS task_facts (
		task_id TEXT PRIMARY KEY REFERENCES tasks(id) ON DELETE CASCADE,
		facts TEXT DEFAULT '[]',
		updated_at TIMESTAMP NOT NULL
	);

	CREATE TABLE IF NOT EXISTS terminal_commands (
		id INTEGER PRIMARY KEY AUTOINCREMENT,
		session_id TEXT NOT NULL REFERENCES sessions(id) ON DELETE CASCADE,
		command TEXT NOT NULL,
		created_at TIMESTAMP NOT NULL
	);
	CREATE INDEX IF NOT EXISTS idx_terminal_commands_session_id ON terminal_commands(session_id);

	`
	_, err := s.db.ExecContext(context.Background(), schema)
	if err != nil {
		return err
	}

	// Migration: add plan_review columns for existing databases.
	// Retry on SQLITE_BUSY (err 5) — ALTER TABLE requires exclusive access and
	// may contend with in-flight read transactions during startup.
	// Uses PRAGMA table_info to check column existence before ALTER TABLE,
	// avoiding fragile driver-specific error string matching.
	for _, col := range []struct {
		name string
		def  string
	}{
		{"plan_review_phase", "TEXT DEFAULT ''"},
		{"plan_review_path", "TEXT DEFAULT ''"},
		{"plan_review_context", "TEXT DEFAULT ''"},
	} {
		if s.columnExists("sessions", col.name) {
			continue // column already present, skip
		}

		// Column missing — attempt ALTER TABLE with retries on SQLITE_BUSY.
		var lastErr error
		for attempt := range 3 {
			_, lastErr = s.db.ExecContext(context.Background(),
				fmt.Sprintf("ALTER TABLE sessions ADD COLUMN %s %s", col.name, col.def))
			if lastErr == nil {
				break
			}
			if !strings.Contains(lastErr.Error(), "database is locked") {
				break
			}
			if attempt < 2 {
				time.Sleep(time.Duration(attempt+1) * 100 * time.Millisecond)
			}
		}
		if lastErr != nil {
			s.log().Warn("migration ALTER TABLE failed", "column", col.name, "error", lastErr)
		}
	}

	// Migration: add reasoning_content and tool_calls columns for preserving
	// chain-of-thought reasoning and tool call metadata across app restarts.
	for _, col := range []struct {
		name string
		def  string
	}{
		{"reasoning_content", "TEXT"},
		{"tool_calls", "TEXT"},
	} {
		if s.columnExists("session_messages", col.name) {
			continue
		}

		var lastErr error
		for attempt := range 3 {
			_, lastErr = s.db.ExecContext(context.Background(),
				fmt.Sprintf("ALTER TABLE session_messages ADD COLUMN %s %s", col.name, col.def))
			if lastErr == nil {
				break
			}
			if !strings.Contains(lastErr.Error(), "database is locked") {
				break
			}
			if attempt < 2 {
				time.Sleep(time.Duration(attempt+1) * 100 * time.Millisecond)
			}
		}
		if lastErr != nil {
			s.log().Warn("migration ALTER TABLE failed", "table", "session_messages", "column", col.name, "error", lastErr)
		}
	}

	return nil
}

// columnExists checks whether a column exists in a table using PRAGMA table_info.
func (s *SQLiteSessionStore) columnExists(table, column string) bool {
	rows, err := s.db.QueryContext(context.Background(),
		"SELECT 1 FROM pragma_table_info(?) WHERE name = ?", table, column)
	if err != nil {
		return false
	}
	defer func() {
		if cerr := rows.Close(); cerr != nil {
			s.log().Warn("failed to close pragma rows", "error", cerr)
		}
	}()
	return rows.Next()
}

// SaveSession saves or updates a session.
func (s *SQLiteSessionStore) SaveSession(ctx context.Context, info SessionInfo) error {
	// Use created_at as fallback for last_active_at if not set
	lastActiveAt := info.LastActiveAt
	if lastActiveAt == "" {
		lastActiveAt = info.CreatedAt
	}
	_, err := s.db.ExecContext(ctx, `
		INSERT INTO sessions (id, project_id, name, created_at, last_active_at, archived, total_input_tokens, total_output_tokens, model, family, plan_review_phase, plan_review_path, plan_review_context)
		VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?)
		ON CONFLICT(id) DO UPDATE SET
			name = excluded.name,
			last_active_at = excluded.last_active_at,
			archived = excluded.archived,
			total_input_tokens = excluded.total_input_tokens,
			total_output_tokens = excluded.total_output_tokens,
			model = excluded.model,
			family = excluded.family,
			plan_review_phase = excluded.plan_review_phase,
			plan_review_path = excluded.plan_review_path,
			plan_review_context = excluded.plan_review_context`,
		info.ID, info.ProjectID, info.Name, info.CreatedAt, lastActiveAt, info.Archived, info.TotalInputTokens, info.TotalOutputTokens, info.Model, info.Family, info.PlanReviewPhase, info.PlanReviewPath, info.PlanReviewContext,
	)
	if err != nil {
		return fmt.Errorf("failed to save session: %w", err)
	}
	return nil
}

// LoadSession loads a session by ID.
func (s *SQLiteSessionStore) LoadSession(ctx context.Context, id string) (*SessionInfo, error) {
	var info SessionInfo
	err := s.db.QueryRowContext(ctx, `
		SELECT id, project_id, name, created_at, COALESCE(last_active_at, created_at), archived, COALESCE(total_input_tokens, 0), COALESCE(total_output_tokens, 0), COALESCE(model, ''), COALESCE(family, ''), COALESCE(plan_review_phase, ''), COALESCE(plan_review_path, ''), COALESCE(plan_review_context, '') FROM sessions WHERE id = ?`,
		id,
	).Scan(&info.ID, &info.ProjectID, &info.Name, &info.CreatedAt, &info.LastActiveAt, &info.Archived, &info.TotalInputTokens, &info.TotalOutputTokens, &info.Model, &info.Family, &info.PlanReviewPhase, &info.PlanReviewPath, &info.PlanReviewContext)

	if errors.Is(err, sql.ErrNoRows) {
		return nil, nil
	}
	if err != nil {
		return nil, fmt.Errorf("failed to load session: %w", err)
	}

	return &info, nil
}

// ListSessions returns all sessions ordered by last activity time (newest first).
func (s *SQLiteSessionStore) ListSessions(ctx context.Context) ([]SessionInfo, error) {
	rows, err := s.db.QueryContext(ctx, `
		SELECT id, project_id, name, created_at,
		       COALESCE(last_active_at, created_at),
		       archived,
		       COALESCE(total_input_tokens, 0),
		       COALESCE(total_output_tokens, 0),
		       COALESCE(model, ''),
		       COALESCE(family, ''),
		       COALESCE(plan_review_phase, ''),
		       COALESCE(plan_review_path, ''),
		       COALESCE(plan_review_context, '')
		FROM sessions
		ORDER BY COALESCE(last_active_at, created_at) DESC`)
	if err != nil {
		return nil, fmt.Errorf("failed to list sessions: %w", err)
	}
	defer func() {
		if err := rows.Close(); err != nil {
			s.log().Warn("failed to close database rows", "error", err)
		}
	}()

	var sessions []SessionInfo
	for rows.Next() {
		var info SessionInfo
		if err := rows.Scan(&info.ID, &info.ProjectID, &info.Name, &info.CreatedAt, &info.LastActiveAt, &info.Archived, &info.TotalInputTokens, &info.TotalOutputTokens, &info.Model, &info.Family, &info.PlanReviewPhase, &info.PlanReviewPath, &info.PlanReviewContext); err != nil {
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

// ListSessionsByProject returns all sessions for a given project, ordered by last activity (newest first).
func (s *SQLiteSessionStore) ListSessionsByProject(ctx context.Context, projectID string) ([]SessionInfo, error) {
	rows, err := s.db.QueryContext(ctx, `
		SELECT id, project_id, name, created_at, COALESCE(last_active_at, created_at), archived, COALESCE(total_input_tokens, 0), COALESCE(total_output_tokens, 0), COALESCE(model, ''), COALESCE(family, ''), COALESCE(plan_review_phase, ''), COALESCE(plan_review_path, ''), COALESCE(plan_review_context, '') FROM sessions
		WHERE project_id = ?
		ORDER BY COALESCE(last_active_at, created_at) DESC`, projectID)
	if err != nil {
		return nil, fmt.Errorf("failed to list sessions by project: %w", err)
	}
	defer func() {
		if err := rows.Close(); err != nil {
			s.log().Warn("failed to close database rows", "error", err)
		}
	}()

	var sessions []SessionInfo
	for rows.Next() {
		var info SessionInfo
		if err := rows.Scan(&info.ID, &info.ProjectID, &info.Name, &info.CreatedAt, &info.LastActiveAt, &info.Archived, &info.TotalInputTokens, &info.TotalOutputTokens, &info.Model, &info.Family, &info.PlanReviewPhase, &info.PlanReviewPath, &info.PlanReviewContext); err != nil {
			return nil, fmt.Errorf("failed to scan session: %w", err)
		}
		sessions = append(sessions, info)
	}
	if err := rows.Err(); err != nil {
		return nil, fmt.Errorf("error iterating sessions: %w", err)
	}

	if sessions == nil {
		sessions = []SessionInfo{}
	}
	return sessions, nil
}

// DeleteSession deletes a session and all its messages (cascade).
func (s *SQLiteSessionStore) DeleteSession(ctx context.Context, id string) error {
	_, err := s.db.ExecContext(ctx, `DELETE FROM sessions WHERE id = ?`, id)
	if err != nil {
		return fmt.Errorf("failed to delete session: %w", err)
	}
	return nil
}

// ArchiveSession sets the archived flag on a session.
func (s *SQLiteSessionStore) ArchiveSession(ctx context.Context, id string, archived bool) error {
	_, err := s.db.ExecContext(ctx, `UPDATE sessions SET archived = ? WHERE id = ?`, archived, id)
	if err != nil {
		return fmt.Errorf("failed to archive session: %w", err)
	}
	return nil
}

// RenameSession updates a session's name.
func (s *SQLiteSessionStore) RenameSession(ctx context.Context, id, name string) error {
	_, err := s.db.ExecContext(ctx, `UPDATE sessions SET name = ? WHERE id = ?`, name, id)
	if err != nil {
		return fmt.Errorf("failed to rename session: %w", err)
	}
	return nil
}

// UpdateSessionTokens updates the accumulated token counts and model info for a session.
func (s *SQLiteSessionStore) UpdateSessionTokens(ctx context.Context, id string, inputTokens, outputTokens int, model, family string) error {
	_, err := s.db.ExecContext(ctx, `
		UPDATE sessions SET total_input_tokens = ?, total_output_tokens = ?, model = ?, family = ? WHERE id = ?`,
		inputTokens, outputTokens, model, family, id,
	)
	if err != nil {
		return fmt.Errorf("failed to update session tokens: %w", err)
	}
	return nil
}

// UpdateSessionActivity updates the last_active_at timestamp for a session.
func (s *SQLiteSessionStore) UpdateSessionActivity(ctx context.Context, id string) error {
	now := time.Now().Format(time.RFC3339)
	_, err := s.db.ExecContext(ctx, `
		UPDATE sessions SET last_active_at = ? WHERE id = ?`,
		now, id,
	)
	if err != nil {
		return fmt.Errorf("failed to update session activity: %w", err)
	}
	return nil
}

// UpdateSessionPlanReview persists plan review state for restart survival.
// Also clears plan_review_context so the column stays consistent with phase/path.
func (s *SQLiteSessionStore) UpdateSessionPlanReview(ctx context.Context, id, phase, planPath string) error {
	_, err := s.db.ExecContext(ctx, `
		UPDATE sessions SET plan_review_phase = ?, plan_review_path = ?, plan_review_context = '' WHERE id = ?`,
		phase, planPath, id,
	)
	if err != nil {
		return fmt.Errorf("failed to update session plan review state: %w", err)
	}
	return nil
}

// UpdateSessionPlanReviewContext persists plan review state including the
// restart-survival context (original message, mode, skills as JSON).
func (s *SQLiteSessionStore) UpdateSessionPlanReviewContext(ctx context.Context, id, phase, planPath, contextJSON string) error {
	_, err := s.db.ExecContext(ctx, `
		UPDATE sessions SET plan_review_phase = ?, plan_review_path = ?, plan_review_context = ? WHERE id = ?`,
		phase, planPath, contextJSON, id,
	)
	if err != nil {
		return fmt.Errorf("failed to update session plan review context: %w", err)
	}
	return nil
}

// GetSessionsInPlanReview returns all sessions currently in a plan review phase.
func (s *SQLiteSessionStore) GetSessionsInPlanReview(ctx context.Context, projectID string) ([]SessionInfo, error) {
	rows, err := s.db.QueryContext(ctx, `
		SELECT id, project_id, name, created_at, COALESCE(last_active_at, created_at), archived, COALESCE(total_input_tokens, 0), COALESCE(total_output_tokens, 0), COALESCE(model, ''), COALESCE(family, ''), COALESCE(plan_review_phase, ''), COALESCE(plan_review_path, ''), COALESCE(plan_review_context, '') FROM sessions
		WHERE plan_review_phase != '' AND project_id = ?
		ORDER BY COALESCE(last_active_at, created_at) DESC`, projectID)
	if err != nil {
		return nil, fmt.Errorf("failed to get sessions in plan review: %w", err)
	}
	defer func() {
		if err := rows.Close(); err != nil {
			s.log().Warn("failed to close database rows", "error", err)
		}
	}()

	var sessions []SessionInfo
	for rows.Next() {
		var info SessionInfo
		if err := rows.Scan(&info.ID, &info.ProjectID, &info.Name, &info.CreatedAt, &info.LastActiveAt, &info.Archived, &info.TotalInputTokens, &info.TotalOutputTokens, &info.Model, &info.Family, &info.PlanReviewPhase, &info.PlanReviewPath, &info.PlanReviewContext); err != nil {
			return nil, fmt.Errorf("failed to scan session: %w", err)
		}
		sessions = append(sessions, info)
	}
	if err := rows.Err(); err != nil {
		return nil, fmt.Errorf("error iterating sessions: %w", err)
	}

	if sessions == nil {
		sessions = []SessionInfo{}
	}
	return sessions, nil
}

// HasResolvedPlanReviewMessage checks whether the session has a plan_review
// message marked as resolved (accepted or rejected) in its chat history.
// This is used during plan review recovery to skip re-emitting plan_review_ready
// for sessions whose plan was already handled.
func (s *SQLiteSessionStore) HasResolvedPlanReviewMessage(ctx context.Context, sessionID string) (bool, error) {
	rows, err := s.db.QueryContext(ctx, `
		SELECT metadata FROM session_messages
		WHERE session_id = ? AND role = 'plan_review'
		  AND json_extract(metadata, '$.resolved') = 1
		LIMIT 1`, sessionID)
	if err != nil {
		return false, fmt.Errorf("failed to check resolved plan review: %w", err)
	}
	defer func() {
		if err := rows.Close(); err != nil {
			s.log().Warn("failed to close database rows", "error", err)
		}
	}()
	return rows.Next(), rows.Err()
}

// SaveMessage saves a chat message.
func (s *SQLiteSessionStore) SaveMessage(ctx context.Context, msg ChatMessage) error {
	var reasoningVal, toolCallsVal any
	if msg.ReasoningContent != nil {
		reasoningVal = *msg.ReasoningContent
	}
	if msg.ToolCalls != nil && len(*msg.ToolCalls) > 0 {
		toolCallsVal = string(*msg.ToolCalls)
	}
	_, err := s.db.ExecContext(ctx, `
		INSERT INTO session_messages (session_id, role, content, reasoning_content, tool_calls, metadata, created_at)
		VALUES (?, ?, ?, ?, ?, ?, ?)`,
		msg.SessionID, msg.Role, msg.Content, reasoningVal, toolCallsVal, string(msg.Metadata), msg.CreatedAt,
	)
	if err != nil {
		return fmt.Errorf("failed to save message: %w", err)
	}
	return nil
}

// LoadMessages loads all messages for a session ordered by creation time.
func (s *SQLiteSessionStore) LoadMessages(ctx context.Context, sessionID string) ([]ChatMessage, error) {
	rows, err := s.db.QueryContext(ctx, `
		SELECT id, session_id, role, content, reasoning_content, tool_calls, metadata, created_at
		FROM session_messages
		WHERE session_id = ?
		ORDER BY created_at ASC`,
		sessionID,
	)
	if err != nil {
		return nil, fmt.Errorf("failed to load messages: %w", err)
	}
	defer func() {
		if err := rows.Close(); err != nil {
			s.log().Warn("failed to close database rows", "error", err)
		}
	}()

	var messages []ChatMessage
	for rows.Next() {
		var msg ChatMessage
		var metadataStr string
		var reasoningStr, toolCallsStr sql.NullString
		if err := rows.Scan(&msg.ID, &msg.SessionID, &msg.Role, &msg.Content, &reasoningStr, &toolCallsStr, &metadataStr, &msg.CreatedAt); err != nil {
			return nil, fmt.Errorf("failed to scan message: %w", err)
		}
		if reasoningStr.Valid && reasoningStr.String != "" {
			v := reasoningStr.String
			msg.ReasoningContent = &v
		}
		if toolCallsStr.Valid && toolCallsStr.String != "" {
			raw := json.RawMessage(toolCallsStr.String)
			msg.ToolCalls = &raw
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
func (s *SQLiteSessionStore) DeleteMessages(ctx context.Context, sessionID string) error {
	_, err := s.db.ExecContext(ctx, `DELETE FROM session_messages WHERE session_id = ?`, sessionID)
	if err != nil {
		return fmt.Errorf("failed to delete messages: %w", err)
	}
	return nil
}

// SaveTerminalCommand saves a terminal command to the history.
func (s *SQLiteSessionStore) SaveTerminalCommand(ctx context.Context, sessionID, command string) error {
	now := time.Now().Format(time.RFC3339)
	_, err := s.db.ExecContext(ctx, `
		INSERT INTO terminal_commands (session_id, command, created_at)
		VALUES (?, ?, ?)`,
		sessionID, command, now,
	)
	if err != nil {
		return fmt.Errorf("failed to save terminal command: %w", err)
	}
	return nil
}

// LoadTerminalCommands loads the most recent terminal commands for a session.
func (s *SQLiteSessionStore) LoadTerminalCommands(ctx context.Context, sessionID string, limit int) ([]TerminalCommand, error) {
	if limit <= 0 {
		limit = 100
	}
	rows, err := s.db.QueryContext(ctx, `
		SELECT id, session_id, command, created_at
		FROM terminal_commands
		WHERE session_id = ?
		ORDER BY created_at DESC
		LIMIT ?`,
		sessionID, limit,
	)
	if err != nil {
		return nil, fmt.Errorf("failed to load terminal commands: %w", err)
	}
	defer func() {
		if err := rows.Close(); err != nil {
			s.log().Warn("failed to close database rows", "error", err)
		}
	}()

	var commands []TerminalCommand
	for rows.Next() {
		var cmd TerminalCommand
		if err := rows.Scan(&cmd.ID, &cmd.SessionID, &cmd.Command, &cmd.CreatedAt); err != nil {
			return nil, fmt.Errorf("failed to scan terminal command: %w", err)
		}
		commands = append(commands, cmd)
	}

	if err := rows.Err(); err != nil {
		return nil, fmt.Errorf("error iterating terminal commands: %w", err)
	}

	if commands == nil {
		commands = []TerminalCommand{}
	}

	return commands, nil
}

// Close is a no-op — the DB lifecycle is managed externally.
func (s *SQLiteSessionStore) Close() error {
	return nil
}

// ---------------------------------------------------------------------------
// Task persistence types
// ---------------------------------------------------------------------------

// TaskRecord represents a persisted task (one per Orchestrator.Handle call).
type TaskRecord struct {
	ID              string          `json:"id"`
	SessionID       string          `json:"session_id"`
	OriginalRequest string          `json:"original_request"`
	RoutingDecision json.RawMessage `json:"routing_decision"`
	Plan            json.RawMessage `json:"plan"`
	Reflections     json.RawMessage `json:"reflections"`
	FinalOutput     string          `json:"final_output"`
	AttemptCount    int             `json:"attempt_count"`
	Status          string          `json:"status"` // "in_progress", "completed", "failed", "cancelled"
	CreatedAt       time.Time       `json:"created_at"`
	CompletedAt     *time.Time      `json:"completed_at,omitempty"`
}

// TaskStepRecord represents a persisted step result.
type TaskStepRecord struct {
	StepID     string          `json:"step_id"`
	TaskID     string          `json:"task_id"`
	Summary    string          `json:"summary"`
	FullOutput string          `json:"full_output"`
	ErrorText  string          `json:"error_text"`
	Steps      json.RawMessage `json:"steps"` // JSON array of executor steps
	CreatedAt  time.Time       `json:"created_at"`
}

// ---------------------------------------------------------------------------
// TaskStore interface
// ---------------------------------------------------------------------------

// TaskStore provides persistent storage for orchestration tasks and step results.
type TaskStore interface {
	SaveTask(ctx context.Context, task TaskRecord) error
	UpdateTaskPlan(ctx context.Context, taskID string, plan json.RawMessage) error
	UpdateTaskRouting(ctx context.Context, taskID string, routing json.RawMessage) error
	SaveTaskStep(ctx context.Context, taskID string, step TaskStepRecord) error
	AddTaskReflection(ctx context.Context, taskID string, reflectionJSON json.RawMessage) error
	CompleteTask(ctx context.Context, taskID, finalOutput string, attemptCount int) error
	FailTask(ctx context.Context, taskID string) error
	CancelTask(ctx context.Context, taskID string) error
	LoadTask(ctx context.Context, taskID string) (*TaskRecord, error)
	LoadTaskSteps(ctx context.Context, taskID string) ([]TaskStepRecord, error)
	SaveFacts(ctx context.Context, taskID string, factsJSON json.RawMessage) error
	LoadFacts(ctx context.Context, taskID string) (json.RawMessage, error)
	GetUnfinishedTask(ctx context.Context, sessionID string) (*TaskRecord, error)
	GetLatestTaskID(ctx context.Context, sessionID string) (string, error)
	ReactivateTask(ctx context.Context, taskID string) error
}

// compile-time checks
var _ SessionStore = (*SQLiteSessionStore)(nil)
var _ PlanReviewStore = (*SQLiteSessionStore)(nil)
var _ TaskStore = (*SQLiteSessionStore)(nil)

// ---------------------------------------------------------------------------
// TaskStore implementation on SQLiteSessionStore
// ---------------------------------------------------------------------------

// SaveTask inserts a new task record.
func (s *SQLiteSessionStore) SaveTask(ctx context.Context, task TaskRecord) error {
	_, err := s.db.ExecContext(ctx, `
		INSERT INTO tasks (id, session_id, original_request, routing_decision, plan, reflections, final_output, attempt_count, status, created_at, completed_at)
		VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?)`,
		task.ID, task.SessionID, task.OriginalRequest,
		string(task.RoutingDecision), string(task.Plan),
		string(task.Reflections),
		task.FinalOutput, task.AttemptCount, task.Status,
		task.CreatedAt, task.CompletedAt,
	)
	if err != nil {
		return fmt.Errorf("failed to save task: %w", err)
	}
	return nil
}

// UpdateTaskPlan updates the plan JSON for a task.
func (s *SQLiteSessionStore) UpdateTaskPlan(ctx context.Context, taskID string, plan json.RawMessage) error {
	_, err := s.db.ExecContext(ctx, `UPDATE tasks SET plan = ? WHERE id = ?`, string(plan), taskID)
	if err != nil {
		return fmt.Errorf("failed to update task plan: %w", err)
	}
	return nil
}

// UpdateTaskRouting updates the routing decision JSON for a task.
func (s *SQLiteSessionStore) UpdateTaskRouting(ctx context.Context, taskID string, routing json.RawMessage) error {
	_, err := s.db.ExecContext(ctx, `UPDATE tasks SET routing_decision = ? WHERE id = ?`, string(routing), taskID)
	if err != nil {
		return fmt.Errorf("failed to update task routing: %w", err)
	}
	return nil
}

// SaveTaskStep inserts or replaces a task step record.
func (s *SQLiteSessionStore) SaveTaskStep(ctx context.Context, taskID string, step TaskStepRecord) error {
	_, err := s.db.ExecContext(ctx, `
		INSERT OR REPLACE INTO task_steps (step_id, task_id, summary, full_output, error_text, steps, created_at)
		VALUES (?, ?, ?, ?, ?, ?, ?)`,
		step.StepID, taskID, step.Summary, step.FullOutput, step.ErrorText,
		string(step.Steps), step.CreatedAt,
	)
	if err != nil {
		return fmt.Errorf("failed to save task step: %w", err)
	}
	return nil
}

// AddTaskReflection appends a reflection JSON object to the task's reflections array.
func (s *SQLiteSessionStore) AddTaskReflection(ctx context.Context, taskID string, reflectionJSON json.RawMessage) error {
	// BEGIN IMMEDIATE prevents SQLITE_BUSY from deferred->write upgrade in WAL mode.
	_, err := s.db.ExecContext(ctx, "BEGIN IMMEDIATE")
	if err != nil {
		return fmt.Errorf("failed to begin transaction: %w", err)
	}
	//nolint:errcheck // Rollback on error is best-effort.
	defer func() {
		if err != nil {
			s.db.ExecContext(context.Background(), "ROLLBACK")
		}
	}()

	// Read current reflections, append, write back.
	var current string
	err = s.db.QueryRowContext(ctx, `SELECT reflections FROM tasks WHERE id = ?`, taskID).Scan(&current)
	if err != nil {
		return fmt.Errorf("failed to read task reflections: %w", err)
	}

	var arr []json.RawMessage //nolint:prealloc // dynamic append from JSON unmarshal
	if current != "" && current != "[]" {
		if err = json.Unmarshal([]byte(current), &arr); err != nil {
			return fmt.Errorf("failed to unmarshal task reflections: %w", err)
		}
	}
	arr = append(arr, reflectionJSON)

	updated, mErr := json.Marshal(arr)
	if mErr != nil {
		err = fmt.Errorf("failed to marshal task reflections: %w", mErr)
		return err
	}

	_, err = s.db.ExecContext(ctx, `UPDATE tasks SET reflections = ? WHERE id = ?`, string(updated), taskID)
	if err != nil {
		return fmt.Errorf("failed to update task reflections: %w", err)
	}

	_, err = s.db.ExecContext(ctx, "COMMIT")
	if err != nil {
		return fmt.Errorf("failed to commit transaction: %w", err)
	}
	return nil
}

// CompleteTask marks a task as completed with final output.
func (s *SQLiteSessionStore) CompleteTask(ctx context.Context, taskID, finalOutput string, attemptCount int) error {
	now := time.Now()
	_, err := s.db.ExecContext(ctx, `
		UPDATE tasks SET status = 'completed', final_output = ?, attempt_count = ?, completed_at = ? WHERE id = ?`,
		finalOutput, attemptCount, now, taskID,
	)
	if err != nil {
		return fmt.Errorf("failed to complete task: %w", err)
	}
	return nil
}

// FailTask marks a task as failed.
func (s *SQLiteSessionStore) FailTask(ctx context.Context, taskID string) error {
	now := time.Now()
	_, err := s.db.ExecContext(ctx, `
		UPDATE tasks SET status = 'failed', completed_at = ? WHERE id = ?`,
		now, taskID,
	)
	if err != nil {
		return fmt.Errorf("failed to fail task: %w", err)
	}
	return nil
}

// CancelTask marks a task as cancelled.
func (s *SQLiteSessionStore) CancelTask(ctx context.Context, taskID string) error {
	now := time.Now()
	_, err := s.db.ExecContext(ctx, `
		UPDATE tasks SET status = 'cancelled', completed_at = ? WHERE id = ?`,
		now, taskID,
	)
	if err != nil {
		return fmt.Errorf("failed to cancel task: %w", err)
	}
	return nil
}

// ReactivateTask reactivates a completed task back to in_progress.
func (s *SQLiteSessionStore) ReactivateTask(ctx context.Context, taskID string) error {
	_, err := s.db.ExecContext(ctx,
		`UPDATE tasks SET status = 'in_progress', completed_at = NULL WHERE id = ?`,
		taskID,
	)
	if err != nil {
		return fmt.Errorf("failed to reactivate task: %w", err)
	}
	return nil
}

// LoadTask loads a task by ID. Returns nil, nil if not found.
func (s *SQLiteSessionStore) LoadTask(ctx context.Context, taskID string) (*TaskRecord, error) {
	var task TaskRecord
	var routingDec, plan, reflections string
	var completedAt sql.NullTime

	err := s.db.QueryRowContext(ctx, `
		SELECT id, session_id, original_request, routing_decision, plan, reflections, final_output, attempt_count, status, created_at, completed_at
		FROM tasks WHERE id = ?`,
		taskID,
	).Scan(&task.ID, &task.SessionID, &task.OriginalRequest,
		&routingDec, &plan, &reflections,
		&task.FinalOutput, &task.AttemptCount, &task.Status,
		&task.CreatedAt, &completedAt,
	)

	if errors.Is(err, sql.ErrNoRows) {
		return nil, nil
	}
	if err != nil {
		return nil, fmt.Errorf("failed to load task: %w", err)
	}

	task.RoutingDecision = json.RawMessage(routingDec)
	task.Plan = json.RawMessage(plan)
	task.Reflections = json.RawMessage(reflections)
	if completedAt.Valid {
		task.CompletedAt = &completedAt.Time
	}

	return &task, nil
}

// LoadTaskSteps loads all step records for a task ordered by creation time.
func (s *SQLiteSessionStore) LoadTaskSteps(ctx context.Context, taskID string) ([]TaskStepRecord, error) {
	rows, err := s.db.QueryContext(ctx, `
		SELECT step_id, task_id, summary, full_output, error_text, steps, created_at
		FROM task_steps
		WHERE task_id = ?
		ORDER BY created_at ASC`,
		taskID,
	)
	if err != nil {
		return nil, fmt.Errorf("failed to load task steps: %w", err)
	}
	defer func() {
		if err := rows.Close(); err != nil {
			s.log().Warn("failed to close database rows", "error", err)
		}
	}()

	var steps []TaskStepRecord
	for rows.Next() {
		var step TaskStepRecord
		var stepsJSON string
		if err := rows.Scan(&step.StepID, &step.TaskID, &step.Summary, &step.FullOutput, &step.ErrorText, &stepsJSON, &step.CreatedAt); err != nil {
			return nil, fmt.Errorf("failed to scan task step: %w", err)
		}
		if stepsJSON != "" {
			step.Steps = json.RawMessage(stepsJSON)
		} else {
			step.Steps = json.RawMessage("[]")
		}
		steps = append(steps, step)
	}

	if err := rows.Err(); err != nil {
		return nil, fmt.Errorf("error iterating task steps: %w", err)
	}

	if steps == nil {
		steps = []TaskStepRecord{}
	}

	return steps, nil
}

// GetUnfinishedTask returns the most recent unfinished (in-progress or failed) task for a session, or nil if none.
func (s *SQLiteSessionStore) GetUnfinishedTask(ctx context.Context, sessionID string) (*TaskRecord, error) {
	var task TaskRecord
	var routingDec, plan, reflections string
	var completedAt sql.NullTime

	err := s.db.QueryRowContext(ctx, `
		SELECT id, session_id, original_request, routing_decision, plan, reflections, final_output, attempt_count, status, created_at, completed_at
		FROM tasks
		WHERE session_id = ? AND status IN ('in_progress', 'failed')
		ORDER BY created_at DESC LIMIT 1`,
		sessionID,
	).Scan(&task.ID, &task.SessionID, &task.OriginalRequest,
		&routingDec, &plan, &reflections,
		&task.FinalOutput, &task.AttemptCount, &task.Status,
		&task.CreatedAt, &completedAt,
	)

	if err == sql.ErrNoRows {
		return nil, nil
	}
	if err != nil {
		return nil, fmt.Errorf("failed to get unfinished task: %w", err)
	}

	task.RoutingDecision = json.RawMessage(routingDec)
	task.Plan = json.RawMessage(plan)
	task.Reflections = json.RawMessage(reflections)
	if completedAt.Valid {
		task.CompletedAt = &completedAt.Time
	}

	return &task, nil
}

// GetLatestTaskID returns the ID of the most recent task for a session, regardless of status.
// Returns "", nil if no tasks exist.
func (s *SQLiteSessionStore) GetLatestTaskID(ctx context.Context, sessionID string) (string, error) {
	var taskID string
	err := s.db.QueryRowContext(ctx, `
		SELECT id FROM tasks WHERE session_id = ? ORDER BY created_at DESC LIMIT 1`,
		sessionID,
	).Scan(&taskID)

	if errors.Is(err, sql.ErrNoRows) {
		return "", nil
	}
	if err != nil {
		return "", fmt.Errorf("failed to get latest task ID: %w", err)
	}
	return taskID, nil
}

// SaveFacts inserts or replaces the facts JSON blob for a task.
func (s *SQLiteSessionStore) SaveFacts(ctx context.Context, taskID string, factsJSON json.RawMessage) error {
	_, err := s.db.ExecContext(ctx, `
		INSERT OR REPLACE INTO task_facts (task_id, facts, updated_at)
		VALUES (?, ?, ?)`,
		taskID, string(factsJSON), time.Now(),
	)
	if err != nil {
		return fmt.Errorf("failed to save facts: %w", err)
	}
	return nil
}

// LoadFacts loads the facts JSON blob for a task. Returns nil if not found.
func (s *SQLiteSessionStore) LoadFacts(ctx context.Context, taskID string) (json.RawMessage, error) {
	var factsStr string
	err := s.db.QueryRowContext(ctx, `
		SELECT facts FROM task_facts WHERE task_id = ?`, taskID,
	).Scan(&factsStr)
	if errors.Is(err, sql.ErrNoRows) {
		return nil, nil
	}
	if err != nil {
		return nil, fmt.Errorf("failed to load facts: %w", err)
	}
	if factsStr == "" || factsStr == "[]" {
		return nil, nil
	}
	return json.RawMessage(factsStr), nil
}
