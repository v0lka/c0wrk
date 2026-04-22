package backend

import (
	"bufio"
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

// GetGitStatus returns a map of absolute file paths to their git status for the active project.
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

	cmd := exec.CommandContext(context.Background(), "git", "status", "--porcelain", "-uall")
	cmd.Dir = absRoot
	var stdout bytes.Buffer
	cmd.Stdout = &stdout
	cmd.Stderr = nil

	if err := cmd.Run(); err != nil {
		// Not a git repo or git not available — silently return empty status.
		return map[string]GitStatusEntry{}, nil //nolint:nilerr // absence of git is not a failure; return empty map so Wails serializes as {} not null
	}

	result := make(map[string]GitStatusEntry)
	scanner := bufio.NewScanner(&stdout)
	for scanner.Scan() {
		line := scanner.Text()
		if len(line) < 3 {
			continue
		}
		x := line[0]
		y := line[1]

		if x == 'R' || y == 'R' || x == 'C' || y == 'C' {
			continue // skip renames and copies
		}

		rawPath := line[3:]

		if x == '?' && y == '?' {
			path := filepath.Join(absRoot, rawPath)
			result[path] = GitStatusEntry{Status: "A", Staged: false}
			continue
		}

		status := ' '
		staged := false
		if x == 'M' || x == 'A' {
			status = rune(x)
			staged = true
		} else if y == 'M' || y == 'A' {
			status = rune(y)
			staged = false
		}

		if status == ' ' {
			continue
		}

		path := filepath.Join(absRoot, rawPath)
		result[path] = GitStatusEntry{Status: string(status), Staged: staged}
	}

	return result, nil
}

// ReadFile returns the content of a file within the active project workspace.
// Binary file content is returned as-is; the frontend is responsible for
// detecting binary files and displaying a fallback message.
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
// within the active project workspace. It concatenates staged and unstaged
// diffs. For untracked (new) files where both diffs are empty, it produces
// a diff showing the entire file as added. An empty string is returned when
// there are no changes or when git is not available.
func (f *FrontendAPI) GetFileDiff(filePath string) (string, error) {
	absPath, absRoot, err := f.resolveWorkspacePath(filePath)
	if err != nil {
		return "", err
	}

	// Use path relative to workspace root so git can find the file.
	relPath, err := filepath.Rel(absRoot, absPath)
	if err != nil {
		return "", fmt.Errorf("failed to compute relative path: %w", err)
	}

	var result strings.Builder

	// Staged changes first
	staged, err := f.runGitDiff(absRoot, true, relPath)
	if err == nil {
		result.WriteString(staged)
	}

	// Unstaged changes
	unstaged, err := f.runGitDiff(absRoot, false, relPath)
	if err == nil {
		result.WriteString(unstaged)
	}

	// If no diff was produced, check if the file is untracked.
	// Only for untracked files (or non-git directories) do we generate
	// a full-file diff; tracked files with no changes should return empty.
	if result.Len() == 0 && !f.isGitTracked(absRoot, relPath) {
		untrackedDiff, untrackedErr := f.runGitDiffNoIndex(absRoot, relPath)
		if untrackedErr == nil {
			result.WriteString(untrackedDiff)
		}
	}

	return result.String(), nil
}

// isGitTracked reports whether relPath is tracked by git in dir.
func (f *FrontendAPI) isGitTracked(dir, relPath string) bool {
	cmd := exec.CommandContext(context.Background(), "git", "ls-files", "--error-unmatch", relPath)
	cmd.Dir = dir
	cmd.Stdout = nil
	cmd.Stderr = nil
	return cmd.Run() == nil
}

// runGitDiff executes a git diff command and returns its output.
func (f *FrontendAPI) runGitDiff(dir string, cached bool, relPath string) (string, error) {
	args := []string{"diff"}
	if cached {
		args = append(args, "--cached")
	}
	args = append(args, "--", relPath)

	cmd := exec.CommandContext(context.Background(), "git", args...)
	cmd.Dir = dir
	var stdout bytes.Buffer
	cmd.Stdout = &stdout
	cmd.Stderr = nil

	if err := cmd.Run(); err != nil {
		return "", err
	}
	return stdout.String(), nil
}

// runGitDiffNoIndex produces a diff for an untracked file by comparing it
// against /dev/null. This shows the entire file content as added lines.
// git diff --no-index exits with code 1 when differences exist, so we treat
// that as success.
func (f *FrontendAPI) runGitDiffNoIndex(dir, relPath string) (string, error) {
	cmd := exec.CommandContext(context.Background(), "git", "diff", "--no-index", "/dev/null", relPath)
	cmd.Dir = dir
	var stdout bytes.Buffer
	var stderr bytes.Buffer
	cmd.Stdout = &stdout
	cmd.Stderr = &stderr

	err := cmd.Run()
	if err != nil {
		// git diff --no-index exits with code 1 when there are differences
		// (which is the expected case for an untracked file with content).
		// Any other exit code is a real error.
		var exitErr *exec.ExitError
		if errors.As(err, &exitErr) && exitErr.ExitCode() == 1 {
			return stdout.String(), nil
		}
		return "", err
	}
	return stdout.String(), nil
}

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

// GetSessionWorkspace returns the workspace directory path for a given session.
func (f *FrontendAPI) GetSessionWorkspace(sessionID string) (string, error) {
	f.activeProjectMu.RLock()
	projectPath := f.activeProjectPath
	f.activeProjectMu.RUnlock()

	if projectPath == "" {
		return "", errors.New("no active project")
	}
	return projectPath, nil
}

// ListDirectory returns the immediate children of a directory, sorted directories first then alphabetically.
func (f *FrontendAPI) ListDirectory(dirPath string) ([]FileNode, error) {
	f.activeProjectMu.RLock()
	projectPath := f.activeProjectPath
	f.activeProjectMu.RUnlock()

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
func (f *FrontendAPI) ListDirectoryRecursive(dirPath string) ([]FileNode, error) {
	f.activeProjectMu.RLock()
	projectPath := f.activeProjectPath
	f.activeProjectMu.RUnlock()

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
	ignoredPaths := gitIgnoredPaths(absDir, f.log())

	var nodes []FileNode
	err = filepath.WalkDir(absDir, func(path string, d fs.DirEntry, walkErr error) error {
		if walkErr != nil {
			f.log().Warn("skipping unreadable path", "path", path, "error", walkErr)
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
func gitIgnoredPaths(dir string, logger *slog.Logger) map[string]bool {
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
		logger.Debug("git ls-files failed, skipping gitignore filtering", "dir", dir, "error", err)
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

// UpdateSessionTokens persists accumulated token counts for a session.
func (f *FrontendAPI) UpdateSessionTokens(sessionID string, inputTokens, outputTokens int, model, family string) error {
	if f.store == nil {
		return nil
	}
	return f.store.UpdateSessionTokens(sessionID, inputTokens, outputTokens, model, family)
}

// GetSessionTokens returns persisted token counts for a session.
func (f *FrontendAPI) GetSessionTokens(sessionID string) SessionTokensResponse {
	var result SessionTokensResponse
	if f.store == nil || sessionID == "" {
		return result
	}
	info, err := f.store.LoadSession(sessionID)
	if err != nil || info == nil {
		return result
	}
	result.TotalInputTokens = info.TotalInputTokens
	result.TotalOutputTokens = info.TotalOutputTokens
	result.Model = info.Model
	result.Family = info.Family
	return result
}
