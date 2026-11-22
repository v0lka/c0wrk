/** Minimal input shape — only needs id and dependsOn */
export interface DAGItem {
  id: string
  dependsOn: string[]
}

export interface DAGNode {
  id: string
  lane: number
}

export interface DAGConnector {
  fromLane: number
  toLane: number
  fromRow: number
  toRow: number
  type: 'vertical' | 'fork' | 'merge'
  /** px offset lowering the fork's horizontal run at fromRow; +3 per earlier overlapping run in the same row, capped at FAN_DY_MAX_PX */
  exitDy?: number
  /** px offset lowering the merge's horizontal run at toRow; +3 per earlier overlapping run in the same row, capped at FAN_DY_MAX_PX */
  entryDy?: number
  /** Lane continuation whose endpoints have no dependency edge between them — a false continuation, drawn broken */
  broken?: boolean
}

export interface DAGLayout {
  nodes: DAGNode[]
  connectors: DAGConnector[]
  maxLane: number
}

const FAN_STEP_PX = 3
/** Single source of the row pitch — DAGGraph.tsx derives BASE_ROW_HEIGHT from this export. */
export const ROW_HEIGHT_PX = 24
/** Cap on stacked fork/merge dy: the largest FAN_STEP_PX multiple whose run (band middle + dy)
 *  stays inside its 24px row band with the 7px corner dip (CORNER_RADIUS in dagEdgePath.ts)
 *  clear of the next row's node circles; fans beyond the cap share the capped y. */
const FAN_DY_MAX_PX = 9

/**
 * Compute lane assignments and SVG connector segments from plan items
 * in topological order (guaranteed by backend).
 *
 * Lanes are owner-checked: a child parks on a parent's lane only while that
 * parent still owns it, and a freed lane is handed out only when its last
 * occupant sits above every fork drop that would land on it, so no vertical
 * segment crosses a node. Fork/merge horizontal runs sharing a row stack
 * +3px (exitDy/entryDy) per earlier overlapping run, capped at FAN_DY_MAX_PX.
 */
export function computeDAGLayout(items: DAGItem[]): DAGLayout {
  if (items.length === 0) return { nodes: [], connectors: [], maxLane: -1 }

  const rowOf = new Map<string, number>()
  for (let i = 0; i < items.length; i++) {
    rowOf.set(items[i]!.id, i)
  }

  // Build children map
  const childrenMap = new Map<string, string[]>()
  for (const item of items) {
    for (const dep of item.dependsOn) {
      let children = childrenMap.get(dep)
      if (!children) { children = []; childrenMap.set(dep, children) }
      children.push(item.id)
    }
  }

  const remainingChildren = new Map<string, number>()
  const childCount = new Map<string, number>()
  for (const item of items) {
    const count = childrenMap.get(item.id)?.length ?? 0
    remainingChildren.set(item.id, count)
    childCount.set(item.id, count)
  }

  // Lane assignment state
  const laneOf = new Map<string, number>()
  const laneOwner = new Map<number, string>()
  // Lowest row already visually claimed on the lane: the deepest parked node,
  // extended by merge drops (which dangle below their parent lane down to the
  // child row). A lane may only be reused by drops starting at or below it.
  const laneLastRow = new Map<number, number>()
  const freeLanes: number[] = []
  let nextLane = 0

  function laneEligible(lane: number, minForkRow: number): boolean {
    return (laneLastRow.get(lane) ?? -Infinity) <= minForkRow
  }

  function removeFreeLane(lane: number): void {
    const idx = freeLanes.indexOf(lane)
    if (idx !== -1) freeLanes.splice(idx, 1)
  }

  /**
   * Nearest-eligible allocation around `anchor`: freed lanes are scanned by
   * ascending distance, right side first (anchor+1, anchor-1, anchor+2, ...),
   * then a fresh lane. The anchor lane itself is never taken: a freed lane
   * always carries parked nodes below its former owner, which a vertical
   * continuation would pierce.
   */
  function allocateLane(anchor: number, minForkRow: number): number {
    for (let d = 1; ; d++) {
      const right = anchor + d
      const left = anchor - d
      if (right >= nextLane && left < 0) break
      if (right < nextLane && freeLanes.includes(right) && laneEligible(right, minForkRow)) {
        removeFreeLane(right)
        return right
      }
      if (left >= 0 && freeLanes.includes(left) && laneEligible(left, minForkRow)) {
        removeFreeLane(left)
        return left
      }
    }
    return nextLane++
  }

  function freeLane(lane: number): void {
    let i = 0
    while (i < freeLanes.length && freeLanes[i]! < lane) i++
    freeLanes.splice(i, 0, lane)
  }

  function decrementAndMaybeFree(parentId: string, excludeLane?: number): void {
    const rem = remainingChildren.get(parentId)! - 1
    remainingChildren.set(parentId, rem)
    if (rem === 0) {
      const pLane = laneOf.get(parentId)!
      if (pLane !== excludeLane && laneOwner.get(pLane) === parentId) {
        freeLane(pLane)
        laneOwner.delete(pLane)
      }
    }
  }

  /** Lowest row among the parents whose edges fork-drop onto the child's lane. */
  function forkParentMinRow(parents: string[], excludeId?: string): number {
    let min = Infinity
    for (const p of parents) {
      if (p === excludeId) continue
      if ((childCount.get(p) ?? 0) > 1) min = Math.min(min, rowOf.get(p)!)
    }
    return min
  }

  // Pass 1: Assign lanes
  const nodes: DAGNode[] = []
  for (let row = 0; row < items.length; row++) {
    const item = items[row]!
    const parents = item.dependsOn
    let assignedLane: number
    if (parents.length === 0) {
      assignedLane = allocateLane(0, Infinity)
    } else if (parents.length === 1) {
      const parentId = parents[0]!
      const parentLane = laneOf.get(parentId)!
      if (laneOwner.get(parentLane) === parentId) {
        assignedLane = parentLane
      } else {
        assignedLane = allocateLane(parentLane, forkParentMinRow(parents))
      }
      decrementAndMaybeFree(parentId, assignedLane)
    } else {
      const parentLanes = parents.map(d => ({ id: d, lane: laneOf.get(d)! }))
      parentLanes.sort((a, b) => a.lane - b.lane)
      assignedLane = -1
      for (const pl of parentLanes) {
        if (laneOwner.get(pl.lane) !== pl.id) continue
        // A vertical from a co-parent parked higher on the same lane would pierce the chain between them
        if (parents.some(o => o !== pl.id && laneOf.get(o) === pl.lane)) continue
        if (!laneEligible(pl.lane, forkParentMinRow(parents, pl.id))) continue
        assignedLane = pl.lane
        break
      }
      if (assignedLane === -1) {
        assignedLane = allocateLane(parentLanes[0]!.lane, forkParentMinRow(parents))
      }
      for (const p of parents) decrementAndMaybeFree(p, assignedLane)
    }
    laneOf.set(item.id, assignedLane)
    laneOwner.set(assignedLane, item.id)
    laneLastRow.set(assignedLane, row)
    // A merge-type parent edge dangles below its own lane down to this row —
    // claim that extent so the lane is not reused underneath the drop.
    for (const p of parents) {
      const pLane = laneOf.get(p)!
      if (pLane !== assignedLane && (childCount.get(p) ?? 0) <= 1) {
        laneLastRow.set(pLane, Math.max(laneLastRow.get(pLane) ?? -Infinity, row))
      }
    }
    nodes.push({ id: item.id, lane: assignedLane })
  }

  // Pass 2: Compute connectors
  const connectors: DAGConnector[] = []
  // Horizontal runs already placed (final y, x extent in lanes) — a new run
  // takes the lowest +3px step whose y is free over its overlapping peers,
  // capped at FAN_DY_MAX_PX (beyond the cap runs share the capped y).
  const hPlaced: { yPx: number; lo: number; hi: number }[] = []

  for (let row = 0; row < items.length; row++) {
    const item = items[row]!
    const childLane = laneOf.get(item.id)!
    for (const parentId of item.dependsOn) {
      const parentRow = rowOf.get(parentId)!
      const parentLane = laneOf.get(parentId)!
      let type: DAGConnector['type']
      if (parentLane === childLane) type = 'vertical'
      else if ((childCount.get(parentId) || 0) > 1) type = 'fork'
      else type = 'merge'
      if (type === 'vertical') {
        connectors.push({ fromLane: parentLane, toLane: childLane, fromRow: parentRow, toRow: row, type })
        continue
      }
      const lo = Math.min(parentLane, childLane)
      const hi = Math.max(parentLane, childLane)
      const hRow = type === 'fork' ? parentRow : row
      let dy = 0
      for (;; dy += FAN_STEP_PX) {
        const yPx = hRow * ROW_HEIGHT_PX + dy
        const free = !hPlaced.some(r => r.yPx === yPx && Math.min(r.hi, hi) > Math.max(r.lo, lo))
        if (free || dy >= FAN_DY_MAX_PX) break
      }
      hPlaced.push({ yPx: hRow * ROW_HEIGHT_PX + dy, lo, hi })
      const connector: DAGConnector = { fromLane: parentLane, toLane: childLane, fromRow: parentRow, toRow: row, type }
      if (type === 'fork') connector.exitDy = dy
      else connector.entryDy = dy
      connectors.push(connector)
    }
  }

  // Pass 3: Vertical continuation segments
  const laneEvents = new Map<number, number[]>()
  function addLaneEvent(lane: number, row: number): void {
    let rows = laneEvents.get(lane)
    if (!rows) { rows = []; laneEvents.set(lane, rows) }
    rows.push(row)
  }

  for (let row = 0; row < nodes.length; row++) {
    addLaneEvent(nodes[row]!.lane, row)
  }

  const existingVerticals = new Set<string>()
  for (const c of connectors) {
    if (c.type === 'vertical' && c.fromLane === c.toLane) {
      existingVerticals.add(`${c.fromLane}:${c.fromRow}:${c.toRow}`)
    }
  }

  for (const [lane, rows] of laneEvents) {
    const unique = [...new Set(rows)].sort((a, b) => a - b)
    for (let i = 0; i < unique.length - 1; i++) {
      const from = unique[i]!
      const to = unique[i + 1]!
      const key = `${lane}:${from}:${to}`
      if (!existingVerticals.has(key)) {
        // False continuation guard (BUG4): consecutive occupants of a lane with
        // no dependency edge between them must not render as an unbroken chain.
        const fromId = items[from]!.id
        const toId = items[to]!.id
        const edgeBacked =
          (childrenMap.get(fromId)?.includes(toId) ?? false) ||
          (childrenMap.get(toId)?.includes(fromId) ?? false)
        const connector: DAGConnector = { fromLane: lane, toLane: lane, fromRow: from, toRow: to, type: 'vertical' }
        if (!edgeBacked) connector.broken = true
        connectors.push(connector)
        existingVerticals.add(key)
      }
    }
  }

  const maxLane = nodes.reduce((max, n) => Math.max(max, n.lane), 0)
  return { nodes, connectors, maxLane }
}
