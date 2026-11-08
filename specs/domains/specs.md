# Specs Subsystem

## Purpose

The specification subsystem gives every workspace a spec-lifecycle capability and a human surface for it: the vendored `spec-*` skillset (six Agent Skills adapted from the external vibespec skillset and then evolved independently) plus the `specs` Subagent Profile, both seeded globally at startup like the RESEARCH packs; and the Specs browser — a single sidebar panel (header with the integrity badge and aggregate counts, the quick-actions row, a nested section tree whose DECISIONS leaves carry git-derived dates, and the findings cascade — each card carrying a per-finding fix badge and a hover-revealed copy of its finding line, the header badge doubling as the aggregate fix-findings dispatch) whose every mutating gesture dispatches a `spec-*` skill into the chat. See [ADR-105](../decisions/105-vendor-spec-skillset.md), [ADR-106](../decisions/106-specs-sidebar-panel.md), [ADR-108](../decisions/108-specs-panel-usability-redesign.md), [ADR-109](../decisions/109-specs-panel-simplification.md), and [ADR-116](../decisions/116-specs-root-follows-active-session.md) (the session-following specs root).

## Key Files

- `core/specs/skillpack.go` - embedded six-skill `spec-*` pack (`core/specs/skills/`) and its versioned global seeding (`SeedSkills`; a thin consumer of the shared core below)
- `core/specs/unseed.go` - the delete half of the pack lifecycle behind the `specs.enabled` surface gate: `UnseedSkills`/`UnseedAgents` remove only byte-identical (or pack-marked truncated) copies, fail-soft per entry (a failure is recorded and joined into the error, never aborting the rest of the pack)
- `core/specs/skills/` - the vendored skill sources: six `spec-*/SKILL.md` bodies (`spec-init`, `spec-explore`, `spec-create`, `spec-update`, `spec-check`, `spec-consult`; tool-agnostic; `spec-init` carries `assets/meta-template.md` + `assets/adr-template.md`, `spec-explore` carries its `references/` bodies)
- `core/specs/agentpack.go` - embedded `specs` Subagent Profile (`core/specs/agents/specs/AGENT.md`) and its seeding (`SeedAgents`)
- `core/specs/LICENSE.md` - the upstream MIT attribution (vibespec); the origin record of the vendored tree
- `core/specs/model.go` - pure domain types of the parsed specs/ tree (`SpecsRoot`, `SpecFile`, `SpecsIndex`, `IndexTask`, `ADRMeta`, `Finding`); no I/O
- `core/specs/parse.go` - the tolerant read-only parser (`ParseSpecsRoot`) and the tier-1 integrity findings
- `internal/specslint/adr_numbering_test.go` - the guard over THIS repository's own `specs/decisions/` tree (`TestADRNumberingContract`): it fails the default Go suite on a file that is neither `NNN-slug.md` nor `_template.md`, on a malformed ADR file name, on a missing `_template.md`, on a first line that is not an `# ADR-NNN:` heading, on an H1 number disagreeing with the file name's, and on two files claiming one NNN
- `core/seedpack` - the shared crash-safe seeding core (content-hash classification, staged write + atomic rename, `.seed-version` marker handling, leftovers sweep) behind the seeding invariants below; `core/research`, `core/papers`, and `core/specs` are thin consumers (ADR-105 decision 5)
- `backend/frontend_api_research.go` - `seedGlobalPacks`: the single per-launch global pack reconcile that seeds the specs packs alongside research/papers while the `specs.enabled` surface gate is on and unseeds them while it is off, before the skill/agent watchers start
- `backend/frontend_api_packgates.go` - the `SetSpecsEnabled` surface-gate RPC (persist + seed/unseed in one beat; [ADR-115](../decisions/115-sidebar-pack-gates-reconcile-and-serialization.md))
- `backend/frontend_api_specs.go` - `GetSpecsStatus` RPC and the `SpecsStatusDTO`/`SpecsCountsDTO`/`ADRMetaDTO` wire shapes (each `adrs` element carries the optional git-derived `date`); `specsRootWithBoundary` — the session-following root resolver ([ADR-116](../decisions/116-specs-root-follows-active-session.md))
- `backend/frontend_api_specs_adrdates.go` - the fail-soft git-derived ADR-date enrichment (`specsADRDates`/`parseADRDatesLog`/`enrichSpecsADRDates`; one bounded `git log --diff-filter=A` per call)
- `backend/config/paths.go` - `ProjectSpecsPath` (`<workspace>/specs`) — the one path constructor for the specs root
- `frontend/src/api/specs.ts` - the RPC wrapper: boundary validation + normalization of the status payload
- `frontend/src/stores/specsStore.ts` - the browser's store: the status/fetch state and the persisted per-project tree expansion
- `frontend/src/components/specs/` - the sidebar panel (`SpecsPanel.tsx` orchestrator + in-panel `workspace:tree_changed` subscription; `SpecsHeader.tsx` with the integrity badge that doubles as the fix-findings dispatch; `SpecsSectionTree.tsx` nested tree with the DECISIONS date column; `SpecsFindings.tsx`; `SpecsEmptyState.tsx`; `SpecsQuickActions.tsx`), plus `specsActions.ts` (the auditable dispatch constants, `buildFixFindingsAction` included), `specsPaths.ts` (pure path/tree/date/label helpers), `useSpecsDispatch.ts` (the single `sendMessage` path)
- `frontend/src/components/layout/WorkspacePanel.tsx` - the host tab strip; the `specs` tab (after `research`) renders `SpecsPanel`
- `frontend/src/stores/uiStore.ts` - `WorkspaceTab` gains `'specs'`, registered in `WORKSPACE_TAB_VALUES` (per-project tab memory)

## Core Types

```go
type FindingKind string // PATH_MISSING | CROSS_REF_BROKEN | DUPLICATE_ADR_NUMBER | INDEX_STALE | ROOT_FILE_MISSING | ADR_NAME_MALFORMED | ADR_NUMBER_MISMATCH | SUPERSEDED_TARGET_MISSING | SUPERSEDED_CYCLE

type SpecFile struct {
    Path      string   // specs-root-relative, forward slashes; "INDEX.md", "domains/research.md"
    Section   string   // architecture | domains | contracts | decisions; "" for INDEX.md and META.md
    Title     string   // first ATX heading outside code blocks, verbatim
    KeyFiles  []string // backticked entries of "## Key Files", verbatim
    CrossRefs []string // relative markdown link targets (file path, optional #anchor, or pure #anchor)
}

type IndexTask struct {          // one "## Navigation by Task" row
    Task  string
    Specs []string               // the row's relative link targets, anchors included
}

type ADRMeta struct {            // one decisions/NNN-slug.md record
    Number, Slug, File           // from the file name (the template's authority)
    Title, Status, SupersededBy  // from the body; Status classifies the status line's first word
}

type Finding struct{ Kind FindingKind; File, Detail string }

type SpecsRoot struct {
    Path     string        // exactly as passed to ParseSpecsRoot
    Present  bool          // root existed and was a directory; a missing or non-directory root is the neutral state, never an error
    Files    []SpecFile    // sorted by path; "_"-prefixed files (templates) excluded
    Index    *SpecsIndex   // parsed INDEX.md; nil only when missing/unreadable
    ADRs     []ADRMeta     // sorted by number
    Findings []Finding     // tier-1 integrity findings, sorted by kind, file, detail
}
```

The wire shape wraps each parsed ADR in `ADRMetaDTO` (`backend/frontend_api_specs.go`) — the `ADRMeta` above embedded verbatim plus an optional `date` (ISO `YYYY-MM-DD`, the file's git add date; `omitempty` when unknown) that the backend derives after the pure parse (ADR-108 decision 3); `core/specs` itself stays date-free. The frontend turns that `date` map into the DECISIONS tree's date column (`adrDatesByFile`, ADR-109).

## Flow

```
App startup
  -> seedGlobalPacks seeds every c0wrk-owned pack into ~/.c0wrk/.agents:
     the six spec-* skills into .agents/skills/spec-*, the specs profile
     into .agents/agents/specs/ (content-hash classified, staged +
     atomically swapped, non-destructive) — BEFORE the skill/agent
     watchers; the skill/agent caches are invalidated afterwards

Specs tab mounted (a real project)
  -> SpecsPanel fetches GetSpecsStatus(projectId) — one light read-only
     parse of the active session's <workspace>/specs (a session worktree
     when one resolves, the project checkout as fallback — ADR-116); a
     session switch refetches
  -> specsStore applies the payload under its fetch ticket
     (last-write-wins by initiation order)

specs/ file changes (agent-written or human)
  -> workspace watcher emits workspace:tree_changed
  -> the panel's own subscription filters it to specs/ paths and refetches
     (fail-open when the payload carries no paths) — no dedicated event

Human gestures
  -> Open spec / Key File / finding file link -> fileViewerStore.openFile
     (the existing viewer)
  -> Header's findings badge ("N findings") -> ONE spec-update task whose
     prompt enumerates the current parse's findings verbatim
     (buildFixFindingsAction; Shift = new session); the "Integrity OK"
     chip is inert
  -> A finding card's fix badge -> ONE spec-update task scoped to that
     single finding (buildFixFindingAction — the aggregate's repair
     instructions shared verbatim, the finding quoted through
     findingLine; Shift = new session)
  -> A finding card's hover-revealed copy button -> the finding's
     findingLine on the clipboard via clipboardSetText — byte-identical
     to the bullet both fix prompts list (opacity-only reveal, the
     MessageFooter pattern — no layout shift)
  -> Header's refresh button (specs-refresh) -> the same GetSpecsStatus
     refetch as the mount/tree_changed paths — the store's LWW fetch
     ticket serializes overlapping flights and a same-project refetch
     keeps the previous status rendered; no new RPC, no event
  -> Init / Check / Consult / Explore -> sendMessage with the spec-* skill
     in the activeSkills slot and the constant prompt from
     specsActions.ts (Shift = new session)
```

## Invariants

- The vendored pack contains exactly six skills — `spec-init`, `spec-explore`, `spec-create`, `spec-update`, `spec-check`, `spec-consult` — and one Subagent Profile (`specs`); every `SKILL.md` frontmatter `name:` matches its directory name.
- The vendored tree is adapted once to c0wrk's layout and then evolved independently of upstream: there is no sync obligation, and the upstream MIT attribution stays in `core/specs/LICENSE.md`.
- Skill bodies stay tool-agnostic (no c0wrk tool names), so they remain portable; the profile's directive lives in `core/specs/agents/specs/AGENT.md`.
- `spec-init` emits the c0wrk tree shape (META.md / INDEX.md / architecture / domains / contracts / decisions + `_template.md`); `specs/WORKFLOW.md` is deliberately absent — `AGENTS.md` owns the "how to use the spec system" pointer role, and the vendored `assets/` carry only `meta-template.md` and `adr-template.md`.
- The `specs` profile is least-privilege by group tokens (`tools: local-read, local-write` — spec work reads and writes Markdown and executes nothing), and its `skills:` list (`spec-consult, spec-create, spec-update, spec-check`) resolves fail-closed against the skill catalog at launch.
- Seeding runs once per launch from `seedGlobalPacks` BEFORE the skill and agent directory watchers are created, is per-pack fail-soft (a failure is logged and never fatal to startup), and is idempotent — a repeat run re-seeds nothing (all destinations classify as `Current`). Each surface reconciles with its sidebar surface gate ([ADR-115](../decisions/115-sidebar-pack-gates-reconcile-and-serialization.md)): the specs packs seed only while `specs.enabled` is on, and a disabled surface's packs are UNSEEDED at the toggle and again at every launch — an idempotent reconcile that converges any drift a failed toggle-time removal or an interrupted run left behind, so a launch never resurrects them.
- Seeding classifies each destination by CONTENT HASH against the embedded pack (never mtime/size, never the marker alone): equal content is `Current` (a missing/stale `.seed-version` marker is re-stamped); a pack-marked truncated subset is repaired; a pack-marked diverging directory is a local edit — preserved and reported `Modified`; a marker-less diverging directory is user-owned and preserved byte-identically; a marker from an older pack version is overwritten in full.
- Seeding writes are crash-safe: each entry is staged in a hidden sibling temp directory and swapped in with a single rename; staging/backup leftovers from a hard kill are swept on the next run. The specs pack versions (`specs.CurrentSeedVersion`, `specs.AgentSeedVersion`) bump independently of the research/papers packs.
- The `specs.enabled` surface gate (pointer bool, default on) hides the Specs sidebar tab and drives the pack lifecycle in the same beat: `SetSpecsEnabled` persists first (a failed write rolls the in-memory value back), then seeds (enable) or unseeds (disable) the spec skill/agent packs under `configMu` → `researchSeedMu` — serialized against the launch seed, which re-reads the gate mirror the apply writes under the seed lock, so neither path can act on a stale gate. Unseeding is the non-destructive mirror of the seeding classification: only byte-identical copies (any marker state) and pack-marked truncated subsets are removed; user-owned and locally modified copies are preserved byte-identically, the modified ones named in a visible warning, and per-entry failures are fail-soft (never aborting the rest of the pack). The workspace tab strip follows the gate live, and a remembered specs tab falls back to the layout's primary tab — the indexing gate's contract.
- Skill/agent discovery precedence is project-local `<workspace>/.agents/{skills,agents}` → c0wrk global `~/.c0wrk/.agents/{skills,agents}` → user `~/.agents/{skills,agents}`: the seeded spec pack outranks a stale same-named user copy, and `ListSkills`/`ListAgents` expose it with no catalog change.
- The specs root is `<workspace>/specs` (`config.ProjectSpecsPath`) of the workspace the ACTIVE SESSION resolves to — a session worktree when one resolves, the project checkout as fallback ([ADR-116](../decisions/116-specs-root-follows-active-session.md); ADR-099's protocol) — and is read-only for the application: the panel and the parser never create, write, or delete anything under it — every mutation is dispatched to a `spec-*` skill.
- The parser is tolerant: a MISSING or non-directory `specs/` root parses to the neutral `Present=false` state; an existing EMPTY or PARTIAL tree parses `Present=true` with whatever content is readable — never an error in either case; files whose base name starts with `_` (templates) are excluded from the model; the parser keys off the documented `specs/` layout, not c0wrk-specific paths (any workspace with a `specs/` directory renders the panel).
- `GetSpecsStatus` requires a real project: the No-Project pseudo-project returns the neutral `present=false` DTO (the defensive contract — the workspace tab strip, and with it the panel, is not rendered in CHAT mode at all), and every other error path is a transport-level Go error. A workspace-less project and a session worktree without `specs/` resolve the same neutral state (the Init empty state renders, not an error — [ADR-116](../decisions/116-specs-root-follows-active-session.md)).
- Every DTO collection and the counts histogram are non-nil on the wire (empty `[]`/`{}`, never `null`); the frontend boundary re-validates the outer shape, drops malformed entries per-entry, and recomputes the counts from the normalized collections so the badges always agree with the rendered lists.
- Tier-1 integrity findings are computed by the parser on every call (`PATH_MISSING` — a Key Files path that does not exist; `CROSS_REF_BROKEN` — a relative link or anchor that does not resolve; `DUPLICATE_ADR_NUMBER` — two `decisions/NNN-*.md` sharing NNN; `INDEX_STALE` — a section file neither the INDEX.md directory listing nor the task navigation table names; `ROOT_FILE_MISSING` — INDEX.md or META.md absent from a tree holding at least one parsed file, a fully empty tree staying clean; `ADR_NAME_MALFORMED` — a decisions/ file whose base name is not `NNN-slug.md`; `ADR_NUMBER_MISMATCH` — a `# ADR-NNN:` heading number differing from the file name's, compared numerically; `SUPERSEDED_TARGET_MISSING` — a superseded ADR whose successor NNN no decisions/ file carries; `SUPERSEDED_CYCLE` — a supersession chain looping back to its own records, reported once on the lowest-path member) and are never persisted — the parse is cheap and always fresh. The last five checks are powered by the parser's ADR status classification (the `## Status` line's first word reduces to `accepted`/`superseded`; a superseded line's first successor reference — bracketed `[084]`/`[ADR-055]` or plain `ADR-055` — yields `superseded_by`, numeric comparison throughout) ([ADR-109](../decisions/109-specs-panel-simplification.md)).
- Tier-2 semantic drift (type/behavior/config mismatch, violated invariants — `spec-check`'s vocabulary) stays conversational: the panel surfaces it only through the "Check drift" quick action, which dispatches `spec-check` into the chat; no machine-readable check artifact exists in v1.
- This repository's own `specs/decisions/` tree carries a hard version of three tier-1 checks: `internal/specslint`'s `TestADRNumberingContract` runs in the default Go suite and fails on a file that is neither `NNN-slug.md` nor `_template.md`, on a missing `_template.md`, on a first line that is not an `# ADR-NNN:` heading, on an H1 number disagreeing with the file name's, and on one NNN claimed by two files (the `ADR_NAME_MALFORMED`/`ADR_NUMBER_MISMATCH`/`DUPLICATE_ADR_NUMBER` findings, enforced on c0wrk itself).
- The panel refreshes on mount (tab activation), on an active-session switch (both fetch effects re-key — the mount/project-switch effect refires and the `workspace:tree_changed` subscription resubscribes — [ADR-116](../decisions/116-specs-root-follows-active-session.md)), on `workspace:tree_changed` events scoped to `specs/` paths, and on the header's manual refresh button (`specs-refresh`, inert while a fetch is in flight, rendered in both header branches) — all through the same `specsStore` fetch funnel: a repeated `GetSpecsStatus` whose LWW fetch ticket serializes overlapping flights and whose same-project same-session refetch keeps the previous status rendered (no flicker); there is no dedicated `specs:*` event pair (research needed one only for its status/graph split) and no refresh-specific RPC; the subscription lives inside `SpecsPanel` (the single consumer of `specsStore`), and the filter is fail-open when the event payload carries no paths.
- Reads open through the existing file viewer (`fileViewerStore.openFile` + `setCollapsed(false)`); spec-relative links resolve lexically (`specsPaths.joinSpecPath` handles `./`, `#anchor`, and `../` escapes to absolute viewer paths).
- The sidebar panel is the only Specs browser surface ([ADR-109](../decisions/109-specs-panel-simplification.md)): the header carries the integrity badge and the aggregate counts in both the `present` and `!present` branches, and the body carries the quick-actions row, the nested section tree, and the findings cascade.
- ADR dates are derived from git history in the backend after the pure parse (one `git log --diff-filter=A -- specs/decisions` call, bounded by `gitCmdTimeout`, skipped when there are no ADRs) and are never written into the spec files; the derivation reads the workspace the root was derived from — the parsed tree's own HEAD, so a session worktree's dates come from the worktree, never the checkout ([ADR-116](../decisions/116-specs-root-follows-active-session.md)) — and is fail-soft: a non-git workspace, a missing git binary, an unborn HEAD, or any git error leaves every date absent while the RPC still succeeds (Debug-only logging). The sidebar tree renders each DECISIONS leaf's date in a fixed-width right-aligned column, and an undated record renders no column at all ([ADR-109](../decisions/109-specs-panel-simplification.md)).
- The header's findings badge is the aggregate fix gesture and every finding card carries the per-finding one ([ADR-109](../decisions/109-specs-panel-simplification.md)): any nonzero finding count renders the header badge a button that dispatches exactly one `spec-update` task whose prompt enumerates the current parse's findings verbatim (kind, file, detail), built at click time from the store so the dispatched task sees exactly what the panel renders, and each finding card's fix badge dispatches exactly one `spec-update` task scoped to that single finding — the same dispatch path, the same current-project guard, and the aggregate's repair instructions shared verbatim; all three surfaces (the two prompts and the card's copy button) render their finding text through the single `specsActions.findingLine` (`- [kind] file: detail`), so they cannot drift apart; zero findings renders the inert "Integrity OK" chip.
- The findings cascade's copy gesture writes the card's `findingLine` verbatim through the chat's native clipboard contract — `clipboardSetText` (the Wails runtime binding), never `navigator.clipboard` (unreliable inside the webview) — so the clipboard byte-matches the dispatched fix prompt's bullet; the button rides the card's hover `group` (and keyboard focus) as an opacity-only reveal that always occupies its layout slot, so revealing it shifts nothing.
- The sidebar tree starts fully collapsed; its per-project expansion state (`treeExpandedNodes`, keyed projectId → node id) is the only persisted `specsStore` slice (zustand `persist` key `c0wrk-specs-view`), so expansion survives tab switches and app restarts while each project keeps its own set.
- Every mutating quick action dispatches exactly one `spec-*` skill through `sendMessage`'s `activeSkills` slot with a constant prompt from `specsActions.ts` (`Init` — the empty state's CTA shares the `INITIALIZE_ACTION` constant verbatim — is always enabled; `Check`/`Consult`/`Explore` require a present tree; the narrow strip renders the short labels, with each constant's `fullLabel` spelling the gesture out in the tooltip, [ADR-109](../decisions/109-specs-panel-simplification.md)); a failed dispatch surfaces on `specsStore`'s error line (the panel keeps the previous status rendered), and Shift dispatches into a new session.
- `specsStore` fetches are last-write-wins by initiation order (a monotonic ticket discards superseded responses) and are stamped with the (project, session) pair read at fetch start — a fetch for a new session of the same project clears the previous status exactly like a project switch does ([ADR-116](../decisions/116-specs-root-follows-active-session.md)); a failed fetch keeps the previous status and records the error.

## Configuration

| Parameter | Default | Description |
| --------- | ------- | ----------- |
| Specs root | `<workspace>/specs` | Session-following root (`config.ProjectSpecsPath`): the active session's workspace when one resolves, the project checkout as fallback ([ADR-116](../decisions/116-specs-root-follows-active-session.md)); read-only, never persisted, no override |
| Surface gate | `specs.enabled: true` | The Settings → General master toggle ([ADR-115](../decisions/115-sidebar-pack-gates-reconcile-and-serialization.md)): off hides the Specs sidebar tab and unseeds the spec skill/agent packs; off also unseeds them at the launch seed (idempotent reconcile) |
| Global seed dir | `~/.c0wrk/.agents/` (`config.SkillsDir`/`config.AgentsDir` on the agent dir) | Destination of the startup `seedGlobalPacks` run; outranks `~/.agents` in discovery |
| Skill-pack seed version | `2` (`specs.CurrentSeedVersion`) | Stamped into each seeded skill's `.seed-version`; bump to refresh pack-marked copies |
| Agent-pack seed version | `2` (`specs.AgentSeedVersion`) | The `specs` profile's independent version |

The specs root, parser, and RPCs are always on: there is no experimental gate and no per-project toggle (the panel's availability follows the workspace having a `specs/` tree; the seed follows the RESEARCH always-on precedent, [ADR-056](../decisions/056-research-always-on-global-seeding.md)). The single operator switch is the sidebar surface gate `specs.enabled` ([ADR-115](../decisions/115-sidebar-pack-gates-reconcile-and-serialization.md), default on), which hides the Specs sidebar tab and unseeds the globally-seeded spec packs — the root, the parser, and `GetSpecsStatus` stay untouched.

## Extension Points

- Update the vendored skills by changing `core/specs/skills/` (and `core/specs/agents/specs/AGENT.md`) and incrementing `CurrentSeedVersion`/`AgentSeedVersion` when existing pack-marked copies must refresh; evaluate upstream vibespec changes by hand — never pull them.
- Add a tier-1 finding kind in `core/specs` (a new `FindingKind` + its check in `parse.go`), then mirror it in `frontend/src/types/models.ts` (`SpecFindingKind`) and `specsPaths.findingKindLabel` (an unknown kind passes through the boundary with a generic label, so the frontend degrades instead of dropping).
- Extend the parsed model (a new `SpecFile`/`ADRMeta` field) by extending the pure parser, the DTO, the frontend boundary validation, and the panel together — the DTO contract is additive-only for the same hand-written-guard reasons as the other boundaries ([../contracts/desktop-frontend.md](../contracts/desktop-frontend.md) § Specs). A view-only enrichment the parse model must not carry (e.g. the git-derived ADR `date`, ADR-108 decision 3) lives in the backend DTO layer alone, keeping `core/specs` pure.
- Add a quick action in `specsActions.ts` (one constant: short `label` + optional `fullLabel` + the `spec-*` skill + the prompt); the dispatch path (`useSpecsDispatch`) and the row render it without further wiring.
- Change the panel's refresh model only with matching updates to this document and the desktop-frontend contract; a dedicated `specs:*` event requires superseding [ADR-106](../decisions/106-specs-sidebar-panel.md) decision 3.

## Related Specs

- [ADR-105](../decisions/105-vendor-spec-skillset.md) - the vendoring, the c0wrk adaptation, the independent-evolution policy, and the `core/seedpack` consolidation.
- [ADR-106](../decisions/106-specs-sidebar-panel.md) - the panel's six design decisions (placement, data source, refresh, interaction split, two-tier drift, scope boundary).
- [ADR-108](../decisions/108-specs-panel-usability-redesign.md) - the usability redesign: the fail-soft git-derived ADR dates, the sidebar recomposition (nested tree + findings) and the persisted per-project tree expansion (the overview page and the Open-overview opener it also introduced are superseded by [ADR-109](../decisions/109-specs-panel-simplification.md)).
- [ADR-109](../decisions/109-specs-panel-simplification.md) - the simplification: the overview page removed (the sidebar panel is the Specs browser again), the findings badge as the one-click `spec-update` fix dispatch, short quick-action labels with full-name tooltips, the DECISIONS date column, and the tier-1 net widened to nine checks.
- [ADR-115](../decisions/115-sidebar-pack-gates-reconcile-and-serialization.md) - the `specs.enabled` surface gate: hides the sidebar tab and unseeds/reseeds the spec packs (the delete half of the pack lifecycle; amends ADR-105's always-seeded invariant).
- [ADR-116](../decisions/116-specs-root-follows-active-session.md) - the specs root follows the active session: the session-worktree root resolution with the checkout fallback, the shared parse/date boundary, and the frontend (project, session) pair contract — amends ADR-106's root clause by reference.
- [../contracts/desktop-frontend.md](../contracts/desktop-frontend.md) - the `GetSpecsStatus` RPC surface and wire shapes (§ Specs).
- [research.md](research.md) - the global pack-seeding precedent this pack inherits (ADR-056) and the closest panel precedent.
- [frontend/stores.md](frontend/stores.md) - `specsStore` and the `WorkspaceTab` extension in `uiStore`.
