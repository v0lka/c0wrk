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
}

export interface DAGLayout {
  nodes: DAGNode[]
  connectors: DAGConnector[]
  maxLane: number
}

/**
 * Compute lane assignments and SVG connector segments from plan items
 * in topological order (guaranteed by backend).
 * Uses a greedy lane-allocation algorithm that reuses freed lanes.
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
  for (const item of items) {
    remainingChildren.set(item.id, childrenMap.get(item.id)?.length ?? 0)
  }

  // Lane assignment state
  const laneOf = new Map<string, number>()
  const freeLanes: number[] = []
  let nextLane = 0

  function allocateLane(): number {
    return freeLanes.length > 0 ? freeLanes.shift()! : nextLane++
  }

  function freeLane(lane: number): void {
    let i = 0
    while (i < freeLanes.length && freeLanes[i]! < lane) i++
    freeLanes.splice(i, 0, lane)
  }

  const laneOwner = new Map<number, string>()

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

  // Pass 1: Assign lanes
  const nodes: DAGNode[] = []
  for (const item of items) {
    let assignedLane: number
    if (item.dependsOn.length === 0) {
      assignedLane = allocateLane()
    } else if (item.dependsOn.length === 1) {
      const parentId = item.dependsOn[0]!
      const parentLane = laneOf.get(parentId)!
      assignedLane = laneOwner.get(parentLane) === parentId ? parentLane : allocateLane()
      decrementAndMaybeFree(parentId, assignedLane)
    } else {
      const parentLanes = item.dependsOn.map(d => ({ id: d, lane: laneOf.get(d)! }))
      parentLanes.sort((a, b) => a.lane - b.lane)
      assignedLane = parentLanes[0]!.lane
      for (const p of parentLanes) decrementAndMaybeFree(p.id, assignedLane)
    }
    laneOf.set(item.id, assignedLane)
    laneOwner.set(assignedLane, item.id)
    nodes.push({ id: item.id, lane: assignedLane })
  }

  // Pass 2: Compute connectors
  const connectors: DAGConnector[] = []
  const childCount = new Map<string, number>()
  for (const item of items) {
    for (const dep of item.dependsOn) {
      childCount.set(dep, (childCount.get(dep) || 0) + 1)
    }
  }

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
      connectors.push({ fromLane: parentLane, toLane: childLane, fromRow: parentRow, toRow: row, type })
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
        connectors.push({ fromLane: lane, toLane: lane, fromRow: from, toRow: to, type: 'vertical' })
        existingVerticals.add(key)
      }
    }
  }

  const maxLane = nodes.reduce((max, n) => Math.max(max, n.lane), 0)
  return { nodes, connectors, maxLane }
}
