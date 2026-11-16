package backend

// Send-time workspace binding tests (the draft-free creation flow): the
// first message of a FRESH session carries a SessionWorkspaceChoice, and
// bindSessionWorkspace provisions (or adopts) the managed worktree, persists
// the binding inside the fresh window, and evicts the in-memory session so
// the send's own session access restores an orchestrator built for the NEW
// workspace. Everything runs against the REAL git binary through the
// production primitives, mirroring the sibling lifecycle tests above.

import (
	"context"
	"errors"
	"io"
	"log/slog"
	"os"
	"path/filepath"
	"strings"
	"sync/atomic"
	"testing"

	"github.com/v0lka/c0wrk/backend/session"
	"github.com/v0lka/c0wrk/backend/worktrees"
	"github.com/v0lka/c0wrk/core"
	"github.com/v0lka/c0wrk/core/workspace"
	"github.com/v0lka/sp4rk/orchestration"
)

// newFreshLocalSession creates a plain local session through the production
// CreateSession RPC — exactly the row the draft-free "New Session" gesture
// produces in a CODE project.
func newFreshLocalSession(t *testing.T, h *wtHarness) *session.SessionInfo {
	t.Helper()
	info, err := h.api.CreateSession()
	if err != nil {
		t.Fatalf("CreateSession: %v", err)
	}
	if b := info.WorkspaceBinding; b == nil || b.Kind != session.WorkspaceLocal {
		t.Fatalf("fresh session binding must be local, got %+v", b)
	}
	return info
}

// mustManagedSession creates a CODE session and binds its fresh window to a
// managed worktree on the given branch — the draft-free equivalent of the
// former CreateManagedSession RPC (session created, tree provisioned or
// adopted, binding persisted, execution workspace retargeted). The returned
// info is reloaded from the store so it carries the managed binding.
func (f *FrontendAPI) mustManagedSession(t *testing.T, branch string, createBranch bool, startPoint string) *session.SessionInfo {
	t.Helper()
	info, err := f.CreateSession()
	if err != nil {
		t.Fatalf("CreateSession: %v", err)
	}
	if err := f.bindSessionWorkspace(f.ctx(), info.ID, &SessionWorkspaceChoice{
		Branch:       branch,
		CreateBranch: createBranch,
		StartPoint:   startPoint,
	}); err != nil {
		t.Fatalf("bindSessionWorkspace(%q, create=%v, start=%q): %v", branch, createBranch, startPoint, err)
	}
	reloaded, err := f.store.LoadSession(f.ctx(), info.ID)
	if err != nil {
		t.Fatalf("reload bound session: %v", err)
	}
	return reloaded
}

func noManagedTrees(t *testing.T, repoRoot, what string) {
	t.Helper()
	trees, err := workspace.ListWorktrees(context.Background(), repoRoot)
	if err != nil {
		t.Fatalf("list worktrees: %v", err)
	}
	for _, tr := range trees {
		if tr.Kind == workspace.WorktreeManaged {
			t.Fatalf("%s provisioned a managed tree %q", what, tr.Path)
		}
	}
}

// countManagedTrees returns the number of managed worktrees of the project.
func countManagedTrees(t *testing.T, repoRoot string) int {
	t.Helper()
	trees, err := workspace.ListWorktrees(context.Background(), repoRoot)
	if err != nil {
		t.Fatalf("list worktrees: %v", err)
	}
	managed := 0
	for _, tr := range trees {
		if tr.Kind == workspace.WorktreeManaged {
			managed++
		}
	}
	return managed
}

// orphanTree provisions a managed tree OUTSIDE any session lifecycle, as an
// app crash or a lost session row would leave it.
func orphanTree(t *testing.T, repoRoot, name, branch string) workspace.WorktreeInfo {
	t.Helper()
	owner := worktrees.NewOwner(nil)
	info, err := owner.ProvisionNewBranch(context.Background(), repoRoot, name, branch, "")
	if err != nil {
		t.Fatalf("provision orphan tree: %v", err)
	}
	return info
}

func TestBindSessionWorkspace_ProvisionsTreeAndRestoresIntoIt(t *testing.T) {
	h := newWTHarness(t, wtFactory())
	fresh := newFreshLocalSession(t, h)

	if err := h.api.bindSessionWorkspace(context.Background(), fresh.ID, &SessionWorkspaceChoice{
		Branch:       "feature/bind",
		CreateBranch: true,
	}); err != nil {
		t.Fatalf("bindSessionWorkspace: %v", err)
	}

	// The binding was persisted inside the fresh window.
	binding := managedBindingOf(t, h.store, fresh.ID)
	if binding == nil || binding.Kind != session.WorkspaceManagedWorktree || binding.Branch != "feature/bind" {
		t.Fatalf("persisted binding mismatch: %+v", binding)
	}
	if binding.WorktreeName != managedTreeName(fresh.ID) {
		t.Fatalf("tree name %q != derived %q", binding.WorktreeName, managedTreeName(fresh.ID))
	}
	// The tree exists on the pinned branch, and the branch was created.
	if entry := treeEntry(t, h.project.WorkspacePath, binding.WorkspacePath); entry == nil || entry.Branch != "feature/bind" {
		t.Fatalf("managed tree missing or wrong identity: %+v", entry)
	}
	if !branchExists(t, h.repoRoot, "feature/bind") {
		t.Fatal("create-branch request did not create the branch")
	}
	// The eviction happened: the send's own session access lazily restores
	// an orchestrator built for the NEW workspace — execution retargets.
	if wp, ok := h.manager.GetSessionWorkspacePath(fresh.ID); !ok || wp != binding.WorkspacePath {
		t.Fatalf("runtime workspace %q (ok=%v) != tree path %q", wp, ok, binding.WorkspacePath)
	}
}

func TestBindSessionWorkspace_ExistingFreeBranchProvisions(t *testing.T) {
	h := newWTHarness(t, wtFactory())
	fresh := newFreshLocalSession(t, h)
	wtGit(t, h.repoRoot, "branch", "feature/topic")

	if err := h.api.bindSessionWorkspace(context.Background(), fresh.ID, &SessionWorkspaceChoice{Branch: "feature/topic"}); err != nil {
		t.Fatalf("bindSessionWorkspace: %v", err)
	}
	binding := managedBindingOf(t, h.store, fresh.ID)
	if binding == nil || binding.Branch != "feature/topic" {
		t.Fatalf("persisted binding mismatch: %+v", binding)
	}
	if entry := treeEntry(t, h.project.WorkspacePath, binding.WorkspacePath); entry == nil || entry.Branch != "feature/topic" {
		t.Fatalf("tree not on feature/topic: %+v", entry)
	}
}

func TestBindSessionWorkspace_AdoptsSharedTree(t *testing.T) {
	h := newWTHarness(t, wtFactory())
	wtGit(t, h.repoRoot, "branch", "feature/shared")
	holder := h.api.mustManagedSession(t, "feature/shared", false, "")
	fresh := newFreshLocalSession(t, h)

	if err := h.api.bindSessionWorkspace(context.Background(), fresh.ID, &SessionWorkspaceChoice{Branch: "feature/shared"}); err != nil {
		t.Fatalf("bindSessionWorkspace: %v", err)
	}
	binding := managedBindingOf(t, h.store, fresh.ID)
	if binding == nil || binding.WorktreeName != holder.WorkspaceBinding.WorktreeName {
		t.Fatalf("fresh session must adopt the holding tree %q, got %+v", holder.WorkspaceBinding.WorktreeName, binding)
	}
	if wp, ok := h.manager.GetSessionWorkspacePath(fresh.ID); !ok || wp != holder.WorkspaceBinding.WorkspacePath {
		t.Fatalf("runtime workspace %q (ok=%v) != shared tree %q", wp, ok, holder.WorkspaceBinding.WorkspacePath)
	}
	// Exactly one managed tree serves both sessions.
	trees, err := workspace.ListWorktrees(context.Background(), h.project.WorkspacePath)
	if err != nil {
		t.Fatalf("list worktrees: %v", err)
	}
	managed := 0
	for _, tr := range trees {
		if tr.Kind == workspace.WorktreeManaged {
			managed++
		}
	}
	if managed != 1 {
		t.Fatalf("managed trees = %d, want 1 (the shared holder)", managed)
	}
}

func TestBindSessionWorkspace_CheckoutHeldBranchIsNoOp(t *testing.T) {
	h := newWTHarness(t, wtFactory())
	fresh := newFreshLocalSession(t, h)

	// main is held by the checkout: the choice lands as a no-op success —
	// the session already executes exactly there, its binding stays local.
	if err := h.api.bindSessionWorkspace(context.Background(), fresh.ID, &SessionWorkspaceChoice{Branch: "main"}); err != nil {
		t.Fatalf("bindSessionWorkspace on checkout-held branch: %v", err)
	}
	binding := managedBindingOf(t, h.store, fresh.ID)
	if binding == nil || binding.Kind != session.WorkspaceLocal {
		t.Fatalf("binding must stay local, got %+v", binding)
	}
	noManagedTrees(t, h.project.WorkspacePath, "checkout-held no-op")
}

func TestBindSessionWorkspace_RefusesExternalTreeHolder(t *testing.T) {
	h := newWTHarness(t, wtFactory())
	fresh := newFreshLocalSession(t, h)
	wtGit(t, h.repoRoot, "branch", "feature/ext")
	external := filepath.Join(t.TempDir(), "external-tree")
	wtGit(t, h.repoRoot, "worktree", "add", external, "feature/ext")

	err := h.api.bindSessionWorkspace(context.Background(), fresh.ID, &SessionWorkspaceChoice{Branch: "feature/ext"})
	if err == nil {
		t.Fatal("expected refusal for an external worktree holding the branch")
	}
	if !errors.Is(err, workspace.ErrBranchBusy) || !strings.Contains(err.Error(), "external worktree") {
		t.Fatalf("refusal must be a typed branch-busy error naming the external tree: %v", err)
	}
	if binding := managedBindingOf(t, h.store, fresh.ID); binding == nil || binding.Kind != session.WorkspaceLocal {
		t.Fatalf("refused bind must leave the local binding, got %+v", binding)
	}
	noManagedTrees(t, h.project.WorkspacePath, "refused bind")
}

func TestBindSessionWorkspace_RefusesArchivedSession(t *testing.T) {
	h := newWTHarness(t, wtFactory())
	fresh := newFreshLocalSession(t, h)
	if err := h.api.ArchiveSession(fresh.ID); err != nil {
		t.Fatalf("ArchiveSession: %v", err)
	}

	err := h.api.bindSessionWorkspace(context.Background(), fresh.ID, &SessionWorkspaceChoice{Branch: "feature/any"})
	if err == nil {
		t.Fatal("expected refusal for an archived session")
	}
	if !errors.Is(err, session.ErrSessionArchived) {
		t.Fatalf("refusal must be a typed archived-session error: %v", err)
	}
	if binding := managedBindingOf(t, h.store, fresh.ID); binding == nil || binding.Kind != session.WorkspaceLocal {
		t.Fatalf("refused bind must leave the local binding, got %+v", binding)
	}
	noManagedTrees(t, h.project.WorkspacePath, "refused archived bind")
}

func TestBindSessionWorkspace_DWIMCreatesMissingBranchFromRemoteStartPoint(t *testing.T) {
	h := newWTHarness(t, wtFactory())
	fresh := newFreshLocalSession(t, h)
	remoteSha := commitRemoteTip(t, h.repoRoot, "feature/topic")

	if err := h.api.bindSessionWorkspace(context.Background(), fresh.ID, &SessionWorkspaceChoice{
		Branch:       "feature/topic",
		CreateBranch: true,
		StartPoint:   "origin/feature/topic",
	}); err != nil {
		t.Fatalf("bindSessionWorkspace (remote-tracking pick): %v", err)
	}
	if !branchExists(t, h.repoRoot, "feature/topic") {
		t.Fatal("the local branch was not created from the remote-tracking start point")
	}
	binding := managedBindingOf(t, h.store, fresh.ID)
	entry := treeEntry(t, h.project.WorkspacePath, binding.WorkspacePath)
	if entry == nil || entry.Branch != "feature/topic" {
		t.Fatalf("managed tree missing or wrong identity: %+v", entry)
	}
	if got := wtGit(t, entry.Path, "rev-parse", "HEAD"); got != remoteSha {
		t.Fatalf("tree HEAD %q != remote-tracking tip %q", got, remoteSha)
	}
}

func TestBindSessionWorkspace_EmptyBranchRefused(t *testing.T) {
	h := newWTHarness(t, wtFactory())
	fresh := newFreshLocalSession(t, h)

	err := h.api.bindSessionWorkspace(context.Background(), fresh.ID, &SessionWorkspaceChoice{})
	if err == nil || !strings.Contains(err.Error(), "a branch is required") {
		t.Fatalf("empty branch without create_branch must be refused, got: %v", err)
	}
	noManagedTrees(t, h.project.WorkspacePath, "refused empty-branch bind")
}

func TestBindSessionWorkspace_FreshWindowEnforcedByStore(t *testing.T) {
	h := newWTHarness(t, wtFactory())
	fresh := newFreshLocalSession(t, h)
	// Close the fresh window behind the bind's back: a persisted message
	// makes the store's atomic guard refuse the write AFTER provisioning —
	// the typed error must surface and the fresh tree must be compensated
	// away (branches are never deleted, trees are).
	msg := session.ChatMessage{SessionID: fresh.ID, Role: "user", Content: "first"}
	if err := h.store.SaveMessage(context.Background(), msg); err != nil {
		t.Fatalf("save message: %v", err)
	}

	err := h.api.bindSessionWorkspace(context.Background(), fresh.ID, &SessionWorkspaceChoice{
		Branch:       "feature/late",
		CreateBranch: true,
	})
	if err == nil {
		t.Fatal("expected the fresh-window guard to refuse the binding")
	}
	if !errors.Is(err, session.ErrSessionNotFresh) {
		t.Fatalf("refusal must be the typed not-fresh error, got: %v", err)
	}
	if binding := managedBindingOf(t, h.store, fresh.ID); binding == nil || binding.Kind != session.WorkspaceLocal {
		t.Fatalf("refused bind must leave the local binding, got %+v", binding)
	}
	noManagedTrees(t, h.project.WorkspacePath, "compensated fresh-window refusal")
}

// The compensation path (a provisioned tree released after the binding write
// failed) is exercised by TestBindSessionWorkspace_FreshWindowEnforcedByStore:
// the store's atomic fresh-window guard refuses the write after provisioning,
// for real, without any injection seam.

func TestRollbackSessionBinding_RestoresPreviousBinding(t *testing.T) {
	h := newWTHarness(t, wtFactory())
	fresh := newFreshLocalSession(t, h)
	prev := managedBindingOf(t, h.store, fresh.ID)

	bound := &session.WorkspaceBinding{
		Kind:         session.WorkspaceManagedWorktree,
		WorktreeName: managedTreeName(fresh.ID),
		Branch:       "feature/rollback",
	}
	if err := h.store.BindSessionWorkspace(context.Background(), fresh.ID, h.project.ID, bound); err != nil {
		t.Fatalf("seed the managed binding: %v", err)
	}
	if err := h.api.rollbackSessionBinding(context.Background(), fresh.ID, h.project.ID, prev, bound.WorktreeName); err != nil {
		t.Fatalf("rollback with the window open must succeed: %v", err)
	}

	restored := managedBindingOf(t, h.store, fresh.ID)
	if restored == nil || restored.Kind != session.WorkspaceLocal {
		t.Fatalf("rollback must restore the local binding, got %+v", restored)
	}
}

// TestRollbackSessionBinding_RefusedWhenWindowClosed pins the observability
// half of the rollback: when the fresh window closed between the bind write
// and the rollback (a parallel send started a task — message rows exist), the
// store's atomic guard refuses the restore. The rollback must REPORT the
// refusal (the caller keeps the provisioned tree instead of releasing it
// under the live persisted binding) and leave the managed binding in place.
func TestRollbackSessionBinding_RefusedWhenWindowClosed(t *testing.T) {
	h := newWTHarness(t, wtFactory())
	fresh := newFreshLocalSession(t, h)
	prev := managedBindingOf(t, h.store, fresh.ID)

	bound := &session.WorkspaceBinding{
		Kind:         session.WorkspaceManagedWorktree,
		WorktreeName: managedTreeName(fresh.ID),
		Branch:       "feature/rollback",
	}
	if err := h.store.BindSessionWorkspace(context.Background(), fresh.ID, h.project.ID, bound); err != nil {
		t.Fatalf("seed the managed binding: %v", err)
	}
	// Close the fresh window behind the rollback's back.
	msg := session.ChatMessage{SessionID: fresh.ID, Role: "user", Content: "first"}
	if err := h.store.SaveMessage(context.Background(), msg); err != nil {
		t.Fatalf("save message: %v", err)
	}

	err := h.api.rollbackSessionBinding(context.Background(), fresh.ID, h.project.ID, prev, bound.WorktreeName)
	if err == nil {
		t.Fatal("rollback must report the store's refusal, not swallow it")
	}
	if !errors.Is(err, session.ErrSessionNotFresh) {
		t.Fatalf("refusal must surface the typed not-fresh error, got: %v", err)
	}
	// The refused rollback leaves the persisted binding naming the tree — the
	// caller's cue to keep it (releasing it would strand the binding on a
	// missing path and force a surprise re-provision on the next restore).
	kept := managedBindingOf(t, h.store, fresh.ID)
	if kept == nil || kept.Kind != session.WorkspaceManagedWorktree || kept.WorktreeName != bound.WorktreeName {
		t.Fatalf("refused rollback must keep the persisted binding, got %+v", kept)
	}
}

// TestBindSessionWorkspace_SameChoiceRepeatIsNoOp pins the fresh-window
// re-selection rule: a window whose bind already persisted (a previous send
// bound, then the send itself failed — no rows, window open) accepts the SAME
// choice as a no-op success. The session already executes exactly where the
// repeated choice wants it.
func TestBindSessionWorkspace_SameChoiceRepeatIsNoOp(t *testing.T) {
	h := newWTHarness(t, wtFactory())
	fresh := newFreshLocalSession(t, h)
	choice := &SessionWorkspaceChoice{Branch: "feature/repeat", CreateBranch: true}

	if err := h.api.bindSessionWorkspace(context.Background(), fresh.ID, choice); err != nil {
		t.Fatalf("first bind: %v", err)
	}
	first := managedBindingOf(t, h.store, fresh.ID)
	if first == nil || first.Kind != session.WorkspaceManagedWorktree {
		t.Fatalf("first bind must persist the managed binding, got %+v", first)
	}

	if err := h.api.bindSessionWorkspace(context.Background(), fresh.ID, choice); err != nil {
		t.Fatalf("same-choice re-bind must be a no-op success, got: %v", err)
	}
	second := managedBindingOf(t, h.store, fresh.ID)
	if second == nil || second.WorkspacePath != first.WorkspacePath || second.WorktreeName != first.WorktreeName {
		t.Fatalf("repeated bind must not touch the binding: %+v vs %+v", first, second)
	}
	// Still exactly one managed tree (the repeat must not re-provision).
	if got := countManagedTrees(t, h.project.WorkspacePath); got != 1 {
		t.Fatalf("managed trees = %d after the repeat, want 1", got)
	}
}

// TestBindSessionWorkspace_DifferentChoiceRefusedAfterBind pins the other
// half: a DIFFERENT choice on an already-bound fresh window is refused with
// the typed already-chosen error — dispatching by branch holder alone would
// silently re-provision (orphaning the first tree) or no-op (leaving
// execution in the old tree).
func TestBindSessionWorkspace_DifferentChoiceRefusedAfterBind(t *testing.T) {
	h := newWTHarness(t, wtFactory())
	fresh := newFreshLocalSession(t, h)

	if err := h.api.bindSessionWorkspace(context.Background(), fresh.ID, &SessionWorkspaceChoice{Branch: "feature/first", CreateBranch: true}); err != nil {
		t.Fatalf("first bind: %v", err)
	}
	first := managedBindingOf(t, h.store, fresh.ID)

	err := h.api.bindSessionWorkspace(context.Background(), fresh.ID, &SessionWorkspaceChoice{Branch: "feature/second", CreateBranch: true})
	if err == nil {
		t.Fatal("a different choice on the already-bound window must be refused")
	}
	if !errors.Is(err, ErrWorkspaceAlreadyChosen) {
		t.Fatalf("refusal must be the typed already-chosen error, got: %v", err)
	}
	if !strings.Contains(err.Error(), first.WorktreeName) {
		t.Fatalf("refusal must name the tree the session is bound to, got: %v", err)
	}
	// The state is untouched: the first binding stands, no second tree.
	kept := managedBindingOf(t, h.store, fresh.ID)
	if kept == nil || kept.WorkspacePath != first.WorkspacePath {
		t.Fatalf("refused re-bind must keep the first binding, got %+v", kept)
	}
	if got := countManagedTrees(t, h.project.WorkspacePath); got != 1 {
		t.Fatalf("managed trees = %d after the refused re-bind, want 1", got)
	}
	// The window is still open for the SAME choice (the recovery path).
	if err := h.api.bindSessionWorkspace(context.Background(), fresh.ID, &SessionWorkspaceChoice{Branch: "feature/first", CreateBranch: true}); err != nil {
		t.Fatalf("same-choice resend after the refusal must no-op: %v", err)
	}
}

// TestBindSessionWorkspace_RefusesConcurrentBind pins the backend backstop of
// the consumption-window re-entrancy guard: a second bind for a session whose
// bind is already in flight is refused (the frontend refuses such sends
// itself; this is the last line against a hand-crafted concurrent send).
func TestBindSessionWorkspace_RefusesConcurrentBind(t *testing.T) {
	h := newWTHarness(t, wtFactory())
	fresh := newFreshLocalSession(t, h)

	h.api.bindInFlight.Store(fresh.ID, struct{}{})
	defer h.api.bindInFlight.Delete(fresh.ID)

	err := h.api.bindSessionWorkspace(context.Background(), fresh.ID, &SessionWorkspaceChoice{Branch: "feature/race", CreateBranch: true})
	if !errors.Is(err, ErrBindInProgress) {
		t.Fatalf("concurrent bind must be refused with the typed in-progress error, got: %v", err)
	}
	noManagedTrees(t, h.project.WorkspacePath, "refused concurrent bind")
}

// TestBindSessionWorkspace_StopsLiveTerminalBeforeEvict pins the terminal
// quiesce: the bind stops the session's live terminal before the eviction
// (its PTY cwd lives in the OLD workspace and cannot be moved) — the same
// protocol the promotion flow and the deletion path follow.
func TestBindSessionWorkspace_StopsLiveTerminalBeforeEvict(t *testing.T) {
	h := newWTHarness(t, wtFactory())
	fresh := newFreshLocalSession(t, h)
	if err := h.terminal.Start(fresh.ID, "placeholder"); err != nil {
		t.Fatalf("start terminal: %v", err)
	}

	if err := h.api.bindSessionWorkspace(context.Background(), fresh.ID, &SessionWorkspaceChoice{Branch: "feature/term", CreateBranch: true}); err != nil {
		t.Fatalf("bindSessionWorkspace: %v", err)
	}
	if got := h.terminal.stopCount(); got != 1 {
		t.Fatalf("bind must stop the session's live terminal once, stopped %d times", got)
	}
}

func TestBindSessionWorkspace_DWIMReusesExistingFreeBranch(t *testing.T) {
	h := newWTHarness(t, wtFactory())
	fresh := newFreshLocalSession(t, h)
	// A local feature/topic ALREADY exists (at the initial commit), free of
	// any worktree; the remote-tracking ref points at a NEWER commit. Git
	// DWIM semantics: the create request must check the EXISTING branch out —
	// never re-create (or reset) it at the remote start point.
	wtGit(t, h.repoRoot, "branch", "feature/topic")
	localSha := wtGit(t, h.repoRoot, "rev-parse", "refs/heads/feature/topic")
	_ = commitRemoteTip(t, h.repoRoot, "feature/topic")

	if err := h.api.bindSessionWorkspace(context.Background(), fresh.ID, &SessionWorkspaceChoice{
		Branch:       "feature/topic",
		CreateBranch: true,
		StartPoint:   "origin/feature/topic",
	}); err != nil {
		t.Fatalf("bind over an existing free branch: %v", err)
	}
	binding := managedBindingOf(t, h.store, fresh.ID)
	entry := treeEntry(t, h.project.WorkspacePath, binding.WorkspacePath)
	if entry == nil || entry.Branch != "feature/topic" {
		t.Fatalf("tree not on the existing feature/topic: %+v", entry)
	}
	// The tree sits on the LOCAL branch's tip — reuse, not a re-create from
	// the remote ref (that would silently move the tip to the newer commit).
	if got := wtGit(t, entry.Path, "rev-parse", "HEAD"); got != localSha {
		t.Fatalf("tree HEAD %q != pre-existing local tip %q (branch was re-created, not reused)", got, localSha)
	}
}

func TestBindSessionWorkspace_DWIMRefusesBusyExternalHolder(t *testing.T) {
	h := newWTHarness(t, wtFactory())
	fresh := newFreshLocalSession(t, h)
	wtGit(t, h.repoRoot, "branch", "feature/ext")
	external := filepath.Join(t.TempDir(), "external-tree")
	wtGit(t, h.repoRoot, "worktree", "add", external, "feature/ext")
	_ = commitRemoteTip(t, h.repoRoot, "feature/ext")

	// The create-variant over an EXISTING branch occupied by an external
	// tree refuses with the same typed branch-busy error as the
	// existing-branch pick — it must never force or share the holder.
	err := h.api.bindSessionWorkspace(context.Background(), fresh.ID, &SessionWorkspaceChoice{
		Branch:       "feature/ext",
		CreateBranch: true,
		StartPoint:   "origin/feature/ext",
	})
	if !errors.Is(err, workspace.ErrBranchBusy) || !strings.Contains(err.Error(), "external worktree") {
		t.Fatalf("refusal must be a typed branch-busy error naming the external tree: %v", err)
	}
	noManagedTrees(t, h.project.WorkspacePath, "refused DWIM bind")
}

func TestBindSessionWorkspace_DWIMSharesManagedHolder(t *testing.T) {
	h := newWTHarness(t, wtFactory())
	wtGit(t, h.repoRoot, "branch", "feature/topic")
	_ = commitRemoteTip(t, h.repoRoot, "feature/topic")

	holder := h.api.mustManagedSession(t, "feature/topic", false, "")
	fresh := newFreshLocalSession(t, h)

	// The branch is already held by a managed tree: the create-variant over
	// the existing branch must SHARE that tree (ADR-082), exactly like
	// picking the existing branch directly.
	if err := h.api.bindSessionWorkspace(context.Background(), fresh.ID, &SessionWorkspaceChoice{
		Branch:       "feature/topic",
		CreateBranch: true,
		StartPoint:   "origin/feature/topic",
	}); err != nil {
		t.Fatalf("DWIM bind over the held branch: %v", err)
	}
	binding := managedBindingOf(t, h.store, fresh.ID)
	if binding == nil || binding.WorktreeName != holder.WorkspaceBinding.WorktreeName || binding.WorkspacePath != holder.WorkspaceBinding.WorkspacePath {
		t.Fatalf("create variant must share the holding tree: %+v vs %+v", binding, holder.WorkspaceBinding)
	}
	if got := countManagedTrees(t, h.project.WorkspacePath); got != 1 {
		t.Fatalf("managed trees = %d, want 1 (the shared holder)", got)
	}
}

func TestBindSessionWorkspace_AdoptsOrphanedTree(t *testing.T) {
	h := newWTHarness(t, wtFactory())
	orphan := orphanTree(t, h.project.WorkspacePath, "s-orphan000", "feature/orphan")
	fresh := newFreshLocalSession(t, h)

	if err := h.api.bindSessionWorkspace(context.Background(), fresh.ID, &SessionWorkspaceChoice{Branch: "feature/orphan"}); err != nil {
		t.Fatalf("bind on orphan-held branch: %v", err)
	}
	binding := managedBindingOf(t, h.store, fresh.ID)
	if binding == nil || binding.WorktreeName != "s-orphan000" {
		t.Fatalf("session must adopt the orphan's tree name, got %+v", binding)
	}
	if binding.WorkspacePath != orphan.Path {
		t.Fatalf("adopted path %q != orphan path %q", binding.WorkspacePath, orphan.Path)
	}
	if got := countManagedTrees(t, h.project.WorkspacePath); got != 1 {
		t.Fatalf("managed trees = %d, want 1 (the adopted orphan)", got)
	}
}

func TestBindSessionWorkspace_AdoptsPrunableOrphan(t *testing.T) {
	h := newWTHarness(t, wtFactory())
	orphan := orphanTree(t, h.project.WorkspacePath, "s-prunable0", "feature/prunable")
	if err := os.RemoveAll(orphan.Path); err != nil {
		t.Fatal(err)
	}
	fresh := newFreshLocalSession(t, h)

	if err := h.api.bindSessionWorkspace(context.Background(), fresh.ID, &SessionWorkspaceChoice{Branch: "feature/prunable"}); err != nil {
		t.Fatalf("bind on prunable-orphan branch: %v", err)
	}
	binding := managedBindingOf(t, h.store, fresh.ID)
	if binding == nil || binding.WorktreeName != "s-prunable0" {
		t.Fatalf("session must adopt the orphan's tree name, got %+v", binding)
	}
	entry := treeEntry(t, h.project.WorkspacePath, orphan.Path)
	if entry == nil || entry.Prunable {
		t.Fatalf("stale metadata must be pruned and the tree recreated, got %+v", entry)
	}
}

// TestSendMessage_BindRefusalTouchesNoState pins the send-path ORDER contract
// (ADR-091): a workspace-carrying send whose bind refuses is rejected BEFORE
// any state is touched — no activity bump, no persisted message row.
func TestSendMessage_BindRefusalTouchesNoState(t *testing.T) {
	h := newWTHarness(t, wtFactory())
	fresh := newFreshLocalSession(t, h)
	before, err := h.store.LoadSession(context.Background(), fresh.ID)
	if err != nil || before == nil {
		t.Fatalf("load session: %v", err)
	}
	wtGit(t, h.repoRoot, "branch", "feature/ext")
	external := filepath.Join(t.TempDir(), "external-tree")
	wtGit(t, h.repoRoot, "worktree", "add", external, "feature/ext")

	err = h.api.SendMessage(fresh.ID, "hello", nil, nil, nil, "", "", false, "", false, false, "", &SessionWorkspaceChoice{Branch: "feature/ext"})
	if !errors.Is(err, workspace.ErrBranchBusy) {
		t.Fatalf("send must be refused with the typed branch-busy error, got: %v", err)
	}
	rows, err := h.store.LoadMessages(context.Background(), fresh.ID)
	if err != nil {
		t.Fatalf("load messages: %v", err)
	}
	if len(rows) != 0 {
		t.Fatalf("refused send must not persist any message rows, got %d", len(rows))
	}
	after, err := h.store.LoadSession(context.Background(), fresh.ID)
	if err != nil || after == nil {
		t.Fatalf("reload session: %v", err)
	}
	if after.LastActiveAt != before.LastActiveAt {
		t.Fatalf("refused send must not bump last_active_at: %q -> %q", before.LastActiveAt, after.LastActiveAt)
	}
	if b := after.WorkspaceBinding; b == nil || b.Kind != session.WorkspaceLocal {
		t.Fatalf("refused send must leave the local binding, got %+v", b)
	}
}

// TestSendMessage_BindOkDispatchFailedKeepsBinding pins the send path's
// post-bind failure contract: when the bind succeeded but the dispatch itself
// fails (here the post-eviction restore cannot rebuild the orchestrator), the
// persisted managed binding and the provisioned tree SURVIVE the failed send
// (no compensation runs — the binding names the live tree), no message row is
// written, and the fresh window stays open — the same-choice re-bind no-ops
// and the retried send executes inside the bound tree.
func TestSendMessage_BindOkDispatchFailedKeepsBinding(t *testing.T) {
	base := wtFactory()
	var breakRestore atomic.Bool
	factory := session.OrchestratorFactory(func(emitter core.Emitter, log *slog.Logger, ws string, _ string, bb core.BlackboardFactory, dump io.Writer, tracker *orchestration.StepDumpTracker) (*core.Orchestrator, error) {
		if breakRestore.Load() {
			return nil, errors.New("orchestrator restore broken for the test")
		}
		return base(emitter, log, ws, "", bb, dump, tracker)
	})
	h := newWTHarness(t, factory)
	fresh := newFreshLocalSession(t, h)

	breakRestore.Store(true)
	err := h.api.SendMessage(fresh.ID, "hello", nil, nil, nil, "", "", false, "", false, false, "", &SessionWorkspaceChoice{Branch: "feature/late", CreateBranch: true})
	if err == nil {
		t.Fatal("the send must fail when the post-bind restore cannot rebuild the orchestrator")
	}
	// The bind's effects survive the failed dispatch: the managed binding
	// stands and the provisioned tree is kept (not compensated).
	b := managedBindingOf(t, h.store, fresh.ID)
	if b == nil || b.Kind != session.WorkspaceManagedWorktree || b.Branch != "feature/late" {
		t.Fatalf("binding must stay managed after a failed dispatch, got %+v", b)
	}
	if got := countManagedTrees(t, h.project.WorkspacePath); got != 1 {
		t.Fatalf("managed trees = %d after the failed dispatch, want 1 (kept under the live binding)", got)
	}
	rows, err := h.store.LoadMessages(context.Background(), fresh.ID)
	if err != nil {
		t.Fatalf("load messages: %v", err)
	}
	if len(rows) != 0 {
		t.Fatalf("failed dispatch must not persist message rows, got %d", len(rows))
	}
	// The window is still open (no transcript, no task rows): the same choice
	// re-binds as a no-op, and the session's lazy restore rebuilds the
	// orchestrator INSIDE the bound tree — the retried work executes where the
	// binding points, not in the project checkout.
	breakRestore.Store(false)
	if err := h.api.bindSessionWorkspace(context.Background(), fresh.ID, &SessionWorkspaceChoice{Branch: "feature/late"}); err != nil {
		t.Fatalf("fresh window must stay open after the failed dispatch: %v", err)
	}
	if ws, ok := h.api.app.Manager().GetSessionWorkspacePath(fresh.ID); !ok || ws != b.WorkspacePath {
		t.Fatalf("restored session must execute in the bound tree %q, got %q (ok=%v)", b.WorkspacePath, ws, ok)
	}
}

// TestBindSessionWorkspace_AdoptionLosingRaceRollsBack pins the
// adoption↔deletion critical section (managedTreeMu): when the last owner's
// deletion releases the shared tree between the adoption's binding commit
// and its post-bind re-validation, the re-validation must notice the missing
// tree and roll the just-persisted binding back — no session may remain
// bound to a removed tree, and a retry provisions a fresh tree for the
// surviving branch. The bindEvictHook seam replays the excluded
// interleaving: the tree is removed at the eviction point, inside the
// adoption's critical section and right before the Inspect.
func TestBindSessionWorkspace_AdoptionLosingRaceRollsBack(t *testing.T) {
	h := newWTHarness(t, wtFactory())
	ctx := context.Background()

	holder := h.api.mustManagedSession(t, "feature/race", true, "")
	treeName := holder.WorkspaceBinding.WorktreeName
	repoRoot := h.project.WorkspacePath
	fresh := newFreshLocalSession(t, h)

	h.api.bindEvictHook = func(string) (*session.PendingAttachmentSnapshot, error) {
		// The losing deletion's release half, replayed at the excluded point.
		if err := h.api.worktreeOwner().Release(ctx, repoRoot, treeName, workspace.RemoveWorktreeOptions{}); err != nil {
			t.Errorf("release shared tree: %v", err)
		}
		return nil, nil
	}

	err := h.api.bindSessionWorkspace(ctx, fresh.ID, &SessionWorkspaceChoice{Branch: "feature/race"})
	// The seam must not leak into the retry bind below.
	h.api.bindEvictHook = nil
	if err == nil {
		t.Fatal("adoption must fail when its tree was removed concurrently")
	}
	if !strings.Contains(err.Error(), "was gone at the post-bind re-validation") {
		t.Fatalf("adoption error must name the concurrent removal, got: %v", err)
	}

	// The rollback restored the fresh session's previous (local) binding —
	// no session may remain bound to the removed tree.
	if binding := managedBindingOf(t, h.store, fresh.ID); binding == nil || binding.Kind != session.WorkspaceLocal {
		t.Fatalf("rolled-back binding must be local, got %+v", binding)
	}
	// The adopted tree is never compensated or resurrected by the rollback:
	// it stays removed (0 managed trees), while the branch survives.
	if got := countManagedTrees(t, repoRoot); got != 0 {
		t.Fatalf("managed trees = %d after the losing race, want 0", got)
	}
	if !branchExists(t, repoRoot, "feature/race") {
		t.Fatal("branch must survive the rollback (branches are never deleted)")
	}
	// The retry provisions a FRESH tree for the branch — it never re-binds
	// the removed one.
	retry := h.api.mustManagedSession(t, "feature/race", false, "")
	if retry.WorkspaceBinding.WorktreeName == treeName {
		t.Fatal("retry must provision a fresh tree, not re-bind the removed one")
	}
	if treeEntry(t, repoRoot, retry.WorkspaceBinding.WorkspacePath) == nil {
		t.Fatalf("retry's tree %q must exist", retry.WorkspaceBinding.WorkspacePath)
	}
}

// TestBindSessionWorkspace_AdoptionFailureKeepsSharedTree pins the
// never-compensated invariant of the adoption path: a failure AFTER the
// binding persisted (the eviction refusal here) rolls the fresh session's
// binding back but must leave the pre-existing shared tree in place —
// adopted trees are never released.
func TestBindSessionWorkspace_AdoptionFailureKeepsSharedTree(t *testing.T) {
	h := newWTHarness(t, wtFactory())

	holder := h.api.mustManagedSession(t, "feature/keep", true, "")
	sharedTree := holder.WorkspaceBinding.WorkspacePath
	fresh := newFreshLocalSession(t, h)

	h.api.bindEvictHook = func(string) (*session.PendingAttachmentSnapshot, error) {
		return nil, errors.New("eviction refused")
	}
	defer func() { h.api.bindEvictHook = nil }()

	err := h.api.bindSessionWorkspace(context.Background(), fresh.ID, &SessionWorkspaceChoice{Branch: "feature/keep"})
	if err == nil {
		t.Fatal("expected the eviction refusal to surface")
	}
	if !strings.Contains(err.Error(), "previous workspace was restored") {
		t.Fatalf("refusal error must report the confirmed rollback, got: %v", err)
	}
	if binding := managedBindingOf(t, h.store, fresh.ID); binding == nil || binding.Kind != session.WorkspaceLocal {
		t.Fatalf("rolled-back binding must be local, got %+v", binding)
	}
	// The adopted tree is NOT compensated away: it pre-existed and stays.
	entry := treeEntry(t, h.project.WorkspacePath, sharedTree)
	if entry == nil || entry.Kind != workspace.WorktreeManaged || entry.Branch != "feature/keep" {
		t.Fatalf("adopted tree must survive a failed adoption, got %+v", entry)
	}
	// The holder's binding is untouched.
	if binding := managedBindingOf(t, h.store, holder.ID); binding == nil || binding.WorkspacePath != sharedTree {
		t.Fatalf("holder binding must be untouched, got %+v", binding)
	}
}

// TestBindSessionWorkspace_RollbackWhenEvictionFails pins the end-to-end
// compensation of the provisioned path: a post-persist eviction refusal
// rolls the binding back to the previous (local) one, releases the freshly
// provisioned tree, and keeps the branch — the store, the repo, and the
// error all agree on the pre-bind state.
func TestBindSessionWorkspace_RollbackWhenEvictionFails(t *testing.T) {
	h := newWTHarness(t, wtFactory())
	fresh := newFreshLocalSession(t, h)

	h.api.bindEvictHook = func(string) (*session.PendingAttachmentSnapshot, error) {
		return nil, errors.New("eviction refused")
	}
	defer func() { h.api.bindEvictHook = nil }()

	err := h.api.bindSessionWorkspace(context.Background(), fresh.ID, &SessionWorkspaceChoice{
		Branch:       "feature/rollback",
		CreateBranch: true,
	})
	if err == nil {
		t.Fatal("expected the eviction refusal to surface")
	}
	if !strings.Contains(err.Error(), "previous workspace was restored") {
		t.Fatalf("refusal error must report the confirmed rollback, got: %v", err)
	}
	if binding := managedBindingOf(t, h.store, fresh.ID); binding == nil || binding.Kind != session.WorkspaceLocal {
		t.Fatalf("rolled-back binding must be local, got %+v", binding)
	}
	noManagedTrees(t, h.project.WorkspacePath, "compensated post-eviction-failure bind")
	if !branchExists(t, h.repoRoot, "feature/rollback") {
		t.Fatal("the fresh branch must be kept (no operation in this path deletes a branch)")
	}
}
