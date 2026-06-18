package backend

import (
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"strings"

	"github.com/epilande/go-devicons"

	"github.com/v0lka/c0wrk/backend/project"
	"github.com/v0lka/c0wrk/core/workspace"
)

// resolveWorkspacePath validates that filePath is within the active project
// workspace and returns the resolved absolute path and workspace root.
// For No Project (CHAT mode), the validation root is the No Project base
// directory (which contains both the placeholder Workspace and per-session
// session workspaces), allowing file operations on session-isolated workspaces.
func (f *FrontendAPI) resolveWorkspacePath(filePath string) (absPath, absRoot string, err error) {
	f.activeProjectMu.RLock()
	projectPath := f.activeProjectPath
	projectID := f.activeProjectID
	f.activeProjectMu.RUnlock()

	if projectPath == "" {
		return "", "", errors.New("no active project")
	}

	absPath, err = filepath.Abs(filePath)
	if err != nil {
		return "", "", fmt.Errorf("invalid path: %w", err)
	}

	// For No Project, validate against the No Project base directory
	// (e.g. ~/.c0wrk/projects/__no_project__/) instead of the project
	// workspace (e.g. .../__no_project__/Workspace) so that per-session
	// workspaces (e.g. .../__no_project__/sessions/<id>/Workspace) pass.
	//
	// NOTE: resolveWorkspacePath does NOT enforce the structural
	// sessions/<uuid>/Workspace constraint — that lives in ListDirectory,
	// the user-facing entry point. ReadFile, GetFileIcon, and GetFileDiff
	// receive paths returned by ListDirectory, so the trust boundary is
	// maintained.
	if projectID == project.NoProjectID {
		absRoot = filepath.Dir(projectPath)
		absRoot, err = filepath.Abs(absRoot)
	} else {
		absRoot, err = filepath.Abs(projectPath)
	}
	if err != nil {
		return "", "", fmt.Errorf("invalid workspace path: %w", err)
	}
	if absPath != absRoot && !strings.HasPrefix(absPath, absRoot+string(filepath.Separator)) {
		return "", "", errors.New("path outside project workspace")
	}
	return absPath, absRoot, nil
}

// resolveFileIcon returns the Nerd Font icon and hex color for a file or directory.
// The color is snapped to the nearest theme palette color.
func resolveFileIcon(info os.FileInfo) (icon, color string) {
	style := devicons.IconForInfo(info)
	return style.Icon, snapToTheme(style.Color)
}

// GetSessionWorkspace returns the workspace directory path for a given session.
// When the session is known to the session manager, its specific WorkspacePath
// is returned if it belongs to the active project — this guards against
// returning a stale workspace from a session that belongs to a different
// project the user switched away from.
// For No Project (CHAT mode), each session has its own isolated workspace
// (under projectsDir/__no_project__/sessions/<id>/Workspace) which differs
// from the project-level workspace path. In this case the session workspace
// is always preferred.
// Falls back to the active project workspace path if the session is not yet
// registered, the manager is unavailable, or the session belongs to a
// different project.
func (f *FrontendAPI) GetSessionWorkspace(sessionID string) (string, error) {
	f.activeProjectMu.RLock()
	activeProject := f.activeProjectPath
	activeProjectID := f.activeProjectID
	f.activeProjectMu.RUnlock()

	if sessionID != "" && f.app != nil {
		if mgr := f.app.Manager(); mgr != nil {
			if wsPath, ok := mgr.GetSessionWorkspacePath(sessionID); ok && wsPath != "" {
				// Return the session workspace if it belongs to the active project.
				// For No Project, session and project workspaces differ by design
				// (per-session isolation), so match by project ID instead.
				if wsPath == activeProject || activeProject == "" || activeProjectID == project.NoProjectID {
					return wsPath, nil
				}
			}
		}
	}

	if activeProject == "" {
		return "", errors.New("no active project")
	}
	return activeProject, nil
}

// GetFileIcon returns the Nerd Font icon and hex color for a file path.
// The path must be within the active project workspace.
func (f *FrontendAPI) GetFileIcon(filePath string) (FileIconResponse, error) {
	absPath, _, err := f.resolveWorkspacePath(filePath)
	if err != nil {
		return FileIconResponse{}, err
	}
	style := devicons.IconForPath(absPath)
	return FileIconResponse{Icon: style.Icon, IconColor: snapToTheme(style.Color)}, nil
}

// GetGitStatus returns a map of absolute file paths to their git status for the
// active project. Delegates to core/workspace.GitStatus after path validation.
// Returns an empty map for No Project (no git operations).
func (f *FrontendAPI) GetGitStatus(dirPath string) (map[string]GitStatusEntry, error) {
	f.activeProjectMu.RLock()
	projectPath := f.activeProjectPath
	projectID := f.activeProjectID
	f.activeProjectMu.RUnlock()

	if projectPath == "" {
		return nil, errors.New("no active project")
	}

	// No Project: git operations are not available.
	if projectID == project.NoProjectID {
		return map[string]GitStatusEntry{}, nil
	}

	absDir, err := filepath.Abs(dirPath)
	if err != nil {
		return nil, fmt.Errorf("invalid path: %w", err)
	}
	absRoot, err := filepath.Abs(projectPath)
	if err != nil {
		return nil, fmt.Errorf("invalid workspace path: %w", err)
	}
	if absDir != absRoot && !strings.HasPrefix(absDir, absRoot+string(filepath.Separator)) {
		return nil, errors.New("path outside project workspace")
	}

	return workspace.GitStatus(f.ctx(), absRoot)
}

// ReadFile returns the content of a file within the active project workspace.
func (f *FrontendAPI) ReadFile(filePath string) (string, error) {
	absPath, _, err := f.resolveWorkspacePath(filePath)
	if err != nil {
		return "", err
	}

	content, err := os.ReadFile(absPath)
	if err != nil {
		return "", fmt.Errorf("failed to read file: %w", err)
	}

	return string(content), nil
}

// GetFileDiff returns the unified diff of uncommitted changes for a single file
// within the active project workspace. Uses the cached isGitRepo check to
// avoid redundant git rev-parse calls, then delegates to the appropriate
// core/workspace variant.
// Returns an empty string for No Project (no git operations).
func (f *FrontendAPI) GetFileDiff(filePath string) (string, error) {
	// No Project: git diff is not available. Check before resolveWorkspacePath
	// to avoid misleading path-resolution errors.
	f.activeProjectMu.RLock()
	isNoProject := f.activeProjectID == project.NoProjectID
	f.activeProjectMu.RUnlock()
	if isNoProject {
		return "", nil
	}

	absPath, absRoot, err := f.resolveWorkspacePath(filePath)
	if err != nil {
		return "", err
	}

	relPath, err := filepath.Rel(absRoot, absPath)
	if err != nil {
		return "", fmt.Errorf("failed to compute relative path: %w", err)
	}

	if !f.isGitRepo(absRoot) {
		return workspace.GetFileDiffNoRepo(f.ctx(), absRoot, relPath)
	}
	return workspace.GetFileDiffInRepo(f.ctx(), absRoot, relPath)
}

// ListDirectory returns the children of a directory. When recursive is false,
// only the immediate children are listed, sorted directories first then
// alphabetically. When recursive is true, a flat list of all files and
// directories found recursively under dirPath is returned. The .git directory
// and its contents are excluded. Files and directories ignored by .gitignore
// are included but flagged with GitIgnored=true so the frontend can render
// them with a subdued color.
//
// Delegates to core/workspace.ListDirFlat / ListDirRecursive and attaches icons.
func (f *FrontendAPI) ListDirectory(dirPath string, recursive bool) ([]FileNode, error) {
	f.activeProjectMu.RLock()
	projectPath := f.activeProjectPath
	projectID := f.activeProjectID
	f.activeProjectMu.RUnlock()

	if projectPath == "" {
		return nil, errors.New("no active project")
	}

	absDir, err := filepath.Abs(dirPath)
	if err != nil {
		return nil, fmt.Errorf("invalid path: %w", err)
	}

	// For No Project, validate against the No Project base directory
	// (e.g. ~/.c0wrk/projects/__no_project__/) instead of the project
	// workspace (e.g. .../__no_project__/Workspace) so that per-session
	// workspaces (e.g. .../__no_project__/sessions/<id>/Workspace) pass.
	var absRoot string
	if projectID == project.NoProjectID {
		absRoot = filepath.Dir(projectPath)
	} else {
		absRoot = projectPath
	}
	absRoot, err = filepath.Abs(absRoot)
	if err != nil {
		return nil, fmt.Errorf("invalid workspace path: %w", err)
	}
	if absDir != absRoot && !strings.HasPrefix(absDir, absRoot+string(filepath.Separator)) {
		return nil, errors.New("path outside project workspace")
	}

	// No Project: enforce that sessions/ paths follow the
	// sessions/<uuid>/Workspace/... pattern. This prevents access to
	// other sessions' workspaces and to non-workspace files like
	// .session_index.json. Other paths under the No Project base
	// directory (e.g. the shared Workspace placeholder) are allowed.
	if projectID == project.NoProjectID {
		rel, relErr := filepath.Rel(absRoot, absDir)
		if relErr == nil {
			parts := strings.Split(filepath.ToSlash(rel), "/")
			if len(parts) >= 1 && parts[0] == "sessions" {
				// Must be sessions/<uuid>/Workspace[/...]
				if len(parts) < 3 || parts[2] != "Workspace" {
					return nil, errors.New("access denied: path under sessions/ must be sessions/<id>/Workspace")
				}
			}
		}
	}

	var ignoredPaths map[string]bool
	if f.isGitRepo(absRoot) {
		ignoredPaths, err = workspace.GitIgnoredPaths(f.ctx(), absRoot)
		if err != nil {
			return nil, err
		}
	}

	opts := []workspace.ListDirOption{
		workspace.WithIconResolver(resolveFileIcon),
		workspace.WithLogger(f.log()),
	}

	var nodes []FileNode
	if !recursive {
		nodes, err = workspace.ListDirFlat(absDir, ignoredPaths, opts...)
	} else {
		nodes, err = workspace.ListDirRecursive(absDir, ignoredPaths, opts...)
	}
	if err != nil {
		return nil, err
	}

	return nodes, nil
}

// WatchDirectory adds a directory to the file watcher.
func (f *FrontendAPI) WatchDirectory(dirPath string) error {
	if f.watcher == nil {
		return errors.New("no active file watcher")
	}
	return f.watcher.WatchDir(dirPath)
}

// UnwatchDirectory removes a directory from the file watcher.
func (f *FrontendAPI) UnwatchDirectory(dirPath string) error {
	if f.watcher == nil {
		return errors.New("no active file watcher")
	}
	return f.watcher.UnwatchDir(dirPath)
}
