# ADR-105: Specs sidebar panel

## Status

Accepted

## Context

Issue [#69](https://github.com/v0lka/c0wrk/issues/69): the vendored `spec-*` skills (ADR-104) let an _agent_ drive the spec lifecycle, but there is no _human_ surface for the specification system. Today `specs/` is browsable only through the generic file tree / file viewer — no task→spec navigation, no ADR timeline, no drift signal.

The sidebar host pattern is established: `frontend/src/components/layout/WorkspacePanel.tsx` renders a `SegmentedControl` tab strip (`explorer` | `git` | `semantics` | `research`) typed by `WorkspaceTab` in `frontend/src/stores/uiStore.ts`, with per-project tab memory and a fail-closed persisted-value whitelist. The Research panel is the closest precedent (segmented inner views, `researchStore`, RPC wrappers in `frontend/src/api/research.ts`, an App-root event bridge, quick actions that dispatch `research-*` skills into the chat).

Six research questions had to be answered: information architecture, drift status, placement, interaction split, data source, and scope boundary.

## Decision

**1. Placement: a new `specs` tab in `WorkspacePanel`, after `research`.** The tab is always rendered for real projects (empty state included); in No-Project (CHAT) the workspace tab strip is not rendered at all and the panel issues no RPC — exactly the Research panel's No-Project contract. `WorkspaceTab` gains `'specs'`, registered in `WORKSPACE_TAB_VALUES` so the persist merge validates it entry-by-entry (no persist-version migration needed: unknown values are dropped, new values pass).

**2. Data source: a backend parser in `core/specs` + one RPC.** `ParseSpecsRoot(<workspace>/specs)` builds the model — files grouped by section (`architecture/`, `domains/`, `contracts/`, `decisions/`), the `INDEX.md` task→spec navigation table, ADR metadata (number, slug, title, `Status`, supersession links), each spec's Key Files, and relative cross-references. `backend/frontend_api_specs.go` exposes `GetSpecsStatus(projectID) → SpecsStatusDTO {present, root, files, index_tasks, adrs, findings, counts}` (snake_case wire shapes, non-nil slices/maps). The parser tolerates a missing or partial `specs/` (neutral `present: false`, never an error), mirroring `ParseResearchRoot`. The frontend stays thin: `frontend/src/api/specs.ts` + `stores/specsStore.ts` — no Markdown parsing in TypeScript, because the same parse also feeds the deterministic integrity findings (decision 5), which need a Go-side model anyway.

**3. Refresh: mount + `workspace:tree_changed`, no new event.** The workspace watcher already fires `workspace:tree_changed` for edits under `specs/` (agent-written specs land as ordinary file writes). The panel refetches `GetSpecsStatus` on mount and on tree changes that touch `specs/` paths. A dedicated `specs:changed`/`specs:file_changed` event pair was rejected: research needed one because it splits a heavy full status from a light graph fetch — the Specs surface has a single light RPC. The store subscription lives inside the panel (single consumer), not in an App-root bridge — research's root bridge exists because two surfaces (sidebar panel + workspace tab) share its store.

**4. Interaction: reads are human-triggered, everything mutating is dispatched to the skills.** Opening a spec or a Key File goes through the existing file viewer (`fileViewerStore.openFile`) — no new file-read RPC. Lifecycle actions (Initialize, Check drift, Consult, Explore) are quick actions that activate a `spec-*` skill via `sendMessage`'s `activeSkills` slot with constant prompts, centralized in an auditable `specsActions.ts` module (the `researchActions.ts` pattern). The panel never writes to `specs/` itself.

**5. Drift status is two-tier.** Tier 1 — **deterministic static findings** computed by the parser on every `GetSpecsStatus`: `PATH_MISSING` (a Key Files path does not exist), `CROSS_REF_BROKEN` (a relative spec link or anchor does not resolve), `DUPLICATE_ADR_NUMBER` (two `decisions/NNN-*.md` share a leading NNN), `INDEX_STALE` (a spec file not listed in `INDEX.md`'s directory listing). Not persisted — the parse is cheap and always fresh. Tier 2 — **semantic drift** (`TYPE_MISMATCH`, `BEHAVIOR_DRIFT`, `CONFIG_DRIFT`, `INVARIANT_VIOLATED` in `spec-check`'s vocabulary) is not machine-checkable without a model; the panel surfaces it through the "Check drift" quick action, which dispatches `spec-check` into the chat. No machine-readable `spec-check` run artifact is introduced in v1 (there is no producer today; defining a report format would be premature).

**6. Scope boundary: generic viewer, c0wrk dogfooding.** The parser keys off the documented `specs/` layout, not c0wrk-specific paths; any workspace with a `specs/` directory renders the panel. c0wrk's own tree is the primary consumer and the test fixture.

## Consequences

- The spec system gains a human entry point: navigation, ADR history, and a cheap always-fresh integrity signal, without a new event channel or persistence format.
- `GetSpecsStatus` parses the whole `specs/` tree per call; on c0wrk's own tree (~150 files) this is a lightweight read, and calls happen only on mount/tab-activity/tree-changes touching `specs/`.
- The four static finding categories deliberately under-approximate `spec-check`: semantic drift stays conversational until a persisted report format exists.
- The panel adds a fifth workspace tab; the segmented control's `@min-[280px]` label-hiding behavior keeps the strip usable at narrow sidebar widths.

## Alternatives Considered

- **Standalone sidebar panel (outside the tab strip).** Rejected: `WorkspacePanel` is the established host for workspace-scoped surfaces; a parallel host would fork the tab-memory and ARIA patterns.
- **Client-side parsing (thin backend or none).** Rejected: it would duplicate the Markdown/table parsing in TypeScript and could not host the Go-side integrity findings; the backend parser serves both the DTO and the checks from one model.
- **Dedicated `specs:changed` event + scoped watcher.** Rejected: `workspace:tree_changed` already covers `specs/` writes; research's dedicated events exist for its status/graph split, which does not apply here.
- **Persisted `spec-check` report artifact (e.g. `specs/.check-report.json`) displayed by the panel.** Rejected for v1: no producer exists, the interactive per-finding resolution flow belongs to the skill, and a format frozen now would constrain the skill's evolution.
- **Display only the INDEX table (no tree/ADR/findings).** Rejected: INDEX alone is a flat table; the ADR timeline and integrity signal are the two facets a file tree cannot give.

## Related

- [ADR-104](./104-vendor-spec-skillset.md) — the vendored `spec-*` skills the panel dispatches.
- `specs/domains/specs.md` — the panel, the RPC surface, and the refresh model (created with the implementation sync; not yet present when this ADR was accepted).
- [../contracts/desktop-frontend.md](../contracts/desktop-frontend.md) — the RPC contract; its `### Specs` section is added with the implementation sync.
