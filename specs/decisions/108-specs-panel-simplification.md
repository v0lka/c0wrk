# ADR-108: Specs panel simplification — overview-page removal, findings-driven fix dispatch, wider tier-1 integrity net

## Status

Accepted

## Context

[ADR-107](./107-specs-panel-usability-redesign.md) split the Specs browser into two surfaces: a narrow sidebar (nested tree + findings + an Open-overview opener) and a full-width `c0wrk:specs` overview page in the file viewer (grouped/searchable INDEX navigation, date-sorted ADR timeline). Real use showed the split pulling its weight only once — the INDEX navigation and the timeline are read-once surfaces, while the sidebar is the surface people keep open — and the duplicated store view state, the guarded opener, and the pseudo-path plumbing (routing, icon/label, snapshot exclusion) taxed every subsequent change. Meanwhile the tier-1 integrity net stayed deliberately small (four checks), so whole classes of mechanically decidable breakage — a deleted INDEX.md, a renumbered-but-unretitled ADR, a supersession pointing at nothing — surfaced only through a manual `spec-check` dispatch, and resolving the findings the panel already showed required the user to hand-write the fix prompt themselves.

## Decision

**1. The overview page is removed; the sidebar panel is the Specs browser again.** The `c0wrk:specs` pseudo-path, the guarded `openSpecsOverview()` action, the opener button, and the overview view state (`overviewQuery`/`overviewSortSections`/`overviewCollapsedGroups`) are deleted together with the page components (`SpecsWorkspace`, `SpecsOverviewIndex*`, `SpecsOverviewTimeline`, `SpecsIndexTable`, `SpecsADRTimeline`, `specsADRs`). `specsStore` carries exactly three concerns again: the parse + fetch bookkeeping (status/isLoading/error/projectId/fetchSeq), the persisted per-project tree expansion, and nothing else. `c0wrk:specs` leaves the reserved viewer pseudo-path namespace; `c0wrk:research` and `c0wrk:paper:*` remain. The parsed INDEX task table stays in the model and on the wire: it is the parser's INDEX_STALE baseline (a file the navigation table points at is not forgotten), not a UI concern.

**2. The findings badge is the fix gesture.** The header's integrity badge — "N findings" — is a button: clicking it dispatches ONE `spec-update` task into the chat (Shift = a new session) whose prompt enumerates every tier-1 finding verbatim from the CURRENT parse (`buildFixFindingsAction` — kind, file, detail per line, plus the resolve-and-re-verify directive). The prompt is derived, not paraphrased, so the task sees exactly what the panel renders; it is built at click time from the store, so a mid-flight refetch updates the list. Zero findings renders the inert "Integrity OK" chip (the clean signal needs no affordance). The per-finding row keeps its read-only gesture: the finding's file link opens it in the viewer. The aggregate dispatch replaces per-finding fix buttons because tier-1 findings interlock — one renumber resolves the duplicate-number, name-malformed and supersession findings around it — and N nearly-identical tasks would each re-verify a tree the previous task had already changed.

**3. Quick actions render short labels.** The narrow sidebar strip renders `Init` / `Check` / `Consult` / `Explore`; each constant gains an optional `fullLabel` ("Initialize specs", "Check drift") spelled out in the button tooltip together with the skill name and the Shift hint. The empty state's CTA shares the same `INITIALIZE_ACTION` constant verbatim, so the label change applies to both.

**4. The git-derived ADR date moves into the DECISIONS tree.** [ADR-107](./107-specs-panel-usability-redesign.md) decision 3 stands — the fail-soft `git log --diff-filter=A` enrichment and the `ADRMetaDTO.date` wire field are unchanged — but its consumer is now the sidebar tree: each `decisions/` leaf with a known date renders it in a fixed-width right-aligned column (tabular digits, "Added DATE" tooltip); undated records (non-git workspace, uncommitted file) render no column at all, so dead space never narrows the title. The date-sorted timeline dies with the overview page; the tree keeps ADR-number order, which is the stable identity ordering.

**5. Tier-1 grows five decidable checks (4 → 9).** Powered by the new ADR status classification in the parser (the `## Status` line's first word reduces to `accepted`/`superseded`, and a superseded line's first successor reference — bracketed `[084]`/`[ADR-055]` or plain `ADR-055`; years never match — yields `superseded_by`):
- `ROOT_FILE_MISSING` — a tree holding at least one parsed file is missing INDEX.md or META.md (a fully empty tree stays clean: the not-initialized-yet state);
- `ADR_NAME_MALFORMED` — a decisions/ file whose base name is not `NNN-slug.md` (invisible to the ADR model; `_`-prefixed templates stay exempt);
- `ADR_NUMBER_MISMATCH` — a `# ADR-NNN:` heading number differing from the file name number (compared numerically, so an unpadded heading matches; a non-template heading produces no finding);
- `SUPERSEDED_TARGET_MISSING` — a superseded ADR whose successor NNN no decisions/ file carries (numeric comparison);
- `SUPERSEDED_CYCLE` — a supersession chain looping back to its own records (reported once, on the lowest-path member, with the walk order in the detail).

Every check stays deterministic, under-approximating, and decidable from the tree itself — semantic drift remains tier-2 (`spec-check`) territory. The wire shape is unchanged: the findings kind travels as a string and the frontend boundary keeps passing unknown kinds through with a generic label, so the kind union growing is invisible to the contract.

## Consequences

- `specsStore` shrinks by the overview trio and the opener; the persisted slice is still exactly `treeExpandedNodes` (persist key `c0wrk-specs-view` v1 — no migration).
- The panel is a single surface again: no guarded opener, no virtual-tab plumbing in `FileViewerContent`, no shared-collapse bookkeeping; the spec subsystem's frontend surface is one store, one panel, one dispatch path.
- Trees that were previously clean can now report findings (a missing root file, a malformed name, a renumber inconsistency); the badge → `spec-update` dispatch gives the user a one-click resolution path, which is what makes the wider net usable rather than noisy.
- The fix prompt names the `spec-update` skill's own repair vocabulary (renumber protocol, INDEX registration, root files), keeping the dispatched task inside the skill's documented procedures.
- The INDEX task table survives as parser-internal data with no rendering consumer — it still feeds INDEX_STALE, so dropping it from the model would weaken that check.

## Alternatives Considered

- **Keep the overview page.** Rejected: after the tree gained nesting, counts, dates and the findings cascade, the page's two sections were re-read-once content duplicating sidebar gestures; every future feature paid the two-surface tax (store view state, opener guards, pseudo-path obligations) for a surface real use had stopped opening.
- **Per-finding fix buttons on each finding row.** Rejected: findings interlock (one renumber clears several), so per-finding tasks would race each other's rewrites and each re-verify a half-repaired tree; one aggregate task resolves the whole set and re-verifies once.
- **Deriving the fix prompt from the finding KINDS only ("fix all findings").** Rejected: a paraphrased or kind-only prompt lets the dispatched task resolve a different tree state than the one the user saw (the parse can change between render and click); the verbatim enumeration is the anchor.
- **Writing the dates into the ADR files or re-sorting the tree by date.** Rejected for the same reasons ADR-107 rejected file-resident dates (churns accepted records; git is the authoritative source), and because number order is the ADR system's identity ordering — recency belongs to the tooltip.
- **Restricting the frontend kind union to the nine known values.** Rejected: the boundary's pass-through contract is what lets a future tier's kinds render (degraded label) instead of dropping findings.

## Related

- [ADR-105](./105-specs-sidebar-panel.md) — the panel's foundation (placement, data source, refresh, interaction split, two-tier drift, scope boundary); all six decisions stand.
- [ADR-107](./107-specs-panel-usability-redesign.md) — the redesign this ADR partially supersedes (the overview page and the opener; the git dates, the nested tree and the persisted expansion stand).
- [ADR-104](./104-vendor-spec-skillset.md) — the vendored `spec-*` skills the panel dispatches; the fix gesture targets `spec-update`.
- `specs/domains/specs.md` — the subsystem spec (panel composition, the nine-kind findings vocabulary, invariants).
- [../contracts/desktop-frontend.md](../contracts/desktop-frontend.md) § Specs — the unchanged `GetSpecsStatus` wire shape and the kind pass-through rule.
