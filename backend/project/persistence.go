package project

import (
	"context"
	"database/sql"
	"encoding/json"
	"errors"
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

	CREATE TABLE IF NOT EXISTS project_ui_state (
		project_id TEXT PRIMARY KEY REFERENCES projects(id) ON DELETE CASCADE,
		saved_session_id TEXT DEFAULT '',
		open_tabs TEXT NOT NULL DEFAULT '[]',
		active_file TEXT DEFAULT '',
		updated_at TIMESTAMP NOT NULL
	);
	`
	_, err := s.db.ExecContext(context.Background(), schema)
	return err
}

// SaveProject inserts or updates a project (upsert).
func (s *SQLiteProjectStore) SaveProject(ctx context.Context, info ProjectInfo) error {
	lastActiveAt := info.LastActiveAt
	if lastActiveAt == "" {
		lastActiveAt = info.CreatedAt
	}
	_, err := s.db.ExecContext(ctx, `
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
func (s *SQLiteProjectStore) LoadProject(ctx context.Context, id string) (*ProjectInfo, error) {
	var info ProjectInfo
	err := s.db.QueryRowContext(ctx, `
		SELECT id, name, workspace_path, is_external, created_at, COALESCE(last_active_at, created_at)
		FROM projects WHERE id = ?`,
		id,
	).Scan(&info.ID, &info.Name, &info.WorkspacePath, &info.IsExternal, &info.CreatedAt, &info.LastActiveAt)

	if errors.Is(err, sql.ErrNoRows) {
		return nil, nil
	}
	if err != nil {
		return nil, fmt.Errorf("failed to load project: %w", err)
	}
	return &info, nil
}

// ListProjects returns all projects ordered by last activity (newest first).
func (s *SQLiteProjectStore) ListProjects(ctx context.Context) ([]ProjectInfo, error) {
	rows, err := s.db.QueryContext(ctx, `
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
func (s *SQLiteProjectStore) DeleteProject(ctx context.Context, id string) error {
	_, err := s.db.ExecContext(ctx, `DELETE FROM projects WHERE id = ?`, id)
	if err != nil {
		return fmt.Errorf("failed to delete project: %w", err)
	}
	return nil
}

// RenameProject updates a project's name.
func (s *SQLiteProjectStore) RenameProject(ctx context.Context, id, name string) error {
	_, err := s.db.ExecContext(ctx, `UPDATE projects SET name = ? WHERE id = ?`, name, id)
	if err != nil {
		return fmt.Errorf("failed to rename project: %w", err)
	}
	return nil
}

// UpdateProjectActivity updates the last_active_at timestamp to now.
func (s *SQLiteProjectStore) UpdateProjectActivity(ctx context.Context, id string) error {
	now := time.Now().Format(time.RFC3339)
	_, err := s.db.ExecContext(ctx, `UPDATE projects SET last_active_at = ? WHERE id = ?`, now, id)
	if err != nil {
		return fmt.Errorf("failed to update project activity: %w", err)
	}
	return nil
}

// SaveUIState saves per-project switch state (open files + selected session).
func (s *SQLiteProjectStore) SaveUIState(ctx context.Context, state ProjectUIState) error {
	openTabsJSON, err := json.Marshal(state.OpenTabs)
	if err != nil {
		return fmt.Errorf("failed to marshal open tabs: %w", err)
	}

	updatedAt := state.UpdatedAt
	if updatedAt == "" {
		updatedAt = time.Now().Format(time.RFC3339)
	}

	_, err = s.db.ExecContext(ctx, `
		INSERT INTO project_ui_state (project_id, saved_session_id, open_tabs, active_file, updated_at)
		VALUES (?, ?, ?, ?, ?)
		ON CONFLICT(project_id) DO UPDATE SET
			saved_session_id = excluded.saved_session_id,
			open_tabs = excluded.open_tabs,
			active_file = excluded.active_file,
			updated_at = excluded.updated_at`,
		state.ProjectID, state.SavedSessionID, string(openTabsJSON), state.ActiveFile, updatedAt,
	)
	if err != nil {
		return fmt.Errorf("failed to save project UI state: %w", err)
	}
	return nil
}

// LoadUIState loads per-project switch state. Returns nil if not found.
func (s *SQLiteProjectStore) LoadUIState(ctx context.Context, projectID string) (*ProjectUIState, error) {
	var state ProjectUIState
	var openTabsJSON string
	err := s.db.QueryRowContext(ctx, `
		SELECT project_id, COALESCE(saved_session_id, ''), COALESCE(open_tabs, '[]'), COALESCE(active_file, ''), updated_at
		FROM project_ui_state
		WHERE project_id = ?`,
		projectID,
	).Scan(&state.ProjectID, &state.SavedSessionID, &openTabsJSON, &state.ActiveFile, &state.UpdatedAt)
	if errors.Is(err, sql.ErrNoRows) {
		return nil, nil
	}
	if err != nil {
		return nil, fmt.Errorf("failed to load project UI state: %w", err)
	}

	if err := json.Unmarshal([]byte(openTabsJSON), &state.OpenTabs); err != nil {
		return nil, fmt.Errorf("failed to unmarshal project open tabs: %w", err)
	}
	if state.OpenTabs == nil {
		state.OpenTabs = []string{}
	}

	return &state, nil
}

// Close is a no-op — the DB lifecycle is managed externally.
func (s *SQLiteProjectStore) Close() error {
	return nil
}
