package session

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
)

// SaveMCPMentions atomically unions names; only duplicate-key conflicts are ignored.
func (s *SQLiteSessionStore) SaveMCPMentions(ctx context.Context, taskID string, names []string) error {
	if len(names) == 0 {
		return nil
	}
	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return fmt.Errorf("begin MCP mention union: %w", err)
	}
	defer func() {
		if err := tx.Rollback(); err != nil && !errors.Is(err, sql.ErrTxDone) {
			s.log().Warn("failed to roll back MCP mention union", "error", err)
		}
	}()
	for _, name := range names {
		if _, err := tx.ExecContext(ctx, `INSERT INTO task_mcp_mentions (task_id, server_name)
			VALUES (?, ?) ON CONFLICT(task_id, server_name) DO NOTHING`, taskID, name); err != nil {
			return fmt.Errorf("persist MCP mention: %w", err)
		}
	}
	if err := tx.Commit(); err != nil {
		return fmt.Errorf("commit MCP mention union: %w", err)
	}
	return nil
}

// LoadMCPMentions returns durable intent, ordered and deduplicated by the primary key.
func (s *SQLiteSessionStore) LoadMCPMentions(ctx context.Context, taskID string) ([]string, error) {
	rows, err := s.db.QueryContext(ctx, `SELECT server_name FROM task_mcp_mentions WHERE task_id = ? ORDER BY server_name`, taskID)
	if err != nil {
		return nil, fmt.Errorf("load MCP mentions: %w", err)
	}
	defer func() {
		if err := rows.Close(); err != nil {
			s.log().Warn("failed to close MCP mention rows", "error", err)
		}
	}()
	var names []string
	for rows.Next() {
		var name string
		if err := rows.Scan(&name); err != nil {
			return nil, fmt.Errorf("scan MCP mention: %w", err)
		}
		names = append(names, name)
	}
	if err := rows.Err(); err != nil {
		return nil, fmt.Errorf("iterate MCP mentions: %w", err)
	}
	return names, nil
}
