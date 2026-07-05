package workspace

import (
	"bufio"
	"bytes"
	"context"
	"errors"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"strconv"
	"strings"
)

// unquoteGitPath strips surrounding double-quotes and unescapes C-style
// escapes from a git output path. git quotes paths containing special
// characters (spaces, tabs, non-ASCII) when core.quotePath is true (the
// default). Returns the path unchanged if it is not quoted.
func unquoteGitPath(path string) string {
	if len(path) >= 2 && path[0] == '"' && path[len(path)-1] == '"' {
		if unquoted, err := strconv.Unquote(path); err == nil {
			return unquoted
		}
	}
	return path
}

// errNotGitRepo reports whether a git command failure is due to the target
// directory not being a git repository (as opposed to a real operational
// error).
func errNotGitRepo(err error, stderr string) bool {
	var exitErr *exec.ExitError
	if !errors.As(err, &exitErr) {
		return false
	}
	s := strings.ToLower(stderr)
	return strings.Contains(s, "not a git repository")
}

// IsGitRepo reports whether dir is inside a git work tree. This function
// does not cache results — caching is the caller's responsibility if needed.
func IsGitRepo(ctx context.Context, dir string) bool {
	cmd := exec.CommandContext(ctx, "git", "-C", dir, "rev-parse", "--is-inside-work-tree")
	cmd.Stdout = nil
	cmd.Stderr = nil
	return cmd.Run() == nil
}

// IsGitTracked reports whether relPath is tracked by git in dir.
func IsGitTracked(ctx context.Context, dir, relPath string) bool {
	cmd := exec.CommandContext(ctx, "git", "ls-files", "--error-unmatch", relPath)
	cmd.Dir = dir
	cmd.Stdout = nil
	cmd.Stderr = nil
	return cmd.Run() == nil
}

// GitStatus runs git status --porcelain in repoPath and returns a map of
// absolute file paths to their git status entries.  Each entry captures
// both the index (staged) and work-tree (unstaged) status when both are
// present.  For backward compatibility the legacy Status/Staged fields
// reflect the index side when available, falling back to the work tree.
func GitStatus(ctx context.Context, repoPath string) (map[string]GitStatusEntry, error) {
	cmd := exec.CommandContext(ctx, "git", "status", "--porcelain", "-uall")
	cmd.Dir = repoPath
	var stdout, stderr bytes.Buffer
	cmd.Stdout = &stdout
	cmd.Stderr = &stderr

	if err := cmd.Run(); err != nil {
		if errNotGitRepo(err, stderr.String()) {
			return map[string]GitStatusEntry{}, nil
		}
		return nil, fmt.Errorf("git status failed: %w", err)
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

		rawPath := line[3:]
		if x == 'R' || y == 'R' || x == 'C' || y == 'C' {
			if idx := strings.LastIndex(rawPath, " -> "); idx >= 0 {
				rawPath = rawPath[idx+4:]
			}
		}

		xStatus := porcelainStatus(x)
		yStatus := porcelainStatus(y)

		if x == '?' && y == '?' {
			// Untracked file — not in index, present in work tree.
			path := filepath.Join(repoPath, unquoteGitPath(rawPath))
			result[path] = GitStatusEntry{
				Status:         "A",
				Staged:         false,
				IndexStatus:    "",
				WorkTreeStatus: "?",
			}
			continue
		}

		// Compute legacy Status/Staged: prefer index (staged) over
		// work-tree (unstaged) for backward compatibility.
		legacyStatus := yStatus
		legacyStaged := false
		if xStatus != "" {
			legacyStatus = xStatus
			legacyStaged = true
		}

		if legacyStatus == "" {
			continue
		}

		path := filepath.Join(repoPath, unquoteGitPath(rawPath))
		result[path] = GitStatusEntry{
			Status:         legacyStatus,
			Staged:         legacyStaged,
			IndexStatus:    xStatus,
			WorkTreeStatus: yStatus,
		}
	}

	if scanErr := scanner.Err(); scanErr != nil {
		return nil, fmt.Errorf("parsing git status output: %w", scanErr)
	}

	return result, nil
}

// porcelainStatus maps a single git-status --porcelain status column
// character to its letter string.  Returns empty string for unmodified.
// 'D' (deleted) is included so that staged/work-tree deletions and
// both-deleted (DD) merge conflicts are surfaced to consumers instead of
// being silently skipped.
func porcelainStatus(c byte) string {
	switch c {
	case 'M', 'A', 'R', 'C', 'U', 'D':
		return string(c)
	case '?':
		return "?"
	default:
		return ""
	}
}

// GetFileDiff returns the unified diff of uncommitted changes for a file
// within a git repository. For tracked files it concatenates staged and
// unstaged diffs. For untracked files and non-git workspaces, it produces
// a full-file diff via git diff --no-index.
//
// Callers that already know whether the path is in a git repository should
// prefer the more specific GetFileDiffInRepo or GetFileDiffNoRepo variants
// to avoid the redundant IsGitRepo check.
func GetFileDiff(ctx context.Context, repoPath, relPath string) (string, error) {
	if !IsGitRepo(ctx, repoPath) {
		return GetFileDiffNoRepo(ctx, repoPath, relPath)
	}
	return GetFileDiffInRepo(ctx, repoPath, relPath)
}

// GetFileDiffInRepo returns the unified diff of uncommitted changes for a
// file within a git repository. The caller must ensure repoPath is a git
// repository. For tracked files it concatenates staged and unstaged diffs.
// For untracked files it produces a full-file diff via git diff --no-index.
func GetFileDiffInRepo(ctx context.Context, repoPath, relPath string) (string, error) {
	var result strings.Builder

	staged, err := runGitDiff(ctx, repoPath, true, relPath)
	if err != nil {
		return "", fmt.Errorf("git diff --cached: %w", err)
	}
	result.WriteString(staged)

	unstaged, err := runGitDiff(ctx, repoPath, false, relPath)
	if err != nil {
		return "", fmt.Errorf("git diff: %w", err)
	}
	result.WriteString(unstaged)

	if result.Len() == 0 && !IsGitTracked(ctx, repoPath, relPath) {
		untrackedDiff, untrackedErr := runGitDiffNoIndex(ctx, repoPath, relPath)
		if untrackedErr != nil {
			return "", fmt.Errorf("git diff --no-index: %w", untrackedErr)
		}
		result.WriteString(untrackedDiff)
	}

	return result.String(), nil
}

// GetFileDiffNoRepo produces a full-file diff via git diff --no-index for
// workspaces that are known not to be git repositories. The caller is
// responsible for determining that the path is not in a git repo (e.g. via
// a cached check) — this function does not call IsGitRepo.
func GetFileDiffNoRepo(ctx context.Context, repoPath, relPath string) (string, error) {
	diff, err := runGitDiffNoIndex(ctx, repoPath, relPath)
	if err != nil {
		return "", fmt.Errorf("git diff --no-index: %w", err)
	}
	return diff, nil
}

// runGitDiff executes a git diff command and returns its output.
func runGitDiff(ctx context.Context, dir string, cached bool, relPath string) (string, error) {
	args := []string{"diff"}
	if cached {
		args = append(args, "--cached")
	}
	args = append(args, "--", relPath)

	cmd := exec.CommandContext(ctx, "git", args...)
	cmd.Dir = dir
	var stdout, stderr bytes.Buffer
	cmd.Stdout = &stdout
	cmd.Stderr = &stderr

	if err := cmd.Run(); err != nil {
		return "", fmt.Errorf("git diff: %w", err)
	}
	return stdout.String(), nil
}

// runGitDiffNoIndex produces a diff for an untracked file by comparing it
// against /dev/null. git diff --no-index exits with code 1 when differences
// exist, so we treat that as success.
func runGitDiffNoIndex(ctx context.Context, dir, relPath string) (string, error) {
	cmd := exec.CommandContext(ctx, "git", "diff", "--no-index", os.DevNull, relPath)
	cmd.Dir = dir
	var stdout bytes.Buffer
	var stderr bytes.Buffer
	cmd.Stdout = &stdout
	cmd.Stderr = &stderr

	err := cmd.Run()
	if err != nil {
		var exitErr *exec.ExitError
		if errors.As(err, &exitErr) && exitErr.ExitCode() == 1 {
			return stdout.String(), nil
		}
		return "", fmt.Errorf("git diff --no-index failed: %w", err)
	}
	return stdout.String(), nil
}

// GitIgnoredPaths returns a set of absolute paths that are ignored by git
// in the given directory. Returns (nil, nil) when the directory is not a
// git repository (no filtering) or when there are no ignored paths.
func GitIgnoredPaths(ctx context.Context, dir string) (map[string]bool, error) {
	cmd := exec.CommandContext(ctx, "git", "ls-files", "--others", "--ignored", "--exclude-standard", "--directory", "-z")
	cmd.Dir = dir
	var stdout, stderr bytes.Buffer
	cmd.Stdout = &stdout
	cmd.Stderr = &stderr

	if err := cmd.Run(); err != nil {
		if errNotGitRepo(err, stderr.String()) {
			return nil, nil //nolint:nilnil // non-git workspace: no filtering
		}
		return nil, fmt.Errorf("git ls-files failed: %w", err)
	}

	output := stdout.Bytes()
	if len(output) == 0 {
		return map[string]bool{}, nil
	}

	result := make(map[string]bool)
	for _, entry := range bytes.Split(output, []byte{'\x00'}) {
		rel := string(entry)
		if rel == "" {
			continue
		}
		rel = strings.TrimSuffix(rel, "/")
		result[filepath.Join(dir, rel)] = true
	}
	return result, nil
}
