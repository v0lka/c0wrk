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
// (backend ownership coordinator, session restore, the Git panel) goes through;
// every invocation rides GitCmdInRepo's scan pipeline — `git worktree add`'s
// checkout and its config.worktree overlay reads included. See
// specs/domains/workspace.md, session-worktrees.md.

// --- Kinds, entry model ----------------------------------------------------

// WorktreeKind classifies one entry of `git worktree list`.
type WorktreeKind string

const (
	// WorktreeMain is the repository's primary checkout (the "local" workspace
	// of a project) — one per repo; a bare repository reports its gitdir with Bare=true.
	WorktreeMain WorktreeKind = "main"
	// WorktreeManaged is a linked worktree in the legacy checkout-local
	// container or a repository-marked app-data/custom container.
	WorktreeManaged WorktreeKind = "managed"
	// WorktreeExternal is a linked worktree outside the managed container —
	// created by the user or other tooling, never owned by c0wrk.
	WorktreeExternal WorktreeKind = "external"
)

// WorktreeInfo is one entry of the repository's worktree list; Branch is
// the short name ("" for detached/bare), Locked/Prunable carry git's reasons.
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

// BranchHolders maps each checked-out branch to the worktree path holding it,
// making git's one-branch-one-worktree refusal explicit BEFORE a doomed add.
func BranchHolders(trees []WorktreeInfo) map[string]string {
	holders := make(map[string]string, len(trees))
	for _, w := range trees {
		if w.Branch != "" {
			holders[w.Branch] = w.Path
		}
	}
	return holders
}

// FindWorktree returns the entry whose path equals target (clean absolute
// paths), or nil when target is not a worktree of this repository.
func FindWorktree(trees []WorktreeInfo, target string) *WorktreeInfo {
	clean := filepath.Clean(target)
	for i := range trees {
		if trees[i].Path == clean {
			return &trees[i]
		}
	}
	return nil
}

// --- Explicit failure taxonomy ---------------------------------------------

// Worktree operation failures are explicit and matchable with errors.Is;
// StateError carries git's verbatim reason where one exists.
var (
	// ErrWorktreeMalformed: `worktree list --porcelain` violated the
	// documented format; refused, never silently truncated.
	ErrWorktreeMalformed = errors.New("malformed git worktree output")
	// ErrWorktreeExists: the target path already exists. Creation never
	// overwrites foreign directories (--force is never passed to add).
	ErrWorktreeExists = errors.New("worktree path already exists")
	// ErrWorktreePathInvalid: structural validation failed (relative, unclean,
	// contains the repo, inside .git, a linked worktree, the container, symlinked ancestor).
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
	// ErrWorktreePrunable: the directory is gone but its metadata remains;
	// prune before the path can be reused.
	ErrWorktreePrunable = errors.New("worktree is prunable (stale metadata)")
	// ErrWorktreeMain: the target is the repository's main worktree, which
	// can never be removed as a worktree.
	ErrWorktreeMain = errors.New("path is the repository's main worktree")
	// ErrWorktreeNotLinked: the target is not a linked worktree of this
	// repository (foreign directory or the checkout itself).
	ErrWorktreeNotLinked = errors.New("path is not a linked worktree of this repository")
	// ErrWorktreeBranchMismatch: the existing tree at target is on a
	// different branch; recreate never silently retargets.
	ErrWorktreeBranchMismatch = errors.New("worktree is on a different branch than requested")
	// ErrRefInvalid: a branch or start-point ref failed validation.
	ErrRefInvalid = errors.New("invalid ref name")
)

// StateError attaches a verbatim reason (locked/prunable text, git stderr) to
// a sentinel so callers can errors.Is the sentinel and errors.As the reason.
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

// --- Per-repo serialization of conflicting operations ----------------------

// Two concurrent `git worktree add` calls for the SAME branch can interleave
// check-then-create, leaving the loser a raw exit code instead of a verdict.
// Mutating primitives therefore serialize per repo and validate inside the
// serialized section (specs/domains/workspace.md).
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

// --- Hardened git runner (operation-scoped scan reuse) ---------------------

// runWorktreeGit runs one git invocation inside a logical worktree operation,
// reusing its scan memo (review [15], specs/decisions/033-git-subprocess-hardening.md).
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

// --- Listing ----------------------------------------------------------------

// ListWorktrees returns the structured worktree list of the repository at repoRoot
// (main checkout, managed trees, external linked worktrees); malformed porcelain
// output fails closed with ErrWorktreeMalformed.
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

// parseWorktreePorcelain strictly parses `git worktree list --porcelain`:
// blank-line-separated entries of `worktree <abs-path>`, `HEAD <sha>`, and
// `branch|detached|bare|locked|prunable [reason]` (bare: no HEAD); any deviation
// is ErrWorktreeMalformed — a partial list would hide trees the checks need.
func parseWorktreePorcelain(out string) ([]WorktreeInfo, error) {
	var trees []WorktreeInfo
	var cur *WorktreeInfo
	flush := func() error {
		if cur == nil {
			return nil
		}
		// A bare repository emits `worktree <path>` + `bare` with NO HEAD line
		// (verified on git 2.55.0) — only non-bare entries must carry HEAD.
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

// classifyWorktrees stamps each entry's Kind against the repository root: the
// first (primary) entry is main; linked entries in managed containers are
// managed; all others external.
func classifyWorktrees(trees []WorktreeInfo, root string) {
	for i := range trees {
		if i == 0 {
			trees[i].Kind = WorktreeMain
			continue
		}
		if core.IsManagedWorktreePath(root, trees[i].Path) {
			trees[i].Kind = WorktreeManaged
		} else {
			trees[i].Kind = WorktreeExternal
		}
	}
}

// --- Creation ---------------------------------------------------------------

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

// AddWorktree creates a linked worktree at target in the repository at repoRoot,
// serialized per repository; structural path safety and branch occupancy are
// validated inside the serialized section, before git runs. Managed targets
// install the `/.worktrees/` exclusion in the common dir's info/exclude (never
// .gitignore); the returned info is the verified post-add entry. See specs/domains/workspace.md.
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
// worktree-operation lock (RecreateWorktree reuses it — not reentrant).
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
	// The target must not be inside another linked worktree (nested worktrees
	// would confuse ownership; the managed container is a top-level child).
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

	// No --force in any form: a pre-existing target was rejected above, and if
	// the filesystem changes underneath, git's own refusal stands. Without an
	// explicit commit-ish after the path, git names a branch after the target's
	// basename — never what a caller pinned — so the branch arg is always passed.
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
// serialized section, before git runs: creating over an existing ref or
// checking out a held branch is an explicit error; a checkout-mode add requires the branch to exist (creation never invents branches).
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

// BranchExists reports whether a LOCAL branch (refs/heads/<branch>) exists at
// repoRoot — the exact ref, never DWIM: a same-named tag or remote ref doesn't count.
func BranchExists(ctx context.Context, repoRoot, branch string) (bool, error) {
	root, err := cleanRepoRoot(repoRoot)
	if err != nil {
		return false, err
	}
	return branchExists(ctx, newGitScanMemo(root), branch)
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

// validateWorktreeTarget enforces the structural path rules shared by add and
// recreate (specs/domains/workspace.md): absolute and clean; not the checkout,
// its ancestor, .git, or the managed container; no symlinked existing ancestor; and the target must not exist — foreign directories are never overwritten.
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

// validateRefName rejects ref spellings interpretable as git options or refname
// metacharacters — defense in depth over `--` (a leading-dash branch stays ambiguous).
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

// --- Recreate (session restore) ---------------------------------------------

// RecreateWorktree guarantees a worktree exists at target on the pinned branch,
// for restoring a session whose tree is gone. A live tree on the branch returns
// as-is; on another branch it is ErrWorktreeBranchMismatch (never silently
// retargeted); prunable metadata is pruned and the tree re-created; an absent
// target with an existing branch is created; a missing branch is ErrBranchMissing
// — restore never creates branches, and no path deletes one (branch lifetime
// belongs to git and the user).
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
			// Directory gone, metadata stale: prune only removes entries
			// whose directory is already missing — live trees untouched.
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

// --- Removal -----------------------------------------------------------------

// RemoveWorktreeOptions governs removal decisions that belong to the caller:
// dirty-tree loss confirmation (Force) and locked-tree unlock (Unlock).
type RemoveWorktreeOptions struct {
	// Force confirms the loss of uncommitted changes in the tree.
	Force bool
	// Unlock authorizes unlocking a locked tree before removal.
	Unlock bool
}

// RemoveWorktree removes the linked worktree at target, serialized per repo:
// main/foreign paths are refused, locked trees need Unlock, dirty trees need
// Force. The branch the tree was on is NEVER deleted — it may hold unmerged work.
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

// worktreeDirty reports uncommitted changes or untracked files — git's own
// criterion for refusing `worktree remove` without --force; rooted at the
// worktree so its full config chain is scanned.
func worktreeDirty(ctx context.Context, path string) (bool, error) {
	out, err := runWorktreeGit(ctx, newGitScanMemo(path), "status", "--porcelain")
	if err != nil {
		return false, fmt.Errorf("checking worktree %s for uncommitted changes: %w", path, err)
	}
	return strings.TrimSpace(out) != "", nil
}

// WorktreeDirty is the exported read-only probe of worktreeDirty — the same
// classification the removal applies, for non-destructive pre-checks (review [161]).
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

// --- Prune -------------------------------------------------------------------

// PruneWorktrees removes administrative metadata of worktrees whose directories
// no longer exist (`git worktree prune`), serialized per repo; live trees and
// branches untouched.
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

// --- Managed-container exclusion (common info/exclude) ----------------------

// worktreesExcludePattern is appended to the common dir's info/exclude when a
// managed tree is created: repo-root-anchored, directory-only — hides the
// container from `git status` without touching tracked .gitignore.
const worktreesExcludePattern = "/" + core.WorktreesRelativePath + "/"

// ensureWorktreesExcluded idempotently appends worktreesExcludePattern to
// <common-dir>/info/exclude when target is inside the managed container;
// .gitignore is never touched — the container is c0wrk infrastructure.
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
	// MkdirAllReal refuses links it creates but RESOLVES pre-existing ones
	// (macOS /var, symlinked homes), so a link planted inside the repository is
	// enforced HERE, after creation: below the repo root every component is
	// re-verified as a real directory (review [41]) — a planted link at info
	// (or .git) cannot become an exclude write outside the repository; a common
	// dir outside the root is git's own indirection, not repo content.
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
	// safeio.ReadFile refuses non-regular targets (review [38]) — a planted FIFO
	// would block the open forever. Missing (or dangling final link) is the
	// expected first-run case, treated as empty.
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
	// Atomic same-directory replace: a crash cannot leave a truncated exclude
	// file; the rename replaces a symlink at exclude instead of writing through.
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
