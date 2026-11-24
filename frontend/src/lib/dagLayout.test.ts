import { describe, it, expect } from 'vitest'
import { BROKEN_GAP_PX, computeDAGLayout, drawnDeps, type DAGConnector, type DAGItem, type DAGLayout } from './dagLayout'

describe('computeDAGLayout', () => {
  it('returns empty layout for empty input', () => {
    const result = computeDAGLayout([])
    expect(result).toEqual({ nodes: [], connectors: [], maxLane: -1 })
  })

  it('places a single node at lane 0', () => {
    const result = computeDAGLayout([{ id: 'A', dependsOn: [] }])
    expect(result.nodes).toEqual([{ id: 'A', lane: 0 }])
    expect(result.connectors).toEqual([])
    expect(result.maxLane).toBe(0)
  })

  it('places a linear chain in lane 0 with vertical connectors', () => {
    const items: DAGItem[] = [
      { id: 'A', dependsOn: [] },
      { id: 'B', dependsOn: ['A'] },
      { id: 'C', dependsOn: ['B'] },
    ]
    const result = computeDAGLayout(items)
    expect(result.nodes).toEqual([
      { id: 'A', lane: 0 },
      { id: 'B', lane: 0 },
      { id: 'C', lane: 0 },
    ])
    expect(result.maxLane).toBe(0)
    // Should have vertical connectors A->B and B->C
    const verticals = result.connectors.filter(c => c.type === 'vertical')
    expect(verticals.length).toBeGreaterThanOrEqual(2)
    expect(verticals).toContainEqual({ fromLane: 0, toLane: 0, fromRow: 0, toRow: 1, type: 'vertical' })
    expect(verticals).toContainEqual({ fromLane: 0, toLane: 0, fromRow: 1, toRow: 2, type: 'vertical' })
  })

  it('forks into separate lanes', () => {
    // A -> B and A -> C
    const items: DAGItem[] = [
      { id: 'A', dependsOn: [] },
      { id: 'B', dependsOn: ['A'] },
      { id: 'C', dependsOn: ['A'] },
    ]
    const result = computeDAGLayout(items)
    // B should inherit A's lane 0, C should get lane 1
    expect(result.nodes[0]).toEqual({ id: 'A', lane: 0 })
    expect(result.nodes[1]).toEqual({ id: 'B', lane: 0 })
    expect(result.nodes[2]).toEqual({ id: 'C', lane: 1 })
    expect(result.maxLane).toBe(1)
    // Should have a fork connector from A to C
    const forks = result.connectors.filter(c => c.type === 'fork')
    expect(forks.length).toBeGreaterThanOrEqual(1)
  })

  it('merges from multiple parents into leftmost lane', () => {
    // A and B (independent roots) -> C depends on both
    const items: DAGItem[] = [
      { id: 'A', dependsOn: [] },
      { id: 'B', dependsOn: [] },
      { id: 'C', dependsOn: ['A', 'B'] },
    ]
    const result = computeDAGLayout(items)
    expect(result.nodes[0]).toEqual({ id: 'A', lane: 0 })
    expect(result.nodes[1]).toEqual({ id: 'B', lane: 1 })
    // C merges — should inherit leftmost parent lane (0)
    expect(result.nodes[2]).toEqual({ id: 'C', lane: 0 })
    // Should have a merge connector
    const merges = result.connectors.filter(c => c.type === 'merge')
    expect(merges.length).toBeGreaterThanOrEqual(1)
  })

  it('handles diamond pattern (fork then merge)', () => {
    // A -> B, A -> C, B+C -> D
    const items: DAGItem[] = [
      { id: 'A', dependsOn: [] },
      { id: 'B', dependsOn: ['A'] },
      { id: 'C', dependsOn: ['A'] },
      { id: 'D', dependsOn: ['B', 'C'] },
    ]
    const result = computeDAGLayout(items)
    expect(result.nodes).toHaveLength(4)
    // A at lane 0, B inherits 0, C gets 1, D merges to leftmost (0)
    expect(result.nodes[0]).toEqual({ id: 'A', lane: 0 })
    expect(result.nodes[1]).toEqual({ id: 'B', lane: 0 })
    expect(result.nodes[2]).toEqual({ id: 'C', lane: 1 })
    expect(result.nodes[3]).toEqual({ id: 'D', lane: 0 })
  })

  it('reuses freed lanes', () => {
    // A -> B (B has no children), A -> C -> D
    // After B is processed with no children, its lane should be freed for reuse
    const items: DAGItem[] = [
      { id: 'A', dependsOn: [] },
      { id: 'B', dependsOn: ['A'] },
      { id: 'C', dependsOn: ['A'] },
      { id: 'D', dependsOn: ['C'] },
    ]
    const result = computeDAGLayout(items)
    expect(result.nodes).toHaveLength(4)
    // A at 0, B inherits 0 (first child), C gets 1
    // D inherits C's lane 1
    expect(result.nodes[0]).toEqual({ id: 'A', lane: 0 })
    expect(result.nodes[1]).toEqual({ id: 'B', lane: 0 })
    expect(result.nodes[2]).toEqual({ id: 'C', lane: 1 })
    expect(result.nodes[3]).toEqual({ id: 'D', lane: 1 })
  })

  it('handles two independent roots', () => {
    const items: DAGItem[] = [
      { id: 'A', dependsOn: [] },
      { id: 'B', dependsOn: [] },
    ]
    const result = computeDAGLayout(items)
    expect(result.nodes).toEqual([
      { id: 'A', lane: 0 },
      { id: 'B', lane: 1 },
    ])
    expect(result.maxLane).toBe(1)
  })
})

function laneOf(layout: DAGLayout, id: string): number {
  const node = layout.nodes.find(n => n.id === id)
  expect(node, `node ${id} in layout`).toBeDefined()
  return node!.lane
}

describe('owner-checked parking (T2)', () => {
  // B's lane 0 is taken over by C (A→B→C park chain); F merges from B and E and
  // must not land on either parent's unusable lane.
  const t2Items: DAGItem[] = [
    { id: 'A', dependsOn: [] },
    { id: 'B', dependsOn: ['A'] },
    { id: 'C', dependsOn: ['B'] },
    { id: 'D', dependsOn: ['A'] },
    { id: 'E', dependsOn: ['D'] },
    { id: 'F', dependsOn: ['B', 'E'] },
  ]

  it('never parks a multi-parent child on a parent lane owned by another node', () => {
    const layout = computeDAGLayout(t2Items)
    expect(laneOf(layout, 'A')).toBe(0)
    expect(laneOf(layout, 'B')).toBe(0)
    expect(laneOf(layout, 'C')).toBe(0)
    expect(laneOf(layout, 'E')).toBe(1)
    // lane 0 is owned by C (not B), lane 1 is ineligible (E sits below B's row)
    expect(laneOf(layout, 'F')).toBe(2)
    const bf = layout.connectors.find(c => c.fromRow === 1 && c.toRow === 5)
    expect(bf?.type).toBe('fork')
    expect(bf?.toLane).toBe(2)
  })

  it('rejects an owned parent lane when a fork parent sits above its occupant', () => {
    // A forks B and C, then K merges from B, C and A: parking on C's lane 1
    // would put A's fork drop (starting at row 0) through C.
    const items: DAGItem[] = [
      { id: 'A', dependsOn: [] },
      { id: 'B', dependsOn: ['A'] },
      { id: 'C', dependsOn: ['A'] },
      { id: 'K', dependsOn: ['B', 'C', 'A'] },
    ]
    const layout = computeDAGLayout(items)
    expect(laneOf(layout, 'K')).toBe(2)
    // A→K forks onto K's own lane; B and C merge into it
    const ak = layout.connectors.find(c => c.fromRow === 0 && c.toRow === 3)
    expect(ak?.type).toBe('fork')
    const merges = layout.connectors.filter(c => c.type === 'merge')
    expect(merges).toHaveLength(2)
  })
})

describe('freed-lane reuse (drop piercing)', () => {
  it('never allocates a freed lane whose last occupant sits below the parent row', () => {
    // Y frees lane 1 at row 1; E forks from A (row 0): reusing lane 1 would
    // drop A→E vertically through Y.
    const items: DAGItem[] = [
      { id: 'A', dependsOn: [] },
      { id: 'Y', dependsOn: [] },
      { id: 'B', dependsOn: ['A', 'Y'] },
      { id: 'C', dependsOn: ['B'] },
      { id: 'E', dependsOn: ['A'] },
    ]
    const layout = computeDAGLayout(items)
    expect(laneOf(layout, 'Y')).toBe(1)
    expect(laneOf(layout, 'E')).toBe(2)
    const ae = layout.connectors.find(c => c.fromRow === 0 && c.toRow === 4)
    expect(ae?.type).toBe('fork')
    expect(ae?.toLane).toBe(2)
  })
})

describe('freed anchor lane 0 (parentless reclaim)', () => {
  it('lets a parentless item take a freed anchor lane 0', () => {
    // C frees lane 0 at row 3 (M parks on F's lane 1, the only lane left:
    // F forks from row 1 above C's parked tail, making lane 0 ineligible for
    // M). F2 must still not ride the freed lane 0 (it has a fork parent), but
    // the following parentless P has no incoming edge and reclaims it.
    const items: DAGItem[] = [
      { id: 'A', dependsOn: [] },
      { id: 'F', dependsOn: [] },
      { id: 'C', dependsOn: ['A'] },
      { id: 'M', dependsOn: ['C', 'F'] },
      { id: 'F2', dependsOn: ['F'] },
      { id: 'P', dependsOn: [] },
    ]
    const layout = computeDAGLayout(items)
    expect(laneOf(layout, 'A')).toBe(0)
    expect(laneOf(layout, 'F')).toBe(1)
    expect(laneOf(layout, 'C')).toBe(0)
    expect(laneOf(layout, 'M')).toBe(1)
    expect(laneOf(layout, 'F2')).toBe(2)
    expect(laneOf(layout, 'P')).toBe(0)
    expectCleanLayout(items, 'parentless-anchor-reclaim')
  })
})

describe('allocateLane scan order (right side first)', () => {
  it('prefers the eligible freed lane on the anchor\'s right over the one on its left', () => {
    // W0 frees lane 0 (its merge parks on K's lane, completing U0); W frees
    // lane 2 (lane 0 is ineligible for it: U0 sits below K's fork row). By
    // the time D2 allocates around anchor lane 1, both neighbors are free
    // with their last occupants above P's row, and D1 owns the anchor. The
    // documented order (anchor+1 before anchor-1) must return lane 2 — a
    // left-first scan would return 0, a broken anchor pickup would return 1.
    const items: DAGItem[] = [
      { id: 'A', dependsOn: [] }, // lane 0
      { id: 'K', dependsOn: [] }, // lane 1 — fork (W0, W)
      { id: 'B', dependsOn: ['A'] }, // lane 0
      { id: 'U0', dependsOn: ['B'] }, // lane 0, last occupant at row 3
      { id: 'V', dependsOn: [] }, // fresh lane 2 (lane 0 not free yet)
      { id: 'W0', dependsOn: ['K', 'U0'] }, // parks lane 1 (K owns it), frees lane 0
      { id: 'W', dependsOn: ['K', 'V'] }, // fresh lane 3 (lane 0 ineligible), frees lane 2
      { id: 'P', dependsOn: ['W0'] }, // lane 1 — fork (D1, D2)
      { id: 'D1', dependsOn: ['P'] }, // lane 1 (takes ownership from P)
      { id: 'D2', dependsOn: ['P'] }, // allocateLane(1, row 7) -> lane 2
    ]
    const layout = computeDAGLayout(items)
    expect(laneOf(layout, 'A')).toBe(0)
    expect(laneOf(layout, 'K')).toBe(1)
    expect(laneOf(layout, 'B')).toBe(0)
    expect(laneOf(layout, 'U0')).toBe(0)
    expect(laneOf(layout, 'V')).toBe(2)
    expect(laneOf(layout, 'W0')).toBe(1)
    expect(laneOf(layout, 'W')).toBe(3)
    expect(laneOf(layout, 'P')).toBe(1)
    expect(laneOf(layout, 'D1')).toBe(1)
    expect(laneOf(layout, 'D2')).toBe(2)
    expectCleanLayout(items, 'right-side-first')
  })
})

describe('lane-continuation broken marking (BUG4)', () => {
  // B is an independent root whose lane 1 is freed when C parks on A's lane 0;
  // F later allocates that freed lane, so the lane-1 continuation runs from B
  // (row 1) to F (row 5) with no dependency edge between the two occupants.
  const reuseItems: DAGItem[] = [
    { id: 'A', dependsOn: [] },
    { id: 'B', dependsOn: [] },
    { id: 'C', dependsOn: ['A', 'B'] },
    { id: 'D', dependsOn: ['A'] },
    { id: 'E', dependsOn: ['D'] },
    { id: 'F', dependsOn: ['D', 'E'] },
  ]

  it('marks a lane continuation between unrelated occupants broken:true', () => {
    const layout = computeDAGLayout(reuseItems)
    expect(laneOf(layout, 'F')).toBe(1)
    const continuation = layout.connectors.find(c => c.type === 'vertical' && c.fromLane === 1 && c.fromRow === 1)
    // gapPx 72 = the midpoint of the bare stretch (53..91) between the merge
    // tail C←B ink (ending one corner radius above its run, 53) and the fork
    // drop F←D ink (starting one radius below its run, 91)
    expect(continuation).toEqual({ fromLane: 1, toLane: 1, fromRow: 1, toRow: 5, type: 'vertical', broken: true, gapPx: 72 })
    expectCleanLayout(reuseItems, 'broken-reuse')
  })

  it('leaves edge-backed verticals untouched (no broken flag anywhere else)', () => {
    const layout = computeDAGLayout(reuseItems)
    for (const c of layout.connectors) {
      if (c.broken !== true) expect(c.broken).toBeUndefined()
    }
    // the two edge-backed verticals keep their exact pre-BUG4 shape
    const edgeBacked = layout.connectors.filter(c => c.type === 'vertical' && c.broken === undefined)
    expect(edgeBacked).toEqual([
      { fromLane: 0, toLane: 0, fromRow: 0, toRow: 2, type: 'vertical' },
      { fromLane: 2, toLane: 2, fromRow: 3, toRow: 4, type: 'vertical' },
    ])
  })

  it('diamond layout: all continuations are solid (edge-backed, no broken flag)', () => {
    const items: DAGItem[] = [
      { id: 'A', dependsOn: [] },
      { id: 'B', dependsOn: ['A'] },
      { id: 'C', dependsOn: ['A'] },
      { id: 'D', dependsOn: ['B', 'C'] },
    ]
    const layout = computeDAGLayout(items)
    expect(layout.connectors.some(c => c.broken === true)).toBe(false)
    expect(layout.connectors.filter(c => c.type === 'vertical')).toEqual([
      { fromLane: 0, toLane: 0, fromRow: 0, toRow: 1, type: 'vertical' },
      { fromLane: 0, toLane: 0, fromRow: 1, toRow: 3, type: 'vertical' },
    ])
    expectCleanLayout(items, 'diamond-solid')
  })

  it('places the broken gap in the largest bare stretch (review repro: merge tail cannot cover it)', () => {
    // The review's contract-valid, in-order repro: X reclaims the freed lane 0,
    // its broken continuation (rows 1→5) used to gap at the segment midpoint
    // (84, px 81..87) — fully covered by the merge tail Q→C ink [36..101], so
    // the false continuation read as one solid Q→X line.
    const items: DAGItem[] = [
      { id: 'R', dependsOn: [] },
      { id: 'Q', dependsOn: ['R'] },
      { id: 'A', dependsOn: ['R'] },
      { id: 'B', dependsOn: ['A'] },
      { id: 'C', dependsOn: ['Q', 'R'] },
      { id: 'X', dependsOn: [] },
    ]
    const layout = computeDAGLayout(items)
    expect(laneOf(layout, 'X')).toBe(0)
    const continuation = layout.connectors.find(c => c.type === 'vertical' && c.broken === true)
    // gapPx 116.5 = the midpoint of the bare stretch (101..132) below the
    // merge tail Q→C ink (which ends one corner radius above its run at 101)
    expect(continuation).toEqual({ fromLane: 0, toLane: 0, fromRow: 1, toRow: 5, type: 'vertical', broken: true, gapPx: 116.5 })
    expect(continuationDefects(items, layout)).toEqual([])
    expectCleanLayout(items, 'review-repro-covered-gap')
  })
})

describe('side-aware fork fan', () => {
  it('stacks exitDy +3px per earlier overlapping fork run from the same parent', () => {
    const items: DAGItem[] = [
      { id: 'A', dependsOn: [] },
      { id: 'B', dependsOn: ['A'] },
      { id: 'C', dependsOn: ['A'] },
      { id: 'D', dependsOn: ['A'] },
      { id: 'E', dependsOn: ['A'] },
    ]
    const layout = computeDAGLayout(items)
    expect(layout.nodes.map(n => n.lane)).toEqual([0, 0, 1, 2, 3])
    const forks = layout.connectors.filter(c => c.type === 'fork').sort((a, b) => a.toRow - b.toRow)
    expect(forks.map(f => f.toLane)).toEqual([1, 2, 3])
    expect(forks.map(f => f.exitDy)).toEqual([0, 3, 6])
  })

  it('stacks entryDy +3px per earlier overlapping merge run into the same child', () => {
    const items: DAGItem[] = [
      { id: 'A', dependsOn: [] },
      { id: 'B', dependsOn: ['A'] },
      { id: 'C', dependsOn: ['A'] },
      { id: 'K', dependsOn: ['B', 'C', 'A'] },
    ]
    const layout = computeDAGLayout(items)
    const merges = layout.connectors.filter(c => c.type === 'merge').sort((a, b) => a.fromRow - b.fromRow)
    // B (row 1) merges first at K's row: dy 0; C (row 2) overlaps it: dy 3
    expect(merges.map(m => m.fromRow)).toEqual([1, 2])
    expect(merges.map(m => m.entryDy)).toEqual([0, 3])
  })

  it('caps fan dy so stacked runs stay inside their 24px row band', () => {
    // 10 roots merging into one child: dy stacks 0,3,6,9 then holds at the
    // cap instead of walking out of the row band and across the next row's
    // nodes (previously it reached 24 at ten parents).
    const items: DAGItem[] = [
      ...Array.from({ length: 10 }, (_, i) => ({ id: `p${i}`, dependsOn: [] as string[] })),
      { id: 'K', dependsOn: Array.from({ length: 10 }, (_, i) => `p${i}`) },
    ]
    const layout = computeDAGLayout(items)
    const merges = layout.connectors.filter(c => c.type === 'merge').sort((a, b) => a.fromRow - b.fromRow)
    expect(merges).toHaveLength(9) // p0's edge is vertical — K parks on its lane
    expect(merges.map(m => m.entryDy)).toEqual([0, 3, 6, 9, 9, 9, 9, 9, 9])
  })
})

describe('input normalization (declaration order & duplicates)', () => {
  // The declare_plan contract guarantees a DAG, not topological declaration
  // order. A forward reference used to send allocateLane into a synchronous
  // infinite loop (NaN anchor): this case is the canary — a regression here
  // wedges the whole test run instead of failing a single test.
  it('terminates on a forward reference and lays the child out as a root', () => {
    const items: DAGItem[] = [
      { id: 'b', dependsOn: ['a'] },
      { id: 'a', dependsOn: [] },
    ]
    const layout = computeDAGLayout(items)
    expect(layout.nodes).toEqual([
      { id: 'b', lane: 0 },
      { id: 'a', lane: 1 },
    ])
    expect(layout.connectors).toEqual([])
    expectCleanLayout(items, 'forward-ref')
  })

  it('draws back-references and drops forward ones in mixed declaration order', () => {
    // Diamond A→B, A→C, B+C→D declared as [A, D, B, C]: the A→B/A→C
    // back-references render, the B→D/C→D forward references drop, and D
    // lays out as a root.
    const items: DAGItem[] = [
      { id: 'A', dependsOn: [] },
      { id: 'D', dependsOn: ['B', 'C'] },
      { id: 'B', dependsOn: ['A'] },
      { id: 'C', dependsOn: ['A'] },
    ]
    const layout = computeDAGLayout(items)
    expect(layout.nodes.map(n => n.lane)).toEqual([0, 1, 0, 2])
    const edges = layout.connectors.map(c => `${c.fromRow}->${c.toRow}`).sort()
    expect(edges).toEqual(['0->2', '0->3'])
    expectCleanLayout(items, 'mixed-order')
  })

  it('collapses duplicate depends_on entries to a single connector', () => {
    const items: DAGItem[] = [
      { id: 'a', dependsOn: [] },
      { id: 'b', dependsOn: ['a', 'a'] },
    ]
    const layout = computeDAGLayout(items)
    expect(layout.nodes).toEqual([
      { id: 'a', lane: 0 },
      { id: 'b', lane: 0 },
    ])
    expect(layout.connectors).toEqual([
      { fromLane: 0, toLane: 0, fromRow: 0, toRow: 1, type: 'vertical' },
    ])
    expectCleanLayout(items, 'duplicate-dep')
  })

  it('drops a reference to an undeclared id', () => {
    const items: DAGItem[] = [
      { id: 'a', dependsOn: [] },
      { id: 'b', dependsOn: ['a', 'ghost'] },
    ]
    const layout = computeDAGLayout(items)
    expect(layout.nodes.map(n => n.lane)).toEqual([0, 0])
    expect(layout.connectors).toEqual([
      { fromLane: 0, toLane: 0, fromRow: 0, toRow: 1, type: 'vertical' },
    ])
    expectCleanLayout(items, 'undeclared-ref')
  })
})

// --- Invariant battery ------------------------------------------------------

const ROW_PX = 24
/** Cap offset from FAN_DY_MAX_PX in dagLayout.ts — the only dy where H overlaps are by design. */
const FAN_DY_CAP_OFFSET_PX = 9

interface VSeg { lane: number; top: number; bottom: number }
interface HSeg { yPx: number; dy: number; lo: number; hi: number; id: string }

function geometry(layout: DAGLayout): { vsegs: VSeg[]; hsegs: HSeg[] } {
  const vsegs: VSeg[] = []
  const hsegs: HSeg[] = []
  for (const c of layout.connectors) {
    const lo = Math.min(c.fromLane, c.toLane)
    const hi = Math.max(c.fromLane, c.toLane)
    if (c.type === 'vertical') {
      vsegs.push({ lane: c.fromLane, top: Math.min(c.fromRow, c.toRow), bottom: Math.max(c.fromRow, c.toRow) })
    } else if (c.type === 'fork') {
      hsegs.push({ yPx: c.fromRow * ROW_PX + (c.exitDy ?? 0), dy: c.exitDy ?? 0, lo, hi, id: `fork ${c.fromLane}->${c.toLane}@r${c.fromRow}` })
      vsegs.push({ lane: c.toLane, top: c.fromRow, bottom: c.toRow })
    } else {
      hsegs.push({ yPx: c.toRow * ROW_PX + (c.entryDy ?? 0), dy: c.entryDy ?? 0, lo, hi, id: `merge ${c.fromLane}->${c.toLane}@r${c.toRow}` })
      vsegs.push({ lane: c.fromLane, top: c.fromRow, bottom: c.toRow })
    }
  }
  return { vsegs, hsegs }
}

function findDefects(layout: DAGLayout): string[] {
  const defects: string[] = []
  const nodes = layout.nodes.map((n, row) => ({ ...n, row }))
  const { vsegs, hsegs } = geometry(layout)

  // 1. No vertical segment may pass strictly through a node.
  for (const s of vsegs) {
    for (const nd of nodes) {
      if (nd.lane === s.lane && s.top < nd.row && nd.row < s.bottom) {
        defects.push(`pierced node ${nd.id} (lane ${nd.lane}, row ${nd.row}) inside vertical [${s.top}..${s.bottom}]`)
      }
    }
  }

  // 2. No two horizontal runs at the same y may overlap in x — except at the
  //    fan-dy cap itself (FAN_DY_MAX_PX in dagLayout.ts): fans wider than the
  //    row band share the capped y by design; below the cap the stacking
  //    search keeps every y unique.
  for (let i = 0; i < hsegs.length; i++) {
    for (let j = i + 1; j < hsegs.length; j++) {
      const a = hsegs[i]!, b = hsegs[j]!
      const atFanCap = a.dy === FAN_DY_CAP_OFFSET_PX && b.dy === FAN_DY_CAP_OFFSET_PX
      if (a.yPx === b.yPx && !atFanCap && Math.min(a.hi, b.hi) > Math.max(a.lo, b.lo)) {
        defects.push(`H overlap at y=${a.yPx}: ${a.id} vs ${b.id}`)
      }
    }
  }

  // 3. Vertical overlaps are allowed only between segments sharing an endpoint node.
  const nodeAt = new Set(nodes.map(n => `${n.lane}:${n.row}`))
  for (let i = 0; i < vsegs.length; i++) {
    for (let j = i + 1; j < vsegs.length; j++) {
      const a = vsegs[i]!, b = vsegs[j]!
      if (a.lane !== b.lane) continue
      const ovTop = Math.max(a.top, b.top)
      const ovBottom = Math.min(a.bottom, b.bottom)
      if (ovTop >= ovBottom) continue
      const sharesEndpointNode = [a.top, a.bottom].some(r =>
        nodeAt.has(`${a.lane}:${r}`) && (r === b.top || r === b.bottom),
      )
      if (!sharesEndpointNode) {
        defects.push(`V overlap on lane ${a.lane} in [${ovTop}..${ovBottom}] without shared endpoint node`)
      }
    }
  }
  return defects
}

function expectCleanLayout(items: DAGItem[], label: string): void {
  const layout = computeDAGLayout(items)
  const defects = [...findDefects(layout), ...continuationDefects(items, layout)].map(d => `${label}: ${d}`)
  expect(defects).toEqual([])
}

/**
 * BUG4 invariant: a lane-continuation vertical carries `broken: true` exactly
 * when its two endpoint nodes have no dependency edge between them —
 * edge-backed continuations stay unmarked (solid, unchanged shape).
 *
 * Gap-visibility invariant (PR review): a broken continuation's visible gap
 * (`gapPx`, ± BROKEN_GAP_PX/2) must sit in a stretch of its lane no other
 * segment's ink covers — ink spanning the gap reconnects the stubs and the
 * false continuation reads as solid. The px ink mirror below follows the
 * buildEdgePath shapes: verticals span node-center to node-center; a corner's
 * arc leaves the lane line, so a fork's vertical ink on the child lane starts
 * one corner radius (7 = LANE_WIDTH / 2) below its run and a merge's ink on
 * the parent lane ends one radius above it; a horizontal run crosses strictly
 * intermediate lanes at its final y and would bridge a gap within half a gap
 * width of that y.
 */
const GAP_HALF_PX = BROKEN_GAP_PX / 2
const ROW_Y_PX = (row: number): number => row * ROW_PX + ROW_PX / 2

function inkOnLane(c: DAGConnector, lane: number): [number, number][] {
  const spans: [number, number][] = []
  const lo = Math.min(c.fromLane, c.toLane)
  const hi = Math.max(c.fromLane, c.toLane)
  if (c.type === 'vertical') {
    if (c.fromLane !== lane) return spans
    spans.push([ROW_Y_PX(Math.min(c.fromRow, c.toRow)), ROW_Y_PX(Math.max(c.fromRow, c.toRow))])
  } else if (c.type === 'fork') {
    const hy = ROW_Y_PX(c.fromRow) + (c.exitDy ?? 0)
    if (c.toLane === lane) spans.push([hy + 7, ROW_Y_PX(c.toRow)])
    if (c.fromLane === lane && (c.exitDy ?? 0) > 0) spans.push([ROW_Y_PX(c.fromRow), hy])
    if (lane > lo && lane < hi) spans.push([hy - GAP_HALF_PX, hy + GAP_HALF_PX])
  } else {
    const hy = ROW_Y_PX(c.toRow) + (c.entryDy ?? 0)
    if (c.fromLane === lane) spans.push([ROW_Y_PX(c.fromRow), hy - 7])
    if (c.toLane === lane && (c.entryDy ?? 0) > 0) spans.push([ROW_Y_PX(c.toRow), hy])
    if (lane > lo && lane < hi) spans.push([hy - GAP_HALF_PX, hy + GAP_HALF_PX])
  }
  return spans
}

function continuationDefects(items: DAGItem[], layout: DAGLayout): string[] {
  // Mirror the layout's edge set (drawnDeps): the invariant is about the
  // edges the layout draws, not the raw depends_on lists.
  const depsOf = drawnDeps(items)
  const children = new Map<string, string[]>()
  for (let i = 0; i < items.length; i++) {
    for (const dep of depsOf[i]!) {
      const list = children.get(dep) ?? []
      list.push(items[i]!.id)
      children.set(dep, list)
    }
  }
  const defects: string[] = []
  for (const c of layout.connectors) {
    if (c.type !== 'vertical' || c.fromLane !== c.toLane) continue
    const from = Math.min(c.fromRow, c.toRow)
    const to = Math.max(c.fromRow, c.toRow)
    const upper = items[from]!.id
    const lower = items[to]!.id
    const edgeBacked =
      (children.get(upper)?.includes(lower) ?? false) ||
      (children.get(lower)?.includes(upper) ?? false)
    if (!edgeBacked && c.broken !== true) {
      defects.push(`solid continuation between unrelated nodes ${upper} (r${from}) and ${lower} (r${to}) on lane ${c.fromLane}`)
    }
    if (edgeBacked && c.broken === true) {
      defects.push(`edge-backed continuation ${upper}->${lower} wrongly marked broken`)
    }
    if (c.broken !== true) continue
    const y1 = ROW_Y_PX(from)
    const y2 = ROW_Y_PX(to)
    if (c.gapPx === undefined || !Number.isFinite(c.gapPx)) {
      defects.push(`broken continuation ${upper} (r${from}) -> ${lower} (r${to}) on lane ${c.fromLane} has no gap position`)
      continue
    }
    if (c.gapPx < y1 || c.gapPx > y2) {
      defects.push(`broken continuation ${upper} -> ${lower} on lane ${c.fromLane}: gap ${c.gapPx} outside the segment [${y1}..${y2}]`)
      continue
    }
    const gapLo = c.gapPx - GAP_HALF_PX
    const gapHi = c.gapPx + GAP_HALF_PX
    for (const o of layout.connectors) {
      if (o === c) continue
      for (const [lo, hi] of inkOnLane(o, c.fromLane)) {
        if (Math.min(hi, gapHi) > Math.max(lo, gapLo)) {
          defects.push(
            `gap [${gapLo}..${gapHi}] of broken lane-${c.fromLane} continuation ${upper} -> ${lower} `
              + `covered by ${o.type} ${o.fromLane}->${o.toLane} (rows ${o.fromRow}->${o.toRow}) ink [${lo}..${hi}]`,
          )
        }
      }
    }
  }
  return defects
}

// --- Seeded-random corpus ----------------------------------------------------

/**
 * mulberry32 — tiny deterministic PRNG. The battery replays a fixed seed list,
 * so every run (local and CI) exercises the exact same graph corpus.
 */
function mulberry32(seed: number): () => number {
  let a = seed >>> 0
  return () => {
    a = (a + 0x6d2b79f5) | 0
    let t = Math.imul(a ^ (a >>> 15), 1 | a)
    t = (t + Math.imul(t ^ (t >>> 7), 61 | t)) ^ t
    return ((t ^ (t >>> 14)) >>> 0) / 4294967296
  }
}

/** Fixed seed list: each seed permanently pins its slice of the corpus. */
const SWEEP_SEEDS: readonly number[] = [0xd46e6a, 0x1b873593, 0x9e3779b9, 0x85ebca6b, 0xcc9e2d51]
/** Random graphs generated per seed. */
const GRAPHS_PER_SEED = 100
/** Shuffled (out-of-order declaration) graphs generated per seed. */
const SHUFFLED_GRAPHS_PER_SEED = 20

/** Random topologically ordered DAG: node i depends on up to 3 earlier nodes. */
function randomDag(rng: () => number, n: number): DAGItem[] {
  const items: DAGItem[] = []
  for (let i = 0; i < n; i++) {
    const maxParents = Math.min(i, 3)
    const count = Math.floor(rng() * (maxParents + 1))
    const pool = Array.from({ length: i }, (_, k) => k)
    const deps: string[] = []
    for (let k = 0; k < count && pool.length > 0; k++) {
      const idx = Math.floor(rng() * pool.length)
      deps.push(String(pool[idx]))
      pool.splice(idx, 1)
    }
    items.push({ id: String(i), dependsOn: deps })
  }
  return items
}

/** All battery defects across the whole corpus of one seed. */
function sweepDefects(seed: number): string[] {
  const rng = mulberry32(seed)
  const defects: string[] = []
  for (let g = 0; g < GRAPHS_PER_SEED; g++) {
    const n = 5 + Math.floor(rng() * 36)
    const items = randomDag(rng, n)
    const layout = computeDAGLayout(items)
    const ctx = `seed 0x${seed.toString(16)} graph #${g} (n=${n})`
    defects.push(...findDefects(layout).map(d => `${ctx}: ${d}`))
    defects.push(...continuationDefects(items, layout).map(d => `${ctx}: ${d}`))
  }
  return defects
}

/**
 * Shuffled corpus: a random topological DAG re-declared in a random order.
 * Normalization keeps only the back-references (roughly half the edges), so
 * this hunts the invariant set under out-of-order input — the class of input
 * the topological sweep never exercises.
 */
function sweepShuffledDefects(seed: number): string[] {
  const gen = mulberry32(seed + 0x2545f491)
  const defects: string[] = []
  for (let g = 0; g < SHUFFLED_GRAPHS_PER_SEED; g++) {
    const items = randomDag(gen, 5 + Math.floor(gen() * 36))
    for (let i = items.length - 1; i > 0; i--) {
      const j = Math.floor(gen() * (i + 1))
      ;[items[i], items[j]] = [items[j]!, items[i]!]
    }
    const layout = computeDAGLayout(items)
    const ctx = `shuffled seed 0x${seed.toString(16)} graph #${g} (n=${items.length})`
    defects.push(...findDefects(layout).map(d => `${ctx}: ${d}`))
    defects.push(...continuationDefects(items, layout).map(d => `${ctx}: ${d}`))
  }
  return defects
}

describe('layout invariant battery', () => {
  // T1: diamond with freed-lane reuse — C's lane 1 is freed when D parks on
  // B's lane 0, and F reuses it for D's fork.
  it('T1 diamond with fork/merge and freed-lane reuse', () => {
    const items: DAGItem[] = [
      { id: 'A', dependsOn: [] },
      { id: 'B', dependsOn: ['A'] },
      { id: 'C', dependsOn: ['A'] },
      { id: 'D', dependsOn: ['B', 'C'] },
      { id: 'E', dependsOn: ['D'] },
      { id: 'F', dependsOn: ['D', 'E'] },
    ]
    const layout = computeDAGLayout(items)
    expect(laneOf(layout, 'F')).toBe(1)
    // C (row 2) and F (row 5) share lane 1 with no dependency edge between
    // them — the lane continuation must be marked broken (BUG4). gapPx 85.5 =
    // the midpoint of the bare stretch (77..94) between the merge corner C→D
    // and the corner of the fork drop F←D (entryDy 0, exitDy 3).
    const continuation = layout.connectors.find(c => c.type === 'vertical' && c.fromLane === 1)
    expect(continuation).toEqual({ fromLane: 1, toLane: 1, fromRow: 2, toRow: 5, type: 'vertical', broken: true, gapPx: 85.5 })
    expectCleanLayout(items, 'T1')
  })

  it('T2 multi-parent parking across owned and ineligible parent lanes', () => {
    const items: DAGItem[] = [
      { id: 'A', dependsOn: [] },
      { id: 'B', dependsOn: ['A'] },
      { id: 'C', dependsOn: ['B'] },
      { id: 'D', dependsOn: ['A'] },
      { id: 'E', dependsOn: ['D'] },
      { id: 'F', dependsOn: ['B', 'E'] },
    ]
    expectCleanLayout(items, 'T2')
  })

  it('T3 freed-lane reuse with stale occupant below the fork parent', () => {
    const items: DAGItem[] = [
      { id: 'A', dependsOn: [] },
      { id: 'Y', dependsOn: [] },
      { id: 'B', dependsOn: ['A', 'Y'] },
      { id: 'C', dependsOn: ['B'] },
      { id: 'E', dependsOn: ['A'] },
    ]
    expectCleanLayout(items, 'T3')
  })

  it('T4 wide fan with exitDy and entryDy stacking', () => {
    const items: DAGItem[] = [
      { id: 'A', dependsOn: [] },
      { id: 'B', dependsOn: ['A'] },
      { id: 'C', dependsOn: ['A'] },
      { id: 'D', dependsOn: ['A'] },
      { id: 'E', dependsOn: ['A'] },
      { id: 'P', dependsOn: ['B'] },
      { id: 'Q', dependsOn: ['C'] },
      { id: 'R', dependsOn: ['D'] },
      { id: 'S', dependsOn: ['E'] },
      { id: 'K', dependsOn: ['P', 'Q', 'R', 'S', 'A'] },
    ]
    const layout = computeDAGLayout(items)
    expect(laneOf(layout, 'K')).toBe(4)
    const ak = layout.connectors.find(c => c.fromRow === 0 && c.toRow === 9)
    expect(ak?.type).toBe('fork')
    expect(ak?.exitDy).toBe(9)
    const merges = layout.connectors.filter(c => c.type === 'merge').sort((a, b) => a.fromRow - b.fromRow)
    expect(merges.map(m => m.entryDy)).toEqual([0, 3, 6, 9])
    expectCleanLayout(items, 'T4')
  })

  it('battery self-check: the pierced-node detector fires on a known-bad layout', () => {
    const badLayout: DAGLayout = {
      nodes: [{ id: 'A', lane: 0 }, { id: 'B', lane: 0 }, { id: 'C', lane: 0 }],
      connectors: [
        { fromLane: 0, toLane: 0, fromRow: 0, toRow: 1, type: 'vertical' },
        // hand-written vertical A→C passing straight through B
        { fromLane: 0, toLane: 0, fromRow: 0, toRow: 2, type: 'vertical' },
      ],
      maxLane: 0,
    }
    expect(findDefects(badLayout).some(d => d.includes('pierced node B'))).toBe(true)
  })

  it('battery self-check: every detector fires on a hand-bad layout', () => {
    // H overlap: two fork runs stacked at the same y with overlapping x-extents
    const hBad: DAGLayout = {
      nodes: [
        { id: 'P', lane: 0 },
        { id: 'Q', lane: 1 },
        { id: 'R', lane: 2 },
        { id: 'S', lane: 3 },
      ],
      connectors: [
        { fromLane: 0, toLane: 2, fromRow: 0, toRow: 2, type: 'fork', exitDy: 0 },
        { fromLane: 1, toLane: 3, fromRow: 0, toRow: 3, type: 'fork', exitDy: 0 },
      ],
      maxLane: 3,
    }
    const hDefects = findDefects(hBad)
    expect(hDefects.some(d => d.startsWith('H overlap'))).toBe(true)
    expect(hDefects.some(d => d.includes('pierced'))).toBe(false)

    // collinear V overlap: two verticals sharing lane 1 and overlapping rows
    // 1..2, where no lane-1 node sits — no shared endpoint node to excuse it
    const vBad: DAGLayout = {
      nodes: [
        { id: 'A', lane: 1 },
        { id: 'X', lane: 0 },
        { id: 'Y', lane: 0 },
        { id: 'C', lane: 1 },
      ],
      connectors: [
        { fromLane: 1, toLane: 1, fromRow: 0, toRow: 3, type: 'vertical' },
        { fromLane: 1, toLane: 1, fromRow: 1, toRow: 2, type: 'vertical' },
      ],
      maxLane: 1,
    }
    const vDefects = findDefects(vBad)
    expect(vDefects.some(d => d.startsWith('V overlap'))).toBe(true)
    expect(vDefects.some(d => d.includes('pierced'))).toBe(false)

    // false continuation: a solid vertical whose endpoint nodes share no edge
    const fcItems: DAGItem[] = [
      { id: 'A', dependsOn: [] },
      { id: 'B', dependsOn: ['A'] },
      { id: 'C', dependsOn: ['B'] },
    ]
    const fcLayout = computeDAGLayout(fcItems)
    fcLayout.connectors.push({ fromLane: 0, toLane: 0, fromRow: 0, toRow: 2, type: 'vertical' })
    expect(
      continuationDefects(fcItems, fcLayout).some(d => d.includes('solid continuation between unrelated nodes A')),
    ).toBe(true)

    // ...and its dual: marking an edge-backed vertical broken is a defect too
    const wbItems: DAGItem[] = [{ id: 'A', dependsOn: [] }, { id: 'B', dependsOn: ['A'] }]
    const wbLayout = computeDAGLayout(wbItems)
    const edgeVertical = wbLayout.connectors.find(c => c.type === 'vertical')
    expect(edgeVertical).toBeDefined()
    edgeVertical!.broken = true
    expect(continuationDefects(wbItems, wbLayout).some(d => d.includes('wrongly marked broken'))).toBe(true)

    // covered gap: the review repro hand-built — a broken lane-0 continuation
    // (rows 1→5) gapped at the segment midpoint (84, px 81..87) sits under the
    // merge tail Q→C ink [36..101]; the gap detector must fire on it
    const cgItems: DAGItem[] = [
      { id: 'R', dependsOn: [] },
      { id: 'Q', dependsOn: ['R'] },
      { id: 'A', dependsOn: ['R'] },
      { id: 'B', dependsOn: ['A'] },
      { id: 'C', dependsOn: ['Q', 'R'] },
      { id: 'X', dependsOn: [] },
    ]
    const cgLayout: DAGLayout = {
      nodes: [
        { id: 'R', lane: 0 },
        { id: 'Q', lane: 0 },
        { id: 'A', lane: 1 },
        { id: 'B', lane: 1 },
        { id: 'C', lane: 2 },
        { id: 'X', lane: 0 },
      ],
      connectors: [
        { fromLane: 0, toLane: 0, fromRow: 0, toRow: 1, type: 'vertical' },
        { fromLane: 0, toLane: 1, fromRow: 0, toRow: 2, type: 'fork', exitDy: 0 },
        { fromLane: 1, toLane: 1, fromRow: 2, toRow: 3, type: 'vertical' },
        { fromLane: 0, toLane: 2, fromRow: 1, toRow: 4, type: 'merge', entryDy: 0 },
        { fromLane: 0, toLane: 2, fromRow: 0, toRow: 4, type: 'fork', exitDy: 3 },
        { fromLane: 0, toLane: 0, fromRow: 1, toRow: 5, type: 'vertical', broken: true, gapPx: 84 },
      ],
      maxLane: 2,
    }
    const cgDefects = continuationDefects(cgItems, cgLayout)
    expect(cgDefects.some(d => d.includes('covered by merge'))).toBe(true)
    // ...and the layout-placed gap (116.5, the bare stretch below the merge
    // tail) is clean on the same hand layout
    const placedGap = { ...cgLayout.connectors[5]!, gapPx: 116.5 }
    const cgPlaced: DAGLayout = { ...cgLayout, connectors: [...cgLayout.connectors.slice(0, 5), placedGap] }
    expect(continuationDefects(cgItems, cgPlaced)).toEqual([])
  })

  for (const seed of SWEEP_SEEDS) {
    it(
      `seeded random sweep (seed 0x${seed.toString(16)}): zero pierced nodes, zero H overlaps, `
        + 'V overlaps only at shared endpoint nodes, zero solid continuations without an edge',
      () => {
        expect(sweepDefects(seed)).toEqual([])
      },
    )
  }

  for (const seed of SWEEP_SEEDS) {
    it(
      `shuffled declaration-order sweep (seed 0x${seed.toString(16)}): the same invariants `
        + 'hold after normalization drops the forward references',
      () => {
        expect(sweepShuffledDefects(seed)).toEqual([])
      },
    )
  }
})
