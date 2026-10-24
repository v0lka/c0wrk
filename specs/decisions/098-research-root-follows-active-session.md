# ADR-098: Research/papers root follows the active session

## Status

Accepted. The "root the watcher at the session tree" alternative below was
superseded by the watcher-root change (the CODE watcher root follows the
active session, with per-root consumers routed at the watched root — see
`specs/domains/workspace.md`, Filesystem Watcher); the RPC/papers protocol
here is unchanged.

## Context

RESEARCH artifacts have a split brain in worktree sessions. The agent writes
research artifacts under the SESSION's execution workspace —
`session/Manager.researchProjectInfo` resolves the root from the session's own
`WorkspacePath`, so a managed-worktree session writes
`<tree>/.research/...` (pinned by
`backend/session/manager_research_root_test.go`). The panel-side readers,
however, resolved the root from the PROJECT row:
`GetResearchStatus`/`GetResearchGraph`/`GetResearchNextStep` and the mutation
RPCs derived `<proj.WorkspacePath>/.research`, and the papers library
(`effectiveResearchRoot`, papers/comparisons paths) did the same. The workspace
watcher only recursively watched the checkout's `.research` and matched
emissions against the checkout root. The net effect: research an agent
produces inside a worktree session was invisible to the Research panel and the
Papers surfaces, and never emitted `research:file_changed` / `papers:changed`.

Two earlier mechanisms already established the pattern this decision follows:

- `GetSessionWorkspace` resolves a session's workspace for the explorer with
  project-ID membership (store row, resident in-memory fallback) and falls
  back to the active project workspace.
- The git-panel focus and the vector-index focus follow the session's tree
  (ADR-080's `GitPanelTarget`).

A managed tree legitimately lives OUTSIDE the project checkout (the default
agent-dir container), which also means checkout-relative containment checks
and the checkout-rooted watcher's under-root gate reject session-tree paths.

## Decision

One protocol, documented once on the resolver declaration
(`FrontendAPI.effectiveResearchRoot` in `backend/frontend_api_papers.go`):
**the research/papers root follows the ACTIVE SESSION.**

- The effective research root of a project is
  `<workspace-of-the-active-session>/.research`, where the active session is
  the project's most recently active non-archived session
  (`ListSessionsByProjectCtx` ordering), resolved through the READ-ONLY
  `Manager.WorkspacePathFor` lookup with project-ID membership decided exactly
  as `GetSessionWorkspace` decides it (`activeSessionWorkspace`: store row
  `ProjectID`, resident in-memory fallback; both reads share one
  `terminalPathLookupTimeout`-bounded context).
- Fallback to the project checkout (`<proj.WorkspacePath>/.research`) when no
  session workspace resolves: a fresh project without sessions, a
  manager/store unavailability, or a foreign (other-project) session — a
  foreign session never steers the root. A local session's workspace IS the
  checkout, so its resolution and the fallback agree by construction.
- Containment for the root and the papers/comparisons subdirectories is
  validated against the workspace the root was derived from
  (`researchRootWithBoundary`): the session workspace when the session
  resolution won (a managed tree is outside the checkout), the checkout on
  the fallback path. The `IsWithinPath` checks stay — only the boundary
  changed.
- The workspace watcher follows: `switchProjectSetupWatcher` recursively
  watches the checkout's `.research` (unchanged) AND the active session's
  research root when it differs; the CODE-mode `WatchDirectory` call — the
  frontend re-roots the file tree at the session workspace on every session
  switch — re-points the tracked emit roots
  (`activeResearchRoot`/`activePapersRoot`/`activeComparisonsRoot`) at the
  session's roots, so `research:file_changed` and `papers:changed` keep
  flowing for the session the user switched to. A session tree outside the
  watcher's checkout root is admitted through the watcher's new `AllowRoot`
  primitive (idempotent extra admitted roots for the under-root gate;
  fsnotify itself has no such constraint). [Superseded mechanism, same
  guarantee: the watcher ROOT now follows the active session — `WatchDirectory`
  re-scopes it and moves the tracked emit roots in one step; the dedicated
  re-pointing helper is gone. See `specs/domains/workspace.md`, Filesystem
  Watcher.]
- Call sites carry only deltas; the protocol lives on the resolver
  declaration alone (no per-caller restatements, no caller enumerations).

## Consequences

- Research produced in a worktree session is visible in the Research panel
  and the Papers/Compare surfaces immediately; the incremental
  `research:file_changed` path works for session trees.
- Root changes are now session-driven: switching the active session (by
  activity) moves the panel's root. The existing
  `errResearchRootChanged` guards naturally cover this — a root that moved
  between resolution and the mutation's re-check fails the RPC and the
  frontend refreshes.
- `core/workspace.Watcher` grows `AllowRoot` — a small policy extension; no
  watcher instance or callback shape changed, and the checkout's research
  tree stays watched for tree-change annotation.
- Every resolver call performs bounded session-manager/store reads (list +
  point reads under one 10 s bound). Paths that already paid for a project
  load pay one extra bounded read; switch-path callers resolve the roots
  before taking `activeProjectMu` so watcher callbacks never wait on I/O.
- Specs: session-worktrees.md documents the protocol; research/papers specs
  pick it up through the INDEX.

## Alternatives Considered

- **Pass the session ID from the frontend** on every research/papers RPC —
  rejected: changes the wire surface of eight RPCs for information the
  backend can derive, and the panel is project-scoped by design.
- **Root the watcher at the session tree** (re-scope the whole CODE watcher
  on session switch) — rejected HERE because the watcher callback also fed
  git-cache invalidation, skill/agent caches, ignore-cache eviction, and the
  vector manager keyed on the checkout. SUPERSEDED since: the whole watcher
  was re-rooted at the session workspace anyway, with every per-root consumer
  routed at the WATCHED root instead of the checkout — the concern cited
  above resolved by routing, not by keeping the checkout root (see
  `specs/domains/workspace.md`, Filesystem Watcher).
- **A second, session-scoped watcher instance** — rejected: duplicates the
  lifecycle (create/teardown per switch) and the callback wiring; the extra
  admitted root on the existing watcher is one idempotent call.
- **Write research to the checkout from worktree sessions** — rejected: two
  concurrently running sessions of one project would share (and overwrite)
  research state; the session-local root is deliberate agent-side behavior
  (pinned by test) and ADR-080's isolation principle.
