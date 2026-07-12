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
	"github.com/v0lka/sp4rk/pathutil"
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

// GetDiffStats returns added and deleted line counts for every file with
// uncommitted changes in the active project's repository, in a single
// `git diff --numstat HEAD` call (covering both staged and unstaged
// changes relative to HEAD). The result maps absolute file paths —
// filepath.Join(repoRoot, numstatPath), matching the GitStatus key
// convention so the frontend can enrich status entries by direct key
// equality — to their DiffStat. Binary files report zero added/deleted
// (their "-" numstat fields). Untracked files are not included (git diff
// does not report them). Returns an empty map (not nil) when the working
// tree is clean. Returns an error when no project is active, the project
// is No Project, or the git command fails.
func (f *FrontendAPI) GetDiffStats() (map[string]DiffStat, error) {
	repoPath, err := f.resolveGitRepoRoot()
	if err != nil {
		return nil, err
	}

	out, err := f.runGitCmd(repoPath, "diff", "--numstat", "HEAD")
	if err != nil {
		return nil, err
	}

	return parseDiffStats(out, repoPath), nil
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

// parseDiffStats parses the output of `git diff --numstat HEAD` into a
// map keyed by absolute path (filepath.Join(repoPath, rawPath)), matching
// the GitStatus key convention. Each line is
// "<added>\t<deleted>\t<path>"; binary files use "-" for their counts and
// map to zero. Paths are kept exactly as git emits them (quoted for
// special characters) to stay consistent with GitStatus keys. Lines that
// do not parse are skipped. Returns an empty (non-nil) map when output is
// empty.
func parseDiffStats(output, repoPath string) map[string]DiffStat {
	stats := make(map[string]DiffStat)
	output = strings.TrimSpace(output)
	if output == "" {
		return stats
	}
	for _, line := range strings.Split(output, "\n") {
		line = strings.TrimSpace(line)
		if line == "" {
			continue
		}
		fields := strings.SplitN(line, "\t", 3)
		if len(fields) < 3 {
			continue
		}
		added, err := parseNumstatField(fields[0])
		if err != nil {
			continue
		}
		deleted, err := parseNumstatField(fields[1])
		if err != nil {
			continue
		}
		absPath := filepath.Join(repoPath, unquoteGitPath(fields[2]))
		stats[absPath] = DiffStat{Added: added, Deleted: deleted}
	}
	return stats
}

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
// project's repository root and returns the SHA of the newly created
// commit. The message must be non-empty and is passed to git as a
// separate argv element (never interpolated into the command line) to
// prevent shell injection. Emits git:status_changed on success. Returns
// the new commit's 40-character SHA, or an error when no project is
// active, the project is No Project, the message is empty, there is
// nothing to commit, or a git command fails.
func (f *FrontendAPI) Commit(message string) (string, error) {
	if !commitMsgRe.MatchString(message) {
		return "", errors.New("commit message must not be empty")
	}

	repoPath, err := f.resolveGitRepoRoot()
	if err != nil {
		return "", err
	}

	if _, err := f.runGitCmd(repoPath, "commit", "-m", message); err != nil {
		return "", err
	}

	// Resolve the SHA of the commit just created so the frontend can
	// display it. git rev-parse HEAD yields the 40-character commit SHA.
	sha, err := f.runGitCmd(repoPath, "rev-parse", "HEAD")
	if err != nil {
		return "", fmt.Errorf("resolve new commit SHA: %w", err)
	}
	sha = strings.TrimSpace(sha)

	f.emitGitStatusChanged(repoPath)
	return sha, nil
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

// GetBranchBases returns all refs that can serve as a start-point for
// CreateBranch: local branches, remote-tracking branches, tags, and the
// 20 most recent commits. The currently checked-out branch is excluded
// from the local list (it is the default HEAD base). Remote symbolic
// refs (e.g. origin/HEAD) are skipped. Results are ordered local →
// remote → tag → commit, matching for-each-ref's refname sort order.
// Returns an empty slice (not nil) when no refs exist. Returns an error
// when no project is active, the project is No Project, or a git command
// fails.
func (f *FrontendAPI) GetBranchBases() ([]BranchBase, error) {
	repoPath, err := f.resolveGitRepoRoot()
	if err != nil {
		return nil, err
	}

	// Collect refs (local branches, remote-tracking branches, tags) via
	// for-each-ref. Format: short-name<NUL>full-refname<NUL>HEAD-marker<NUL>
	out, err := f.runGitCmd(repoPath, "for-each-ref",
		"refs/heads/", "refs/remotes/", "refs/tags/",
		"--format=%(refname:short)%00%(refname)%00%(HEAD)%00",
	)
	if err != nil {
		return nil, err
	}

	var bases []BranchBase
	out = strings.TrimSpace(out)
	if out != "" {
		for _, line := range strings.Split(out, "\n") {
			fields := strings.Split(line, "\x00")
			if len(fields) < 3 {
				continue
			}
			shortName := fields[0]
			fullRef := fields[1]
			isCurrent := fields[2] == "*"

			var refType string
			switch {
			case strings.HasPrefix(fullRef, "refs/heads/"):
				refType = "local"
				// Exclude the currently checked-out branch — it is the
				// default HEAD base and would be redundant in the list.
				if isCurrent {
					continue
				}
			case strings.HasPrefix(fullRef, "refs/remotes/"):
				refType = "remote"
				// Skip symbolic refs like origin/HEAD — not useful as a
				// start-point.
				if strings.HasSuffix(shortName, "/HEAD") {
					continue
				}
			case strings.HasPrefix(fullRef, "refs/tags/"):
				refType = "tag"
			default:
				continue
			}

			bases = append(bases, BranchBase{
				Ref:   shortName,
				Label: shortName,
				Type:  refType,
			})
		}
	}

	// Recent commits: short SHA + subject. In a fresh repo with no
	// commits git log fails — return what we have (branches/tags only).
	commitOut, err := f.runGitCmd(repoPath, "log", "-20", "--format=%h%x00%s")
	if err != nil {
		if bases == nil {
			return []BranchBase{}, nil
		}
		return bases, nil
	}

	commitOut = strings.TrimSpace(commitOut)
	if commitOut != "" {
		for _, line := range strings.Split(commitOut, "\n") {
			fields := strings.Split(line, "\x00")
			if len(fields) < 2 {
				continue
			}
			bases = append(bases, BranchBase{
				Ref:    fields[0],
				Label:  fields[0],
				Type:   "commit",
				Detail: fields[1],
			})
		}
	}

	if bases == nil {
		return []BranchBase{}, nil
	}
	return bases, nil
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

// ErrInvalidBaseRef is returned when CreateBranch is called with a base
// ref that does not resolve to a commit (e.g. a non-existent branch, tag,
// or SHA). Callers can match it with errors.Is.
var ErrInvalidBaseRef = errors.New("invalid base ref")

// CreateBranch creates a new branch and checks it out. When base is
// empty the branch is created from the current HEAD (git checkout -b
// <name>). When base is non-empty it is used as the start-point
// (git checkout -b <name> <base>); if base is a remote-tracking branch,
// --track is added so the new branch sets up upstream tracking
// automatically. Emits git:status_changed on success. Returns an error
// when no project is active, the project is No Project, the branch name
// is empty, the branch already exists, the base ref is invalid, or the
// git command fails.
func (f *FrontendAPI) CreateBranch(name, base string) error {
	branchName := strings.TrimSpace(name)
	if branchName == "" {
		return errors.New("branch name must not be empty")
	}

	repoPath, err := f.resolveGitRepoRoot()
	if err != nil {
		return err
	}

	baseRef := strings.TrimSpace(base)
	args := []string{"checkout", "-b", branchName}
	if baseRef != "" {
		// Pre-validate the base ref with rev-parse so we can return a clear,
		// version-independent error instead of relying on git checkout's
		// stderr wording. ^{commit} peels annotated tags to their commit and
		// rejects refs that don't resolve to a commit object.
		if _, err := f.runGitCmd(repoPath, "rev-parse", "--verify", "--quiet", baseRef+"^{commit}"); err != nil {
			return fmt.Errorf("base %q is not a valid ref: %w", baseRef, ErrInvalidBaseRef)
		}
		if f.isRemoteTrackingRef(repoPath, baseRef) {
			// --track sets up upstream tracking when branching from a
			// remote-tracking branch whose name doesn't match the new
			// branch (git doesn't auto-track in that case).
			args = append(args, "--track")
		}
		args = append(args, baseRef)
	}

	if _, err := f.runGitCmd(repoPath, args...); err != nil {
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

// isRemoteTrackingRef reports whether ref resolves under refs/remotes/,
// i.e. it is a remote-tracking branch. Used to decide whether to add
// --track when creating a branch from a remote base.
func (f *FrontendAPI) isRemoteTrackingRef(repoPath, ref string) bool {
	_, err := f.runGitCmd(repoPath, "rev-parse", "--verify", "--quiet", "refs/remotes/"+ref)
	return err == nil
}

// ---------------------------------------------------------------------------
// AI commit message RPC (Phase 4)
// ---------------------------------------------------------------------------

// commitMsgGenTimeout caps the LLM request used to generate a commit
// message so the UI does not hang on a slow or unresponsive provider.
// A large staged diff can take a while for the model to process, and the
// router may retry on transient provider errors (429/502/503) with
// exponential backoff, so the budget is generous.
const commitMsgGenTimeout = 120 * time.Second

// GenerateCommitMessage asks the configured LLM to produce a Conventional
// Commits-formatted commit message from the active project's staged
// changes. The staged diff is obtained with a single `git diff --staged`
// invocation (rather than per-file diffs assembled by the caller), which
// avoids redundant work and keeps the diff size proportional to the real
// staged changeset. The LLM request is bounded by commitMsgGenTimeout.
// Returns an error when the application is not initialised, no project is
// active, there are no staged changes, or the LLM call fails.
func (f *FrontendAPI) GenerateCommitMessage() (string, error) {
	b := f.builder()
	if b == nil {
		f.log().Warn("GenerateCommitMessage: application not initialized (builder is nil)")
		return "", errors.New("application not initialized")
	}

	repoPath, err := f.resolveGitRepoRoot()
	if err != nil {
		f.log().Warn("GenerateCommitMessage: no active project", "err", err)
		return "", err
	}

	// Obtain the full staged diff in a single git invocation. Using
	// `git diff --staged` (rather than concatenating per-file diffs)
	// gives the model an accurate, deduplicated view of the staged
	// changeset and avoids the unstaged portions that per-file diff
	// would otherwise include.
	diff, err := f.runGitCmd(repoPath, "diff", "--staged")
	if err != nil {
		f.log().Error("GenerateCommitMessage: git diff --staged failed",
			"err", err, "repo", repoPath)
		return "", fmt.Errorf("git diff --staged: %w", err)
	}

	trimmed := strings.TrimSpace(diff)
	if trimmed == "" {
		f.log().Debug("GenerateCommitMessage: no staged changes to generate from",
			"repo", repoPath)
		return "", errors.New("no staged changes to generate a commit message from")
	}

	f.log().Debug("GenerateCommitMessage: sending staged diff to LLM",
		"diff_bytes", len(trimmed), "repo", repoPath)

	ctx, cancel := context.WithTimeout(f.ctx(), commitMsgGenTimeout)
	defer cancel()

	// The core layer logs the detailed LLM-side cause (context window,
	// timeout, provider error); this boundary only logs its own
	// preconditions and passes the core error through to the frontend.
	return b.GenerateCommitMessage(ctx, trimmed)
}

// ---------------------------------------------------------------------------
// Remote operations RPCs (Phase 5)
// ---------------------------------------------------------------------------

// remoteGitCmdTimeout bounds pull/push/fetch, which can take noticeably
// longer than local git commands over a slow network.
const remoteGitCmdTimeout = 2 * time.Minute

// allowedRemoteFlags maps each remote operation to the set of git flags
// the frontend may pass via the flags parameter. This allowlist prevents
// arbitrary flag injection through the RPC boundary: only the documented
// per-operation options are accepted, and each flag is passed to git as a
// separate argv element (never interpolated into the command line).
var allowedRemoteFlags = map[string]map[string]bool{
	"pull": {
		"--ff-only":   true,
		"--rebase":    true,
		"--autostash": true,
	},
	"push": {
		"--force":            true,
		"--force-with-lease": true,
		"--no-verify":        true,
	},
	"fetch": {
		"--tags":  true,
		"--prune": true,
	},
}

// validateRemoteFlags returns an error when any flag in flags is not in
// the allowlist for the given operation. An empty or nil flags slice is
// always valid (the default operation with no extra options).
func validateRemoteFlags(op string, flags []string) error {
	allowed := allowedRemoteFlags[op]
	for _, flag := range flags {
		if !allowed[flag] {
			return fmt.Errorf("unsupported %s flag: %s", op, flag)
		}
	}
	return nil
}

// Pull fetches from and integrates the named remote into the current
// branch (git pull <remote> [flags...]). When remote is empty, git uses
// the configured upstream. flags carries optional pull strategies
// (--ff-only, --rebase, --rebase --autostash); each must be in the
// allowedRemoteFlags allowlist. The combined stdout+stderr output is
// returned for display in the UI. Parallel remote operations are
// serialized via remoteOpMu. Emits git:status_changed on completion.
// Returns an error when no project is active, the project is No Project,
// a flag is not allowed, or the git command fails.
func (f *FrontendAPI) Pull(remote string, flags []string) (string, error) {
	return f.runRemoteOp("pull", remote, flags)
}

// Push sends local commits to the named remote (git push <remote>
// [flags...]). When remote is empty, git uses the configured upstream.
// flags carries optional push options (--force, --force-with-lease,
// --no-verify); each must be in the allowedRemoteFlags allowlist. The
// combined stdout+stderr output is returned for display in the UI.
// Parallel remote operations are serialized via remoteOpMu. Emits
// git:status_changed on completion. Returns an error when no project is
// active, the project is No Project, a flag is not allowed, or the git
// command fails.
func (f *FrontendAPI) Push(remote string, flags []string) (string, error) {
	return f.runRemoteOp("push", remote, flags)
}

// Fetch downloads objects and refs from the named remote without merging
// (git fetch <remote> [flags...]). When remote is empty, git uses the
// configured upstream. flags carries optional fetch options (--tags,
// --prune); each must be in the allowedRemoteFlags allowlist. The
// combined stdout+stderr output is returned for display in the UI.
// Parallel remote operations are serialized via remoteOpMu. Emits
// git:status_changed on completion so the frontend can refresh
// ahead/behind indicators. Returns an error when no project is active,
// the project is No Project, a flag is not allowed, or the git command
// fails.
func (f *FrontendAPI) Fetch(remote string, flags []string) (string, error) {
	return f.runRemoteOp("fetch", remote, flags)
}

// runRemoteOp executes a serialized remote git operation (pull/push/
// fetch) and returns its combined output. It is the shared body of Pull,
// Push and Fetch. flags are validated against allowedRemoteFlags and
// appended to the git argv after the optional remote. It is intentionally
// unexported so it is not exposed as a Wails RPC method.
func (f *FrontendAPI) runRemoteOp(op, remote string, flags []string) (string, error) {
	if err := validateRemoteFlags(op, flags); err != nil {
		return "", err
	}

	repoPath, err := f.resolveGitRepoRoot()
	if err != nil {
		return "", err
	}

	args := []string{op}
	if r := strings.TrimSpace(remote); r != "" {
		args = append(args, r)
	}
	args = append(args, flags...)

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

// defaultGitHistoryLimit is the page size used by GetGitHistory when the
// caller does not request a positive limit.
const defaultGitHistoryLimit = 50

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

// StashDrop removes the stash at the given index without applying it
// (git stash drop stash@{<index>}). Emits git:status_changed on success.
// Returns an error when no project is active, the project is No Project,
// the index is negative, or the git command fails (for example, when the
// stash does not exist).
func (f *FrontendAPI) StashDrop(index int) error {
	if index < 0 {
		return errors.New("stash index must not be negative")
	}

	repoPath, err := f.resolveGitRepoRoot()
	if err != nil {
		return err
	}

	stashRef := fmt.Sprintf("stash@{%d}", index)
	f.log().Debug("git stash drop", "index", index)
	if _, err := f.runGitCmd(repoPath, "stash", "drop", stashRef); err != nil {
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

// GetGitHistory returns a page of commit history for the unified
// history+graph view, each commit carrying the union of the
// human-readable log fields (author/email/date/message) and the graph
// topology fields (parents/refs). limit caps the number of commits
// returned; skip offsets into the history for lazy-load pagination
// ("Load more"). A non-positive limit defaults to defaultGitHistoryLimit;
// a negative skip is treated as zero. Returns an empty slice (not nil)
// when there are no commits. Returns an error when no project is active,
// the project is No Project, or the git command fails.
func (f *FrontendAPI) GetGitHistory(limit, skip int) ([]GitHistoryCommit, error) {
	if limit <= 0 {
		limit = defaultGitHistoryLimit
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
	// between commits. Fields: SHA, parents, author, email, date,
	// subject, ref decorations.
	out, err := f.runGitCmd(repoPath, "log",
		"-n", strconv.Itoa(limit),
		"--skip", strconv.Itoa(skip),
		"--format=%H%x1f%P%x1f%an%x1f%ae%x1f%ad%x1f%s%x1f%d%x1e",
	)
	if err != nil {
		return nil, err
	}

	return parseGitHistory(out), nil
}

// parseGitHistory parses git log output produced with the
// --format=%H%x1f%P%x1f%an%x1f%ae%x1f%ad%x1f%s%x1f%d%x1e pretty format.
// Records are separated by %x1e (record separator) and fields within a
// record by %x1f (unit separator): SHA, parents (space-separated), author,
// email, date, subject, ref decorations. Returns an empty slice when
// output is empty.
func parseGitHistory(output string) []GitHistoryCommit {
	output = strings.TrimSpace(output)
	if output == "" {
		return []GitHistoryCommit{}
	}
	records := strings.Split(output, "\x1e")
	commits := make([]GitHistoryCommit, 0, len(records))
	for _, rec := range records {
		rec = strings.TrimSpace(rec)
		if rec == "" {
			continue
		}
		fields := strings.Split(rec, "\x1f")
		if len(fields) < 7 {
			continue
		}
		var parents []string
		if p := strings.TrimSpace(fields[1]); p != "" {
			parents = strings.Fields(p)
		}
		commits = append(commits, GitHistoryCommit{
			SHA:     fields[0],
			Parents: parents,
			Author:  fields[2],
			Email:   fields[3],
			Date:    fields[4],
			Message: fields[5],
			Refs:    parseGitRefs(fields[6]),
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
