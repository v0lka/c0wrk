# Session Worktrees

## Purpose

Defines how a CODE session is bound to an execution workspace — the project
checkout (`local`) or a session-owned git worktree (`managed_worktree`) — and
how that binding stays separate from project identity and the Git panel's
focus. Covers persistence, validation, migration, restore, and fork identity,
plus the worktree provisioning primitives (`core/workspace/worktrees.go`),
the backend ownership coordinator (`backend/worktrees`), the
session-lifecycle wiring that drives them from the session RPC flows
(`backend/frontend_api_worktrees.go`: creation, restore, fork, deletion,
archive), and the Git-panel focus model that consumes the separation
(`backend/frontend_api_git_focus.go`: focus-target resolution behind every
git RPC, the worktree listing RPC, and the pinned-branch refusal).

## Key Files

- `core/workspace/worktrees.go` — the safe git-worktree primitive service: `ListWorktrees` (strict porcelain parsing, main/managed/external classification), `AddWorktree`/`RecreateWorktree`/`RemoveWorktree`/`PruneWorktrees` with per-repo serialization, branch-occupancy validation, structural path validation and the explicit failure taxonomy; installs the `/.worktrees/` exclusion into the common dir's `info/exclude`
- `backend/worktrees/owner.go` — `Owner`: the backend ownership coordinator (Provision/Recreate/Release/List/Inspect); every path derived via `config.ManagedWorktreePath`, managed-only mutation, release never deletes branches
- `backend/frontend_api_worktrees.go` — the Git lifecycle owner driving the coordinator from the session flows: `CreateManagedSession` (draft → provision → `CreateSessionFromDraft` → persist, compensated on runtime/DB failure), the `WorkspaceEnsurer` installed on the session manager (restore validation + recreation warning), `forkManagedSession` (new tree at source committed HEAD), and the deletion protocol (`DeleteSessionWithOptions`, `SessionDeleteBlockedError`)
- `core/pathsegments.go` — `WorktreesRelativePath` (the shared `.worktrees` segment constant)
- `backend/session/workspace_binding.go` — `WorkspaceKind`, `WorkspaceBinding`, `WorkspaceEnsurer`, `NormalizeWorkspaceBinding`, `SessionDraft`/`NewSessionDraft`, context DTOs (`ProjectContext`, `GitPanelTarget`, `SessionContexts`)
- `backend/session/workspace_context.go` — `ResolveSessionContexts` (execution workspace vs Git-panel target)
- `backend/session/persistence_binding.go` — `sessions.workspace_binding` migration, binding encode/decode, shared binding columns for list/load queries
- `backend/session/persistence.go` — binding-aware `SaveSession`/`LoadSession`/`ListSessions*`; immutable-identity upsert
- `backend/session/persistence_fork.go` — `ForkSessionWithBinding` (managed fork requires a new tree + derived branch)
- `backend/session/manager.go` — `CreateSessionFromDraft` (commit a prepared workspace), `SetWorkspaceEnsurer` + the restore-time ensure inside `getOrRestoreSession`'s single-flight, `HasSession` (non-restoring existence), binding-aware lazy restore and `WorkspacePathFor`
- `backend/config/paths.go` — `ManagedWorktreesDir`, `ManagedWorktreePath` (the only place `.worktrees` paths are constructed or validated)
- `backend/frontend_api_git_focus.go` — the Git-panel focus model: `SetGitPanelFocus`/`GetGitPanelFocus`/`ListProjectWorktrees` RPCs, `resolveGitFocusRoot` (the focus resolution behind `resolveGitRepoRoot`), owning-session metadata, the pinned-branch guard (`ErrPinnedWorktreeBranch`, `refusePinnedBranchSwitch`), and the fail-soft vector-registry re-point every accepted focus move carries (`focusVectorRoots` → `ApplyFocus`)
- `backend/frontend_api_vector.go` — vector-index RPC surface (`SearchVectorStore`/`GetVectorIndexStatus`/`ReindexVectorIndex`, all routed to the Git-panel FOCUS root), focus-target resolution (`resolveVectorIndexTarget` — the project switch resolves the saved session's tree), worktree-scoped storage (`config.WorktreeVectorIndexPath`), release cleanup (`deleteWorktreeVectorIndex`: registry release → storage → per-tree cache) and provisioning seeding (`seedWorktreeEmbeddingCache`: copies the checkout's embedding cache into a fresh tree's cache root)
- `backend/vector_roots.go` — the per-root registry (ADR-081): one `vectorindex.Manager` per workspace root (single-flight creation, LRU-bounded live set with the focus pinned), agent-side routing by the executor context's workspace root, `ApplyFocus`/`LeaveFocus`, `Release`/`ReleaseProject`/`ShutdownAll`, and `Application.buildVectorRouter` (the shared search closures)
- `core/vectorindex/git.go` — `resolveGitDir` follows a linked worktree's `.git` pointer file to the private git directory that owns HEAD, so the vector index's branch monitoring works inside managed trees; `backend/config/paths.go` `ManagedWorktreeNameFromPath`/`WorktreeVectorIndexPath` derive the per-tree index storage
- `frontend/src/lib/gitFocus.ts` + `frontend/src/hooks/useGitFocusSync.ts` — the frontend side: serialized/supersede-guarded focus applies and the follow-the-session effect (project/session switches move the focus to the active session's execution workspace); the hook is mounted once at the App root (beside `useVectorIndexStatus`) — the Git panel lives only while the git tab is active, and the follow must also run on chat-area session switches with any other tab open
- `frontend/src/stores/sessionDraftStore.ts` + `frontend/src/lib/sessionDraft.ts` — the chat-side draft UX: the pending New-Session draft (project + drafted workspace) and `createSessionFromDraft()`, the single commit path that routes a branch draft to `CreateManagedSession` and everything else to `CreateSession`
- `frontend/src/components/chat/SessionWorkspaceSelector.tsx` + `frontend/src/components/GitPanel/BranchPicker.tsx` — the draft's selection surfaces: the chat toolbar selector (`local` / `branch…`, read-only pinned display for existing sessions) and the BranchPicker's intent-separated draft mode (`branchPickerMode: 'draft'`) that records a picked/created branch without any checkout

## Core Types

```go
type WorkspaceKind string // "local" | "managed_worktree"

type WorkspaceBinding struct {
    Kind          WorkspaceKind
    WorkspacePath string // derived at validation; stored copy is advisory
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

### Creation (managed)

The chat-side draft UX feeds this RPC: New Session arms a draft
(`sessionDraftStore`); the chat toolbar's workspace selector and the
BranchPicker's draft mode record the choice (`local` or a branch — selection
never checks out, and the working tree's own branch is disabled since a
managed worktree needs a free branch); the first send / attachment / paste /
terminal open commits it via `lib/sessionDraft.createSessionFromDraft()`,
which calls `CreateManagedSession` for a branch draft and `CreateSession`
otherwise.

```
UI → RPC CreateManagedSession(branch, createBranch, startPoint)
  → NewSessionDraft(projectID, nil)                 // identity reserved, nothing created
  → binding validated (NormalizeWorkspaceBinding)   // BEFORE any git runs
  → worktrees.Owner.ProvisionNewBranch / ProvisionBranch
                                                    // serialized git worktree add at
                                                    // <repo>/.worktrees/s-<short id>; branch
                                                    // occupancy validated; /.worktrees/
                                                    // exclusion installed in info/exclude
  → Manager.CreateSessionFromDraft(draft, repoRoot) // validates, then builds orchestrator
  → store.SaveSession(info)                         // persisted binding REQUIRED
```

Compensation: if the runtime commit or the persistence step fails after
provisioning, the in-memory session is removed and the fresh tree released
(best-effort, logged); the created branch is KEPT — no operation in this path
ever deletes a branch. `CreateSession(projectID, workspacePath)` remains the
local/CHAT entry point and wraps the same path with a nil binding.

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
is registered; `managed_worktree` re-derives
`<repoRoot>/.worktrees/<name>`. For managed bindings `getOrRestoreSession`
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
tree BEFORE any session state is removed, in this order:

1. join the running task (`CancelTask` cancels and waits — the task's final
   writes are then visible to the dirty recheck);
2. stop the session terminal (its shell's cwd lives inside the tree);
3. `worktrees.Owner.Release` — the primitives recheck dirtiness and lock
   state at removal time, so the confirmation is never based on a stale
   snapshot. A dirty tree requires `SessionDeleteOptions.ConfirmUncommittedLoss`
   and a locked tree `UnlockLockedTree`; otherwise the call fails with
   `*SessionDeleteBlockedError` (naming the deciding option) and the session,
   tree, and branch stay fully intact and retryable.

A tree that is no longer linked is already gone (nothing to do); a tree
classified non-managed (main/external/foreign) is NEVER removed. Deletion
never resurrects: `Manager.DeleteSession` operates only on in-memory sessions
(the RPC gates on `Manager.HasSession`), so no restore can re-provision a
tree the pre-flight just released; store-only sessions release their tree via
the same pre-flight and finish through the store-only fallback. The branch is
never deleted (primitive-level invariant). Archiving a session retains its
tree and branch untouched.

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
  worktree switcher ────────► SetGitPanelFocus(tree path)            // explicit
  focus button ─────────────► SetGitPanelFocus(session workspace)     // snap back
  any git RPC ◄──────────── resolveGitRepoRoot() = resolveGitFocusRoot()
  vector RPCs/status ◄───── vectorRootsRegistry().ApplyFocus(root)    // rides every accepted
                                                                      // move (fail-soft)
```

- `SetGitPanelFocus("")` resets to the default; a non-empty path is validated
  against the live `git worktree list` of the active project (checkout,
  managed tree, or external linked tree — foreign paths fail with
  `ErrWorktreeNotLinked`) and stores the canonical entry path.
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
- The pinned-branch decision: `CheckoutBranch`, `CreateBranch`,
  `CheckoutRemoteBranch`, and renaming the pinned branch are refused with
  `ErrPinnedWorktreeBranch` while the focus is a managed session worktree
  (classification via the `.worktrees` container containment — no git spawn);
  checking out the pin itself stays allowed, and the local checkout and
  external trees keep normal checkout. Switching a managed tree off its pin
  would desynchronize the immutable binding and break restore/recreate.
- Focus is UI state on the backend because the RPCs resolve it server-side;
  it never retargets execution (ADR-080's `GitPanelTarget` separation).
  `GetGitStatus` follows the focus root the same way (containment accepts
  the project workspace and, for an external-tree focus, the focus root).
  The vector-registry focus an accepted move drags along is likewise
  user-side only — agent-side routing stays context-borne (ADR-081), so
  focusing a tree never redirects another session's searches.

## Invariants

- A binding is immutable after creation: project, kind, worktree name, and
  branch never change for an existing session row (enforced by the upsert's
  `WHERE` identity comparison).
- At most one session owns a given managed tree (partial unique index on the
  derived `workspace_path`).
- Execution paths are derived, never trusted from storage: `local` ⇒ project
  root, `managed_worktree` ⇒ `ManagedWorktreePath(repoRoot, name)`; a stored
  `workspace_path` that disagrees is rewritten to the derived value.
- CHAT (`__no_project__`) sessions have no binding; a non-nil binding for them
  is invalid.
- `GitPanelTarget` may name only the owning project's checkout or one of its
  managed trees; it can never change `ExecutionWorkspace`.
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
- `.worktrees` paths are constructed exclusively via `backend/config`
  helpers; the name is validated (portable, no traversal, no device names) and
  the container/tree must not be a symlink and must be inside the repo.
- Worktree mutations flow only through `core/workspace` primitives (hardened
  spawns, serialized per repo, explicit failure taxonomy) and the backend
  `worktrees.Owner`; mutating worktree operations never run arbitrary
  user/agent commands.
- The managed container is hidden via the common dir's `info/exclude`
  (`/.worktrees/`, idempotent, atomic); `.gitignore` is never modified.
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
  containment in the project's `.worktrees` container, never by comparing
  paths to the checkout, and an unknown root is rejected rather than
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

## Configuration

No `config.yaml` keys. The managed container is always
`<repo>/.worktrees/`, excluded from the main checkout's status via the common
`info/exclude` (installed by the primitives). CHAT workspaces remain
`~/.c0wrk/projects/__no_project__/<id>/workspace`.

## Extension Points

- Frontend UI for the managed lifecycle: the draft-time branch selection is landed — the chat toolbar's workspace selector (`SessionWorkspaceSelector`) offers `local` and `branch…`, and the BranchPicker's draft mode records a picked existing branch or a drafted new branch (+ start point) into `sessionDraftStore`; provisioning runs at draft commit via `CreateManagedSession` with no in-place checkout. Remaining consumer work: the `*SessionDeleteBlockedError` confirmation dialog → `DeleteSessionWithOptions` with the named option, and rendering of the `service` (`phase: "orchestration"`) recreation warning (the chat row rendering itself is live; a dedicated visual treatment may follow). The RPC protocol is complete.
- Git panel: the focus model is landed — `SetGitPanelFocus`/
  `GetGitPanelFocus`/`ListProjectWorktrees` with the worktree switcher
  (BranchPicker + BranchDropdown), the header focus button, and the
  follow-the-session sync (`useGitFocusSync`, mounted at the App root).
  Extensions keep the same
  contract: the focus target stays UI state resolved server-side and is
  never written back into the session binding; `Owner.List`/`Inspect` back
  the worktree views.
- Project deletion: `DeleteProject` currently removes in-memory sessions and
  the project directory; releasing the managed trees of the project's
  store-only sessions (same confirmation protocol as session deletion) is a
  follow-up consumer of the same `Owner.Release` seam.
- Additional kinds (if ever needed) extend `WorkspaceKind` and
  `NormalizeWorkspaceBinding` only — storage, restore, and fork identity flow
  through the same validation.

## Related Specs

- [session-lifecycle.md](session-lifecycle.md) — creation/restore/fork flows this binds into
- [ADR-080 Typed Session Workspace Bindings](../decisions/080-session-worktrees.md)
- [ADR-030 Session Context Restore](../decisions/030-session-context-restore.md)
- [backend-core.md](../contracts/backend-core.md) — the factory's workspace parameter is the execution workspace
- [git-auto-fetch.md](git-auto-fetch.md) — remote refresh funnels through the same focus-resolving `resolveGitRepoRoot` (equivalent for fetch: the common dir is shared by every worktree)
