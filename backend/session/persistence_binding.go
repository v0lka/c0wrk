package session

import (
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"fmt"
	"strings"
)

// migrateWorkspaceBindings changes only session metadata. The transaction makes
// an interrupted upgrade retryable without half-backfilled CODE rows.
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
	// Plain (non-unique) index: since shared managed worktrees (ADR-080,
	// shared-tree revision) several sessions may bind to the same tree; the
	// index only accelerates ownership lookups. migrateSharedManagedWorktrees
	// rewrites the historical UNIQUE form left by earlier installs.
	if _, err := tx.ExecContext(ctx, `CREATE INDEX idx_session_managed_workspace ON sessions(json_extract(workspace_binding, '$.workspace_path')) WHERE workspace_binding <> '' AND json_extract(workspace_binding, '$.kind') = 'managed_worktree'`); err != nil {
		return fmt.Errorf("create managed workspace index: %w", err)
	}
	if err := tx.Commit(); err != nil {
		return fmt.Errorf("commit workspace binding migration: %w", err)
	}
	return nil
}

// migrateSharedManagedWorktrees rewrites the historical UNIQUE form of
// idx_session_managed_workspace (the one-session-per-managed-tree ownership
// rule of ADR-080 as first accepted) into the plain lookup index the shared
// managed-worktree semantics require: several sessions may now execute in
// the same tree, so ownership must not be enforced by the schema. Fresh
// databases never carry the UNIQUE form (migrateWorkspaceBindings creates
// the plain index directly), and an interrupted rewrite is retryable — the
// whole migration is one transaction.
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

// CountManagedWorktreeSessions counts the session rows bound to the managed
// worktree name, excluding excludeSessionID. The count is deliberately NOT
// scoped by project: tree names are UUID-derived and globally unique, and a
// repository CAN be registered as several projects — every binding row then
// holds the same tree open, so a project-scoped count would under-count and
// let one project's last-owner deletion release a tree another project's
// session still executes in. Archived rows are counted deliberately too: an
// archived session can be unarchived and then executes in its tree again.
// The deletion protocol uses this to release a shared tree only when its
// last binding goes away.
func (s *SQLiteSessionStore) CountManagedWorktreeSessions(ctx context.Context, worktreeName, excludeSessionID string) (int, error) {
	var n int
	err := s.db.QueryRowContext(ctx, `
		SELECT COUNT(*) FROM sessions
		WHERE id <> ?
		  AND json_extract(workspace_binding, '$.kind') = 'managed_worktree'
		  AND json_extract(workspace_binding, '$.worktree_name') = ?`,
		excludeSessionID, worktreeName).Scan(&n)
	if err != nil {
		return 0, fmt.Errorf("count managed worktree sessions (tree %s): %w", worktreeName, err)
	}
	return n, nil
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

// sessionBindingColumns includes project root in the row, avoiding nested reads
// while list rows hold the shared SQLite connection. COALESCE keeps CHAT
// sessions loadable even if their pseudo-project row is absent (their binding
// is nil and the root is unused); CODE sessions without a project row fail
// closed at validation.
const sessionBindingColumns = `workspace_binding, COALESCE((SELECT workspace_path FROM projects WHERE projects.id = sessions.project_id), '')`
