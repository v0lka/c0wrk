package desktop

import (
	"errors"
	"fmt"
	"log/slog"

	wailsRuntime "github.com/wailsapp/wails/v2/pkg/runtime"

	"github.com/user/agent/backend/project"
	"github.com/user/agent/backend/workspace"
)

// CreateProject creates a new project. If externalPath is empty, an internal workspace is created.
func (a *App) CreateProject(name, externalPath string) (*project.ProjectInfo, error) {
	if a.projectManager == nil {
		return nil, errors.New("project subsystem not initialized")
	}
	p, err := a.projectManager.CreateProject(name, externalPath)
	if err != nil {
		return nil, err
	}
	wailsRuntime.EventsEmit(a.ctx, "project:created", p)
	return p, nil
}

// DeleteProject deletes a project and all its sessions.
func (a *App) DeleteProject(id string) error {
	if a.projectManager == nil {
		return errors.New("project subsystem not initialized")
	}
	// If deleting the active project, clear active state
	a.activeProjectMu.Lock()
	wasActive := a.activeProjectID == id
	if wasActive {
		a.activeProjectID = ""
		a.activeProjectPath = ""
	}
	a.activeProjectMu.Unlock()

	if err := a.projectManager.DeleteProject(id); err != nil {
		return err
	}

	// Stop watcher if this was the active project
	if wasActive && a.watcher != nil {
		_ = a.watcher.Close()
		a.watcher = nil
	}

	wailsRuntime.EventsEmit(a.ctx, "project:deleted", id)
	return nil
}

// RenameProject renames a project.
func (a *App) RenameProject(id, name string) error {
	if a.projectManager == nil {
		return errors.New("project subsystem not initialized")
	}
	if err := a.projectManager.RenameProject(id, name); err != nil {
		return fmt.Errorf("failed to rename project: %w", err)
	}
	wailsRuntime.EventsEmit(a.ctx, "project:renamed", map[string]string{"id": id, "name": name})
	return nil
}

// ListProjects returns all projects sorted by last activity.
func (a *App) ListProjects() ([]project.ProjectInfo, error) {
	if a.projectManager == nil {
		return nil, errors.New("project subsystem not initialized")
	}
	return a.projectManager.ListProjects()
}

// PickDirectory opens a native directory picker dialog.
func (a *App) PickDirectory() (string, error) {
	return wailsRuntime.OpenDirectoryDialog(a.ctx, wailsRuntime.OpenDialogOptions{
		Title: "Select Workspace Directory",
	})
}

// SwitchProject activates a project, setting it as the current workspace.
func (a *App) SwitchProject(id string) error {
	if a.projectManager == nil {
		return errors.New("project subsystem not initialized")
	}
	p, err := a.projectManager.GetProject(id)
	if err != nil {
		return err
	}
	if p == nil {
		return fmt.Errorf("project not found: %s", id)
	}

	a.activeProjectMu.Lock()
	a.activeProjectID = p.ID
	a.activeProjectPath = p.WorkspacePath
	a.activeProjectMu.Unlock()

	// Update project activity timestamp
	_ = a.projStore.UpdateProjectActivity(id)

	// Recreate file watcher for the new project workspace
	if a.watcher != nil {
		_ = a.watcher.Close()
		a.watcher = nil
	}
	watcher, err := workspace.NewWatcher(p.WorkspacePath, func() {
		wailsRuntime.EventsEmit(a.ctx, "workspace:tree_changed", nil)
	})
	if err != nil {
		slog.Warn("failed to start workspace file watcher", "project", id, "error", err)
	} else {
		a.watcher = watcher
	}

	wailsRuntime.EventsEmit(a.ctx, "project:switched", p)
	return nil
}
