// frontend_api_worktrees.go — the session-lifecycle side of ADR-080 managed
// worktrees: the "Git lifecycle owner" driving the stateless worktrees.Owner
// coordinator from the session RPC flows.
// Protocols (binding, shared trees, release, fork): specs/domains/session-worktrees.md.
package backend

import (
	"context"
	"errors"
	"fmt"
	"path/filepath"
	"strings"
	"time"

	"github.com/v0lka/c0wrk/backend/config"
	"github.com/v0lka/c0wrk/backend/project"
	"github.com/v0lka/c0wrk/backend/session"
	"github.com/v0lka/c0wrk/backend/worktrees"
	"github.com/v0lka/c0wrk/core"
	"github.com/v0lka/c0wrk/core/workspace"
)

// managedProvisionTimeout bounds one provisioning git operation (worktree add
// materializes a full checkout): generous — a wedged git must fail explicitly,
// never hang.
const managedProvisionTimeout = 10 * time.Minute

// canonicalWorktreeRoot returns the symlink-resolved form of a repository root
// (fail-soft to the given form). Every seam feeding the git-facing worktree
// primitives must present this canonical form; stored paths, event payloads,
// and frontend keying stay verbatim.
func canonicalWorktreeRoot(path string) string {
	if resolved, err := filepath.EvalSymlinks(path); err == nil {
		return resolved
	}
	return path
}

// SessionDeleteOptions carries the user's explicit deletion decisions for a
// managed worktree; a deletion needing an unset flag fails with
// *SessionDeleteBlockedError and stays retryable.
// See specs/domains/session-worktrees.md (Release).
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

// SessionDeleteBlockedError reports that deletion needs an explicit user
// decision: session, tree, and branch are untouched — re-issue with the
// named option once the user confirms.
type SessionDeleteBlockedError struct {
	// Reason is the human-readable explanation shown to the user.
	Reason string `json:"reason"`
	// Option names the SessionDeleteOptions field that authorizes proceeding.
	Option string `json:"option"`
}

func (e *SessionDeleteBlockedError) Error() string {
	return fmt.Sprintf("session deletion blocked: %s (confirm via %s)", e.Reason, e.Option)
}

// worktreeOwner returns the backend ownership coordinator; the stateless
// Owner (a logger wrapper over the hardened core/workspace primitives) makes
// per-call construction safe.
func (f *FrontendAPI) worktreeOwner() *worktrees.Owner {
	return worktrees.NewOwner(f.log())
}

// newWorktreeOwner samples config only for provisioning a fresh tree.
func (f *FrontendAPI) newWorktreeOwner(projectID string) (*worktrees.Owner, error) {
	f.configMu.RLock()
	override := ""
	if f.config != nil {
		override = f.config.Git.WorktreesRoot
	}
	f.configMu.RUnlock()
	container, err := config.NewManagedWorktreesDir(f.agentDir, projectID, override)
	if err != nil {
		return nil, err
	}
	return worktrees.NewOwner(f.log(), container), nil
}

// shortIdentity derives the short identity suffix (first 8 chars) for
// derived tree and branch names; session ids are UUIDs, so the prefix is hex.
func shortIdentity(id string) string {
	if len(id) < 8 {
		return id
	}
	return id[:8]
}

// managedTreeName derives the managed worktree name (a single portable path
// component, re-validated inside every owner operation).
func managedTreeName(sessionID string) string {
	return "s-" + shortIdentity(sessionID)
}

// forkBranchName derives the fork branch: <source branch>-fork-<short id> —
// sortable, collision-free, never reusing the source branch.
func forkBranchName(sourceBranch, dstSessionID string) string {
	return sourceBranch + "-fork-" + shortIdentity(dstSessionID)
}

// installWorkspaceEnsurer wires the manager's lazy-restore hook (from
// NewFrontendAPI); without it managed restores fail closed —
// struct-literal-constructed APIs must call it or SetWorkspaceEnsurer.
func (f *FrontendAPI) installWorkspaceEnsurer() {
	if f.appCell() == nil {
		return
	}
	if m := f.app.Manager(); m != nil {
		m.SetWorkspaceEnsurer(f.ensureManagedWorkspace)
	}
}

// ensureManagedWorkspace is the WorkspaceEnsurer on the session manager: it
// guarantees the tree exists on the pinned branch (binding already normalized
// by the manager) and reports recreated=true exactly when the tree (or its
// stale metadata) was absent and had to be recreated from the branch — the
// manager surfaces that as an explicit data-loss warning.
// See specs/domains/session-worktrees.md.
func (f *FrontendAPI) ensureManagedWorkspace(ctx context.Context, repoRoot string, binding *session.WorkspaceBinding) (bool, error) {
	// Canonicalize the root the resolver handed back (fail-soft — see
	// canonicalWorktreeRoot): legacy rows may keep a symlinked ancestor.
	repoRoot = canonicalWorktreeRoot(repoRoot)
	owner := f.worktreeOwner()
	trees, err := owner.List(ctx, repoRoot)
	if err != nil {
		return false, fmt.Errorf("list worktrees of %s: %w", repoRoot, err)
	}
	// Already-normalized binding: the durable path is used exactly as recorded
	// — re-recognizing it would re-gate restore on the container marker and
	// break restore after marker/container removal. Only a pre-durable row
	// (empty path) resolves from the live list or historical container.
	target := canonicalWorktreeRoot(binding.WorkspacePath)
	if target == "" {
		target, err = owner.Path(ctx, repoRoot, binding.WorktreeName)
		if err != nil {
			return false, err
		}
	}
	// Probe the durable path in canonical form, matching Git's listing even
	// for legacy registrations containing an operator/system symlink.
	entry := workspace.FindWorktree(trees, target)
	missing := entry == nil || !entry.IsLinked() || entry.Prunable
	if _, err := owner.RecreateAt(ctx, repoRoot, binding.WorktreeName, binding.Branch, target); err != nil {
		return false, err
	}
	// Re-establish the container's ownership marker when absent (e.g. the
	// app-data container was cleaned and the tree recreated here) —
	// classification keys on it, so an ensured tree must stay recognizable
	// as managed. Best-effort; the legacy checkout-local container is
	// marker-less by design and never marked.
	if container := filepath.Dir(target); filepath.Base(container) == core.ProjectWorktreesSegment {
		if err := core.MarkWorktreeContainer(repoRoot, container); err != nil {
			f.log().Warn("could not re-establish the managed worktree container marker",
				"repo", repoRoot, "container", container, "error", err)
		}
	}
	return missing, nil
}

// SessionWorkspaceChoice is the send-time workspace request of the draft-free
// creation flow (ADR-091): the first message of a fresh session may carry the
// choice made in the chat toolbar's selector; nil (every ordinary send) is a
// no-op. See specs/domains/session-worktrees.md (Binding).
type SessionWorkspaceChoice struct {
	// Branch is the picked branch; empty with CreateBranch=true derives a
	// session-scoped branch name (mirroring the former CreateManagedSession).
	Branch string `json:"branch"`
	// CreateBranch requests a MISSING branch be created at StartPoint (empty
	// = repository HEAD); an EXISTING branch is never re-created — git DWIM
	// semantics: the request behaves like an existing-branch pick.
	CreateBranch bool `json:"create_branch"`
	// StartPoint is the creation start point for a missing branch; a
	// remote-tracking pick lands here ("origin/x" creates local x).
	StartPoint string `json:"start_point"`
}

// Bind errors wrap the returned error so callers classify with errors.Is.
var (
	// ErrBindInProgress reports that another send for this session is already
	// applying a workspace bind. The frontend's re-entrancy guard makes this
	// unreachable from the UI; it is the backend backstop.
	ErrBindInProgress = errors.New("a workspace bind is already in progress for this session")
	// ErrWorkspaceAlreadyChosen reports that the session's fresh window
	// already persisted a managed-worktree binding for a DIFFERENT choice —
	// the workspace is chosen once per fresh session.
	ErrWorkspaceAlreadyChosen = errors.New("session workspace already chosen")
)

// bindSessionWorkspace applies a send-time workspace choice to a FRESH session
// (atomically — the store's BindSessionWorkspace). Branch-holder dispatch: a
// managed tree ADOPTS (shared trees, ADR-082), the project checkout is a
// no-op success, an external tree is a typed ErrBranchBusy refusal; a
// provisioned tree is compensated on every later failure, adopted trees never
// touched. The binding write is followed by an in-memory eviction (terminal
// stop first, staged attachments carried over the memory-only snapshot) so
// the send's own access restores an orchestrator built for the NEW workspace.
// Full flow: specs/domains/session-worktrees.md (Binding).
func (f *FrontendAPI) bindSessionWorkspace(ctx context.Context, id string, choice *SessionWorkspaceChoice) error {
	if choice == nil {
		return nil
	}
	if f.appCell() == nil || f.app.Manager() == nil {
		return errors.New("session manager not initialized - check startup logs for LLM router or configuration errors")
	}
	if f.store == nil {
		return errors.New("session store not initialized")
	}
	// One bind at a time per session: a second send carrying a choice would
	// race the same fresh window (double provision, conflicting compensations).
	if _, busy := f.bindInFlight.LoadOrStore(id, struct{}{}); busy {
		return fmt.Errorf("%w: wait for the in-flight send to settle", ErrBindInProgress)
	}
	defer f.bindInFlight.Delete(id)
	ctx, cancel := context.WithTimeout(ctx, managedProvisionTimeout)
	defer cancel()

	info, err := f.store.LoadSession(ctx, id)
	if err != nil {
		return fmt.Errorf("load session for workspace binding: %w", err)
	}
	if info == nil {
		return fmt.Errorf("session %s not found", id)
	}
	projectID := info.ProjectID
	if projectID == project.NoProjectID {
		return errors.New("CHAT (No Project) sessions cannot own a managed worktree")
	}
	if info.HasUnfinishedTask {
		return fmt.Errorf("%w: the session already has an unfinished task", session.ErrSessionNotFresh)
	}
	// Archived sessions are read-only; the authoritative ErrSessionArchived
	// gate would only fire AFTER provision+persist+evict — refuse before any
	// of that runs.
	if info.Archived {
		return fmt.Errorf("%w: a read-only session cannot have its workspace bound", session.ErrSessionArchived)
	}
	// Refuse a running session before any git runs (the store guard below
	// remains the authoritative race-closer).
	if status, statusErr := f.app.Manager().GetSessionRuntimeStatus(id); statusErr == nil && status.Active {
		return fmt.Errorf("%w: the session has an active task", session.ErrSessionNotFresh)
	}

	branch := strings.TrimSpace(choice.Branch)
	if choice.CreateBranch && branch == "" {
		branch = "session-" + shortIdentity(id)
	}
	if branch == "" {
		return errors.New("a branch is required — name an existing branch or pass create_branch=true")
	}
	// A fresh window that already persists a managed binding (a previous
	// send's bind succeeded but the send failed) accepts only the SAME choice
	// (no-op success); a DIFFERENT one is refused — dispatching by branch
	// holder alone would silently re-provision (orphaning the tree) or no-op.
	// Deleting the session resets the window.
	if prev := info.WorkspaceBinding; prev != nil && prev.Kind == session.WorkspaceManagedWorktree {
		if prev.Branch == branch {
			return nil
		}
		return fmt.Errorf("%w: the session is already bound to managed worktree %q on branch %q (the earlier send provisioned it before failing); the workspace is chosen once per fresh session — delete the session to choose again", ErrWorkspaceAlreadyChosen, prev.WorktreeName, prev.Branch)
	}
	repoRoot, err := f.resolveProjectRoot(ctx, projectID)
	if err != nil {
		return err
	}
	binding := &session.WorkspaceBinding{
		Kind:         session.WorkspaceManagedWorktree,
		WorktreeName: managedTreeName(id),
		Branch:       branch,
	}
	// Validated before any git runs so a malformed request never provisions.
	if _, err := session.NormalizeWorkspaceBinding(projectID, repoRoot, binding); err != nil {
		return fmt.Errorf("invalid workspace choice: %w", err)
	}

	createExisting := false
	if choice.CreateBranch {
		exists, err := workspace.BranchExists(ctx, repoRoot, branch)
		if err != nil {
			return fmt.Errorf("checking branch %s: %w", branch, err)
		}
		createExisting = exists
	}
	owner := f.worktreeOwner()
	if !choice.CreateBranch || createExisting {
		trees, err := owner.List(ctx, repoRoot)
		if err != nil {
			return fmt.Errorf("listing worktrees of %s: %w", repoRoot, err)
		}
		if holder := branchHolder(trees, branch); holder != nil {
			switch holder.Kind {
			case workspace.WorktreeManaged:
				return f.bindAdoptedTree(ctx, owner, repoRoot, id, projectID, branch, holder, info.WorkspaceBinding)
			case workspace.WorktreeMain:
				// The checkout holds the branch: the fresh session already
				// executes exactly there — keep the local binding, report
				// success.
				return nil
			default:
				return fmt.Errorf("%w: branch %s is checked out in external worktree %s — created outside c0wrk; use a local session or remove that tree first", workspace.ErrBranchBusy, branch, holder.Path)
			}
		}
	}

	provOwner, err := f.newWorktreeOwner(projectID)
	if err != nil {
		return err
	}
	var provisioned workspace.WorktreeInfo
	if choice.CreateBranch && !createExisting {
		provisioned, err = provOwner.ProvisionNewBranch(ctx, repoRoot, binding.WorktreeName, branch, choice.StartPoint)
	} else {
		provisioned, err = provOwner.ProvisionBranch(ctx, repoRoot, binding.WorktreeName, branch)
	}
	if err != nil {
		return err
	}
	binding.WorkspacePath = provisioned.Path
	if err := f.store.BindSessionWorkspace(ctx, id, projectID, binding); err != nil {
		f.compensateProvisionedTree(ctx, repoRoot, binding.WorktreeName, "workspace binding failed")
		return err
	}
	// Quiesce the session in memory so the send's own access lazily restores
	// with an orchestrator built for the NEW workspace: the terminal stops
	// first (its PTY cwd lives in the OLD workspace — the frontend hears
	// session:<id>:terminal_exited and re-spawns in the new one), and the
	// staged attachments are snapshotted before the eviction and re-staged
	// after (the pending lists are memory-only).
	if f.stopSessionTerminal(id) {
		f.emitTerminalExited(id)
	}
	// The eviction captures staged attachments under the same critical
	// section that deletes the session, so a staging append racing it is
	// either carried or re-stages itself — no interleaving loses it.
	pendingSnap, evictErr := f.bindEvict(id)
	if evictErr != nil {
		// Roll the persisted binding back, then release the tree — but only
		// on a CONFIRMED rollback: a refused rollback (the fresh window
		// closed) leaves the binding naming this tree, and releasing it
		// would strand the binding on a missing path.
		if rbErr := f.rollbackSessionBinding(ctx, id, projectID, info.WorkspaceBinding, binding.WorktreeName); rbErr != nil {
			f.log().Error("workspace-binding rollback refused; provisioned tree kept under the live binding",
				"session_id", id, "worktree", binding.WorktreeName, "cause", rbErr)
			return fmt.Errorf("session workspace binding persisted but the session could not be reloaded; the rollback was refused (the fresh window closed) and the session remains bound to worktree %q: %w", binding.WorktreeName, evictErr)
		}
		f.compensateProvisionedTree(ctx, repoRoot, binding.WorktreeName, "session eviction failed")
		return fmt.Errorf("session workspace binding persisted but the session could not be reloaded; the previous workspace was restored: %w", evictErr)
	}
	if err := f.app.Manager().RestorePendingAttachments(id, pendingSnap); err != nil {
		// The session restored WITHOUT the staged attachments; rejecting the
		// send (the event drops the stale chips) keeps the retry honest — a
		// silent retry would start the task without the files.
		f.app.Manager().EmitStagedAttachmentsLost(id, pendingSnap)
		return fmt.Errorf("session workspace bound, but its staged attachments could not be carried across the workspace switch; the files were NOT sent — re-attach them: %w", err)
	}
	// Seed the tree's embedding cache after the success point (the task's
	// vector manager builds against the new workspace root).
	f.seedWorktreeEmbeddingCache(projectID, binding.WorktreeName)
	return nil
}

// bindAdoptedTree re-binds a fresh session to an EXISTING managed worktree
// (shared trees, ADR-082) under the adoption race protocol: Recreate, binding
// persist, and eviction inside the adoption↔deletion critical section
// (managedTreeMu), then re-validation — a concurrent last-owner deletion
// either counts this session's committed binding or rolls the just-persisted
// binding back. Adopted trees are never released or compensated.
// See specs/domains/session-worktrees.md.
func (f *FrontendAPI) bindAdoptedTree(ctx context.Context, owner *worktrees.Owner, repoRoot, id, projectID, branch string, holder *workspace.WorktreeInfo, prevBinding *session.WorkspaceBinding) error {
	f.managedTreeMu.Lock()
	defer f.managedTreeMu.Unlock()
	if _, err := owner.Recreate(ctx, repoRoot, holder.Name, branch, holder.Path); err != nil {
		return err
	}
	binding := &session.WorkspaceBinding{
		Kind:          session.WorkspaceManagedWorktree,
		WorkspacePath: holder.Path,
		WorktreeName:  holder.Name,
		Branch:        branch,
	}
	if err := f.store.BindSessionWorkspace(ctx, id, projectID, binding); err != nil {
		return err
	}
	// Quiesce + carry-over, mirroring the provisioned path above.
	if f.stopSessionTerminal(id) {
		f.emitTerminalExited(id)
	}
	pendingSnap, evictErr := f.bindEvict(id)
	if evictErr != nil {
		if rbErr := f.rollbackSessionBinding(ctx, id, projectID, prevBinding, holder.Name); rbErr != nil {
			return fmt.Errorf("session workspace binding persisted but the session could not be reloaded; the rollback was refused (the fresh window closed) and the session remains bound to worktree %q: %w", holder.Name, evictErr)
		}
		return fmt.Errorf("session workspace binding persisted but the session could not be reloaded; the previous workspace was restored: %w", evictErr)
	}
	if err := f.app.Manager().RestorePendingAttachments(id, pendingSnap); err != nil {
		f.app.Manager().EmitStagedAttachmentsLost(id, pendingSnap)
		return fmt.Errorf("session workspace bound, but its staged attachments could not be carried across the workspace switch; the files were NOT sent — re-attach them: %w", err)
	}
	if _, err := owner.Inspect(ctx, repoRoot, holder.Name); err != nil {
		if rbErr := f.rollbackSessionBinding(ctx, id, projectID, prevBinding, holder.Name); rbErr != nil {
			return fmt.Errorf("managed worktree %q was gone at the post-bind re-validation (removed by a concurrent last-owner deletion); the rollback was refused (the fresh window closed) and the session remains bound to it: %w", holder.Name, err)
		}
		return fmt.Errorf("managed worktree %q was gone at the post-bind re-validation (removed by a concurrent last-owner deletion); the previous workspace was restored — retry the send to provision a fresh tree: %w", holder.Name, err)
	}
	// Idempotent: the tree's first session seeded the cache already; a fresh
	// seed warms adopted (previously orphaned) trees too.
	f.seedWorktreeEmbeddingCache(projectID, holder.Name)
	return nil
}

// rollbackSessionBinding restores a session's previous persisted workspace
// binding after a post-write step (eviction refusal, adoption re-validation)
// failed, and REPORTS the outcome: nil — the previous binding is durably
// recorded, the caller may release a provisioned tree; non-nil — the restore
// did NOT happen (typically the store's atomic fresh-window guard refused: a
// parallel send started a task), so the caller must KEEP the tree, which the
// persisted binding still names.
func (f *FrontendAPI) rollbackSessionBinding(ctx context.Context, id, projectID string, prev *session.WorkspaceBinding, treeName string) error {
	if prev == nil {
		// Unreachable from the bind paths (every CODE session gets a
		// non-nil binding), but with no previous binding the rollback
		// outcome is unknowable — refuse rather than let the caller
		// release a tree under the live persisted binding.
		return errors.New("no previous workspace binding recorded; rollback outcome unknown")
	}
	if err := f.store.BindSessionWorkspace(ctx, id, projectID, prev); err != nil {
		f.log().Error("failed to roll back the persisted workspace binding",
			"session_id", id, "worktree", treeName, "error", err)
		return err
	}
	return nil
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

// bindEvict performs the bind paths' in-memory eviction and returns the
// staged-attachment capture: the test seam when installed
// (FrontendAPI.bindEvictHook), the manager's eviction-with-capture otherwise.
func (f *FrontendAPI) bindEvict(id string) (*session.PendingAttachmentSnapshot, error) {
	if f.bindEvictHook != nil {
		return f.bindEvictHook(id)
	}
	return f.app.Manager().EvictSessionCarryingAttachments(id)
}

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

// forkManagedSession forks a managed-worktree session into its OWN tree on a
// derived branch created at the source tree's COMMITTED HEAD — uncommitted
// changes are NOT copied and the source tree is left byte-identical; a store
// failure releases the fresh tree (branch kept).
// See specs/domains/session-worktrees.md (Fork).
func (f *FrontendAPI) forkManagedSession(ctx context.Context, src *session.SessionInfo, cloneReview session.ForkReviewCloner) (*session.SessionInfo, error) {
	f.seedAcquire()
	// The caller passes a never-deadlined context; bound the inspect and the
	// store fork with the provisioning budget so a wedged git fails the
	// synchronous ForkSession RPC instead of hanging it. ProvisionNewBranch
	// derives its own (nested) deadline below.
	ctx, cancel := context.WithTimeout(ctx, managedProvisionTimeout)
	defer cancel()

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
	owner, err = f.newWorktreeOwner(src.ProjectID)
	if err != nil {
		return nil, err
	}
	provisioned, err := owner.ProvisionNewBranch(provisionCtx, repoRoot, treeName, branch, sourceHead)
	if err != nil {
		return nil, err
	}

	commit := f.managedForkCommit
	if commit == nil {
		commit = f.store.ForkSessionWithBinding
	}
	info, err := commit(ctx, src.ID, draft.ID, &session.WorkspaceBinding{
		Kind:          session.WorkspaceManagedWorktree,
		WorkspacePath: provisioned.Path,
		WorktreeName:  treeName,
		Branch:        branch,
	}, cloneReview)
	if err != nil {
		f.compensateProvisionedTree(provisionCtx, repoRoot, treeName, "fork persistence failed")
		return nil, err
	}
	// Seed after the success point, before the RPC returns and the frontend
	// focuses the fork's tree: the fork starts at the source's committed
	// HEAD, so the checkout cache is a near-perfect match for its first pass.
	f.seedWorktreeEmbeddingCache(src.ProjectID, treeName)
	f.emitSessionsChanged()
	return info, nil
}

// resolveProjectRoot resolves a project's registered repository root and
// validates its shape; deletion, restore, and fork derive tree paths from it,
// so an unusable root fails explicitly rather than guessing.
func (f *FrontendAPI) resolveProjectRoot(ctx context.Context, projectID string) (string, error) {
	f.seedAcquire()
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
	// Canonicalized fail-soft for the git primitives — see
	// canonicalWorktreeRoot.
	return canonicalWorktreeRoot(root), nil
}

// prepareManagedTreeDeletion runs the managed part of session deletion BEFORE
// any session state is removed, so a blocked or failed removal leaves the
// session fully retryable; it returns the session's project ID when the
// pre-flight removed the store row itself (the shared-tree co-owner path —
// a concurrent co-owner's count must observe it gone), "" otherwise. No-op
// for CHAT/local sessions, missing sessions, and — deliberately — a store
// read failure (the tolerant legacy path handles cleanup).
// See specs/domains/session-worktrees.md (Release).
func (f *FrontendAPI) prepareManagedTreeDeletion(ctx context.Context, id string, opts SessionDeleteOptions) (cleanupProjectID string, err error) {
	f.seedAcquire()
	if f.store == nil {
		return "", nil
	}
	// The caller passes a never-deadlined context; bound the git work (and
	// the store read) with the provisioning budget so a wedged git fails the
	// RPC explicitly while the removal holds the per-repo worktree lock.
	ctx, cancel := context.WithTimeout(ctx, managedProvisionTimeout)
	defer cancel()
	info, err := f.store.LoadSession(ctx, id)
	if err != nil {
		f.log().Warn("failed to load session for managed-tree deletion check", "session_id", id, "error", err)
		return "", nil
	}
	if info == nil || info.WorkspaceBinding == nil || info.WorkspaceBinding.Kind != session.WorkspaceManagedWorktree {
		return "", nil
	}
	binding := info.WorkspaceBinding

	// 0. Decide the block FIRST, touching nothing: a blocked deletion must
	// leave the session fully intact and retryable, but the task-join and
	// terminal-stop below are irreversible — an unconfirmed first call must
	// not already have killed the in-flight run. The probe applies the same
	// locked/dirty classification the removal primitive applies; the
	// authoritative recheck still gates the removal.
	//
	// A shared tree removes nothing, so no confirmation is asked and the
	// probe is skipped; the count here is advisory (step 3 re-counts under
	// the critical section), and on a count failure the probe still runs — a
	// wedged store can only over-block, never under-block.
	coOwnerShared := false
	if sharers, cerr := f.store.CountManagedWorktreeSessions(ctx, binding.WorktreeName, id); cerr != nil {
		f.log().Warn("failed to count shared-tree co-owners before the deletion probe; probing anyway",
			"session_id", id, "worktree", binding.WorktreeName, "error", cerr)
	} else {
		coOwnerShared = sharers > 0
	}
	if !coOwnerShared {
		repoRoot, err := f.resolveProjectRoot(ctx, info.ProjectID)
		if err != nil {
			return "", fmt.Errorf("cannot resolve project root to release session worktree: %w", err)
		}
		if blockErr := f.probeSessionTreeDeletionBlock(ctx, repoRoot, binding, opts); blockErr != nil {
			return "", blockErr
		}
	}

	// 1. Join the running task first: its final writes must be visible to
	// the dirty recheck below (CancelTask signals and waits).
	if mgr := f.app.Manager(); mgr != nil {
		if status, serr := mgr.GetSessionRuntimeStatus(id); serr == nil && status.Active {
			if cerr := mgr.CancelTask(id); cerr != nil {
				f.log().Warn("failed to cancel running task before session deletion", "session_id", id, "error", cerr)
			}
		}
	}
	f.stopSessionTerminal(id)

	// 3. The [co-owner count → own-row removal → release] protocol runs
	// inside the adoption↔deletion critical section (managedTreeMu, see
	// bindAdoptedTree): an adoption either finishes first and is counted, or
	// commits after the release and rolls itself back; a concurrent co-owner
	// deletion either observes this row removed or removes its own first.
	f.managedTreeMu.Lock()
	released, rowGoneProjectID, repoRoot, releaseErr := f.removeOwnerAndReleaseTreeLocked(ctx, info.ProjectID, binding, id, opts)
	f.managedTreeMu.Unlock()
	if releaseErr != nil {
		return "", releaseErr
	}
	if released {
		// The tree is gone: drop its worktree-scoped vector-index storage too
		// (best-effort — only derived data, re-indexed on demand; in the
		// narrow resurrection window an in-flight adoption's Recreate may
		// re-create a just-released tree before this cleanup runs). The
		// registry release first shuts the root's live manager (closing its
		// open chromem handles so removal works on Windows too).
		f.deleteWorktreeVectorIndex(repoRoot, info.ProjectID, binding.WorktreeName, binding.WorkspacePath)
	}
	return rowGoneProjectID, nil
}

// removeOwnerAndReleaseTreeLocked removes the deleting session's row from the
// shared tree's owner set and releases the tree only when that made it the
// LAST owner. With co-owners remaining, the row is still removed (their counts
// must observe it gone — see prepareManagedTreeDeletion) but the tree and its
// uncommitted work stay and the dirty/locked confirmations are never reached;
// the returned project ID tells the caller the row is already gone. A count
// failure blocks the deletion (retryable). Caller must hold managedTreeMu.
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
	// removal time.
	repoRoot, err = f.resolveProjectRoot(ctx, projectID)
	if err != nil {
		return false, "", "", fmt.Errorf("cannot resolve project root to release session worktree: %w", err)
	}
	if err := f.releaseSessionTree(ctx, repoRoot, binding, opts); err != nil {
		return false, "", "", err
	}
	return true, "", repoRoot, nil
}

// probeSessionTreeDeletionBlock reports the *SessionDeleteBlockedError the
// release attempt would produce, without touching anything: the same
// locked/dirty classification RemoveWorktree applies, evaluated BEFORE the
// destructive pre-flight so a blocked first call destroys neither the
// in-flight task nor the terminal. Nil = not blocked, or the probe could not
// classify — the authoritative recheck inside releaseSessionTree still gates.
func (f *FrontendAPI) probeSessionTreeDeletionBlock(ctx context.Context, repoRoot string, binding *session.WorkspaceBinding, opts SessionDeleteOptions) error {
	entry, err := f.worktreeOwner().Inspect(ctx, repoRoot, binding.WorktreeName)
	if err != nil {
		// Not linked anymore (nothing to release) or the listing failed:
		// neither is a user decision — let the release attempt decide.
		return nil //nolint:nilerr // the authoritative release path re-checks and reports the real block
	}
	if entry.Kind != workspace.WorktreeManaged {
		return nil // non-managed tree: the release leaves it in place
	}
	if entry.Locked && !opts.UnlockLockedTree {
		return &SessionDeleteBlockedError{
			Reason: fmt.Sprintf("the session's worktree %q is locked (%s)", binding.WorktreeName, entry.LockedReason),
			Option: "unlock_locked_tree",
		}
	}
	if !opts.ConfirmUncommittedLoss && !entry.Prunable {
		if dirty, derr := workspace.WorktreeDirty(ctx, entry.Path); derr == nil && dirty {
			return &SessionDeleteBlockedError{
				Reason: fmt.Sprintf("the session's worktree %q (branch %s) contains uncommitted changes; deleting the session removes the tree and those changes cannot be recovered (the branch itself is kept)", binding.WorktreeName, binding.Branch),
				Option: "confirm_uncommitted_loss",
			}
		}
	}
	return nil
}

// releaseSessionTree removes the session's managed tree per the caller's
// explicit decisions. Never-removal boundaries: an unlinked tree is already
// gone; a non-managed tree (main/external) is left untouched — session
// deletion never removes a foreign checkout, and the branch is never deleted.
// See specs/domains/session-worktrees.md (Release).
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
// whether a live terminal was stopped; the bind paths use the report to emit
// session:<id>:terminal_exited — the one explicit stop the frontend must
// hear about, because its xterm instance survives the stop.
func (f *FrontendAPI) stopSessionTerminal(id string) bool {
	f.seedAcquire()
	if f.terminalManager == nil || !f.terminalManager.IsActive(id) {
		return false
	}
	if err := f.terminalManager.Stop(id); err != nil {
		f.log().Warn("failed to stop terminal for session", "session_id", id, "error", err)
	}
	return true
}

// emitTerminalExited announces an explicit terminal stop so the frontend's
// xterm instance lazily resurrects the shell — in the session's (new)
// workspace on its next activation. Nil-guarded for early lifecycle.
func (f *FrontendAPI) emitTerminalExited(id string) {
	if f.emitEvent == nil {
		return
	}
	f.emitEvent(fmt.Sprintf("session:%s:terminal_exited", id), map[string]string{})
}
