/** Minimal input shape — only needs id and dependsOn */
export interface DAGItem {
  id: string
  dependsOn: string[]
}

export interface DAGNode {
  id: string
  lane: number // horizontal lane index (0-based)
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
  maxLane: number // for computing SVG width
}

/**
 * Computes lane assignments and SVG connector segments from plan items
 * in topological order (guaranteed by backend).
 *
 * Uses a greedy lane-allocation algorithm that reuses freed lanes
 * to keep the graph narrow.
 */
export function computeDAGLayout(items: DAGItem[]): DAGLayout {
  if (items.length === 0) {
    return { nodes: [], connectors: [], maxLane: -1 }
  }

  // Index lookup: id → row index
  const rowOf = new Map<string, number>()
  for (let i = 0; i < items.length; i++) {
    const item = items[i]! // Safe: loop bounds guarantee valid index
    rowOf.set(item.id, i)
  }

  // Build children map: parentId → list of child ids
  const childrenMap = new Map<string, string[]>()
  for (const item of items) {
    for (const dep of item.dependsOn) {
      let children = childrenMap.get(dep)
      if (!children) {
        children = []
        childrenMap.set(dep, children)
      }
      children.push(item.id)
    }
  }

  // Remaining children count for each item
  const remainingChildren = new Map<string, number>()
  for (const item of items) {
    const children = childrenMap.get(item.id)
    remainingChildren.set(item.id, children ? children.length : 0)
  }

  // Lane assignment state
  const laneOf = new Map<string, number>()
  const freeLanes: number[] = [] // sorted ascending
  let nextLane = 0

  function allocateLane(): number {
    if (freeLanes.length > 0) {
      return freeLanes.shift()!
    }
    return nextLane++
  }

  function freeLane(lane: number): void {
    // Insert into sorted position
    let i = 0
    while (i < freeLanes.length && freeLanes[i]! < lane) i++ // Safe: loop bounds guarantee valid index
    freeLanes.splice(i, 0, lane)
  }

  // Track when each lane becomes active/inactive for vertical continuations
  // laneOwner: lane → id of the node currently "owning" this lane
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

  // --- Pass 1: Assign lanes ---
  const nodes: DAGNode[] = []

  for (const item of items) {
    let assignedLane: number

    if (item.dependsOn.length === 0) {
      // Root node — allocate a lane
      assignedLane = allocateLane()
    } else if (item.dependsOn.length === 1) {
      // Single dependency — inherit parent's lane only if parent still owns it
      const parentId = item.dependsOn[0]! // Safe: length === 1 guarantees valid index
      const parentLane = laneOf.get(parentId)!
      if (laneOwner.get(parentLane) === parentId) {
        assignedLane = parentLane
      } else {
        assignedLane = allocateLane()
      }
      decrementAndMaybeFree(parentId, assignedLane)
    } else {
      // Multiple dependencies (merge) — pick leftmost parent lane
      const parentLanes = item.dependsOn.map((d) => ({
        id: d,
        lane: laneOf.get(d)!,
      }))
      parentLanes.sort((a, b) => a.lane - b.lane)

      assignedLane = parentLanes[0]!.lane // Safe: dependsOn.length > 1 guarantees at least one entry

      // Decrement all parents; free those with no remaining children
      for (const p of parentLanes) {
        decrementAndMaybeFree(p.id, assignedLane)
      }
    }

    laneOf.set(item.id, assignedLane)
    laneOwner.set(assignedLane, item.id)
    nodes.push({ id: item.id, lane: assignedLane })
  }

  // --- Pass 2: Compute connectors ---
  const connectors: DAGConnector[] = []

  // Determine which parents have multiple children (for fork detection)
  const childCount = new Map<string, number>()
  for (const item of items) {
    for (const dep of item.dependsOn) {
      childCount.set(dep, (childCount.get(dep) || 0) + 1)
    }
  }

  // Parent→child connectors
  for (let row = 0; row < items.length; row++) {
    const item = items[row]! // Safe: loop bounds guarantee valid index
    const childLane = laneOf.get(item.id)!

    for (const parentId of item.dependsOn) {
      const parentRow = rowOf.get(parentId)!
      const parentLane = laneOf.get(parentId)!

      let type: DAGConnector['type']
      if (parentLane === childLane) {
        type = 'vertical'
      } else if ((childCount.get(parentId) || 0) > 1) {
        type = 'fork'
      } else {
        type = 'merge'
      }

      connectors.push({
        fromLane: parentLane,
        toLane: childLane,
        fromRow: parentRow,
        toRow: row,
        type,
      })
    }
  }

  // --- Pass 3: Vertical continuation segments ---
  // For each lane, find all rows that have activity (a node placed or a
  // connector entering/exiting). Then fill gaps with vertical segments.
  const laneEvents = new Map<number, number[]>() // lane → sorted row indices

  function addLaneEvent(lane: number, row: number): void {
    let rows = laneEvents.get(lane)
    if (!rows) {
      rows = []
      laneEvents.set(lane, rows)
    }
    rows.push(row)
  }

  // Nodes placed in lanes
  for (let row = 0; row < nodes.length; row++) {
    addLaneEvent(nodes[row]!.lane, row) // Safe: loop bounds guarantee valid index
  }

  // Cross-lane connectors (fork/merge) draw their own complete visual paths
  // via the SVG path elements. No additional lane events are needed for them.
  // Vertical continuation segments are only needed between nodes in the same lane,
  // which are already registered above.

  // Build a set of existing vertical connectors to avoid duplicates
  const existingVerticals = new Set<string>()
  for (const c of connectors) {
    if (c.type === 'vertical' && c.fromLane === c.toLane) {
      existingVerticals.add(`${c.fromLane}:${c.fromRow}:${c.toRow}`)
    }
  }

  // For each lane, sort events and add vertical segments through gaps
  for (const [lane, rows] of laneEvents) {
    const unique = [...new Set(rows)].sort((a, b) => a - b)
    for (let i = 0; i < unique.length - 1; i++) {
      const from = unique[i]! // Safe: loop bounds guarantee valid index
      const to = unique[i + 1]! // Safe: i < unique.length - 1 guarantees valid index
      if (to - from > 1 || !existingVerticals.has(`${lane}:${from}:${to}`)) {
        // Only add if this exact segment doesn't already exist
        const key = `${lane}:${from}:${to}`
        if (!existingVerticals.has(key)) {
          connectors.push({
            fromLane: lane,
            toLane: lane,
            fromRow: from,
            toRow: to,
            type: 'vertical',
          })
          existingVerticals.add(key)
        }
      }
    }
  }

  const maxLane = nodes.reduce((max, n) => Math.max(max, n.lane), 0)

  return { nodes, connectors, maxLane }
}
