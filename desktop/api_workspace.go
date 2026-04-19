package desktop

import (
	"bytes"
	"context"
	"errors"
	"fmt"
	"io/fs"
	"log/slog"
	"os"
	"os/exec"
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

// SessionTokensResponse holds token usage statistics for a session.
type SessionTokensResponse struct {
	TotalInputTokens  int    `json:"total_input_tokens"`
	TotalOutputTokens int    `json:"total_output_tokens"`
	Model             string `json:"model"`
	Family            string `json:"family"`
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
	if absDir != absRoot && !strings.HasPrefix(absDir, absRoot+string(filepath.Separator)) {
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

// ListDirectoryRecursive returns a flat list of all files and directories
// found recursively under dirPath. Within each directory level, directories
// are listed before files and both groups are sorted alphabetically.
// The .git directory and its contents are excluded.
// Files and directories ignored by .gitignore are also excluded when the
// workspace is a git repository.
func (a *App) ListDirectoryRecursive(dirPath string) ([]FileNode, error) {
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
	if absDir != absRoot && !strings.HasPrefix(absDir, absRoot+string(filepath.Separator)) {
		return nil, errors.New("path outside project workspace")
	}

	// Build the set of gitignored paths for filtering.
	ignoredPaths := gitIgnoredPaths(absDir)

	var nodes []FileNode
	err = filepath.WalkDir(absDir, func(path string, d fs.DirEntry, walkErr error) error {
		if walkErr != nil {
			slog.Warn("skipping unreadable path", "path", path, "error", walkErr)
			return nil
		}
		// Skip .git directory tree
		if d.IsDir() && d.Name() == ".git" {
			return filepath.SkipDir
		}
		// Skip the root directory itself
		if path == absDir {
			return nil
		}
		// Skip gitignored entries; skip entire directory trees for performance.
		if ignoredPaths[path] {
			if d.IsDir() {
				return filepath.SkipDir
			}
			return nil
		}
		nodes = append(nodes, FileNode{
			Name:  d.Name(),
			Path:  path,
			IsDir: d.IsDir(),
		})
		return nil
	})
	if err != nil {
		return nil, fmt.Errorf("failed to walk directory: %w", err)
	}

	// Sort: directories before files, alphabetically within each group,
	// preserving depth-first order by using the full path as a tiebreaker.
	sort.SliceStable(nodes, func(i, j int) bool {
		di := filepath.Dir(nodes[i].Path)
		dj := filepath.Dir(nodes[j].Path)
		if di != dj {
			return nodes[i].Path < nodes[j].Path
		}
		// Same parent: directories first
		if nodes[i].IsDir != nodes[j].IsDir {
			return nodes[i].IsDir
		}
		return nodes[i].Name < nodes[j].Name
	})

	return nodes, nil
}

// gitIgnoredPaths returns a set of absolute paths that are ignored by git
// in the given directory. It uses "git ls-files" to collect ignored untracked
// files and directories. If the directory is not a git repository or git is
// not available, it returns nil (no filtering).
func gitIgnoredPaths(dir string) map[string]bool {
	// Quick check: if there's no .git dir, not a git repo — skip.
	if _, err := os.Stat(filepath.Join(dir, ".git")); err != nil {
		// Could be a subdirectory of a repo; try running git anyway.
		// But first check if git is available at all.
		if _, lookErr := exec.LookPath("git"); lookErr != nil {
			return nil
		}
	}

	// Use git ls-files to get ignored files and directories.
	// --others: untracked files
	// --ignored: only show ignored ones
	// --exclude-standard: use .gitignore, .git/info/exclude, global gitignore
	// --directory: show ignored directories as a single entry (don't recurse)
	// -z: null-separated output
	cmd := exec.CommandContext(context.Background(), "git", "ls-files", "--others", "--ignored", "--exclude-standard", "--directory", "-z")
	cmd.Dir = dir
	var stdout bytes.Buffer
	cmd.Stdout = &stdout
	cmd.Stderr = nil // discard stderr

	if err := cmd.Run(); err != nil {
		slog.Debug("git ls-files failed, skipping gitignore filtering", "dir", dir, "error", err)
		return nil
	}

	output := stdout.Bytes()
	if len(output) == 0 {
		return nil
	}

	// Parse null-separated paths (relative to dir).
	result := make(map[string]bool)
	for _, entry := range bytes.Split(output, []byte{'\x00'}) {
		rel := string(entry)
		if rel == "" {
			continue
		}
		// git ls-files --directory appends '/' to directory entries.
		rel = strings.TrimSuffix(rel, "/")
		result[filepath.Join(dir, rel)] = true
	}
	return result
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
func (a *App) UpdateSessionTokens(sessionID string, inputTokens, outputTokens int, model, family string) error {
	if a.store == nil {
		return nil
	}
	return a.store.UpdateSessionTokens(sessionID, inputTokens, outputTokens, model, family)
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
	result.Model = info.Model
	result.Family = info.Family
	return result
}
