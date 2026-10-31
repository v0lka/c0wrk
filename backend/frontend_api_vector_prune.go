package backend

import (
	"strings"

	"github.com/v0lka/c0wrk/backend/config"
	"github.com/v0lka/c0wrk/backend/project"
	"github.com/v0lka/c0wrk/core/vectorindex"
)

// Background stale-branch index cleanup: branches that disappear from the
// local clone leave per-branch vector-index artifacts in the project
// checkout's storage root and every managed-worktree storage root, and
// nothing ever re-opens them. The switch-triggered sweep (below) and the
// delete-triggered purge (purgeBranchIndexData) remove them — silent by the
// auto-fetch quiet-failure contract.
// See specs/domains/workspace.md.

// pruneAbsentBranchVectorIndexes removes the stored indexes of branches
// absent from the local clone, across the checkout's storage root and every
// managed-worktree storage root under it. Best-effort and silent; safe on a
// detached goroutine — the firing switch has already committed.
func (f *FrontendAPI) pruneAbsentBranchVectorIndexes(p *project.ProjectInfo) {
	if p == nil || p.IsNoProject {
		return
	}
	f.seedAcquire()
	// Single-flight: rapid toggles fire one sweep per switch; the running
	// sweep reads fresh git state, so a duplicate would only redo work.
	if !f.pruneStaleBranchesInFlight.CompareAndSwap(false, true) {
		return
	}
	defer f.pruneStaleBranchesInFlight.Store(false)

	viRoot := config.ProjectVectorIndexPath(f.agentDir, p.ID)
	branches, err := f.localBranchNames(p.WorkspacePath)
	if err != nil {
		f.log().Debug("vector index: stale-branch prune skipped: cannot list local branches",
			"project", p.ID, "error", err)
		return
	}

	// The registry accessor lazily creates (the same entry every vector
	// path uses), so it never returns nil; on a not-yet-wired factory
	// ManagerForCleanup reports no manager and the sweep is skipped below —
	// unless the registry is DISABLED: that is a user preference, not a
	// transient state, so the sweep runs the manager-free FS forms instead
	// of skipping for the whole disabled period (Disable shut every manager
	// down before dropping the factory, so no live or parked state holds
	// handles and the branch surgery is pure fs).
	mgr, disposable := f.vectorRootsRegistry().ManagerForCleanup()
	if mgr == nil {
		vr := f.vectorRootsRegistry()
		if vr.Disabled() {
			removed := 0
			for _, root := range append([]string{viRoot}, worktreeStorageNames(viRoot)...) {
				removed += vectorindex.PruneMissingBranchDataFS(f.log(), root, branches)
			}
			if removed > 0 {
				f.log().Debug("vector index: pruned index data of branches absent from the local clone (indexing disabled)",
					"project", p.ID, "artifacts_removed", removed)
			}
			return
		}
		// Embedder still loading or unavailable: nothing is readable right
		// now. The next switch retries; the stale data is disk garbage
		// until then, not a correctness problem.
		f.log().Debug("vector index: stale-branch prune skipped: no vector manager available",
			"project", p.ID)
		return
	}
	if disposable {
		defer mgr.Shutdown()
	}

	roots := make([]string, 0, 8)
	roots = append(roots, viRoot)
	roots = append(roots, worktreeStorageNames(viRoot)...)
	for _, root := range roots {
		// PruneMissingBranchData is fail-soft internally (missing root is a
		// no-op, per-entry failures are Debug-logged and skipped); a zero
		// return needs no log — most switches prune nothing.
		if removed := mgr.Service().PruneMissingBranchData(root, branches); removed > 0 {
			f.log().Debug("vector index: pruned index data of branches absent from the local clone",
				"project", p.ID, "storage", root, "artifacts_removed", removed)
		}
	}
}

// localBranchNames lists the repository's existing local branch names
// (refs/heads — the set every worktree of the clone shares, checked-out
// branches included). An empty result is valid: a repository without refs
// keeps nothing.
func (f *FrontendAPI) localBranchNames(repoPath string) ([]string, error) {
	out, err := f.runGitCmd(repoPath, "for-each-ref", "--format=%(refname:short)", "refs/heads/")
	if err != nil {
		return nil, err
	}
	var names []string
	for _, line := range strings.Split(out, "\n") {
		if line = strings.TrimSpace(line); line != "" {
			names = append(names, line)
		}
	}
	return names, nil
}

// purgeDeletedBranchIndexes resolves the active project with the same guards
// the git-panel RPCs apply and fires the targeted purge on a detached
// goroutine. No Project (and any unresolved project) has no storage to clean.
// The launch goes through the purgeBranchLaunchFn seam (nil in production —
// a plain `go`), which tests swap for a synchronous call.
func (f *FrontendAPI) purgeDeletedBranchIndexes(branch string) {
	path, id, err := f.activeGitProjectRoot()
	if err != nil {
		return
	}
	launch := f.purgeBranchLaunchFn
	if launch == nil {
		launch = func(fn func()) { go fn() }
	}
	launch(func() {
		f.purgeBranchIndexData(&project.ProjectInfo{ID: id, WorkspacePath: path}, branch)
	})
}

// purgeBranchIndexData removes the stored index data of a just-deleted branch
// across the checkout's and every managed-worktree storage root — the
// delete-triggered counterpart of the switch sweep, fired by a Git-panel
// branch deletion so the artifacts go away immediately. The surviving-branch
// list is taken AFTER the git deletion (DeleteBranchData needs it for the
// lossy-sanitizer collision guard); a listing failure skips the whole purge.
// Deliberately NOT single-flighted: it must run even while a sweep is in
// flight, whose ref listing may predate the deletion.
// See specs/domains/workspace.md.
func (f *FrontendAPI) purgeBranchIndexData(p *project.ProjectInfo, branch string) {
	if p == nil || p.IsNoProject || branch == "" {
		return
	}
	f.seedAcquire()

	remaining, err := f.localBranchNames(p.WorkspacePath)
	if err != nil {
		f.log().Debug("vector index: deleted-branch purge skipped: cannot list remaining local branches",
			"project", p.ID, "branch", branch, "error", err)
		return
	}

	viRoot := config.ProjectVectorIndexPath(f.agentDir, p.ID)

	// The registry accessor lazily creates (the same entry every vector
	// path uses), so it never returns nil; on a not-yet-wired factory
	// ManagerForCleanup reports no manager and the purge is skipped below —
	// unless the registry is DISABLED: that is a user preference, not a
	// transient state, so the purge runs the manager-free FS form instead
	// of skipping for the whole disabled period (see the sweep above).
	mgr, disposable := f.vectorRootsRegistry().ManagerForCleanup()
	if mgr == nil {
		vr := f.vectorRootsRegistry()
		if vr.Disabled() {
			removed := 0
			for _, root := range append([]string{viRoot}, worktreeStorageNames(viRoot)...) {
				removed += vectorindex.DeleteBranchDataFS(f.log(), root, branch, remaining)
			}
			if removed > 0 {
				f.log().Debug("vector index: purged index data of the deleted branch (indexing disabled)",
					"project", p.ID, "branch", branch, "artifacts_removed", removed)
			}
			return
		}
		f.log().Debug("vector index: deleted-branch purge skipped: no vector manager available",
			"project", p.ID, "branch", branch)
		return
	}
	if disposable {
		defer mgr.Shutdown()
	}

	roots := make([]string, 0, 8)
	roots = append(roots, viRoot)
	roots = append(roots, worktreeStorageNames(viRoot)...)
	for _, root := range roots {
		// DeleteBranchData is fail-soft internally; a zero return needs no
		// log — most deletions purge data from only some of the roots.
		if removed := mgr.Service().DeleteBranchData(root, branch, remaining); removed > 0 {
			f.log().Debug("vector index: purged index data of the deleted branch",
				"project", p.ID, "branch", branch, "storage", root, "artifacts_removed", removed)
		}
	}
}
