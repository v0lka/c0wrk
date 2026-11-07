# ADR-116: Specs root follows the active session

## Status

Accepted. Amends [ADR-106](./106-specs-sidebar-panel.md) by reference — decision 2's "unconditionally `<workspace>/specs`" root now resolves through the ACTIVE SESSION's workspace with the project checkout as fallback; the wire shape, the read-only rule, the tolerant-parser contract and every other ADR-106 decision stand unchanged.

## Context

The Specs panel had the same split brain in worktree sessions that ADR-099 fixed for research — one root later in the pipeline, and on the repair side of it. Every mutating gesture of the panel (the findings badge, each finding card's fix badge, the quick actions) dispatches a `spec-*` skill into the chat, and the dispatched task executes in the ACTIVE session — whose execution workspace in a worktree session is the managed tree ([ADR-080](./080-session-worktrees.md)), so the agent edits `<tree>/specs/...`. The panel-side reader, however, resolved the root from the PROJECT row: `GetSpecsStatus` derived `<proj.WorkspacePath>/specs`. The net effect: the fix-findings dispatch loop repaired the tree the session owns while the panel kept scanning the checkout's copy — findings reported from the checkout were never cleared by repairs written into the session tree, and the dispatch could re-run forever on a stale scan. The ADR add-date enrichment ([ADR-108](./108-specs-panel-usability-redesign.md) decision 3) had the same split: it read `git log` at the checkout boundary even when the parse had read a different tree, so a worktree's DECISIONS dates came from the checkout's HEAD.

[ADR-099](./099-research-root-follows-active-session.md) already established the pattern and its seam: a panel root follows the ACTIVE SESSION through `activeSessionWorkspace` (the user-selected session when a usable selection is persisted, else the project's most recently active non-archived session), falls back to the project checkout when no session workspace resolves, and validates containment against the workspace the root was derived from (`researchRootWithBoundary`). A managed tree legitimately lives OUTSIDE the project checkout, which is exactly why the boundary travels with the root.

## Decision

One protocol, documented once on the resolver declaration
(`FrontendAPI.specsRootWithBoundary` in `backend/frontend_api_specs.go`, a
structural mirror of `researchRootWithBoundary`):
**the specs root follows the ACTIVE SESSION.**

- The effective specs root of a project is
  `<workspace-of-the-active-session>/specs`, resolved through the same
  `activeSessionWorkspace` seam ADR-099 documented, with the project
  checkout (`<proj.WorkspacePath>/specs`) as the fallback when no session
  workspace resolves: a fresh project without sessions, a manager/store
  unavailability, or a foreign (other-project) session — a foreign session
  never steers the root. A local session's workspace IS the checkout, so its
  resolution and the fallback agree by construction. `nil`, No Project, or a
  workspace-less project yields `("", "")` — the neutral `present=false`
  state via `ParseSpecsRoot`'s missing-root branch.
- `GetSpecsStatus` uses the resolution for BOTH consumers: the parse root
  (`specs.ParseSpecsRoot(specsRoot)`) and the ADR add-date enrichment —
  `enrichSpecsADRDates` receives the BOUNDARY workspace, so the
  `git log --diff-filter=A` call reads the same tree that was parsed and a
  session worktree's DECISIONS dates come from the worktree's own HEAD,
  never the checkout's.
- No RPC signature change: `GetSpecsStatus(projectID)` keeps its wire shape,
  and the No-Project neutral state, the guards and the tolerant-parser
  contract are untouched. A session worktree without `specs/` parses
  `present=false` — the documented neutral state, not an error.
- The frontend follows the session the way it already follows the project:
  `SpecsPanel` adds the active session id to both fetch effects (the
  mount/project-switch effect and the `workspace:tree_changed` subscription,
  which re-keys and resubscribes on a session switch), and `specsStore`
  stamps every fetch with the (project, session) pair read at fetch start —
  the clean-slate rule widens from project to the PAIR, so a fetch for a new
  session of the same project clears the previous status synchronously at
  start; the `fetchSeq` last-write-wins ticket is untouched.
- Call sites carry only deltas; the protocol lives on the resolver
  declaration alone (no per-caller restatements).

## Consequences

- The fix-findings loop closes: a `spec-update` dispatched from the panel
  repairs the tree the panel reads, so re-parsed findings actually clear,
  and the panel can no longer report findings from a tree the dispatch loop
  never writes.
- Root changes are session-driven like research (ADR-099): switching the
  active session moves the panel's root. The store's pair clean-slate rule
  plus the LWW ticket keep a mid-flight response from the departed session's
  tree from rendering.
- A worktree that legitimately lacks `specs/` (a session tree created before
  `spec-init` ran, or a project without the tree) renders the panel's
  neutral state with the Init CTA — the empty state, not an error.
- DECISIONS dates now follow the parsed tree; a worktree whose HEAD predates
  an ADR file shows that record undated (fail-soft absence), matching the
  tree the user is reading.
- Reads pay the bounded session-manager/store lookups ADR-099 already priced
  (one `activeSessionWorkspace` read per RPC); paths that already paid a
  project load pay one extra bounded read.
- Specs: `specs/domains/specs.md`, `specs/contracts/desktop-frontend.md`
  (§ Specs) and `specs/domains/frontend/stores.md` carry the protocol; the
  session-worktrees domain's user-facing-roots list picks it up through the
  INDEX.

## Alternatives Considered

- **Pass the session ID from the frontend** on `GetSpecsStatus` — rejected:
  changes the wire surface for information the backend can derive, and the
  panel is project-scoped by design (same rejection as ADR-099).
- **Teach the fix-findings dispatch to patch the checkout instead** —
  rejected: it would redirect the dispatched task away from the session's
  own execution workspace (the agent edits a tree it is not bound to), and
  two concurrently running sessions of one project would race each other's
  repairs in the shared checkout — the session-local workspace is
  [ADR-080](./080-session-worktrees.md)'s isolation principle.
- **Parse both trees and merge findings** — rejected: a merged view reports
  a repair done in one tree against the other's stale copies — the exact bug
  this ADR fixes, resurrected by the merge; it also doubles the parse and
  git cost, and findings attribution across trees is ambiguous.
