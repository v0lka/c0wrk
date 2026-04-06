package project

import (
	"context"
	"database/sql"
	"fmt"
	"time"
)

// SQLiteProjectStore implements ProjectStore using SQLite.
type SQLiteProjectStore struct {
	db *sql.DB
}

// NewSQLiteProjectStore wraps an existing *sql.DB and creates the projects table.
// The caller is responsible for opening the DB and applying pragmas (WAL, foreign_keys).
func NewSQLiteProjectStore(db *sql.DB) (*SQLiteProjectStore, error) {
	store := &SQLiteProjectStore{db: db}
	if err := store.createTables(); err != nil {
		return nil, fmt.Errorf("failed to create project tables: %w", err)
	}
	return store, nil
}

// createTables creates the projects table and indexes.
func (s *SQLiteProjectStore) createTables() error {
	schema := `
	CREATE TABLE IF NOT EXISTS projects (
		id TEXT PRIMARY KEY,
		name TEXT NOT NULL,
		workspace_path TEXT NOT NULL,
		is_external BOOLEAN NOT NULL DEFAULT 0,
		created_at TIMESTAMP NOT NULL,
		last_active_at TIMESTAMP
	);
	`
	_, err := s.db.ExecContext(context.Background(), schema)
	return err
}

// SaveProject inserts or updates a project (upsert).
func (s *SQLiteProjectStore) SaveProject(info ProjectInfo) error {
	lastActiveAt := info.LastActiveAt
	if lastActiveAt == "" {
		lastActiveAt = info.CreatedAt
	}
	_, err := s.db.ExecContext(context.Background(), `
		INSERT INTO projects (id, name, workspace_path, is_external, created_at, last_active_at)
		VALUES (?, ?, ?, ?, ?, ?)
		ON CONFLICT(id) DO UPDATE SET
			name = excluded.name,
			workspace_path = excluded.workspace_path,
			is_external = excluded.is_external,
			last_active_at = excluded.last_active_at`,
		info.ID, info.Name, info.WorkspacePath, info.IsExternal, info.CreatedAt, lastActiveAt,
	)
	if err != nil {
		return fmt.Errorf("failed to save project: %w", err)
	}
	return nil
}

// LoadProject loads a project by ID. Returns nil if not found.
func (s *SQLiteProjectStore) LoadProject(id string) (*ProjectInfo, error) {
	var info ProjectInfo
	err := s.db.QueryRowContext(context.Background(), `
		SELECT id, name, workspace_path, is_external, created_at, COALESCE(last_active_at, created_at)
		FROM projects WHERE id = ?`,
		id,
	).Scan(&info.ID, &info.Name, &info.WorkspacePath, &info.IsExternal, &info.CreatedAt, &info.LastActiveAt)

	if err == sql.ErrNoRows {
		return nil, nil
	}
	if err != nil {
		return nil, fmt.Errorf("failed to load project: %w", err)
	}
	return &info, nil
}

// ListProjects returns all projects ordered by last activity (newest first).
func (s *SQLiteProjectStore) ListProjects() ([]ProjectInfo, error) {
	rows, err := s.db.QueryContext(context.Background(), `
		SELECT id, name, workspace_path, is_external, created_at, COALESCE(last_active_at, created_at)
		FROM projects
		ORDER BY COALESCE(last_active_at, created_at) DESC`)
	if err != nil {
		return nil, fmt.Errorf("failed to list projects: %w", err)
	}
	defer func() { _ = rows.Close() }()

	var projects []ProjectInfo
	for rows.Next() {
		var info ProjectInfo
		if err := rows.Scan(&info.ID, &info.Name, &info.WorkspacePath, &info.IsExternal, &info.CreatedAt, &info.LastActiveAt); err != nil {
			return nil, fmt.Errorf("failed to scan project: %w", err)
		}
		projects = append(projects, info)
	}
	if err := rows.Err(); err != nil {
		return nil, fmt.Errorf("error iterating projects: %w", err)
	}

	// Return empty slice instead of nil for consistency
	if projects == nil {
		projects = []ProjectInfo{}
	}
	return projects, nil
}

// DeleteProject deletes a project by ID. FK cascade handles sessions.
func (s *SQLiteProjectStore) DeleteProject(id string) error {
	_, err := s.db.ExecContext(context.Background(), `DELETE FROM projects WHERE id = ?`, id)
	if err != nil {
		return fmt.Errorf("failed to delete project: %w", err)
	}
	return nil
}

// RenameProject updates a project's name.
func (s *SQLiteProjectStore) RenameProject(id, name string) error {
	_, err := s.db.ExecContext(context.Background(), `UPDATE projects SET name = ? WHERE id = ?`, name, id)
	if err != nil {
		return fmt.Errorf("failed to rename project: %w", err)
	}
	return nil
}

// UpdateProjectActivity updates the last_active_at timestamp to now.
func (s *SQLiteProjectStore) UpdateProjectActivity(id string) error {
	now := time.Now().Format(time.RFC3339)
	_, err := s.db.ExecContext(context.Background(), `UPDATE projects SET last_active_at = ? WHERE id = ?`, now, id)
	if err != nil {
		return fmt.Errorf("failed to update project activity: %w", err)
	}
	return nil
}

// Close is a no-op — the DB lifecycle is managed externally.
func (s *SQLiteProjectStore) Close() error {
	return nil
}
