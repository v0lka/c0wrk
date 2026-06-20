package project

import (
	"context"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"sync"
	"time"

	"github.com/google/uuid"

	"github.com/v0lka/c0wrk/backend/config"
)

// Manager provides high-level project lifecycle operations.
type Manager struct {
	store     ProjectStore
	agentDir  string // ~/.c0wrk
	mu        sync.RWMutex
}

// EnsureNoProject creates the No Project pseudo-project if it does not already exist.
// It is safe to call multiple times (idempotent).
func (m *Manager) EnsureNoProject() error {
	m.mu.Lock()
	defer m.mu.Unlock()

	proj, err := m.store.LoadProject(context.Background(), NoProjectID)
	if err != nil {
		return fmt.Errorf("checking No Project: %w", err)
	}
	if proj != nil {
		return nil // already exists
	}

	now := time.Now().UTC().Format(time.RFC3339)
	wsPath := config.ProjectWorkspacePath(m.agentDir, NoProjectID)
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
	if err := os.MkdirAll(info.WorkspacePath, 0o755); err != nil {
		return fmt.Errorf("creating No Project workspace dir: %w", err)
	}
	return m.store.SaveProject(context.Background(), info)
}

// NewManager creates a new project Manager.
func NewManager(store ProjectStore, agentDir string) *Manager {
	return &Manager{
		store:    store,
		agentDir: agentDir,
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
		// External project: validate the external path exists
		if _, err := os.Stat(externalPath); err != nil {
			return nil, fmt.Errorf("external path does not exist: %w", err)
		}
		info.WorkspacePath = externalPath
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
