package workspace

import (
	"bytes"
	"context"
	"errors"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"sync"
	"unicode"

	"github.com/v0lka/c0wrk/core"
	"github.com/v0lka/sp4rk/pathutil"
	"github.com/v0lka/sp4rk/safeio"
)

// Worktree primitives: the safe git-worktree service every higher layer
// (backend ownership coordinator, session restore, the Git panel) goes
// through. Every git invocation is routed through GitCmdInRepo's scan
// pipeline (gitCmdInRepoScanned), so the full spawn-layer hardening —
// fresh config scan, neutralizing argv, hooks/filters/transport
// neutralization, trusted-repo opt-out — applies to worktree operations
// exactly as it does to status/diff/commit. Worktrees are especially
// sensitive here: `git worktree add` performs a checkout (clean/smudge
// filters, post-checkout hook) and reads config from BOTH the common dir
// and the per-worktree `config.worktree` overlay — all covered by the
// scanner.

// ---------------------------------------------------------------------------
// Kinds, entry model
// ---------------------------------------------------------------------------

// WorktreeKind classifies one entry of `git worktree list`.
type WorktreeKind string

const (
	// WorktreeMain is the repository's primary checkout (the "local"
	// workspace of a project). Exactly one per repository; a bare
	// repository reports its gitdir as the main entry with Bare=true.
	WorktreeMain WorktreeKind = "main"
	// WorktreeManaged is a linked worktree inside the app-managed container
	// <repoRoot>/.worktrees (session-owned trees, ADR-080).
	WorktreeManaged WorktreeKind = "managed"
	// WorktreeExternal is a linked worktree outside the managed container —
	// created by the user or other tooling, never owned by c0wrk.
	WorktreeExternal WorktreeKind = "external"
)

// WorktreeInfo is one structured entry of the repository's worktree list.
// Branch is the short branch name ("main"), empty for detached or bare
// entries. Locked/Prunable carry git's reasons verbatim when present.
type WorktreeInfo struct {
	Path           string       `json:"path"`
	Name           string       `json:"name"` // base name of Path
	Kind           WorktreeKind `json:"kind"`
	Head           string       `json:"head"`
	Branch         string       `json:"branch,omitempty"`
	Detached       bool         `json:"detached,omitempty"`
	Bare           bool         `json:"bare,omitempty"`
	Locked         bool         `json:"locked,omitempty"`
	LockedReason   string       `json:"locked_reason,omitempty"`
	Prunable       bool         `json:"prunable,omitempty"`
	PrunableReason string       `json:"prunable_reason,omitempty"`
}

// IsLinked reports whether the entry is a linked worktree (managed or
// external), i.e. anything `git worktree remove` could remove.
func (w WorktreeInfo) IsLinked() bool {
	return w.Kind == WorktreeManaged || w.Kind == WorktreeExternal
}

// BranchHolders maps each checked-out branch to the worktree path holding
// it. git refuses to check the same branch out in two worktrees; this map
// is how the primitives make that refusal explicit BEFORE spawning a
// doomed `git worktree add`.
func BranchHolders(trees []WorktreeInfo) map[string]string {
	holders := make(map[string]string, len(trees))
	for _, w := range trees {
		if w.Branch != "" {
			holders[w.Branch] = w.Path
		}
	}
	return holders
}

// FindWorktree returns the entry whose path equals target (compared as
// clean absolute paths), or nil when target is not a worktree of this
// repository.
func FindWorktree(trees []WorktreeInfo, target string) *WorktreeInfo {
	clean := filepath.Clean(target)
	for i := range trees {
		if trees[i].Path == clean {
			return &trees[i]
		}
	}
	return nil
}

// ---------------------------------------------------------------------------
// Explicit failure taxonomy
// ---------------------------------------------------------------------------

// Worktree operation failures are explicit and matchable with errors.Is;
// StateError carries git's verbatim reason where one exists.
var (
	// ErrWorktreeMalformed: git's `worktree list --porcelain` output
	// violated the documented format. Fail-closed: the listing is refused
	// rather than silently truncated.
	ErrWorktreeMalformed = errors.New("malformed git worktree output")
	// ErrWorktreeExists: the target path already exists. Creation never
	// overwrites foreign directories (--force is never passed to add).
	ErrWorktreeExists = errors.New("worktree path already exists")
	// ErrWorktreePathInvalid: structural path validation failed (relative,
	// unclean, contains the repository, inside .git, a linked worktree, the
	// managed container itself, or has a symlinked ancestor).
	ErrWorktreePathInvalid = errors.New("invalid worktree path")
	// ErrBranchBusy: the branch is checked out in another worktree.
	ErrBranchBusy = errors.New("branch is checked out in another worktree")
	// ErrBranchExists: branch creation (-b) targeted an existing ref.
	ErrBranchExists = errors.New("branch already exists")
	// ErrBranchMissing: the branch does not exist. Restore recreates trees,
	// never branches — a missing branch is an explicit failure.
	ErrBranchMissing = errors.New("branch does not exist")
	// ErrWorktreeLocked: the worktree is locked; removal requires an
	// explicit unlock decision, never a silent override.
	ErrWorktreeLocked = errors.New("worktree is locked")
	// ErrWorktreeDirty: the worktree has uncommitted changes; removal
	// requires an explicit loss confirmation (Force).
	ErrWorktreeDirty = errors.New("worktree has uncommitted changes")
	// ErrWorktreePrunable: the worktree's directory is gone but its
	// administrative metadata remains; pruning is required before the path
	// can be reused.
	ErrWorktreePrunable = errors.New("worktree is prunable (stale metadata)")
	// ErrWorktreeMain: the target is the repository's main worktree, which
	// can never be removed as a worktree.
	ErrWorktreeMain = errors.New("path is the repository's main worktree")
	// ErrWorktreeNotLinked: the target is not a linked worktree of this
	// repository (foreign directory or the checkout itself).
	ErrWorktreeNotLinked = errors.New("path is not a linked worktree of this repository")
	// ErrWorktreeBranchMismatch: an existing worktree at the target is on a
	// different branch than the pinned one; recreate never silently
	// retargets a tree.
	ErrWorktreeBranchMismatch = errors.New("worktree is on a different branch than requested")
	// ErrRefInvalid: a branch or start-point ref failed validation.
	ErrRefInvalid = errors.New("invalid ref name")
)

// StateError attaches a verbatim reason (locked/prunable text, git stderr)
// to a sentinel so callers can errors.Is the sentinel and errors.As the
// reason out in one match.
type StateError struct {
	// Sentinel is the matching ErrWorktree* (or generic) error.
	Sentinel error
	// Reason is git's own explanation (locked/prunable reason, stderr).
	Reason string
}

func (e *StateError) Error() string {
	if e.Reason == "" {
		return e.Sentinel.Error()
	}
	return e.Sentinel.Error() + ": " + e.Reason
}

func (e *StateError) Unwrap() error { return e.Sentinel }

// ---------------------------------------------------------------------------
// Per-repo serialization of conflicting operations
// ---------------------------------------------------------------------------

// git serializes its own worktree metadata writes, but two concurrent
// `git worktree add` invocations for the SAME branch can interleave
// check-then-create with each other, leaving the loser with a raw exit
// code instead of an explicit verdict. The primitives serialize all
// mutating worktree operations per repository (process-wide: one desktop
// process) and run their validation inside the serialized section, so
// concurrent conflicting creates cannot BOTH succeed and every failure is
// a typed error.
var (
	worktreeOpsMu    sync.Mutex
	worktreeOpsLocks = make(map[string]*sync.Mutex)
)

func lockWorktreeOps(repoRoot string) func() {
	key := filepath.Clean(repoRoot)
	worktreeOpsMu.Lock()
	mu, ok := worktreeOpsLocks[key]
	if !ok {
		mu = &sync.Mutex{}
		worktreeOpsLocks[key] = mu
	}
	worktreeOpsMu.Unlock()
	mu.Lock()
	return mu.Unlock
}

// ---------------------------------------------------------------------------
// Hardened git runner (operation-scoped scan reuse)
// ---------------------------------------------------------------------------

// runWorktreeGit executes one git invocation inside a logical worktree
// operation. scan is the operation's memo (created over repoRoot or over a
// worktree path — the command's working directory is always scan.path),
// reusing one config scan across the operation's invocations, exactly as
// GitCmdInRepo sanctions for multi-invocation operations.
func runWorktreeGit(ctx context.Context, scan *gitScanMemo, args ...string) (string, error) {
	cmd, err := gitCmdInRepoScanned(ctx, scan, args...)
	if err != nil {
		return "", err
	}
	var stdout, stderr bytes.Buffer
	cmd.Stdout = &stdout
	cmd.Stderr = &stderr
	if err := cmd.Run(); err != nil {
		var exitErr *exec.ExitError
		if errors.As(err, &exitErr) {
			return stdout.String(), &StateError{
				Sentinel: fmt.Errorf("git %s failed", strings.Join(args, " ")),
				Reason:   strings.TrimSpace(stderr.String()),
			}
		}
		return "", fmt.Errorf("git %s: %w", strings.Join(args, " "), err)
	}
	return stdout.String(), nil
}

// ---------------------------------------------------------------------------
// Listing
// ---------------------------------------------------------------------------

// ListWorktrees returns the structured worktree list of the repository at
// repoRoot: the main checkout ("local" discovery), app-managed trees under
// <repoRoot>/.worktrees, and external linked worktrees elsewhere. Output
// that violates the porcelain format fails closed with an error matching
// ErrWorktreeMalformed — never a silently truncated list.
func ListWorktrees(ctx context.Context, repoRoot string) ([]WorktreeInfo, error) {
	root, err := cleanRepoRoot(repoRoot)
	if err != nil {
		return nil, err
	}
	scan := newGitScanMemo(root)
	out, runErr := runWorktreeGit(ctx, scan, "worktree", "list", "--porcelain")
	if runErr != nil {
		return nil, fmt.Errorf("git worktree list: %w", runErr)
	}
	trees, err := parseWorktreePorcelain(out)
	if err != nil {
		return nil, err
	}
	classifyWorktrees(trees, root)
	return trees, nil
}

// parseWorktreePorcelain strictly parses `git worktree list --porcelain`
// output. Grammar (git-worktree docs): entries separated by blank lines;
// each entry starts `worktree <absolute-path>` followed by `HEAD <sha>`
// and any of `branch <ref>` | `detached`, `bare`, `locked [reason]`,
// `prunable [reason]` — except a bare repository, whose entry is only
// `worktree <path>` + `bare` (git emits no HEAD there). Deviations —
// unknown attribute, entry missing its worktree line or its HEAD line
// (when not bare), non-absolute path, empty stream — are
// ErrWorktreeMalformed. Fail-closed matters: a partially parsed list would
// silently hide trees that the branch-occupancy and ownership checks
// depend on.
func parseWorktreePorcelain(out string) ([]WorktreeInfo, error) {
	var trees []WorktreeInfo
	var cur *WorktreeInfo
	flush := func() error {
		if cur == nil {
			return nil
		}
		// A bare repository emits `worktree <path>` + `bare` with NO HEAD
		// line (verified on git 2.55.0, with and without commits) — only a
		// non-bare entry must carry HEAD (review [95]; the package doc at
		// the top of the file documents the bare main entry with
		// Bare=true).
		if cur.Path == "" || (cur.Head == "" && !cur.Bare) {
			return fmt.Errorf("%w: entry missing worktree/HEAD line", ErrWorktreeMalformed)
		}
		trees = append(trees, *cur)
		cur = nil
		return nil
	}
	for lineNo, raw := range strings.Split(out, "\n") {
		line := strings.TrimRight(raw, "\r")
		switch {
		case line == "":
			if err := flush(); err != nil {
				return nil, err
			}
		case strings.HasPrefix(line, "worktree "):
			if err := flush(); err != nil {
				return nil, err
			}
			path := strings.TrimSpace(strings.TrimPrefix(line, "worktree "))
			if !filepath.IsAbs(path) {
				return nil, fmt.Errorf("%w: non-absolute worktree path %q", ErrWorktreeMalformed, path)
			}
			cur = &WorktreeInfo{Path: filepath.Clean(path)}
		case cur == nil:
			return nil, fmt.Errorf("%w: attribute before worktree header: %q", ErrWorktreeMalformed, line)
		case strings.HasPrefix(line, "HEAD "):
			if cur.Head != "" {
				return nil, fmt.Errorf("%w: duplicate HEAD in entry %q", ErrWorktreeMalformed, cur.Path)
			}
			cur.Head = strings.TrimSpace(strings.TrimPrefix(line, "HEAD "))
		case strings.HasPrefix(line, "branch "):
			ref := strings.TrimSpace(strings.TrimPrefix(line, "branch "))
			cur.Branch = strings.TrimPrefix(ref, "refs/heads/")
		case line == "detached":
			cur.Detached = true
		case line == "bare":
			cur.Bare = true
		case strings.HasPrefix(line, "locked"):
			cur.Locked = true
			cur.LockedReason = strings.TrimSpace(strings.TrimPrefix(line, "locked"))
		case strings.HasPrefix(line, "prunable"):
			cur.Prunable = true
			cur.PrunableReason = strings.TrimSpace(strings.TrimPrefix(line, "prunable"))
		default:
			return nil, fmt.Errorf("%w: unknown attribute %q at line %d", ErrWorktreeMalformed, line, lineNo+1)
		}
	}
	if err := flush(); err != nil {
		return nil, err
	}
	if len(trees) == 0 {
		return nil, fmt.Errorf("%w: empty worktree list", ErrWorktreeMalformed)
	}
	for i := range trees {
		trees[i].Name = filepath.Base(trees[i].Path)
	}
	return trees, nil
}

// classifyWorktrees stamps each entry's Kind against the repository root:
// the first (primary) entry is the main checkout; linked entries inside
// <root>/.worktrees are managed; every other linked entry is external.
func classifyWorktrees(trees []WorktreeInfo, root string) {
	container := filepath.Join(root, core.WorktreesRelativePath)
	for i := range trees {
		if i == 0 {
			trees[i].Kind = WorktreeMain
			continue
		}
		if within, err := pathutil.IsWithinPath(container, trees[i].Path); err == nil && within {
			trees[i].Kind = WorktreeManaged
		} else {
			trees[i].Kind = WorktreeExternal
		}
	}
}

// ---------------------------------------------------------------------------
// Creation
// ---------------------------------------------------------------------------

// AddWorktreeOptions selects what `git worktree add` checks out at target.
type AddWorktreeOptions struct {
	// Branch names the branch to create (CreateBranch) or check out.
	Branch string
	// CreateBranch makes the add create Branch at StartPoint (`-b`); the
	// default checks out the existing Branch.
	CreateBranch bool
	// StartPoint is the ref a created branch starts from (CreateBranch
	// only); empty means HEAD. Ignored otherwise.
	StartPoint string
	// Detached checks out StartPoint (or HEAD) with no branch at all.
	Detached bool
}

// AddWorktree creates a linked worktree at target (absolute, not yet
// existing) in the repository at repoRoot, serialized against every other
// mutating worktree operation on the same repository. Validations run
// before git, inside the serialized section: structural path safety
// (absolute, clean, not the checkout or an ancestor of it, not inside
// .git, not the managed container, not inside another linked worktree, no
// symlinked ancestor — foreign directories are never overwritten, and
// --force is never passed) and branch occupancy (the same branch cannot be
// checked out in two worktrees; creating over an existing ref or checking
// out a held branch fails explicitly). Managed targets additionally
// install the `/.worktrees/` exclusion in the common dir's info/exclude
// (never .gitignore). The returned info is the verified post-add list
// entry.
func AddWorktree(ctx context.Context, repoRoot, target string, opts AddWorktreeOptions) (WorktreeInfo, error) {
	root, err := cleanRepoRoot(repoRoot)
	if err != nil {
		return WorktreeInfo{}, err
	}
	unlock := lockWorktreeOps(root)
	defer unlock()
	return addWorktreeLocked(ctx, root, filepath.Clean(target), opts)
}

// addWorktreeLocked is AddWorktree's body; the caller holds the per-repo
// worktree-operation lock (RecreateWorktree reuses it while already
// holding the lock — the lock is not reentrant).
func addWorktreeLocked(ctx context.Context, root, target string, opts AddWorktreeOptions) (WorktreeInfo, error) {
	if err := validateWorktreeTarget(root, target); err != nil {
		return WorktreeInfo{}, err
	}
	if !opts.Detached {
		if err := validateRefName(opts.Branch); err != nil {
			return WorktreeInfo{}, fmt.Errorf("branch: %w", err)
		}
	}
	if opts.StartPoint != "" {
		if err := validateRefName(opts.StartPoint); err != nil {
			return WorktreeInfo{}, fmt.Errorf("start point: %w", err)
		}
	}

	trees, err := ListWorktrees(ctx, root)
	if err != nil {
		return WorktreeInfo{}, err
	}
	// The target must not be inside another linked worktree (nested
	// worktrees would confuse ownership; the managed container is a
	// top-level child of the checkout).
	for _, w := range trees {
		if !w.IsLinked() {
			continue
		}
		if within, werr := pathutil.IsWithinPath(w.Path, target); werr == nil && within {
			return WorktreeInfo{}, fmt.Errorf("%w: target is inside linked worktree %s", ErrWorktreePathInvalid, w.Path)
		}
	}

	scan := newGitScanMemo(root)
	if !opts.Detached {
		if err := validateBranchAvailability(ctx, scan, trees, opts); err != nil {
			return WorktreeInfo{}, err
		}
	}

	// No --force in any form: a pre-existing target was rejected above, and
	// if the filesystem changes underneath, git's own refusal stands.
	// Argv shapes (verified against git 2.5x):
	//   create:   worktree add -b <branch> -- <target> [<startpoint>]
	//   checkout: worktree add -- <target> <branch>
	//   detached: worktree add --detach -- <target> [<startpoint>]
	// Without an explicit commit-ish after the path, git creates a branch
	// named after the target's basename — never what a caller pinned — so
	// the branch argument is always passed explicitly.
	args := []string{"worktree", "add"}
	if opts.Detached {
		args = append(args, "--detach")
	}
	if opts.CreateBranch {
		args = append(args, "-b", opts.Branch)
	}
	args = append(args, "--", target)
	switch {
	case opts.CreateBranch && opts.StartPoint != "":
		args = append(args, opts.StartPoint)
	case opts.Detached && opts.StartPoint != "":
		args = append(args, opts.StartPoint)
	case !opts.CreateBranch && !opts.Detached:
		args = append(args, opts.Branch)
	}
	if _, runErr := runWorktreeGit(ctx, scan, args...); runErr != nil {
		return WorktreeInfo{}, classifyAddFailure(runErr, target)
	}

	if err := ensureWorktreesExcluded(ctx, scan, root, target); err != nil {
		return WorktreeInfo{}, err
	}

	after, err := ListWorktrees(ctx, root)
	if err != nil {
		return WorktreeInfo{}, err
	}
	entry := FindWorktree(after, target)
	if entry == nil {
		return WorktreeInfo{}, fmt.Errorf("worktree add reported success but %q is not in the worktree list", target)
	}
	if !opts.Detached && entry.Branch != opts.Branch {
		return WorktreeInfo{}, fmt.Errorf("worktree add reported success but %q is on %q, not %q", target, entry.Branch, opts.Branch)
	}
	return *entry, nil
}

// validateBranchAvailability enforces one-branch-one-worktree inside the
// serialized section: creating over an existing ref and checking out a
// branch another worktree holds are explicit errors before git runs. A
// checkout-mode add also requires the branch to exist — creation never
// invents branches; restoring a pinned branch is RecreateWorktree's job.
func validateBranchAvailability(ctx context.Context, scan *gitScanMemo, trees []WorktreeInfo, opts AddWorktreeOptions) error {
	if holder, busy := BranchHolders(trees)[opts.Branch]; busy {
		return fmt.Errorf("%w: %s held by %s", ErrBranchBusy, opts.Branch, holder)
	}
	exists, err := branchExists(ctx, scan, opts.Branch)
	if err != nil {
		return err
	}
	if opts.CreateBranch && exists {
		return fmt.Errorf("%w: %s", ErrBranchExists, opts.Branch)
	}
	if !opts.CreateBranch && !exists {
		return fmt.Errorf("%w: %s", ErrBranchMissing, opts.Branch)
	}
	return nil
}

// branchExists checks refs/heads/<branch> without DWIM resolution, so a
// branch name can never silently resolve to a tag or remote ref here.
func branchExists(ctx context.Context, scan *gitScanMemo, branch string) (bool, error) {
	out, err := runWorktreeGit(ctx, scan, "rev-parse", "--verify", "--quiet", "refs/heads/"+branch)
	if err != nil {
		var state *StateError
		// --quiet: exit 1 with empty output is "ref does not exist".
		if errors.As(err, &state) && strings.TrimSpace(out) == "" {
			return false, nil
		}
		return false, fmt.Errorf("checking branch %s: %w", branch, err)
	}
	return strings.TrimSpace(out) != "", nil
}

// classifyAddFailure maps git's raw add refusals onto the taxonomy, so a
// race lost between validation and git still yields an explicit verdict.
func classifyAddFailure(err error, target string) error {
	var state *StateError
	if !errors.As(err, &state) {
		return fmt.Errorf("git worktree add %s: %w", target, err)
	}
	msg := strings.ToLower(state.Reason)
	switch {
	case strings.Contains(msg, "already exists"):
		if strings.Contains(msg, "branch") || strings.Contains(msg, "refs/heads") {
			return fmt.Errorf("%w: %s", ErrBranchExists, state.Reason)
		}
		return fmt.Errorf("%w: %s", ErrWorktreeExists, state.Reason)
	case strings.Contains(msg, "already checked out"):
		return fmt.Errorf("%w: %s", ErrBranchBusy, state.Reason)
	case strings.Contains(msg, "invalid reference") ||
		strings.Contains(msg, "not a valid object name") ||
		strings.Contains(msg, "unknown revision"):
		return fmt.Errorf("%w: %s", ErrBranchMissing, state.Reason)
	}
	return fmt.Errorf("git worktree add %s: %w", target, err)
}

// validateWorktreeTarget enforces the structural path rules shared by add
// and recreate: absolute and lexically clean; not the repository checkout
// itself nor an ancestor of it; not inside the repository's .git; not the
// managed container itself; no existing ancestor may be a symlink (a
// symlinked prefix would relocate the tree on disk — the same refusal
// config.ManagedWorktreePath applies to the container); and the target
// must not exist: creation never overwrites foreign directories.
func validateWorktreeTarget(root, target string) error {
	if !filepath.IsAbs(target) || filepath.Clean(target) != target {
		return fmt.Errorf("%w: target must be an absolute clean path, got %q", ErrWorktreePathInvalid, target)
	}
	target = filepath.Clean(target)
	if target == root {
		return fmt.Errorf("%w: target is the repository root", ErrWorktreePathInvalid)
	}
	if within, err := pathutil.IsWithinPath(target, root); err == nil && within {
		return fmt.Errorf("%w: target contains the repository root", ErrWorktreePathInvalid)
	}
	container := filepath.Join(root, core.WorktreesRelativePath)
	if target == container {
		return fmt.Errorf("%w: target is the managed worktree container", ErrWorktreePathInvalid)
	}
	gitDir := filepath.Join(root, ".git")
	if within, err := pathutil.IsWithinPath(gitDir, target); err == nil && within {
		return fmt.Errorf("%w: target is inside %s", ErrWorktreePathInvalid, gitDir)
	}
	if _, err := os.Lstat(target); err == nil {
		return fmt.Errorf("%w: %s exists", ErrWorktreeExists, target)
	} else if !errors.Is(err, os.ErrNotExist) {
		return fmt.Errorf("%w: inspecting target: %w", ErrWorktreePathInvalid, err)
	}
	// Walk the existing ancestors: a symlink anywhere on the chain would
	// relocate the tree (or a prefix of its path) somewhere else on disk.
	ancestor := filepath.Dir(target)
	for ancestor != root && ancestor != filepath.Dir(ancestor) {
		fi, err := os.Lstat(ancestor)
		if err != nil {
			if errors.Is(err, os.ErrNotExist) {
				break // nothing exists that deep yet
			}
			return fmt.Errorf("%w: inspecting ancestor %s: %w", ErrWorktreePathInvalid, ancestor, err)
		}
		if fi.Mode()&os.ModeSymlink != 0 {
			return fmt.Errorf("%w: ancestor %s is a symlink", ErrWorktreePathInvalid, ancestor)
		}
		ancestor = filepath.Dir(ancestor)
	}
	return nil
}

// validateRefName rejects ref spellings that could be interpreted as git
// options or refname metacharacters. Defense in depth on top of the `--`
// separator (argv after `--` is never option-parsed, but a leading-dash
// branch would still be ambiguous in every other git invocation).
func validateRefName(ref string) error {
	if ref == "" || ref == "HEAD" || ref == "@" ||
		strings.HasPrefix(ref, "-") || strings.HasPrefix(ref, "/") ||
		strings.HasSuffix(ref, "/") || strings.HasSuffix(ref, ".") ||
		strings.HasSuffix(ref, ".lock") ||
		strings.Contains(ref, "..") || strings.Contains(ref, "@{") ||
		strings.Contains(ref, "//") {
		return fmt.Errorf("%w: %q", ErrRefInvalid, ref)
	}
	for _, r := range ref {
		if unicode.IsControl(r) || unicode.IsSpace(r) || strings.ContainsRune("~^:?*[\\", r) {
			return fmt.Errorf("%w: %q", ErrRefInvalid, ref)
		}
	}
	for _, part := range strings.Split(ref, "/") {
		if strings.HasPrefix(part, ".") {
			return fmt.Errorf("%w: %q", ErrRefInvalid, ref)
		}
	}
	return nil
}

// ---------------------------------------------------------------------------
// Recreate (session restore)
// ---------------------------------------------------------------------------

// RecreateWorktree guarantees a worktree exists at target on the pinned
// branch, for restoring a session whose tree is gone. Cases:
//
//   - target is a live worktree on branch → returned as-is (no-op);
//   - target is a live worktree on ANOTHER branch →
//     ErrWorktreeBranchMismatch (restore never silently retargets a tree
//     that someone else may be using);
//   - target's directory is gone but stale metadata remains (prunable) →
//     metadata pruned, tree re-created from the existing branch;
//   - target absent and branch exists → tree created from the branch;
//   - branch does not exist → ErrBranchMissing (restore never creates or
//     resurrects branches).
//
// No path here deletes the branch: branch lifetime belongs to git and the
// user, never to worktree housekeeping.
func RecreateWorktree(ctx context.Context, repoRoot, target, branch string) (WorktreeInfo, error) {
	root, err := cleanRepoRoot(repoRoot)
	if err != nil {
		return WorktreeInfo{}, err
	}
	unlock := lockWorktreeOps(root)
	defer unlock()

	if err := validateRefName(branch); err != nil {
		return WorktreeInfo{}, fmt.Errorf("branch: %w", err)
	}
	target = filepath.Clean(target)
	trees, err := ListWorktrees(ctx, root)
	if err != nil {
		return WorktreeInfo{}, err
	}
	if entry := FindWorktree(trees, target); entry != nil {
		if entry.Prunable {
			// Directory gone, metadata stale: clear it so the path can be
			// re-created below. Prune only ever removes entries whose
			// directory is already missing — live trees are untouched.
			if _, err := runWorktreeGit(ctx, newGitScanMemo(root), "worktree", "prune"); err != nil {
				return WorktreeInfo{}, fmt.Errorf("pruning stale worktree metadata for %s: %w", target, err)
			}
		} else {
			if entry.Branch != branch {
				return WorktreeInfo{}, fmt.Errorf("%w: %q is on %q, want %q", ErrWorktreeBranchMismatch, target, entry.Branch, branch)
			}
			return *entry, nil
		}
	}
	// The lock is already held; addWorktreeLocked is AddWorktree's body.
	return addWorktreeLocked(ctx, root, target, AddWorktreeOptions{Branch: branch})
}

// ---------------------------------------------------------------------------
// Removal
// ---------------------------------------------------------------------------

// RemoveWorktreeOptions governs removal decisions that belong to the
// caller: dirty-tree loss confirmation (Force) and locked-tree unlock
// (Unlock). Neither is ever taken implicitly.
type RemoveWorktreeOptions struct {
	// Force confirms the loss of uncommitted changes in the tree.
	Force bool
	// Unlock authorizes unlocking a locked tree before removal.
	Unlock bool
}

// RemoveWorktree removes the linked worktree at target from the repository
// at repoRoot, serialized per repository. The main worktree and foreign
// paths are refused (ErrWorktreeMain / ErrWorktreeNotLinked); locked trees
// fail with ErrWorktreeLocked unless the caller explicitly authorized
// unlocking; dirty trees fail with ErrWorktreeDirty unless the caller
// confirmed the loss. The branch the tree was on is NEVER deleted — branch
// lifetime belongs to git and the user, not to worktree housekeeping (a
// session may be deleted while its branch still holds unmerged work).
func RemoveWorktree(ctx context.Context, repoRoot, target string, opts RemoveWorktreeOptions) error {
	root, err := cleanRepoRoot(repoRoot)
	if err != nil {
		return err
	}
	unlock := lockWorktreeOps(root)
	defer unlock()

	target = filepath.Clean(target)
	trees, err := ListWorktrees(ctx, root)
	if err != nil {
		return err
	}
	entry := FindWorktree(trees, target)
	if entry == nil {
		return fmt.Errorf("%w: %s", ErrWorktreeNotLinked, target)
	}
	if entry.Kind == WorktreeMain {
		return fmt.Errorf("%w: %s", ErrWorktreeMain, target)
	}

	scan := newGitScanMemo(root)
	if entry.Locked {
		if !opts.Unlock {
			return &StateError{Sentinel: ErrWorktreeLocked, Reason: entry.LockedReason}
		}
		if _, err := runWorktreeGit(ctx, scan, "worktree", "unlock", "--", target); err != nil {
			return fmt.Errorf("unlocking worktree %s: %w", target, err)
		}
	}
	if !opts.Force && !entry.Prunable {
		dirty, derr := worktreeDirty(ctx, target)
		if derr != nil {
			return derr
		}
		if dirty {
			return &StateError{
				Sentinel: ErrWorktreeDirty,
				Reason:   "uncommitted changes or untracked files present; pass Force to confirm loss",
			}
		}
	}

	args := []string{"worktree", "remove"}
	if opts.Force {
		args = append(args, "--force")
	}
	args = append(args, "--", target)
	if _, runErr := runWorktreeGit(ctx, scan, args...); runErr != nil {
		return classifyRemoveFailure(runErr, target)
	}
	return nil
}

// worktreeDirty reports whether the worktree at path has uncommitted
// changes or untracked files — git's own criterion for refusing
// `git worktree remove` without --force. The status runs rooted at the
// worktree so its config chain (common dir + config.worktree overlay) is
// scanned by the hardened runner like any other repo-scoped call.
func worktreeDirty(ctx context.Context, path string) (bool, error) {
	out, err := runWorktreeGit(ctx, newGitScanMemo(path), "status", "--porcelain")
	if err != nil {
		return false, fmt.Errorf("checking worktree %s for uncommitted changes: %w", path, err)
	}
	return strings.TrimSpace(out) != "", nil
}

// WorktreeDirty is the exported read-only probe of worktreeDirty: it
// applies the same dirty classification the removal primitive applies at
// removal time (uncommitted changes or untracked files) without touching
// anything, so a caller can decide a would-be-blocked removal BEFORE its
// destructive pre-flight (the session-deletion pre-flight's non-destructive
// block probe, review [161]).
func WorktreeDirty(ctx context.Context, path string) (bool, error) {
	return worktreeDirty(ctx, path)
}

// classifyRemoveFailure maps git's raw removal refusals onto the taxonomy.
func classifyRemoveFailure(err error, target string) error {
	var state *StateError
	if !errors.As(err, &state) {
		return fmt.Errorf("git worktree remove %s: %w", target, err)
	}
	msg := strings.ToLower(state.Reason)
	switch {
	case strings.Contains(msg, "dirty") || strings.Contains(msg, "modified or untracked"):
		return &StateError{Sentinel: ErrWorktreeDirty, Reason: state.Reason}
	case strings.Contains(msg, "locked"):
		return &StateError{Sentinel: ErrWorktreeLocked, Reason: state.Reason}
	case strings.Contains(msg, "validation failed") || strings.Contains(msg, "does not exist"):
		return &StateError{Sentinel: ErrWorktreePrunable, Reason: state.Reason}
	}
	return fmt.Errorf("git worktree remove %s: %w", target, err)
}

// ---------------------------------------------------------------------------
// Prune
// ---------------------------------------------------------------------------

// PruneWorktrees removes administrative metadata of worktrees whose
// directories no longer exist (`git worktree prune`), serialized per
// repository. It never touches live worktrees and never deletes branches.
func PruneWorktrees(ctx context.Context, repoRoot string) error {
	root, err := cleanRepoRoot(repoRoot)
	if err != nil {
		return err
	}
	unlock := lockWorktreeOps(root)
	defer unlock()
	if _, err := runWorktreeGit(ctx, newGitScanMemo(root), "worktree", "prune"); err != nil {
		return fmt.Errorf("git worktree prune: %w", err)
	}
	return nil
}

// ---------------------------------------------------------------------------
// Managed-container exclusion (common info/exclude)
// ---------------------------------------------------------------------------

// worktreesExcludePattern is the single line appended to the common dir's
// info/exclude when a managed tree is created. Anchored to the repository
// root and directory-only: it hides the container from `git status` of the
// main checkout without touching any tracked .gitignore.
const worktreesExcludePattern = "/" + core.WorktreesRelativePath + "/"

// ensureWorktreesExcluded idempotently appends worktreesExcludePattern to
// <common-dir>/info/exclude when target is inside the managed container.
// The exclude file lives in the COMMON dir (shared by every worktree of
// the repository); .gitignore is never read or modified — the container is
// c0wrk infrastructure, not repository content the user tracks.
func ensureWorktreesExcluded(ctx context.Context, scan *gitScanMemo, root, target string) error {
	container := filepath.Join(root, core.WorktreesRelativePath)
	within, err := pathutil.IsWithinPath(container, target)
	if err != nil {
		return fmt.Errorf("classifying worktree target: %w", err)
	}
	if !within {
		return nil // external target: nothing to exclude
	}
	out, err := runWorktreeGit(ctx, scan, "rev-parse", "--git-common-dir")
	if err != nil {
		return fmt.Errorf("resolving git common dir: %w", err)
	}
	common := strings.TrimSpace(out)
	if common == "" {
		return errors.New("resolving git common dir: empty result")
	}
	if !filepath.IsAbs(common) {
		common = filepath.Join(root, common)
	}
	infoDir := filepath.Join(common, "info")
	// Directory creation must not be redirected out of the repository
	// (review [41], repo-arrives-as-files threat model): MkdirAllReal creates
	// only real directories — a link swapped into a component it creates, or
	// a dangling link at any component, is refused — but it RESOLVES
	// pre-existing links by design, so ordinary operator/system ancestors
	// (macOS /var → /private/var, a symlinked home) keep working. A link
	// shipped inside the repository is therefore enforced HERE, after
	// creation: when the common dir lives under the repo root, every
	// component below the root is re-verified as a real directory, so a
	// planted link at info (or .git) is refused instead of being resolved
	// into an exclude write outside the repository. A common dir outside the
	// root is git's own .git-file indirection (linked worktrees), not
	// repository-controlled content, and keeps the plain MkdirAllReal path.
	if err := safeio.MkdirAllReal(infoDir, 0o755); err != nil {
		return fmt.Errorf("creating %s: %w", infoDir, err)
	}
	if within, werr := pathutil.IsWithinPath(root, infoDir); werr != nil {
		return fmt.Errorf("classifying common info dir: %w", werr)
	} else if within {
		if err := safeio.CheckRealDirsBelow(root, infoDir); err != nil {
			return fmt.Errorf("refusing non-real component in %s: %w", infoDir, err)
		}
	}
	excludePath := filepath.Join(infoDir, "exclude")
	// safeio.ReadFile refuses a non-regular target (review [38]): a planted
	// FIFO would otherwise block the open forever, hanging the managed-tree
	// provisioning path — the same reason the config scan reads through
	// safeio. A missing file (or a dangling link at the final component) is
	// the expected first-run case and is treated as empty.
	existing, err := safeio.ReadFile(excludePath)
	if err != nil && !errors.Is(err, os.ErrNotExist) {
		return fmt.Errorf("reading %s: %w", excludePath, err)
	}
	for _, line := range strings.Split(string(existing), "\n") {
		if strings.TrimSpace(line) == worktreesExcludePattern {
			return nil // already excluded
		}
	}
	updated := existing
	if len(updated) > 0 && !bytes.HasSuffix(updated, []byte("\n")) {
		updated = append(updated, '\n')
	}
	updated = append(updated, []byte(worktreesExcludePattern+"\n")...)
	// Atomic replace within the same directory: a crash mid-write can
	// never leave a truncated exclude file behind. safeio.WriteFileAtomic
	// stages through a randomly named temp (no plantable fixed path) and
	// the rename replaces a symlink at exclude itself instead of writing
	// through it.
	if err := safeio.WriteFileAtomic(excludePath, updated, 0o644); err != nil {
		return fmt.Errorf("updating %s: %w", excludePath, err)
	}
	return nil
}

// cleanRepoRoot validates the repository root argument shared by every
// primitive: absolute and lexically clean.
func cleanRepoRoot(repoRoot string) (string, error) {
	if !filepath.IsAbs(repoRoot) || filepath.Clean(repoRoot) != repoRoot {
		return "", fmt.Errorf("%w: repository root must be an absolute clean path, got %q", ErrWorktreePathInvalid, repoRoot)
	}
	return filepath.Clean(repoRoot), nil
}
