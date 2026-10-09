# ADR-080: Typed Session Workspace Bindings

## Status

Accepted (amended by [ADR-082](./082-shared-managed-worktrees.md): the
one-session-per-managed-tree rule — the partial unique index — is replaced by
shared trees with reference-counted release; every other decision stands)

## Context

A session's execution workspace was implicit: `Session.WorkspacePath` was set
from the active project's checkout at creation and never persisted, while the
Git panel, file tree, and diffs all implicitly targeted that same path. The
roadmap decision to give CODE sessions optional per-session git worktrees
(one tree per session, under `<repo>/.worktrees/<name>`, branch pinned at
selection) requires the database and every runtime consumer to know *which*
workspace a session executes in — independently of which workspace the Git
panel is focused on. Without a persisted, typed identity:

- restore cannot rebind a lazily restored session to its tree (the path is
  re-derived from the project checkout, silently retargeting execution),
- fork could copy a session that shares a managed tree with its source,
- nothing distinguishes "project checkout" from "session-owned tree", so the
  Git panel would follow execution focus with no way to express a different
  target.

CHAT (No Project) sessions already have per-session workspaces under
`~/.c0wrk/projects/__no_project__/` and must stay unchanged.

## Decision

1. **Typed, persisted, immutable binding.** `session.WorkspaceBinding`
   (`backend/session/workspace_binding.go`) is a nullable JSON value stored in
   `sessions.workspace_binding`:

   - `local` — the project checkout. `WorkspacePath` is *derived* at
     validation time from the project's current registered root (never
     trusted from the stored copy), so a moved checkout does not strand
     sessions; `worktree_name`/`branch` must be empty.
   - `managed_worktree` — a session-owned tree. Identity is
     (`worktree_name`, `branch`); the execution path is *derived* as
     `config.ManagedWorktreePath(repoRoot, name)` → `<repo>/.worktrees/<name>`.
   - `nil` — CHAT sessions. A non-nil binding on `__no_project__` is invalid.

   The binding is immutable after creation: `SaveSession` rejects any change
   to `project_id`, `kind`, `worktree_name`, or `branch` for an existing row
   (the upsert's `WHERE` compares exactly those identity fields; a changed
   `workspace_path` is re-derived to canonical form, not treated as mutation).
   A unique partial index enforces one session per managed tree.

2. **Path derivation is the containment gate.** `ManagedWorktreePath`
   (`backend/config/paths.go`) validates the name (single portable component,
   no traversal, no Windows device names) and refuses a symlinked or
   non-directory container/tree, and the result must be inside the repo root
   (`config.IsWithinPath`). No caller constructs `.worktrees` paths inline.

3. **Migration.** `migrateWorkspaceBindings` adds the column and backfills
   existing CODE rows with `local` bindings in one transaction; CHAT rows stay
   empty (nil). Existing messages, tasks, terminal history, and flags are
   untouched; the migration is idempotent.

4. **Separated contexts.** `SessionContexts` / `ResolveSessionContexts`
   (`backend/session/workspace_context.go`) model three distinct values:
   project identity (`ProjectContext`), immutable execution workspace, and
   Git-panel target (`GitPanelTarget`). A Git target may be the checkout or a
   managed tree of the *same* project only; it can never retarget execution.

5. **Creation via draft, fork via prepared binding.**
   `Manager.CreateSessionFromDraft` commits a `SessionDraft` (identity
   reserved with `NewSessionDraft`, before any orchestrator/log/file is
   created) so the Git lifecycle owner can provision the tree first. Forking a
   managed session requires `ForkSessionWithBinding` with a *new* tree name
   and a derived branch; the plain `ForkSession` refuses managed sources.
   Local forks keep the local binding. Git worktree provisioning/removal
   itself is out of scope for this ADR (subsequent roadmap steps).

## Consequences

- Session rows are self-describing: restore, `WorkspacePathFor`, list, and
  fork all resolve the execution workspace from persisted identity instead of
  ambient active-project state.
- Stored absolute paths are advisory only; derivation makes tampering with
  `workspace_path` ineffective (the derived path is what executes) and makes
  project moves transparent for `local` sessions.
- `SessionInfo` (returned verbatim by the frontend API) gains
  `workspace_binding`, so the UI can show the session's tree/branch; CHAT
  omits the field (`omitempty`).
- Tests that construct `SessionInfo` fixtures must register the workspace they
  pass to `CreateSession` as the project's `workspace_path` (production always
  does this via the active project path).
- Managed-tree creation/deletion/restore (git `worktree add/remove`, dirty
  confirmation, uncommitted-loss warning) are wired into the session
  lifecycle in `backend/frontend_api_worktrees.go` (this ADR originally
  landed only the identity, storage, and context foundation; the lifecycle
  consumption followed): `CreateManagedSession` (draft → provision →
  commit → persist, compensated on failure), the restore `WorkspaceEnsurer`
  (recreate-from-pinned-branch with the explicit data-loss warning; never a
  silent local fallback), managed fork at the source's committed HEAD, and
  the deletion protocol (join task/terminal → recheck-dirty release with
  `SessionDeleteBlockedError`; deletion never resurrects; branches never
  deleted; archive retains the tree).

## Alternatives Considered

- **Reuse `WorkspacePath` without persistence** — rejected: restore and fork
  cannot know the tree; the Git panel cannot diverge from execution focus.
- **Store only an absolute path** — rejected: breaks on project moves and
  invites path-traversal tampering; identity (name, branch) plus derivation is
  safer and portable.
- **Separate join table `session_workspaces`** — rejected for now: one
  nullable column with a partial unique index expresses "at most one tree per
  session" without a join on the hot session-list path.
- **Make the binding mutable via a setter** — rejected: the roadmap fixes
  tree selection at creation time; mutability reintroduces the retarget races
  the separation of contexts exists to prevent.
