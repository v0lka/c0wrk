// Package worktrees coordinates managed worktree ownership over hardened git primitives.
package worktrees

import (
	"context"
	"errors"
	"fmt"
	"log/slog"

	"github.com/v0lka/c0wrk/backend/config"
	"github.com/v0lka/c0wrk/core"
	"github.com/v0lka/c0wrk/core/workspace"
	"github.com/v0lka/sp4rk/pathutil"
)

// Owner provisions NEW managed trees into the configured container; existing
// tree identities always resolve from the live repo list or a validated
// persisted path, never from current config. See specs/domains/session-worktrees.md.
type Owner struct {
	log       *slog.Logger
	container string
}

// NewOwner creates a coordinator. An omitted container preserves the legacy
// primitive/test entry point; production supplies its project-scoped container.
func NewOwner(logger *slog.Logger, container ...string) *Owner {
	if logger == nil {
		logger = slog.New(slog.DiscardHandler)
	}
	o := &Owner{log: logger}
	if len(container) > 0 {
		o.container = container[0]
	}
	return o
}

// NewPath validates and derives a path for a new tree without creating it.
func (o *Owner) NewPath(repoRoot, name string) (string, error) {
	if o.container == "" {
		return config.ManagedWorktreePath(repoRoot, name)
	}
	path, err := config.WorktreePathInContainer(o.container, name)
	if err != nil {
		return "", err
	}
	for _, pair := range [][2]string{{repoRoot, o.container}, {o.container, repoRoot}} {
		within, err := pathutil.IsWithinPath(pair[0], pair[1])
		if err != nil {
			return "", err
		}
		if within {
			return "", errors.New("new managed worktree container must be outside the repository")
		}
	}
	return path, nil
}

func (o *Owner) provision(ctx context.Context, repoRoot, name string, opts workspace.AddWorktreeOptions) (workspace.WorktreeInfo, error) {
	path, err := o.NewPath(repoRoot, name)
	if err != nil {
		return workspace.WorktreeInfo{}, err
	}
	trees, err := o.List(ctx, repoRoot)
	if err != nil {
		return workspace.WorktreeInfo{}, err
	}
	// Names also key vector storage and shared ownership counts. Do not reuse a
	// live name in another container after a configuration change.
	for _, tree := range trees {
		if tree.Kind == workspace.WorktreeManaged && tree.Name == name {
			return workspace.WorktreeInfo{}, fmt.Errorf("%w: managed name %s already exists", workspace.ErrWorktreeExists, name)
		}
	}
	if o.container != "" {
		if err := core.MarkWorktreeContainer(repoRoot, o.container); err != nil {
			return workspace.WorktreeInfo{}, fmt.Errorf("mark managed container: %w", err)
		}
	}
	info, err := workspace.AddWorktree(ctx, repoRoot, path, opts)
	if err != nil {
		return workspace.WorktreeInfo{}, fmt.Errorf("provisioning managed worktree %q: %w", name, err)
	}
	o.log.Info("provisioned managed worktree", "repo", repoRoot, "name", name, "path", info.Path)
	return info, nil
}

// ProvisionNewBranch creates a fresh tree and branch at startPoint (empty = HEAD).
func (o *Owner) ProvisionNewBranch(ctx context.Context, repoRoot, name, branch, startPoint string) (workspace.WorktreeInfo, error) {
	return o.provision(ctx, repoRoot, name, workspace.AddWorktreeOptions{Branch: branch, CreateBranch: true, StartPoint: startPoint})
}

// ProvisionBranch checks out an existing branch in a fresh tree.
func (o *Owner) ProvisionBranch(ctx context.Context, repoRoot, name, branch string) (workspace.WorktreeInfo, error) {
	return o.provision(ctx, repoRoot, name, workspace.AddWorktreeOptions{Branch: branch})
}

// Path resolves an existing managed tree's identity independently of the
// configured creation root; a supplied persisted path is validated even when
// the tree no longer exists. See specs/domains/session-worktrees.md.
func (o *Owner) Path(ctx context.Context, repoRoot, name string, persisted ...string) (string, error) {
	if len(persisted) > 0 && persisted[0] != "" {
		got, err := config.ManagedWorktreeNameFromPath(repoRoot, persisted[0])
		if err != nil {
			return "", err
		}
		if got != name {
			return "", errors.New("managed worktree name/path mismatch")
		}
		return persisted[0], nil
	}
	// The legacy checkout-local container's on-disk state must not gate
	// resolution: a healthy tree may live in a marked container, and ownership
	// recognition is independent of current config. The legacy container is
	// consulted, and validated, only by the terminal fallback below.
	// See specs/domains/session-worktrees.md.
	if err := config.ValidateManagedWorktreeName(name); err != nil {
		return "", err
	}
	trees, err := o.List(ctx, repoRoot)
	if err != nil {
		return "", err
	}
	path := ""
	for _, tree := range trees {
		if tree.Kind != workspace.WorktreeManaged || tree.Name != name {
			continue
		}
		if path != "" {
			return "", errors.New("ambiguous managed worktree name")
		}
		path = tree.Path
	}
	if path != "" {
		return path, nil
	}
	// Only pathless legacy identities use this fallback, never a new config root.
	return config.ManagedWorktreePath(repoRoot, name)
}

// Recreate ensures the pinned branch at its durable path; branches are never created.
func (o *Owner) Recreate(ctx context.Context, repoRoot, name, branch string, persisted ...string) (workspace.WorktreeInfo, error) {
	path, err := o.Path(ctx, repoRoot, name, persisted...)
	if err != nil {
		return workspace.WorktreeInfo{}, err
	}
	return o.RecreateAt(ctx, repoRoot, name, branch, path)
}

// RecreateAt ensures the pinned branch at an ALREADY-RESOLVED durable path: it
// never re-resolves or re-recognizes it (the caller's normalized binding is used
// exactly as recorded); the primitives still enforce structural validation.
func (o *Owner) RecreateAt(ctx context.Context, repoRoot, name, branch, path string) (workspace.WorktreeInfo, error) {
	info, err := workspace.RecreateWorktree(ctx, repoRoot, path, branch)
	if err != nil {
		return workspace.WorktreeInfo{}, fmt.Errorf("recreating managed worktree %q: %w", name, err)
	}
	return info, nil
}

// Release removes only a linked managed tree, never its branch.
func (o *Owner) Release(ctx context.Context, repoRoot, name string, opts workspace.RemoveWorktreeOptions) error {
	entry, err := o.Inspect(ctx, repoRoot, name)
	if err != nil {
		return err
	}
	if entry.Kind != workspace.WorktreeManaged {
		return workspace.ErrWorktreeNotLinked
	}
	if err := workspace.RemoveWorktree(ctx, repoRoot, entry.Path, opts); err != nil {
		return fmt.Errorf("releasing managed worktree %q: %w", name, err)
	}
	o.log.Info("released managed worktree", "repo", repoRoot, "name", name, "path", entry.Path)
	return nil
}

// List includes main, managed (legacy or marked), and external worktrees.
func (o *Owner) List(ctx context.Context, repoRoot string) ([]workspace.WorktreeInfo, error) {
	return workspace.ListWorktrees(ctx, repoRoot)
}

// Inspect returns one linked managed tree by name; ambiguous names fail closed.
func (o *Owner) Inspect(ctx context.Context, repoRoot, name string) (*workspace.WorktreeInfo, error) {
	path, err := o.Path(ctx, repoRoot, name)
	if err != nil {
		return nil, err
	}
	trees, err := o.List(ctx, repoRoot)
	if err != nil {
		return nil, err
	}
	entry := workspace.FindWorktree(trees, path)
	if entry == nil || entry.Kind != workspace.WorktreeManaged {
		return nil, fmt.Errorf("managed worktree %q: %w", name, workspace.ErrWorktreeNotLinked)
	}
	return entry, nil
}

// IsExplicitFailure reports expected caller-decidable worktree failures.
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
