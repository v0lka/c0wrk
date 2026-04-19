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
	ListSessionsByProject(projectID string) ([]SessionInfo, error)
	DeleteSession(id string) error
	ArchiveSession(id string, archived bool) error
	RenameSession(id, name string) error

	// Token tracking
	UpdateSessionTokens(id string, inputTokens, outputTokens int, model, family string) error

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

	if err := store.runMigrations(); err != nil {
		return nil, fmt.Errorf("failed to run migrations: %w", err)
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
		family TEXT DEFAULT ''
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

	CREATE TABLE IF NOT EXISTS task_step_file_changes (
		task_id TEXT NOT NULL REFERENCES tasks(id) ON DELETE CASCADE,
		step_id TEXT NOT NULL,
		file_changes TEXT DEFAULT '[]',
		created_at TIMESTAMP NOT NULL,
		PRIMARY KEY (task_id, step_id)
	);

	CREATE TABLE IF NOT EXISTS task_facts (
		task_id TEXT PRIMARY KEY REFERENCES tasks(id) ON DELETE CASCADE,
		facts TEXT DEFAULT '[]',
		updated_at TIMESTAMP NOT NULL
	);
	`
	_, err := s.db.ExecContext(context.Background(), schema)
	return err
}

// runMigrations applies incremental schema changes for existing databases.
func (s *SQLiteSessionStore) runMigrations() error {
	migrations := []string{
		"ALTER TABLE sessions ADD COLUMN model TEXT DEFAULT ''",
		"ALTER TABLE sessions ADD COLUMN family TEXT DEFAULT ''",
	}
	for _, m := range migrations {
		if _, err := s.db.ExecContext(context.Background(), m); err != nil {
			// Ignore "duplicate column" errors — the column already exists.
			if isDuplicateColumnError(err) {
				continue
			}
			return fmt.Errorf("migration failed: %w", err)
		}
	}
	return nil
}

// isDuplicateColumnError returns true if the error indicates a column already exists.
func isDuplicateColumnError(err error) bool {
	return err != nil && strings.Contains(err.Error(), "duplicate column")
}

// SaveSession saves or updates a session.
func (s *SQLiteSessionStore) SaveSession(info SessionInfo) error {
	// Use created_at as fallback for last_active_at if not set
	lastActiveAt := info.LastActiveAt
	if lastActiveAt == "" {
		lastActiveAt = info.CreatedAt
	}
	_, err := s.db.ExecContext(context.Background(), `
		INSERT INTO sessions (id, project_id, name, created_at, last_active_at, archived, total_input_tokens, total_output_tokens, model, family)
		VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?, ?)
		ON CONFLICT(id) DO UPDATE SET
			name = excluded.name,
			last_active_at = excluded.last_active_at,
			archived = excluded.archived,
			total_input_tokens = excluded.total_input_tokens,
			total_output_tokens = excluded.total_output_tokens,
			model = excluded.model,
			family = excluded.family`,
		info.ID, info.ProjectID, info.Name, info.CreatedAt, lastActiveAt, info.Archived, info.TotalInputTokens, info.TotalOutputTokens, info.Model, info.Family,
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
		SELECT id, project_id, name, created_at, COALESCE(last_active_at, created_at), archived, COALESCE(total_input_tokens, 0), COALESCE(total_output_tokens, 0), COALESCE(model, ''), COALESCE(family, '') FROM sessions WHERE id = ?`,
		id,
	).Scan(&info.ID, &info.ProjectID, &info.Name, &info.CreatedAt, &info.LastActiveAt, &info.Archived, &info.TotalInputTokens, &info.TotalOutputTokens, &info.Model, &info.Family)

	if errors.Is(err, sql.ErrNoRows) {
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
		SELECT id, project_id, name, created_at,
		       COALESCE(last_active_at, created_at),
		       archived,
		       COALESCE(total_input_tokens, 0),
		       COALESCE(total_output_tokens, 0),
		       COALESCE(model, ''),
		       COALESCE(family, '')
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
		if err := rows.Scan(&info.ID, &info.ProjectID, &info.Name, &info.CreatedAt, &info.LastActiveAt, &info.Archived, &info.TotalInputTokens, &info.TotalOutputTokens, &info.Model, &info.Family); err != nil {
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
func (s *SQLiteSessionStore) ListSessionsByProject(projectID string) ([]SessionInfo, error) {
	rows, err := s.db.QueryContext(context.Background(), `
		SELECT id, project_id, name, created_at, COALESCE(last_active_at, created_at), archived, COALESCE(total_input_tokens, 0), COALESCE(total_output_tokens, 0), COALESCE(model, ''), COALESCE(family, '') FROM sessions
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
		if err := rows.Scan(&info.ID, &info.ProjectID, &info.Name, &info.CreatedAt, &info.LastActiveAt, &info.Archived, &info.TotalInputTokens, &info.TotalOutputTokens, &info.Model, &info.Family); err != nil {
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

// UpdateSessionTokens updates the accumulated token counts and model info for a session.
func (s *SQLiteSessionStore) UpdateSessionTokens(id string, inputTokens, outputTokens int, model, family string) error {
	_, err := s.db.ExecContext(context.Background(), `
		UPDATE sessions SET total_input_tokens = ?, total_output_tokens = ?, model = ?, family = ? WHERE id = ?`,
		inputTokens, outputTokens, model, family, id,
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
	defer func() {
		if err := rows.Close(); err != nil {
			s.log().Warn("failed to close database rows", "error", err)
		}
	}()

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
	Status          string          `json:"status"` // "in_progress", "completed", "failed"
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
	SaveTask(task TaskRecord) error
	UpdateTaskPlan(taskID string, plan json.RawMessage) error
	UpdateTaskRouting(taskID string, routing json.RawMessage) error
	SaveTaskStep(taskID string, step TaskStepRecord) error
	SaveStepFileChanges(taskID, stepID string, fileChangesJSON json.RawMessage) error
	AddTaskReflection(taskID string, reflectionJSON json.RawMessage) error
	CompleteTask(taskID, finalOutput string, attemptCount int) error
	FailTask(taskID string) error
	LoadTask(taskID string) (*TaskRecord, error)
	LoadTaskSteps(taskID string) ([]TaskStepRecord, error)
	LoadStepFileChanges(taskID string) (map[string]json.RawMessage, error)
	SaveFacts(taskID string, factsJSON json.RawMessage) error
	LoadFacts(taskID string) (json.RawMessage, error)
	GetUnfinishedTask(sessionID string) (*TaskRecord, error)
	ReactivateTask(taskID string) error
}

// compile-time check
var _ TaskStore = (*SQLiteSessionStore)(nil)

// ---------------------------------------------------------------------------
// TaskStore implementation on SQLiteSessionStore
// ---------------------------------------------------------------------------

// SaveTask inserts a new task record.
func (s *SQLiteSessionStore) SaveTask(task TaskRecord) error {
	_, err := s.db.ExecContext(context.Background(), `
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
func (s *SQLiteSessionStore) UpdateTaskPlan(taskID string, plan json.RawMessage) error {
	_, err := s.db.ExecContext(context.Background(), `UPDATE tasks SET plan = ? WHERE id = ?`, string(plan), taskID)
	if err != nil {
		return fmt.Errorf("failed to update task plan: %w", err)
	}
	return nil
}

// UpdateTaskRouting updates the routing decision JSON for a task.
func (s *SQLiteSessionStore) UpdateTaskRouting(taskID string, routing json.RawMessage) error {
	_, err := s.db.ExecContext(context.Background(), `UPDATE tasks SET routing_decision = ? WHERE id = ?`, string(routing), taskID)
	if err != nil {
		return fmt.Errorf("failed to update task routing: %w", err)
	}
	return nil
}

// SaveTaskStep inserts or replaces a task step record.
func (s *SQLiteSessionStore) SaveTaskStep(taskID string, step TaskStepRecord) error {
	_, err := s.db.ExecContext(context.Background(), `
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
func (s *SQLiteSessionStore) AddTaskReflection(taskID string, reflectionJSON json.RawMessage) error {
	// Read current reflections, append, write back.
	var current string
	err := s.db.QueryRowContext(context.Background(), `SELECT reflections FROM tasks WHERE id = ?`, taskID).Scan(&current)
	if err != nil {
		return fmt.Errorf("failed to read task reflections: %w", err)
	}

	var arr []json.RawMessage //nolint:prealloc // dynamic append from JSON unmarshal
	if current != "" && current != "[]" {
		if err := json.Unmarshal([]byte(current), &arr); err != nil {
			return fmt.Errorf("failed to unmarshal task reflections: %w", err)
		}
	}
	arr = append(arr, reflectionJSON)

	updated, err := json.Marshal(arr)
	if err != nil {
		return fmt.Errorf("failed to marshal task reflections: %w", err)
	}

	_, err = s.db.ExecContext(context.Background(), `UPDATE tasks SET reflections = ? WHERE id = ?`, string(updated), taskID)
	if err != nil {
		return fmt.Errorf("failed to update task reflections: %w", err)
	}
	return nil
}

// CompleteTask marks a task as completed with final output.
func (s *SQLiteSessionStore) CompleteTask(taskID, finalOutput string, attemptCount int) error {
	now := time.Now()
	_, err := s.db.ExecContext(context.Background(), `
		UPDATE tasks SET status = 'completed', final_output = ?, attempt_count = ?, completed_at = ? WHERE id = ?`,
		finalOutput, attemptCount, now, taskID,
	)
	if err != nil {
		return fmt.Errorf("failed to complete task: %w", err)
	}
	return nil
}

// FailTask marks a task as failed.
func (s *SQLiteSessionStore) FailTask(taskID string) error {
	now := time.Now()
	_, err := s.db.ExecContext(context.Background(), `
		UPDATE tasks SET status = 'failed', completed_at = ? WHERE id = ?`,
		now, taskID,
	)
	if err != nil {
		return fmt.Errorf("failed to fail task: %w", err)
	}
	return nil
}

// ReactivateTask reactivates a completed task back to in_progress.
func (s *SQLiteSessionStore) ReactivateTask(taskID string) error {
	_, err := s.db.ExecContext(context.Background(),
		`UPDATE tasks SET status = 'in_progress', completed_at = NULL WHERE id = ?`,
		taskID,
	)
	if err != nil {
		return fmt.Errorf("failed to reactivate task: %w", err)
	}
	return nil
}

// LoadTask loads a task by ID. Returns nil, nil if not found.
func (s *SQLiteSessionStore) LoadTask(taskID string) (*TaskRecord, error) {
	var task TaskRecord
	var routingDec, plan, reflections string
	var completedAt sql.NullTime

	err := s.db.QueryRowContext(context.Background(), `
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
func (s *SQLiteSessionStore) LoadTaskSteps(taskID string) ([]TaskStepRecord, error) {
	rows, err := s.db.QueryContext(context.Background(), `
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

// SaveStepFileChanges inserts or replaces file changes JSON for a task step.
func (s *SQLiteSessionStore) SaveStepFileChanges(taskID, stepID string, fileChangesJSON json.RawMessage) error {
	_, err := s.db.ExecContext(context.Background(), `
		INSERT OR REPLACE INTO task_step_file_changes (task_id, step_id, file_changes, created_at)
		VALUES (?, ?, ?, ?)`,
		taskID, stepID, string(fileChangesJSON), time.Now(),
	)
	if err != nil {
		return fmt.Errorf("failed to save step file changes: %w", err)
	}
	return nil
}

// LoadStepFileChanges loads all file change records for a task, returning a map of stepID -> JSON.
func (s *SQLiteSessionStore) LoadStepFileChanges(taskID string) (map[string]json.RawMessage, error) {
	rows, err := s.db.QueryContext(context.Background(), `
		SELECT step_id, file_changes
		FROM task_step_file_changes
		WHERE task_id = ?
		ORDER BY created_at ASC`,
		taskID,
	)
	if err != nil {
		return nil, fmt.Errorf("failed to load step file changes: %w", err)
	}
	defer func() {
		if err := rows.Close(); err != nil {
			s.log().Warn("failed to close database rows", "error", err)
		}
	}()

	result := make(map[string]json.RawMessage)
	for rows.Next() {
		var stepID, changesJSON string
		if err := rows.Scan(&stepID, &changesJSON); err != nil {
			return nil, fmt.Errorf("failed to scan step file changes: %w", err)
		}
		if changesJSON != "" && changesJSON != "[]" {
			result[stepID] = json.RawMessage(changesJSON)
		}
	}

	if err := rows.Err(); err != nil {
		return nil, fmt.Errorf("error iterating step file changes: %w", err)
	}

	return result, nil
}

// GetUnfinishedTask returns the most recent unfinished (in-progress or failed) task for a session, or nil if none.
func (s *SQLiteSessionStore) GetUnfinishedTask(sessionID string) (*TaskRecord, error) {
	var task TaskRecord
	var routingDec, plan, reflections string
	var completedAt sql.NullTime

	err := s.db.QueryRowContext(context.Background(), `
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

// SaveFacts inserts or replaces the facts JSON blob for a task.
func (s *SQLiteSessionStore) SaveFacts(taskID string, factsJSON json.RawMessage) error {
	_, err := s.db.ExecContext(context.Background(), `
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
func (s *SQLiteSessionStore) LoadFacts(taskID string) (json.RawMessage, error) {
	var factsStr string
	err := s.db.QueryRowContext(context.Background(), `
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
