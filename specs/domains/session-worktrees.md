# Session Worktrees

## Purpose

Defines how a CODE session is bound to an execution workspace — the project
checkout (`local`) or a managed git worktree (`managed_worktree`, shared by
several sessions since ADR-082) — and
how that binding stays separate from project identity and the Git panel's
focus. Covers persistence, validation, migration, restore, and fork identity,
plus the worktree provisioning primitives (`core/workspace/worktrees.go`),
the backend ownership coordinator (`backend/worktrees`), the
session-lifecycle wiring that drives them from the session RPC flows
(`backend/frontend_api_worktrees.go`: creation, restore, fork, deletion,
archive), and the Git-panel focus model that consumes the separation
(`backend/frontend_api_git_focus.go`: focus-target resolution behind every
git RPC, the worktree listing and deletion RPCs, and the pinned-branch refusal) — including `DeleteWorktree` (removes any linked tree) and `CreateBranchInWorktree` (a branch born directly in a fresh managed tree, focus untouched).

## Key Files

- `core/workspace/worktrees.go` — the safe git-worktree primitive service: `ListWorktrees` (strict porcelain parsing, main/managed/external classification), `AddWorktree`/`RecreateWorktree`/`RemoveWorktree`/`PruneWorktrees` with per-repo serialization, branch-occupancy validation, structural path validation and the explicit failure taxonomy; installs the `/.worktrees/` exclusion into the common dir's `info/exclude`
- `backend/worktrees/owner.go` — `Owner`: the backend ownership coordinator (Provision/Recreate/Release/List/Inspect); every path constructed/validated through centralized config helpers; existing identities resolve independently of the creation root, managed-only mutation, release never deletes branches
- `backend/frontend_api_worktrees.go` — the Git lifecycle owner driving the coordinator from the session flows: `bindSessionWorkspace` (ADR-091's send-time binding — freshness/re-bind guards → provision-or-adopt dispatch → atomic fresh-window persist → terminal stop → eviction, compensated on runtime/DB failure), the `WorkspaceEnsurer` installed on the session manager (restore validation + recreation warning), `forkManagedSession` (new tree at source committed HEAD), and the deletion protocol (`DeleteSessionWithOptions`, `SessionDeleteBlockedError`)
- `core/pathsegments.go` — `WorktreesRelativePath` (the shared `.worktrees` segment constant)
- `backend/session/workspace_binding.go` — `WorkspaceKind`, `WorkspaceBinding`, `WorkspaceEnsurer`, `NormalizeWorkspaceBinding`, `SessionDraft`/`NewSessionDraft`, context DTOs (`ProjectContext`, `GitPanelTarget`, `SessionContexts`)
- `backend/session/workspace_context.go` — `ResolveSessionContexts` (execution workspace vs Git-panel target)
- `backend/session/persistence_binding.go` — `sessions.workspace_binding` migration, the shared-tree index migration (UNIQUE → plain `idx_session_managed_workspace`) and `CountManagedWorktreeSessions` (the release refcount), binding encode/decode, shared binding columns for list/load queries
- `backend/session/persistence.go` — binding-aware `SaveSession`/`LoadSession`/`ListSessions*`; immutable-identity upsert
- `backend/session/persistence_fork.go` — `ForkSessionWithBinding` (managed fork requires a new tree + derived branch)
- `backend/session/manager.go` — `CreateSessionFromDraft` (commit a prepared workspace), `SetWorkspaceEnsurer` + the restore-time ensure inside `getOrRestoreSession`'s single-flight, `HasSession` (non-restoring existence), binding-aware lazy restore and `WorkspacePathFor`
- `core/worktree_paths.go` — config-independent legacy/marked-container ownership recognition, project-scoped (marker-independent) durable-path recognition, and safe marker creation, shared by backend path helpers and primitive classification
- `backend/config/paths.go` — `NewManagedWorktreesDir`, `WorktreePathInContainer`, `ManagedWorktreeNameFromPath`/`ManagedWorktreeNameFromPathProject`, `ManagedWorktreeLegacyPath`, `ManagedWorktreeRootForPath`, legacy `ManagedWorktreesDir`/`ManagedWorktreePath`, and project-deletion preflight
- `backend/frontend_api_git_focus.go` — the Git-panel focus model: `SetGitPanelFocus`/`GetGitPanelFocus`/`ListProjectWorktrees` RPCs, `resolveGitFocusRoot` (the focus resolution behind `resolveGitRepoRoot`), owning-session metadata, the pinned-branch guard (`ErrPinnedWorktreeBranch`, `refusePinnedBranchSwitch`), the fail-soft vector-registry re-point every accepted focus move carries (`focusVectorRoots` → `ApplyFocus`), and `DeleteWorktree` (removes any linked tree of the active project with the session-release cleanup contract)
- `backend/frontend_api_git.go` — `CreateBranchInWorktree`, the Git-panel RPC that births a branch and its managed tree atomically from a base ref (through `worktrees.Owner.ProvisionNewBranch`), never switching or consulting the focus
- `backend/frontend_api_vector.go` — vector-index RPC surface (`SearchVectorStore`/`GetVectorIndexStatus`/`ReindexVectorIndex`, all routed to the Git-panel FOCUS root), focus-target resolution (`resolveVectorIndexTarget` — the project switch resolves the saved session's tree), worktree-scoped storage (`config.WorktreeVectorIndexPath`), release cleanup (`deleteWorktreeVectorIndex`: registry release → storage → per-tree cache) and provisioning seeding (`seedWorktreeEmbeddingCache`: copies the checkout's embedding cache into a fresh tree's cache root)
- `backend/vector_roots.go` — the per-root registry (ADR-081): one `vectorindex.Manager` per workspace root (single-flight creation, LRU-bounded live set with the focus pinned), agent-side routing by the executor context's workspace root, `ApplyFocus`/`LeaveFocus`, `Release`/`ReleaseProject`/`ShutdownAll`, and `Application.buildVectorRouter` (the shared search closures)
- `core/vectorindex/git.go` — `resolveGitDir` follows a linked worktree's `.git` pointer file to the private git directory that owns HEAD, so the vector index's branch monitoring works inside managed trees; `backend/config/paths.go` `ManagedWorktreeNameFromPath`/`WorktreeVectorIndexPath` derive the per-tree index storage
- `frontend/src/lib/gitFocus.ts` + `frontend/src/hooks/useGitFocusSync.ts` — the frontend side: serialized/supersede-guarded focus applies and the follow-the-session effect (project/session switches and the active session's send-time-bind transition move the focus to the active session's execution workspace); the hook is mounted once at the App root (beside `useVectorIndexStatus`) — the Git panel lives only while the git tab is active, and the follow must also run on chat-area session switches with any other tab open
- `frontend/src/stores/sessionWorkspaceStore.ts` — the chat-side pending-choice state (ADR-091): per-fresh-session recorded workspace choices plus the single-slot `consumingFor` consumption window; transient by design
- `frontend/src/components/chat/SessionWorkspaceSelector.tsx` + `frontend/src/components/GitPanel/BranchPicker.tsx` + `frontend/src/components/layout/SessionSelector.tsx` — the choice's selection surfaces: the chat toolbar selector (`local` / `branch…` while the active session is FRESH, read-only pinned display for existing sessions), the session selector's grouped-view "+" per managed branch row (`handleNewSessionOnBranch` — presets a `createBranch=false` choice, so the first send ADOPTS the branch's existing tree and the new session executes in the same worktree), and the BranchPicker's intent-separated pick mode (`branchPickerMode: 'draft'`) that records a picked/created branch without any checkout (remote-tracking rows are pick targets too: picking `origin/x` records a create-branch request — local `x` from `origin/x`); the picker's always-visible Worktrees section (`WorktreesSection` — focus switching plus linked-tree deletion through the mandatory `WorktreeDeleteConfirmDialog`, shown in pick mode too) and `NewBranchSection`'s Worktree checkbox (routes creation through `CreateBranchInWorktree`, never moving the focus) round out the panel-side management; in manage mode both surfaces render the ACTIVE managed session's pin (`frontend/src/hooks/useSessionWorktreePin.ts`) — a lock instead of the check on the pinned branch and the session's own tree row, no delete affordance on either, and a disabled Rename on the pinned branch (see the focus model's pin-rendering bullet)
- `frontend/src/components/chat/WorktreePreparingIndicator.tsx` — the send-time bind's icon-only, focusable preparation status with a hover/focus tooltip, rendered beside the chosen branch label while the consumption window is open
- `frontend/src/components/layout/SessionDeleteBlockedDialog.tsx` — the session-deletion refusal dialog (the `*SessionDeleteBlockedError` consumer): count-aware copy naming the parked session(s) or branch-named group, the verbatim refusal reason, both worktree consents as explicit checkboxes pre-ticked to the option the refusal named; confirm retries the parked set through `DeleteSessionWithOptions`, cancel deletes nothing (see [Release](#release-deletion))
- `frontend/src/components/layout/SessionDeleteBlockedDialog.test.tsx` — the dialog's render/consent/reset/retry contract (null renders nothing, verbatim reason, pre-tick permutations, confirm payloads, inline error, park renewal)

## Core Types

```go
type WorkspaceKind string // "local" | "managed_worktree"

type WorkspaceBinding struct {
    Kind          WorkspaceKind
    WorkspacePath string // durable managed path; local follows the project root
    WorktreeName  string // managed only; single portable path component
    Branch        string // managed only; pinned at selection, not live HEAD
}

type SessionDraft struct {          // identity reserved before provisioning
    ID              string
    ProjectID       string
    WorkspaceBinding *WorkspaceBinding // nil for CHAT and local
}
```

`SessionInfo.WorkspaceBinding *WorkspaceBinding` (nil ⇒ CHAT) is returned by
the frontend API as-is; `Session.WorkspaceBinding()` returns a defensive copy.

## Flow

### Binding (managed — the first send, ADR-091)

The chat-side choice feeds `SendMessage`'s `workspace` argument: New Session
eagerly creates a plain LOCAL session row; the chat toolbar's workspace
selector, the BranchPicker's pick mode, and the session selector's grouped
view (each managed branch row's "+" action presets a `createBranch=false`
choice for that branch — `useSessionActions.handleNewSessionOnBranch`) record
the pending choice in `sessionWorkspaceStore` (`local` or a branch —
selection never checks out; a REMOTE-tracking pick records a create-branch
request: local branch = the remote ref's short name, start point = the
remote-tracking ref). The FRESH session's FIRST send carries the choice as
the `workspace` argument (a local choice — or none — sends null and stays
local); `bindSessionWorkspace` provisions/adopts, persists the binding inside
the fresh window, and evicts the in-memory session so the send's own access
restores an orchestrator built for the new workspace.

The picker's pick mode keeps every offered target provisionable: the
working tree's own branch row is disabled (a worktree needs a free branch),
and a remote-tracking row whose LOCAL twin (the ref's short name) is that
branch is disabled the same way — its pick would DWIM-reuse the checkout's
branch and land the session in the shared checkout instead of a worktree.
The create form refuses a submit naming an existing local branch (the DWIM
reuse would silently ignore the explicitly chosen base); the non-pick
Manage-Branches form needs no such guard — the backend refuses with its own
explicit "branch already exists" error.

While the send's bind is running, the chat toolbar renders a small spinner
beside the chosen branch label, without a visible label. Its accessible name
and hover/focus tooltip are `Preparing worktree…`; the accessible name is
carried as the `role="status"` live region's own text content (a visually
hidden `sr-only` span) rather than an `aria-label` — a live region announces its
content, so screen readers reliably announce it on insertion. The indicator is
reachable while workspace selection is locked. It uses the store's
single-slot `consumingFor` consumption window, not an independent busy flag:
`beginConsumption` opens it only for a send carrying a non-null workspace
choice (a second concurrent send carrying a choice is refused — one window at
a time), `endConsumption` settles it in `finally`, on success or failure.
The window replaces BOTH the fresh picker and the pinned chip with the
spinner + the chosen branch label: the pre-send binding would assert the
opposite of what is happening for the whole provisioning wait (a full
checkout, up to `managedProvisionTimeout`). Local-only sends, another
session's window, and selectors hidden by the No-Project/no-active-project
gates show no preparation spinner. Existing-session workspace pins remain
immutable.

```
UI → SendMessage(..., workspace: SessionWorkspaceChoice)
  → freshness pre-checks (no unfinished task, no runtime task)  // nothing touched yet
  → re-bind guard: a window whose persisted binding is already
    managed accepts only the SAME choice (no-op success); a DIFFERENT
    one is the typed ErrWorkspaceAlreadyChosen refusal — a re-selection
    can never silently re-provision (orphaning the first tree) or no-op
  → binding validated (NormalizeWorkspaceBinding)   // BEFORE any git runs
  → branch-holder lookup (createBranch=false, or a create request whose
    branch ALREADY exists — the DWIM rule below)    // git allows one worktree per branch
      managed tree holds it  → ADOPT: binding persisted to the holder's
                               tree name; Owner.Recreate re-validates (no-op /
                               prune+recreate / explicit mismatch failure);
                               post-bind Inspect re-validates under the
                               adoption↔deletion critical section; no
                               compensation — the tree is not ours
      main checkout holds it → deliberate no-op success: the fresh session
                               already executes exactly there; the local
                               binding stays
      external tree holds it → typed ErrBranchBusy refusal naming the tree
      no holder              → provision below
  → worktrees.Owner.ProvisionNewBranch / ProvisionBranch
                                                    // serialized git worktree add at
                                                    // <base>/<project-id>/worktrees/s-<short id>;
                                                    // branch occupancy validated; legacy
                                                    // /.worktrees/ exclusion retained
  → store.BindSessionWorkspace(...)                 // persisted binding REQUIRED,
                                                    // one atomic fresh-window statement
  → stop the session terminal (session:<id>:terminal_exited — the shell
    resurrects in the new workspace) → Manager.EvictSessionCarryingAttachments
    (through FrontendAPI.bindEvict: the staged attachments are captured
    under the same critical section and re-staged on the restored session)
    → the send's own session access lazily restores into the tree
```

Every failure before the persist leaves NOTHING behind (no activity bump, no
message row, no task — the send is rejected and the session stays fresh). A
failure after the persist compensates: the freshly provisioned tree is
released ONLY on a confirmed binding rollback — a rollback the store refused
(the fresh window closed: a parallel send started a task) keeps the tree,
because the persisted binding still names it and releasing it would strand
the binding on a missing path; adopted trees are never touched; the created
branch is kept — no path ever deletes branches.

DWIM creation (git switch semantics): `createBranch=true` is an
ensure-the-branch request, never an unconditional re-create. Before any git
runs, the LOCAL branch's existence is checked (`workspace.BranchExists`,
exact `refs/heads/<branch>` — a same-named tag or remote ref never counts).
A MISSING branch is created at `startPoint` (empty = the checkout's HEAD) —
this is how a remote-tracking pick lands: branch `x` from start point
`origin/x` provisions the tree on a NEW local `x` whose tip IS the remote
ref's commit. An ALREADY EXISTING branch is never re-created or reset: the
request falls through to the same branch-holder dispatch as an
existing-branch pick (free → `ProvisionBranch` checks the existing branch
out in the fresh tree; held → adopt / checkout no-op / typed busy refusal),
so a colliding create can never move the branch under the user.

Sharing (ADR-082): a branch held by an existing managed tree is REUSED — the
new session binds to that tree and several sessions execute in it, exactly
like local sessions sharing the checkout. Orphaned trees (crash between
provisioning and commit, a lost session row) are adopted the same way, and a
prunable orphan is repaired on adoption. The adoption commit re-validates the
tree after the binding is persisted, inside the same critical section
(`FrontendAPI.managedTreeMu`) as the release's [count → remove] pair below:
when the last owner's deletion removed the tree in between, the just-committed
BINDING is rolled back to the previous one (`rollbackSessionBinding` — a
store rewrite only: the session row SURVIVES with its pre-bind binding, no
in-memory session is ever removed) and the send fails with an explicit
retryable error — a session can never survive bound to a tree that no longer
exists (the regression test `TestBindSessionWorkspace_AdoptionLosingRaceRollsBack`
pins the local restored binding and the fresh-tree retry). Compensation on
the provisioned path: a persist failure releases the fresh tree
(`compensateProvisionedTree`, best-effort, logged); a post-persist eviction
failure rolls the binding back and releases the tree ONLY on a confirmed
rollback (see above); adopted trees are never touched; the created branch is
KEPT — no operation in this path ever deletes a branch. The eager
`CreateSession` RPC (no arguments) creates the plain local row; provisioning
lives exclusively in the first send's `bindSessionWorkspace`.

#### Embedding-cache seeding

After its success point (create: after the binding is persisted; fork: after
the store fork), both provisioning flows call `seedWorktreeEmbeddingCache`
(backend `frontend_api_vector.go`): the project checkout's content-addressed
embedding cache is copied into the fresh tree's cache root
(`config.WorktreeEmbeddingCachePath`) — synchronously, before the RPC returns,
so the frontend's immediate focus move builds the tree's vector manager over
an already-warm cache. A session tree is 99-100% identical to its branch
point, and the cache — keyed by chunk-text hash plus model fingerprint with
paths deliberately excluded — is the one layer where that identity is legally
reusable (branch collections are absolute-path-keyed and cannot be shared);
with a warm seed the tree's first index pass reuses the checkout's embeddings
instead of re-running ONNX inference over near-identical content. The seed is
best-effort and idempotent: a missing checkout cache, a derivation failure, or
a partial copy only means the first pass runs cold (copied entries are
checksum-guarded, so a torn file is a miss, never a wrong vector); an existing
target directory is never touched; per-file failures against the checkout's
live cache are skipped. The recreate path needs no call — its cache root
survives the lost tree.

### Restore

Lazy restore and `WorkspacePathFor` decode the stored binding and normalize it
against the project's *current* root: `local` follows the checkout wherever it
is registered; `managed_worktree` preserves its validated stored path,
independent of `git.worktrees_root`. Only a managed binding with an empty path
uses the legacy `<repoRoot>/.worktrees/<name>` derivation. A recorded path is
recognized by the project's legacy/marked container or its project-scoped
`<base>/<projectID>/worktrees` layout, independent of the repository marker's
presence. Foreign containers, name/path mismatches and symlinked trees are
explicit validation failures, not reasons to relocate a tree. For managed bindings `getOrRestoreSession`
runs the installed `WorkspaceEnsurer` (backend `ensureManagedWorkspace`)
inside the restore single-flight, before the orchestrator is built:
`worktrees.Owner.Recreate` guarantees the tree exists on the pinned branch —
no-op when present and matching, mismatch refused (never silently
retargeted), stale metadata pruned and the tree re-created from the existing
branch, and a missing branch is an explicit failure (restore never creates
branches). When the tree had to be recreated, the manager emits a
`service` event (`phase: "orchestration"` — the chat-visibility
discriminator, so the warning renders as a chat row and persists across
reloads) stating that uncommitted changes that existed only in the missing
tree could not be recovered. Identity lookup
never invents a path, and a managed restore never falls back to the project
checkout (no ensurer configured ⇒ fail closed). Once restored, the session's
search tools route through the executor context to its own tree's manager —
created on demand by the per-root registry
([Vector Index routing](workspace.md#vector-index), [ADR-081](../decisions/081-vector-per-root-registry.md)) — so a
missing tree is materialized by the restore ensurer before the first search
ever targets it.

### Release (deletion)

`FrontendAPI.DeleteSession`/`DeleteSessionWithOptions` release the managed
tree BEFORE any session runtime state is removed (the one exception is the
deleting session's own store row on the shared path — step 3), in this order:

1. join the running task (`CancelTask` cancels and waits — the task's final
   writes are then visible to the dirty recheck);
2. stop the session terminal (its shell's cwd lives inside the tree);
3. count the tree's remaining owners
   (`CountManagedWorktreeSessions`, excluding the deleting session) and
   remove the deleting session's OWN row when others remain: while any other
   row — live or archived — still binds the tree, the release is SKIPPED
   (the tree and its uncommitted work stay for the remaining sessions, so
   the confirmations below are never reached) and the deleting session's row
   is removed right there — a concurrent co-owner deletion must observe it
   gone, or two simultaneous deletions of the last two owners would both
   skip and orphan the tree (the pre-flight then returns the project ID so
   the caller's store-only fallback can still clean the session's internal
   files); a count failure blocks the deletion (retryable) instead of
   risking a shared tree;
4. `worktrees.Owner.Release` (last owner only) — the primitives recheck
   dirtiness and lock state at removal time, so the confirmation is never
   based on a stale snapshot. A dirty tree requires
   `SessionDeleteOptions.ConfirmUncommittedLoss` and a locked tree
   `UnlockLockedTree`; otherwise the call fails with
   `*SessionDeleteBlockedError` (naming the deciding option) and the session,
   tree, and branch stay fully intact and retryable (the own-row removal of
   step 3 only happens on the shared path, where no blocking decision
   exists).

Steps 3 and 4 run together inside the adoption↔deletion critical section
(`FrontendAPI.managedTreeMu`) shared with the adoption commit's
[persist → re-validate] pair and with every other deletion's protocol, so
the count always sees every committed binding: a concurrent adoption either
commits (and is counted — release skipped) or commits after the removal and
rolls itself back (see Creation), and of two simultaneous co-owner
deletions the second always observes the first's row gone and releases.

The blocked refusal is a user-facing decision point, not a dead end: the
frontend parks it (`useSessionActions.blockedDelete` — the refused session
first, then every group member still pending; a group keeps its branch-name
label) and `SessionDeleteBlockedDialog` — mounted on both session-list
surfaces (`SessionList`, `SessionSelector`) — names the parked session(s),
explains that confirming deletes the session(s) and their worktree
director{y/ies} while the git branch is kept, shows the verbatim refusal
reason, and offers both consents as explicit checkboxes
(`confirmUncommittedLoss` / `unlockLockedTree`), pre-ticking the one the
refusal named. Confirm retries the whole parked set through
`DeleteSessionWithOptions` carrying exactly the ticked flags — a renewed
refusal refreshes the park in place (new reason/option, remaining ids), any
other failure surfaces inline while the set stays parked and retryable, and
cancel/dismiss releases the park deleting nothing.

A tree that is no longer linked is already gone (nothing to do); a tree
classified non-managed (main/external/foreign) is NEVER removed. Deletion
never resurrects: `Manager.DeleteSession` operates only on in-memory sessions
(the RPC gates on `Manager.HasSession`), so no restore can re-provision a
tree the pre-flight just released; store-only sessions release their tree via
the same pre-flight and finish through the store-only fallback. The branch is
never deleted (primitive-level invariant). Archiving a session retains its
tree and branch untouched. This release protocol is triggered by SESSION
deletion only; the Git panel's `DeleteWorktree`
([Git-panel focus model](#git-panel-focus-model)) is a separate, explicit
worktree-removal path that may delete any linked tree — including a managed
one with bound sessions — on direct user confirmation.

### Fork

`ForkSession` (local source) keeps the local binding. A managed source goes
through `forkManagedSession` with a fresh tree name and the derived branch
`<source branch>-fork-<short dst id>` created at the source tree's COMMITTED
HEAD via `worktrees.Owner.ProvisionNewBranch`; reusing the source tree or
branch is rejected before any row is copied (`ForkSessionWithBinding`
enforces the distinct tree + branch). Uncommitted changes in the source are
NOT copied — the fork's starting point is the commit, and the source tree is
left untouched. A store-fork failure releases the freshly provisioned tree
(its branch is kept). The unfinished-task guard applies to both paths
unchanged.

### Git-panel focus model

Every git RPC resolves its repository root through the focus target
(`resolveGitRepoRoot` → `resolveGitFocusRoot`): the explicitly focused
worktree of the active project when one is set, the project checkout
otherwise. A vanished focus tree falls back to the checkout fail-soft and
clears the override (a stale focus never bricks every git RPC).

```
frontend (useGitFocusSync, mounted at the App root)  backend
  project/session switch ──► GetSessionWorkspace(sessionID)
                            └► SetGitPanelFocus(session workspace)   // default target
  binding transition ───────► (same session-default re-apply)        // the send-time bind's
                                                                      // post-send mirror
  worktree switcher ────────► SetGitPanelFocus(tree path)            // explicit
  worktree delete ──────────► DeleteWorktree(path, force, unlock)     // any linked tree; focus re-resolved
  branch-in-new-worktree ───► CreateBranchInWorktree(name, base)      // focus neither switched nor consulted
  any git RPC ◄──────────── resolveGitRepoRoot() = resolveGitFocusRoot()
  vector RPCs/status ◄───── vectorRootsRegistry().ApplyFocus(root)    // rides every accepted
                                                                      // move (fail-soft)
```

- `SetGitPanelFocus("")` resets to the default; a non-empty path is validated
  against the live `git worktree list` of the active project (checkout,
  managed tree, or external linked tree — foreign paths fail with
  `ErrWorktreeNotLinked`) and stores the canonical entry path.
- The follow sync's third trigger — the send-time-bind re-apply: the hook
  also subscribes to the session list and re-applies the session default
  when the ACTIVE session's `workspace_binding` TRANSITIONS (in practice
  absent/local → managed_worktree — the send-time bind landing). The
  trigger exists because the binding reaches the session store only after
  the fact — the post-send sessions refresh (`refreshSessionRow`) mirrors
  the backend fact once `bindSessionWorkspace` has committed — so without
  it the panel would keep the pre-bind focus target until the next
  unrelated switch. The comparison key is a PRIMITIVE string derived from
  the binding (`<kind>:<workspace_path>`), so a duplicate list delivery
  (fresh binding object, equal content), another session's transition, or
  a `last_active_at`-only update yields an equal key and never re-applies.
  The re-apply rides the same serialized/supersede-guarded apply chain as
  every other focus apply. It intentionally OVERRIDES an explicit focus
  choice made before the bind — the freshly bound tree is the session
  default, exactly what a session switch would apply; an explicit choice
  made AFTER the bind persists until the next project/session switch or
  binding transition — the hook re-applies on a dependency change, never
  continuously.
- Every accepted focus move also re-points the per-root vector registry
  (`focusVectorRoots` → `ApplyFocus`): a non-empty focus lands on the
  canonical tree path, and the empty reset lands on the project checkout —
  deliberately NOT `LeaveFocus` (the visible index identity follows the
  checkout instead of going blank). Fail-soft, mirroring the project-switch
  vector setup: a root the registry cannot focus (manager factory still
  unwired in the startup race, unresolvable root) logs Warn and never fails
  the git RPC. `GetVectorIndexStatus`/`SearchVectorStore`/
  `ReindexVectorIndex` and the `vector_index:status` stream carry only the
  focused root, so after a session starts its tree's indexing progress is
  what the status bar reflects.
- `GetGitPanelFocus` reports the resolved target with its classification
  (`kind`: main/managed/external) and owning session; `ListProjectWorktrees`
  returns the full decorated list (`managed`/`pinned`, session id/name,
  `is_focus`) via `worktrees.Owner.List`.
- Worktree deletion (`DeleteWorktree(path, force, unlock)`): removes ANY
  linked tree of the active project — a managed session tree or an external
  one; the main checkout is refused by the removal primitive (it is the
  project's registration root and is never removed). The path is validated
  against the live `git worktree list` (a foreign path fails with
  `ErrWorktreeNotLinked`); `force` confirms the loss of uncommitted changes
  and `unlock` authorizes removing a locked tree — neither is ever taken
  implicitly (`ErrWorktreeDirty` / `ErrWorktreeLocked` name the missing
  confirmation). Managed trees are removed REGARDLESS of bound sessions —
  an explicit user decision: the session rows and their immutable bindings
  stay, the tree under them is gone, and each bound session's next restore
  re-creates the tree from its still-existing pinned branch (the branch
  itself is never deleted). Cleanup mirrors the session-release flow: the
  vector-registry root is released first (open chromem handles dropped so
  removal is Windows-safe) and a managed tree's derived index storage +
  embedding cache are dropped (`deleteWorktreeVectorIndex`); an external
  tree only releases its registry root. `git:status_changed` is emitted for
  the checkout — the refresh re-resolves the focus server-side, so a focus
  pointing at the deleted tree rolls back to the checkout through the
  stale-focus fallback.
- Branch born in a new worktree (`CreateBranchInWorktree(name, base)`): the
  branch and its tree are created atomically (`git worktree add -b <branch>
  <tree> [<base>]`; empty base = the checkout's HEAD) at a
  managed-container path whose name is derived from the branch (portable
  slug, numeric suffix when taken). The base is pre-validated
  (`rev-parse … ^{commit}` → `ErrInvalidBaseRef`) and branch pre-existence
  fails with `ErrBranchExists` before git runs. The focus is NEITHER
  switched NOR consulted — the focused tree is never checked out into the
  new branch, so a managed-tree focus does not block the operation, and
  entering the new tree stays the user's decision. On success
  `git:status_changed` refreshes the branch list and status while the
  frontend's Git-focus refresh (`refreshGitFocus`) re-reads the worktree
  list, landing the new tree in the Worktrees section; the tree's embedding
  cache is seeded best-effort, same contract as session provisioning.
- The Worktrees section is shown in pick mode too: the pick records a
  BRANCH, but an existing worktree (e.g. a stale managed tree) can still be
  deleted — through the mandatory `WorktreeDeleteConfirmDialog` — without
  leaving the chat-side picker; the deletion never touches the recorded
  choice. In pick mode `NewBranchSection`'s Worktree checkbox is disabled
  ("The session always gets its own worktree"): the recorded branch always
  lands in its own provisioned (or adopted) tree.
- The pinned-branch decision: `CheckoutBranch`, `CreateBranch`,
  `CheckoutRemoteBranch`, and renaming the pinned branch are refused with
  `ErrPinnedWorktreeBranch` while the focus is a managed session worktree
  (classification via legacy or repository-marked container ownership — no git spawn);
  checking out the pin itself stays allowed, and the local checkout and
  external trees keep normal checkout. Switching a managed tree off its pin
  would desynchronize the immutable binding and break restore/recreate.
- The pin is rendered, not just enforced: while the ACTIVE session is
  managed-worktree-bound, `BranchDropdown` and `BranchPicker` derive the pin
  from the session's `workspace_binding` (`useSessionWorktreePin` — the
  session tree is matched by `session_id`, stored `workspace_path`, or
  managed `worktree_name`, covering shared trees and non-canonical stored
  paths) and mark the pinned branch row and the session's own tree row with
  a LOCK instead of the focus/current checkmark — the check stays a
  mutable-status marker (Git-panel focus, focused tree's checked-out
  branch) and may sit on a different row when the focus was moved. The
  locked rows offer NO delete action (the pinned branch cannot be deleted
  while the session's tree holds it; deleting the session's own tree from
  inside the session is never a valid move), the pinned branch row's
  Rename action is disabled with a pinned reason (the branch name is part
  of the immutable binding), and the pinned branch row is a no-op click
  regardless of `is_current`, its tooltip rendering the pinned reason as
  cause — consequence — remedy: `Pinned: this branch is held by this
  session's managed worktree — the Git panel follows that tree; switch the
  focus in the Worktrees section to work on other branches`.
  Focus switching stays allowed on
  every row, the locked ones included; other sessions' managed trees keep
  their delete affordance. The pin props are dropped in pick mode —
  `Choose Branch` selects a workspace for a FRESH session and no session pin
  exists there. This is a UI reflection of the server-side refusal above,
  never a second enforcement layer.
- The managed-FOCUS refusal is rendered up front too: the same hook exposes
  a `branchSwitchBlockedReason` gate that keys on the FOCUSED tree (any
  session's — the focus can rest on another session's tree while a local
  session is active), not on the active session's binding. While the focus
  sits on a managed session tree, every off-pin branch row in
  `BranchDropdown` and `BranchPicker` (local AND remote checkouts) dims
  with that reason as its tooltip, the click is a no-op, and the row keeps
  its tab stop with `aria-disabled` so keyboard users reach the
  explanation; `NewBranchSection`'s in-place creation path is disabled with
  the same reason. Not gated: the rows' hover actions (push, merge, rebase,
  rename, delete — none is focus-scoped), the Worktrees section's focus
  switching, and `NewBranchSection`'s "Worktree" path (it births a fresh
  tree and never touches the focused one). In pick mode the gate is absent
  — rows record an intent and nothing is checked out. The gate is a
  pre-emptive mirror of the same server-side refusal (a stale focus
  snapshot can still hit the live backend answer), never a second
  enforcement layer.
- Focus is UI state on the backend because the RPCs resolve it server-side;
  it never retargets execution (ADR-080's `GitPanelTarget` separation).
  `GetGitStatus` follows the focus root the same way (containment accepts
  the project workspace and, for an external-tree focus, the focus root).
  The vector-registry focus an accepted move drags along is likewise
  user-side only — agent-side routing stays context-borne (ADR-081), so
  focusing a tree never redirects another session's searches.

### User-facing workspace roots

The workspace root for USER-FACING path resolution follows the ACTIVE
SESSION (its execution workspace); the project checkout is only the
fallback. Chat file references — markdown links, `@file` clicks, and
tool-card paths — resolve through `resolveChatWorkspaceRoot()` against the
session's own workspace: the managed tree for a `managed_worktree` session
of the active project (the binding's path, or the backend
`GetSessionWorkspace` RPC while the binding row has not landed), the
checkout for `local`/unbound sessions, with a project-ID membership guard
so a foreign session's tree never leaks into another project's window
([ADR-095](../decisions/095-chat-file-reference-path-semantics.md),
Decision 6). The research/papers roots and the workspace watcher follow the
same session-first rule (below and
[workspace.md](workspace.md#filesystem-watcher)); agent-side execution
paths are unrelated — a session always executes and searches in its own
workspace regardless of which session is active.

### Research/papers roots

The RESEARCH and papers subsystems resolve their root the same way the
explorer does — by the ACTIVE SESSION, not the project row (the "research
root follows the active session" protocol, [ADR-099](../decisions/099-research-root-follows-active-session.md)):

- The ACTIVE SESSION is the project's user-selected session when a usable
  selection is persisted (`project_ui_state.saved_session_id`, written by
  `SaveProjectActiveSession` on every picker switch), falling back to the
  most recently active non-archived session when no usable selection
  resolves — ONE definition (`activeSessionID`) shared by every backend
  consumer, so the panel, papers surfaces, watcher and tracked emit roots can
  never disagree with the session the UI shows.
- The agent writes research artifacts under `<session-workspace>/.research`
  (`session/Manager.researchProjectInfo` derives the root from the session's
  own execution workspace), so every panel-side read/mutation path
  (`FrontendAPI.effectiveResearchRoot`), the papers library and comparisons
  roots, and the workspace watcher's tracked emit roots resolve
  `<workspace-of-the-active-session>/.research` through the READ-ONLY
  `Manager.WorkspacePathFor` with `GetSessionWorkspace`'s project-ID
  membership, falling back to `<proj.WorkspacePath>/.research` when no
  session of the project resolves.
- Containment for the root (and the papers/comparisons subdirectories) is
  validated against the workspace the root was derived from
  (`researchRootWithBoundary`) — a managed tree lives OUTSIDE the checkout,
  so the checkout is the wrong containment boundary whenever the session
  resolution won.
- The CODE-mode watcher ROOT follows the active session too (see
  [workspace.md](workspace.md#filesystem-watcher)): its root is the session's
  workspace — the managed tree — so the session's `.research` is an in-root
  recursive watch, while the checkout's `.research` stays watched through an
  admitted `AllowRoot` tree when the two differ. The frontend's session-switch
  `WatchDirectory(session workspace)` call re-scopes the watcher to the new
  tree and re-points the tracked emit roots, so `research:file_changed` /
  `papers:changed` (and `workspace:tree_changed`) follow the session the user
  switched to.

## Invariants

- A binding is immutable after creation: project, kind, worktree name, and
  branch never change for an existing session row (enforced by the upsert's
  `WHERE` identity comparison).
- A managed tree may be bound by SEVERAL sessions (ADR-082 shared trees);
  `idx_session_managed_workspace` is a plain lookup index, and the tree is
  released only when its last bound row is deleted
  (`CountManagedWorktreeSessions`) — and exactly then: the deletion protocol
  [count co-owners → own-row removal → release] runs inside the shared
  critical section, so simultaneous deletions of the last two owners release
  exactly once instead of both skipping and orphaning the tree. The count is
  archived rows included, and NOT scoped by project: tree names are
  UUID-derived and globally unique, so a repository registered as several
  projects binds the same tree across those project rows, and every binding
  must hold it open.
- A committed binding always names a tree that existed at commit time: the
  adoption's [persist binding → re-validate tree] and the release's
  [count co-owners → remove tree] pairs share one critical section
  (`FrontendAPI.managedTreeMu`), so a concurrent last-owner deletion either
  sees the committed binding (release skipped) or removes the tree first and
  the adoption rolls its just-committed session back — no row, no in-memory
  session; a retry then provisions a fresh tree for the surviving branch.
- Managed execution paths are durable identity: normalization validates and
  preserves the stored path, independent of the current creation root and of
  the repository marker (a project-scoped `<base>/<projectID>/worktrees` path is
  recognized by its project-id layout); an empty legacy managed path derives
  from `<repoRoot>/.worktrees/<name>`. Local execution follows the project's
  current registered checkout.
- CHAT (`__no_project__`) sessions have no binding; a non-nil binding for them
  is invalid.
- `GitPanelTarget` may name only the owning project's checkout or one of its
  managed trees; it can never change `ExecutionWorkspace`.
- The research/papers root follows the active session (ADR-099): a
  panel-side resolver may derive the root only from the requesting project's
  active session workspace or its checkout — never from another project's
  session — and containment uses the workspace the root was derived from.
- The workspace root for user-facing path resolution follows the active
  session (ADR-095 Decision 6, ADR-099): chat file references, the file
  viewer, and every panel-side root resolver answer the ACTIVE SESSION's
  execution workspace — the managed tree for a bound session, the project
  checkout otherwise — never another project's workspace.
- Git-panel focus resolution always funnels through `resolveGitFocusRoot`;
  a focus override is admitted only after worktree-list validation and falls
  back to the project checkout when the tree is gone.
- A `SetGitPanelFocus` move re-points the per-root vector registry focus
  (the empty reset targets the checkout, not `LeaveFocus`) and is fail-soft:
  a vector-side focus failure is logged at Warn and never fails the git RPC;
  the `vector_index:status` stream and the vector RPCs follow the panel.
- Branch-switching RPCs (`CheckoutBranch`, `CreateBranch`,
  `CheckoutRemoteBranch`, rename of the pinned branch) fail with
  `ErrPinnedWorktreeBranch` while the Git panel is focused on a managed
  session worktree and the resulting branch differs from the pin; the local
  checkout and external linked trees always keep normal checkout.
- Worktree deletion removes any linked tree — managed or external — but
  never the main checkout; a managed tree goes even with bound sessions
  (their rows and immutable bindings survive; the next restore re-creates
  the tree from the surviving pinned branch), and the branch is never
  deleted.
- `CreateBranchInWorktree` never moves or consults the Git-panel focus: the
  branch and its tree are born atomically from the base in the managed
  container, and the currently focused tree is never checked out into the
  new branch.
- Managed paths are constructed and validated through `backend/config`
  helpers and shared `core/worktree_paths.go` ownership checks. Names are
  portable single components; trees and containers are real directories.
  New containers are outside the checkout. Legacy checkout-local containers
  remain valid without any migration.
- Worktree mutations flow only through `core/workspace` primitives (hardened
  spawns, serialized per repo, explicit failure taxonomy) and the backend
  `worktrees.Owner`; mutating worktree operations never run arbitrary
  user/agent commands.
- The legacy checkout-local container is hidden via the common dir's
  `info/exclude` (`/.worktrees/`, idempotent, atomic); new containers live
  outside the checkout, and `.gitignore` is never modified.
- No operation in the provisioning path deletes a branch; session deletion
  removes the tree and its metadata only.
- Deletion never resurrects: `Manager.DeleteSession` refuses sessions not
  live in memory (the RPC gates on `HasSession`), so a deletion flow can
  never re-provision a tree it just released through the restore ensurer.
- Vector-index routing is per workspace root (ADR-081): every routed root
  owns a `vectorindex.Manager` with worktree-scoped storage under
  `<project vector_index>/worktrees/<name>` (branch resolved from that root;
  disjoint, simultaneously live state per tree). Agent-side search resolves
  the root from the executor context — a session always searches its own
  tree, regardless of which session is active or focused — while the
  user-facing RPCs follow the Git-panel focus. Membership is decided by
  legacy or repository-marked container ownership, independent of current
  config, and an unknown root is rejected rather than
  silently indexed. No send ever re-points shared index state.
- Releasing a managed tree releases its registry manager first (open chromem
  handles dropped so removal is Windows-safe), then removes its vector-index
  storage root and per-tree embedding cache; project deletion removes every
  worktree root together with the project root.
- Worktree embedding-cache seeding is a provisioning-time, best-effort,
  once-per-tree step: it runs only after the flow's success point and before
  the RPC returns, never touches an existing cache root, copies only the
  content-addressed entries (never branch collections — those are
  absolute-path-keyed and tree-specific), and its failure never fails the
  provisioning flow.
- A tool-call path argument resolving inside a managed worktree container
  (`<root>/.worktrees`) reachable from the session's own roots escalates as
  the canonical hard reason `managed_worktree_traversal`
  (`worktreeTraversalHardReason` / `ReasonCodeManagedWorktreeTraversal`,
  `core/tools/registry_worktrees.go`): the container lives inside the
  checkout, so root containment cannot separate the checkout from sibling
  session trees, and a local session would otherwise read/write another
  session's isolated tree silently. Exemptions: the session's own managed
  tree (the workspace is a child of the container), and targets already
  out-of-root (soft containment covers those). Shell tools are untouched
  (ordinary flowsh/judge gates); group deny is enforced upstream. The reason
  is canonical: `allow` never executes it silently and `ExecuteUnattended`
  blocks it.

## Configuration

No settings UI or RPC exposes the root. `git.worktrees_root` is an optional
absolute, clean, literal filesystem path (no tilde or environment expansion).
Empty/unset selects `<agentDir>/projects`; a custom value replaces that base.
Fresh trees use `<base>/<project-id>/worktrees/<name>`; the usual default is
`~/.c0wrk/projects/<project-id>/worktrees/<name>`. Editing YAML takes effect
on restart and affects only fresh session trees, forks, and Git-panel
branch-in-worktree creation. Existing worktrees stay at their original paths;
there is no migration.

At provisioning, existing operator/system prefixes of the base are resolved.
Project/container/tree descendants remain real directories, and the concrete
container is outside (and does not contain) the repository checkout. Each new
container carries an owner-only `.c0wrk-worktree-repository` regular-file
marker identifying its canonical repository. Classification of arbitrary
git-listed trees uses that marker (with the legacy container), not current
config, so orphan adoption, focus pinning, and vector routing still recognize
old containers after config changes. Markers survive tree deletion to permit
recreation at the persisted path. A durable binding additionally records a
project-scoped `<base>/<project-id>/worktrees/<name>` path that is recognized
by its project-id layout WITHOUT the marker, so a recorded identity stays
decodable and restorable after the marker or the app-data container is removed;
only foreign or arbitrary paths are refused (containment and symlink-safety are
still enforced). Legacy `<repo>/.worktrees/<name>` paths remain managed without
markers; only those legacy targets install the common `info/exclude` entry.

Vector storage/cache paths remain project/name-keyed and are not relocated.
Project deletion refuses while its default app-data worktree container holds
entries other than the marker, before any session or vector teardown; explicit
worktree removal supplies the existing dirty/lock confirmations first.
CHAT workspaces remain `~/.c0wrk/projects/__no_project__/<id>/workspace`.

## Extension Points

- Frontend UI for the managed lifecycle: the send-time branch selection is landed — the chat toolbar's workspace selector (`SessionWorkspaceSelector`) offers `local` and `branch…` while the active session is FRESH, and the BranchPicker's pick mode records a picked existing branch, a drafted new branch (+ start point), or a picked remote-tracking branch (create-branch request from the remote ref) into `sessionWorkspaceStore`; provisioning runs at the session's FIRST send via `SendMessage`'s `workspace` argument with no in-place checkout (ADR-091), and the blocked-deletion refusal has its confirmation surface — `SessionDeleteBlockedDialog` on both session-list surfaces retrying through `DeleteSessionWithOptions` (see [Release](#release-deletion)). Remaining consumer work: rendering of the `service` (`phase: "orchestration"`) recreation warning (the chat row rendering itself is live; a dedicated visual treatment may follow). The RPC protocol is complete.
- Git panel: the focus model is landed — `SetGitPanelFocus`/
  `GetGitPanelFocus`/`ListProjectWorktrees` with the worktree switcher
  (BranchPicker + BranchDropdown) and the
  follow-the-session sync (`useGitFocusSync`, mounted at the App root —
  project/session switches plus the active session's send-time-bind
  transition, re-applying the session default) —
  together with the panel-side management RPCs and their UI: `DeleteWorktree`
  behind the Worktrees section's hover delete (mandatory
  `WorktreeDeleteConfirmDialog`, section shown in pick mode too) and
  `CreateBranchInWorktree` behind `NewBranchSection`'s Worktree checkbox
  (branch + tree created from the base, focus never switched; disabled in
  pick mode — the recorded choice always gets its own worktree).
  Extensions keep the same
  contract: the focus target stays UI state resolved server-side and is
  never written back into the session binding; `Owner.List`/`Inspect` back
  the worktree views.
- Project deletion: `DeleteProject` refuses before teardown while the default
  app-data container holds worktrees. After explicit removal it removes
  in-memory sessions and project data. Coordinated removal of custom-root
  and legacy trees during project deletion (with the same dirty/lock protocol)
  remains a follow-up consumer of `Owner.Release`; those trees are not moved.
- Additional kinds (if ever needed) extend `WorkspaceKind` and
  `NormalizeWorkspaceBinding` only — storage, restore, and fork identity flow
  through the same validation.

## Related Specs

- [session-lifecycle.md](session-lifecycle.md) — creation/restore/fork flows this binds into
- [ADR-080 Typed Session Workspace Bindings](../decisions/080-session-worktrees.md)
- [ADR-082 Shared Managed Worktrees](../decisions/082-shared-managed-worktrees.md)
- [ADR-099 Research/papers root follows the active session](../decisions/099-research-root-follows-active-session.md)
- [ADR-030 Session Context Restore](../decisions/030-session-context-restore.md)
- [backend-core.md](../contracts/backend-core.md) — the factory's workspace parameter is the execution workspace
- [git-auto-fetch.md](git-auto-fetch.md) — remote refresh funnels through the same focus-resolving `resolveGitRepoRoot` (equivalent for fetch: the common dir is shared by every worktree)
