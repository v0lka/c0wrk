// Pure commit-graph lane layout (Phase 6).
//
// Assigns each commit (newest-first) to a horizontal "lane" and records the
// lane each parent occupies, so the renderer can draw branch lines and merge
// edges. No React/DOM dependencies — fully testable in isolation.

import type { GitHistoryCommit } from '@/types/models'

/** A parent edge: the parent SHA and the lane it occupies (downstream). */
export interface GraphParentEdge {
  sha: string
  lane: number
}

/** A single commit node positioned in the graph grid. */
export interface GraphNode {
  sha: string
  /** Horizontal lane index (0-based). */
  lane: number
  /** Vertical row index (0-based, newest commit = row 0). */
  row: number
  message: string
  refs: string[]
  /** Outgoing edges to this commit's parents. */
  parents: GraphParentEdge[]
  /** True when the commit has more than one parent (a merge commit). */
  isMerge: boolean
}

/**
 * Compute a lane layout for a list of commits ordered newest-first.
 *
 * Algorithm: maintain an array `lanes` where each slot holds the SHA of the
 * commit currently "travelling" down that lane (a parent awaiting its own
 * row). For each commit:
 *   1. Reuse the lane already carrying this SHA, else take the first free lane.
 *   2. Free the commit's lane, then place each parent — reusing an existing
 *      parent lane (merge), the just-freed lane, or the next free lane.
 */
export function computeGraphLayout(commits: GitHistoryCommit[]): GraphNode[] {
  const nodes: GraphNode[] = []
  // lanes[l] = SHA currently travelling down lane l, or null when free.
  const lanes: (string | null)[] = []

  for (let i = 0; i < commits.length; i++) {
    const commit = commits[i]!

    // Step 1: find this commit's lane.
    let lane = lanes.findIndex((s) => s === commit.sha)
    if (lane === -1) {
      lane = lanes.findIndex((s) => s === null)
      if (lane === -1) {
        lane = lanes.length
        lanes.push(null)
      }
    }

    // The commit occupies this row; free its lane for parent placement.
    lanes[lane] = null

    const parentEdges: GraphParentEdge[] = []
    for (const parentSha of commit.parents) {
      // Reuse a lane already carrying this parent (shared by another child).
      let parentLane = lanes.findIndex((s) => s === parentSha)
      if (parentLane === -1) {
        // Prefer the just-freed lane, else the first free lane, else a new lane.
        if (lanes[lane] === null) {
          parentLane = lane
        } else {
          parentLane = lanes.findIndex((s) => s === null)
          if (parentLane === -1) {
            parentLane = lanes.length
            lanes.push(null)
          }
        }
      }
      lanes[parentLane] = parentSha
      parentEdges.push({ sha: parentSha, lane: parentLane })
    }

    nodes.push({
      sha: commit.sha,
      lane,
      row: i,
      message: commit.message,
      refs: commit.refs,
      parents: parentEdges,
      isMerge: commit.parents.length > 1,
    })
  }

  return nodes
}

/** Short SHA prefix for display (7 chars, mirroring GitCommitRow). */
export function shortSha(sha: string): string {
  return sha.slice(0, 7)
}

/**
 * Per-row vertical layout for a variable-height commit graph.
 *
 * The renderer draws each commit node aligned with the TOP of its row (where
 * the commit line sits); expanded file lists render below the commit line and
 * push subsequent rows down. `yFor(row)` therefore returns the vertical center
 * of the row's commit-line area: `sum(rowHeights[0..row-1]) + rowSpacing/2`.
 * When every row has the same height this collapses to the original
 * fixed-spacing formula (`row * rowSpacing + rowSpacing/2`); when some rows
 * expand inline the taller rows push every later node down by their height
 * delta, so SVG edges route around them.
 */
export interface RowYLayout {
  /** Vertical center (px) of the commit-line area of the given 0-based row. */
  yFor: (row: number) => number
  /** Total SVG height (px) spanning all rows plus a half-row bottom pad. */
  totalHeight: number
}

/**
 * Compute vertical positions when rows may have variable heights.
 *
 * `rowHeights[i]` is the pixel height of row `i` (use `rowSpacing` for rows
 * that are not expanded). The node for row `r` is placed at
 * `sum(rowHeights[0..r-1]) + nodeOffset` (the center of the commit-message
 * line at the top of the row), and `totalHeight` is
 * `sum(rowHeights) + rowSpacing/2` (matching the current bottom pad).
 *
 * `nodeOffset` defaults to `rowSpacing / 2` (the geometric row center, used
 * for single-line rows). For two-line rows where the message line sits above
 * the center, pass `NODE_OFFSET` from `gitGraphRender` so the SVG node aligns
 * with the message line rather than the row center.
 *
 * Backward compatibility: when `nodeOffset` is omitted and every
 * `rowHeights[i] === rowSpacing`, `yFor(row) === row * rowSpacing + rowSpacing/2`
 * exactly and `totalHeight === rowHeights.length * rowSpacing + rowSpacing/2`.
 *
 * Out-of-range rows are handled defensively: `row < 0` returns `0` (top edge)
 * and `row >= rowHeights.length` returns the cumulative content height (bottom
 * edge) — neither case arises for real graph nodes but both avoid `NaN`.
 *
 * Pure: does not mutate `rowHeights` and the returned `yFor` closure depends
 * only on an internal prefix-sum snapshot, so later caller mutations have no
 * effect.
 */
export function computeRowYLayout(rowHeights: number[], rowSpacing: number, nodeOffset?: number): RowYLayout {
  const len = rowHeights.length
  // prefix[i] = cumulative height of rows [0..i-1]; prefix[0] = 0.
  const prefix: number[] = new Array<number>(len + 1)
  prefix[0] = 0
  for (let i = 0; i < len; i++) {
    prefix[i + 1] = prefix[i]! + rowHeights[i]!
  }
  const totalContent = prefix[len]!
  const totalHeight = totalContent + rowSpacing / 2
  // Distance from the top of a row to the commit-message-line center.
  // Defaults to half the row spacing (single-line rows); pass NODE_OFFSET
  // for two-line rows where the message line sits above the row center.
  const offset = nodeOffset ?? rowSpacing / 2

  const yFor = (row: number): number => {
    if (row < 0) return 0
    if (row >= len) return totalContent
    // Node sits at the commit-message-line center at the TOP of the row,
    // so the SVG circle aligns with the message line even when the row is
    // expanded with changed files below it.
    return prefix[row]! + offset
  }

  return { yFor, totalHeight }
}
