# Plan DAG Graph

## Role

The SVG lane gutter of the collapsible Execution plan panel: one node circle per plan step at its row, with dependency edges routed as vertical lane segments and fork/merge horizontal runs — a compact visual of the plan's dependency DAG beside the step rows.

## Key Files

- `frontend/src/components/chat/ExecutionPanels.tsx` — the panel surface: collapsible "Execution plan" header (ListChecks glyph, `completed/total` count, failed count) over a `max-h-48` scroll area; per plan group a flex row mounting the gutter and the step rows
- `frontend/src/components/chat/DAGGraph.tsx` — the renderer: computes the layout and draws one `<svg>` (node circles, every edge as a single `<path>`); owns the renderer-side geometry constants
- `frontend/src/components/chat/dagEdgePath.ts` — pure SVG path builder (`buildEdgePath`) + lane geometry constants, kept out of the component file for fast-refresh compliance and direct unit testing
- `frontend/src/lib/dagLayout.ts` — pure layout: `computeDAGLayout(items) → { nodes, connectors, maxLane }` (input normalization via the exported `drawnDeps`, owner-checked lane assignment, connector computation, lane-continuation synthesis, broken-gap placement)
- `frontend/src/stores/planStore.ts` — the single plan group (`planGroups[0]`) and its per-step statuses the graph visualizes
- `frontend/src/lib/dagLayout.test.ts` — the invariant battery: fixed topologies (including forward references, mixed declaration order, duplicate `depends_on` entries and undeclared ids) plus seeded-random sweeps over a topological corpus (500 DAGs per run) and a shuffled declaration-order corpus (100 graphs per run), asserting the invariants below on every graph

## Behavior

The panel renders only when a plan group exists and the session is not E2S (an E2S session renders the Execution State panel instead — [../e2s.md](../e2s.md)). The graph is collapsed/expanded with the panel; the step rows beside it are `PlanView`'s 24px rows (status icon, title, duration), clickable to scroll to the step's chat block. Input is `PlanItem[]` with unique ids and `depends_on` entries referencing declared ids — the `declare_plan` contract guarantees this shape ([../../contracts/conductor-tools.md](../../contracts/conductor-tools.md)) but not the declaration order (steps may reference ids declared later; waves keep declaration order verbatim). The layout normalizes dependencies first (`drawnDeps`): duplicate entries collapse to one edge, and an entry naming a later-declared or undeclared id is dropped — an edge pointing upward renders as nothing. Layout output depends only on `id` and `dependsOn`, so a re-render is deterministic with no DOM measurement.

`computeDAGLayout` runs three passes:

1. **Lane assignment (owner-checked parking).** Row = item index. A child parks on its parent's lane only while that parent still owns the lane, no co-parent shares it, and the lane is eligible. `laneLastRow` tracks the lowest visually claimed row per lane — the deepest parked node, extended by merge-drop tails (a merge connector dangles down its parent's lane to the child row, and that extent is claimed at assignment time). A freed lane is reallocated nearest-first around the parent's lane (right, then left: +1, −1, +2, −2 …), only when its claim ends at or above the earliest fork drop that would land on it; the anchor lane itself is never taken (it always carries parked nodes below its former owner); a fresh lane otherwise. The parentless branch is the one exception: with no incoming edge nothing can pierce the node, so a freed anchor lane 0 is claimed directly.
2. **Connectors.** Each dependency edge becomes a `vertical` (parent and child share a lane), a `fork` (parent has multiple children: drop onto a horizontal run at the parent's row), or a `merge` (descend the parent's lane onto a horizontal run at the child's row). Overlapping horizontal runs at the same row stack downward in +3px steps (`exitDy`/`entryDy`) — a run conflicts only with an earlier run at the same final y AND overlapping lane range. Stacking caps at `FAN_DY_MAX_PX`; fans beyond the cap share the capped y.
3. **Lane continuations.** Between consecutive occupants of a lane a vertical continuation segment is synthesized unless an edge-backed vertical already covers it. A continuation whose endpoints have no dependency edge between them (checked in both directions) is marked `broken` — the honest-continuation rule.
4. **Break-gap placement.** Each `broken` continuation carries `gapPx` — the absolute px y of its visible gap, the midpoint of the largest interval of the lane no other segment's ink covers. The segment midpoint would sit under legitimate same-lane ink whenever a merge tail dangles past it or a fork drop lands on the lower occupant's lane, visually healing the false continuation into one solid line. Ink is measured on the lane line itself: corners' arcs leave the lane (vertical ink stops one corner radius short of a horizontal run), and a run crossing strictly intermediate lanes bridges a half-gap band around its y. A fully covered lane — unreachable under the invariants — falls back to the segment midpoint.

`buildEdgePath` turns each connector into one SVG path. Verticals are straight lines; a broken continuation renders as two stubs separated by a visible gap at `gapPx` (absent `gapPx` ≡ segment midpoint, legacy). Forks drop from the parent node onto the run lowered by `exitDy`, then round a corner into the child's lane; merges round onto the child-row run lowered by `entryDy` with a short tail rising back up to the child node (tail omitted at zero offset). Zero `exitDy`/`entryDy` reproduces the legacy path shapes byte-identically.

## Geometry Constants

| Constant | Value | File | Meaning |
| -------- | ----- | ---- | ------- |
| `ROW_HEIGHT_PX` / `BASE_ROW_HEIGHT` | 24 | `dagLayout.ts` / `DAGGraph.tsx` | Row pitch; `ROW_HEIGHT_PX` is the exported single source and `BASE_ROW_HEIGHT` is defined from it in the renderer; `rowY(row) = row·24 + 12` |
| `FAN_DY_MAX_PX` | 9 | `dagLayout.ts` | Cap on stacked fork/merge `dy` — keeps runs inside their row band; fans beyond the cap share the capped y |
| `FAN_STEP_PX` | 3 | `dagLayout.ts` | Vertical step between stacked overlapping fork/merge runs |
| `BROKEN_GAP_PX` | 6 | `dagLayout.ts` | Visible gap between the stubs of a broken continuation; pass 4 clears other ink by half this width |
| `LANE_WIDTH` | 14 | `dagEdgePath.ts` | Horizontal lane pitch; `laneX(lane) = lane·14 + PADDING + 7` |
| `CORNER_RADIUS` | `LANE_WIDTH / 2` (7) | `dagEdgePath.ts` | Fork/merge rounded-corner radius — scales with the lane pitch; mirrored as the local `cornerRadiusPx` (7) in `dagLayout.ts` for gap-placement ink math |
| `PADDING` / `NODE_RADIUS` / `STROKE_WIDTH` | 6 / 3 / 1 | `DAGGraph.tsx` | SVG inset, node circle radius, edge stroke (`--color-muted-foreground`) |

SVG size: `width = (maxLane + 1)·LANE_WIDTH + 2·PADDING`, `height = rows·BASE_ROW_HEIGHT`.

## Error Handling

An empty plan (`maxLane === -1`) renders nothing. The layout is pure and total over its input contract — a contract-valid item list in any declaration order always produces a layout over the drawn edges; there are no partial-failure states, and out-of-order input degrades gracefully (forward-referenced edges do not render; the remaining graph keeps every invariant below). The seeded-random batteries double as a regression gate: any invariant violation in a future change fails the frontend test suite rather than shipping a mangled gutter — the shuffled corpus and the forward-reference cases pin the normalization against non-terminating layouts.

## Invariants

- **Edge-never-pierces-node:** no vertical segment crosses a node circle. Lanes are owner-checked; a freed lane is reused only when every node parked on it sits above the fork drop that would land on it; the anchor lane is never taken by the scan (a parentless item — no incoming edge — may take a freed anchor lane 0 directly); a lane is never reused underneath a merge drop (`laneLastRow` includes the drop extent).
- **Honest continuations:** a lane continuation renders solid only when a dependency edge backs it; unrelated consecutive occupants of a lane render as a broken (two-stub, visible gap) segment, with the gap placed at `gapPx` in the largest stretch of the lane no other segment's ink covers — a merge tail or fork drop spanning the midpoint cannot visually heal the false continuation into a solid line (the battery asserts no same-lane ink covers the gap interval, and a missing or out-of-segment `gapPx` is a defect). `broken` is set only on synthesized continuations — edge-backed verticals never carry it, so absence ≡ solid.
- **Fan rules:** fork runs exit at the parent's row, merge runs enter at the child's row; below the cap, overlapping horizontal runs never share a final y — later runs stack downward in `FAN_STEP_PX` steps up to `FAN_DY_MAX_PX`, keeping runs inside their 24px row band. Fans wider than the band saturate the cap and share the capped y — accepted degradation, mirrored by the battery's cap-only H-overlap exception.
- **Row-band sync:** `BASE_ROW_HEIGHT` in `DAGGraph.tsx` is defined as the `ROW_HEIGHT_PX` exported from `dagLayout.ts` — the dy-band argument holds under the shared pitch.
- **Determinism:** the SVG is a pure function of the items — same input, same layout, same paths; no DOM measurement, no randomness.
- **Legacy compatibility:** connectors with zero `exitDy`/`entryDy` render byte-identically to the pre-fan shapes; absent optional fields ≡ zero/solid, absent `gapPx` ≡ segment-midpoint gap.
- **Order-independent safety:** the layout terminates and holds every invariant above on contract-valid input in any declaration order — a `depends_on` entry naming a later-declared or undeclared id is dropped by normalization and renders no edge; duplicate entries render one edge.

## Related Specs

- [rendering.md](rendering.md) — the chat display pipeline whose `plan_step` blocks these rows scroll to (click-to-scroll via the ScrollContext)
- [../e2s.md](../e2s.md) — the Execution State panel that replaces the plan view (and this gutter) for E2S sessions
- [stores.md](stores.md) — `planStore`: the single plan group and step statuses feeding the graph
- [../../contracts/conductor-tools.md](../../contracts/conductor-tools.md) — the `declare_plan` contract (mandatory `depends_on`, unique resolvable ids, acyclicity) that guarantees the input's DAG shape while leaving declaration order free — the layout normalizes it
