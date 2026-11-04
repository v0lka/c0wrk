# ADR-108: Specs panel usability redesign

## Status

Accepted → Partially superseded by [ADR-109](./109-specs-panel-simplification.md) (decisions 1 and 2 — the `c0wrk:specs` overview page and its INDEX navigation — and decision 4's Open-overview opener are removed; decision 3's git-derived ADR dates stand, consumed by the sidebar DECISIONS tree instead of the overview timeline, and decision 4's nested-tree + findings body and decision 5's persisted per-project expansion stand unchanged)

## Context

[ADR-106](./106-specs-sidebar-panel.md) shipped the Specs sidebar panel: one narrow `WorkspacePanel` tab rendering the parsed tree as a flat section table (`SpecsIndexTable`) and an ADR timeline (`SpecsADRTimeline`) stacked above quick actions. Real use surfaced three usability gaps:

1. **The sidebar is narrow.** The INDEX task→spec table and the ADR timeline truncate hard at 180–500px; both need viewer width to be readable.
2. **ADR history has no dates.** The timeline orders by ADR number only; recency (which decision is newest, how stale the set is) is invisible, and the information exists for free in git history.
3. **The flat section list does not scale or search.** Deep `domains/frontend/`-style trees render as path strings, and the only way to find a spec is to read every row.

The codebase already has an established pattern for "full-width interactive workspace page": the file viewer's virtual pseudo-path tabs (`RESEARCH_TAB_PATH` = `c0wrk:research`, the papers library's `c0wrk:paper:<slug>` prefix), which render before the file-data lookup and never hit the read RPCs.

## Decision

**1. The overview page is a viewer pseudo-path (`c0wrk:specs`), not a panel view.** A full-width interactive Specs overview page (`SpecsWorkspace.tsx`) renders in the file viewer on the synthetic pseudo-path `c0wrk:specs` (`SPECS_TAB_PATH` in `frontend/src/stores/specsStore.ts`, paralleling `RESEARCH_TAB_PATH`). It opens only through the guarded store action `openSpecsOverview()` — click-time guards: an active REAL project (No-Project is a no-op), the store parse stamped to THAT project, `status.present` — then `setCollapsed(false)` + `openVirtualFile(SPECS_TAB_PATH)`; an already-open tab re-activates instead of duplicating. The tab is VIRTUAL: `FileViewerContent` routes the pseudo-path before the file-data lookup, so the loader never `readFile`s it (on activation or on `workspace:tree_changed` refresh); the tab bar renders a dedicated BookOpen icon and the label "Specs overview" without a `getFileIcon` RPC; and the viewer's persisted snapshot excludes the pseudo-path — it never restores across an app restart (test-pinned in `specsStore.test`).

**2. The overview page owns the INDEX navigation — section-grouped toggle + client-side FTS.** The Navigation section renders the INDEX rows (`SpecsOverviewIndex`/`SpecsOverviewIndexRow`): flat document order by default, grouped by section when the toolbar's "By sections" toggle is on (`overviewSortSections`: `'appearance'` default | `'alpha'`; canonical group order, root files group first). Group collapse lives in `overviewCollapsedGroups` keyed by the raw section (`''` = root files group) and is SHARED between the overview section tree and the INDEX navigation — collapsing a section hides it in both views. Filtering is client-side multi-token AND, case-insensitive FTS (`splitQueryTokens`/`queryMatches`/`filterIndexTasks` in `specsPaths.ts`) over task text + link paths verbatim + link display basenames, with non-overlapping `<mark>` highlighting (`highlightSegments`). All of this view state is store-owned, not local component state, because the floating viewer auto-collapses on outside focus and sibling-tab switches unmount the workspace — local state would silently drop it on every remount (the `ResearchWorkspace` precedent). One-shot convergence fetch only (a restored tab or unwitnessed project switch), never on same-project remounts.

**3. ADR dates come from git — fail-soft, and the spec file format is unchanged.** `GetSpecsStatus` enriches each wire `adrs` element with an optional `date` (ISO `YYYY-MM-DD`, the file's add date; `omitempty` when unknown): a new `ADRMetaDTO` embeds `specs.ADRMeta` and adds the one field — an additive wire change. `core/specs` is untouched; the enrichment lives in the backend (`backend/frontend_api_specs_adrdates.go`) strictly after the pure parse: ONE hardened call `git log --diff-filter=A --name-only --format=%as -- specs/decisions` (newest-first blocks, first-seen-wins per path = the add date survives delete+re-add), keyed through git's C-style path quoting and any repo-root nesting to the parser's specs-root-relative `File` names, bounded by the shared `gitCmdTimeout` (30s) ceiling, and skipped entirely for No Project / an unparseable root / zero ADRs. It is fail-soft ALWAYS: a non-git workspace, a missing git binary, an unborn HEAD, or any spawn/run error yields an empty map — dates absent, the RPC still succeeds, logging stays at Debug (no Warn spam). The ADR template and the specs file format do not change: git history is the only source of dates and they are never written into the records. The overview Timeline (`SpecsOverviewTimeline`) sorts ADRs date-desc with number-desc tie-breaks, pins undated rows (uncommitted files, or any non-git workspace — rendered with an em-dash) into the top "uncommitted" group with a header hint when nothing has dates, and renders sticky year separators; supersession resolution is unchanged, and duplicate-number rows resolve deterministically (the group's smallest path opens, the tooltip says so).

**4. Sidebar composition: nested tree + findings + opener.** The sidebar panel body carries only the nested section tree and the findings cascade — `SpecsIndexTable` and `SpecsADRTimeline` render ONLY on the overview page. `SpecsHeader` renders in both `present` and `!present` branches with an icon-only "Open overview" button (`specs-open-overview`, `disabled` with a "No specs/ tree to open" tooltip when the tree is absent), so the affordance never teleports in or out. The overview page composes: header (title + anchor chips) → toolbar (FTS + By-sections) → INDEX navigation → collapsible section tree → date timeline → findings.

**5. The sidebar tree is nested, and its expansion persists per project.** `buildSpecsTree` derives nesting from path segments — accent top-level section headers, plain subdir nodes, file leaves (title + path tooltip + Key Files chip); the root files group (`INDEX.md`/`META.md`) pins first; nodes carry subtree file counts; `_`-prefixed basenames stay excluded defensively. Everything starts collapsed; expansion lives in `specsStore.treeExpandedNodes: Record<projectId, Record<nodeId, boolean>>` (node id = the directory path relative to the specs root), keyed on the ACTIVE project at toggle time — and that map is the store's ONLY persisted slice (zustand `persist`, key `c0wrk-specs-view` v1, `partialize` → that map only): the panel unmounts on every tab switch and localStorage is the only state that survives an app restart. Per-project isolation is structural — a foreign project never inherits another project's expanded set.

## Consequences

- The wire change is additive-only (`date`, `omitempty`); the frontend boundary guard tolerates its absence, so an older frontend works against a newer backend and vice versa.
- `GetSpecsStatus` gains at most ONE bounded git invocation (none when there are no ADRs); the parse itself is unchanged.
- The sidebar sheds its two truncation-prone wide components; the overview page gives them full viewer width plus search and grouping.
- A new persisted localStorage key `c0wrk-specs-view` (v1) exists; adding/removing persisted fields requires a version bump + migration (the store's `partialize` test fails if the slice set drifts).
- `c0wrk:specs` joins the reserved viewer pseudo-path namespace (`c0wrk:research`, `c0wrk:paper:*`) with the same four obligations: pre-data-lookup routing, no file-read, dedicated icon/label, exclusion from the persisted snapshot.
- The timeline's date quality equals git history quality: uncommitted or freshly re-added ADRs show no date or the original add date respectively — correct by definition, not a bug.

## Alternatives Considered

- **A second standalone panel or dialog for the overview.** Rejected: it forks the surface split; the viewer already hosts full-width workspace pages (research, papers) with routing, icons, and persistence handled.
- **Rendering dates into the ADR files (frontmatter or the Status line).** Rejected: churns accepted records and the ADR template, forces a parser format change, and git history is already the authoritative source — fail-soft enrichment keeps the on-disk format frozen.
- **Backend-side search/filtering for the INDEX rows.** Rejected: the parsed payload is small and already client-side; a per-keystroke RPC round-trip buys nothing and adds latency.
- **Persisting the overview view state (query/sort/collapse) and the `c0wrk:specs` tab across restarts.** Rejected: they are session-scoped — a restored pseudo-path tab would render against a possibly stale or switched project's parse; only the tree expansion has durable value.
- **Keeping the INDEX table and timeline in the sidebar.** Rejected: 180–500px truncates both into unreadability; the narrow column is for navigation (tree + findings), tables belong on the page.
- **Local component state for the overview view state.** Rejected: the floating viewer's auto-collapse and sibling-tab switches remount the workspace and would silently drop the filter/sort/collapse state on every remount.

## Related

- [ADR-106](./106-specs-sidebar-panel.md) — the panel this redesign recomposes; its six decisions stand (placement, data source, refresh, interaction split, two-tier drift, scope boundary).
- [ADR-105](./105-vendor-spec-skillset.md) — the vendored `spec-*` skills the panel dispatches.
- `specs/domains/specs.md` — the subsystem spec (panel + overview page composition, date enrichment, invariants).
- [../contracts/desktop-frontend.md](../contracts/desktop-frontend.md) § Specs — the `ADRMetaDTO.date` wire shape and the fail-soft contract.
