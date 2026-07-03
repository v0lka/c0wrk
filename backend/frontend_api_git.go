package backend

import (
	"bytes"
	"context"
	"errors"
	"fmt"
	"io/fs"
	"os"
	"os/exec"
	"path/filepath"
	"regexp"
	"strconv"
	"strings"
	"time"

	"github.com/v0lka/c0wrk/backend/project"
	"github.com/v0lka/c0wrk/sdk/pathutil"
)

const gitCmdTimeout = 30 * time.Second

// ---------------------------------------------------------------------------
// Git staging RPCs
// ---------------------------------------------------------------------------

// StageFile stages a file relative to the active project's git repository
// root (git add <path>). The path is resolved against the active project
// workspace. Returns an error when no project is active, the project is
// No Project, the path is outside the workspace, or the git command fails.
func (f *FrontendAPI) StageFile(path string) error {
	repoPath, relPath, err := f.resolveGitPath(path)
	if err != nil {
		return err
	}
	if _, err := f.runGitCmd(repoPath, "add", "--", relPath); err != nil {
		return err
	}
	f.emitGitStatusChanged(repoPath)
	return nil
}

// UnstageFile unstages a file relative to the active project's git
// repository root (git reset HEAD <path>). The path is resolved against
// the active project workspace. Emits git:status_changed on success.
// Returns an error when no project is active, the project is No Project,
// the path is outside the workspace, or the git command fails.
func (f *FrontendAPI) UnstageFile(path string) error {
	repoPath, relPath, err := f.resolveGitPath(path)
	if err != nil {
		return err
	}
	if _, err := f.runGitCmd(repoPath, "reset", "HEAD", "--", relPath); err != nil {
		return err
	}
	f.emitGitStatusChanged(repoPath)
	return nil
}

// StageAll stages all changes in the active project's git repository
// (git add -A). Emits git:status_changed on success. Returns an error
// when no project is active, the project is No Project, or the git
// command fails.
func (f *FrontendAPI) StageAll() error {
	repoPath, err := f.resolveGitRepoRoot()
	if err != nil {
		return err
	}
	if _, err := f.runGitCmd(repoPath, "add", "-A"); err != nil {
		return err
	}
	f.emitGitStatusChanged(repoPath)
	return nil
}

// UnstageAll unstages all changes in the active project's git repository
// (git reset HEAD). Emits git:status_changed on success. Returns an error
// when no project is active, the project is No Project, or the git
// command fails.
func (f *FrontendAPI) UnstageAll() error {
	repoPath, err := f.resolveGitRepoRoot()
	if err != nil {
		return err
	}
	if _, err := f.runGitCmd(repoPath, "reset", "HEAD"); err != nil {
		return err
	}
	f.emitGitStatusChanged(repoPath)
	return nil
}

// ---------------------------------------------------------------------------
// DiffStat RPC
// ---------------------------------------------------------------------------

// GetDiffStat returns added and deleted line counts for uncommitted
// changes on a file (git diff --numstat <path>). The path is resolved
// against the active project workspace. Returns an error when no project
// is active, the project is No Project, the path is outside the
// workspace, or the git command fails.
func (f *FrontendAPI) GetDiffStat(path string) (*DiffStat, error) {
	repoPath, relPath, err := f.resolveGitPath(path)
	if err != nil {
		return nil, err
	}

	ctx, cancel := context.WithTimeout(f.ctx(), gitCmdTimeout)
	defer cancel()

	cmd := exec.CommandContext(ctx, "git", "diff", "--numstat", "--", relPath)
	cmd.Dir = repoPath
	var stdout, stderr bytes.Buffer
	cmd.Stdout = &stdout
	cmd.Stderr = &stderr

	if err := cmd.Run(); err != nil {
		return nil, fmt.Errorf("git diff --numstat: %w", err)
	}

	return parseDiffStat(stdout.String())
}

// parseDiffStat parses the output of git diff --numstat.
// Format: <added>\t<deleted>\t<path>
// Lines in binary files are counted as "-".
func parseDiffStat(output string) (*DiffStat, error) {
	output = strings.TrimSpace(output)
	if output == "" {
		return &DiffStat{}, nil
	}
	fields := strings.SplitN(output, "\t", 3)
	if len(fields) < 2 {
		return &DiffStat{}, nil
	}
	added, err := parseNumstatField(fields[0])
	if err != nil {
		return nil, fmt.Errorf("parsing added lines: %w", err)
	}
	deleted, err := parseNumstatField(fields[1])
	if err != nil {
		return nil, fmt.Errorf("parsing deleted lines: %w", err)
	}
	return &DiffStat{Added: added, Deleted: deleted}, nil
}

// parseNumstatField parses a single numstat count field. Returns 0 for
// "-" (binary file indicator).
func parseNumstatField(s string) (int, error) {
	if s == "-" {
		return 0, nil
	}
	n, err := strconv.Atoi(s)
	if err != nil {
		return 0, err
	}
	return n, nil
}

// ---------------------------------------------------------------------------
// shared helpers
// ---------------------------------------------------------------------------

// resolveGitPath validates that path is inside the active project
// workspace (but not No Project), returns the git repository root and the
// relative path from the repo root to the given path.
func (f *FrontendAPI) resolveGitPath(path string) (repoPath, relPath string, err error) {
	repoPath, err = f.resolveGitRepoRoot()
	if err != nil {
		return "", "", err
	}
	absPath, err := filepath.Abs(path)
	if err != nil {
		return "", "", fmt.Errorf("invalid path: %w", err)
	}
	relPath, err = filepath.Rel(repoPath, absPath)
	if err != nil {
		return "", "", fmt.Errorf("failed to compute relative path: %w", err)
	}
	ok, err := pathutil.IsWithinPath(repoPath, absPath)
	if err != nil {
		return "", "", fmt.Errorf("path containment check failed: %w", err)
	}
	if !ok {
		return "", "", errors.New("path outside project workspace")
	}
	return repoPath, relPath, nil
}

// resolveGitRepoRoot returns the active project's workspace path after
// validating that a project is active and is not No Project.
func (f *FrontendAPI) resolveGitRepoRoot() (string, error) {
	f.activeProjectMu.RLock()
	projectPath := f.activeProjectPath
	projectID := f.activeProjectID
	f.activeProjectMu.RUnlock()

	if projectPath == "" {
		return "", errors.New("no active project")
	}
	if projectID == project.NoProjectID {
		return "", errors.New("no git operations in No Project mode")
	}
	return projectPath, nil
}

// runGitCmd executes a git sub-command in the given repository directory
// with a 30s timeout and returns stdout. Returns an error when no
// arguments are provided, or when the command fails.
func (f *FrontendAPI) runGitCmd(dir string, args ...string) (string, error) {
	if len(args) == 0 {
		return "", errors.New("runGitCmd: no arguments")
	}

	ctx, cancel := context.WithTimeout(f.ctx(), gitCmdTimeout)
	defer cancel()

	cmd := exec.CommandContext(ctx, "git", args...)
	cmd.Dir = dir

	out, err := cmd.Output()
	if err != nil {
		// Surface git's stderr so callers can match on specific failure
		// messages (e.g. "already exists", "local changes would be
		// overwritten"). cmd.Output captures stderr into *exec.ExitError.
		var exitErr *exec.ExitError
		if errors.As(err, &exitErr) && len(exitErr.Stderr) > 0 {
			return "", fmt.Errorf("git: %w: %s", err, strings.TrimSpace(string(exitErr.Stderr)))
		}
		return "", fmt.Errorf("git: %w", err)
	}
	return string(out), nil
}

// runGitCmdCombined executes a git sub-command in the given repository
// directory and returns the combined stdout+stderr output. Unlike
// runGitCmd, which only returns stdout, this helper preserves stderr
// because git writes progress and informational messages to stderr even
// on success (notably pull/push/fetch). The timeout is caller-supplied
// so long-running remote operations can opt into a larger budget. On
// failure the partial output is still returned alongside the wrapped
// error so the UI can display whatever git printed. Returns an error
// when no arguments are provided or the command fails.
func (f *FrontendAPI) runGitCmdCombined(dir string, timeout time.Duration, args ...string) (string, error) {
	if len(args) == 0 {
		return "", errors.New("runGitCmdCombined: no arguments")
	}

	ctx, cancel := context.WithTimeout(f.ctx(), timeout)
	defer cancel()

	cmd := exec.CommandContext(ctx, "git", args...)
	cmd.Dir = dir

	// Capture stdout and stderr separately (each stream gets its own copy
	// goroutine) and concatenate them; routing both into a single
	// bytes.Buffer would race. stderr typically carries git's progress and
	// informational messages, which we surface to the UI.
	var stdout, stderr bytes.Buffer
	cmd.Stdout = &stdout
	cmd.Stderr = &stderr

	if err := cmd.Run(); err != nil {
		return combinedGitOutput(stdout.String(), stderr.String()), fmt.Errorf("git: %w", err)
	}
	return combinedGitOutput(stdout.String(), stderr.String()), nil
}

// combinedGitOutput joins stdout and stderr (stdout first) and trims
// surrounding whitespace, for display of remote git command output.
func combinedGitOutput(stdout, stderr string) string {
	out := strings.TrimSpace(stdout)
	if s := strings.TrimSpace(stderr); s != "" {
		if out != "" {
			out += "\n"
		}
		out += s
	}
	return out
}

// emitGitStatusChanged emits the git:status_changed event to the
// frontend with the repository path as payload so the frontend knows
// which project was affected.
func (f *FrontendAPI) emitGitStatusChanged(repoPath string) {
	if f.emitEvent != nil {
		f.emitEvent(EventGitStatusChanged, repoPath)
	}
}

// ---------------------------------------------------------------------------
// Commit RPC
// ---------------------------------------------------------------------------

// commitMsgRe validates that the commit message is not empty or
// whitespace-only.
var commitMsgRe = regexp.MustCompile(`\S`)

// Commit creates a git commit with the given message at the active
// project's repository root. The message must be non-empty. Emits
// git:status_changed on success. Returns an error when no project is
// active, the project is No Project, the message is empty, there is
// nothing to commit, or the git command fails.
func (f *FrontendAPI) Commit(message string) error {
	if !commitMsgRe.MatchString(message) {
		return errors.New("commit message must not be empty")
	}

	repoPath, err := f.resolveGitRepoRoot()
	if err != nil {
		return err
	}

	if _, err := f.runGitCmd(repoPath, "commit", "-m", message); err != nil {
		return err
	}
	f.emitGitStatusChanged(repoPath)
	return nil
}

// ---------------------------------------------------------------------------
// Branch RPCs
// ---------------------------------------------------------------------------

// GetBranches returns all local branches with their current-state flag.
// Returns an empty slice (not nil) when no branches exist. Returns an
// error when no project is active, the project is No Project, or the git
// command fails.
func (f *FrontendAPI) GetBranches() ([]Branch, error) {
	repoPath, err := f.resolveGitRepoRoot()
	if err != nil {
		return nil, err
	}

	// --format: %(refname:short)<NUL>%(HEAD)<NUL>
	// Each branch is separated by LF, fields within a branch by NUL.
	out, err := f.runGitCmd(repoPath, "for-each-ref", "refs/heads/",
		"--format=%(refname:short)%00%(HEAD)%00")
	if err != nil {
		return nil, err
	}

	out = strings.TrimSpace(out)
	if out == "" {
		return []Branch{}, nil
	}

	lines := strings.Split(out, "\n")
	branches := make([]Branch, 0, len(lines))
	for _, line := range lines {
		fields := strings.Split(line, "\x00")
		if len(fields) < 2 {
			continue
		}
		branches = append(branches, Branch{
			Name:      fields[0],
			IsCurrent: fields[1] == "*",
		})
	}
	return branches, nil
}

// GetCurrentBranch returns information about the currently checked-out
// branch: its name, configured upstream, and ahead/behind counts relative
// to that upstream. Name is "HEAD" in detached HEAD state. Upstream is
// empty and Ahead/Behind are zero when no upstream is configured or in
// detached HEAD. Returns an error when no project is active, the project
// is No Project, or the git command fails.
func (f *FrontendAPI) GetCurrentBranch() (BranchInfo, error) {
	repoPath, err := f.resolveGitRepoRoot()
	if err != nil {
		return BranchInfo{}, err
	}

	out, err := f.runGitCmd(repoPath, "rev-parse", "--abbrev-ref", "HEAD")
	if err != nil {
		return BranchInfo{}, err
	}
	name := strings.TrimSpace(out)

	info := BranchInfo{Name: name}

	// Upstream and ahead/behind only make sense on a real branch (not
	// detached HEAD). @{upstream} does not resolve in detached HEAD or
	// when no tracking branch is configured; treat both as no-upstream.
	if name == "" || name == "HEAD" {
		return info, nil
	}

	upstream, err := f.runGitCmd(repoPath, "rev-parse", "--abbrev-ref", "@{upstream}")
	if err != nil {
		// No upstream configured — return the branch name with zero counts.
		return info, nil //nolint:nilerr // no upstream configured: partial info, not an error
	}
	info.Upstream = strings.TrimSpace(upstream)

	// git rev-list --count --left-right @{upstream}...HEAD
	// Output: "<behind>\t<ahead>" — left = upstream-only, right = HEAD-only.
	cnt, err := f.runGitCmd(repoPath, "rev-list", "--count", "--left-right", "@{upstream}...HEAD")
	if err != nil {
		// Upstream ref may not resolve locally yet; return what we have.
		return info, nil //nolint:nilerr // upstream ref unresolved locally: return partial info
	}
	fields := strings.Split(strings.TrimSpace(cnt), "\t")
	if len(fields) == 2 {
		info.Behind, _ = strconv.Atoi(fields[0])
		info.Ahead, _ = strconv.Atoi(fields[1])
	}

	return info, nil
}

// ---------------------------------------------------------------------------
// Branch management RPCs (Phase 4)
// ---------------------------------------------------------------------------

// CheckoutBranch switches the active project's repository to the named
// local branch (git checkout <name>). Emits git:status_changed on
// success. Returns an error when no project is active, the project is
// No Project, the branch name is empty, or the git command fails (for
// example, when local changes would be overwritten by the checkout).
func (f *FrontendAPI) CheckoutBranch(name string) error {
	branchName := strings.TrimSpace(name)
	if branchName == "" {
		return errors.New("branch name must not be empty")
	}

	repoPath, err := f.resolveGitRepoRoot()
	if err != nil {
		return err
	}

	if _, err := f.runGitCmd(repoPath, "checkout", branchName); err != nil {
		if isLocalChangesOverwritten(err) {
			return errors.New("cannot switch branch: local changes would be overwritten. Commit or stash your changes first")
		}
		return err
	}
	f.emitGitStatusChanged(repoPath)
	return nil
}

// CreateBranch creates a new branch from the current HEAD and checks it
// out (git checkout -b <name>). Emits git:status_changed on success.
// Returns an error when no project is active, the project is No Project,
// the branch name is empty, the branch already exists, or the git
// command fails.
func (f *FrontendAPI) CreateBranch(name string) error {
	branchName := strings.TrimSpace(name)
	if branchName == "" {
		return errors.New("branch name must not be empty")
	}

	repoPath, err := f.resolveGitRepoRoot()
	if err != nil {
		return err
	}

	if _, err := f.runGitCmd(repoPath, "checkout", "-b", branchName); err != nil {
		if isBranchAlreadyExists(err) {
			return fmt.Errorf("branch %q already exists", branchName)
		}
		return err
	}
	f.emitGitStatusChanged(repoPath)
	return nil
}

// isLocalChangesOverwritten reports whether err is a git checkout failure
// caused by uncommitted/untracked changes that would be overwritten. git
// emits variants like "Your local changes ... would be overwritten by
// checkout" (tracked) and "untracked working tree files would be
// overwritten by checkout" (untracked) on stderr (captured by runGitCmd
// via cmd.Output's *exec.ExitError).
func isLocalChangesOverwritten(err error) bool {
	if err == nil {
		return false
	}
	return strings.Contains(err.Error(), "would be overwritten")
}

// isBranchAlreadyExists reports whether err is a git checkout -b failure
// because the branch name is already taken.
func isBranchAlreadyExists(err error) bool {
	if err == nil {
		return false
	}
	return strings.Contains(err.Error(), "already exists")
}

// ---------------------------------------------------------------------------
// AI commit message RPC (Phase 4)
// ---------------------------------------------------------------------------

// commitMsgGenTimeout caps the LLM request used to generate a commit
// message so the UI does not hang on a slow or unresponsive provider.
const commitMsgGenTimeout = 15 * time.Second

// GenerateCommitMessage asks the configured LLM to produce a Conventional
// Commits-formatted commit message from the given staged diff (typically
// the output of `git diff --staged`). The LLM request is bounded by a
// 15-second timeout. Returns an error when the application is not
// initialised, the diff is empty, or the LLM call fails.
func (f *FrontendAPI) GenerateCommitMessage(diff string) (string, error) {
	b := f.builder()
	if b == nil {
		return "", errors.New("application not initialized")
	}

	trimmed := strings.TrimSpace(diff)
	if trimmed == "" {
		return "", errors.New("no staged changes to generate a commit message from")
	}

	ctx, cancel := context.WithTimeout(f.ctx(), commitMsgGenTimeout)
	defer cancel()

	return b.GenerateCommitMessage(ctx, trimmed)
}

// ---------------------------------------------------------------------------
// Remote operations RPCs (Phase 5)
// ---------------------------------------------------------------------------

// remoteGitCmdTimeout bounds pull/push/fetch, which can take noticeably
// longer than local git commands over a slow network.
const remoteGitCmdTimeout = 2 * time.Minute

// Pull fetches from and integrates the named remote into the current
// branch (git pull <remote>). When remote is empty, git uses the
// configured upstream. The combined stdout+stderr output is returned for
// display in the UI. Parallel remote operations are serialized via
// remoteOpMu. Emits git:status_changed on completion. Returns an error
// when no project is active, the project is No Project, or the git
// command fails.
func (f *FrontendAPI) Pull(remote string) (string, error) {
	return f.runRemoteOp("pull", remote)
}

// Push sends local commits to the named remote (git push <remote>). When
// remote is empty, git uses the configured upstream. The combined
// stdout+stderr output is returned for display in the UI. Parallel remote
// operations are serialized via remoteOpMu. Emits git:status_changed on
// completion. Returns an error when no project is active, the project is
// No Project, or the git command fails.
func (f *FrontendAPI) Push(remote string) (string, error) {
	return f.runRemoteOp("push", remote)
}

// Fetch downloads objects and refs from the named remote without merging
// (git fetch <remote>). When remote is empty, git uses the configured
// upstream. The combined stdout+stderr output is returned for display in
// the UI. Parallel remote operations are serialized via remoteOpMu.
// Emits git:status_changed on completion so the frontend can refresh
// ahead/behind indicators. Returns an error when no project is active,
// the project is No Project, or the git command fails.
func (f *FrontendAPI) Fetch(remote string) (string, error) {
	return f.runRemoteOp("fetch", remote)
}

// runRemoteOp executes a serialized remote git operation (pull/push/
// fetch) and returns its combined output. It is the shared body of Pull,
// Push and Fetch. It is intentionally unexported so it is not exposed as
// a Wails RPC method.
func (f *FrontendAPI) runRemoteOp(op, remote string) (string, error) {
	repoPath, err := f.resolveGitRepoRoot()
	if err != nil {
		return "", err
	}

	args := []string{op}
	if r := strings.TrimSpace(remote); r != "" {
		args = append(args, r)
	}

	f.log().Debug("git remote operation", "op", op, "remote", remote)

	// Serialize remote ops so only one network operation runs at a time.
	f.remoteOpMu.Lock()
	defer f.remoteOpMu.Unlock()

	out, err := f.runGitCmdCombined(repoPath, remoteGitCmdTimeout, args...)
	if err != nil {
		f.log().Warn("git remote operation failed", "op", op, "remote", remote, "error", err)
		return out, err
	}
	f.emitGitStatusChanged(repoPath)
	return out, nil
}

// ---------------------------------------------------------------------------
// Commit history RPCs (Phase 5)
// ---------------------------------------------------------------------------

// defaultCommitLogLimit is the page size used by GetCommitLog when the
// caller does not request a positive limit.
const defaultCommitLogLimit = 50

// GetCommitLog returns a page of commit history from the active
// project's repository. limit caps the number of commits returned; skip
// offsets into the history for pagination ("Load more"). A non-positive
// limit defaults to defaultCommitLogLimit; a negative skip is treated as
// zero. Returns an empty slice (not nil) when there are no commits.
// Returns an error when no project is active, the project is No Project,
// or the git command fails.
func (f *FrontendAPI) GetCommitLog(limit, skip int) ([]CommitInfo, error) {
	if limit <= 0 {
		limit = defaultCommitLogLimit
	}
	if skip < 0 {
		skip = 0
	}

	repoPath, err := f.resolveGitRepoRoot()
	if err != nil {
		return nil, err
	}

	// Pretty format using control characters as separators so commit
	// messages containing "|" or newlines do not corrupt parsing:
	// %x1f (unit separator) between fields, %x1e (record separator)
	// between commits.
	out, err := f.runGitCmd(repoPath, "log",
		"-n", strconv.Itoa(limit),
		"--skip", strconv.Itoa(skip),
		"--format=%H%x1f%an%x1f%ae%x1f%ad%x1f%s%x1e",
	)
	if err != nil {
		return nil, err
	}

	return parseCommitLog(out), nil
}

// parseCommitLog parses git log output produced with the
// --format=%H%x1f%an%x1f%ae%x1f%ad%x1f%s%x1e pretty format. Records are
// separated by %x1e (record separator) and fields within a record by
// %x1f (unit separator). Returns an empty slice when output is empty.
func parseCommitLog(output string) []CommitInfo {
	output = strings.TrimSpace(output)
	if output == "" {
		return []CommitInfo{}
	}
	records := strings.Split(output, "\x1e")
	commits := make([]CommitInfo, 0, len(records))
	for _, rec := range records {
		rec = strings.TrimSpace(rec)
		if rec == "" {
			continue
		}
		fields := strings.Split(rec, "\x1f")
		if len(fields) < 5 {
			continue
		}
		commits = append(commits, CommitInfo{
			SHA:     fields[0],
			Author:  fields[1],
			Email:   fields[2],
			Date:    fields[3],
			Message: fields[4],
		})
	}
	return commits
}

// GetCommitFiles returns the list of files changed by the given commit
// (git diff-tree --no-commit-id --name-status -r -M <sha>). -M enables
// rename detection so renames are reported as a single "R" entry rather
// than separate add/delete pairs. Each result carries the name-status
// letter and the post-commit path (the destination for renames/copies).
// Returns an empty slice (not nil) when the commit changed no files.
// Returns an error when no project is active, the project is No Project,
// the sha is empty, or the git command fails.
func (f *FrontendAPI) GetCommitFiles(sha string) ([]CommitFile, error) {
	commit := strings.TrimSpace(sha)
	if commit == "" {
		return nil, errors.New("commit sha must not be empty")
	}

	repoPath, err := f.resolveGitRepoRoot()
	if err != nil {
		return nil, err
	}

	out, err := f.runGitCmd(repoPath, "diff-tree", "--no-commit-id", "--name-status", "-r", "-M", commit)
	if err != nil {
		return nil, err
	}

	return parseCommitFiles(out), nil
}

// parseCommitFiles parses git diff-tree --name-status output. Each line
// is "<status>\t<path>" or "<status>\t<old>\t<new>" for renames (R) and
// copies (C); the destination (last field) is used for R/C. The status
// may carry a similarity score (e.g. "R100") which is normalized to its
// leading letter. Returns an empty slice when output is empty.
func parseCommitFiles(output string) []CommitFile {
	output = strings.TrimSpace(output)
	if output == "" {
		return []CommitFile{}
	}
	lines := strings.Split(output, "\n")
	files := make([]CommitFile, 0, len(lines))
	for _, line := range lines {
		line = strings.TrimSpace(line)
		if line == "" {
			continue
		}
		fields := strings.Split(line, "\t")
		if len(fields) < 2 {
			continue
		}
		status := fields[0]
		if len(status) > 1 {
			// Strip similarity score (R100 -> R, C090 -> C).
			status = string(status[0])
		}
		files = append(files, CommitFile{
			Status: status,
			Path:   fields[len(fields)-1],
		})
	}
	return files
}

// ---------------------------------------------------------------------------
// Stash RPCs (Phase 5)
// ---------------------------------------------------------------------------

// StashCreate saves the current working-tree and index changes into a
// new stash (git stash push -m <message>). When message is empty, git
// uses its default stash message. Emits git:status_changed on success.
// Returns an error when no project is active, the project is No Project,
// or the git command fails.
func (f *FrontendAPI) StashCreate(message string) error {
	repoPath, err := f.resolveGitRepoRoot()
	if err != nil {
		return err
	}

	args := []string{"stash", "push"}
	if m := strings.TrimSpace(message); m != "" {
		args = append(args, "-m", m)
	}

	f.log().Debug("git stash create", "message", message)
	if _, err := f.runGitCmd(repoPath, args...); err != nil {
		return err
	}
	f.emitGitStatusChanged(repoPath)
	return nil
}

// StashPop applies and removes the stash at the given index
// (git stash pop stash@{<index>}). Emits git:status_changed on success.
// Returns an error when no project is active, the project is No Project,
// the index is negative, or the git command fails (for example, when
// popping would overwrite local changes or the stash does not exist).
func (f *FrontendAPI) StashPop(index int) error {
	if index < 0 {
		return errors.New("stash index must not be negative")
	}

	repoPath, err := f.resolveGitRepoRoot()
	if err != nil {
		return err
	}

	stashRef := fmt.Sprintf("stash@{%d}", index)
	f.log().Debug("git stash pop", "index", index)
	if _, err := f.runGitCmd(repoPath, "stash", "pop", stashRef); err != nil {
		return err
	}
	f.emitGitStatusChanged(repoPath)
	return nil
}

// StashList returns the list of stashes in the active project's
// repository (git stash list). Each entry carries its index and message.
// Returns an empty slice (not nil) when there are no stashes. Returns an
// error when no project is active, the project is No Project, or the git
// command fails.
func (f *FrontendAPI) StashList() ([]StashEntry, error) {
	repoPath, err := f.resolveGitRepoRoot()
	if err != nil {
		return nil, err
	}

	out, err := f.runGitCmd(repoPath, "stash", "list")
	if err != nil {
		return nil, err
	}

	return parseStashList(out), nil
}

// stashListRe matches a single "git stash list" line of the form
// "stash@{<index>}: <message>".
var stashListRe = regexp.MustCompile(`^stash@\{(\d+)\}:\s*(.*)$`)

// parseStashList parses git stash list output. Each line has the form
// "stash@{<index>}: <message>". Returns an empty slice when output is
// empty.
func parseStashList(output string) []StashEntry {
	output = strings.TrimSpace(output)
	if output == "" {
		return []StashEntry{}
	}
	lines := strings.Split(output, "\n")
	entries := make([]StashEntry, 0, len(lines))
	for _, line := range lines {
		line = strings.TrimSpace(line)
		if line == "" {
			continue
		}
		m := stashListRe.FindStringSubmatch(line)
		if m == nil {
			continue
		}
		idx, err := strconv.Atoi(m[1])
		if err != nil {
			continue
		}
		entries = append(entries, StashEntry{
			Index:   idx,
			Message: m[2],
		})
	}
	return entries
}

// ---------------------------------------------------------------------------
// Context-menu & discard RPCs (Phase 6)
// ---------------------------------------------------------------------------

// DiscardChanges reverts a file to HEAD, discarding both staged and
// unstaged modifications. Tracked files are unstaged (git reset HEAD)
// then restored from HEAD (git checkout --). Untracked files ("??"
// status), which have no HEAD version to restore, are removed with
// git clean -f. Emits git:status_changed on success. Returns an error
// when no project is active, the project is No Project, the path is
// outside the workspace, or a git command fails.
func (f *FrontendAPI) DiscardChanges(path string) error {
	repoPath, relPath, err := f.resolveGitPath(path)
	if err != nil {
		return err
	}

	// Inspect the file's porcelain status to distinguish untracked files,
	// which must be cleaned rather than checked out.
	out, err := f.runGitCmd(repoPath, "status", "--porcelain", "--", relPath)
	if err != nil {
		return err
	}
	status := strings.TrimSpace(out)
	if status == "" {
		// No changes for this path — nothing to discard.
		return nil
	}

	// Untracked files ("XY" == "??") cannot be restored with reset/
	// checkout; they have no committed counterpart.
	if len(status) >= 2 && status[0] == '?' && status[1] == '?' {
		if _, err := f.runGitCmd(repoPath, "clean", "-f", "--", relPath); err != nil {
			return err
		}
		f.emitGitStatusChanged(repoPath)
		return nil
	}

	// Tracked files: drop any staged version, then restore the worktree
	// from HEAD.
	if _, err := f.runGitCmd(repoPath, "reset", "HEAD", "--", relPath); err != nil {
		return err
	}
	if _, err := f.runGitCmd(repoPath, "checkout", "--", relPath); err != nil {
		return err
	}
	f.emitGitStatusChanged(repoPath)
	return nil
}

// AppendToGitignore appends pattern to the repository-root .gitignore,
// creating the file when it does not yet exist. Existing patterns are
// not duplicated: when pattern is already present the call is a no-op.
// Emits git:status_changed on success so the frontend refreshes the
// (now possibly unignored) worktree. Returns an error when no project
// is active, the project is No Project, the pattern is empty, or the
// .gitignore cannot be read or written.
func (f *FrontendAPI) AppendToGitignore(pattern string) error {
	p := strings.TrimSpace(pattern)
	if p == "" {
		return errors.New("gitignore pattern must not be empty")
	}

	repoPath, err := f.resolveGitRepoRoot()
	if err != nil {
		return err
	}

	gitignorePath := filepath.Join(repoPath, ".gitignore")

	// Read existing content to detect duplicates; a missing file is
	// expected and treated as empty.
	data, err := os.ReadFile(gitignorePath)
	if err != nil && !errors.Is(err, fs.ErrNotExist) {
		return fmt.Errorf("read .gitignore: %w", err)
	}
	existing := string(data)
	if patternAlreadyIgnored(existing, p) {
		return nil
	}

	// Ensure the file ends with a newline before appending the pattern.
	content := existing
	if content != "" && !strings.HasSuffix(content, "\n") {
		content += "\n"
	}
	content += p + "\n"

	if err := os.WriteFile(gitignorePath, []byte(content), 0o644); err != nil {
		return fmt.Errorf("write .gitignore: %w", err)
	}

	f.emitGitStatusChanged(repoPath)
	return nil
}

// patternAlreadyIgnored reports whether pattern already appears as a
// line in content, ignoring surrounding whitespace per line.
func patternAlreadyIgnored(content, pattern string) bool {
	for _, line := range strings.Split(content, "\n") {
		if strings.TrimSpace(line) == pattern {
			return true
		}
	}
	return false
}

// ---------------------------------------------------------------------------
// Merge / rebase RPCs (Phase 6)
// ---------------------------------------------------------------------------

// Merge integrates the named branch into the current branch
// (git merge <branch>). Emits git:status_changed on success. Returns an
// error when no project is active, the project is No Project, the branch
// name is empty, or the merge fails (for example, due to conflicts).
func (f *FrontendAPI) Merge(branch string) error {
	branchName := strings.TrimSpace(branch)
	if branchName == "" {
		return errors.New("branch name must not be empty")
	}

	repoPath, err := f.resolveGitRepoRoot()
	if err != nil {
		return err
	}

	f.log().Debug("git merge", "branch", branchName)
	if _, err := f.runGitCmd(repoPath, "merge", branchName); err != nil {
		return err
	}
	f.emitGitStatusChanged(repoPath)
	return nil
}

// Rebase replays the current branch onto the named branch
// (git rebase <branch>). Emits git:status_changed on success. Returns an
// error when no project is active, the project is No Project, the branch
// name is empty, or the rebase fails (for example, due to conflicts).
func (f *FrontendAPI) Rebase(branch string) error {
	branchName := strings.TrimSpace(branch)
	if branchName == "" {
		return errors.New("branch name must not be empty")
	}

	repoPath, err := f.resolveGitRepoRoot()
	if err != nil {
		return err
	}

	f.log().Debug("git rebase", "branch", branchName)
	if _, err := f.runGitCmd(repoPath, "rebase", branchName); err != nil {
		return err
	}
	f.emitGitStatusChanged(repoPath)
	return nil
}

// AbortMerge cancels an in-progress merge (git merge --abort), restoring
// the pre-merge state. Emits git:status_changed on success. Returns an
// error when no project is active, the project is No Project, or no
// merge is in progress.
func (f *FrontendAPI) AbortMerge() error {
	repoPath, err := f.resolveGitRepoRoot()
	if err != nil {
		return err
	}

	if _, err := f.runGitCmd(repoPath, "merge", "--abort"); err != nil {
		return err
	}
	f.emitGitStatusChanged(repoPath)
	return nil
}

// AbortRebase cancels an in-progress rebase (git rebase --abort),
// restoring the pre-rebase state. Emits git:status_changed on success.
// Returns an error when no project is active, the project is No Project,
// or no rebase is in progress.
func (f *FrontendAPI) AbortRebase() error {
	repoPath, err := f.resolveGitRepoRoot()
	if err != nil {
		return err
	}

	if _, err := f.runGitCmd(repoPath, "rebase", "--abort"); err != nil {
		return err
	}
	f.emitGitStatusChanged(repoPath)
	return nil
}

// GetRebaseMergeState reports whether a merge or rebase is currently in
// progress. A merge is detected via the presence of MERGE_HEAD in the
// git directory; a rebase via either the rebase-apply or rebase-merge
// directory. The git directory is resolved with git rev-parse --git-dir
// so worktrees and GIT_DIR overrides are handled. Returns an error when
// no project is active, the project is No Project, or the git command
// fails.
func (f *FrontendAPI) GetRebaseMergeState() (MergeRebaseState, error) {
	repoPath, err := f.resolveGitRepoRoot()
	if err != nil {
		return MergeRebaseState{}, err
	}

	out, err := f.runGitCmd(repoPath, "rev-parse", "--git-dir")
	if err != nil {
		return MergeRebaseState{}, err
	}
	gitDir := strings.TrimSpace(out)
	if !filepath.IsAbs(gitDir) {
		gitDir = filepath.Join(repoPath, gitDir)
	}

	state := MergeRebaseState{}
	if _, err := os.Stat(filepath.Join(gitDir, "MERGE_HEAD")); err == nil {
		state.IsMerging = true
	}
	if isRebaseActive(gitDir) {
		state.IsRebasing = true
	}
	return state, nil
}

// isRebaseActive reports whether a rebase is in progress by checking for
// either the rebase-apply (am-style) or rebase-merge (interactive)
// directory inside the git directory.
func isRebaseActive(gitDir string) bool {
	for _, name := range []string{"rebase-apply", "rebase-merge"} {
		if info, err := os.Stat(filepath.Join(gitDir, name)); err == nil && info.IsDir() {
			return true
		}
	}
	return false
}

// ---------------------------------------------------------------------------
// Commit graph RPC (Phase 6)
// ---------------------------------------------------------------------------

// defaultGitGraphLimit is the number of commits GetGitGraph returns. The
// RPC has no pagination parameters (see roadmap), so a single capped
// batch of recent history is returned for visualization.
const defaultGitGraphLimit = 100

// GetGitGraph returns a recent slice of commit history for graph
// visualization, each carrying its parent SHAs (so the frontend computes
// lane layout) and decorated ref names. The graph ASCII output (--graph)
// is intentionally omitted: the frontend derives lanes from the parents.
// Returns an empty slice (not nil) when there are no commits. Returns an
// error when no project is active, the project is No Project, or the git
// command fails.
func (f *FrontendAPI) GetGitGraph() ([]GraphCommit, error) {
	repoPath, err := f.resolveGitRepoRoot()
	if err != nil {
		return nil, err
	}

	out, err := f.runGitCmd(repoPath, "log",
		"-n", strconv.Itoa(defaultGitGraphLimit),
		"--format=%H%x1f%P%x1f%s%x1f%d%x1e",
	)
	if err != nil {
		return nil, err
	}

	return parseGitGraph(out), nil
}

// parseGitGraph parses git log output produced with the
// --format=%H%x1f%P%x1f%s%x1f%d%x1e pretty format. Records are separated
// by %x1e (record separator) and fields within a record by %x1f (unit
// separator): SHA, parents (space-separated), subject, ref decorations.
// Returns an empty slice when output is empty.
func parseGitGraph(output string) []GraphCommit {
	output = strings.TrimSpace(output)
	if output == "" {
		return []GraphCommit{}
	}
	records := strings.Split(output, "\x1e")
	commits := make([]GraphCommit, 0, len(records))
	for _, rec := range records {
		rec = strings.TrimSpace(rec)
		if rec == "" {
			continue
		}
		fields := strings.Split(rec, "\x1f")
		if len(fields) < 4 {
			continue
		}
		var parents []string
		if p := strings.TrimSpace(fields[1]); p != "" {
			parents = strings.Fields(p)
		}
		commits = append(commits, GraphCommit{
			SHA:     fields[0],
			Parents: parents,
			Message: fields[2],
			Refs:    parseGitRefs(fields[3]),
		})
	}
	return commits
}

// parseGitRefs parses the %d decoration field, which git wraps in
// parentheses, e.g. " (HEAD -> main, tag: v1.0)". Returns an empty slice
// when there are no decorations.
func parseGitRefs(decoration string) []string {
	d := strings.TrimSpace(decoration)
	d = strings.TrimPrefix(d, "(")
	d = strings.TrimSuffix(d, ")")
	d = strings.TrimSpace(d)
	if d == "" {
		return []string{}
	}
	raw := strings.Split(d, ",")
	refs := make([]string, 0, len(raw))
	for _, r := range raw {
		if r = strings.TrimSpace(r); r != "" {
			refs = append(refs, r)
		}
	}
	return refs
}

// ---------------------------------------------------------------------------
// Hunk-level staging RPC (Phase 6)
// ---------------------------------------------------------------------------

// hunkHeaderRe matches a unified-diff hunk header, capturing the old
// and new start lines and (optional) counts. Omitted counts default to
// 1 per the unified-diff specification.
var hunkHeaderRe = regexp.MustCompile(`^@@ -(\d+)(?:,(\d+))? \+(\d+)(?:,(\d+))? @@`)

// StageHunks stages the selected hunks of a file's unstaged changes into
// the index. The diff is read with git diff -- <path> (index vs worktree),
// the requested hunks are extracted by their old-file line ranges, and
// the resulting patch is applied to the index with git apply --cached.
// HunkRange is in old-file coordinates: StartLine is the hunk's old
// start line and EndLine is StartLine + oldLineCount - 1. Emits
// git:status_changed on success. Returns an error when no project is
// active, the project is No Project, the path is outside the workspace,
// there are no unstaged changes, no hunks match the requested ranges, or
// the patch cannot be applied.
func (f *FrontendAPI) StageHunks(path string, hunks []HunkRange) error {
	if len(hunks) == 0 {
		return nil
	}

	repoPath, relPath, err := f.resolveGitPath(path)
	if err != nil {
		return err
	}

	// Work-tree changes not yet staged: index vs worktree.
	diffOut, err := f.runGitCmd(repoPath, "diff", "--", relPath)
	if err != nil {
		return err
	}
	diffOut = strings.TrimSpace(diffOut)
	if diffOut == "" {
		return errors.New("no unstaged changes to stage hunks from")
	}

	patch, selected, err := buildHunkPatch(diffOut, hunks)
	if err != nil {
		return err
	}
	if selected == 0 {
		return errors.New("no hunks matched the requested ranges")
	}

	// Persist the patch to a temp file and apply it to the index.
	tmp, err := os.CreateTemp("", "c0wrk-hunk-*.patch")
	if err != nil {
		return fmt.Errorf("create temp patch file: %w", err)
	}
	tmpPath := tmp.Name()
	defer func() { _ = os.Remove(tmpPath) }()

	if _, err := tmp.WriteString(patch); err != nil {
		_ = tmp.Close()
		return fmt.Errorf("write temp patch file: %w", err)
	}
	if err := tmp.Close(); err != nil {
		return fmt.Errorf("close temp patch file: %w", err)
	}

	if _, err := f.runGitCmd(repoPath, "apply", "--cached", tmpPath); err != nil {
		return err
	}
	f.emitGitStatusChanged(repoPath)
	return nil
}

// buildHunkPatch extracts the hunks of diff whose old-file line range
// matches one of ranges and assembles them into a single unified-diff
// patch (file header + selected hunk blocks). It returns the patch text,
// the number of hunks selected, and any parse error.
func buildHunkPatch(diff string, ranges []HunkRange) (patch string, selected int, err error) {
	lines := strings.Split(diff, "\n")

	var header, patchBuf strings.Builder
	headerDone := false

	i := 0
	for i < len(lines) {
		// Lines before the first "@@" hunk header form the file header.
		if !headerDone {
			if strings.HasPrefix(lines[i], "@@") {
				headerDone = true
				patchBuf.WriteString(header.String())
			} else {
				header.WriteString(lines[i])
				header.WriteString("\n")
				i++
				continue
			}
		}

		// At this point lines[i] starts a hunk header.
		m := hunkHeaderRe.FindStringSubmatch(lines[i])
		if m == nil {
			i++
			continue
		}
		oldStart, _ := strconv.Atoi(m[1])
		oldCount := 1
		if m[2] != "" {
			oldCount, _ = strconv.Atoi(m[2])
		}

		// Collect the full hunk block: header + body up to the next hunk
		// header or end of diff.
		blockStart := i
		i++
		for i < len(lines) && !strings.HasPrefix(lines[i], "@@") {
			i++
		}
		block := strings.Join(lines[blockStart:i], "\n")

		if hunkInRange(oldStart, oldCount, ranges) {
			patchBuf.WriteString(block)
			patchBuf.WriteString("\n")
			selected++
		}
	}

	return patchBuf.String(), selected, nil
}

// hunkInRange reports whether the hunk with the given old-file start line
// and line count matches one of the requested ranges. A hunk matches when
// both its start line and end line (start + count - 1) equal a range,
// keeping the comparison consistent with the frontend's HunkRange
// derivation (see HunkRange docs).
func hunkInRange(oldStart, oldCount int, ranges []HunkRange) bool {
	hunkEnd := oldStart + oldCount - 1
	for _, r := range ranges {
		if oldStart == r.StartLine && hunkEnd == r.EndLine {
			return true
		}
	}
	return false
}
