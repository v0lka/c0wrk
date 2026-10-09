// frontend_api_worktrees.go — the session-lifecycle side of ADR-080 managed
// worktrees. The worktrees.Owner coordinator (step: safe primitives) is
// deliberately stateless; this file is the "Git lifecycle owner" that drives
// it from the session RPC flows:
//
//   - CreateManagedSession: reserve identity (SessionDraft) → provision the
//     tree → commit the runtime session → persist the binding, compensating
//     (releasing the fresh tree) when the runtime or DB step fails. An
//     adopted (shared) tree commits its binding and re-validates the tree
//     inside the adoption↔deletion critical section (managedTreeMu), rolling
//     the just-committed session back when the last owner's deletion removed
//     the tree in between.
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
//     stays retryable until the user confirms the loss. The [count remaining
//     co-owners → release] pair runs under the same critical section as the
//     adoption commit, so a shared tree is only ever released for a last
//     owner whose count saw every committed binding.
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

// CreateManagedSession creates a new CODE session bound to a managed
// worktree under <repo>/.worktrees before any orchestrator, logger, or
// persisted row exists (ADR-080).
//
// branch names the git branch the tree checks out. When createBranch is
// true, the branch is CREATED at startPoint (empty startPoint = the
// repository's current HEAD) instead of being required to already exist; an
// empty branch with createBranch derives "session-<short id>".
//
// A branch can be checked out in at most one worktree, so an existing holder
// decides what "create a session on this branch" means (the shared-tree
// revision of ADR-080):
//   - a managed tree already holds the branch → the new session is bound to
//     THAT tree (several sessions may execute in one tree, mirroring how
//     local sessions share the checkout);
//   - the main checkout holds the branch → a plain local session is created
//     (a managed tree can never hold a branch the checkout holds);
//   - an external (non-c0wrk) linked worktree holds the branch → explicit
//     ErrBranchBusy refusal.
//
// On success the persisted session row carries the managed binding (the
// binding is durable before the session is claimed). If the runtime session
// or the persistence step fails after provisioning a FRESH tree, that tree
// is released again (compensation); adopted trees are never touched. The
// created branch is kept — no operation in this path ever deletes a branch.
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
	binding := &session.WorkspaceBinding{
		Kind:         session.WorkspaceManagedWorktree,
		WorktreeName: treeName,
		Branch:       branch,
	}
	// Validate the whole binding (branch syntax, tree name, containment)
	// BEFORE any git runs, so a malformed request never provisions anything.
	if _, err := session.NormalizeWorkspaceBinding(projectID, repoRoot, binding); err != nil {
		return nil, fmt.Errorf("invalid managed session request: %w", err)
	}

	ctx, cancel := context.WithTimeout(f.ctx(), managedProvisionTimeout)
	defer cancel()

	owner := f.worktreeOwner()
	if !createBranch {
		trees, err := owner.List(ctx, repoRoot)
		if err != nil {
			return nil, fmt.Errorf("listing worktrees of %s: %w", repoRoot, err)
		}
		if holder := branchHolder(trees, branch); holder != nil {
			switch holder.Kind {
			case workspace.WorktreeManaged:
				return f.adoptManagedTree(ctx, owner, repoRoot, projectID, branch, holder)
			case workspace.WorktreeMain:
				return f.createLocalFallbackSession(projectID, repoRoot, branch)
			default:
				return nil, fmt.Errorf("%w: branch %s is checked out in external worktree %s — created outside c0wrk; use a local session or remove that tree first", workspace.ErrBranchBusy, branch, holder.Path)
			}
		}
	}

	draft.WorkspaceBinding = binding
	if createBranch {
		if _, err := owner.ProvisionNewBranch(ctx, repoRoot, treeName, branch, startPoint); err != nil {
			return nil, err
		}
	} else if _, err := owner.ProvisionBranch(ctx, repoRoot, treeName, branch); err != nil {
		return nil, err
	}
	info, err := f.commitManagedDraft(ctx, draft, repoRoot, treeName)
	if err != nil {
		return nil, err
	}
	// Seed the tree's embedding cache from the checkout's — after the success
	// point (a failed creation must leave no seeded state) and before the RPC
	// returns (the frontend switches to the new session immediately, and that
	// focus move builds the tree's vector manager with this cache root).
	f.seedWorktreeEmbeddingCache(projectID, treeName)
	return info, nil
}

// branchHolder returns the worktree entry currently holding branch, or nil
// when the branch is not checked out anywhere.
func branchHolder(trees []workspace.WorktreeInfo, branch string) *workspace.WorktreeInfo {
	for i := range trees {
		if trees[i].Branch == branch {
			return &trees[i]
		}
	}
	return nil
}

// adoptManagedTree binds a new session to an EXISTING managed worktree (the
// shared-tree revision of ADR-080): the binding carries the tree's own name,
// so the derived execution path is the tree already holding the branch.
// owner.Recreate re-validates the tree at adoption time — a no-op for a live
// tree on the pinned branch, a prune+recreate for stale metadata, and an
// explicit mismatch/missing-branch failure when the world moved between the
// listing above and here. The commit itself re-validates the tree after the
// binding is persisted (see bindAdoptedSession): the last owner's deletion
// may release the tree between the Recreate above and the commit, and a
// session bound to a tree that no longer exists would have no self-healing
// until restart. Nothing is compensated: the tree pre-existed with its own
// sessions and is never touched by this creation's failures.
func (f *FrontendAPI) adoptManagedTree(ctx context.Context, owner *worktrees.Owner, repoRoot, projectID, branch string, holder *workspace.WorktreeInfo) (*session.SessionInfo, error) {
	if _, err := owner.Recreate(ctx, repoRoot, holder.Name, branch); err != nil {
		return nil, err
	}
	draft := session.NewSessionDraft(projectID, &session.WorkspaceBinding{
		Kind:         session.WorkspaceManagedWorktree,
		WorktreeName: holder.Name,
		Branch:       branch,
	})
	info, err := f.bindAdoptedSession(ctx, owner, repoRoot, draft, holder.Name)
	if err != nil {
		return nil, err
	}
	// Idempotent: the tree's first session seeded the cache already; a fresh
	// seed keeps adopted (previously orphaned) trees warm too.
	f.seedWorktreeEmbeddingCache(projectID, holder.Name)
	return info, nil
}

// bindAdoptedSession commits the adopted-tree draft and re-validates the
// tree inside the adoption↔deletion critical section (managedTreeMu). The
// re-validation is what closes the shared-tree race: the release's
// [count co-owners → remove tree] pair runs under the same mutex
// (releaseTreeIfLastOwnerLocked), so a concurrent last-owner deletion either
// counts this session's committed binding (release skipped) or removes the
// tree BEFORE this binding is committed — in which case the re-validation
// below fails and the just-committed session is rolled back instead of
// surviving bound to a removed tree.
func (f *FrontendAPI) bindAdoptedSession(ctx context.Context, owner *worktrees.Owner, repoRoot string, draft session.SessionDraft, treeName string) (*session.SessionInfo, error) {
	f.managedTreeMu.Lock()
	defer f.managedTreeMu.Unlock()
	return f.bindAdoptedSessionLocked(ctx, owner, repoRoot, draft, treeName)
}

// bindAdoptedSessionLocked is bindAdoptedSession without the locking; the
// caller must hold managedTreeMu (tests call it directly while holding the
// mutex to pin the interleaving the lock excludes).
func (f *FrontendAPI) bindAdoptedSessionLocked(ctx context.Context, owner *worktrees.Owner, repoRoot string, draft session.SessionDraft, treeName string) (*session.SessionInfo, error) {
	info, err := f.commitManagedDraft(ctx, draft, repoRoot, "")
	if err != nil {
		return nil, err
	}
	if _, err := owner.Inspect(ctx, repoRoot, treeName); err != nil {
		f.rollbackAdoptedSession(ctx, info, treeName, err)
		return nil, fmt.Errorf("managed worktree %q was gone at the post-commit re-validation (removed by a concurrent last-owner deletion); the session was rolled back — retry the creation to provision a fresh tree: %w", treeName, err)
	}
	return info, nil
}

// rollbackAdoptedSession undoes a just-committed adoption whose tree was
// removed concurrently by the last owner's deletion. Both halves of the
// session go: the persisted row (it would restore into a missing tree) and
// the in-memory session (the restore ensurer never runs for in-memory
// sessions, so it could not self-heal before restart). Best-effort but loud:
// a row that survives a failed delete self-heals on the next restore (the
// ensurer re-provisions the tree from the pinned branch), and the branch is
// never touched.
func (f *FrontendAPI) rollbackAdoptedSession(ctx context.Context, info *session.SessionInfo, treeName string, cause error) {
	if err := f.store.DeleteSession(ctx, info.ID); err != nil {
		f.log().Error("failed to roll back the persisted binding of an adopted session whose tree was removed",
			"session_id", info.ID, "worktree", treeName, "error", err)
	}
	if err := f.app.Manager().DeleteSession(info.ID); err != nil {
		f.log().Warn("failed to remove the in-memory session of an adopted session whose tree was removed",
			"session_id", info.ID, "worktree", treeName, "error", err)
	}
	f.log().Info("rolled back adopted session: its tree was removed by the last owner's deletion during creation",
		"session_id", info.ID, "worktree", treeName, "cause", cause)
}

// createLocalFallbackSession serves a managed-session request whose branch is
// checked out in the project's MAIN worktree: a managed tree can never hold
// that branch, so the session runs in the checkout itself — exactly what a
// plain local session does. Persistence stays best-effort (the CreateSession
// contract): nothing was provisioned, so there is nothing to roll back.
func (f *FrontendAPI) createLocalFallbackSession(projectID, repoRoot, branch string) (*session.SessionInfo, error) {
	info, err := f.app.Manager().CreateSession(projectID, repoRoot)
	if err != nil {
		return nil, fmt.Errorf("branch %s is held by the project checkout and the local fallback session failed: %w", branch, err)
	}
	if f.store != nil {
		if err := f.store.SaveSession(context.Background(), *info); err != nil {
			f.log().Error("failed to save local fallback session to store", "error", err)
		}
	}
	return info, nil
}

// commitManagedDraft turns a validated managed draft into a runtime session
// and persists its binding. Persistence is REQUIRED, not best-effort: a
// managed session that exists only in memory would restore nowhere after a
// restart, so a store failure rolls the whole creation back. The freshly
// provisioned tree named by compensateTreeName is released on any failure —
// pass "" for an ADOPTED tree, which pre-existed and must never be touched.
func (f *FrontendAPI) commitManagedDraft(ctx context.Context, draft session.SessionDraft, repoRoot, compensateTreeName string) (*session.SessionInfo, error) {
	info, err := f.app.Manager().CreateSessionFromDraft(draft, repoRoot)
	if err != nil {
		f.compensateProvisionedTree(ctx, repoRoot, compensateTreeName, "session commit failed")
		return nil, err
	}
	if err := f.store.SaveSession(ctx, *info); err != nil {
		f.compensateProvisionedTree(ctx, repoRoot, compensateTreeName, "session persistence failed")
		if delErr := f.app.Manager().DeleteSession(info.ID); delErr != nil {
			f.log().Warn("failed to remove unpersisted session after store failure", "session_id", info.ID, "error", delErr)
		}
		return nil, fmt.Errorf("failed to persist managed session binding: %w", err)
	}
	return info, nil
}

// compensateProvisionedTree releases a just-provisioned tree after a later
// step of creation failed. Best-effort but loud: the primitives keep
// branches alive, so a failed compensation leaves an empty tree on a fresh
// branch — visible in the worktree list, never silent data loss. The fresh
// tree is clean by construction, so no Force is passed; if it is somehow
// dirty the release refuses and the warning says so.
func (f *FrontendAPI) compensateProvisionedTree(ctx context.Context, repoRoot, treeName, cause string) {
	if treeName == "" {
		return // an adopted tree: it pre-existed and is never touched
	}
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
// the session fully retryable. The one exception is the shared-tree co-owner
// path: when other sessions still bind the tree, no removal (and therefore
// no blocking confirmation) can happen, so the pre-flight removes the
// deleting session's own store row itself, inside the critical section — a
// concurrent co-owner's count must observe it gone, or two simultaneous
// deletions of the last two owners would both skip the release and orphan
// the tree. It then returns the session's project ID so the caller's
// store-only fallback can still clean the session's internal files (the row
// they would normally be resolved from is gone); it returns "" when the row
// was left to the caller. The pre-flight is otherwise a no-op (returning "")
// for CHAT and local sessions, for sessions that no longer exist, and —
// deliberately — for a store read failure (the tolerant legacy path then
// handles cleanup, matching DeleteSession's established best-effort contract
// for internal files; a tree can be released by retrying the deletion once
// the store is reachable again).
func (f *FrontendAPI) prepareManagedTreeDeletion(ctx context.Context, id string, opts SessionDeleteOptions) (cleanupProjectID string, err error) {
	if f.store == nil {
		return "", nil
	}
	info, err := f.store.LoadSession(ctx, id)
	if err != nil {
		f.log().Warn("failed to load session for managed-tree deletion check", "session_id", id, "error", err)
		return "", nil
	}
	if info == nil || info.WorkspaceBinding == nil || info.WorkspaceBinding.Kind != session.WorkspaceManagedWorktree {
		return "", nil
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

	// 3. The [co-owner count → own-row removal → release] protocol runs
	// inside the adoption↔deletion critical section (managedTreeMu, see
	// bindAdoptedSession): an adoption that is committing its binding either
	// finishes first and is counted below, or commits after the release and
	// re-validates against the already-removed tree, rolling itself back —
	// and a concurrent co-owner deletion either observes this session's row
	// removed and releases, or removes its own row first for this session to
	// observe.
	f.managedTreeMu.Lock()
	released, rowGoneProjectID, repoRoot, releaseErr := f.removeOwnerAndReleaseTreeLocked(ctx, info.ProjectID, binding, id, opts)
	f.managedTreeMu.Unlock()
	if releaseErr != nil {
		return "", releaseErr
	}
	if released {
		// The tree is gone: drop its worktree-scoped vector-index storage
		// too (best-effort — leftover data would only be derived garbage:
		// in the narrow resurrection window an in-flight adoption's Recreate
		// can re-create a just-released tree from its branch before this
		// cleanup runs, and only that tree's derived vector/cache data is
		// then discarded — re-indexed on demand). The registry release first
		// shuts the root's live manager (closing its open chromem handles so
		// the removal works on Windows too).
		f.deleteWorktreeVectorIndex(repoRoot, info.ProjectID, binding.WorktreeName)
	}
	return rowGoneProjectID, nil
}

// removeOwnerAndReleaseTreeLocked removes the deleting session's row from the
// shared tree's owner set and releases the tree only when that made it the
// LAST owner. When co-owners remain, the row is still removed (so their
// counts observe it gone — see prepareManagedTreeDeletion for why that must
// happen inside the critical section), but the tree — and any uncommitted
// work in it — stays for the remaining sessions and the dirty/locked
// confirmations are simply never reached; the returned project ID then tells
// the caller the row is already gone. A count failure blocks the deletion
// (retryable) instead of risking a shared tree's removal. The caller must
// hold managedTreeMu so this [count → row removal → release] protocol cannot
// interleave with an adoption's [commit binding → re-validate tree] pair
// (bindAdoptedSession) or with another deletion's protocol.
func (f *FrontendAPI) removeOwnerAndReleaseTreeLocked(ctx context.Context, projectID string, binding *session.WorkspaceBinding, deletingSessionID string, opts SessionDeleteOptions) (released bool, rowGoneProjectID, repoRoot string, err error) {
	sharers, err := f.store.CountManagedWorktreeSessions(ctx, binding.WorktreeName, deletingSessionID)
	if err != nil {
		return false, "", "", fmt.Errorf("count sessions sharing worktree %q: %w", binding.WorktreeName, err)
	}
	if sharers > 0 {
		f.log().Debug("skipping managed tree release: other sessions still share it",
			"session_id", deletingSessionID, "worktree", binding.WorktreeName, "other_sessions", sharers)
		if err := f.store.DeleteSession(ctx, deletingSessionID); err != nil {
			return false, "", "", fmt.Errorf("remove deleted session %s from the owner set of worktree %q: %w", deletingSessionID, binding.WorktreeName, err)
		}
		return false, projectID, "", nil
	}
	// The removal primitives recheck dirtiness and lock state themselves, at
	// removal time, so the confirmation is never based on a stale snapshot.
	repoRoot, err = f.resolveProjectRoot(ctx, projectID)
	if err != nil {
		return false, "", "", fmt.Errorf("cannot resolve project root to release session worktree: %w", err)
	}
	if err := f.releaseSessionTree(ctx, repoRoot, binding, opts); err != nil {
		return false, "", "", err
	}
	return true, "", repoRoot, nil
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
