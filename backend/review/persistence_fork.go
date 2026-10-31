package review

import (
	"context"
	"database/sql"
	"errors"
	"fmt"

	"github.com/google/uuid"
)

// CloneReview copies the per-session review buffer (status + comments) to
// another session in its own transaction; CloneReviewTx is the
// caller-transaction variant used by session forking.
// See specs/domains/review.md.
func (s *SQLiteReviewStore) CloneReview(ctx context.Context, srcSessionID, dstSessionID string) error {
	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return fmt.Errorf("failed to begin review clone transaction: %w", err)
	}
	defer func() { _ = tx.Rollback() }()

	if err := s.CloneReviewTx(ctx, tx, srcSessionID, dstSessionID); err != nil {
		return err
	}

	if err := tx.Commit(); err != nil {
		return fmt.Errorf("failed to commit review clone transaction: %w", err)
	}
	return nil
}

// CloneReviewTx clones the review buffer on the caller-supplied transaction —
// the caller owns commit/rollback. General/file comments keep their
// deterministic ids, hunk comments get fresh UUIDs; an empty source is a no-op.
// See specs/domains/review.md.
func (s *SQLiteReviewStore) CloneReviewTx(ctx context.Context, tx *sql.Tx, srcSessionID, dstSessionID string) error {
	if srcSessionID == "" {
		return errors.New("source session id is required")
	}
	if dstSessionID == "" {
		return errors.New("destination session id is required")
	}

	// review_state — at most one row (PK = session_id). INSERT OR IGNORE is safe
	// even if the destination already has a state row.
	if _, err := tx.ExecContext(ctx, `
		INSERT OR IGNORE INTO review_state (session_id, status, updated_at)
		SELECT ?, status, updated_at FROM review_state WHERE session_id = ?`,
		dstSessionID, srcSessionID,
	); err != nil {
		return fmt.Errorf("failed to clone review state: %w", err)
	}

	// review_comments — regenerate ids so the fork owns independent rows.
	rows, err := tx.QueryContext(ctx, `
		SELECT kind, file_path, hunk_id, body, created_at
		FROM review_comments WHERE session_id = ?
		ORDER BY created_at`, srcSessionID)
	if err != nil {
		return fmt.Errorf("failed to query source review comments: %w", err)
	}
	defer func() {
		if cerr := rows.Close(); cerr != nil {
			s.log().Warn("failed to close review comment rows", "error", cerr)
		}
	}()

	for rows.Next() {
		var kind, body, createdAt string
		var filePath, hunkID sql.NullString
		if err := rows.Scan(&kind, &filePath, &hunkID, &body, &createdAt); err != nil {
			return fmt.Errorf("failed to scan source review comment: %w", err)
		}
		// Deterministic ids keep a later upsert updating the cloned row in
		// place; only hunk comments (whose upsert keys on the
		// (session_id, file_path, hunk_id) index, not the row id) get a fresh
		// random UUID.
		var id string
		switch CommentKind(kind) {
		case KindGeneral:
			id = generalCommentID(dstSessionID)
		case KindFile:
			id = fileCommentID(dstSessionID, filePath.String)
		default: // KindHunk
			id = uuid.NewString()
		}
		if _, err := tx.ExecContext(ctx, `
			INSERT INTO review_comments (id, session_id, kind, file_path, hunk_id, body, created_at)
			VALUES (?, ?, ?, ?, ?, ?, ?)`,
			id, dstSessionID, kind, filePath, hunkID, body, createdAt,
		); err != nil {
			return fmt.Errorf("failed to insert cloned review comment: %w", err)
		}
	}
	if err := rows.Err(); err != nil {
		return fmt.Errorf("error iterating source review comments: %w", err)
	}

	return nil
}
