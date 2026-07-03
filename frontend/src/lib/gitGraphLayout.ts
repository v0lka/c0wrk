// Pure commit-graph lane layout (Phase 6).
//
// Assigns each commit (newest-first) to a horizontal "lane" and records the
// lane each parent occupies, so the renderer can draw branch lines and merge
// edges. No React/DOM dependencies — fully testable in isolation.

import type { GraphCommit } from '@/types/models'

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
export function computeGraphLayout(commits: GraphCommit[]): GraphNode[] {
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
