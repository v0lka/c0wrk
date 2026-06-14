package backend

import (
	"bufio"
	"bytes"
	"context"
	"errors"
	"fmt"
	"io/fs"
	"os"
	"os/exec"
	"path/filepath"
	"sort"
	"strings"
	"time"

	"github.com/epilande/go-devicons"
)

// errNotGitRepo reports whether a git command failure is due to the target
// directory not being a git repository (as opposed to a real operational
// error). Git is a declared prerequisite, so "command not found" is no
// longer a legitimate concern — only repo presence.
func errNotGitRepo(err error, stderr string) bool {
	var exitErr *exec.ExitError
	if !errors.As(err, &exitErr) {
		return false
	}
	s := strings.ToLower(stderr)
	return strings.Contains(s, "not a git repository")
}

// gitRepoCacheEntry is a cached result of isGitRepo.
type gitRepoCacheEntry struct {
	isRepo  bool
	expires time.Time
}

// isGitRepo reports whether dir is inside a git work tree. Results are
// cached per path for 30 seconds to avoid repeated git invocations.
// Expired entries are swept lazily when the cache exceeds 100 entries.
func (f *FrontendAPI) isGitRepo(ctx context.Context, dir string) bool {
	f.gitRepoCacheMu.Lock()
	if e, ok := f.gitRepoCache[dir]; ok && time.Now().Before(e.expires) {
		f.gitRepoCacheMu.Unlock()
		return e.isRepo
	}
	// Lazy sweep: if cache grows beyond 100 entries, purge all expired ones.
	if len(f.gitRepoCache) > 100 {
		now := time.Now()
		for k, e := range f.gitRepoCache {
			if now.After(e.expires) {
				delete(f.gitRepoCache, k)
			}
		}
	}
	f.gitRepoCacheMu.Unlock()

	cmd := exec.CommandContext(ctx, "git", "-C", dir, "rev-parse", "--is-inside-work-tree")
	cmd.Stdout = nil
	cmd.Stderr = nil
	result := cmd.Run() == nil

	f.gitRepoCacheMu.Lock()
	if f.gitRepoCache == nil {
		f.gitRepoCache = make(map[string]gitRepoCacheEntry)
	}
	f.gitRepoCache[dir] = gitRepoCacheEntry{isRepo: result, expires: time.Now().Add(30 * time.Second)}
	f.gitRepoCacheMu.Unlock()

	return result
}

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

	cmd := exec.CommandContext(f.ctx(), "git", "status", "--porcelain", "-uall")
	cmd.Dir = absRoot
	var stdout, stderr bytes.Buffer
	cmd.Stdout = &stdout
	cmd.Stderr = &stderr

	if err := cmd.Run(); err != nil {
		if errNotGitRepo(err, stderr.String()) {
			// Workspace is not a git repository — a legitimate state.
			// Return an empty map (not nil) so Wails serializes it as {}.
			return map[string]GitStatusEntry{}, nil
		}
		return nil, fmt.Errorf("git status failed: %w (%s)", err, strings.TrimSpace(stderr.String()))
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

		// Extract the path part after the status XY and space.
		// For renames/copies the format is: XY orig -> dest
		rawPath := line[3:]
		if x == 'R' || y == 'R' || x == 'C' || y == 'C' {
			if idx := strings.LastIndex(rawPath, " -> "); idx >= 0 {
				rawPath = rawPath[idx+4:] // use the destination path
			}
		}

		if x == '?' && y == '?' {
			path := filepath.Join(absRoot, rawPath)
			result[path] = GitStatusEntry{Status: "A", Staged: false}
			continue
		}

		status := ' '
		staged := false
		if x == 'M' || x == 'A' || x == 'R' || x == 'C' || x == 'U' {
			status = rune(x)
			staged = true
		} else if y == 'M' || y == 'A' || y == 'R' || y == 'C' || y == 'U' {
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

// GetFileDiff returns the unified diff of uncommitted changes for a single
// file within the active project workspace. For tracked files, it concatenates
// staged and unstaged diffs. For untracked (new) files and for files in
// non-git workspaces, it produces a full-file diff via `git diff --no-index`.
//
// Git is a declared prerequisite, so any failure of git itself (other than
// the legitimate "not a git repository" case) is propagated as an error.
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

	// Non-git workspaces short-circuit directly to --no-index, which
	// produces a full-file diff showing every line as added.
	if !f.isGitRepo(f.ctx(), absRoot) {
		diff, noIndexErr := f.runGitDiffNoIndex(absRoot, relPath)
		if noIndexErr != nil {
			return "", fmt.Errorf("git diff --no-index: %w", noIndexErr)
		}
		return diff, nil
	}

	var result strings.Builder

	// Staged changes first.
	staged, err := f.runGitDiff(absRoot, true, relPath)
	if err != nil {
		return "", fmt.Errorf("git diff --cached: %w", err)
	}
	result.WriteString(staged)

	// Unstaged changes.
	unstaged, err := f.runGitDiff(absRoot, false, relPath)
	if err != nil {
		return "", fmt.Errorf("git diff: %w", err)
	}
	result.WriteString(unstaged)

	// Untracked files in a git repo: show the full file as added.
	if result.Len() == 0 && !f.isGitTracked(absRoot, relPath) {
		untrackedDiff, untrackedErr := f.runGitDiffNoIndex(absRoot, relPath)
		if untrackedErr != nil {
			return "", fmt.Errorf("git diff --no-index: %w", untrackedErr)
		}
		result.WriteString(untrackedDiff)
	}

	return result.String(), nil
}

// isGitTracked reports whether relPath is tracked by git in dir.
func (f *FrontendAPI) isGitTracked(dir, relPath string) bool {
	cmd := exec.CommandContext(f.ctx(), "git", "ls-files", "--error-unmatch", relPath)
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

	cmd := exec.CommandContext(f.ctx(), "git", args...)
	cmd.Dir = dir
	var stdout, stderr bytes.Buffer
	cmd.Stdout = &stdout
	cmd.Stderr = &stderr

	if err := cmd.Run(); err != nil {
		return "", fmt.Errorf("%w (%s)", err, strings.TrimSpace(stderr.String()))
	}
	return stdout.String(), nil
}

// runGitDiffNoIndex produces a diff for an untracked file by comparing it
// against /dev/null. This shows the entire file content as added lines.
// git diff --no-index exits with code 1 when differences exist, so we treat
// that as success.
func (f *FrontendAPI) runGitDiffNoIndex(dir, relPath string) (string, error) {
	cmd := exec.CommandContext(f.ctx(), "git", "diff", "--no-index", "/dev/null", relPath)
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

// resolveFileIcon returns the Nerd Font icon and hex color for a file or directory.
// The color is snapped to the nearest theme palette color.
func resolveFileIcon(info os.FileInfo) (icon, color string) {
	style := devicons.IconForInfo(info)
	return style.Icon, snapToTheme(style.Color)
}

// isHidden reports whether a file or directory should be considered hidden.
// On Unix-like systems this is determined by a leading dot in the name.
func isHidden(name string) bool {
	return strings.HasPrefix(name, ".")
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

// ListDirectory returns the children of a directory. When recursive is false,
// only the immediate children are listed, sorted directories first then
// alphabetically. When recursive is true, a flat list of all files and
// directories found recursively under dirPath is returned. Within each
// directory level, directories are listed before files and both groups are
// sorted alphabetically. The .git directory and its contents are excluded.
// Files and directories ignored by .gitignore are included but flagged with
// GitIgnored=true so the frontend can render them with a subdued color.
func (f *FrontendAPI) ListDirectory(dirPath string, recursive bool) ([]FileNode, error) {
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

	if !recursive {
		return f.listDirectoryFlat(absDir)
	}
	return f.listDirectoryWalk(absDir)
}

// listDirectoryFlat returns the immediate children of a directory,
// sorted directories first then alphabetically.
func (f *FrontendAPI) listDirectoryFlat(absDir string) ([]FileNode, error) {
	entries, err := os.ReadDir(absDir)
	if err != nil {
		return nil, fmt.Errorf("failed to read directory: %w", err)
	}

	ignoredPaths, err := gitIgnoredPaths(f.ctx(), absDir)
	if err != nil {
		return nil, err
	}

	var dirs, files []FileNode
	for _, entry := range entries {
		info, infoErr := entry.Info()
		node := FileNode{
			Name:   entry.Name(),
			Path:   filepath.Join(absDir, entry.Name()),
			IsDir:  entry.IsDir(),
			Hidden: isHidden(entry.Name()),
		}
		if ignoredPaths != nil {
			node.GitIgnored = ignoredPaths[node.Path]
		}
		if infoErr != nil {
			f.log().Warn("failed to get file info", "path", node.Path, "error", infoErr)
		} else if !entry.IsDir() {
			node.Icon, node.IconColor = resolveFileIcon(info)
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

// listDirectoryWalk returns a flat list of all files and directories
// found recursively under absDir. Within each directory level, directories
// are listed before files and both groups are sorted alphabetically.
// The .git directory and its contents are excluded.
// Files and directories ignored by .gitignore are included but flagged with
// GitIgnored=true so the frontend can render them with a subdued color.
func (f *FrontendAPI) listDirectoryWalk(absDir string) ([]FileNode, error) {
	// Build the set of gitignored paths for marking.
	ignoredPaths, err := gitIgnoredPaths(f.ctx(), absDir)
	if err != nil {
		return nil, err
	}

	var nodes []FileNode
	walkErr := filepath.WalkDir(absDir, func(path string, d fs.DirEntry, walkErr error) error {
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
		info, infoErr := d.Info()
		node := FileNode{
			Name:   d.Name(),
			Path:   path,
			IsDir:  d.IsDir(),
			Hidden: isHidden(d.Name()),
		}
		if ignoredPaths != nil {
			node.GitIgnored = ignoredPaths[path]
		}
		if infoErr != nil {
			f.log().Warn("failed to get file info", "path", path, "error", infoErr)
		} else if !d.IsDir() {
			node.Icon, node.IconColor = resolveFileIcon(info)
		}
		nodes = append(nodes, node)
		return nil
	})
	if walkErr != nil {
		return nil, fmt.Errorf("failed to walk directory: %w", walkErr)
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
// in the given directory. Git is a declared prerequisite, so the only
// tolerated failure mode is "not a git repository" — in which case the
// function returns (nil, nil) to signal "no filtering". Any other git
// failure is propagated as an error.
func gitIgnoredPaths(ctx context.Context, dir string) (map[string]bool, error) {
	// Use git ls-files to get ignored files and directories.
	// --others: untracked files
	// --ignored: only show ignored ones
	// --exclude-standard: use .gitignore, .git/info/exclude, global gitignore
	// --directory: show ignored directories as a single entry (don't recurse)
	// -z: null-separated output
	cmd := exec.CommandContext(ctx, "git", "ls-files", "--others", "--ignored", "--exclude-standard", "--directory", "-z")
	cmd.Dir = dir
	var stdout, stderr bytes.Buffer
	cmd.Stdout = &stdout
	cmd.Stderr = &stderr

	if err := cmd.Run(); err != nil {
		if errNotGitRepo(err, stderr.String()) {
			return nil, nil //nolint:nilnil // non-git workspace: no filtering, not an error
		}
		return nil, fmt.Errorf("git ls-files failed: %w (%s)", err, strings.TrimSpace(stderr.String()))
	}

	output := stdout.Bytes()
	if len(output) == 0 {
		return nil, nil //nolint:nilnil // no ignored paths is not an error
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
	return result, nil
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
