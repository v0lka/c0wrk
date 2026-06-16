package backend

import (
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"strings"

	"github.com/epilande/go-devicons"

	"github.com/v0lka/c0wrk/core/workspace"
)

// resolveWorkspacePath validates that filePath is within the active project
// workspace and returns the resolved absolute path and workspace root.
func (f *FrontendAPI) resolveWorkspacePath(filePath string) (absPath, absRoot string, err error) {
	f.activeProjectMu.RLock()
	projectPath := f.activeProjectPath
	f.activeProjectMu.RUnlock()

	if projectPath == "" {
		return "", "", errors.New("no active project")
	}

	absPath, err = filepath.Abs(filePath)
	if err != nil {
		return "", "", fmt.Errorf("invalid path: %w", err)
	}
	absRoot, err = filepath.Abs(projectPath)
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
// is returned ONLY if it matches the active project — this guards against
// returning a stale workspace from a session that belongs to a different
// project the user switched away from.
// Falls back to the active project workspace path if the session is not yet
// registered, the manager is unavailable, or the session's workspace does not
// match the active project.
func (f *FrontendAPI) GetSessionWorkspace(sessionID string) (string, error) {
	f.activeProjectMu.RLock()
	activeProject := f.activeProjectPath
	f.activeProjectMu.RUnlock()

	if sessionID != "" && f.app != nil {
		if mgr := f.app.Manager(); mgr != nil {
			if wsPath, ok := mgr.GetSessionWorkspacePath(sessionID); ok && wsPath != "" {
				// Only return the session workspace if it belongs to the active project.
				if wsPath == activeProject || activeProject == "" {
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
func (f *FrontendAPI) GetGitStatus(dirPath string) (map[string]GitStatusEntry, error) {
	f.activeProjectMu.RLock()
	projectPath := f.activeProjectPath
	f.activeProjectMu.RUnlock()

	if projectPath == "" {
		return nil, errors.New("no active project")
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
func (f *FrontendAPI) GetFileDiff(filePath string) (string, error) {
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
	f.activeProjectMu.RUnlock()

	if projectPath == "" {
		return nil, errors.New("no active project")
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
