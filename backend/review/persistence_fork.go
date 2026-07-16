package review

import (
	"context"
	"database/sql"
	"errors"
	"fmt"

	"github.com/google/uuid"
)

// CloneReview copies the per-session review buffer (status + general and hunk
// comments) from one session to another. It is used by session forking to give
// the fork an identical review state.
//
// Identifier handling:
//   - The general comment keeps its deterministic id scheme (generalCommentID)
//     so a later UpsertGeneralComment in the fork does not create a duplicate.
//   - Hunk comments get fresh UUIDs.
//
// A source session with no review data is a no-op (returns nil). The operation
// runs in a single transaction; on error nothing is written.
// CloneReview copies the per-session review buffer (status + general and hunk
// comments) from one session to another in its own transaction. It is used by
// session forking to give the fork an identical review state.
//
// For atomic forking prefer CloneReviewTx, which runs on a caller-supplied
// transaction so the clone commits or rolls back together with the rest of the
// fork. See SQLiteSessionStore.ForkSession.
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

// CloneReviewTx copies the per-session review buffer onto the caller-supplied
// transaction, so the clone can participate in a larger atomic operation (e.g.
// session forking). The caller owns tx and is responsible for committing or
// rolling it back.
//
// Identifier handling:
//   - The general comment keeps its deterministic id scheme (generalCommentID)
//     so a later UpsertGeneralComment in the fork does not create a duplicate.
//   - Hunk comments get fresh UUIDs.
//
// A source session with no review data is a no-op (returns nil).
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
		// General comments use the deterministic id so a later upsert updates in
		// place; hunk comments get a fresh random UUID.
		var id string
		if CommentKind(kind) == KindGeneral {
			id = generalCommentID(dstSessionID)
		} else {
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
