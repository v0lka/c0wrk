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

// Copies review data into the forked session inside ForkSession's
// transaction — a review-copy failure aborts the whole fork. Nil skips
// review cloning. See specs/domains/session-lifecycle.md (§ Session
// Forking).
type ForkReviewCloner func(ctx context.Context, tx *sql.Tx, srcSessionID, dstSessionID string) error

// Deep, independent copy with fresh identifiers (no shared rows), runtime
// accounting reset, "<source> (fork N)"; one atomic transaction. Caller
// enforces the no-unfinished-task guard. See
// specs/domains/session-lifecycle.md (§ Session Forking).
func (s *SQLiteSessionStore) ForkSession(ctx context.Context, srcID string, cloneReview ForkReviewCloner) (*SessionInfo, error) {
	return s.ForkSessionWithBinding(ctx, srcID, "", nil, cloneReview)
}

// Persists a fork after its destination workspace has been prepared;
// managed sources require a distinct managed binding (the Git lifecycle
// owner creates the derived branch first). See
// specs/domains/session-lifecycle.md (§ Session Execution Workspace Binding).
func (s *SQLiteSessionStore) ForkSessionWithBinding(ctx context.Context, srcID, dstID string, binding *WorkspaceBinding, cloneReview ForkReviewCloner) (*SessionInfo, error) {
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

	if src.WorkspaceBinding != nil && src.WorkspaceBinding.Kind == WorkspaceManagedWorktree {
		if binding == nil || binding.Kind != WorkspaceManagedWorktree || binding.WorkspacePath == src.WorkspaceBinding.WorkspacePath || binding.Branch == src.WorkspaceBinding.Branch {
			return nil, errors.New("managed session fork requires a new tree and derived branch")
		}
	} else if binding != nil && binding.Kind != WorkspaceLocal {
		return nil, errors.New("local session fork retains the local checkout")
	}
	bindingJSON, err := s.sessionBindingJSON(ctx, SessionInfo{ProjectID: src.ProjectID, WorkspaceBinding: binding})
	if err != nil {
		return nil, fmt.Errorf("validate fork binding: %w", err)
	}
	forkName := s.resolveForkName(ctx, src.ProjectID, src.Name)

	now := time.Now().UTC().Format(time.RFC3339)
	newSessionID := dstID
	if newSessionID == "" {
		newSessionID = uuid.NewString()
	}
	if newSessionID == srcID {
		return nil, errors.New("fork destination must have a new session identity")
	}

	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return nil, fmt.Errorf("failed to begin fork transaction: %w", err)
	}
	defer func() { _ = tx.Rollback() }()

	// 1. New session row (runtime accounting reset).
	if _, err := tx.ExecContext(ctx, `
		INSERT INTO sessions (id, project_id, name, created_at, last_active_at, archived, pinned, total_input_tokens, total_output_tokens, model, family, fill_percent, workspace_binding)
		VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?)`,
		newSessionID, src.ProjectID, forkName, now, now, false, false, 0, 0, "", "", 0, bindingJSON,
	); err != nil {
		return nil, fmt.Errorf("failed to insert forked session: %w", err)
	}

	// 2. Messages — tool_call_id correlation is self-consistent JSON within
	// the copied set; no id rewriting.
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

	// 5. Tasks and child tables — new task id per task; NULL completed_at
	// and all blackboard/plan/trajectory data preserved.
	if err := s.forkTasks(ctx, tx, srcID, newSessionID); err != nil {
		return nil, err
	}

	// 6. Optional review clone — same transaction, all-or-nothing with the fork.
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

// Copies the source's work directories with a regenerated UUID PK per row
// (the unique (session_id, path) index cannot collide across sessions).
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

// Copies every task under a fresh task id, remapping dependent rows onto
// it; step-level identifiers survive verbatim — only the task id is
// remapped — so the fork's resume wave can still correlate specs.
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

		// task_mcp_mentions — independent durable intent under the fork's task id.
		if _, err := tx.ExecContext(ctx, `
			INSERT INTO task_mcp_mentions (task_id, server_name)
			SELECT ?, server_name FROM task_mcp_mentions WHERE task_id = ?`,
			newTaskID, oldTaskID,
		); err != nil {
			return fmt.Errorf("failed to copy task MCP mentions for %q: %w", oldTaskID, err)
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

		// task_goal_state (PK = task_id) — the fork keeps the goal history
		// of its task.
		if _, err := tx.ExecContext(ctx, `
			INSERT INTO task_goal_state (task_id, goal_state, updated_at)
			SELECT ?, goal_state, updated_at FROM task_goal_state WHERE task_id = ?`,
			newTaskID, oldTaskID,
		); err != nil {
			return fmt.Errorf("failed to copy task goal state for %q: %w", oldTaskID, err)
		}

		// task_e2s_state (PK = task_id) — the fork keeps the same resumable
		// Σ snapshot as its source task. See specs/domains/e2s.md.
		if _, err := tx.ExecContext(ctx, `
			INSERT INTO task_e2s_state (task_id, e2s_state, updated_at)
			SELECT ?, e2s_state, updated_at FROM task_e2s_state WHERE task_id = ?`,
			newTaskID, oldTaskID,
		); err != nil {
			return fmt.Errorf("failed to copy task e2s state for %q: %w", oldTaskID, err)
		}

		// task_delegations — the auto-resume wave's input; the step ids it
		// references survive verbatim, so a plain copy keeps the fork's
		// Resume able to rebuild paused subagents.
		if _, err := tx.ExecContext(ctx, `
			INSERT INTO task_delegations (task_id, delegation_id, parent_id, depth, spec, created_at)
			SELECT ?, delegation_id, parent_id, depth, spec, created_at
			FROM task_delegations WHERE task_id = ?`,
			newTaskID, oldTaskID,
		); err != nil {
			return fmt.Errorf("failed to copy task delegations for %q: %w", oldTaskID, err)
		}
	}
	if err := rows.Err(); err != nil {
		return fmt.Errorf("error iterating source tasks: %w", err)
	}
	return nil
}

// Returns the next "<baseName> (fork N)" name, N = highest existing fork
// number + 1 (see specs/domains/session-lifecycle.md, § Session Forking);
// non-matching names are ignored, query errors fall back to "(fork 1)".
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
