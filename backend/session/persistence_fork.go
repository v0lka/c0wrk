package session

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
	"strconv"
	"strings"
	"time"

	"github.com/google/uuid"
)

// ForkReviewCloner copies review data from the source session to the forked
// session within the fork transaction (tx). It is invoked inside ForkSession's
// transaction so a review-copy failure aborts the entire fork (rolled back). A
// nil callback skips review cloning. The review store implements it via
// CloneReviewTx, sharing the caller's transaction because both stores are
// backed by the same *sql.DB.
type ForkReviewCloner func(ctx context.Context, tx *sql.Tx, srcSessionID, dstSessionID string) error

// ForkSession creates a deep, independent copy of a session and all of its
// associated data — messages, terminal commands, work directories, tasks and
// their steps/facts/attachments/trajectory — with freshly generated
// identifiers (new session id, new task ids, regenerated work-directory UUIDs)
// so the fork shares no rows with the original.
//
// Runtime counters (tokens, fill percent) and the model/family fields are
// reset: the fork keeps the conversation history but starts with fresh runtime
// accounting. The fork is created in the same project as the source and named
// "<source name> (fork N)" where N is the smallest positive integer making the
// name unique within the project.
//
// If cloneReview is non-nil it is run inside the same transaction so that the
// whole fork (session + tasks + review) commits atomically; on any error the
// fork is rolled back and the source is left untouched.
//
// The caller is responsible for ensuring the source session has no unfinished
// tasks (use GetUnfinishedTask) before calling this method; the store performs
// a faithful copy regardless of task status.
func (s *SQLiteSessionStore) ForkSession(ctx context.Context, srcID string, cloneReview ForkReviewCloner) (*SessionInfo, error) {
	if srcID == "" {
		return nil, errors.New("source session id is required")
	}

	src, err := s.LoadSession(ctx, srcID)
	if err != nil {
		return nil, fmt.Errorf("failed to load source session: %w", err)
	}
	if src == nil {
		return nil, fmt.Errorf("source session %q not found", srcID)
	}

	forkName := s.resolveForkName(ctx, src.ProjectID, src.Name)

	now := time.Now().UTC().Format(time.RFC3339)
	newSessionID := uuid.NewString()

	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return nil, fmt.Errorf("failed to begin fork transaction: %w", err)
	}
	defer func() { _ = tx.Rollback() }()

	// 1. New session row (fresh runtime accounting: tokens/fill/model/family reset).
	if _, err := tx.ExecContext(ctx, `
		INSERT INTO sessions (id, project_id, name, created_at, last_active_at, archived, pinned, total_input_tokens, total_output_tokens, model, family, fill_percent)
		VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?)`,
		newSessionID, src.ProjectID, forkName, now, now, false, false, 0, 0, "", "", 0,
	); err != nil {
		return nil, fmt.Errorf("failed to insert forked session: %w", err)
	}

	// 2. Messages — autoincrement id; content/metadata/timestamps preserved.
	// The tool_call_id correlation lives inside the tool_calls/metadata JSON and
	// is self-consistent within a copied message set, so no id rewriting is needed.
	if _, err := tx.ExecContext(ctx, `
		INSERT INTO session_messages (session_id, role, content, reasoning_content, tool_calls, metadata, created_at)
		SELECT ?, role, content, reasoning_content, tool_calls, metadata, created_at
		FROM session_messages WHERE session_id = ?
		ORDER BY id`,
		newSessionID, srcID,
	); err != nil {
		return nil, fmt.Errorf("failed to copy session messages: %w", err)
	}

	// 3. Terminal commands — autoincrement id.
	if _, err := tx.ExecContext(ctx, `
		INSERT INTO terminal_commands (session_id, command, created_at)
		SELECT ?, command, created_at
		FROM terminal_commands WHERE session_id = ?
		ORDER BY id`,
		newSessionID, srcID,
	); err != nil {
		return nil, fmt.Errorf("failed to copy terminal commands: %w", err)
	}

	// 4. Work directories — UUID PK regenerated per row.
	if err := s.forkSessionWorkDirs(ctx, tx, srcID, newSessionID); err != nil {
		return nil, err
	}

	// 5. Tasks and their child tables — new task id per task, preserving NULL
	// completed_at and all blackboard/plan/trajectory data.
	if err := s.forkTasks(ctx, tx, srcID, newSessionID); err != nil {
		return nil, err
	}

	// 6. Optional review clone — runs inside the same transaction so the whole
	// fork (session + tasks + review) commits atomically or rolls back together.
	if cloneReview != nil {
		if err := cloneReview(ctx, tx, srcID, newSessionID); err != nil {
			return nil, fmt.Errorf("failed to clone review: %w", err)
		}
	}

	if err := tx.Commit(); err != nil {
		return nil, fmt.Errorf("failed to commit fork transaction: %w", err)
	}

	created, err := s.LoadSession(ctx, newSessionID)
	if err != nil {
		return nil, fmt.Errorf("failed to load forked session: %w", err)
	}
	if created == nil {
		return nil, fmt.Errorf("forked session %q not found after insert", newSessionID)
	}
	return created, nil
}

// forkSessionWorkDirs copies all work directories of the source session to the
// fork, regenerating the UUID primary key for each row. The unique
// (session_id, path) index cannot collide because the session id differs.
func (s *SQLiteSessionStore) forkSessionWorkDirs(ctx context.Context, tx *sql.Tx, srcID, newSessionID string) error {
	rows, err := tx.QueryContext(ctx,
		`SELECT path, description, created_at FROM session_work_directories WHERE session_id = ? ORDER BY created_at`,
		srcID,
	)
	if err != nil {
		return fmt.Errorf("failed to query source work directories: %w", err)
	}
	defer func() {
		if cerr := rows.Close(); cerr != nil {
			s.log().Warn("failed to close work-directory rows", "error", cerr)
		}
	}()

	for rows.Next() {
		var path, description, createdAt string
		if err := rows.Scan(&path, &description, &createdAt); err != nil {
			return fmt.Errorf("failed to scan source work directory: %w", err)
		}
		if _, err := tx.ExecContext(ctx,
			`INSERT INTO session_work_directories (id, session_id, path, description, created_at) VALUES (?, ?, ?, ?, ?)`,
			uuid.NewString(), newSessionID, path, description, createdAt,
		); err != nil {
			return fmt.Errorf("failed to insert forked work directory: %w", err)
		}
	}
	if err := rows.Err(); err != nil {
		return fmt.Errorf("error iterating source work directories: %w", err)
	}
	return nil
}

// forkTasks copies every task of the source session into the fork, generating a
// fresh task id for each and remapping the dependent rows (steps, facts,
// attachments, trajectory) onto it. completed_at NULL is preserved naturally
// via INSERT ... SELECT.
func (s *SQLiteSessionStore) forkTasks(ctx context.Context, tx *sql.Tx, srcID, newSessionID string) error {
	rows, err := tx.QueryContext(ctx,
		`SELECT id FROM tasks WHERE session_id = ? ORDER BY created_at`,
		srcID,
	)
	if err != nil {
		return fmt.Errorf("failed to query source tasks: %w", err)
	}
	defer func() {
		if cerr := rows.Close(); cerr != nil {
			s.log().Warn("failed to close task rows", "error", cerr)
		}
	}()

	for rows.Next() {
		var oldTaskID string
		if err := rows.Scan(&oldTaskID); err != nil {
			return fmt.Errorf("failed to scan source task id: %w", err)
		}
		newTaskID := uuid.NewString()

		// tasks — preserve completed_at NULL via SELECT.
		if _, err := tx.ExecContext(ctx, `
			INSERT INTO tasks (id, session_id, original_request, routing_decision, plan, reflections, final_output, attempt_count, status, created_at, completed_at)
			SELECT ?, ?, original_request, routing_decision, plan, reflections, final_output, attempt_count, status, created_at, completed_at
			FROM tasks WHERE id = ?`,
			newTaskID, newSessionID, oldTaskID,
		); err != nil {
			return fmt.Errorf("failed to copy task %q: %w", oldTaskID, err)
		}

		// task_steps — composite PK (task_id, step_id) remapped via new task id.
		if _, err := tx.ExecContext(ctx, `
			INSERT INTO task_steps (step_id, task_id, summary, full_output, error_text, steps, created_at)
			SELECT step_id, ?, summary, full_output, error_text, steps, created_at
			FROM task_steps WHERE task_id = ?`,
			newTaskID, oldTaskID,
		); err != nil {
			return fmt.Errorf("failed to copy task steps for %q: %w", oldTaskID, err)
		}

		// task_facts — blackboard facts.
		if _, err := tx.ExecContext(ctx, `
			INSERT INTO task_facts (task_id, facts, updated_at)
			SELECT ?, facts, updated_at FROM task_facts WHERE task_id = ?`,
			newTaskID, oldTaskID,
		); err != nil {
			return fmt.Errorf("failed to copy task facts for %q: %w", oldTaskID, err)
		}

		// task_attachments.
		if _, err := tx.ExecContext(ctx, `
			INSERT INTO task_attachments (task_id, attachments, updated_at)
			SELECT ?, attachments, updated_at FROM task_attachments WHERE task_id = ?`,
			newTaskID, oldTaskID,
		); err != nil {
			return fmt.Errorf("failed to copy task attachments for %q: %w", oldTaskID, err)
		}

		// task_trajectory — the ReAct execution path.
		if _, err := tx.ExecContext(ctx, `
			INSERT INTO task_trajectory (task_id, steps, updated_at)
			SELECT ?, steps, updated_at FROM task_trajectory WHERE task_id = ?`,
			newTaskID, oldTaskID,
		); err != nil {
			return fmt.Errorf("failed to copy task trajectory for %q: %w", oldTaskID, err)
		}
	}
	if err := rows.Err(); err != nil {
		return fmt.Errorf("error iterating source tasks: %w", err)
	}
	return nil
}

// resolveForkName returns the next "<baseName> (fork N)" name within the
// project, picking N as one greater than the highest existing fork number. A
// single query scans the project's session names; names that don't match the
// "<baseName> (fork <number>)" shape are ignored, so fork-of-fork names
// ("A (fork 1) (fork 1)") and base names with LIKE metacharacters are handled
// correctly. On a query error it falls back to "(fork 1)" rather than blocking
// the fork.
func (s *SQLiteSessionStore) resolveForkName(ctx context.Context, projectID, baseName string) string {
	const forkPrefix = " (fork "
	const forkSuffix = ")"
	prefix := baseName + forkPrefix

	rows, err := s.db.QueryContext(ctx,
		`SELECT name FROM sessions WHERE project_id = ?`, projectID)
	if err != nil {
		s.log().Warn("failed to query session names for fork dedup", "error", err)
		return baseName + " (fork 1)"
	}
	defer func() {
		if cerr := rows.Close(); cerr != nil {
			s.log().Warn("failed to close session-name rows", "error", cerr)
		}
	}()

	maxN := 0
	for rows.Next() {
		var name string
		if err := rows.Scan(&name); err != nil {
			continue
		}
		if !strings.HasPrefix(name, prefix) || !strings.HasSuffix(name, forkSuffix) {
			continue
		}
		numStr := strings.TrimSuffix(strings.TrimPrefix(name, prefix), forkSuffix)
		if n, atoiErr := strconv.Atoi(numStr); atoiErr == nil && n > maxN {
			maxN = n
		}
	}

	return fmt.Sprintf("%s (fork %d)", baseName, maxN+1)
}
