package project

import (
	"context"
	"errors"
	"fmt"
	"log/slog"
	"os"
	"path/filepath"
	"sync"
	"time"

	"github.com/google/uuid"

	"github.com/v0lka/c0wrk/backend/config"
)

// Manager provides high-level project lifecycle operations.
type Manager struct {
	store    ProjectStore
	agentDir string // ~/.c0wrk
	logger   *slog.Logger
	mu       sync.RWMutex
}

// EnsureNoProject creates the No Project pseudo-project if it does not already exist.
// It is safe to call multiple times (idempotent). Returns created=true when
// the project was newly created (false when it already existed).
//
// For No Project, WorkspacePath points to the project directory itself
// (~/.c0wrk/projects/__no_project__/) rather than a shared Workspace/
// subdirectory. Per-session workspaces live under <session-uuid>/workspace/
// and are created lazily by the session manager.
func (m *Manager) EnsureNoProject() (created bool, err error) {
	m.mu.Lock()
	defer m.mu.Unlock()

	proj, err := m.store.LoadProject(context.Background(), NoProjectID)
	if err != nil {
		return false, fmt.Errorf("checking No Project: %w", err)
	}
	if proj != nil {
		return false, nil // already exists
	}

	now := time.Now().UTC().Format(time.RFC3339)
	// No Project workspace is the project directory itself; per-session
	// workspaces live under <uuid>/workspace/ and are created by the
	// session manager on demand.
	wsPath := config.ProjectDir(m.agentDir, NoProjectID)
	// Ensure the stored path is always absolute so it remains valid
	// regardless of runtime working directory changes.
	if absPath, err := filepath.Abs(wsPath); err == nil {
		wsPath = absPath
	}
	info := ProjectInfo{
		ID:            NoProjectID,
		Name:          "No Project",
		WorkspacePath: wsPath,
		IsExternal:    false,
		IsNoProject:   true,
		CreatedAt:     now,
		LastActiveAt:  now,
	}
	// Do not eagerly create the directory — per-session workspace
	// creation will create parent directories lazily via MkdirAll.
	if err := m.store.SaveProject(context.Background(), info); err != nil {
		return false, err
	}
	return true, nil
}

// NewManager creates a new project Manager and ensures the projects base
// directory (~/.c0wrk/projects/) exists.
//
// logger is used to emit warnings on non-critical directory-creation failures.
// If nil, warnings are suppressed.
func NewManager(store ProjectStore, agentDir string, logger *slog.Logger) *Manager {
	// Ensure the projects base directory exists so downstream project
	// creation (internal workspaces, session directories) can proceed
	// without the caller needing to manage directory layout.
	projectsDir := config.ProjectsDir(agentDir)
	if err := os.MkdirAll(projectsDir, 0o755); err != nil {
		if logger != nil {
			logger.Warn("failed to create projects directory", "path", projectsDir, "error", err)
		}
	}

	return &Manager{
		store:    store,
		agentDir: agentDir,
		logger:   logger,
	}
}

// CreateProject creates a new project with either an internal or external workspace.
// If externalPath is empty, an internal workspace directory is created under the agentDir.
// If externalPath is non-empty, the path is validated and used as-is (external project).
func (m *Manager) CreateProject(name, externalPath string) (*ProjectInfo, error) {
	m.mu.Lock()
	defer m.mu.Unlock()

	id := uuid.New().String()
	now := time.Now().UTC().Format(time.RFC3339)

	info := ProjectInfo{
		ID:           id,
		Name:         name,
		CreatedAt:    now,
		LastActiveAt: now,
	}

	if externalPath == "" {
		// Internal project: create workspace directory under ~/.c0wrk/projects/<id>/Workspace
		info.WorkspacePath = config.ProjectWorkspacePath(m.agentDir, id)
		info.IsExternal = false
		if err := os.MkdirAll(info.WorkspacePath, 0o755); err != nil {
			return nil, fmt.Errorf("failed to create internal workspace directory: %w", err)
		}
	} else {
		// External project: canonicalize the path (absolute, cleaned,
		// symlink-resolved) before persisting. Stored paths participate in
		// security containment (allowed roots), which compares them against
		// resolved tool inputs, so a non-canonical root would silently fail
		// the containment match — mirrors AddWorkDirectory.
		abs, err := filepath.Abs(externalPath)
		if err != nil {
			return nil, fmt.Errorf("cannot resolve external workspace path: %w", err)
		}
		abs = filepath.Clean(abs)
		if _, err := os.Stat(abs); err != nil {
			return nil, fmt.Errorf("external path does not exist: %w", err)
		}
		resolved, err := filepath.EvalSymlinks(abs)
		if err != nil {
			return nil, fmt.Errorf("cannot resolve external workspace symlinks: %w", err)
		}
		info.WorkspacePath = resolved
		info.IsExternal = true
	}

	if err := m.store.SaveProject(context.Background(), info); err != nil {
		return nil, fmt.Errorf("failed to persist project: %w", err)
	}

	return &info, nil
}

// DeleteProject removes a project. For internal projects, the workspace directory is also removed.
// External workspace directories are never touched. The No Project pseudo-project cannot be deleted.
func (m *Manager) DeleteProject(id string) error {
	if id == NoProjectID {
		return errors.New("cannot delete the No Project pseudo-project")
	}
	m.mu.Lock()
	defer m.mu.Unlock()

	proj, err := m.store.LoadProject(context.Background(), id)
	if err != nil {
		return fmt.Errorf("failed to load project for deletion: %w", err)
	}
	if proj == nil {
		return fmt.Errorf("project %q not found", id)
	}

	// Delete from store first (FK cascade handles sessions+messages)
	if err := m.store.DeleteProject(context.Background(), id); err != nil {
		return fmt.Errorf("failed to delete project from store: %w", err)
	}

	if !proj.IsExternal {
		// Internal project: remove the project directory tree
		projectDir := config.ProjectDir(m.agentDir, id)
		if err := os.RemoveAll(projectDir); err != nil {
			return fmt.Errorf("failed to remove internal project directory: %w", err)
		}
	} else {
		// External project: only clean up the project directory under the agentDir if it
		// somehow exists (it shouldn't, but be safe). NEVER touch the external workspace.
		projectDir := config.ProjectDir(m.agentDir, id)
		if _, err := os.Stat(projectDir); err == nil {
			if err := os.RemoveAll(projectDir); err != nil {
				return fmt.Errorf("failed to remove stale project directory: %w", err)
			}
		}
	}

	return nil
}

// RenameProject updates a project's display name.
func (m *Manager) RenameProject(id, name string) error {
	if id == NoProjectID {
		return errors.New("cannot rename the No Project pseudo-project")
	}
	m.mu.Lock()
	defer m.mu.Unlock()
	return m.store.RenameProject(context.Background(), id, name)
}

// ListProjects returns all projects ordered by last activity.
func (m *Manager) ListProjects() ([]ProjectInfo, error) {
	m.mu.RLock()
	defer m.mu.RUnlock()
	return m.store.ListProjects(context.Background())
}

// GetProject returns a project by ID, or nil if not found.
func (m *Manager) GetProject(id string) (*ProjectInfo, error) {
	m.mu.RLock()
	defer m.mu.RUnlock()
	return m.store.LoadProject(context.Background(), id)
}
