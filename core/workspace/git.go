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
	"regexp"
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

// BuildReviewDiff returns the combined unified diff of ALL uncommitted
// changes relative to HEAD — staged and unstaged tracked changes together
// with untracked files — for a git repository, with contextLines lines of
// context per hunk. The result is suitable for ParseReviewDiff.
//
// Tracked changes come from `git diff -U{contextLines} HEAD`, which reports
// staged and unstaged changes together. That command omits untracked files
// (they have no index entry), so each untracked file — present in the work
// tree but never added to the index, and not git-ignored — is diffed against
// /dev/null via `git diff --no-index` and appended. The result is empty when
// the working tree is clean.
//
// The caller must ensure repoPath is a git repository.
func BuildReviewDiff(ctx context.Context, repoPath string, contextLines int) (string, error) {
	var result strings.Builder

	tracked, err := runGitDiffHead(ctx, repoPath, contextLines)
	if err != nil {
		return "", fmt.Errorf("git diff HEAD: %w", err)
	}
	result.WriteString(tracked)

	untracked, err := listUntrackedFiles(ctx, repoPath)
	if err != nil {
		return "", fmt.Errorf("listing untracked files: %w", err)
	}
	for _, rel := range untracked {
		d, dErr := runGitDiffNoIndex(ctx, repoPath, rel)
		if dErr != nil {
			return "", fmt.Errorf("git diff --no-index %s: %w", rel, dErr)
		}
		result.WriteString(d)
	}

	return result.String(), nil
}

// runGitDiffHead runs `git diff -U{contextLines} HEAD`, which reports both
// staged and unstaged changes to tracked files relative to HEAD in a single
// invocation. Untracked files are not included (they have no index entry).
func runGitDiffHead(ctx context.Context, dir string, contextLines int) (string, error) {
	cmd := exec.CommandContext(ctx, "git", "diff", "-U"+strconv.Itoa(contextLines), "HEAD")
	cmd.Dir = dir
	var stdout bytes.Buffer
	cmd.Stdout = &stdout
	cmd.Stderr = nil
	if err := cmd.Run(); err != nil {
		return "", fmt.Errorf("git diff HEAD: %w", err)
	}
	return stdout.String(), nil
}

// listUntrackedFiles returns the repository-relative paths of untracked
// files (present in the work tree but not in the index), respecting
// .gitignore via --exclude-standard. It runs `git ls-files --others
// --exclude-standard -z` and splits the NUL-delimited output. Individual
// files are listed rather than their containing directories.
func listUntrackedFiles(ctx context.Context, dir string) ([]string, error) {
	cmd := exec.CommandContext(ctx, "git", "ls-files", "--others", "--exclude-standard", "-z")
	cmd.Dir = dir
	var stdout bytes.Buffer
	cmd.Stdout = &stdout
	cmd.Stderr = nil
	if err := cmd.Run(); err != nil {
		return nil, fmt.Errorf("git ls-files failed: %w", err)
	}
	var files []string
	for _, entry := range bytes.Split(stdout.Bytes(), []byte{'\x00'}) {
		rel := string(entry)
		if rel == "" {
			continue
		}
		files = append(files, rel)
	}
	return files, nil
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

// reviewHunkHeaderRe matches a unified-diff hunk header, capturing the
// old/new start line and (optional) count. Counts default to 1 when git
// omits them (single-line hunk side).
var reviewHunkHeaderRe = regexp.MustCompile(`^@@ -(\d+)(?:,(\d+))? \+(\d+)(?:,(\d+))? @@`)

// ParseReviewDiff parses a multi-file unified diff — such as the output of
// `git diff -U5 HEAD` — into per-file groups, each carrying its hunks.
// Files are delimited by "diff --git a/<old> b/<new>" header lines: the
// current path comes from the b/ side and OldPath is populated only for
// renames/copies where the a/ and b/ sides differ. Paths are unquoted (git
// quotes paths containing special characters when core.quotePath is set).
// Returns an empty (non-nil) slice when diff is empty. The context-line
// count of each hunk is whatever git emitted (controlled by the caller's
// -U flag), not enforced here.
func ParseReviewDiff(diff string) []ReviewFileDiff {
	diff = strings.TrimSpace(diff)
	if diff == "" {
		return []ReviewFileDiff{}
	}

	lines := strings.Split(diff, "\n")
	// Locate the start line of each file block. A real "diff --git "
	// header has no leading space, which distinguishes it from an
	// identical-looking context line inside a hunk body (" diff --git ").
	var blockStarts []int
	for i, line := range lines {
		if strings.HasPrefix(line, "diff --git ") {
			blockStarts = append(blockStarts, i)
		}
	}

	files := make([]ReviewFileDiff, 0, len(blockStarts))
	for idx, start := range blockStarts {
		end := len(lines)
		if idx+1 < len(blockStarts) {
			end = blockStarts[idx+1]
		}
		files = append(files, parseReviewFileBlock(lines[start:end]))
	}
	return files
}

// parseReviewFileBlock parses a single file's diff block (the "diff --git"
// header line plus its preamble and hunks) into a ReviewFileDiff.
func parseReviewFileBlock(lines []string) ReviewFileDiff {
	file := ReviewFileDiff{}
	if len(lines) > 0 {
		oldPath, newPath := parseDiffGitHeader(lines[0])
		file.Path = newPath
		if oldPath != "" && oldPath != newPath {
			file.OldPath = oldPath
		}
	}

	var hunks []ReviewHunk
	i := 0
	for i < len(lines) {
		if !strings.HasPrefix(lines[i], "@@") {
			i++
			continue
		}
		m := reviewHunkHeaderRe.FindStringSubmatch(lines[i])
		if m == nil {
			i++
			continue
		}
		oldStart, _ := strconv.Atoi(m[1])
		oldCount := 1
		if m[2] != "" {
			oldCount, _ = strconv.Atoi(m[2])
		}
		newStart, _ := strconv.Atoi(m[3])
		newCount := 1
		if m[4] != "" {
			newCount, _ = strconv.Atoi(m[4])
		}

		start := i
		i++
		for i < len(lines) && !strings.HasPrefix(lines[i], "@@") {
			i++
		}
		hunks = append(hunks, ReviewHunk{
			Raw:      strings.Join(lines[start:i], "\n"),
			OldStart: oldStart,
			OldCount: oldCount,
			NewStart: newStart,
			NewCount: newCount,
		})
	}
	file.Hunks = hunks
	return file
}

// parseDiffGitHeader extracts the old (a/) and new (b/) paths from a
// "diff --git a/<old> b/<new>" header line, stripping the a//b/ prefixes
// and unquoting git-quoted paths. Returns empty strings when the line does
// not conform to the expected shape.
func parseDiffGitHeader(line string) (oldPath, newPath string) {
	rest := strings.TrimSpace(strings.TrimPrefix(line, "diff --git "))
	oldRaw, after, ok := nextDiffPath(rest)
	if !ok {
		return "", ""
	}
	newRaw, _, ok := nextDiffPath(after)
	if !ok {
		return stripDiffPrefix(oldRaw), ""
	}
	return stripDiffPrefix(oldRaw), stripDiffPrefix(newRaw)
}

// nextDiffPath extracts the first path token from s: either a double-quoted
// string (git escapes special characters when core.quotePath is set) or an
// unquoted run up to the next space. It returns the unquoted token, the
// remaining string, and whether a token was found.
func nextDiffPath(s string) (token, rest string, ok bool) {
	s = strings.TrimLeft(s, " ")
	if s == "" {
		return "", "", false
	}
	if s[0] == '"' {
		for i := 1; i < len(s); i++ {
			if s[i] == '\\' {
				i++ // skip the escaped character
				continue
			}
			if s[i] == '"' {
				return unquoteGitPath(s[:i+1]), strings.TrimLeft(s[i+1:], " "), true
			}
		}
		return unquoteGitPath(s), "", true
	}
	if idx := strings.IndexByte(s, ' '); idx >= 0 {
		return s[:idx], strings.TrimLeft(s[idx+1:], " "), true
	}
	return s, "", true
}

// stripDiffPrefix removes the leading "a/" or "b/" prefix that git adds to
// paths in the "diff --git" header.
func stripDiffPrefix(p string) string {
	p = strings.TrimPrefix(p, "a/")
	p = strings.TrimPrefix(p, "b/")
	return p
}
