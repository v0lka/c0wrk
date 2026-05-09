package backend

import (
	"context"
	"errors"
	"fmt"

	"github.com/user/agent/backend/project"
	"github.com/user/agent/backend/vectorindex"
	"github.com/user/agent/backend/workspace"
)

// CreateProject creates a new project. If externalPath is empty, an internal workspace is created.
func (f *FrontendAPI) CreateProject(name, externalPath string) (*project.ProjectInfo, error) {
	if f.projectManager == nil {
		return nil, errors.New("project subsystem not initialized")
	}
	p, err := f.projectManager.CreateProject(name, externalPath)
	if err != nil {
		return nil, err
	}

	f.emitEvent(EventProjectCreated, p)

	return p, nil
}

// DeleteProject deletes a project and all its sessions.
func (f *FrontendAPI) DeleteProject(id string) error {
	if f.projectManager == nil {
		return errors.New("project subsystem not initialized")
	}
	// If deleting the active project, clear active state
	f.activeProjectMu.Lock()
	wasActive := f.activeProjectID == id
	if wasActive {
		f.activeProjectID = ""
		f.activeProjectPath = ""
	}
	f.activeProjectMu.Unlock()

	if err := f.projectManager.DeleteProject(id); err != nil {
		return err
	}

	// Clean up vector index data for the deleted project.
	if f.vectorManager != nil {
		_ = f.vectorManager.DeleteProjectData(id) // Best-effort; error is non-critical.
	}

	// Stop watcher if this was the active project
	if wasActive && f.watcher != nil {
		_ = f.watcher.Close() // Best-effort cleanup; error is non-critical.
		f.watcher = nil
	}

	f.emitEvent(EventProjectDeleted, id)
	return nil
}

// RenameProject renames a project.
func (f *FrontendAPI) RenameProject(id, name string) error {
	if f.projectManager == nil {
		return errors.New("project subsystem not initialized")
	}
	if err := f.projectManager.RenameProject(id, name); err != nil {
		return fmt.Errorf("failed to rename project: %w", err)
	}
	f.emitEvent(EventProjectRenamed, map[string]string{"id": id, "name": name})
	return nil
}

// ListProjects returns all projects sorted by last activity.
func (f *FrontendAPI) ListProjects() ([]project.ProjectInfo, error) {
	if f.projectManager == nil {
		return nil, errors.New("project subsystem not initialized")
	}
	return f.projectManager.ListProjects()
}

// SwitchProject activates a project, setting it as the current workspace.
func (f *FrontendAPI) SwitchProject(id string) error {
	if f.projectManager == nil {
		return errors.New("project subsystem not initialized")
	}

	// Idempotency: skip if the same project is already active.
	f.activeProjectMu.RLock()
	alreadyActive := f.activeProjectID == id
	f.activeProjectMu.RUnlock()
	if alreadyActive {
		f.log().Info("SwitchProject: project already active, skipping", "project", id)
		return nil
	}

	p, err := f.projectManager.GetProject(id)
	if err != nil {
		return err
	}
	if p == nil {
		return fmt.Errorf("project not found: %s", id)
	}

	// Cancel any in-flight indexing from a previous project.
	if f.vectorManager != nil {
		f.vectorManager.CancelIndexing()
	}

	f.activeProjectMu.Lock()
	f.activeProjectID = p.ID
	f.activeProjectPath = p.WorkspacePath
	f.activeProjectMu.Unlock()

	// Set MCP working directory to the new project workspace
	if f.app != nil {
		f.app.Builder().SetMCPWorkDir(p.WorkspacePath)
	}

	// Update project activity timestamp
	_ = f.projStore.UpdateProjectActivity(context.Background(), id) // Best-effort; error is non-critical.

	// Recreate file watcher for the new project workspace
	if f.watcher != nil {
		_ = f.watcher.Close() // Best-effort cleanup; error is non-critical.
		f.watcher = nil
	}

	watcher, err := workspace.NewWatcher(p.WorkspacePath, func() {
		// Existing behavior: emit workspace tree change.
		f.emitEvent(EventWorkspaceTreeChanged, nil)

		// Trigger debounced incremental indexing via Manager.
		if f.vectorManager != nil {
			f.vectorManager.NotifyFileChange(p.WorkspacePath)
		}
	})
	if err != nil {
		f.log().Warn("failed to start workspace file watcher", "project", id, "error", err)
	} else {
		f.watcher = watcher
	}

	// --- Vector index wiring ---
	if f.vectorManager != nil {
		branch, branchErr := vectorindex.CurrentBranch(p.WorkspacePath)
		if branchErr != nil {
			f.log().Warn("failed to detect git branch", "error", branchErr)
			branch = vectorindex.DefaultBranch
		}
		capturedBranch := branch

		if switchErr := f.vectorManager.SwitchProject(p.ID, p.WorkspacePath, vectorindex.ProjectCallbacks{
			OnProgress: func(state vectorindex.IndexState, indexed, total int, file string) {
				f.emitEvent(EventVectorIndexStatus, map[string]any{
					"state":         string(state),
					"progress":      progressPercent(indexed, total),
					"files_indexed": indexed,
					"total_files":   total,
					"current_file":  file,
					"branch":        capturedBranch,
				})
			},
		}); switchErr != nil {
			f.log().Warn("vector index project switch failed", "error", switchErr)
		}
	}

	f.emitEvent(EventProjectSwitched, p)

	return nil
}

// progressPercent calculates a percentage value for indexing progress.
func progressPercent(indexed, total int) float64 {
	if total == 0 {
		return 0
	}
	return float64(indexed) / float64(total) * 100
}
