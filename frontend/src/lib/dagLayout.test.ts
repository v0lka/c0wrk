import { describe, it, expect } from 'vitest'
import { computeDAGLayout, type DAGItem, type DAGLayout } from './dagLayout'

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
    expect(continuation).toEqual({ fromLane: 1, toLane: 1, fromRow: 1, toRow: 5, type: 'vertical', broken: true })
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
 */
function continuationDefects(items: DAGItem[], layout: DAGLayout): string[] {
  const children = new Map<string, string[]>()
  for (const item of items) {
    for (const dep of item.dependsOn) {
      const list = children.get(dep) ?? []
      list.push(item.id)
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
    // them — the lane continuation must be marked broken (BUG4).
    const continuation = layout.connectors.find(c => c.type === 'vertical' && c.fromLane === 1)
    expect(continuation).toEqual({ fromLane: 1, toLane: 1, fromRow: 2, toRow: 5, type: 'vertical', broken: true })
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
})
