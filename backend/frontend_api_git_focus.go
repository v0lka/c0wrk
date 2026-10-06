// frontend_api_git_focus.go — the Git-panel focus model (the RPC-side
// consumption of ADR-080's GitPanelTarget): which worktree the Git panel
// operates on. The panel's focus target is the project checkout by default
// and can be switched explicitly to any worktree of the active project
// (local checkout, app-managed session tree, or external linked tree).
//
// The focus is UI state on the backend because every git RPC resolves its
// repository root through resolveGitRepoRoot(); the frontend moves the focus
// automatically when the active project or session changes (the default
// target is the active session's execution workspace — the managed tree of a
// managed session, the checkout for a local session) via SetGitPanelFocus.
// The focus never retargets execution: a session's orchestrator keeps
// running in its own workspace regardless of what the panel displays
// (ResolveSessionContexts keeps the two values separate).
//
// The pinned-branch decision (ADR-080): a managed session worktree's branch
// is part of the session's immutable identity. Switching such a tree to
// another branch would desynchronize the persisted binding and break
// restore/recreate, so branch-switching RPCs (checkout / checkout -b /
// checkout --track / rename of the pinned branch) are refused while the
// panel is focused on a managed tree. The local checkout and external
// linked trees keep normal checkout.

package backend

import (
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"strings"

	"github.com/v0lka/c0wrk/backend/project"
	"github.com/v0lka/c0wrk/backend/session"
	"github.com/v0lka/c0wrk/core"
	"github.com/v0lka/c0wrk/core/workspace"
	"github.com/v0lka/sp4rk/pathutil"
)

// ErrPinnedWorktreeBranch is returned when a branch-switching RPC targets a
// managed session worktree. The tree's branch is pinned by its owning
// session (ADR-080); switching it would desynchronize the persisted binding.
// Matchable with errors.Is.
var ErrPinnedWorktreeBranch = errors.New("managed session worktree branch is pinned")

// GitWorktree is one worktree of the active project's repository, enriched
// with the ownership metadata the panel's focus switcher renders: managed
// trees carry their owning session, and every entry knows whether it is the
// current focus target.
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
// after the same guards resolveGitRepoRoot applies: a project must be active
// and must not be the No Project pseudo-project. Callers that need the
// project checkout itself (worktree listing, focus classification) instead
// of the focus-resolved root use this.
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

// resolveGitFocusRoot resolves the Git-panel focus target for projectPath:
// the explicitly focused worktree when one is set (and still exists), the
// project checkout otherwise. A vanished focus (session deletion, external
// `git worktree remove`) falls back to the checkout fail-soft — a stale
// focus must not brick every git RPC — and clears the stored override.
// Focus membership in the project's worktree list was validated when the
// focus was set (SetGitPanelFocus); this path stays cheap (one stat) because
// it runs inside every git RPC.
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

// SetGitPanelFocus switches the Git panel's focus target. An empty path
// resets the focus to the default (the project checkout); a non-empty path
// must be a worktree of the active project's repository — the checkout
// itself, an app-managed session tree, or an external linked tree — and is
// validated against the live `git worktree list` before being stored.
// Emits git:status_changed for the new target so the panel refreshes its
// status/branch/branch-list from the focused tree. Returns an error when no
// project is active, the project is No Project, the listing fails, or the
// path is not a worktree of the active project.
func (f *FrontendAPI) SetGitPanelFocus(worktreePath string) error {
	projectPath, _, err := f.activeGitProjectRoot()
	if err != nil {
		return err
	}
	target := strings.TrimSpace(worktreePath)
	if target == "" {
		f.setGitFocusPath("")
		f.emitGitStatusChanged(projectPath)
		return nil
	}
	abs, err := filepath.Abs(target)
	if err != nil {
		return fmt.Errorf("invalid focus path: %w", err)
	}
	trees, err := f.worktreeOwner().List(f.ctx(), projectPath)
	if err != nil {
		return fmt.Errorf("listing worktrees of the active project: %w", err)
	}
	entry := workspace.FindWorktree(trees, abs)
	if entry == nil {
		return fmt.Errorf("%w: %s is not a worktree of the active project", workspace.ErrWorktreeNotLinked, abs)
	}
	f.setGitFocusPath(entry.Path)
	f.emitGitStatusChanged(entry.Path)
	return nil
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
	focus := f.resolveGitFocusRoot(projectPath)
	trees, err := f.worktreeOwner().List(f.ctx(), projectPath)
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
// repository — the local checkout (main), the app-managed session trees, and
// external linked trees — via the worktrees.Owner listing, decorated with
// managed/pinned flags and the owning session of each managed tree. The
// current focus target is flagged (IsFocus) so the UI can mark it. Returns
// an error when no project is active, the project is No Project, or the
// listing fails.
func (f *FrontendAPI) ListProjectWorktrees() ([]GitWorktree, error) {
	projectPath, projectID, err := f.activeGitProjectRoot()
	if err != nil {
		return nil, err
	}
	trees, err := f.worktreeOwner().List(f.ctx(), projectPath)
	if err != nil {
		return nil, fmt.Errorf("listing worktrees of the active project: %w", err)
	}
	focus := f.resolveGitFocusRoot(projectPath)
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

// managedTreeOwner resolves the session that owns the managed worktree
// name, if a live session claims it. The session's persisted binding is the
// ownership record (one session per managed tree, enforced by the partial
// unique index); an unresolvable listing is non-fatal — ownership metadata
// is display-only enrichment.
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
	if f.app == nil {
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

// isManagedTreePath reports whether target lies inside the app-managed
// worktree container <checkout>/.worktrees — the same classification
// core/workspace's classifyWorktrees applies, evaluated without spawning
// git so the pinned-branch guard stays cheap and fail-closed: a path inside
// the container is managed by construction (the container is app-owned).
func isManagedTreePath(checkout, target string) bool {
	container := filepath.Join(checkout, core.WorktreesRelativePath)
	within, err := pathutil.IsWithinPath(container, target)
	return err == nil && within
}

// gitFocusRoots returns the active project's checkout path together with the
// resolved Git-panel focus root — the pair the pinned-branch guard and the
// checkout-family RPCs need (they operate on the focus but classify against
// the checkout).
func (f *FrontendAPI) gitFocusRoots() (checkout, focus string, err error) {
	checkout, _, err = f.activeGitProjectRoot()
	if err != nil {
		return "", "", err
	}
	return checkout, f.resolveGitFocusRoot(checkout), nil
}

// refusePinnedBranchSwitch enforces the pinned-branch decision for the
// Git-panel focus root. checkout is the project checkout; focusRoot the
// resolved panel target; resultingBranch the branch the operation would
// leave the tree on ("" for a brand-new branch — always off-pin). A
// same-branch checkout is allowed (it cannot move the tree off its pin).
// The local checkout and external linked trees keep normal checkout.
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
