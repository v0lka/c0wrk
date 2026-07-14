// Package review provides SQLite-backed persistence for the per-session code
// review buffer (a general comment plus hunk-scoped comments) and its lifecycle
// status. The store wraps the shared application *sql.DB and is initialized
// after the session store because review_comments/review_state carry foreign
// keys to the sessions table.
package review

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
	"log/slog"
	"time"

	"github.com/google/uuid"
)

// ReviewStatus is the lifecycle stage of a review.
type ReviewStatus string

const (
	// StatusActive means a review is in progress (the default for any session
	// that has not yet been submitted or approved).
	StatusActive ReviewStatus = "active"
	// StatusSubmitted means the review has been handed off for resolution.
	StatusSubmitted ReviewStatus = "submitted"
	// StatusApproved means the review has been accepted and is considered done.
	StatusApproved ReviewStatus = "approved"
)

// CommentKind distinguishes a session-wide general comment from a hunk-scoped one.
type CommentKind string

const (
	// KindGeneral is a single session-wide review comment.
	KindGeneral CommentKind = "general"
	// KindHunk is a comment scoped to a (file_path, hunk_id) pair.
	KindHunk CommentKind = "hunk"
)

// HunkComment is a review comment scoped to a specific file hunk.
type HunkComment struct {
	ID        string `json:"id"`
	SessionID string `json:"session_id"`
	FilePath  string `json:"file_path"`
	HunkID    string `json:"hunk_id"`
	Body      string `json:"body"`
	CreatedAt string `json:"created_at"` // RFC 3339 formatted timestamp
}

// Review is the aggregate view of a session's review buffer returned by
// GetReview. HunkComments is never nil (an empty slice means no hunk comments).
type Review struct {
	SessionID      string        `json:"session_id"`
	Status         ReviewStatus  `json:"status"`
	GeneralComment string        `json:"general_comment"`
	HunkComments   []HunkComment `json:"hunk_comments"`
	UpdatedAt      string        `json:"updated_at"` // RFC 3339 formatted timestamp of the last status change
}

// SQLiteReviewStore persists the per-session review buffer and status using
// SQLite. The DB lifecycle is owned externally; Close is a no-op.
type SQLiteReviewStore struct {
	db     *sql.DB
	logger *slog.Logger
}

// NewSQLiteReviewStore wraps an existing *sql.DB and auto-creates the review
// tables. The caller is responsible for opening the DB and applying pragmas
// (WAL, foreign_keys). The sessions table must be created before calling this
// (review_comments/review_state have FKs to sessions), so initialize this store
// after the session store.
func NewSQLiteReviewStore(db *sql.DB) (*SQLiteReviewStore, error) {
	store := &SQLiteReviewStore{db: db}
	if err := store.createTables(); err != nil {
		return nil, fmt.Errorf("failed to create review tables: %w", err)
	}
	return store, nil
}

// SetLogger sets an optional logger. Safe to call at any time.
func (s *SQLiteReviewStore) SetLogger(l *slog.Logger) {
	s.logger = l
}

// log returns the store logger, falling back to slog.Default().
func (s *SQLiteReviewStore) log() *slog.Logger {
	if s.logger != nil {
		return s.logger
	}
	return slog.Default()
}

// Close is a no-op — the DB lifecycle is managed externally.
func (s *SQLiteReviewStore) Close() error {
	return nil
}

// createTables creates the review tables and indexes idempotently.
func (s *SQLiteReviewStore) createTables() error {
	schema := `
	CREATE TABLE IF NOT EXISTS review_comments (
		id TEXT PRIMARY KEY,
		session_id TEXT NOT NULL REFERENCES sessions(id) ON DELETE CASCADE,
		kind TEXT NOT NULL CHECK (kind IN ('general','hunk')),
		file_path TEXT,
		hunk_id TEXT,
		body TEXT NOT NULL,
		created_at TIMESTAMP NOT NULL
	);
	CREATE INDEX IF NOT EXISTS idx_review_comments_session ON review_comments(session_id);
	-- Enforces at most one hunk comment per (session, file, hunk) so UpsertHunkComment
	-- can rely on an ON CONFLICT target. General rows have NULL file_path/hunk_id and
	-- never collide here (SQLite treats NULLs as distinct in unique indexes).
	CREATE UNIQUE INDEX IF NOT EXISTS idx_review_comments_hunk ON review_comments(session_id, file_path, hunk_id);

	CREATE TABLE IF NOT EXISTS review_state (
		session_id TEXT PRIMARY KEY REFERENCES sessions(id) ON DELETE CASCADE,
		status TEXT NOT NULL CHECK (status IN ('active','submitted','approved')),
		updated_at TIMESTAMP NOT NULL
	);
	`
	_, err := s.db.ExecContext(context.Background(), schema)
	return err
}

// nowRFC3339 returns the current time as an RFC 3339 string in UTC.
func nowRFC3339() string {
	return time.Now().UTC().Format(time.RFC3339)
}

// GetReview loads the review buffer and status for a session. A session with no
// persisted review returns an active review with no comments rather than nil.
func (s *SQLiteReviewStore) GetReview(ctx context.Context, sessionID string) (*Review, error) {
	if sessionID == "" {
		return nil, errors.New("session id is required")
	}

	review := &Review{
		SessionID:    sessionID,
		Status:       StatusActive,
		HunkComments: []HunkComment{},
	}

	// Status (optional — defaults to active).
	var status, updatedAt string
	err := s.db.QueryRowContext(ctx,
		`SELECT status, updated_at FROM review_state WHERE session_id = ?`, sessionID,
	).Scan(&status, &updatedAt)
	if err != nil && !errors.Is(err, sql.ErrNoRows) {
		return nil, fmt.Errorf("failed to load review status: %w", err)
	}
	if status != "" {
		review.Status = ReviewStatus(status)
		review.UpdatedAt = updatedAt
	}

	// Comments: a single general row (if any) plus zero or more hunk rows.
	rows, err := s.db.QueryContext(ctx, `
		SELECT id, kind, file_path, hunk_id, body, created_at
		FROM review_comments
		WHERE session_id = ?
		ORDER BY created_at, id`, sessionID)
	if err != nil {
		return nil, fmt.Errorf("failed to load review comments: %w", err)
	}
	defer func() {
		if cerr := rows.Close(); cerr != nil {
			s.log().Warn("failed to close review comment rows", "error", cerr)
		}
	}()

	for rows.Next() {
		var (
			id, body, createdAt, kind string
			filePath, hunkID          sql.NullString
		)
		if err := rows.Scan(&id, &kind, &filePath, &hunkID, &body, &createdAt); err != nil {
			return nil, fmt.Errorf("failed to scan review comment: %w", err)
		}
		if CommentKind(kind) == KindGeneral {
			review.GeneralComment = body
			continue
		}
		review.HunkComments = append(review.HunkComments, HunkComment{
			ID:        id,
			SessionID: sessionID,
			FilePath:  filePath.String,
			HunkID:    hunkID.String,
			Body:      body,
			CreatedAt: createdAt,
		})
	}
	if err := rows.Err(); err != nil {
		return nil, fmt.Errorf("error iterating review comments: %w", err)
	}

	return review, nil
}

// UpsertGeneralComment inserts or replaces the single session-wide general
// comment. The general row uses a deterministic id derived from the session id.
func (s *SQLiteReviewStore) UpsertGeneralComment(ctx context.Context, sessionID, body string) error {
	if sessionID == "" {
		return errors.New("session id is required")
	}
	// An empty body clears the general comment so the buffer reflects "no comment".
	if body == "" {
		if _, err := s.db.ExecContext(ctx,
			`DELETE FROM review_comments WHERE session_id = ? AND kind = 'general'`, sessionID); err != nil {
			return fmt.Errorf("failed to clear general comment: %w", err)
		}
		return nil
	}

	id := generalCommentID(sessionID)
	_, err := s.db.ExecContext(ctx, `
		INSERT INTO review_comments (id, session_id, kind, file_path, hunk_id, body, created_at)
		VALUES (?, ?, 'general', NULL, NULL, ?, ?)
		ON CONFLICT(id) DO UPDATE SET body = excluded.body`,
		id, sessionID, body, nowRFC3339())
	if err != nil {
		return fmt.Errorf("failed to upsert general comment: %w", err)
	}
	return nil
}

// UpsertHunkComment inserts or replaces the comment for a single (file, hunk)
// pair and returns the resulting comment id. The id is stable across updates
// (existing rows are mutated in place), so callers can later DeleteComment(id).
func (s *SQLiteReviewStore) UpsertHunkComment(ctx context.Context, sessionID, filePath, hunkID, body string) (string, error) {
	if sessionID == "" {
		return "", errors.New("session id is required")
	}
	if filePath == "" || hunkID == "" {
		return "", errors.New("file path and hunk id are required")
	}
	// An empty body removes the hunk comment so the buffer stays tidy.
	if body == "" {
		if _, err := s.db.ExecContext(ctx,
			`DELETE FROM review_comments WHERE session_id = ? AND kind = 'hunk' AND file_path = ? AND hunk_id = ?`,
			sessionID, filePath, hunkID); err != nil {
			return "", fmt.Errorf("failed to clear hunk comment: %w", err)
		}
		return "", nil
	}

	var id string
	err := s.db.QueryRowContext(ctx, `
		INSERT INTO review_comments (id, session_id, kind, file_path, hunk_id, body, created_at)
		VALUES (?, ?, 'hunk', ?, ?, ?, ?)
		ON CONFLICT(session_id, file_path, hunk_id) DO UPDATE SET body = excluded.body
		RETURNING id`,
		uuid.NewString(), sessionID, filePath, hunkID, body, nowRFC3339(),
	).Scan(&id)
	if err != nil {
		return "", fmt.Errorf("failed to upsert hunk comment: %w", err)
	}
	return id, nil
}

// DeleteComment removes a single comment by id. Deleting a non-existent id is
// not an error.
func (s *SQLiteReviewStore) DeleteComment(ctx context.Context, id string) error {
	if id == "" {
		return errors.New("comment id is required")
	}
	if _, err := s.db.ExecContext(ctx, `DELETE FROM review_comments WHERE id = ?`, id); err != nil {
		return fmt.Errorf("failed to delete comment: %w", err)
	}
	return nil
}

// SetReviewStatus upserts the review status for a session, stamping updated_at.
func (s *SQLiteReviewStore) SetReviewStatus(ctx context.Context, sessionID string, status ReviewStatus) error {
	if sessionID == "" {
		return errors.New("session id is required")
	}
	switch status {
	case StatusActive, StatusSubmitted, StatusApproved:
	default:
		return fmt.Errorf("invalid review status: %q", status)
	}
	_, err := s.db.ExecContext(ctx, `
		INSERT INTO review_state (session_id, status, updated_at)
		VALUES (?, ?, ?)
		ON CONFLICT(session_id) DO UPDATE SET status = excluded.status, updated_at = excluded.updated_at`,
		sessionID, string(status), nowRFC3339())
	if err != nil {
		return fmt.Errorf("failed to set review status: %w", err)
	}
	return nil
}

// ClearComments removes all comments (general + hunk) for a session while
// preserving the review status.
func (s *SQLiteReviewStore) ClearComments(ctx context.Context, sessionID string) error {
	if sessionID == "" {
		return errors.New("session id is required")
	}
	if _, err := s.db.ExecContext(ctx,
		`DELETE FROM review_comments WHERE session_id = ?`, sessionID); err != nil {
		return fmt.Errorf("failed to clear review comments: %w", err)
	}
	return nil
}

// ClearReview resets the whole review for a session: removes all comments and
// the status row. A subsequent GetReview returns an empty active review.
func (s *SQLiteReviewStore) ClearReview(ctx context.Context, sessionID string) error {
	if sessionID == "" {
		return errors.New("session id is required")
	}
	if _, err := s.db.ExecContext(ctx,
		`DELETE FROM review_comments WHERE session_id = ?`, sessionID); err != nil {
		return fmt.Errorf("failed to clear review comments: %w", err)
	}
	if _, err := s.db.ExecContext(ctx,
		`DELETE FROM review_state WHERE session_id = ?`, sessionID); err != nil {
		return fmt.Errorf("failed to clear review state: %w", err)
	}
	return nil
}

// generalCommentID is the deterministic row id for a session's single general
// comment, used as the ON CONFLICT(id) target for UpsertGeneralComment.
func generalCommentID(sessionID string) string {
	return sessionID + ":general"
}
