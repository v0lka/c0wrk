package memory

import (
	"context"
	"database/sql"
	"encoding/json"
	"fmt"
	"time"

	_ "modernc.org/sqlite"
)

// EpisodicEntry represents a general episodic memory entry from a task interaction.
type EpisodicEntry struct {
	SessionID      string
	UserMessage    string
	Summary        string
	Mode           string // "direct", "react", "plan_execute"
	ToolsUsed      []string
	Success        bool
	EvalPassCount  int
	EvalTotalCount int
	Timestamp      time.Time
}

// EpisodicMemory stores reflections from completed tasks in SQLite.
type EpisodicMemory struct {
	db *sql.DB
}

// NewEpisodicMemory opens SQLite at dbPath (use ":memory:" for testing).
// Auto-creates tables and indexes.
func NewEpisodicMemory(dbPath string) (*EpisodicMemory, error) {
	db, err := sql.Open("sqlite", dbPath)
	if err != nil {
		return nil, err
	}

	// Enable WAL mode for better performance
	if _, err := db.Exec("PRAGMA journal_mode=WAL"); err != nil {
		_ = db.Close()
		return nil, err
	}

	em := &EpisodicMemory{db: db}

	if err := em.createTables(); err != nil {
		_ = db.Close()
		return nil, err
	}

	return em, nil
}

// createTables creates the necessary tables and indexes.
func (em *EpisodicMemory) createTables() error {
	entriesSchema := `
	CREATE TABLE IF NOT EXISTS episodic_entries (
		id INTEGER PRIMARY KEY AUTOINCREMENT,
		session_id TEXT NOT NULL DEFAULT '',
		user_message TEXT NOT NULL,
		summary TEXT NOT NULL,
		mode TEXT DEFAULT '',
		tools_used TEXT DEFAULT '[]',
		success BOOLEAN DEFAULT 0,
		eval_pass_count INTEGER DEFAULT 0,
		eval_total_count INTEGER DEFAULT 0,
		created_at TIMESTAMP DEFAULT CURRENT_TIMESTAMP
	);
	CREATE INDEX IF NOT EXISTS idx_episodic_entries_session ON episodic_entries(session_id);
	CREATE INDEX IF NOT EXISTS idx_episodic_entries_created ON episodic_entries(created_at);
	`
	_, err := em.db.Exec(entriesSchema)
	return err
}

// StoreEntry saves a general episodic entry from a task interaction.
func (em *EpisodicMemory) StoreEntry(ctx context.Context, entry EpisodicEntry) error {
	toolsJSON, err := json.Marshal(entry.ToolsUsed)
	if err != nil {
		return err
	}
	ts := entry.Timestamp
	if ts.IsZero() {
		ts = time.Now()
	}
	_, err = em.db.ExecContext(ctx, `
		INSERT INTO episodic_entries 
		(session_id, user_message, summary, mode, tools_used, success, eval_pass_count, eval_total_count, created_at)
		VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?)`,
		entry.SessionID,
		entry.UserMessage,
		entry.Summary,
		entry.Mode,
		string(toolsJSON),
		entry.Success,
		entry.EvalPassCount,
		entry.EvalTotalCount,
		ts,
	)
	return err
}

// RetrieveEntries returns recent episodic entries for a session.
func (em *EpisodicMemory) RetrieveEntries(ctx context.Context, sessionID string, limit int) ([]EpisodicEntry, error) {
	rows, err := em.db.QueryContext(ctx, `
		SELECT session_id, user_message, summary, mode, tools_used, success, 
		       eval_pass_count, eval_total_count, created_at
		FROM episodic_entries
		WHERE session_id = ?
		ORDER BY created_at DESC
		LIMIT ?`, sessionID, limit)
	if err != nil {
		return nil, err
	}
	defer func() { _ = rows.Close() }()

	var results []EpisodicEntry
	for rows.Next() {
		var e EpisodicEntry
		var toolsJSON string
		var createdAt time.Time
		if err := rows.Scan(
			&e.SessionID, &e.UserMessage, &e.Summary, &e.Mode, &toolsJSON,
			&e.Success, &e.EvalPassCount, &e.EvalTotalCount, &createdAt,
		); err != nil {
			return nil, err
		}
		e.Timestamp = createdAt
		if toolsJSON != "" {
			_ = json.Unmarshal([]byte(toolsJSON), &e.ToolsUsed)
		}
		results = append(results, e)
	}
	if err := rows.Err(); err != nil {
		return nil, err
	}
	if results == nil {
		results = []EpisodicEntry{}
	}
	return results, nil
}

// Cleanup removes entries older than the given duration.
func (em *EpisodicMemory) Cleanup(ctx context.Context, olderThan time.Duration) error {
	cutoff := time.Now().Add(-olderThan)
	_, err := em.db.ExecContext(ctx, `
		DELETE FROM episodic_entries
		WHERE created_at < ?`,
		cutoff,
	)
	return err
}

// Count returns the total number of stored episodic entries.
func (em *EpisodicMemory) Count(ctx context.Context) (int, error) {
	var count int
	err := em.db.QueryRowContext(ctx, `
		SELECT COUNT(*) FROM episodic_entries
	`).Scan(&count)
	if err != nil {
		return 0, fmt.Errorf("counting episodic entries: %w", err)
	}
	return count, nil
}

// CountBySession returns the number of episodic entries for a specific session.
func (em *EpisodicMemory) CountBySession(ctx context.Context, sessionID string) (int, error) {
	var count int
	err := em.db.QueryRowContext(ctx, `
		SELECT COUNT(*) FROM episodic_entries WHERE session_id = ?
	`, sessionID).Scan(&count)
	if err != nil {
		return 0, fmt.Errorf("counting episodic entries for session: %w", err)
	}
	return count, nil
}

// Close closes the database connection.
func (em *EpisodicMemory) Close() error {
	return em.db.Close()
}
