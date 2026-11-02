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
	"sort"
	"strconv"
	"strings"
	"time"

	"github.com/v0lka/c0wrk/backend/config"
	"github.com/v0lka/c0wrk/core/workspace"
	"github.com/v0lka/sp4rk/pathutil"
	"github.com/v0lka/sp4rk/safeio"
)

const gitCmdTimeout = 30 * time.Second

// --- Git staging RPCs -------------------------------------------------------

// StageFile stages one file relative to the active project's repo root
// (git add <path>). See resolveGitPath for the shared error contract.
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

// UnstageFile unstages one file relative to the active project's repo
// root (git reset HEAD <path>). See resolveGitPath for the shared error contract.
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

// StageAll stages all changes (git add -A); UnstageAll unstages all
// (git reset HEAD). See resolveGitRepoRoot for the shared error contract.
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

// UnstageAll unstages all changes in the active project's repository.
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

// --- DiffStat RPC -----------------------------------------------------------

// GetDiffStat returns added/deleted line counts for a file's uncommitted
// changes (git diff --numstat <path>). See resolveGitPath for the shared
// error contract.
func (f *FrontendAPI) GetDiffStat(path string) (*DiffStat, error) {
	repoPath, relPath, err := f.resolveGitPath(path)
	if err != nil {
		return nil, err
	}

	ctx, cancel := context.WithTimeout(f.ctx(), gitCmdTimeout)
	defer cancel()

	cmd, err := workspace.GitCmdInRepo(ctx, repoPath, "diff", "--numstat", "--", relPath)
	if err != nil {
		return nil, err
	}
	cmd.Dir = repoPath
	var stdout, stderr bytes.Buffer
	cmd.Stdout = &stdout
	cmd.Stderr = &stderr

	if err := cmd.Run(); err != nil {
		return nil, fmt.Errorf("git diff --numstat: %w", err)
	}

	return parseDiffStat(stdout.String())
}

// GetDiffStats returns added/deleted counts for every file with
// uncommitted changes in one `git diff --numstat HEAD` call (staged and
// unstaged vs HEAD), keyed by absolute path (filepath.Join(repoRoot,
// numstatPath)) — the GitStatus key convention, so the frontend enriches
// status entries by direct key equality. Binary files report zero (their
// "-" numstat fields); untracked files are not included (git diff does
// not report them). Empty map (never nil) on a clean tree; see
// resolveGitRepoRoot for the shared error contract.
// See specs/contracts/desktop-frontend.md (GetDiffStats).
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

// parseDiffStat parses one numstat line: <added>\t<deleted>\t<path>.
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

// parseDiffStats parses `git diff --numstat HEAD` output (one
// "<added>\t<deleted>\t<path>" line per file) into a map keyed by
// filepath.Join(repoPath, rawPath). Paths stay exactly as git emits them
// (quoted for special characters) to match GitStatus keys; unparseable
// lines are skipped; the map is non-nil even when the output is empty.
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

// unquoteGitPath strips surrounding double quotes and unescapes C-style
// escapes from a git output path (git quotes paths with special
// characters — spaces, tabs, non-ASCII — when core.quotePath is on, the
// default). Unquoted input is returned unchanged.
func unquoteGitPath(path string) string {
	if len(path) >= 2 && path[0] == '"' && path[len(path)-1] == '"' {
		if unquoted, err := strconv.Unquote(path); err == nil {
			return unquoted
		}
	}
	return path
}

// --- shared helpers ---------------------------------------------------------

// resolveGitPath validates that path is inside the active project's
// workspace (never No Project) and returns the repo root plus the path
// relative to it. All path-based git RPCs share this error contract:
// a missing/inactive project, a No-Project session, a path outside the
// workspace, or a failing git command surfaces as the return error.
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

// resolveGitRepoRoot returns the repo root every Git-panel RPC operates
// on: the Git-panel focus target (frontend_api_git_focus.go), i.e. the
// explicitly focused worktree or — by default — the active session's
// workspace, after the active-project/No-Project gate. A vanished focus
// tree falls back to the project checkout fail-soft.
// See specs/contracts/desktop-frontend.md and specs/domains/session-worktrees.md.
func (f *FrontendAPI) resolveGitRepoRoot() (string, error) {
	projectPath, _, err := f.activeGitProjectRoot()
	if err != nil {
		return "", err
	}
	return f.resolveGitFocusRoot(projectPath), nil
}

// runGitCmd runs a git sub-command in dir with the 30s gitCmdTimeout and
// returns stdout.
func (f *FrontendAPI) runGitCmd(dir string, args ...string) (string, error) {
	if len(args) == 0 {
		return "", errors.New("runGitCmd: no arguments")
	}

	ctx, cancel := context.WithTimeout(f.ctx(), gitCmdTimeout)
	defer cancel()

	cmd, err := workspace.GitCmdInRepo(ctx, dir, args...)
	if err != nil {
		return "", fmt.Errorf("git: %w", err)
	}
	cmd.Dir = dir
	// A non-*os.File stdout capture makes os/exec copy each pipe on its own
	// goroutine and block Wait until they see EOF, so a grandchild spawned by
	// a trusted repository's hook (which inherits the write end) would stall
	// the call past the deadline forever. WaitDelay bounds exactly that
	// window; an orphan holding the pipes past it surfaces as
	// exec.ErrWaitDelay after the command itself exited 0 — success for this
	// caller, so it is tolerated (cmd.Output would discard the
	// already-captured stdout on that error, hence the explicit buffers).
	cmd.WaitDelay = commitWaitDelay

	var stdout, stderr bytes.Buffer
	cmd.Stdout = &stdout
	cmd.Stderr = &stderr

	if err := cmd.Run(); err != nil && !errors.Is(err, exec.ErrWaitDelay) {
		// Surface git's stderr so callers can match on specific failure
		// phrases (e.g. "already exists", "would be overwritten").
		if msg := strings.TrimSpace(stderr.String()); msg != "" {
			return "", fmt.Errorf("git: %w: %s", err, msg)
		}
		return "", fmt.Errorf("git: %w", err)
	}
	return stdout.String(), nil
}

// runGitCmdWithStdin is runGitCmd with stdinData fed to the command's
// standard input — bypassing ARG_MAX when passing thousands of revisions
// (e.g. `git log --stdin`, `git cat-file --batch-check`).
func (f *FrontendAPI) runGitCmdWithStdin(dir, stdinData string, args ...string) (string, error) {
	if len(args) == 0 {
		return "", errors.New("runGitCmdWithStdin: no arguments")
	}

	ctx, cancel := context.WithTimeout(f.ctx(), gitCmdTimeout)
	defer cancel()

	cmd, err := workspace.GitCmdInRepo(ctx, dir, args...)
	if err != nil {
		return "", fmt.Errorf("git: %w", err)
	}
	cmd.Dir = dir
	cmd.Stdin = strings.NewReader(stdinData)
	// Same orphan-holds-the-pipes bound as runGitCmd (review [62]).
	cmd.WaitDelay = commitWaitDelay

	var stdout, stderr bytes.Buffer
	cmd.Stdout = &stdout
	cmd.Stderr = &stderr

	if err := cmd.Run(); err != nil && !errors.Is(err, exec.ErrWaitDelay) {
		if msg := strings.TrimSpace(stderr.String()); msg != "" {
			return "", fmt.Errorf("git: %w: %s", err, msg)
		}
		return "", fmt.Errorf("git: %w", err)
	}
	return stdout.String(), nil
}

// runGitCmdCombined runs a git sub-command and returns the combined
// stdout+stderr — stderr is kept because git writes progress and
// informational messages there even on success (pull/push/fetch). The
// timeout is caller-supplied so remote operations can opt into a larger
// budget; on failure the partial output (capped at gitOutputLimit) is
// still returned alongside the wrapped error for the UI.
func (f *FrontendAPI) runGitCmdCombined(dir string, timeout time.Duration, args ...string) (string, error) {
	if len(args) == 0 {
		return "", errors.New("runGitCmdCombined: no arguments")
	}

	ctx, cancel := context.WithTimeout(f.ctx(), timeout)
	defer cancel()

	cmd, err := workspace.GitCmdInRepo(ctx, dir, args...)
	if err != nil {
		return "", fmt.Errorf("git: %w", err)
	}
	cmd.Dir = dir

	// Capture the streams separately (each gets its own copy goroutine);
	// one shared bytes.Buffer would race.
	var stdout, stderr bytes.Buffer
	cmd.Stdout = &stdout
	cmd.Stderr = &stderr
	// Same orphan-holds-the-pipes bound as runGitCmd; this is the spawn
	// every remote operation goes through under remoteOpMu. An orphan past
	// the delay after a successful exit keeps the captured output.
	cmd.WaitDelay = commitWaitDelay

	if err := cmd.Run(); err != nil && !errors.Is(err, exec.ErrWaitDelay) {
		return combinedGitOutput(stdout.String(), stderr.String()), fmt.Errorf("git: %w", err)
	}
	return combinedGitOutput(stdout.String(), stderr.String()), nil
}

// gitOutputLimit (64 KiB) is the single cap on captured git output —
// a hook-heavy/signing commit or a chatty remote op can spray megabytes;
// it is shared by limitedBuffer (the commit spawn, capped as it streams)
// and truncateGitOutput (the remote-op path). See
// specs/decisions/059-commit-hooks-signing-gate.md.
const gitOutputLimit = 64 * 1024

// gitOutputTruncationMarker is appended when captured output exceeded
// gitOutputLimit and was cut.
const gitOutputTruncationMarker = "\n[output truncated at 64 KiB]"

// truncateGitOutput caps a finalized output string at gitOutputLimit,
// appending gitOutputTruncationMarker when cut.
func truncateGitOutput(out string) string {
	if len(out) <= gitOutputLimit {
		return out
	}
	return out[:gitOutputLimit] + gitOutputTruncationMarker
}

// combinedGitOutput joins stdout and stderr (stdout first), trims, and
// caps the result at gitOutputLimit with a truncation marker.
func combinedGitOutput(stdout, stderr string) string {
	out := strings.TrimSpace(stdout)
	if s := strings.TrimSpace(stderr); s != "" {
		if out != "" {
			out += "\n"
		}
		out += s
	}
	return truncateGitOutput(out)
}

// emitGitStatusChanged emits git:status_changed (repo path payload; see
// specs/contracts/event-catalog.md) and is the single invalidation funnel
// for the per-repo git caches — every mutating git RPC calls it on
// success, so the cached snapshots drop here (specs/domains/workspace.md,
// frontend_api_gitcache.go).
func (f *FrontendAPI) emitGitStatusChanged(repoPath string) {
	f.seedAcquire()
	f.invalidateGitCaches(repoPath)
	if f.emitEvent != nil {
		f.emitEvent(EventGitStatusChanged, repoPath)
	}
}

// --- Commit RPC -------------------------------------------------------------
// The Commit RPC itself lives in frontend_api_git_commit.go
// (suppression-aware flow; specs/domains/workspace.md, ADR-059).

// commitMsgRe rejects an empty or whitespace-only commit message.
var commitMsgRe = regexp.MustCompile(`\S`)

// --- Branch RPCs ------------------------------------------------------------

// GetBranches returns local and remote branches in one `git
// for-each-ref` call, each Branch carrying its kind, whether it is the
// checked-out local branch, and its upstream short name. Symbolic refs
// (origin/HEAD) are skipped; empty slice (not nil) when none exist. See
// resolveGitRepoRoot for the shared error contract.
func (f *FrontendAPI) GetBranches() ([]Branch, error) {
	repoPath, err := f.resolveGitRepoRoot()
	if err != nil {
		return nil, err
	}

	// --format: full-refname<NUL>short-refname<NUL>HEAD-marker<NUL>upstream-short<NUL>symref<NUL>
	// Each ref is separated by LF, fields within a ref by NUL.
	out, err := f.runGitCmd(repoPath, "for-each-ref", "refs/heads/", "refs/remotes/",
		"--format=%(refname)%00%(refname:short)%00%(HEAD)%00%(upstream:short)%00%(symref)%00")
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
		if len(fields) < 5 {
			continue
		}
		fullName := fields[0]
		// Symbolic refs have their target in %(symref); a real branch
		// (even one literally named "*/HEAD") leaves it empty.
		if fields[4] != "" {
			continue
		}
		kind := "remote"
		if strings.HasPrefix(fullName, "refs/heads/") {
			kind = "local"
		}
		branches = append(branches, Branch{
			Name:      fields[1],
			IsCurrent: kind == "local" && fields[2] == "*",
			Kind:      kind,
			Upstream:  fields[3],
		})
	}
	return branches, nil
}

// GetCurrentBranch returns the checked-out branch's name, configured
// upstream, and ahead/behind counts relative to that upstream. Name is
// "HEAD" in detached HEAD; upstream is empty and counts zero without a
// configured upstream or in detached HEAD. See resolveGitRepoRoot for the
// shared error contract.
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

	// @{upstream} does not resolve in detached HEAD or without a tracking
	// branch; treat both as no-upstream.
	if name == "" || name == "HEAD" {
		return info, nil
	}

	upstream, err := f.runGitCmd(repoPath, "rev-parse", "--abbrev-ref", "@{upstream}")
	if err != nil {
		return info, nil //nolint:nilerr // no upstream configured: partial info, not an error
	}
	info.Upstream = strings.TrimSpace(upstream)

	// git rev-list --count --left-right @{upstream}...HEAD
	// Output: "<behind>\t<ahead>" — left = upstream-only, right = HEAD-only.
	cnt, err := f.runGitCmd(repoPath, "rev-list", "--count", "--left-right", "@{upstream}...HEAD")
	if err != nil {
		return info, nil //nolint:nilerr // upstream ref unresolved locally: return partial info
	}
	fields := strings.Split(strings.TrimSpace(cnt), "\t")
	if len(fields) == 2 {
		info.Behind, _ = strconv.Atoi(fields[0])
		info.Ahead, _ = strconv.Atoi(fields[1])
	}

	return info, nil
}

// GetBranchBases returns every ref usable as a CreateBranch start-point:
// local branches, remote-tracking branches, tags, and the 20 most recent
// commits, ordered local → remote → tag → commit. The checked-out branch
// is excluded from the local list (it is the default HEAD base); symbolic
// refs are skipped; empty slice (not nil) when no refs exist. See
// resolveGitRepoRoot for the shared error contract.
func (f *FrontendAPI) GetBranchBases() ([]BranchBase, error) {
	repoPath, err := f.resolveGitRepoRoot()
	if err != nil {
		return nil, err
	}

	// Collect refs (local branches, remote-tracking branches, tags) via
	// for-each-ref. Format: short-name<NUL>full-refname<NUL>HEAD-marker<NUL>symref<NUL>
	out, err := f.runGitCmd(repoPath, "for-each-ref",
		"refs/heads/", "refs/remotes/", "refs/tags/",
		"--format=%(refname:short)%00%(refname)%00%(HEAD)%00%(symref)%00",
	)
	if err != nil {
		return nil, err
	}

	var bases []BranchBase
	out = strings.TrimSpace(out)
	if out != "" {
		for _, line := range strings.Split(out, "\n") {
			fields := strings.Split(line, "\x00")
			if len(fields) < 4 {
				continue
			}
			shortName := fields[0]
			fullRef := fields[1]
			isCurrent := fields[2] == "*"
			// Skip symbolic refs — same %(symref) rule as GetBranches.
			if fields[3] != "" {
				continue
			}

			var refType string
			switch {
			case strings.HasPrefix(fullRef, "refs/heads/"):
				refType = "local"
				if isCurrent {
					continue
				}
			case strings.HasPrefix(fullRef, "refs/remotes/"):
				refType = "remote"
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

	// Recent commits: short SHA + subject. A fresh repo with no commits
	// makes git log fail — return what we have (branches/tags only).
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

// --- Branch management RPCs -------------------------------------------------

// CheckoutBranch switches the focused worktree to the named local branch
// (git checkout <name>). A managed session-worktree focus refuses the
// switch (ErrPinnedWorktreeBranch) unless the branch is its pin — the
// branch is pinned by the owning session (ADR-080). Emits
// git:status_changed on success; see resolveGitRepoRoot for the shared
// error contract.
func (f *FrontendAPI) CheckoutBranch(name string) error {
	branchName := strings.TrimSpace(name)
	if branchName == "" {
		return errors.New("branch name must not be empty")
	}
	if err := validateGitOperand("branch name", branchName); err != nil {
		return err
	}

	checkout, repoPath, err := f.gitFocusRoots()
	if err != nil {
		return err
	}
	if err := f.refusePinnedBranchSwitch(checkout, repoPath, branchName); err != nil {
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

// ErrInvalidBaseRef is returned when CreateBranch's base ref does not
// resolve to a commit; match with errors.Is.
var ErrInvalidBaseRef = errors.New("invalid base ref")

// CreateBranch creates a new branch and checks it out. An empty base
// branches from HEAD; a non-empty base is the start-point, with --track
// added for a remote-tracking base so upstream tracking is set up
// automatically. A managed session-worktree focus refuses the operation
// (ErrPinnedWorktreeBranch) — a brand-new branch always leaves the pin.
// See resolveGitRepoRoot for the shared error contract.
func (f *FrontendAPI) CreateBranch(name, base string) error {
	branchName := strings.TrimSpace(name)
	if branchName == "" {
		return errors.New("branch name must not be empty")
	}
	if err := validateGitOperand("branch name", branchName); err != nil {
		return err
	}

	checkout, repoPath, err := f.gitFocusRoots()
	if err != nil {
		return err
	}
	if err := f.refusePinnedBranchSwitch(checkout, repoPath, branchName); err != nil {
		return err
	}

	baseRef := strings.TrimSpace(base)
	args := []string{"checkout", "-b", branchName}
	if baseRef != "" {
		// Pre-validate with rev-parse for a clear, version-independent
		// error instead of git checkout's stderr wording; ^{commit} peels
		// annotated tags and rejects non-commit refs.
		if _, err := f.runGitCmd(repoPath, "rev-parse", "--verify", "--quiet", baseRef+"^{commit}"); err != nil {
			return fmt.Errorf("base %q is not a valid ref: %w", baseRef, ErrInvalidBaseRef)
		}
		if f.isRemoteTrackingRef(repoPath, baseRef) {
			// git doesn't auto-track when the new branch name differs
			// from the remote-tracking base.
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

// managedTreeSlugCap caps the slug part of a branch-derived managed tree
// name so a "-NN" disambiguation suffix still fits inside the 120-byte
// portable-identity limit enforced by config.ManagedWorktreePath.
const managedTreeSlugCap = 116

// worktreeSlug derives a portable managed-tree name candidate from a
// branch name: only [A-Za-z0-9._-] survive (config.ManagedWorktreePath's
// identity alphabet; branch names commonly carry '/'), leading '-'/'.'
// and trailing '.' are stripped, empty degrades to "branch", and the
// result is capped for a numeric suffix. Validated again (fail-closed)
// inside every owner operation.
func worktreeSlug(branch string) string {
	var b strings.Builder
	b.Grow(len(branch))
	for _, r := range branch {
		switch {
		case r >= 'a' && r <= 'z', r >= 'A' && r <= 'Z', r >= '0' && r <= '9',
			r == '-', r == '_', r == '.':
			b.WriteRune(r)
		default:
			b.WriteByte('-')
		}
	}
	slug := strings.TrimLeft(b.String(), "-.")
	slug = strings.TrimRight(slug, ".")
	if slug == "" {
		slug = "branch"
	}
	if len(slug) > managedTreeSlugCap {
		slug = slug[:managedTreeSlugCap]
		// A cut can expose a trailing dot — re-sanitize.
		slug = strings.TrimRight(slug, ".")
	}
	return slug
}

// freeManagedTreeName derives a free managed-tree name for branch: the
// worktreeSlug, disambiguated with -2, -3, … when the identity is taken
// on disk (an existing managed tree or any other occupant that would make
// `git worktree add` refuse) or fails portable-identity validation (e.g.
// a reserved Windows device name — the suffix moves it off the stem).
// Bounded: 100 candidates.
func freeManagedTreeName(repoRoot, branch string, container ...string) (string, error) {
	ctx, cancel := context.WithTimeout(context.Background(), gitCmdTimeout)
	defer cancel()
	trees, err := workspace.ListWorktrees(ctx, repoRoot)
	if err != nil {
		return "", err
	}
	occupied := make(map[string]bool)
	for _, tree := range trees {
		if tree.Kind == workspace.WorktreeManaged {
			occupied[tree.Name] = true
		}
	}
	base := worktreeSlug(branch)
	var lastErr error
	for n := 0; n < 100; n++ {
		candidate := base
		if n > 0 {
			candidate = base + "-" + strconv.Itoa(n+1)
		}
		if occupied[candidate] {
			continue
		}
		path, err := config.ManagedWorktreePath(repoRoot, candidate)
		if len(container) > 0 {
			path, err = config.WorktreePathInContainer(container[0], candidate)
		}
		if err != nil {
			lastErr = fmt.Errorf("managed worktree name %q: %w", candidate, err)
			continue
		}
		switch _, statErr := os.Lstat(path); {
		case statErr == nil:
			continue
		case !errors.Is(statErr, os.ErrNotExist):
			return "", fmt.Errorf("inspecting managed worktree name %q: %w", candidate, statErr)
		}
		return candidate, nil
	}
	if lastErr != nil {
		return "", lastErr
	}
	return "", fmt.Errorf("no free managed worktree name derived from branch %q", branch)
}

// CreateBranchInWorktree creates a new branch in a freshly provisioned
// managed worktree: git worktree add -b <branch> <tree> [<base>] — the
// branch and its tree are born atomically (empty base = the checkout's
// HEAD; a pre-existing branch fails with workspace.ErrBranchExists
// before git runs; the tree name is a branch-derived slug disambiguated
// with a numeric suffix). Unlike CreateBranch the Git-panel focus is
// NEITHER switched NOR consulted — the focused tree is never touched;
// entering the new tree is the user's decision. git:status_changed does
// not refresh the worktree list — the frontend's refreshGitFocus() does,
// after this RPC returns (specs/domains/session-worktrees.md); a caller
// that skips it leaves the new tree absent from the switcher. See
// resolveGitRepoRoot for the shared error contract.
func (f *FrontendAPI) CreateBranchInWorktree(name, base string) error {
	branchName := strings.TrimSpace(name)
	if branchName == "" {
		return errors.New("branch name must not be empty")
	}
	if err := validateGitOperand("branch name", branchName); err != nil {
		return err
	}

	checkout, projectID, err := f.activeGitProjectRoot()
	if err != nil {
		return err
	}
	// Canonicalize the checkout before deriving managed paths (fail-soft
	// EvalSymlinks — see canonicalWorktreeRoot): a legacy symlinked row
	// would diverge from git's canonical listings and fail the add
	// post-check AFTER a successful `git worktree add`, stranding the
	// branch and tree. The success event stays on the stored form — the
	// frontend keys git events by the registered path.
	repoRoot := canonicalWorktreeRoot(checkout)

	baseRef := strings.TrimSpace(base)
	if baseRef != "" {
		// Same rev-parse pre-validation as CreateBranch.
		if _, err := f.runGitCmd(repoRoot, "rev-parse", "--verify", "--quiet", baseRef+"^{commit}"); err != nil {
			return fmt.Errorf("base %q is not a valid ref: %w", baseRef, ErrInvalidBaseRef)
		}
	}

	owner, err := f.newWorktreeOwner(projectID)
	if err != nil {
		return err
	}
	probe, err := owner.NewPath(repoRoot, "probe")
	if err != nil {
		return err
	}
	treeName, err := freeManagedTreeName(repoRoot, branchName, filepath.Dir(probe))
	if err != nil {
		return err
	}

	ctx, cancel := context.WithTimeout(f.ctx(), managedProvisionTimeout)
	defer cancel()
	if _, err := owner.ProvisionNewBranch(ctx, repoRoot, treeName, branchName, baseRef); err != nil {
		return err
	}

	// Best-effort, after the success point — the focus does not move, but
	// the tree's first index pass reuses the checkout's computed embeddings.
	f.seedWorktreeEmbeddingCache(projectID, treeName)

	f.emitGitStatusChanged(checkout)
	return nil
}

// isLocalChangesOverwritten reports whether err is a git checkout failure
// caused by changes that would be overwritten; git emits "would be
// overwritten" variants on stderr (tracked and untracked) for both.
func isLocalChangesOverwritten(err error) bool {
	if err == nil {
		return false
	}
	return strings.Contains(err.Error(), "would be overwritten")
}

// isBranchAlreadyExists reports whether err is a checkout/branch failure
// because the branch name is already taken.
func isBranchAlreadyExists(err error) bool {
	if err == nil {
		return false
	}
	return strings.Contains(err.Error(), "already exists")
}

// isRemoteTrackingRef reports whether ref resolves under refs/remotes/
// (a remote-tracking branch) — used to decide whether CreateBranch adds
// --track.
func (f *FrontendAPI) isRemoteTrackingRef(repoPath, ref string) bool {
	_, err := f.runGitCmd(repoPath, "rev-parse", "--verify", "--quiet", "refs/remotes/"+ref)
	return err == nil
}

// RenameBranch renames a local branch (git branch -m <old> <new>),
// including the currently checked-out one. Renaming a managed
// session-worktree focus's pinned branch is refused (the branch name is
// part of the session's immutable binding, ErrPinnedWorktreeBranch); see
// specs/domains/session-worktrees.md and resolveGitRepoRoot for the
// shared error contract.
func (f *FrontendAPI) RenameBranch(oldName, newName string) error {
	oldBranch := strings.TrimSpace(oldName)
	if oldBranch == "" {
		return errors.New("old branch name must not be empty")
	}
	newBranch := strings.TrimSpace(newName)
	if newBranch == "" {
		return errors.New("new branch name must not be empty")
	}
	if err := validateGitOperand("old branch name", oldBranch); err != nil {
		return err
	}
	if err := validateGitOperand("new branch name", newBranch); err != nil {
		return err
	}

	checkout, repoPath, err := f.gitFocusRoots()
	if err != nil {
		return err
	}
	if isManagedTreePath(checkout, repoPath) {
		if current, curErr := f.currentBranchName(repoPath); curErr == nil && oldBranch == current {
			return fmt.Errorf("%w: %s's pinned branch %q cannot be renamed; the owning session's binding records it", ErrPinnedWorktreeBranch, filepath.Base(repoPath), oldBranch)
		}
	}

	if _, err := f.runGitCmd(repoPath, "branch", "-m", oldBranch, newBranch); err != nil {
		if isBranchAlreadyExists(err) {
			return fmt.Errorf("branch %q already exists", newBranch)
		}
		return err
	}
	f.emitGitStatusChanged(repoPath)
	return nil
}

// DeleteBranch deletes a local branch (git branch -d, or -D when force).
// The currently checked-out branch is rejected before git runs — git
// cannot delete it anyway and its stderr wording is worktree-specific.
// See resolveGitRepoRoot for the shared error contract.
func (f *FrontendAPI) DeleteBranch(name string, force bool) error {
	branchName := strings.TrimSpace(name)
	if branchName == "" {
		return errors.New("branch name must not be empty")
	}
	if err := validateGitOperand("branch name", branchName); err != nil {
		return err
	}

	repoPath, err := f.resolveGitRepoRoot()
	if err != nil {
		return err
	}

	current, err := f.currentBranchName(repoPath)
	if err != nil {
		return err
	}
	if branchName == current {
		return errors.New("cannot delete the currently checked-out branch")
	}

	flag := "-d"
	if force {
		flag = "-D"
	}
	if _, err := f.runGitCmd(repoPath, "branch", flag, branchName); err != nil {
		return err
	}
	f.emitGitStatusChanged(repoPath)
	// Quiet background index cleanup: the deleted branch's stored vector
	// artifacts (checkout + every managed worktree) go now instead of
	// waiting for the next project-switch sweep. Detached, fail-soft,
	// silent — never a failure path of this RPC.
	f.purgeDeletedBranchIndexes(branchName)
	return nil
}

// currentBranchName returns the short name of the checked-out branch, or
// "HEAD" in detached HEAD state.
func (f *FrontendAPI) currentBranchName(repoPath string) (string, error) {
	out, err := f.runGitCmd(repoPath, "rev-parse", "--abbrev-ref", "HEAD")
	if err != nil {
		return "", err
	}
	return strings.TrimSpace(out), nil
}

// --- Branch remote operations -----------------------------------------------

// PushBranch pushes the named local branch to its upstream remote, or
// publishes it (setting the upstream) when it has none; pushArgs
// assembles the argv (specs/contracts/desktop-frontend.md). The remote-op
// contract (remoteOpMu serialization, remoteGitCmdTimeout, combined
// output, git:status_changed) lives in runSerializedRemoteOp and
// specs/domains/git-auto-fetch.md. See resolveGitRepoRoot for the shared
// error contract.
func (f *FrontendAPI) PushBranch(name string) (string, error) {
	branchName := strings.TrimSpace(name)
	if branchName == "" {
		return "", errors.New("branch name must not be empty")
	}
	if err := validateGitOperand("branch name", branchName); err != nil {
		return "", err
	}

	repoPath, err := f.resolveGitRepoRoot()
	if err != nil {
		return "", err
	}

	return f.runSerializedRemoteOp(repoPath, f.pushArgs(repoPath, branchName, nil)...)
}

// branchRemote returns the branch's configured upstream remote
// (git config branch.<name>.remote), or "" — so it doubles as the
// canonical "does this branch have an upstream" check.
func (f *FrontendAPI) branchRemote(repoPath, branchName string) string {
	out, err := f.runGitCmd(repoPath, "config", "--get", "branch."+branchName+".remote")
	if err != nil {
		return ""
	}
	return strings.TrimSpace(out)
}

// branchMerge returns the branch's configured upstream merge ref
// (git config branch.<name>.merge, e.g. "refs/heads/main"), or "" when no
// upstream is configured; together with branch.<name>.remote it forms the
// branch's full upstream, which pushArgs reads to target the exact
// tracked ref.
func (f *FrontendAPI) branchMerge(repoPath, branchName string) string {
	out, err := f.runGitCmd(repoPath, "config", "--get", "branch."+branchName+".merge")
	if err != nil {
		return ""
	}
	return strings.TrimSpace(out)
}

// upstreamRefFromMerge strips the "refs/heads/" prefix from a
// branch.<name>.merge value. Only a branch upstream maps to a pushable
// refspec destination — anything else returns "" and the caller falls
// back to the branch's own name. Pure, so the merge value is read once
// per push.
func upstreamRefFromMerge(merge string) string {
	if !strings.HasPrefix(merge, "refs/heads/") {
		return ""
	}
	return strings.TrimPrefix(merge, "refs/heads/")
}

// pushArgs assembles the argv that pushes branchName; flags are optional
// push options (--force, --force-with-lease, --no-verify) placed before
// the positionals. The full contract — push-remote precedence
// (branch.<name>.pushRemote → remote.pushDefault → upstream remote), the
// upstream-style "<local>:<upstreamRef>" refspec, publication of a
// never-published branch via `git push -u` under its own name, and why
// branch.<name>.merge is consulted only for a published branch — is
// specified in specs/contracts/desktop-frontend.md (Push/PushBranch rows).
// Shared by PushBranch and the bare-push path in runRemoteOp; callers
// validate flags and resolve the branch name.
func (f *FrontendAPI) pushArgs(repoPath, branchName string, flags []string) []string {
	args := []string{"push"}
	args = append(args, flags...)

	if upstreamRemote := f.branchRemote(repoPath, branchName); upstreamRemote != "" {
		// Already published: push to the ref the branch tracks, on its
		// push remote.
		refspec := branchName
		if upstream := upstreamRefFromMerge(f.branchMerge(repoPath, branchName)); upstream != "" && upstream != branchName {
			refspec = branchName + ":" + upstream
		}
		return append(args, f.pushRemote(repoPath, branchName, upstreamRemote), refspec)
	}
	// Never published: create the remote branch under the branch's own
	// name and set the upstream in one step.
	return append(args, "-u", f.defaultPushRemote(repoPath, branchName), branchName)
}

// pushRemote returns the remote a push of branchName targets, following
// git's push-remote precedence: branch.<name>.pushRemote, else
// remote.pushDefault, else fallback. Both overriding keys are
// push-specific (they outrank the tracking remote for pushes only), so
// the already-published path passes the branch's upstream remote as the
// fallback while the never-published path passes "" and lets
// defaultPushRemote resolve the conventional default.
func (f *FrontendAPI) pushRemote(repoPath, branchName, fallback string) string {
	for _, key := range []string{
		"branch." + branchName + ".pushRemote",
		"remote.pushDefault",
	} {
		if out, err := f.runGitCmd(repoPath, "config", "--get", key); err == nil {
			if r := strings.TrimSpace(out); r != "" {
				return r
			}
		}
	}
	return fallback
}

// defaultPushRemote returns the remote a never-published branch should be
// published to: git's push-remote precedence first, then — unlike a bare
// `git push`, which errors with "No configured push destination" —
// "origin" when it exists, else the alphabetically first remote (a
// deterministic choice independent of `git remote`'s emission order),
// else "origin" so the failure names a remote the user recognises. That
// fallback is c0wrk's own convenience, not git parity.
func (f *FrontendAPI) defaultPushRemote(repoPath, branchName string) string {
	if r := f.pushRemote(repoPath, branchName, ""); r != "" {
		return r
	}
	if out, err := f.runGitCmd(repoPath, "remote"); err == nil {
		remotes := strings.Fields(out)
		if len(remotes) > 0 {
			sort.Strings(remotes)
			for _, r := range remotes {
				if r == "origin" {
					return "origin"
				}
			}
			return remotes[0]
		}
	}
	return "origin"
}

// CheckoutRemoteBranch creates a local branch from a remote-tracking
// branch and switches to it (git switch -c <local> --track
// <remoteBranch>); the local name is everything after the remote's "/".
// A managed session-worktree focus refuses (ErrPinnedWorktreeBranch).
// Local operation (the remote-tracking ref must exist locally, e.g. after
// a fetch), so it uses gitCmdTimeout, not remoteGitCmdTimeout. See
// resolveGitRepoRoot for the shared error contract.
func (f *FrontendAPI) CheckoutRemoteBranch(remoteBranch string) error {
	rb := strings.TrimSpace(remoteBranch)
	if rb == "" {
		return errors.New("remote branch must not be empty")
	}

	local := ""
	if i := strings.IndexByte(rb, '/'); i >= 0 {
		local = rb[i+1:]
	}
	if local == "" {
		return errors.New("remote branch must be in <remote>/<branch> form")
	}
	if err := validateGitOperand("remote branch", rb); err != nil {
		return err
	}
	if err := validateGitOperand("branch name", local); err != nil {
		return err
	}

	checkout, repoPath, err := f.gitFocusRoots()
	if err != nil {
		return err
	}
	if err := f.refusePinnedBranchSwitch(checkout, repoPath, local); err != nil {
		return err
	}

	if _, err := f.runGitCmd(repoPath, "switch", "-c", local, "--track", rb); err != nil {
		if isBranchAlreadyExists(err) {
			return fmt.Errorf("branch %q already exists", local)
		}
		return err
	}
	f.emitGitStatusChanged(repoPath)
	return nil
}

// DeleteRemoteBranch deletes the named branch on the given remote
// (git push <remote> --delete <name>; default origin). The remote-op
// contract lives in runSerializedRemoteOp; see resolveGitRepoRoot for the
// shared error contract.
func (f *FrontendAPI) DeleteRemoteBranch(name, remote string) (string, error) {
	branchName := strings.TrimSpace(name)
	if branchName == "" {
		return "", errors.New("branch name must not be empty")
	}

	repoPath, err := f.resolveGitRepoRoot()
	if err != nil {
		return "", err
	}

	remoteName := strings.TrimSpace(remote)
	if remoteName == "" {
		remoteName = "origin"
	}
	if err := validateGitOperand("remote name", remoteName); err != nil {
		return "", err
	}
	if err := validateGitOperand("branch name", branchName); err != nil {
		return "", err
	}

	return f.runSerializedRemoteOp(repoPath, "push", remoteName, "--delete", branchName)
}

// --- Tag management RPCs ----------------------------------------------------

// CreateTag creates a lightweight tag at the named commit
// (git tag <name> <sha>) — lightweight is deliberate: annotated tags
// demand an interactive editor, and an armed global tag.gpgsign would
// force even a plain tag into annotated+signed mode; the inline
// `-c tag.gpgsign=false` guarantees an editor-free tag (the ADR-033
// baseline pins commit.gpgsign only). See resolveGitRepoRoot for the
// shared error contract.
func (f *FrontendAPI) CreateTag(name, sha string) error {
	tagName := strings.TrimSpace(name)
	if tagName == "" {
		return errors.New("tag name must not be empty")
	}
	if err := validateGitOperand("tag name", tagName); err != nil {
		return err
	}
	tagSha := strings.TrimSpace(sha)
	if tagSha == "" {
		return errors.New("sha must not be empty")
	}
	if err := validateCommitSha(tagSha); err != nil {
		return err
	}

	repoPath, err := f.resolveGitRepoRoot()
	if err != nil {
		return err
	}

	if _, err := f.runGitCmd(repoPath, "-c", "tag.gpgsign=false", "tag", tagName, tagSha); err != nil {
		return err
	}
	f.emitGitStatusChanged(repoPath)
	return nil
}

// DeleteTag removes a local tag (git tag -d <name>). See
// resolveGitRepoRoot for the shared error contract.
func (f *FrontendAPI) DeleteTag(name string) error {
	tagName := strings.TrimSpace(name)
	if tagName == "" {
		return errors.New("tag name must not be empty")
	}
	if err := validateGitOperand("tag name", tagName); err != nil {
		return err
	}

	repoPath, err := f.resolveGitRepoRoot()
	if err != nil {
		return err
	}

	if _, err := f.runGitCmd(repoPath, "tag", "-d", tagName); err != nil {
		return err
	}
	f.emitGitStatusChanged(repoPath)
	return nil
}

// PushTag pushes one tag to the named remote (git push <remote>
// refs/tags/<name>; default origin). The remote-op contract lives in
// runSerializedRemoteOp; this call bypasses runRemoteOp's flag
// allow-list, so the remote operand is validated explicitly. See
// resolveGitRepoRoot for the shared error contract.
func (f *FrontendAPI) PushTag(name, remote string) (string, error) {
	tagName := strings.TrimSpace(name)
	if tagName == "" {
		return "", errors.New("tag name must not be empty")
	}

	repoPath, err := f.resolveGitRepoRoot()
	if err != nil {
		return "", err
	}

	remoteName := strings.TrimSpace(remote)
	if remoteName == "" {
		remoteName = "origin"
	}
	if err := validateGitOperand("remote name", remoteName); err != nil {
		return "", err
	}
	if err := validateGitOperand("tag name", tagName); err != nil {
		return "", err
	}

	return f.runSerializedRemoteOp(repoPath, "push", remoteName, "refs/tags/"+tagName)
}

// DeleteRemoteTag deletes a tag on the named remote
// (git push <remote> :refs/tags/<name>; default origin). Like PushTag it
// bypasses runRemoteOp's flag allow-list; the remote-op contract lives in
// runSerializedRemoteOp. See resolveGitRepoRoot for the shared error
// contract.
func (f *FrontendAPI) DeleteRemoteTag(name, remote string) (string, error) {
	tagName := strings.TrimSpace(name)
	if tagName == "" {
		return "", errors.New("tag name must not be empty")
	}

	repoPath, err := f.resolveGitRepoRoot()
	if err != nil {
		return "", err
	}

	remoteName := strings.TrimSpace(remote)
	if remoteName == "" {
		remoteName = "origin"
	}
	if err := validateGitOperand("remote name", remoteName); err != nil {
		return "", err
	}
	if err := validateGitOperand("tag name", tagName); err != nil {
		return "", err
	}

	return f.runSerializedRemoteOp(repoPath, "push", remoteName, ":refs/tags/"+tagName)
}

// --- AI commit message RPC --------------------------------------------------

// GenerateCommitMessage asks the configured LLM for a Conventional
// Commits-formatted message from the staged changes, obtained in one
// `git diff --staged` call. The LLM request is bounded by the service
// timeout (serviceLLMTimeout); the embedded-model readiness gate that
// precedes it is described by specs/domains/embedded-llm.md and
// specs/decisions/066-embedded-llm-runtime.md (D13). See
// resolveGitRepoRoot for the shared error contract.
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

	// One `git diff --no-ext-diff --staged` call: an accurate,
	// deduplicated staged-only view for the model. --no-ext-diff is the
	// porcelain rule of specs/decisions/033-git-subprocess-hardening.md —
	// plain `git diff` executes external diff drivers by default, and the
	// scanner's per-key -c kills only neutralize them.
	diff, err := f.runGitCmd(repoPath, "diff", "--no-ext-diff", "--staged")
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

	// The embedded model must be resident BEFORE this request's budget
	// starts (ADR-066 D13): a cold load is never charged to the request.
	// The gate sits after the staged-diff check (a nothing-to-generate
	// call must not load gigabytes) and is a no-op for other providers;
	// while an agent task runs it DEFERS first (a different prompt would
	// evict the task's prompt cache). The whole wait is outside the
	// service timeout by design; each bound is derived once, on its
	// constant. See specs/domains/embedded-llm.md.
	if err := f.serviceEmbeddedGateInteractive(f.ctx()); err != nil {
		f.log().Warn("GenerateCommitMessage: the embedded service gate refused the request", "err", err)
		return "", err
	}

	ctx, cancel := context.WithTimeout(f.ctx(), f.serviceLLMTimeout())
	defer cancel()

	// The core layer logs the LLM-side cause; this boundary logs only its
	// own preconditions and passes the error through.
	return b.GenerateCommitMessage(ctx, trimmed)
}

// --- Remote operations RPCs -------------------------------------------------

// remoteGitCmdTimeout bounds pull/push/fetch, which can take noticeably
// longer than local git commands over a slow network. See
// specs/domains/git-auto-fetch.md.
const remoteGitCmdTimeout = 2 * time.Minute

// allowedRemoteFlags maps each remote operation to the git flags the
// frontend may pass; anything else is rejected at the RPC boundary. Each
// accepted flag is passed to git as a separate argv element.
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

// validateRemoteFlags rejects any flag outside the operation's
// allowedRemoteFlags set; an empty slice is always valid.
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
// branch (git pull <remote> [flags]); empty remote = git's configured
// upstream. Behavior, flags and the remote-op contract are specified in
// specs/contracts/desktop-frontend.md and specs/domains/git-auto-fetch.md.
func (f *FrontendAPI) Pull(remote string, flags []string) (string, error) {
	return f.runRemoteOp("pull", remote, flags)
}

// Push sends local commits to the named remote (git push <remote>
// [flags]). An empty remote pushes the current branch to its push remote,
// or publishes it when it has none — see the Push row in
// specs/contracts/desktop-frontend.md for the full precedence and refspec
// contract.
func (f *FrontendAPI) Push(remote string, flags []string) (string, error) {
	return f.runRemoteOp("push", remote, flags)
}

// Fetch downloads objects and refs from the named remote without merging
// (git fetch <remote> [flags]; empty remote = configured upstream).
// See the Pull doc for the shared contract sources.
func (f *FrontendAPI) Fetch(remote string, flags []string) (string, error) {
	return f.runRemoteOp("fetch", remote, flags)
}

// runSerializedRemoteOp executes one git remote operation under
// remoteOpMu — the single chokepoint for every network git operation
// (pull/push/fetch and the tag push/delete RPCs); the serialization
// contract with the auto-fetch funnel is specified in
// specs/domains/git-auto-fetch.md. Callers build the full argv; output
// is combined, capped at gitOutputLimit, bounded by remoteGitCmdTimeout;
// git:status_changed is emitted on success. Unexported by design — not a
// Wails RPC.
func (f *FrontendAPI) runSerializedRemoteOp(repoPath string, args ...string) (string, error) {
	f.remoteOpMu.Lock()
	defer f.remoteOpMu.Unlock()

	f.log().Debug("git remote operation", "args", args)

	out, err := f.runGitCmdCombined(repoPath, remoteGitCmdTimeout, args...)
	if err != nil {
		f.log().Warn("git remote operation failed", "args", args, "error", err)
		return out, err
	}
	f.emitGitStatusChanged(repoPath)
	return out, nil
}

// runRemoteOp is the shared body of Pull, Push and Fetch: it validates
// flags against allowedRemoteFlags, assembles the argv (op, optional
// remote, flags) and delegates to runSerializedRemoteOp. A push with an
// empty remote publishes the current branch instead of running a bare
// push (see the inline note). Unexported by design — not a Wails RPC.
func (f *FrontendAPI) runRemoteOp(op, remote string, flags []string) (string, error) {
	if err := validateRemoteFlags(op, flags); err != nil {
		return "", err
	}

	repoPath, err := f.resolveGitRepoRoot()
	if err != nil {
		return "", err
	}

	// A bare `git push` on an unpublished branch aborts with "fatal: The
	// current branch <name> has no upstream branch." The panel's push
	// button passes an empty remote, so push the current branch
	// explicitly: pushArgs sends it to its upstream, or publishes it (-u).
	// A detached HEAD falls through to the bare push, as git does.
	if op == "push" && strings.TrimSpace(remote) == "" {
		if branchName, berr := f.currentBranchName(repoPath); berr == nil && branchName != "" && branchName != "HEAD" {
			if verr := validateGitOperand("branch name", branchName); verr != nil {
				return "", verr
			}
			return f.runSerializedRemoteOp(repoPath, f.pushArgs(repoPath, branchName, flags)...)
		}
	}

	args := []string{op}
	if r := strings.TrimSpace(remote); r != "" {
		if err := validateGitOperand("remote name", r); err != nil {
			return "", err
		}
		args = append(args, r)
	}
	args = append(args, flags...)

	return f.runSerializedRemoteOp(repoPath, args...)
}

// --- Commit history RPCs ----------------------------------------------------

// validShaRe matches a plausible git SHA: 7-40 lowercase hex characters
// (7 = git's minimum abbreviation, 40 = full SHA).
var validShaRe = regexp.MustCompile(`^[0-9a-f]{7,40}$`)

// validateCommitSha checks that s is a plausible git SHA. It does not
// verify the SHA exists in the repository — git does that.
func validateCommitSha(s string) error {
	if !validShaRe.MatchString(s) {
		return fmt.Errorf("invalid commit SHA %q: must be 7-40 hex characters", s)
	}
	return nil
}

// validateGitOperand rejects a repository- or RPC-controlled git operand
// (branch/ref/remote/tag name) that git could parse as an OPTION instead
// of a value: git consumes every leading-dash argument up to "--", such
// operands are real (`git update-ref refs/heads/--mirror` or a crafted
// packed-refs creates them; GetBranches lists them verbatim into the
// pickers feeding these RPCs — reviews [26]/[33]/[53]/[54]/[105]), and
// one unguarded operand turns a panel action into e.g. `git checkout -f`
// (discards uncommitted work) or `git push --mirror` (deletes remote
// refs). Every RPC validates positionals here; the shell layer passes
// each name as a single argv element, so a leading dash is the one
// remaining injection channel.
// See specs/architecture/security-model.md (Git Subprocess Hardening,
// operand validation).
func validateGitOperand(kind, name string) error {
	if strings.HasPrefix(name, "-") {
		return fmt.Errorf("invalid %s %q: must not start with '-'", kind, name)
	}
	return nil
}

// GetCommitFiles returns the files changed by the given commit
// (git diff-tree --no-commit-id --name-status -r -M <sha>; -M reports a
// rename as a single "R" entry). Each result carries the status letter
// and the post-commit path (destination for renames/copies); empty slice
// (not nil) for a file-less commit. See resolveGitRepoRoot for the
// shared error contract.
func (f *FrontendAPI) GetCommitFiles(sha string) ([]CommitFile, error) {
	commit := strings.TrimSpace(sha)
	if commit == "" {
		return nil, errors.New("commit sha must not be empty")
	}
	if err := validateCommitSha(commit); err != nil {
		return nil, err
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

// GetCommitFilesBatch returns changed files for many commits in one
// git invocation (git log --no-walk=unsorted --name-status -r -M),
// replacing N GetCommitFiles calls for the history filter. SHAs are
// validated, resolved to full 40-char form via cat-file --batch-check,
// and fed through stdin (with the log call) to bypass ARG_MAX, so the
// result keys match git log's %H for any input abbreviation. Every
// requested SHA gets an entry (empty slice for file-less commits).
// See resolveGitRepoRoot for the shared error contract.
func (f *FrontendAPI) GetCommitFilesBatch(shas []string) (map[string][]CommitFile, error) {
	var trimmed []string
	for _, s := range shas {
		t := strings.TrimSpace(s)
		if t == "" {
			continue
		}
		if err := validateCommitSha(t); err != nil {
			return nil, err
		}
		trimmed = append(trimmed, t)
	}
	if len(trimmed) == 0 {
		return nil, errors.New("at least one commit sha must be provided")
	}

	repoPath, err := f.resolveGitRepoRoot()
	if err != nil {
		return nil, err
	}

	// Resolve SHAs to full 40-char form so the result-map keys match
	// git log's %H even for abbreviated input.
	fullShas, err := f.resolveCommitShas(repoPath, trimmed)
	if err != nil {
		return nil, err
	}

	// %x1e before each SHA delimits commits in the combined output so
	// parseCommitFilesBatch can split them; --stdin bypasses ARG_MAX.
	stdin := strings.Join(fullShas, "\n") + "\n"
	out, err := f.runGitCmdWithStdin(repoPath, stdin,
		"log", "--no-walk=unsorted", "--stdin", "--name-status", "-r", "-M", "--format=%x1e%H")
	if err != nil {
		return nil, err
	}

	return parseCommitFilesBatch(out, fullShas), nil
}

// resolveCommitShas resolves each (possibly abbreviated) SHA to its full
// 40-character form via `git cat-file --batch-check --stdin` (stdin
// bypasses ARG_MAX), so GetCommitFilesBatch's keys match git log's %H.
func (f *FrontendAPI) resolveCommitShas(repoPath string, shas []string) ([]string, error) {
	stdin := strings.Join(shas, "\n") + "\n"
	out, err := f.runGitCmdWithStdin(repoPath, stdin, "cat-file", "--batch-check")
	if err != nil {
		return nil, fmt.Errorf("resolve commit SHAs: %w", err)
	}
	lines := strings.Split(strings.TrimSpace(out), "\n")
	full := make([]string, 0, len(lines))
	for _, line := range lines {
		fields := strings.Fields(line)
		if len(fields) < 2 {
			continue
		}
		if fields[1] == "missing" {
			return nil, fmt.Errorf("resolve commit SHAs: %s not found", fields[0])
		}
		full = append(full, fields[0])
	}
	if len(full) != len(shas) {
		return nil, fmt.Errorf("resolve commit SHAs: expected %d resolved SHAs, got %d", len(shas), len(full))
	}
	return full, nil
}

// parseCommitFilesBatch parses `git log --no-walk=unsorted --name-status
// --format=%x1e%H` output: per commit a SHA line plus name-status lines
// (parseCommitFiles). All requested SHAs are pre-seeded with empty slices
// so the caller always receives a complete map.
func parseCommitFilesBatch(output string, shas []string) map[string][]CommitFile {
	result := make(map[string][]CommitFile, len(shas))
	for _, sha := range shas {
		result[sha] = []CommitFile{}
	}

	output = strings.TrimSpace(output)
	if output == "" {
		return result
	}

	blocks := strings.Split(output, "\x1e")
	for _, block := range blocks {
		block = strings.TrimSpace(block)
		if block == "" {
			continue
		}
		lines := strings.Split(block, "\n")
		sha := strings.TrimSpace(lines[0])
		if sha == "" {
			continue
		}
		var fileLines string
		if len(lines) > 1 {
			fileLines = strings.Join(lines[1:], "\n")
		}
		result[sha] = parseCommitFiles(fileLines)
	}
	return result
}

// parseCommitFiles parses diff-tree --name-status output: each line is
// "<status>\t<path>", or "<status>\t<old>\t<new>" for renames/copies
// (destination = last field). A similarity score ("R100") is normalized
// to its leading letter. Empty slice when output is empty.
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

// --- Stash RPCs -------------------------------------------------------------

// StashCreate saves the working-tree and index changes into a new stash
// (git stash push -m <message>; git's default message when empty). See
// resolveGitRepoRoot for the shared error contract.
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
// (git stash pop stash@{<index>); see resolveGitRepoRoot for the shared
// error contract.
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
// (git stash drop stash@{<index>); see resolveGitRepoRoot for the shared
// error contract.
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

// StashList returns the stashes of the active project's repository
// (git stash list), each entry carrying its index and message. Empty
// slice (not nil) when there are none; see resolveGitRepoRoot for the
// shared error contract.
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

// stashListRe matches one "git stash list" line: "stash@{<index>}: <message>".
var stashListRe = regexp.MustCompile(`^stash@\{(\d+)\}:\s*(.*)$`)

// parseStashList parses git stash list output; unparseable lines are
// skipped, empty output yields an empty slice.
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

// --- Context-menu & discard RPCs --------------------------------------------

// DiscardChanges reverts a file to HEAD, discarding staged and unstaged
// modifications: tracked files are unstaged (git reset HEAD) then
// restored (git checkout --); untracked files ("??") are removed with
// git clean -f; a staged NEW file (index column "A") is unstaged then
// cleaned — same routing as untracked (it has no committed counterpart).
// See resolveGitPath for the shared error contract.
func (f *FrontendAPI) DiscardChanges(path string) error {
	repoPath, relPath, err := f.resolveGitPath(path)
	if err != nil {
		return err
	}

	// Porcelain status distinguishes untracked files, which must be
	// cleaned rather than checked out.
	out, err := f.runGitCmd(repoPath, "status", "--porcelain", "--", relPath)
	if err != nil {
		return err
	}
	status := strings.TrimSpace(out)
	if status == "" {
		return nil
	}

	// Untracked ("XY" == "??") has no committed counterpart to restore.
	if len(status) >= 2 && status[0] == '?' && status[1] == '?' {
		if _, err := f.runGitCmd(repoPath, "clean", "-f", "--", relPath); err != nil {
			return err
		}
		f.emitGitStatusChanged(repoPath)
		return nil
	}

	// A staged NEW file ("A ", "AM", "AD" in the index column) also has no
	// committed counterpart: the tracked branch below would un-stage it
	// and then fail `git checkout --` with "pathspec did not match" —
	// file left on disk, confirmed discard silently denied (review [70]).
	// Un-stage, then clean, mirroring the untracked branch.
	if len(status) >= 2 && status[0] == 'A' {
		if _, err := f.runGitCmd(repoPath, "reset", "HEAD", "--", relPath); err != nil {
			return err
		}
		if _, err := f.runGitCmd(repoPath, "clean", "-f", "--", relPath); err != nil {
			return err
		}
		f.emitGitStatusChanged(repoPath)
		return nil
	}

	if _, err := f.runGitCmd(repoPath, "reset", "HEAD", "--", relPath); err != nil {
		return err
	}
	if _, err := f.runGitCmd(repoPath, "checkout", "--", relPath); err != nil {
		return err
	}
	f.emitGitStatusChanged(repoPath)
	return nil
}

// AppendToGitignore appends pattern to the repository-root .gitignore
// (creating the file when missing); an already-present pattern is a
// no-op. Emits git:status_changed on success so the frontend refreshes
// the (now possibly unignored) worktree. See resolveGitRepoRoot for the
// shared error contract.
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

	// A symlinked .gitignore is refused (review [4]): a git-tracked
	// symlink in an untrusted repo would route both this read and the
	// append's rewrite to any path the process can write — an
	// out-of-workspace modification with attacker-chosen lines.
	// safeio.ReadFile refuses non-regular targets (a planted FIFO would
	// hang read and write-open forever) but follows a symlink to a regular
	// file, so the explicit Lstat is the first-line guard for the write
	// too (no-follow open on unix; Windows still resolves a final symlink,
	// tempered by symlink creation needing elevated/dev-mode rights).
	if info, lerr := os.Lstat(gitignorePath); lerr == nil && info.Mode()&os.ModeSymlink != 0 {
		return fmt.Errorf("refusing to append to .gitignore: %s is a symlink", gitignorePath)
	}

	// Read through safeio for duplicate detection; a missing file is
	// expected (empty), any other non-regular target fails closed instead
	// of hanging the synchronous RPC.
	data, err := safeio.ReadFile(gitignorePath)
	if err != nil && !errors.Is(err, fs.ErrNotExist) {
		return fmt.Errorf("read .gitignore: %w", err)
	}
	existing := string(data)
	if patternAlreadyIgnored(existing, p) {
		return nil
	}

	content := existing
	if content != "" && !strings.HasSuffix(content, "\n") {
		content += "\n"
	}
	content += p + "\n"

	if err := safeio.WriteFile(gitignorePath, []byte(content), 0o644); err != nil {
		return fmt.Errorf("write .gitignore: %w", err)
	}

	f.emitGitStatusChanged(repoPath)
	return nil
}

// patternAlreadyIgnored reports whether pattern already appears as a
// line in content (whitespace-trimmed comparison).
func patternAlreadyIgnored(content, pattern string) bool {
	for _, line := range strings.Split(content, "\n") {
		if strings.TrimSpace(line) == pattern {
			return true
		}
	}
	return false
}

// --- Merge / rebase RPCs ----------------------------------------------------

// Merge integrates the named branch into the current branch
// (git merge <branch>). See resolveGitRepoRoot for the shared error
// contract.
func (f *FrontendAPI) Merge(branch string) error {
	branchName := strings.TrimSpace(branch)
	if branchName == "" {
		return errors.New("branch name must not be empty")
	}
	if err := validateGitOperand("branch name", branchName); err != nil {
		return err
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
// (git rebase <branch>). See resolveGitRepoRoot for the shared error
// contract.
func (f *FrontendAPI) Rebase(branch string) error {
	branchName := strings.TrimSpace(branch)
	if branchName == "" {
		return errors.New("branch name must not be empty")
	}
	if err := validateGitOperand("branch name", branchName); err != nil {
		return err
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
// the pre-merge state; see resolveGitRepoRoot for the shared error
// contract.
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
// restoring the pre-rebase state; see resolveGitRepoRoot for the shared
// error contract.
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

// ResetToCommit moves the current branch HEAD to the named commit
// (git reset --<mode> <sha>): "soft" keeps index and working tree,
// "mixed" resets the index only, "hard" discards tracked working-tree
// changes — which makes the sha validation more than defense-in-depth
// here. See resolveGitRepoRoot for the shared error contract.
func (f *FrontendAPI) ResetToCommit(sha, mode string) error {
	resetSha := strings.TrimSpace(sha)
	if resetSha == "" {
		return errors.New("sha must not be empty")
	}
	if err := validateCommitSha(resetSha); err != nil {
		return err
	}

	switch mode {
	case "soft", "mixed", "hard":
	default:
		return errors.New("invalid reset mode: must be soft, mixed, or hard")
	}

	repoPath, err := f.resolveGitRepoRoot()
	if err != nil {
		return err
	}

	if _, err := f.runGitCmd(repoPath, "reset", "--"+mode, resetSha); err != nil {
		return err
	}
	f.emitGitStatusChanged(repoPath)
	return nil
}

// GetRebaseMergeState reports whether a merge (MERGE_HEAD) or a rebase
// (rebase-apply / rebase-merge directory) is in progress. The git
// directory is resolved via git rev-parse --git-dir so worktrees and
// GIT_DIR overrides are handled; see resolveGitRepoRoot for the shared
// error contract.
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

func isRebaseActive(gitDir string) bool {
	for _, name := range []string{"rebase-apply", "rebase-merge"} {
		if info, err := os.Stat(filepath.Join(gitDir, name)); err == nil && info.IsDir() {
			return true
		}
	}
	return false
}

// GetIsGitRepo reports whether the active project's workspace is inside
// a git work tree — the UI's availability gate for the whole Git
// workspace panel. Delegates to the 30s-cached isGitRepo check (git
// rev-parse --is-inside-work-tree) so rapid re-checks do not spawn a git
// process each time; unscannable repo config reports not-a-repo (fail
// closed). The frontend treats any rejection as "not a repository".
func (f *FrontendAPI) GetIsGitRepo() (bool, error) {
	repoPath, err := f.resolveGitRepoRoot()
	if err != nil {
		return false, err
	}
	return f.isGitRepo(repoPath), nil
}

// --- Commit graph RPC -------------------------------------------------------

// Page-size bounds for GetGitHistory: the default keeps the first
// request bounded for large repositories; the cap protects the UI from
// an unbounded git-log payload.
const (
	gitHistoryDefaultLimit = 300
	gitHistoryMaxLimit     = 1000
)

func normalizeGitHistoryLimit(limit int) int {
	if limit <= 0 {
		return gitHistoryDefaultLimit
	}
	if limit > gitHistoryMaxLimit {
		return gitHistoryMaxLimit
	}
	return limit
}

// GetGitHistory returns one page of the unified commit history for the
// history+graph view — each commit carries both log fields
// (author/email/date/message) and graph topology (parents/refs) — newest
// first via `git log -n <limit> --skip <skip>`. limit<=0 →
// gitHistoryDefaultLimit, above the cap clamped, skip<0 → 0; HasMore is
// true when the page was saturated. See the GetGitHistory row in
// specs/contracts/desktop-frontend.md and resolveGitRepoRoot for the
// shared error contract.
func (f *FrontendAPI) GetGitHistory(limit, skip int) (*GitHistoryPage, error) {
	repoPath, err := f.resolveGitRepoRoot()
	if err != nil {
		return nil, err
	}

	limit = normalizeGitHistoryLimit(limit)
	if skip < 0 {
		skip = 0
	}

	// Control-character separators so commit messages containing "|" or
	// newlines cannot corrupt parsing: %x1f between fields, %x1e between
	// commits. Fields: SHA, parents, author, email, date, subject, ref
	// decorations.
	out, err := f.runGitCmd(repoPath, "log",
		"-n", strconv.Itoa(limit),
		"--skip="+strconv.Itoa(skip),
		"--format=%H%x1f%P%x1f%an%x1f%ae%x1f%ad%x1f%s%x1f%d%x1e",
	)
	if err != nil {
		return nil, err
	}

	commits := parseGitHistory(out)
	hasMore := len(commits) == limit // saturated page → git may have more
	return &GitHistoryPage{
		Commits:  commits,
		NextSkip: skip + len(commits),
		HasMore:  hasMore,
	}, nil
}

// parseGitHistory parses the %x1f/%x1e-separated git log format produced
// by GetGitHistory (SHA, parents, author, email, date, subject, refs);
// records with fewer than seven fields are skipped. Empty slice when
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
		// Non-nil so JSON emits "[]" instead of "null" for root commits —
		// the frontend's Array.isArray(parents) guard rejects null.
		parents := []string{}
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

// parseGitRefs parses the %d decoration field (" (HEAD -> main,
// tag: v1.0)"); empty slice when there are no decorations.
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

// --- Hunk-level staging RPC -------------------------------------------------

// hunkHeaderRe matches a unified-diff hunk header, capturing old/new
// start lines and (optional) counts — omitted counts default to 1 per
// the unified-diff spec.
var hunkHeaderRe = regexp.MustCompile(`^@@ -(\d+)(?:,(\d+))? \+(\d+)(?:,(\d+))? @@`)

// parseHunkInfos parses a unified diff into one HunkDiffInfo per hunk;
// each entry's Diff carries the hunk's header and body, and staged lets
// the caller tell `git diff --cached` hunks from `git diff` ones. Nil
// when diff is empty.
func parseHunkInfos(diff string, staged bool) []HunkDiffInfo {
	if diff == "" {
		return nil
	}
	lines := strings.Split(diff, "\n")
	var hunks []HunkDiffInfo
	i := 0
	for i < len(lines) {
		if !strings.HasPrefix(lines[i], "@@") {
			i++
			continue
		}
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
		newStart, _ := strconv.Atoi(m[3])
		newCount := 1
		if m[4] != "" {
			newCount, _ = strconv.Atoi(m[4])
		}

		// Full hunk block: header + body up to the next header or EOF.
		blockStart := i
		i++
		for i < len(lines) && !strings.HasPrefix(lines[i], "@@") {
			i++
		}
		block := strings.Join(lines[blockStart:i], "\n")

		// The header start includes context lines, so OldStart/NewStart
		// point at the first context line, not the first changed one:
		// walk the body tracking old/new line numbers and record the
		// first non-context position.
		oldLine := oldStart
		newLine := newStart
		oldChangeStart := oldStart
		newChangeStart := newStart
		found := false
		for _, bodyLine := range lines[blockStart+1 : i] {
			if bodyLine == "" {
				continue
			}
			switch bodyLine[0] {
			case ' ':
				oldLine++
				newLine++
			case '-':
				if !found {
					oldChangeStart = oldLine
					newChangeStart = newLine
					found = true
				}
				oldLine++
			case '+':
				if !found {
					oldChangeStart = oldLine
					newChangeStart = newLine
					found = true
				}
				newLine++
			case '\\':
				// "\ No newline at end of file" — skip
			}
		}

		hunks = append(hunks, HunkDiffInfo{
			OldStart:       oldStart,
			OldCount:       oldCount,
			NewStart:       newStart,
			NewCount:       newCount,
			OldChangeStart: oldChangeStart,
			NewChangeStart: newChangeStart,
			Staged:         staged,
			Diff:           block,
		})
	}
	return hunks
}

// GetFileDiffHunks returns structured per-hunk information for a file's
// uncommitted changes, distinguishing staged hunks (HEAD vs index) from
// unstaged hunks (index vs worktree); each hunk carries its line-range
// metadata, a staged flag, and the raw unified-diff block (for tooltip
// display). Read-only. See resolveGitPath for the shared error contract.
func (f *FrontendAPI) GetFileDiffHunks(filePath string) ([]HunkDiffInfo, error) {
	repoPath, relPath, err := f.resolveGitPath(filePath)
	if err != nil {
		return nil, err
	}

	// --no-ext-diff per the ADR-033 porcelain rule, mirroring
	// GenerateCommitMessage's hardened diff invocation.
	stagedDiff, err := f.runGitCmd(repoPath, "diff", "--no-ext-diff", "--cached", "--", relPath)
	if err != nil {
		return nil, err
	}

	// Unstaged: index vs worktree.
	unstagedDiff, err := f.runGitCmd(repoPath, "diff", "--no-ext-diff", "--", relPath)
	if err != nil {
		return nil, err
	}

	hunks := parseHunkInfos(strings.TrimSpace(stagedDiff), true)
	hunks = append(hunks, parseHunkInfos(strings.TrimSpace(unstagedDiff), false)...)
	return hunks, nil
}
