// Package worktrees is the backend ownership coordinator for
// session-managed git worktrees (ADR-080). It is the single backend seam
// that provisions, restores, releases and lists the session-owned trees
// under <repoRoot>/.worktrees, on top of the hardened primitives in
// core/workspace (config-scanned git spawns, per-repo serialization,
// explicit failure taxonomy).
//
// Ownership rules enforced here:
//
//   - Paths are derived, never accepted: every operation resolves its
//     target through config.ManagedWorktreePath, the only constructor of
//     .worktrees paths (portable single-component names, no traversal, no
//     symlinked container/tree, containment inside the repository). A
//     caller cannot point the coordinator at an external or foreign
//     directory.
//   - External linked worktrees (created by the user or other tooling
//     outside the container) are visible in listings but can never be
//     provisioned, recreated or released through this coordinator.
//   - Releasing a session's tree never deletes its branch: branch
//     lifetime belongs to git and the user. Session deletion therefore
//     cannot destroy unmerged work.
package worktrees

import (
	"context"
	"errors"
	"fmt"
	"log/slog"

	"github.com/v0lka/c0wrk/backend/config"
	"github.com/v0lka/c0wrk/core/workspace"
)

// Owner coordinates managed-worktree lifecycle for the backend. It holds
// no mutable state of its own: the serialized, fail-closed primitives in
// core/workspace carry the safety properties, and this type adds the
// ownership policy (derived paths, managed-only mutations, structured
// listings, logging) on top. Safe for concurrent use.
type Owner struct {
	log *slog.Logger
}

// NewOwner returns an Owner logging through logger (nil-safe).
func NewOwner(logger *slog.Logger) *Owner {
	if logger == nil {
		logger = slog.New(slog.DiscardHandler)
	}
	return &Owner{log: logger}
}

// ProvisionNewBranch creates the managed tree <repoRoot>/.worktrees/<name>
// with a NEW branch created at startPoint (empty startPoint = HEAD). Use
// for session creation and managed forks (which derive a fresh branch).
func (o *Owner) ProvisionNewBranch(ctx context.Context, repoRoot, name, branch, startPoint string) (workspace.WorktreeInfo, error) {
	path, err := config.ManagedWorktreePath(repoRoot, name)
	if err != nil {
		return workspace.WorktreeInfo{}, fmt.Errorf("managed worktree %q: %w", name, err)
	}
	info, err := workspace.AddWorktree(ctx, repoRoot, path, workspace.AddWorktreeOptions{
		Branch:       branch,
		CreateBranch: true,
		StartPoint:   startPoint,
	})
	if err != nil {
		return workspace.WorktreeInfo{}, fmt.Errorf("provisioning managed worktree %q on new branch %q: %w", name, branch, err)
	}
	o.log.Info("provisioned managed worktree", "repo", repoRoot, "name", name, "branch", branch, "path", info.Path)
	return info, nil
}

// ProvisionBranch creates the managed tree <repoRoot>/.worktrees/<name>
// checking out an EXISTING branch. The branch must not be checked out
// anywhere else (core primitives enforce one-branch-one-worktree).
func (o *Owner) ProvisionBranch(ctx context.Context, repoRoot, name, branch string) (workspace.WorktreeInfo, error) {
	path, err := config.ManagedWorktreePath(repoRoot, name)
	if err != nil {
		return workspace.WorktreeInfo{}, fmt.Errorf("managed worktree %q: %w", name, err)
	}
	info, err := workspace.AddWorktree(ctx, repoRoot, path, workspace.AddWorktreeOptions{Branch: branch})
	if err != nil {
		return workspace.WorktreeInfo{}, fmt.Errorf("provisioning managed worktree %q on branch %q: %w", name, branch, err)
	}
	o.log.Info("provisioned managed worktree", "repo", repoRoot, "name", name, "branch", branch, "path", info.Path)
	return info, nil
}

// Recreate guarantees the managed tree for a restored session exists on
// its pinned branch (no-op when already present; stale metadata pruned and
// the tree re-created from the existing branch when the directory was
// lost; explicit failure when the branch is gone — restore never creates
// branches). See workspace.RecreateWorktree for the full case table.
func (o *Owner) Recreate(ctx context.Context, repoRoot, name, branch string) (workspace.WorktreeInfo, error) {
	path, err := config.ManagedWorktreePath(repoRoot, name)
	if err != nil {
		return workspace.WorktreeInfo{}, fmt.Errorf("managed worktree %q: %w", name, err)
	}
	info, err := workspace.RecreateWorktree(ctx, repoRoot, path, branch)
	if err != nil {
		return workspace.WorktreeInfo{}, fmt.Errorf("recreating managed worktree %q on branch %q: %w", name, branch, err)
	}
	o.log.Info("ensured managed worktree", "repo", repoRoot, "name", name, "branch", branch, "path", info.Path)
	return info, nil
}

// Release removes the managed tree <repoRoot>/.worktrees/<name>. opts
// carries the caller's explicit decisions only: Force confirms the loss of
// uncommitted changes, Unlock authorizes removing a locked tree. The
// branch is NEVER deleted — neither here nor in any primitive below — so
// deleting a session can never destroy its unmerged work.
func (o *Owner) Release(ctx context.Context, repoRoot, name string, opts workspace.RemoveWorktreeOptions) error {
	path, err := config.ManagedWorktreePath(repoRoot, name)
	if err != nil {
		return fmt.Errorf("managed worktree %q: %w", name, err)
	}
	// Ownership gate: the coordinator mutates only trees inside the managed
	// container. A tree that exists but is classified external is not ours
	// (unreachable in practice because the derived path is inside the
	// container; the check keeps the policy explicit against future
	// refactors).
	trees, err := workspace.ListWorktrees(ctx, repoRoot)
	if err != nil {
		return fmt.Errorf("listing worktrees of %s: %w", repoRoot, err)
	}
	entry := workspace.FindWorktree(trees, path)
	if entry == nil {
		return fmt.Errorf("managed worktree %q: %w", name, workspace.ErrWorktreeNotLinked)
	}
	if entry.Kind != workspace.WorktreeManaged {
		return fmt.Errorf("managed worktree %q is classified %q: %w",
			name, entry.Kind, workspace.ErrWorktreeNotLinked)
	}
	if err := workspace.RemoveWorktree(ctx, repoRoot, path, opts); err != nil {
		return fmt.Errorf("releasing managed worktree %q: %w", name, err)
	}
	o.log.Info("released managed worktree", "repo", repoRoot, "name", name, "path", path,
		"force", opts.Force, "unlock", opts.Unlock)
	return nil
}

// List returns the structured worktree list of the repository: the main
// checkout (local), the app-managed session trees, and external linked
// worktrees created outside the container.
func (o *Owner) List(ctx context.Context, repoRoot string) ([]workspace.WorktreeInfo, error) {
	trees, err := workspace.ListWorktrees(ctx, repoRoot)
	if err != nil {
		return nil, fmt.Errorf("listing worktrees of %s: %w", repoRoot, err)
	}
	return trees, nil
}

// Inspect returns the entry of one managed tree by name, or an error
// matching workspace.ErrWorktreeNotLinked when no such tree exists.
func (o *Owner) Inspect(ctx context.Context, repoRoot, name string) (*workspace.WorktreeInfo, error) {
	path, err := config.ManagedWorktreePath(repoRoot, name)
	if err != nil {
		return nil, fmt.Errorf("managed worktree %q: %w", name, err)
	}
	trees, err := workspace.ListWorktrees(ctx, repoRoot)
	if err != nil {
		return nil, fmt.Errorf("listing worktrees of %s: %w", repoRoot, err)
	}
	entry := workspace.FindWorktree(trees, path)
	if entry == nil {
		return nil, fmt.Errorf("managed worktree %q: %w", name, workspace.ErrWorktreeNotLinked)
	}
	return entry, nil
}

// IsExplicitFailure reports whether err is one of the coordinator's
// expected, caller-decidable failures (busy/locked/dirty/prunable/
// exists/mismatch) rather than an infrastructure error. Convenience for
// the RPC layer mapping failures onto user choices.
func IsExplicitFailure(err error) bool {
	return errors.Is(err, workspace.ErrBranchBusy) ||
		errors.Is(err, workspace.ErrBranchExists) ||
		errors.Is(err, workspace.ErrBranchMissing) ||
		errors.Is(err, workspace.ErrWorktreeLocked) ||
		errors.Is(err, workspace.ErrWorktreeDirty) ||
		errors.Is(err, workspace.ErrWorktreePrunable) ||
		errors.Is(err, workspace.ErrWorktreeExists) ||
		errors.Is(err, workspace.ErrWorktreeBranchMismatch) ||
		errors.Is(err, workspace.ErrWorktreeNotLinked)
}
