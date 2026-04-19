package desktop

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"os/exec"
	"time"

	wailsRuntime "github.com/wailsapp/wails/v2/pkg/runtime"

	"github.com/user/agent/backend/mcp"
	"github.com/user/agent/backend/project"
	"github.com/user/agent/backend/vectorindex"
	"github.com/user/agent/backend/workspace"
)

// checkCodebaseMemoryFunc is the function used to check codebase-memory-mcp installation.
// It can be overridden in tests.
var checkCodebaseMemoryFunc = mcp.CheckCodebaseMemoryMCP

// execCommandFunc is used to create exec commands. It can be overridden in tests.
var execCommandFunc = exec.CommandContext

// CreateProject creates a new project. If externalPath is empty, an internal workspace is created.
func (a *App) CreateProject(name, externalPath string) (*project.ProjectInfo, error) {
	if a.projectManager == nil {
		return nil, errors.New("project subsystem not initialized")
	}
	p, err := a.projectManager.CreateProject(name, externalPath)
	if err != nil {
		return nil, err
	}

	// Trigger async codebase indexing (non-blocking, non-fatal)
	go func() {
		defer func() {
			if r := recover(); r != nil {
				a.log().Error("panic in codebase indexing", "recover", r)
			}
		}()
		a.triggerCodebaseIndexing(p.WorkspacePath)
	}()

	wailsRuntime.EventsEmit(a.ctx, EventProjectCreated, p)

	return p, nil
}

// triggerCodebaseIndexing runs codebase-memory-mcp index_repository for the
// given workspace path. It is designed to run in a goroutine and never panics.
// Errors are logged as warnings and are non-fatal.
func (a *App) triggerCodebaseIndexing(workspacePath string) {
	a.indexingMu.Lock()
	if a.indexingDone != nil {
		// Another indexing run is already in progress; skip.
		a.indexingMu.Unlock()
		a.log().Info("codebase indexing already in progress, skipping", "workspace", workspacePath)
		return
	}
	ch := make(chan struct{})
	a.indexingDone = ch
	a.indexingMu.Unlock()
	defer func() {
		a.indexingMu.Lock()
		close(ch)
		if a.indexingDone == ch {
			a.indexingDone = nil
		}
		a.indexingMu.Unlock()
	}()

	status := checkCodebaseMemoryFunc()
	if !status.Installed {
		return
	}

	parentCtx := a.ctx
	if parentCtx == nil {
		parentCtx = context.Background()
	}
	ctx, cancel := context.WithTimeout(parentCtx, 60*time.Second)
	defer cancel()

	jsonBytes, err := json.Marshal(map[string]string{"workspace_path": workspacePath})
	if err != nil {
		a.log().Warn("failed to marshal codebase indexing JSON arg", "error", err)
		return
	}
	jsonArg := string(jsonBytes)

	cmd := execCommandFunc(ctx, status.Path, "cli", "index_repository", jsonArg)
	output, err := cmd.CombinedOutput()
	if err != nil {
		a.log().Warn("codebase-memory-mcp indexing failed",
			"workspace", workspacePath,
			"error", err,
			"output", string(output),
		)
		return
	}

	a.log().Info("codebase-memory-mcp indexing triggered", "workspace", workspacePath)
	a.resolveCodebaseProjectName(workspacePath)
}

// resolveCodebaseProjectName queries codebase-memory-mcp for the project name
// that matches the given workspace path. The result is stored in codebaseProjectName.
// Errors are logged as warnings and are non-fatal.
func (a *App) resolveCodebaseProjectName(workspacePath string) {
	status := checkCodebaseMemoryFunc()
	if !status.Installed {
		return
	}

	parentCtx := a.ctx
	if parentCtx == nil {
		parentCtx = context.Background()
	}
	ctx, cancel := context.WithTimeout(parentCtx, 10*time.Second)
	defer cancel()

	cmd := execCommandFunc(ctx, status.Path, "cli", "list_projects")
	output, err := cmd.Output()
	if err != nil {
		a.log().Warn("failed to list codebase-memory-mcp projects", "error", err)
		return
	}

	// Parse MCP response: {"content":[{"type":"text","text":"{\"projects\":[...]}"}]}
	var mcpResp struct {
		Content []struct {
			Type string `json:"type"`
			Text string `json:"text"`
		} `json:"content"`
	}
	if err := json.Unmarshal(output, &mcpResp); err != nil {
		a.log().Warn("failed to parse list_projects response", "error", err)
		return
	}
	if len(mcpResp.Content) == 0 {
		return
	}

	var projectList struct {
		Projects []struct {
			Name     string `json:"name"`
			RootPath string `json:"root_path"`
		} `json:"projects"`
	}
	if err := json.Unmarshal([]byte(mcpResp.Content[0].Text), &projectList); err != nil {
		a.log().Warn("failed to parse project list", "error", err)
		return
	}

	for _, p := range projectList.Projects {
		if p.RootPath != workspacePath {
			continue
		}
		a.activeProjectMu.Lock()
		a.codebaseProjectName = p.Name
		a.activeProjectMu.Unlock()
		a.log().Info("resolved codebase-memory-mcp project name", "name", p.Name, "path", workspacePath)
		return
	}

	a.log().Warn("codebase-memory-mcp project not found for workspace", "workspace", workspacePath)
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
		_ = a.watcher.Close() // Best-effort cleanup; error is non-critical.
		a.watcher = nil
	}

	wailsRuntime.EventsEmit(a.ctx, EventProjectDeleted, id)
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
	wailsRuntime.EventsEmit(a.ctx, EventProjectRenamed, map[string]string{"id": id, "name": name})
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

	// Idempotency: skip if the same project is already active.
	a.activeProjectMu.RLock()
	alreadyActive := a.activeProjectID == id
	a.activeProjectMu.RUnlock()
	if alreadyActive {
		a.log().Info("SwitchProject: project already active, skipping", "project", id)
		return nil
	}

	p, err := a.projectManager.GetProject(id)
	if err != nil {
		return err
	}
	if p == nil {
		return fmt.Errorf("project not found: %s", id)
	}

	// Cancel any in-flight indexing from a previous project.
	if a.vectorManager != nil {
		a.vectorManager.CancelIndexing()
	}

	a.activeProjectMu.Lock()
	a.activeProjectID = p.ID
	a.activeProjectPath = p.WorkspacePath
	a.codebaseProjectName = "" // clear stale name
	a.activeProjectMu.Unlock()

	// Set MCP working directory to the new project workspace
	if a.app != nil {
		a.app.Builder().SetMCPWorkDir(p.WorkspacePath)
	}

	// Resolve codebase-memory-mcp project name for new project (fast CLI call)
	a.resolveCodebaseProjectName(p.WorkspacePath)

	// Update project activity timestamp
	_ = a.projStore.UpdateProjectActivity(id) // Best-effort; error is non-critical.

	// Recreate file watcher for the new project workspace
	if a.watcher != nil {
		_ = a.watcher.Close() // Best-effort cleanup; error is non-critical.
		a.watcher = nil
	}

	watcher, err := workspace.NewWatcher(p.WorkspacePath, func() {
		// Existing behavior: emit workspace tree change.
		wailsRuntime.EventsEmit(a.ctx, EventWorkspaceTreeChanged, nil)

		// Trigger debounced incremental indexing via Manager.
		if a.vectorManager != nil {
			a.vectorManager.NotifyFileChange(p.WorkspacePath)
		}
	})
	if err != nil {
		a.log().Warn("failed to start workspace file watcher", "project", id, "error", err)
	} else {
		a.watcher = watcher
	}

	// --- Vector index wiring ---
	if a.vectorManager != nil {
		branch, branchErr := vectorindex.CurrentBranch(p.WorkspacePath)
		if branchErr != nil {
			a.log().Warn("failed to detect git branch", "error", branchErr)
			branch = vectorindex.DefaultBranch
		}
		capturedBranch := branch

		if switchErr := a.vectorManager.SwitchProject(p.ID, p.WorkspacePath, vectorindex.ProjectCallbacks{
			OnProgress: func(state vectorindex.IndexState, indexed, total int, file string) {
				wailsRuntime.EventsEmit(a.ctx, EventVectorIndexStatus, map[string]any{
					"state":         string(state),
					"progress":      progressPercent(indexed, total),
					"files_indexed": indexed,
					"total_files":   total,
					"current_file":  file,
					"branch":        capturedBranch,
				})
			},
		}); switchErr != nil {
			a.log().Warn("vector index project switch failed", "error", switchErr)
		}
	}

	wailsRuntime.EventsEmit(a.ctx, EventProjectSwitched, p)

	return nil
}
