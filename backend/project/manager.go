package project

import (
	"fmt"
	"os"
	"path/filepath"
	"sync"
	"time"

	"github.com/google/uuid"
)

// Manager provides high-level project lifecycle operations.
type Manager struct {
	store       ProjectStore
	projectsDir string // ~/.c0wrk/Projects/
	mu          sync.RWMutex
}

// NewManager creates a new project Manager.
func NewManager(store ProjectStore, projectsDir string) *Manager {
	return &Manager{
		store:       store,
		projectsDir: projectsDir,
	}
}

// CreateProject creates a new project with either an internal or external workspace.
// If externalPath is empty, an internal workspace directory is created under projectsDir.
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
		// Internal project: create workspace directory under projectsDir/<id>/Workspace
		info.WorkspacePath = filepath.Join(m.projectsDir, id, "Workspace")
		info.IsExternal = false
		if err := os.MkdirAll(info.WorkspacePath, 0o755); err != nil {
			return nil, fmt.Errorf("failed to create internal workspace directory: %w", err)
		}
	} else {
		// External project: validate the external path exists
		if _, err := os.Stat(externalPath); err != nil {
			return nil, fmt.Errorf("external path does not exist: %w", err)
		}
		info.WorkspacePath = externalPath
		info.IsExternal = true
	}

	if err := m.store.SaveProject(info); err != nil {
		return nil, fmt.Errorf("failed to persist project: %w", err)
	}

	return &info, nil
}

// DeleteProject removes a project. For internal projects, the workspace directory is also removed.
// External workspace directories are never touched.
func (m *Manager) DeleteProject(id string) error {
	m.mu.Lock()
	defer m.mu.Unlock()

	proj, err := m.store.LoadProject(id)
	if err != nil {
		return fmt.Errorf("failed to load project for deletion: %w", err)
	}
	if proj == nil {
		return fmt.Errorf("project not found: %s", id)
	}

	// Delete from store first (FK cascade handles sessions+messages)
	if err := m.store.DeleteProject(id); err != nil {
		return fmt.Errorf("failed to delete project from store: %w", err)
	}

	if !proj.IsExternal {
		// Internal project: remove the project directory tree
		projectDir := filepath.Join(m.projectsDir, id)
		if err := os.RemoveAll(projectDir); err != nil {
			return fmt.Errorf("failed to remove internal project directory: %w", err)
		}
	} else {
		// External project: only clean up the project directory under projectsDir if it
		// somehow exists (it shouldn't, but be safe). NEVER touch the external workspace.
		projectDir := filepath.Join(m.projectsDir, id)
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
	m.mu.Lock()
	defer m.mu.Unlock()
	return m.store.RenameProject(id, name)
}

// ListProjects returns all projects ordered by last activity.
func (m *Manager) ListProjects() ([]ProjectInfo, error) {
	m.mu.RLock()
	defer m.mu.RUnlock()
	return m.store.ListProjects()
}

// GetProject returns a project by ID, or nil if not found.
func (m *Manager) GetProject(id string) (*ProjectInfo, error) {
	m.mu.RLock()
	defer m.mu.RUnlock()
	return m.store.LoadProject(id)
}
