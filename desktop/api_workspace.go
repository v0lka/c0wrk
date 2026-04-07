package desktop

import (
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"sort"
	"strings"
)

// FileNode represents a file or directory entry in the workspace tree.
type FileNode struct {
	Name  string `json:"name"`
	Path  string `json:"path"`
	IsDir bool   `json:"is_dir"`
}

// SessionTokensResponse holds token usage for a session.
type SessionTokensResponse struct {
	TotalInputTokens  int `json:"total_input_tokens"`
	TotalOutputTokens int `json:"total_output_tokens"`
}

// GetSessionWorkspace returns the workspace directory path for a given session.
func (a *App) GetSessionWorkspace(sessionID string) (string, error) {
	a.activeProjectMu.RLock()
	projectPath := a.activeProjectPath
	a.activeProjectMu.RUnlock()

	if projectPath == "" {
		return "", errors.New("no active project")
	}
	return projectPath, nil
}

// ListDirectory returns the immediate children of a directory, sorted directories first then alphabetically.
func (a *App) ListDirectory(dirPath string) ([]FileNode, error) {
	a.activeProjectMu.RLock()
	projectPath := a.activeProjectPath
	a.activeProjectMu.RUnlock()

	if projectPath == "" {
		return nil, errors.New("no active project")
	}

	// Security: validate path is under the active project's workspace
	absDir, err := filepath.Abs(dirPath)
	if err != nil {
		return nil, fmt.Errorf("invalid path: %w", err)
	}
	absRoot, err := filepath.Abs(projectPath)
	if err != nil {
		return nil, fmt.Errorf("invalid workspace path: %w", err)
	}
	if !strings.HasPrefix(absDir, absRoot) {
		return nil, errors.New("path outside project workspace")
	}

	entries, err := os.ReadDir(absDir)
	if err != nil {
		return nil, fmt.Errorf("failed to read directory: %w", err)
	}

	var dirs, files []FileNode
	for _, entry := range entries {
		node := FileNode{
			Name:  entry.Name(),
			Path:  filepath.Join(absDir, entry.Name()),
			IsDir: entry.IsDir(),
		}
		if entry.IsDir() {
			dirs = append(dirs, node)
		} else {
			files = append(files, node)
		}
	}

	// Sort each group alphabetically
	sort.Slice(dirs, func(i, j int) bool { return dirs[i].Name < dirs[j].Name })
	sort.Slice(files, func(i, j int) bool { return files[i].Name < files[j].Name })

	nodes := make([]FileNode, 0, len(dirs)+len(files))
	nodes = append(nodes, dirs...)
	nodes = append(nodes, files...)
	return nodes, nil
}

// WatchDirectory adds a directory to the file watcher.
func (a *App) WatchDirectory(dirPath string) error {
	if a.watcher == nil {
		return errors.New("no active file watcher")
	}
	return a.watcher.WatchDir(dirPath)
}

// UnwatchDirectory removes a directory from the file watcher.
func (a *App) UnwatchDirectory(dirPath string) error {
	if a.watcher == nil {
		return errors.New("no active file watcher")
	}
	return a.watcher.UnwatchDir(dirPath)
}

// UpdateSessionTokens persists accumulated token counts for a session.
func (a *App) UpdateSessionTokens(sessionID string, inputTokens, outputTokens int) error {
	if a.store == nil {
		return nil
	}
	return a.store.UpdateSessionTokens(sessionID, inputTokens, outputTokens)
}

// GetSessionTokens returns persisted token counts for a session.
func (a *App) GetSessionTokens(sessionID string) SessionTokensResponse {
	var result SessionTokensResponse
	if a.store == nil || sessionID == "" {
		return result
	}
	info, err := a.store.LoadSession(sessionID)
	if err != nil || info == nil {
		return result
	}
	result.TotalInputTokens = info.TotalInputTokens
	result.TotalOutputTokens = info.TotalOutputTokens
	return result
}
