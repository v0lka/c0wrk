# ADR-082: Shared Managed Worktrees

## Status

Accepted

## Context

ADR-080 gave every CODE session an optional per-session managed worktree under
`<repo>/.worktrees/<name>` and enforced **one session per tree** with a partial
UNIQUE index (`idx_session_managed_workspace` on the derived
`workspace_path`). Git, however, allows a branch to be checked out in at most
one worktree, so the uniqueness rule collided with branch reuse at the RPC
layer: creating a second session on a branch that some managed tree (or the
main checkout, or an orphaned tree left by a crashed deletion) already held
failed with the typed `ErrBranchBusy` before git even ran. Local sessions never
had this restriction — any number of them execute in the project checkout — so
the managed path was strictly less capable than the path it was meant to
improve. Meanwhile the primitives already supported every ingredient of reuse
(`RecreateWorktree` no-ops on a live matching tree and repairs stale
metadata), and the deletion protocol already serialized on the tree, not on
per-session state.

## Decision

**Managed worktrees are shareable: several sessions may bind to the same
tree, mirroring how local sessions share the checkout** (normally sessions of
one project; a repository registered as several projects shares its trees
across those project rows too, so the release refcount below is keyed by the
globally unique tree name, never by project).

1. **Creation reuses the branch holder.** `CreateManagedSession` lists the
   repository's worktrees before provisioning and looks up the holder of the
   requested branch:
   - a **managed** tree holds it → the new session's binding carries that
     tree's own name (`WorktreeName`), so the derived execution path is the
     existing tree; `worktrees.Owner.Recreate` re-validates the tree at
     adoption time (no-op for a live matching tree; prune + re-create for
     stale metadata; explicit `ErrWorktreeBranchMismatch`/`ErrBranchMissing`
     on races; a tree that vanished entirely between the listing and the
     Recreate is simply re-created from the branch, and the session then
     lives on that fresh checkout — self-healing, only the deleted tree's
     derived vector/cache data is discarded and re-indexed on demand). The
     commit re-validates the tree AGAIN after the binding is
     persisted and rolls the just-committed session back (row and in-memory
     session removed; the branch is kept) when the tree was removed in
     between by the last owner's deletion: the adoption's
     [commit → re-validate] pair and the release's [count → remove] pair
     below share one critical section (`FrontendAPI.managedTreeMu`), so a
     committed binding always names a tree that existed at commit time — an
     in-memory session would otherwise run against a missing directory with
     no self-healing until restart. The fresh-tree compensation never
     touches an adopted tree;
   - the **main checkout** holds it → the request silently degrades to a
     plain **local** session in the checkout (a managed tree can never hold a
     branch the checkout holds; the fallback keeps `CreateSession`'s
     best-effort persistence, since nothing was provisioned);
   - an **external** linked worktree holds it → an explicit `ErrBranchBusy`
     refusal naming the foreign tree (c0wrk never adopts or removes trees it
     does not own);
   - no holder → a fresh tree is provisioned exactly as before
     (`ProvisionNewBranch`/`ProvisionBranch`).
   The schema no longer rejects two sessions on one tree: the UNIQUE index is
   rewritten to a plain lookup index (`migrateSharedManagedWorktrees`,
   idempotent; fresh databases create the plain form directly), and
   `SQLiteSessionStore.CountManagedWorktreeSessions` counts a tree's bound
   rows (archived included — an archived session can be unarchived and runs
   in its tree again; keyed by the globally unique tree name, not by project,
   so bindings from every project row of a doubly-registered repository hold
   the tree open).

2. **Release is reference-counted.** The deletion pre-flight
   (`prepareManagedTreeDeletion`) runs the [count co-owners → own-row removal
   → release] protocol inside the critical section: when the deleting session
   is the tree's LAST owner, the tree is released; otherwise the tree — and
   any uncommitted work in it — stays for the remaining sessions, the
   dirty/locked confirmations are never reached, and the deleting session's
   own row is still removed right there, so a concurrent co-owner deletion
   counts it gone and performs the release — two simultaneous deletions of
   the last two owners can never both skip and orphan the tree (the
   pre-flight then reports the project ID so the caller's store-only fallback
   can still clean the session's internal files). Task-join/terminal-stop
   still run for the deleted session itself. The whole protocol shares the
   critical section with the adoption commit above, so the count can never
   miss a binding that is being committed concurrently. A count failure
   blocks the deletion (retryable) instead of risking a shared tree's
   removal. Project deletion (`DeleteProject`) deliberately bypasses this
   protocol: it cascades session rows without releasing trees, leaving them
   adoptable orphans — the project-unscoped count keeps any cross-project
   co-owners safe. Everything else about deletion is unchanged: dirty/locked
   confirmations for the last owner, foreign trees never removed, branches
   never deleted, archiving retains the tree.

3. **Everything else about ADR-080 stands**: typed immutable bindings,
   identity `(worktree_name, branch)` with derived paths, the `SessionDraft`
   creation order, restore via the `WorkspaceEnsurer` (a no-op is now the
   common case for every session of a shared tree), managed forks still get
   their own fresh tree and derived branch, and the Git-panel focus model.

## Consequences

- Several sessions can execute in one managed worktree — concurrently, like
  local sessions in the checkout. The agents share a working copy; conflict
  semantics are the user's responsibility, exactly as for co-located local
  sessions.
- The one-session-per-tree DB enforcement is gone. `managedTreeOwner`
  (Git-panel worktree listing enrichment) reports one of the bound sessions.
- Orphaned trees (an app crash between provisioning and commit, a lost
  session row) are adopted instead of bricking the branch, and prunable
  metadata is repaired by the adoption path.
- A shared tree's data-loss surface shrinks: deleting one co-owner can never
  discard uncommitted work that other sessions still depend on; the dirty
  confirmation only gates the last owner's deletion.
- The unique index `idx_session_managed_workspace` becomes a plain index with
  the same name, columns, and predicate; existing databases are rewritten
  once at store open, fresh ones never carry the UNIQUE form.

## Alternatives Considered

- **Keep one-session-per-tree, only adopt unowned (orphaned) trees** —
  rejected: the second session on an actively used branch would still fail,
  which is the exact capability gap the user reported ("several sessions in
  one worktree" must behave like it already does for the main checkout).
- **Refuse shared branches with a clearer message only** — rejected: the
  error remains a dead end for the common case; the share decision was made
  explicitly with the owner.
- **Copy-on-write clones instead of sharing** — rejected: fork already covers
  "independent continuation"; sharing must alias the live tree, including its
  uncommitted state, not snapshot it.

## Related

- [ADR-080 Typed Session Workspace Bindings](./080-session-worktrees.md) —
  the binding model this ADR amends (one-session-per-tree rule replaced).
