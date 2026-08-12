package project

import (
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"fmt"
	"time"

	"github.com/google/uuid"
	"modernc.org/sqlite"
	sqlite3 "modernc.org/sqlite/lib"
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

	CREATE TABLE IF NOT EXISTS project_work_directories (
		id TEXT PRIMARY KEY,
		project_id TEXT NOT NULL REFERENCES projects(id) ON DELETE CASCADE,
		path TEXT NOT NULL,
		description TEXT NOT NULL,
		created_at TIMESTAMP NOT NULL
	);
	CREATE INDEX IF NOT EXISTS idx_project_work_dirs ON project_work_directories(project_id);
	CREATE UNIQUE INDEX IF NOT EXISTS idx_project_work_dirs_unique ON project_work_directories(project_id, path);
	`
	_, err := s.db.ExecContext(context.Background(), schema)
	if err != nil {
		return err
	}

	// Migration: add nullable research_root column to projects for persisting
	// the per-project research workspace root (RESEARCH mode toggle) across
	// app restarts. Idempotent — skipped when the column already exists.
	if !s.columnExists("projects", "research_root") {
		if _, err := s.db.ExecContext(context.Background(),
			`ALTER TABLE projects ADD COLUMN research_root TEXT`); err != nil {
			return fmt.Errorf("failed to migrate projects.research_root: %w", err)
		}
	}

	return nil
}

// columnExists checks whether a column exists in a table using PRAGMA table_info.
func (s *SQLiteProjectStore) columnExists(table, column string) bool {
	rows, err := s.db.QueryContext(context.Background(),
		"SELECT 1 FROM pragma_table_info(?) WHERE name = ?", table, column)
	if err != nil {
		return false
	}
	defer func() { _ = rows.Close() }()
	return rows.Next()
}

// nullableString returns nil for an empty string so the column stores NULL
// rather than an empty string. Used for nullable TEXT columns (e.g. research_root).
func nullableString(s string) any {
	if s == "" {
		return nil
	}
	return s
}

// SaveProject inserts or updates a project (upsert).
func (s *SQLiteProjectStore) SaveProject(ctx context.Context, info ProjectInfo) error {
	lastActiveAt := info.LastActiveAt
	if lastActiveAt == "" {
		lastActiveAt = info.CreatedAt
	}
	_, err := s.db.ExecContext(ctx, `
		INSERT INTO projects (id, name, workspace_path, is_external, research_root, created_at, last_active_at)
		VALUES (?, ?, ?, ?, ?, ?, ?)
		ON CONFLICT(id) DO UPDATE SET
			name = excluded.name,
			workspace_path = excluded.workspace_path,
			is_external = excluded.is_external,
			research_root = excluded.research_root,
			last_active_at = excluded.last_active_at`,
		info.ID, info.Name, info.WorkspacePath, info.IsExternal,
		nullableString(info.ResearchRoot),
		info.CreatedAt, lastActiveAt,
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
		SELECT id, name, workspace_path, is_external, COALESCE(research_root, ''), created_at, COALESCE(last_active_at, created_at)
		FROM projects WHERE id = ?`,
		id,
	).Scan(&info.ID, &info.Name, &info.WorkspacePath, &info.IsExternal, &info.ResearchRoot, &info.CreatedAt, &info.LastActiveAt)

	if errors.Is(err, sql.ErrNoRows) {
		return nil, nil
	}
	if err != nil {
		return nil, fmt.Errorf("failed to load project: %w", err)
	}
	info.IsNoProject = info.ID == NoProjectID
	info.IsResearch = !info.IsNoProject && info.ResearchRoot != ""
	return &info, nil
}

// ListProjects returns all projects ordered by last activity (newest first).
func (s *SQLiteProjectStore) ListProjects(ctx context.Context) ([]ProjectInfo, error) {
	rows, err := s.db.QueryContext(ctx, `
		SELECT id, name, workspace_path, is_external, COALESCE(research_root, ''), created_at, COALESCE(last_active_at, created_at)
		FROM projects
		ORDER BY COALESCE(last_active_at, created_at) DESC`)
	if err != nil {
		return nil, fmt.Errorf("failed to list projects: %w", err)
	}
	defer func() { _ = rows.Close() }()

	var projects []ProjectInfo
	for rows.Next() {
		var info ProjectInfo
		if err := rows.Scan(&info.ID, &info.Name, &info.WorkspacePath, &info.IsExternal, &info.ResearchRoot, &info.CreatedAt, &info.LastActiveAt); err != nil {
			return nil, fmt.Errorf("failed to scan project: %w", err)
		}
		info.IsNoProject = info.ID == NoProjectID
		info.IsResearch = !info.IsNoProject && info.ResearchRoot != ""
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

// isUniqueConstraintError reports whether err is a SQLite UNIQUE-constraint
// violation (extended code SQLITE_CONSTRAINT_UNIQUE = 2067). Used to translate
// duplicate (owner, path) inserts into ErrWorkDirAlreadyExists.
func isUniqueConstraintError(err error) bool {
	var sqliteErr *sqlite.Error
	if errors.As(err, &sqliteErr) {
		return sqliteErr.Code() == sqlite3.SQLITE_CONSTRAINT_UNIQUE
	}
	return false
}

// SaveProjectWorkDir inserts a project-scoped work directory record. If rec.ID
// is empty a new UUID is generated; if CreatedAt is empty the current time is
// used. Inserting a record with an existing ID is an error (upsert is not
// supported — use UpdateProjectWorkDirDescription to mutate).
func (s *SQLiteProjectStore) SaveProjectWorkDir(ctx context.Context, projectID string, rec WorkDirectoryRecord) error {
	if rec.ID == "" {
		rec.ID = uuid.New().String()
	}
	if rec.CreatedAt == "" {
		rec.CreatedAt = time.Now().UTC().Format(time.RFC3339)
	}
	_, err := s.db.ExecContext(ctx, `
		INSERT INTO project_work_directories (id, project_id, path, description, created_at)
		VALUES (?, ?, ?, ?, ?)`,
		rec.ID, projectID, rec.Path, rec.Description, rec.CreatedAt,
	)
	if err != nil {
		if isUniqueConstraintError(err) {
			return ErrWorkDirAlreadyExists
		}
		return fmt.Errorf("failed to save project work directory: %w", err)
	}
	return nil
}

// ListProjectWorkDirs returns all work directories for a project, ordered by
// creation time (oldest first). Returns an empty (non-nil) slice when none exist.
func (s *SQLiteProjectStore) ListProjectWorkDirs(ctx context.Context, projectID string) ([]WorkDirectoryRecord, error) {
	rows, err := s.db.QueryContext(ctx, `
		SELECT id, path, description, created_at
		FROM project_work_directories
		WHERE project_id = ?
		ORDER BY created_at ASC`, projectID)
	if err != nil {
		return nil, fmt.Errorf("failed to list project work directories: %w", err)
	}
	defer func() { _ = rows.Close() }()

	var recs []WorkDirectoryRecord
	for rows.Next() {
		var rec WorkDirectoryRecord
		if err := rows.Scan(&rec.ID, &rec.Path, &rec.Description, &rec.CreatedAt); err != nil {
			return nil, fmt.Errorf("failed to scan project work directory: %w", err)
		}
		recs = append(recs, rec)
	}
	if err := rows.Err(); err != nil {
		return nil, fmt.Errorf("error iterating project work directories: %w", err)
	}
	if recs == nil {
		recs = []WorkDirectoryRecord{}
	}
	return recs, nil
}

// UpdateProjectWorkDirDescription updates the human-readable description of a
// project-scoped work directory by ID. projectID is required as a scope guard:
// only a record owned by that project can be mutated, so a stale or
// cross-scope ID cannot affect another project's row.
func (s *SQLiteProjectStore) UpdateProjectWorkDirDescription(ctx context.Context, projectID, id, description string) error {
	_, err := s.db.ExecContext(ctx,
		`UPDATE project_work_directories SET description = ? WHERE id = ? AND project_id = ?`,
		description, id, projectID)
	if err != nil {
		return fmt.Errorf("failed to update project work directory description: %w", err)
	}
	return nil
}

// DeleteProjectWorkDir removes a project-scoped work directory by ID. projectID
// is required as a scope guard so a cross-scope ID cannot delete another
// project's record.
func (s *SQLiteProjectStore) DeleteProjectWorkDir(ctx context.Context, projectID, id string) error {
	_, err := s.db.ExecContext(ctx,
		`DELETE FROM project_work_directories WHERE id = ? AND project_id = ?`, id, projectID)
	if err != nil {
		return fmt.Errorf("failed to delete project work directory: %w", err)
	}
	return nil
}
