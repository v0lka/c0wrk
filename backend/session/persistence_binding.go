package session

import (
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"fmt"
	"strings"
)

// Metadata-only migration; the transaction makes an interrupted upgrade
// retryable without half-backfilled CODE rows.
func (s *SQLiteSessionStore) migrateWorkspaceBindings() error {
	if s.columnExists("sessions", "workspace_binding") {
		return nil
	}
	ctx := context.Background()
	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return fmt.Errorf("begin workspace binding migration: %w", err)
	}
	defer func() { _ = tx.Rollback() }()
	if _, err := tx.ExecContext(ctx, `ALTER TABLE sessions ADD COLUMN workspace_binding TEXT NOT NULL DEFAULT ''`); err != nil {
		return fmt.Errorf("add workspace binding column: %w", err)
	}
	if _, err := tx.ExecContext(ctx, `UPDATE sessions SET workspace_binding = json_object('kind', 'local', 'workspace_path', (SELECT workspace_path FROM projects WHERE projects.id = sessions.project_id)) WHERE project_id <> '__no_project__'`); err != nil {
		return fmt.Errorf("backfill local workspace bindings: %w", err)
	}
	// Plain (non-unique) index — see migrateSharedManagedWorktrees: shared
	// trees (ADR-082) mean several sessions per tree.
	if _, err := tx.ExecContext(ctx, `CREATE INDEX idx_session_managed_workspace ON sessions(json_extract(workspace_binding, '$.workspace_path')) WHERE workspace_binding <> '' AND json_extract(workspace_binding, '$.kind') = 'managed_worktree'`); err != nil {
		return fmt.Errorf("create managed workspace index: %w", err)
	}
	if err := tx.Commit(); err != nil {
		return fmt.Errorf("commit workspace binding migration: %w", err)
	}
	return nil
}

// Rewrites the historical UNIQUE form of idx_session_managed_workspace into
// the plain lookup index shared managed-worktrees require (ADR-082):
// several sessions may execute in one tree. Fresh databases never carry
// the UNIQUE form; one transaction keeps an interrupted rewrite retryable.
// See specs/domains/session-worktrees.md (§ Release).
func (s *SQLiteSessionStore) migrateSharedManagedWorktrees() error {
	ctx := context.Background()
	var sqlText string
	err := s.db.QueryRowContext(ctx,
		`SELECT sql FROM sqlite_master WHERE type = 'index' AND name = 'idx_session_managed_workspace'`).Scan(&sqlText)
	if errors.Is(err, sql.ErrNoRows) {
		return nil // no index at all: nothing to rewrite (or already gone)
	}
	if err != nil {
		return fmt.Errorf("inspect managed workspace index: %w", err)
	}
	if !strings.Contains(strings.ToUpper(sqlText), "UNIQUE") {
		return nil // already the plain shared-tree form
	}
	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return fmt.Errorf("begin shared managed worktree migration: %w", err)
	}
	defer func() { _ = tx.Rollback() }()
	if _, err := tx.ExecContext(ctx, `DROP INDEX idx_session_managed_workspace`); err != nil {
		return fmt.Errorf("drop unique managed workspace index: %w", err)
	}
	if _, err := tx.ExecContext(ctx, `CREATE INDEX idx_session_managed_workspace ON sessions(json_extract(workspace_binding, '$.workspace_path')) WHERE workspace_binding <> '' AND json_extract(workspace_binding, '$.kind') = 'managed_worktree'`); err != nil {
		return fmt.Errorf("recreate managed workspace index: %w", err)
	}
	if err := tx.Commit(); err != nil {
		return fmt.Errorf("commit shared managed worktree migration: %w", err)
	}
	return nil
}

// Counts the session rows bound to the managed tree, excluding
// excludeSessionID — the release protocol's last-owner check. Deliberately
// NOT project-scoped (tree names are globally unique); archived rows count.
// See specs/domains/session-worktrees.md (§ Release). The non-empty guard
// skips CHAT rows: json_extract on the empty default raises "malformed
// JSON" and would wedge every deletion.
func (s *SQLiteSessionStore) CountManagedWorktreeSessions(ctx context.Context, worktreeName, excludeSessionID string) (int, error) {
	var n int
	err := s.db.QueryRowContext(ctx, `
		SELECT COUNT(*) FROM sessions
		WHERE id <> ?
		  AND workspace_binding <> ''
		  AND json_extract(workspace_binding, '$.kind') = 'managed_worktree'
		  AND json_extract(workspace_binding, '$.worktree_name') = ?`,
		excludeSessionID, worktreeName).Scan(&n)
	if err != nil {
		return 0, fmt.Errorf("count managed worktree sessions (tree %s): %w", worktreeName, err)
	}
	return n, nil
}

// Writes the execution-workspace binding of a FRESH session — the one
// sanctioned mutation of the binding identity (ADR-080 immutability).
// Fresh window and project_id pin live in the UPDATE's WHERE (atomic with
// the write); validation matches SaveSession's — tampered proposals fail
// closed. See specs/domains/session-lifecycle.md (§ Binding).
func (s *SQLiteSessionStore) BindSessionWorkspace(ctx context.Context, sessionID, projectID string, binding *WorkspaceBinding) error {
	if sessionID == "" {
		return errors.New("session identity is required")
	}
	bindingJSON, err := s.sessionBindingJSON(ctx, SessionInfo{ProjectID: projectID, WorkspaceBinding: binding})
	if err != nil {
		return fmt.Errorf("validate session binding: %w", err)
	}
	result, err := s.db.ExecContext(ctx, `
		UPDATE sessions SET workspace_binding = ?
		WHERE id = ?
		  AND project_id = ?
		  AND NOT EXISTS (SELECT 1 FROM session_messages WHERE session_messages.session_id = sessions.id)
		  AND NOT EXISTS (SELECT 1 FROM tasks WHERE tasks.session_id = sessions.id)`,
		bindingJSON, sessionID, projectID,
	)
	if err != nil {
		return fmt.Errorf("failed to bind session workspace: %w", err)
	}
	changed, err := result.RowsAffected()
	if err != nil {
		return fmt.Errorf("check bound session: %w", err)
	}
	if changed == 0 {
		var exists int
		if scanErr := s.db.QueryRowContext(ctx,
			`SELECT EXISTS(SELECT 1 FROM sessions WHERE id = ?)`, sessionID).Scan(&exists); scanErr != nil {
			return fmt.Errorf("failed to bind session workspace: %w", scanErr)
		}
		if exists == 0 {
			return fmt.Errorf("session %s not found", sessionID)
		}
		return ErrSessionNotFresh
	}
	return nil
}

func (s *SQLiteSessionStore) sessionBindingJSON(ctx context.Context, info SessionInfo) (string, error) {
	var root string
	if err := s.db.QueryRowContext(ctx, `SELECT workspace_path FROM projects WHERE id = ?`, info.ProjectID).Scan(&root); err != nil {
		return "", fmt.Errorf("resolve session project: %w", err)
	}
	binding, err := NormalizeWorkspaceBinding(info.ProjectID, root, info.WorkspaceBinding)
	if err != nil {
		return "", err
	}
	if binding == nil {
		return "", nil
	}
	raw, err := json.Marshal(binding)
	if err != nil {
		return "", fmt.Errorf("encode session workspace binding: %w", err)
	}
	return string(raw), nil
}

// Carries the project root in the row to avoid nested reads while list rows
// hold the shared SQLite connection; COALESCE keeps CHAT sessions loadable
// without a pseudo-project row, CODE rows without one fail closed.
const sessionBindingColumns = `workspace_binding, COALESCE((SELECT workspace_path FROM projects WHERE projects.id = sessions.project_id), '')`
