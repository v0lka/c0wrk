package session

import (
	"context"
	"encoding/json"
	"fmt"
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
	if _, err := tx.ExecContext(ctx, `CREATE UNIQUE INDEX idx_session_managed_workspace ON sessions(json_extract(workspace_binding, '$.workspace_path')) WHERE workspace_binding <> '' AND json_extract(workspace_binding, '$.kind') = 'managed_worktree'`); err != nil {
		return fmt.Errorf("create managed workspace ownership index: %w", err)
	}
	if err := tx.Commit(); err != nil {
		return fmt.Errorf("commit workspace binding migration: %w", err)
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

// sessionBindingColumns includes project root in the row, avoiding nested reads
// while list rows hold the shared SQLite connection. COALESCE keeps CHAT
// sessions loadable even if their pseudo-project row is absent (their binding
// is nil and the root is unused); CODE sessions without a project row fail
// closed at validation.
const sessionBindingColumns = `workspace_binding, COALESCE((SELECT workspace_path FROM projects WHERE projects.id = sessions.project_id), '')`
