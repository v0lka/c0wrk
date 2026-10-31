// frontend_api_git_focus.go — the Git-panel focus model: which worktree every
// git RPC operates on (see specs/domains/session-worktrees.md). The focus is
// backend UI state because resolveGitRepoRoot() resolves every git RPC through
// it; the frontend moves it automatically with the active project or session.
// The focus never retargets execution. Pinned-branch decision: a managed
// session worktree's branch is part of its session's immutable identity, so
// branch-switching RPCs are refused while the panel focuses a managed tree.

package backend

import (
	"context"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"strings"

	"github.com/v0lka/c0wrk/backend/project"
	"github.com/v0lka/c0wrk/backend/session"
	"github.com/v0lka/c0wrk/core"
	"github.com/v0lka/c0wrk/core/workspace"
)

// ErrPinnedWorktreeBranch is returned when a branch-switching RPC targets a
// managed session worktree (matchable with errors.Is).
var ErrPinnedWorktreeBranch = errors.New("managed session worktree branch is pinned")

// GitWorktree is one worktree of the active project's repository, enriched
// with the ownership metadata the panel's focus switcher renders.
type GitWorktree struct {
	Path   string `json:"path"`
	Name   string `json:"name"`
	Kind   string `json:"kind"` // "main" | "managed" | "external"
	Branch string `json:"branch,omitempty"`
	Head   string `json:"head"`
	// Detached/Bare/Locked/Prunable mirror git's worktree-list attributes.
	Detached bool `json:"detached,omitempty"`
	Bare     bool `json:"bare,omitempty"`
	Locked   bool `json:"locked,omitempty"`
	Prunable bool `json:"prunable,omitempty"`
	// Managed reports whether the tree is an app-managed session tree under
	// <repoRoot>/.worktrees.
	Managed bool `json:"managed"`
	// Pinned reports whether branch checkout is refused for this tree
	// (managed trees pin their branch to their owning session).
	Pinned bool `json:"pinned"`
	// SessionID/SessionName identify the owning session of a managed tree;
	// empty when no live session claims it (e.g. an orphaned tree retained
	// after its session row was deleted).
	SessionID   string `json:"session_id,omitempty"`
	SessionName string `json:"session_name,omitempty"`
	// IsFocus marks the tree the Git panel is currently focused on.
	IsFocus bool `json:"is_focus"`
}

// GitPanelFocusInfo describes the resolved Git-panel focus target: the
// worktree every git RPC currently operates on.
type GitPanelFocusInfo struct {
	Path   string `json:"path"`
	Name   string `json:"name"`
	Kind   string `json:"kind"` // "main" | "managed" | "external"
	Branch string `json:"branch,omitempty"`
	// Managed reports whether the focus is an app-managed session tree.
	Managed bool `json:"managed"`
	// Pinned reports whether branch checkout is refused at this focus.
	Pinned bool `json:"pinned"`
	// SessionID identifies the owning session when the focus is a managed
	// tree claimed by a live session.
	SessionID string `json:"session_id,omitempty"`
}

// activeGitProjectRoot returns the active project's checkout path and id
// after the same guards resolveGitRepoRoot applies; for callers that need
// the checkout itself (worktree listing, focus classification), not the
// focus-resolved root.
func (f *FrontendAPI) activeGitProjectRoot() (path, id string, err error) {
	f.activeProjectMu.RLock()
	projectPath := f.activeProjectPath
	projectID := f.activeProjectID
	f.activeProjectMu.RUnlock()

	if projectPath == "" {
		return "", "", errors.New("no active project")
	}
	if projectID == project.NoProjectID {
		return "", "", errors.New("no git operations in No Project mode")
	}
	return projectPath, projectID, nil
}

// resolveGitFocusRoot resolves the Git-panel focus target: the focused
// worktree when set and still existing, the checkout otherwise. A vanished
// focus falls back fail-soft — a stale focus must not brick every git RPC —
// and clears the stored override; membership was validated when the focus
// was set (SetGitPanelFocus).
func (f *FrontendAPI) resolveGitFocusRoot(projectPath string) string {
	f.gitFocusMu.RLock()
	focus := f.gitFocusPath
	f.gitFocusMu.RUnlock()
	if focus == "" || focus == projectPath {
		return projectPath
	}
	if info, err := os.Stat(focus); err != nil || !info.IsDir() {
		f.setGitFocusPath("")
		f.log().Warn("git panel focus worktree vanished; falling back to the project checkout", "path", focus)
		return projectPath
	}
	return focus
}

// setGitFocusPath stores (or clears, when path is empty) the focus override.
func (f *FrontendAPI) setGitFocusPath(path string) {
	f.gitFocusMu.Lock()
	f.gitFocusPath = path
	f.gitFocusMu.Unlock()
}

// gitFocusPathSnapshot returns the raw stored focus override ("" = default).
func (f *FrontendAPI) gitFocusPathSnapshot() string {
	f.gitFocusMu.RLock()
	defer f.gitFocusMu.RUnlock()
	return f.gitFocusPath
}

// listWorktreesBounded lists the project's worktrees under the same 30s
// budget every other local git invocation gets: the worktree primitives
// apply no internal bound and f.ctx() carries no deadline, so a wedged git
// would otherwise hang the Git-panel RPCs indefinitely.
func (f *FrontendAPI) listWorktreesBounded(projectPath string) ([]workspace.WorktreeInfo, error) {
	ctx, cancel := context.WithTimeout(f.ctx(), gitCmdTimeout)
	defer cancel()
	return f.worktreeOwner().List(ctx, projectPath)
}

// SetGitPanelFocus switches the Git panel's focus target: an empty path
// resets to the checkout; a non-empty path must be a worktree of the active
// project, validated against the live `git worktree list`. Emits
// git:status_changed for the new target and moves the vector registry's
// focus to the same root (focusVectorRoots, fail-soft).
func (f *FrontendAPI) SetGitPanelFocus(worktreePath string) error {
	projectPath, _, err := f.activeGitProjectRoot()
	if err != nil {
		return err
	}
	target := strings.TrimSpace(worktreePath)
	if target == "" {
		f.setGitFocusPath("")
		// The reset lands on the checkout — NOT LeaveFocus: the visible
		// index identity follows the checkout rather than going blank.
		f.focusVectorRoots(projectPath)
		f.emitGitStatusChanged(projectPath)
		return nil
	}
	abs, err := filepath.Abs(target)
	if err != nil {
		return fmt.Errorf("invalid focus path: %w", err)
	}
	// Canonicalize the repo root for the listing (fail-soft to the stored
	// form — see canonicalWorktreeRoot): the Kind classification and the
	// canonical entries below key on one presented form.
	trees, err := f.listWorktreesBounded(canonicalWorktreeRoot(projectPath))
	if err != nil {
		return fmt.Errorf("listing worktrees of the active project: %w", err)
	}
	entry := workspace.FindWorktree(trees, abs)
	if entry == nil {
		return fmt.Errorf("%w: %s is not a worktree of the active project", workspace.ErrWorktreeNotLinked, abs)
	}
	f.setGitFocusPath(entry.Path)
	f.focusVectorRoots(entry.Path)
	f.emitGitStatusChanged(entry.Path)
	return nil
}

// focusVectorRoots re-points the vector registry at the new Git-panel focus
// root — the "USER routing resolves the Git-panel focus root" contract.
// Fail-soft, mirroring switchProjectSetupVector: an unfocusable root is
// logged at Warn and never fails the git RPC.
func (f *FrontendAPI) focusVectorRoots(root string) {
	// f.ctx() + resolveRootPlan's per-op cap reproduce the bounded projects
	// listing the plain ListProjects() applied before the ctx threading.
	if err := f.vectorRootsRegistry().ApplyFocus(f.ctx(), canonicalRoot(root)); err != nil {
		f.log().Warn("vector focus unavailable for git panel target", "root", root, "error", err)
	}
}

// GetGitPanelFocus returns the resolved focus target with its
// classification and owning-session metadata. Returns an error when no
// project is active, the project is No Project, or the worktree listing
// fails.
func (f *FrontendAPI) GetGitPanelFocus() (GitPanelFocusInfo, error) {
	projectPath, projectID, err := f.activeGitProjectRoot()
	if err != nil {
		return GitPanelFocusInfo{}, err
	}
	// Canonicalize the repo root (see canonicalWorktreeRoot): the listing's
	// entries are canonical and the stored focus is matched against them by
	// string equality, so a legacy symlinked registration must not lose the
	// managed classification and focus flags of the reported payload.
	repoRoot := canonicalWorktreeRoot(projectPath)
	focus := f.resolveGitFocusRoot(repoRoot)
	trees, err := f.listWorktreesBounded(repoRoot)
	if err != nil {
		return GitPanelFocusInfo{}, fmt.Errorf("listing worktrees of the active project: %w", err)
	}
	entry := workspace.FindWorktree(trees, focus)
	if entry == nil {
		// The focus root is the checkout but the listing's main entry does
		// not match it verbatim (e.g. a symlink-resolved registration);
		// report it as the main tree rather than failing the panel.
		return GitPanelFocusInfo{Path: focus, Name: filepath.Base(focus), Kind: string(workspace.WorktreeMain)}, nil
	}
	return f.gitWorktreeFocusInfo(projectID, entry), nil
}

// gitWorktreeFocusInfo classifies one worktree entry as a focus payload.
func (f *FrontendAPI) gitWorktreeFocusInfo(projectID string, entry *workspace.WorktreeInfo) GitPanelFocusInfo {
	info := GitPanelFocusInfo{
		Path:    entry.Path,
		Name:    entry.Name,
		Kind:    string(entry.Kind),
		Branch:  entry.Branch,
		Managed: entry.Kind == workspace.WorktreeManaged,
		Pinned:  entry.Kind == workspace.WorktreeManaged,
	}
	if info.Managed {
		if owner, ok := f.managedTreeOwner(projectID, entry.Name); ok {
			info.SessionID = owner.ID
		}
	}
	return info
}

// ListProjectWorktrees returns every worktree of the active project's
// repository — the checkout, the app-managed session trees, and external
// linked trees — decorated with managed/pinned flags, the owning session of
// each managed tree, and the IsFocus flag.
func (f *FrontendAPI) ListProjectWorktrees() ([]GitWorktree, error) {
	projectPath, projectID, err := f.activeGitProjectRoot()
	if err != nil {
		return nil, err
	}
	// Canonicalize the repo root (see canonicalWorktreeRoot): the IsFocus
	// flag compares the resolved focus against canonical listing entries by
	// string equality, so a legacy symlinked registration must not flag no
	// row at all.
	repoRoot := canonicalWorktreeRoot(projectPath)
	trees, err := f.listWorktreesBounded(repoRoot)
	if err != nil {
		return nil, fmt.Errorf("listing worktrees of the active project: %w", err)
	}
	focus := f.resolveGitFocusRoot(repoRoot)
	out := make([]GitWorktree, 0, len(trees))
	for i := range trees {
		t := trees[i]
		entry := GitWorktree{
			Path:     t.Path,
			Name:     t.Name,
			Kind:     string(t.Kind),
			Branch:   t.Branch,
			Head:     t.Head,
			Detached: t.Detached,
			Bare:     t.Bare,
			Locked:   t.Locked,
			Prunable: t.Prunable,
			IsFocus:  t.Path == focus,
		}
		if t.Kind == workspace.WorktreeManaged {
			entry.Managed = true
			entry.Pinned = true
			if owner, ok := f.managedTreeOwner(projectID, t.Name); ok {
				entry.SessionID = owner.ID
				entry.SessionName = owner.Name
			}
		}
		out = append(out, entry)
	}
	return out, nil
}

// DeleteWorktree removes a linked worktree of the active project's repository
// (session-release cleanup contract; see specs/domains/session-worktrees.md).
// The path must be a worktree of the active project; the checkout is refused
// by the removal primitive itself. force / unlock confirm dirty / locked trees
// and are never taken implicitly; the branch the tree was on is never deleted;
// managed trees are removed regardless of bound sessions (the session rows
// stay).
func (f *FrontendAPI) DeleteWorktree(worktreePath string, force, unlock bool) error {
	projectPath, projectID, err := f.activeGitProjectRoot()
	if err != nil {
		return err
	}
	target := strings.TrimSpace(worktreePath)
	if target == "" {
		return fmt.Errorf("%w: empty worktree path", workspace.ErrWorktreePathInvalid)
	}
	abs, err := filepath.Abs(target)
	if err != nil {
		return fmt.Errorf("invalid worktree path: %w", err)
	}
	// Canonicalize the repo root (see canonicalWorktreeRoot): the per-repo
	// worktree lock keys lexically and the per-tree vector keys derive from
	// this root, so a legacy symlinked registration must not split the lock
	// or miss the live manager's Release. The success event stays on the
	// stored form.
	repoRoot := canonicalWorktreeRoot(projectPath)
	trees, err := f.listWorktreesBounded(repoRoot)
	if err != nil {
		return fmt.Errorf("listing worktrees of the active project: %w", err)
	}
	entry := workspace.FindWorktree(trees, abs)
	if entry == nil {
		return fmt.Errorf("%w: %s is not a worktree of the active project", workspace.ErrWorktreeNotLinked, abs)
	}
	// The removal — full directory deletion plus optional unlock and dirty
	// probe — gets the generous managedProvisionTimeout: convert a wedged
	// git into an explicit error, don't race a slow disk through a large
	// tree. The read-only listing above stays on gitCmdTimeout.
	ctx, cancel := context.WithTimeout(f.ctx(), managedProvisionTimeout)
	defer cancel()
	if err := workspace.RemoveWorktree(ctx, repoRoot, entry.Path, workspace.RemoveWorktreeOptions{
		Force:  force,
		Unlock: unlock,
	}); err != nil {
		return err
	}
	switch entry.Kind {
	case workspace.WorktreeManaged:
		f.deleteWorktreeVectorIndex(repoRoot, projectID, entry.Name, entry.Path)
	case workspace.WorktreeExternal:
		// An external tree may hold the panel's focus and a live manager;
		// release it (no c0wrk-provisioned storage exists to clean).
		f.vectorRootsRegistry().Release(canonicalRoot(entry.Path))
	}
	f.emitGitStatusChanged(projectPath)
	return nil
}

// managedTreeOwner resolves a session bound to the managed worktree name —
// since the shared-tree revision of ADR-080 several sessions may execute in
// one tree, and this returns the listing order's first match. Ownership
// metadata is display-only enrichment.
func (f *FrontendAPI) managedTreeOwner(projectID, worktreeName string) (sessionInfoSnapshot, bool) {
	for _, s := range f.projectSessions(projectID) {
		if b := s.WorkspaceBinding; b != nil && b.Kind == session.WorkspaceManagedWorktree && b.WorktreeName == worktreeName {
			return sessionInfoSnapshot{ID: s.ID, Name: s.Name}, true
		}
	}
	return sessionInfoSnapshot{}, false
}

// sessionInfoSnapshot is the minimal session projection the focus DTOs
// carry (id + display name).
type sessionInfoSnapshot struct {
	ID   string
	Name string
}

// projectSessions lists the active project's sessions for ownership
// metadata; nil when no manager is wired (ownership enrichment degrades to
// absent, never fails the RPC).
func (f *FrontendAPI) projectSessions(projectID string) []session.SessionInfo {
	if f.appCell() == nil {
		return nil
	}
	mgr := f.app.Manager()
	if mgr == nil {
		return nil
	}
	sessions, err := mgr.ListSessionsByProject(projectID)
	if err != nil {
		f.log().Warn("listing sessions for worktree ownership", "project_id", projectID, "error", err)
		return nil
	}
	return sessions
}

// isManagedTreePath applies the same legacy/marked-container ownership
// classification as core/workspace, without a git spawn or current config.
func isManagedTreePath(checkout, target string) bool {
	return core.IsManagedWorktreePath(checkout, target)
}

// gitFocusRoots returns the active project's checkout (canonical form — see
// canonicalWorktreeRoot) together with the resolved Git-panel focus root: the
// pair the pinned-branch guard and the checkout-family RPCs need (they
// operate on the focus but classify against the checkout), so both operands
// must arrive in one form.
func (f *FrontendAPI) gitFocusRoots() (checkout, focus string, err error) {
	checkout, _, err = f.activeGitProjectRoot()
	if err != nil {
		return "", "", err
	}
	checkout = canonicalWorktreeRoot(checkout)
	return checkout, f.resolveGitFocusRoot(checkout), nil
}

// refusePinnedBranchSwitch enforces the pinned-branch decision for the focus
// root. resultingBranch is the branch the operation would leave the tree on
// ("" for a brand-new branch — always off-pin); a same-branch checkout is
// allowed (it cannot move the tree off its pin).
func (f *FrontendAPI) refusePinnedBranchSwitch(checkout, focusRoot, resultingBranch string) error {
	if focusRoot == checkout || !isManagedTreePath(checkout, focusRoot) {
		return nil
	}
	current, err := f.currentBranchName(focusRoot)
	if err != nil {
		return fmt.Errorf("reading the pinned branch of %s: %w", filepath.Base(focusRoot), err)
	}
	if resultingBranch != "" && resultingBranch == current {
		return nil
	}
	return fmt.Errorf("%w: %s is pinned to %q by its owning session; switch the Git panel focus to the local checkout to change branches", ErrPinnedWorktreeBranch, filepath.Base(focusRoot), current)
}
