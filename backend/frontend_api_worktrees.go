// frontend_api_worktrees.go — the session-lifecycle side of ADR-080 managed
// worktrees. The worktrees.Owner coordinator (step: safe primitives) is
// deliberately stateless; this file is the "Git lifecycle owner" that drives
// it from the session RPC flows:
//
//   - CreateManagedSession: reserve identity (SessionDraft) → provision the
//     tree → commit the runtime session → persist the binding, compensating
//     (releasing the fresh tree) when the runtime or DB step fails.
//   - ensureManagedWorkspace (installed as the manager's WorkspaceEnsurer):
//     lazy restore validates the stored Git identity and recreates a missing
//     tree from the pinned branch, surfacing the data-loss warning; it never
//     falls back to the project checkout.
//   - ForkSession (managed source): a new tree on <branch>-fork-<short-id>
//     created at the source's COMMITTED HEAD — uncommitted changes are
//     explicitly not carried into the fork.
//   - DeleteSession/DeleteSessionWithOptions: join the running task, stop the
//     session terminal, then recheck dirtiness at removal time; a dirty (or
//     locked) tree blocks deletion with a typed decision error so the session
//     stays retryable until the user confirms the loss.
package backend

import (
	"context"
	"errors"
	"fmt"
	"path/filepath"
	"time"

	"github.com/v0lka/c0wrk/backend/project"
	"github.com/v0lka/c0wrk/backend/session"
	"github.com/v0lka/c0wrk/backend/worktrees"
	"github.com/v0lka/c0wrk/core/workspace"
)

// managedProvisionTimeout bounds one provisioning git operation (worktree
// add, which materializes a full checkout). Generous on purpose: the point is
// to convert a wedged git into an explicit error, not to race a slow disk.
const managedProvisionTimeout = 10 * time.Minute

// SessionDeleteOptions carries the user's explicit decisions for deleting a
// session that owns a managed worktree. Both flags exist because the
// underlying removal primitives refuse to destroy data or bypass locks
// without them; a deletion that needs a flag the caller did not set fails
// with *SessionDeleteBlockedError and the session is kept intact (retryable).
type SessionDeleteOptions struct {
	// ConfirmUncommittedLoss authorizes removing the session's tree even
	// though it still contains uncommitted changes. They are then lost —
	// the branch is never deleted, but uncommitted work lives only in the
	// working tree.
	ConfirmUncommittedLoss bool `json:"confirm_uncommitted_loss"`
	// UnlockLockedTree authorizes removing a tree the user (or tooling)
	// locked via `git worktree lock`.
	UnlockLockedTree bool `json:"unlock_locked_tree"`
}

// SessionDeleteBlockedError reports that the session cannot be deleted
// without an explicit user decision. The session, its tree, and its branch
// are untouched — the caller re-issues the deletion with the named option
// once the user confirms.
type SessionDeleteBlockedError struct {
	// Reason is the human-readable explanation shown to the user.
	Reason string `json:"reason"`
	// Option names the SessionDeleteOptions field that authorizes proceeding.
	Option string `json:"option"`
}

func (e *SessionDeleteBlockedError) Error() string {
	return fmt.Sprintf("session deletion blocked: %s (confirm via %s)", e.Reason, e.Option)
}

// worktreeOwner returns the backend ownership coordinator. The Owner is
// stateless (a logger wrapper over the hardened core/workspace primitives),
// so per-call construction is safe and keeps this API free of extra lock
// state.
func (f *FrontendAPI) worktreeOwner() *worktrees.Owner {
	return worktrees.NewOwner(f.log())
}

// shortIdentity derives the short identity suffix (first 8 chars) used for
// derived tree and branch names. Session ids are UUIDs, so the prefix is
// always hex and never empty for a well-formed id.
func shortIdentity(id string) string {
	if len(id) < 8 {
		return id
	}
	return id[:8]
}

// managedTreeName derives the managed worktree name for a session identity.
// The name is a single portable path component validated again (via
// ManagedWorktreePath) inside every owner operation.
func managedTreeName(sessionID string) string {
	return "s-" + shortIdentity(sessionID)
}

// forkBranchName derives the fork branch: <source branch>-fork-<short id>.
// The suffix makes forks of forks sortable and collision-free without ever
// reusing the source branch (one branch, one worktree).
func forkBranchName(sourceBranch, dstSessionID string) string {
	return sourceBranch + "-fork-" + shortIdentity(dstSessionID)
}

// installWorkspaceEnsurer wires the manager's lazy-restore hook to this API's
// worktree coordinator. Called from NewFrontendAPI after the manager exists
// (mirroring installServiceLLMGate — the one place manager and FrontendAPI
// meet). Without this line managed restores fail closed; tests that build
// FrontendAPI via struct literal call it (or SetWorkspaceEnsurer) explicitly.
func (f *FrontendAPI) installWorkspaceEnsurer() {
	if f.app == nil {
		return
	}
	if m := f.app.Manager(); m != nil {
		m.SetWorkspaceEnsurer(f.ensureManagedWorkspace)
	}
}

// ensureManagedWorkspace is the WorkspaceEnsurer installed on the session
// manager. It validates the stored Git identity against the repository and
// guarantees the tree exists on the pinned branch before the orchestrator is
// built. It reports recreated=true exactly when the tree (or its stale
// metadata) was absent and had to be recreated from the branch — uncommitted
// data that lived only in the missing tree is unrecoverable, which the
// manager surfaces as an explicit warning.
func (f *FrontendAPI) ensureManagedWorkspace(ctx context.Context, repoRoot string, binding *session.WorkspaceBinding) (bool, error) {
	owner := f.worktreeOwner()
	trees, err := owner.List(ctx, repoRoot)
	if err != nil {
		return false, fmt.Errorf("list worktrees of %s: %w", repoRoot, err)
	}
	entry := workspace.FindWorktree(trees, binding.WorkspacePath)
	missing := entry == nil || !entry.IsLinked() || entry.Prunable
	if _, err := owner.Recreate(ctx, repoRoot, binding.WorktreeName, binding.Branch); err != nil {
		return false, err
	}
	return missing, nil
}

// CreateManagedSession creates a new CODE session bound to its own managed
// worktree provisioned under <repo>/.worktrees before any orchestrator,
// logger, or persisted row exists (ADR-080).
//
// branch names the git branch the tree checks out. When createBranch is
// true, the branch is CREATED at startPoint (empty startPoint = the
// repository's current HEAD) instead of being required to already exist; an
// empty branch with createBranch derives "session-<short id>". On success the
// persisted session row carries the managed binding (the binding is durable
// before the session is claimed). If the runtime session or the persistence
// step fails after provisioning, the fresh tree is released again
// (compensation); the created branch is kept — no operation in this path
// ever deletes a branch.
func (f *FrontendAPI) CreateManagedSession(branch string, createBranch bool, startPoint string) (*session.SessionInfo, error) {
	if f.app == nil || f.app.Manager() == nil {
		return nil, errors.New("session manager not initialized - check startup logs for LLM router or configuration errors")
	}
	if f.store == nil {
		return nil, errors.New("session store not initialized")
	}

	f.activeProjectMu.RLock()
	projectID := f.activeProjectID
	repoRoot := f.activeProjectPath
	f.activeProjectMu.RUnlock()

	if projectID == "" {
		return nil, errors.New("no active project — create or select a project first")
	}
	if projectID == project.NoProjectID {
		return nil, errors.New("CHAT (No Project) sessions cannot own a managed worktree")
	}

	draft := session.NewSessionDraft(projectID, nil)
	if createBranch && branch == "" {
		branch = "session-" + shortIdentity(draft.ID)
	}
	if branch == "" {
		return nil, errors.New("a branch is required — name an existing branch or pass createBranch=true")
	}
	treeName := managedTreeName(draft.ID)
	draft.WorkspaceBinding = &session.WorkspaceBinding{
		Kind:         session.WorkspaceManagedWorktree,
		WorktreeName: treeName,
		Branch:       branch,
	}
	// Validate the whole binding (branch syntax, tree name, containment)
	// BEFORE any git runs, so a malformed request never provisions anything.
	if _, err := session.NormalizeWorkspaceBinding(projectID, repoRoot, draft.WorkspaceBinding); err != nil {
		return nil, fmt.Errorf("invalid managed session request: %w", err)
	}

	ctx, cancel := context.WithTimeout(f.ctx(), managedProvisionTimeout)
	defer cancel()

	owner := f.worktreeOwner()
	if createBranch {
		if _, err := owner.ProvisionNewBranch(ctx, repoRoot, treeName, branch, startPoint); err != nil {
			return nil, err
		}
	} else if _, err := owner.ProvisionBranch(ctx, repoRoot, treeName, branch); err != nil {
		return nil, err
	}

	info, err := f.app.Manager().CreateSessionFromDraft(draft, repoRoot)
	if err != nil {
		f.compensateProvisionedTree(ctx, repoRoot, treeName, "session commit failed")
		return nil, err
	}
	// The persisted managed binding is REQUIRED, not best-effort: a managed
	// session that exists only in memory would restore nowhere after a
	// restart. A store failure therefore rolls the whole creation back.
	if err := f.store.SaveSession(ctx, *info); err != nil {
		f.compensateProvisionedTree(ctx, repoRoot, treeName, "session persistence failed")
		if delErr := f.app.Manager().DeleteSession(info.ID); delErr != nil {
			f.log().Warn("failed to remove unpersisted session after store failure", "session_id", info.ID, "error", delErr)
		}
		return nil, fmt.Errorf("failed to persist managed session binding: %w", err)
	}
	// Seed the tree's embedding cache from the checkout's — after the success
	// point (a failed creation must leave no seeded state) and before the RPC
	// returns (the frontend switches to the new session immediately, and that
	// focus move builds the tree's vector manager with this cache root).
	f.seedWorktreeEmbeddingCache(projectID, treeName)
	return info, nil
}

// compensateProvisionedTree releases a just-provisioned tree after a later
// step of creation failed. Best-effort but loud: the primitives keep
// branches alive, so a failed compensation leaves an empty tree on a fresh
// branch — visible in the worktree list, never silent data loss. The fresh
// tree is clean by construction, so no Force is passed; if it is somehow
// dirty the release refuses and the warning says so.
func (f *FrontendAPI) compensateProvisionedTree(ctx context.Context, repoRoot, treeName, cause string) {
	if err := f.worktreeOwner().Release(ctx, repoRoot, treeName, workspace.RemoveWorktreeOptions{}); err != nil {
		f.log().Warn("compensation failed: provisioned worktree left in place",
			"tree", treeName, "repo", repoRoot, "cause", cause, "error", err)
		return
	}
	f.log().Info("compensated provisioned worktree after failure", "tree", treeName, "repo", repoRoot, "cause", cause)
}

// forkManagedSession forks a session that owns a managed worktree into its
// OWN tree on a derived branch (<source branch>-fork-<short id>) created at
// the source tree's COMMITTED HEAD. Uncommitted changes in the source tree
// are NOT copied — the fork's starting point is the commit, and this path
// makes no uncommitted-copy claim; the source tree is left byte-identical.
// The store fork (deep row copy) and the tree provisioning are compensated
// against each other: a store failure releases the freshly provisioned tree
// (its branch is kept).
func (f *FrontendAPI) forkManagedSession(ctx context.Context, src *session.SessionInfo, cloneReview session.ForkReviewCloner) (*session.SessionInfo, error) {
	binding := src.WorkspaceBinding
	repoRoot, err := f.resolveProjectRoot(ctx, src.ProjectID)
	if err != nil {
		return nil, fmt.Errorf("cannot resolve project root for managed fork: %w", err)
	}

	owner := f.worktreeOwner()
	entry, err := owner.Inspect(ctx, repoRoot, binding.WorktreeName)
	if err != nil {
		if errors.Is(err, workspace.ErrWorktreeNotLinked) {
			return nil, fmt.Errorf("cannot fork: the source session's worktree %q is missing — restore the session first", binding.WorktreeName)
		}
		return nil, err
	}
	sourceHead := entry.Head // the committed HEAD; uncommitted work is deliberately excluded

	provisionCtx, cancel := context.WithTimeout(ctx, managedProvisionTimeout)
	defer cancel()

	draft := session.NewSessionDraft(src.ProjectID, nil)
	treeName := managedTreeName(draft.ID)
	branch := forkBranchName(binding.Branch, draft.ID)
	if _, err := owner.ProvisionNewBranch(provisionCtx, repoRoot, treeName, branch, sourceHead); err != nil {
		return nil, err
	}

	commit := f.managedForkCommit
	if commit == nil {
		commit = f.store.ForkSessionWithBinding
	}
	info, err := commit(ctx, src.ID, draft.ID, &session.WorkspaceBinding{
		Kind:         session.WorkspaceManagedWorktree,
		WorktreeName: treeName,
		Branch:       branch,
	}, cloneReview)
	if err != nil {
		f.compensateProvisionedTree(provisionCtx, repoRoot, treeName, "fork persistence failed")
		return nil, err
	}
	// Same contract as CreateManagedSession: after the success point, before
	// the RPC returns and the frontend focuses the fork's tree. A fork's tree
	// starts at the source's committed HEAD, so the checkout cache is a
	// near-perfect match for its first index pass.
	f.seedWorktreeEmbeddingCache(src.ProjectID, treeName)
	return info, nil
}

// resolveProjectRoot resolves a project's registered repository root and
// validates its shape. Deletion, restore, and fork all derive tree paths
// from it; an unusable root must fail those flows explicitly rather than
// guess.
func (f *FrontendAPI) resolveProjectRoot(ctx context.Context, projectID string) (string, error) {
	if f.projectManager == nil {
		return "", errors.New("project manager not initialized")
	}
	proj, err := f.projectManager.GetProject(projectID)
	if err != nil {
		return "", fmt.Errorf("load project %s: %w", projectID, err)
	}
	if proj == nil {
		return "", fmt.Errorf("project %s not found", projectID)
	}
	root := proj.WorkspacePath
	if root == "" || !filepath.IsAbs(root) || filepath.Clean(root) != root {
		return "", fmt.Errorf("project %s has no absolute registered workspace root", projectID)
	}
	return root, nil
}

// prepareManagedTreeDeletion runs the managed part of session deletion
// BEFORE any session state is removed, so a blocked or failed removal leaves
// the session fully retryable. It is a no-op (returning nil) for CHAT and
// local sessions, for sessions that no longer exist, and — deliberately —
// for a store read failure (the tolerant legacy path then handles cleanup,
// matching DeleteSession's established best-effort contract for internal
// files; a tree can be released by retrying the deletion once the store is
// reachable again).
func (f *FrontendAPI) prepareManagedTreeDeletion(ctx context.Context, id string, opts SessionDeleteOptions) error {
	if f.store == nil {
		return nil
	}
	info, err := f.store.LoadSession(ctx, id)
	if err != nil {
		f.log().Warn("failed to load session for managed-tree deletion check", "session_id", id, "error", err)
		return nil
	}
	if info == nil || info.WorkspaceBinding == nil || info.WorkspaceBinding.Kind != session.WorkspaceManagedWorktree {
		return nil
	}
	binding := info.WorkspaceBinding

	// 1. Join the running task first: its final writes must be visible to
	// the dirty recheck below. CancelTask signals cancellation and waits
	// (bounded by the manager's stop timeout) for the task goroutine.
	if mgr := f.app.Manager(); mgr != nil {
		if status, serr := mgr.GetSessionRuntimeStatus(id); serr == nil && status.Active {
			if cerr := mgr.CancelTask(id); cerr != nil {
				f.log().Warn("failed to cancel running task before session deletion", "session_id", id, "error", cerr)
			}
		}
	}
	// 2. Stop the session terminal: its shell's working directory lives
	// inside the tree being removed.
	f.stopSessionTerminal(id)

	// 3. Resolve the repository root and release the tree — the removal
	// primitives recheck dirtiness and lock state themselves, at removal
	// time, so the confirmation below is never based on a stale snapshot.
	repoRoot, err := f.resolveProjectRoot(ctx, info.ProjectID)
	if err != nil {
		return fmt.Errorf("cannot resolve project root to release session worktree: %w", err)
	}
	if err := f.releaseSessionTree(ctx, repoRoot, binding, opts); err != nil {
		return err
	}
	// The tree is gone: drop its worktree-scoped vector-index storage too
	// (best-effort — leftover data would only be disk garbage on a tree no
	// session can be bound to again, since worktree names are per-session).
	// The registry release first shuts the root's live manager (closing its
	// open chromem handles so the removal works on Windows too).
	f.deleteWorktreeVectorIndex(repoRoot, info.ProjectID, binding.WorktreeName)
	return nil
}

// releaseSessionTree removes the session's managed tree per the caller's
// explicit decisions. Never-removal boundaries: a tree that is not linked
// anymore is already gone (nothing to do), and a tree classified as
// non-managed (main/external) is left untouched — session deletion never
// removes a foreign checkout. The branch is never deleted by any path here.
func (f *FrontendAPI) releaseSessionTree(ctx context.Context, repoRoot string, binding *session.WorkspaceBinding, opts SessionDeleteOptions) error {
	owner := f.worktreeOwner()
	entry, err := owner.Inspect(ctx, repoRoot, binding.WorktreeName)
	if err != nil {
		if errors.Is(err, workspace.ErrWorktreeNotLinked) {
			return nil
		}
		return fmt.Errorf("inspect session worktree %q: %w", binding.WorktreeName, err)
	}
	if entry.Kind != workspace.WorktreeManaged {
		f.log().Warn("session deletion: path holds a non-managed worktree; leaving it in place",
			"path", entry.Path, "kind", string(entry.Kind), "repo", repoRoot)
		return nil
	}
	if err := owner.Release(ctx, repoRoot, binding.WorktreeName, workspace.RemoveWorktreeOptions{
		Force:  opts.ConfirmUncommittedLoss,
		Unlock: opts.UnlockLockedTree,
	}); err != nil {
		switch {
		case errors.Is(err, workspace.ErrWorktreeDirty) && !opts.ConfirmUncommittedLoss:
			return &SessionDeleteBlockedError{
				Reason: fmt.Sprintf("the session's worktree %q (branch %s) contains uncommitted changes; deleting the session removes the tree and those changes cannot be recovered (the branch itself is kept)", binding.WorktreeName, binding.Branch),
				Option: "confirm_uncommitted_loss",
			}
		case errors.Is(err, workspace.ErrWorktreeLocked) && !opts.UnlockLockedTree:
			return &SessionDeleteBlockedError{
				Reason: fmt.Sprintf("the session's worktree %q is locked (%s)", binding.WorktreeName, lockedReasonOf(err)),
				Option: "unlock_locked_tree",
			}
		default:
			return fmt.Errorf("release session worktree %q: %w", binding.WorktreeName, err)
		}
	}
	return nil
}

// lockedReasonOf extracts the primitive's verbatim lock reason when present.
func lockedReasonOf(err error) string {
	var state *workspace.StateError
	if errors.As(err, &state) {
		return state.Reason
	}
	return "no reason recorded"
}

// stopSessionTerminal stops the session's terminal, if any, and reports
// whether a live terminal was stopped. Shared by the managed pre-flight
// (before tree removal) and the legacy deletion path (idempotent: IsActive
// gates the Stop). The promotion flow uses the report to emit
// session:<id>:terminal_exited — the one explicit stop the frontend must
// hear about, because its terminal instance survives the promotion.
func (f *FrontendAPI) stopSessionTerminal(id string) bool {
	if f.terminalManager == nil || !f.terminalManager.IsActive(id) {
		return false
	}
	if err := f.terminalManager.Stop(id); err != nil {
		f.log().Warn("failed to stop terminal for session", "session_id", id, "error", err)
	}
	return true
}
