package memory

import (
	"context"
	"database/sql"
	"encoding/json"
	"fmt"
	"strings"
	"time"

	_ "modernc.org/sqlite"
)

// StoredReflexion represents a reflexion stored in reflexion memory.
// This is a cross-session reflection storage for learning from past experiences.
type StoredReflexion struct {
	TaskDescription string
	Summary         string
	Hypotheses      []string
	SuggestedAction string
	Timestamp       time.Time
}

// ReflexionMemory stores reflections from completed tasks in SQLite.
// Unlike EpisodicMemory, this is cross-session and focuses on reflection storage.
type ReflexionMemory struct {
	db *sql.DB
}

// NewReflexionMemory opens SQLite at dbPath (use ":memory:" for testing).
// Auto-creates tables and indexes.
func NewReflexionMemory(dbPath string) (*ReflexionMemory, error) {
	db, err := sql.Open("sqlite", dbPath)
	if err != nil {
		return nil, err
	}

	// Enable WAL mode for better performance
	if _, err := db.Exec("PRAGMA journal_mode=WAL"); err != nil {
		_ = db.Close()
		return nil, err
	}

	rm := &ReflexionMemory{db: db}

	if err := rm.createTables(); err != nil {
		_ = db.Close()
		return nil, err
	}

	return rm, nil
}

// createTables creates the necessary tables and indexes.
func (rm *ReflexionMemory) createTables() error {
	schema := `
	CREATE TABLE IF NOT EXISTS reflexions (
		id INTEGER PRIMARY KEY AUTOINCREMENT,
		task_description TEXT NOT NULL,
		summary TEXT NOT NULL,
		hypotheses TEXT,
		suggested_action TEXT,
		created_at TIMESTAMP DEFAULT CURRENT_TIMESTAMP
	);
	CREATE INDEX IF NOT EXISTS idx_reflexions_created_at ON reflexions(created_at);
	`
	_, err := rm.db.Exec(schema)
	return err
}

// Store saves a reflection from a completed task.
func (rm *ReflexionMemory) Store(ctx context.Context, reflection StoredReflexion) error {
	hypothesesJSON, err := json.Marshal(reflection.Hypotheses)
	if err != nil {
		return err
	}

	ts := reflection.Timestamp
	if ts.IsZero() {
		ts = time.Now()
	}

	_, err = rm.db.ExecContext(ctx, `
		INSERT INTO reflexions 
		(task_description, summary, hypotheses, suggested_action, created_at)
		VALUES (?, ?, ?, ?, ?)`,
		reflection.TaskDescription,
		reflection.Summary,
		string(hypothesesJSON),
		reflection.SuggestedAction,
		ts,
	)
	return err
}

// Search finds past reflections relevant to the given query.
// Uses LIKE-based keyword matching on task_description field.
// Returns up to 'limit' most recent matching reflections.
// This is cross-session - it does not filter by session_id.
func (rm *ReflexionMemory) Search(ctx context.Context, query string, limit int) ([]StoredReflexion, error) {
	// Split query into keywords
	keywords := extractKeywords(query)

	if len(keywords) == 0 {
		return []StoredReflexion{}, nil
	}

	// Build query with LIKE conditions for each keyword
	var conditions []string
	var args []interface{}
	for _, kw := range keywords {
		conditions = append(conditions, "task_description LIKE ?")
		args = append(args, "%"+kw+"%")
	}
	args = append(args, limit)

	queryStr := `
		SELECT task_description, summary, hypotheses, suggested_action, created_at
		FROM reflexions
		WHERE ` + strings.Join(conditions, " OR ") + `
		ORDER BY created_at DESC
		LIMIT ?`

	rows, err := rm.db.QueryContext(ctx, queryStr, args...)
	if err != nil {
		return nil, err
	}
	defer func() { _ = rows.Close() }()

	var results []StoredReflexion
	for rows.Next() {
		var r StoredReflexion
		var hypothesesJSON string
		var createdAt time.Time

		if err := rows.Scan(
			&r.TaskDescription,
			&r.Summary,
			&hypothesesJSON,
			&r.SuggestedAction,
			&createdAt,
		); err != nil {
			return nil, err
		}

		r.Timestamp = createdAt

		// Parse hypotheses JSON
		if hypothesesJSON != "" {
			if err := json.Unmarshal([]byte(hypothesesJSON), &r.Hypotheses); err != nil {
				r.Hypotheses = nil // Fallback to empty if parse fails
			}
		}

		results = append(results, r)
	}

	if err := rows.Err(); err != nil {
		return nil, err
	}

	// Return empty slice instead of nil for consistency
	if results == nil {
		results = []StoredReflexion{}
	}

	return results, nil
}

// Count returns the total number of stored reflections.
func (rm *ReflexionMemory) Count(ctx context.Context) (int, error) {
	var count int
	err := rm.db.QueryRowContext(ctx, `SELECT COUNT(*) FROM reflexions`).Scan(&count)
	if err != nil {
		return 0, fmt.Errorf("counting reflexions: %w", err)
	}
	return count, nil
}

// Close closes the database connection.
func (rm *ReflexionMemory) Close() error {
	return rm.db.Close()
}

// extractKeywords splits a query into meaningful keywords.
func extractKeywords(query string) []string {
	// Common stop words to filter out
	stopWords := map[string]bool{
		"a": true, "an": true, "the": true, "is": true, "are": true,
		"was": true, "were": true, "be": true, "been": true, "being": true,
		"have": true, "has": true, "had": true, "do": true, "does": true,
		"did": true, "will": true, "would": true, "could": true, "should": true,
		"may": true, "might": true, "must": true, "can": true,
		"to": true, "of": true, "in": true, "for": true, "on": true,
		"with": true, "at": true, "by": true, "from": true, "as": true,
		"into": true, "through": true, "during": true, "before": true,
		"after": true, "above": true, "below": true, "between": true,
		"under": true, "again": true, "further": true, "then": true,
		"once": true, "and": true, "but": true, "or": true, "nor": true,
		"so": true, "yet": true, "both": true, "either": true, "neither": true,
		"not": true, "only": true, "own": true, "same": true, "than": true,
		"too": true, "very": true, "just": true, "also": true,
		"it": true, "its": true, "this": true, "that": true, "these": true,
		"those": true, "i": true, "me": true, "my": true, "we": true,
		"our": true, "you": true, "your": true, "he": true, "him": true,
		"his": true, "she": true, "her": true, "they": true, "them": true,
		"their": true, "what": true, "which": true, "who": true, "whom": true,
	}

	// Split on whitespace, punctuation, and underscores
	words := strings.FieldsFunc(query, func(r rune) bool {
		isLower := r >= 'a' && r <= 'z'
		isUpper := r >= 'A' && r <= 'Z'
		isDigit := r >= '0' && r <= '9'
		return !isLower && !isUpper && !isDigit
	})

	var keywords []string
	seen := make(map[string]bool)

	for _, word := range words {
		lower := strings.ToLower(word)
		// Filter: not a stop word, at least 2 chars, not already seen
		if !stopWords[lower] && len(lower) >= 2 && !seen[lower] {
			keywords = append(keywords, lower)
			seen[lower] = true
		}
	}

	return keywords
}
