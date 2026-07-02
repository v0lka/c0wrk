package backend

import (
	"bytes"
	"context"
	"errors"
	"fmt"
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

// GetCurrentBranch returns the name of the currently checked-out branch.
// Returns "HEAD" when in detached HEAD state. Returns an error when no
// project is active, the project is No Project, or the git command
// fails.
func (f *FrontendAPI) GetCurrentBranch() (string, error) {
	repoPath, err := f.resolveGitRepoRoot()
	if err != nil {
		return "", err
	}

	out, err := f.runGitCmd(repoPath, "rev-parse", "--abbrev-ref", "HEAD")
	if err != nil {
		return "", err
	}
	return strings.TrimSpace(out), nil
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
