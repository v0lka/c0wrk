import { BROKEN_GAP_PX, type DAGConnector } from '@/lib/dagLayout'

export const LANE_WIDTH = 14
/** Fork/merge corner radius — derived from the lane pitch so turns scale with the graph. */
const CORNER_RADIUS = LANE_WIDTH / 2

/**
 * Pure SVG path builder for one DAG connector — kept out of DAGGraph.tsx so
 * the component file exports only a component (fast refresh keeps working,
 * react-refresh/only-export-components) and the path construction stays
 * directly unit-testable.
 *
 * `laneX`/`rowY` mirror the renderer's coordinate functions.
 *
 * Shapes:
 * - vertical: straight line down the lane; `broken` (a false lane
 *   continuation whose endpoints have no dependency edge) renders as two
 *   stubs separated by a visible gap at `gapPx` — the gap center the layout
 *   placed in the lane's largest bare stretch; absent `gapPx` falls back to
 *   the segment midpoint (legacy).
 * - fork: drops from the parent node onto a horizontal run at the parent's
 *   row lowered by `exitDy` (stacked +3px per earlier overlapping run), then
 *   a rounded corner down into the child's lane.
 * - merge: descends the parent's lane, a rounded corner onto a horizontal run
 *   at the child's row lowered by `entryDy`, then a short rise back up to the
 *   child node.
 */
export function buildEdgePath(
  c: DAGConnector,
  laneX: (lane: number) => number,
  rowY: (row: number) => number,
): string {
  const x1 = laneX(c.fromLane)
  const y1 = rowY(c.fromRow)
  const x2 = laneX(c.toLane)
  const y2 = rowY(c.toRow)

  if (c.type === 'vertical') {
    if (c.broken) {
      // gapPx is placed by the layout (pass 4) in the largest stretch of the
      // lane no other segment's ink covers; absent ≡ segment midpoint (legacy).
      const mid = c.gapPx ?? (y1 + y2) / 2
      const half = BROKEN_GAP_PX / 2
      return `M ${x1} ${y1} L ${x1} ${mid - half} M ${x1} ${mid + half} L ${x1} ${y2}`
    }
    return `M ${x1} ${y1} L ${x2} ${y2}`
  }

  const dx = Math.sign(x2 - x1)
  const r = CORNER_RADIUS
  if (c.type === 'fork') {
    // Horizontal run at the parent's row, lowered by exitDy; zero offset
    // reproduces the legacy shape (run straight out of the node).
    const hy = y1 + (c.exitDy ?? 0)
    const drop = c.exitDy ? ` L ${x1} ${hy}` : ''
    return `M ${x1} ${y1}${drop} L ${x2 - dx * r} ${hy} Q ${x2} ${hy} ${x2} ${hy + r} L ${x2} ${y2}`
  }
  // merge: horizontal run at the child's row, lowered by entryDy; the tail
  // rises back up to the node (omitted at zero offset — legacy shape).
  const hy = y2 + (c.entryDy ?? 0)
  const tail = hy === y2 ? '' : ` L ${x2} ${y2}`
  return `M ${x1} ${y1} L ${x1} ${hy - r} Q ${x1} ${hy} ${x1 + dx * r} ${hy} L ${x2} ${hy}${tail}`
}
