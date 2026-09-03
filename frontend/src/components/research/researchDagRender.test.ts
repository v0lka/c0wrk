import { describe, it, expect } from 'vitest'
import type { HypothesisGraph } from '@/types/models'
import {
  NODE_R,
  COLUMN_W,
  ROW_H,
  LABEL_GAP_X,
  LABEL_MAX_CHARS,
  LABEL_CHAR_W,
  LEFT_PAD,
  TOP_PAD,
  buildParentMap,
  assignLevels,
  layoutDag,
  xFor,
  yFor,
  edgePathH,
  statusColorVar,
  formatRate,
  isTerminal,
  findAllRootToLeafPaths,
  findIncompletePaths,
  filterPaths,
  buildDisplayGraph,
  mergePathsToTree,
  projectDir,
  projectFilePaths,
  type PathEntry,
  type MergedTreeNode,
} from './researchDagRender'

function graphOf(
  nodes: { id: string; title?: string; status?: string; parents?: string[] }[],
  edges?: { from: string; to: string }[],
): HypothesisGraph {
  return {
    nodes: nodes.map((n) => ({
      id: n.id,
      title: n.title ?? n.id,
      status: n.status ?? 'open',
      parents: n.parents,
    })),
    edges: edges ?? [],
  }
}

describe('statusColorVar', () => {
  it('maps each lifecycle status to its design token', () => {
    expect(statusColorVar('open')).toBe('var(--color-info)')
    expect(statusColorVar('in-progress')).toBe('var(--color-warning)')
    expect(statusColorVar('confirmed')).toBe('var(--color-success)')
    expect(statusColorVar('refuted')).toBe('var(--color-destructive)')
    expect(statusColorVar('cancelled')).toBe('var(--color-muted-foreground)')
  })

  it('falls back to muted for unknown status', () => {
    expect(statusColorVar('bogus')).toBe('var(--color-muted-foreground)')
  })
})

describe('buildParentMap', () => {
  it('builds child→parents from edges', () => {
    const g = graphOf(
      [{ id: 'a' }, { id: 'b' }, { id: 'c' }],
      [
        { from: 'a', to: 'b' },
        { from: 'a', to: 'c' },
      ],
    )
    expect(buildParentMap(g)).toEqual(
      new Map([
        ['b', ['a']],
        ['c', ['a']],
      ]),
    )
  })

  it('falls back to node.parents when no edges', () => {
    const g = graphOf([{ id: 'a' }, { id: 'b', parents: ['a'] }])
    expect(buildParentMap(g)).toEqual(new Map([['b', ['a']]]))
  })

  it('drops edges referencing unknown nodes', () => {
    const g = graphOf([{ id: 'a' }], [{ from: 'a', to: 'ghost' }])
    expect(buildParentMap(g).size).toBe(0)
  })

  it('dedupes repeated parent edges', () => {
    const g = graphOf(
      [{ id: 'a' }, { id: 'b' }],
      [
        { from: 'a', to: 'b' },
        { from: 'a', to: 'b' },
      ],
    )
    expect(buildParentMap(g)).toEqual(new Map([['b', ['a']]]))
  })
})

describe('assignLevels', () => {
  it('puts roots at level 0 and children deeper', () => {
    const g = graphOf(
      [
        { id: 'root' },
        { id: 'mid', parents: ['root'] },
        { id: 'leaf', parents: ['mid'] },
      ],
    )
    const levels = assignLevels(g)
    expect(levels.get('root')).toBe(0)
    expect(levels.get('mid')).toBe(1)
    expect(levels.get('leaf')).toBe(2)
  })

  it('uses the longest path through multiple parents', () => {
    // diamond: a → b, a → c, b/c → d ; d's level should be 2 (longest)
    const g = graphOf(
      [{ id: 'a' }, { id: 'b' }, { id: 'c' }, { id: 'd' }],
      [
        { from: 'a', to: 'b' },
        { from: 'a', to: 'c' },
        { from: 'b', to: 'd' },
        { from: 'c', to: 'd' },
      ],
    )
    expect(assignLevels(g).get('d')).toBe(2)
  })

  it('breaks cycles instead of recursing forever', () => {
    // a → b → a (cycle). Must terminate and assign finite levels.
    const g = graphOf(
      [{ id: 'a' }, { id: 'b' }],
      [
        { from: 'a', to: 'b' },
        { from: 'b', to: 'a' },
      ],
    )
    const levels = assignLevels(g)
    expect(levels.has('a')).toBe(true)
    expect(levels.has('b')).toBe(true)
    // Both are finite, non-negative numbers.
    expect(levels.get('a')!).toBeGreaterThanOrEqual(0)
    expect(levels.get('b')!).toBeGreaterThanOrEqual(0)
  })
})

describe('xFor / yFor', () => {
  it('places each depth level in its own left-to-right column', () => {
    expect(xFor(0)).toBe(LEFT_PAD)
    expect(xFor(1) - xFor(0)).toBe(COLUMN_W)
    expect(xFor(2) - xFor(1)).toBe(COLUMN_W)
  })

  it('steps y by ROW_H per row slot', () => {
    expect(yFor(0)).toBe(TOP_PAD)
    expect(yFor(1) - yFor(0)).toBe(ROW_H)
  })

  it('sizes columns from the 26-char label budget plus a gap', () => {
    const labelZone = LABEL_GAP_X + LABEL_MAX_CHARS * LABEL_CHAR_W
    expect(COLUMN_W).toBe(labelZone + 20)
    // The label zone stays clear of the next column by the gap.
    expect(xFor(0) + labelZone).toBeLessThan(xFor(1))
  })
})

describe('edgePathH', () => {
  it('emits a horizontal cubic-bezier path string', () => {
    const p = edgePathH(0, 10, 40, 30)
    expect(p).toBe('M 0 10 C 20 10 20 30 40 30')
  })

  it('keeps each control point level with its endpoint (horizontal flow)', () => {
    const p = edgePathH(10, 5, 30, 25)
    // C xm y1, xm y2, x2 y2 with xm = midpoint of x1..x2.
    expect(p).toBe('M 10 5 C 20 5 20 25 30 25')
  })
})

describe('formatRate', () => {
  it('formats 0..1 as a rounded percentage', () => {
    expect(formatRate(0)).toBe('0%')
    expect(formatRate(0.5)).toBe('50%')
    expect(formatRate(0.666)).toBe('67%')
    expect(formatRate(1)).toBe('100%')
  })

  it('clamps non-finite / negative to 0%', () => {
    expect(formatRate(NaN)).toBe('0%')
    expect(formatRate(-0.2)).toBe('0%')
  })
})

describe('layoutDag', () => {
  it('returns an empty layout for no nodes', () => {
    const l = layoutDag(graphOf([]))
    expect(l.nodes).toEqual([])
    expect(l.edges).toEqual([])
    expect(l.width).toBe(0)
    expect(l.height).toBe(0)
  })

  it('positions a single root at the top-left origin', () => {
    const l = layoutDag(graphOf([{ id: 'a', status: 'confirmed' }]))
    expect(l.nodes).toHaveLength(1)
    expect(l.nodes[0]!.x).toBe(LEFT_PAD)
    expect(l.nodes[0]!.y).toBe(TOP_PAD)
    expect(l.nodes[0]!.status).toBe('confirmed')
  })

  it('assigns leaves consecutive row slots in DFS order (≥ ROW_H apart)', () => {
    // Tree: r → x, y ; x → x1, x2 ; y → y1, y2.
    const l = layoutDag(
      graphOf(
        [
          { id: 'r' },
          { id: 'x' },
          { id: 'y' },
          { id: 'x1' },
          { id: 'x2' },
          { id: 'y1' },
          { id: 'y2' },
        ],
        [
          { from: 'r', to: 'x' },
          { from: 'r', to: 'y' },
          { from: 'x', to: 'x1' },
          { from: 'x', to: 'x2' },
          { from: 'y', to: 'y1' },
          { from: 'y', to: 'y2' },
        ],
      ),
    )
    const leaves = ['x1', 'x2', 'y1', 'y2'].map((id) => l.nodes.find((n) => n.id === id)!)
    // No two leaves share a row slot.
    for (let i = 0; i < leaves.length; i++) {
      for (let j = i + 1; j < leaves.length; j++) {
        expect(Math.abs(leaves[i]!.y - leaves[j]!.y)).toBeGreaterThanOrEqual(ROW_H)
      }
    }
    // DFS order → x1 above x2 above y1 above y2.
    expect(leaves[0]!.y).toBeLessThan(leaves[1]!.y)
    expect(leaves[1]!.y).toBeLessThan(leaves[2]!.y)
    expect(leaves[2]!.y).toBeLessThan(leaves[3]!.y)
  })

  it('centers internal nodes on the mean of their children', () => {
    const l = layoutDag(
      graphOf(
        [
          { id: 'r' },
          { id: 'x' },
          { id: 'y' },
          { id: 'x1' },
          { id: 'x2' },
          { id: 'y1' },
          { id: 'y2' },
        ],
        [
          { from: 'r', to: 'x' },
          { from: 'r', to: 'y' },
          { from: 'x', to: 'x1' },
          { from: 'x', to: 'x2' },
          { from: 'y', to: 'y1' },
          { from: 'y', to: 'y2' },
        ],
      ),
    )
    const byId = new Map(l.nodes.map((n) => [n.id, n]))
    const x = byId.get('x')!
    const y = byId.get('y')!
    const r = byId.get('r')!
    const x1 = byId.get('x1')!
    const x2 = byId.get('x2')!
    const y1 = byId.get('y1')!
    const y2 = byId.get('y2')!
    expect(x.y).toBe((x1.y + x2.y) / 2)
    expect(y.y).toBe((y1.y + y2.y) / 2)
    expect(r.y).toBe((x.y + y.y) / 2)
  })

  it('places each node in its depth column (diamond)', () => {
    // diamond: a → b, a → c, b/c → d
    const l = layoutDag(
      graphOf(
        [{ id: 'a' }, { id: 'b' }, { id: 'c' }, { id: 'd' }],
        [
          { from: 'a', to: 'b' },
          { from: 'a', to: 'c' },
          { from: 'b', to: 'd' },
          { from: 'c', to: 'd' },
        ],
      ),
    )
    const byId = new Map(l.nodes.map((n) => [n.id, n]))
    expect(byId.get('a')!.x).toBe(xFor(0))
    expect(byId.get('b')!.x).toBe(xFor(1))
    expect(byId.get('c')!.x).toBe(xFor(1))
    expect(byId.get('d')!.x).toBe(xFor(2))
  })

  it('never lets a label zone reach the next column', () => {
    const l = layoutDag(
      graphOf([{ id: 'a' }, { id: 'b', title: 'x'.repeat(40), parents: ['a'] }]),
    )
    const labelZone = LABEL_GAP_X + LABEL_MAX_CHARS * LABEL_CHAR_W
    for (const n of l.nodes) {
      // Longest rendered label stays inside the node's own column.
      expect(n.x + labelZone).toBeLessThanOrEqual(xFor(n.level + 1))
    }
  })

  it('emits parent→child edges with parent-right / child-left anchors', () => {
    const l = layoutDag(
      graphOf([{ id: 'a' }, { id: 'b', parents: ['a'] }]),
    )
    expect(l.edges).toHaveLength(1)
    const e = l.edges[0]!
    expect(e.from).toBe('a')
    expect(e.to).toBe('b')
    const parent = l.nodes.find((n) => n.id === 'a')!
    const child = l.nodes.find((n) => n.id === 'b')!
    // Parent right-center → child left-center (left-to-right flow).
    expect(e.x1).toBe(parent.x + NODE_R)
    expect(e.y1).toBe(parent.y)
    expect(e.x2).toBe(child.x - NODE_R)
    expect(e.y2).toBe(child.y)
    expect(e.x2).toBeGreaterThan(e.x1)
  })

  it('renders a diamond with every edge anchored on its endpoints', () => {
    // a → b, a → c, b → d, c → d : d is visited once (via b) and shared.
    const l = layoutDag(
      graphOf(
        [{ id: 'a' }, { id: 'b' }, { id: 'c' }, { id: 'd' }],
        [
          { from: 'a', to: 'b' },
          { from: 'a', to: 'c' },
          { from: 'b', to: 'd' },
          { from: 'c', to: 'd' },
        ],
      ),
    )
    expect(l.nodes).toHaveLength(4)
    expect(l.edges).toHaveLength(4)
    const byId = new Map(l.nodes.map((n) => [n.id, n]))
    for (const e of l.edges) {
      const parent = byId.get(e.from)!
      const child = byId.get(e.to)!
      expect(e.x1).toBe(parent.x + NODE_R)
      expect(e.y1).toBe(parent.y)
      expect(e.x2).toBe(child.x - NODE_R)
      expect(e.y2).toBe(child.y)
    }
    // Internal nodes center on their children: b, c → d's slot; a → mean(b, c).
    const d = byId.get('d')!
    expect(byId.get('b')!.y).toBe(d.y)
    expect(byId.get('c')!.y).toBe(d.y)
    expect(byId.get('a')!.y).toBe((byId.get('b')!.y + byId.get('c')!.y) / 2)
  })

  it('keeps generations flowing left-to-right', () => {
    const l = layoutDag(
      graphOf([
        { id: 'a' },
        { id: 'b', parents: ['a'] },
        { id: 'c', parents: ['b'] },
      ]),
    )
    const a = l.nodes.find((n) => n.id === 'a')!
    const c = l.nodes.find((n) => n.id === 'c')!
    expect(c.x).toBeGreaterThan(a.x)
  })

  it('breaks cycles instead of recursing forever', () => {
    // a → b → a (cycle). Must terminate with finite positions.
    const l = layoutDag(
      graphOf(
        [{ id: 'a' }, { id: 'b' }],
        [
          { from: 'a', to: 'b' },
          { from: 'b', to: 'a' },
        ],
      ),
    )
    expect(l.nodes).toHaveLength(2)
    for (const n of l.nodes) {
      expect(Number.isFinite(n.x)).toBe(true)
      expect(Number.isFinite(n.y)).toBe(true)
    }
    // Every edge anchors on placed nodes.
    const byId = new Map(l.nodes.map((n) => [n.id, n]))
    for (const e of l.edges) {
      expect(byId.has(e.from)).toBe(true)
      expect(byId.has(e.to)).toBe(true)
    }
  })

  it('computes positive width/height for a non-empty graph', () => {
    const l = layoutDag(graphOf([{ id: 'a' }, { id: 'b', parents: ['a'] }]))
    expect(l.width).toBeGreaterThan(0)
    expect(l.height).toBeGreaterThan(0)
  })
})

// ── findAllRootToLeafPaths ──────────────────────────────────────────────

describe('findAllRootToLeafPaths', () => {
  it('returns [] for an empty graph', () => {
    expect(findAllRootToLeafPaths(graphOf([]))).toEqual([])
  })

  it('emits a single-node path for a lone root', () => {
    const paths = findAllRootToLeafPaths(graphOf([{ id: 'h1' }]))
    expect(paths).toHaveLength(1)
    expect(paths[0]!.path).toHaveLength(1)
    expect(paths[0]!.path[0]!.node.id).toBe('h1')
    expect(paths[0]!.path[0]!.depth).toBe(0)
  })

  it('emits one path for a linear chain', () => {
    const paths = findAllRootToLeafPaths(
      graphOf(
        [{ id: 'a' }, { id: 'b' }, { id: 'c' }],
        [
          { from: 'a', to: 'b' },
          { from: 'b', to: 'c' },
        ],
      ),
    )
    expect(paths).toHaveLength(1)
    expect(paths[0]!.path.map((p) => p.node.id)).toEqual(['a', 'b', 'c'])
    expect(paths[0]!.path[0]!.depth).toBe(0)
    expect(paths[0]!.path[1]!.depth).toBe(1)
    expect(paths[0]!.path[2]!.depth).toBe(2)
  })

  it('emits multiple paths for branching (binary tree)', () => {
    const paths = findAllRootToLeafPaths(
      graphOf(
        [{ id: 'root' }, { id: 'left' }, { id: 'right' }],
        [
          { from: 'root', to: 'left' },
          { from: 'root', to: 'right' },
        ],
      ),
    )
    expect(paths).toHaveLength(2)
    // Both paths start at root.
    expect(paths[0]!.path[0]!.node.id).toBe('root')
    expect(paths[1]!.path[0]!.node.id).toBe('root')
    // Leaves are left and right (sorted).
    expect(paths[0]!.path[1]!.node.id).toBe('left')
    expect(paths[1]!.path[1]!.node.id).toBe('right')
  })

  it('correctly enumerates a diamond (shared descendant in both paths)', () => {
    // a → b, a → c, b/d → d, c → d
    const paths = findAllRootToLeafPaths(
      graphOf(
        [{ id: 'a' }, { id: 'b' }, { id: 'c' }, { id: 'd' }],
        [
          { from: 'a', to: 'b' },
          { from: 'a', to: 'c' },
          { from: 'b', to: 'd' },
          { from: 'c', to: 'd' },
        ],
      ),
    )
    expect(paths).toHaveLength(2)
    expect(paths[0]!.path.map((p) => p.node.id)).toEqual(['a', 'b', 'd'])
    expect(paths[1]!.path.map((p) => p.node.id)).toEqual(['a', 'c', 'd'])
  })

  it('sorts sibling branches by id for stable ordering', () => {
    const paths = findAllRootToLeafPaths(
      graphOf(
        [{ id: 'root' }, { id: 'z' }, { id: 'm' }],
        [
          { from: 'root', to: 'z' },
          { from: 'root', to: 'm' },
        ],
      ),
    )
    // Siblings sorted by id → 'm' before 'z'.
    expect(paths[0]!.path[1]!.node.id).toBe('m')
    expect(paths[1]!.path[1]!.node.id).toBe('z')
  })

  it('breaks cycles instead of recursing forever', () => {
    // a → b → a (cycle). Must terminate.
    const paths = findAllRootToLeafPaths(
      graphOf(
        [{ id: 'a' }, { id: 'b' }],
        [
          { from: 'a', to: 'b' },
          { from: 'b', to: 'a' },
        ],
      ),
    )
    // Both nodes form paths (cycle boundary stops further expansion).
    expect(paths.length).toBeGreaterThanOrEqual(1)
    // No node appears twice in the same path.
    for (const p of paths) {
      const ids = p.path.map((n) => n.node.id)
      const unique = new Set(ids)
      expect(unique.size).toBe(ids.length)
    }
  })

  it('appends orphans (no parents, no children) as single-node paths', () => {
    // 'b' has a parent reference to a non-existent node; it has no known
    // parents so it becomes a root and is emitted as its own path.
    const paths = findAllRootToLeafPaths(graphOf([{ id: 'b', parents: ['ghost'] }]))
    expect(paths).toHaveLength(1)
    expect(paths[0]!.path[0]!.node.id).toBe('b')
  })

  it('emits each node in every path it belongs to (diamond-safe)', () => {
    // a → b → d, a → c → d
    // Node 'd' appears in both paths.
    const paths = findAllRootToLeafPaths(
      graphOf(
        [{ id: 'a' }, { id: 'b' }, { id: 'c' }, { id: 'd' }],
        [
          { from: 'a', to: 'b' },
          { from: 'a', to: 'c' },
          { from: 'b', to: 'd' },
          { from: 'c', to: 'd' },
        ],
      ),
    )
    const dCount = paths.reduce(
      (acc, p) => acc + (p.path.some((n) => n.node.id === 'd') ? 1 : 0),
      0,
    )
    expect(dCount).toBe(2)
  })
})

// ── isTerminal ─────────────────────────────────────────────────────────

describe('isTerminal', () => {
  it('returns true only for confirmed/refuted/cancelled', () => {
    expect(isTerminal('confirmed')).toBe(true)
    expect(isTerminal('refuted')).toBe(true)
    expect(isTerminal('cancelled')).toBe(true)
  })

  it('returns false for in-flight and unknown statuses', () => {
    expect(isTerminal('open')).toBe(false)
    expect(isTerminal('in-progress')).toBe(false)
    expect(isTerminal('bogus')).toBe(false)
  })
})

// ── findIncompletePaths ────────────────────────────────────────────────

describe('findIncompletePaths', () => {
  it('returns [] for an empty graph', () => {
    expect(findIncompletePaths(graphOf([]))).toEqual([])
  })

  it('drops an all-terminal path', () => {
    const paths = findIncompletePaths(
      graphOf([
        { id: 'a', status: 'confirmed' },
        { id: 'b', status: 'refuted', parents: ['a'] },
      ]),
    )
    expect(paths).toEqual([])
  })

  it('keeps a path containing at least one non-terminal node', () => {
    const paths = findIncompletePaths(
      graphOf([
        { id: 'a', status: 'confirmed' },
        { id: 'b', status: 'open', parents: ['a'] },
      ]),
    )
    expect(paths).toHaveLength(1)
    expect(paths[0]!.path.map((t) => t.node.id)).toEqual(['a', 'b'])
  })

  it('drops fully-terminal paths while keeping mixed ones in the same graph', () => {
    // Two independent chains: a (terminal) → b (terminal); x (terminal) → y (open)
    const paths = findIncompletePaths(
      graphOf([
        { id: 'a', status: 'confirmed' },
        { id: 'b', status: 'refuted', parents: ['a'] },
        { id: 'x', status: 'confirmed' },
        { id: 'y', status: 'open', parents: ['x'] },
      ]),
    )
    expect(paths).toHaveLength(1)
    expect(paths[0]!.path.map((t) => t.node.id)).toEqual(['x', 'y'])
  })

  it('keeps both paths of a diamond with a non-terminal convergence node', () => {
    // a(open) → b(confirmed), a(open) → c(confirmed), b/c → d(open)
    const paths = findIncompletePaths(
      graphOf(
        [
          { id: 'a', status: 'open' },
          { id: 'b', status: 'confirmed', parents: ['a'] },
          { id: 'c', status: 'confirmed', parents: ['a'] },
          { id: 'd', status: 'open', parents: ['b', 'c'] },
        ],
      ),
    )
    expect(paths).toHaveLength(2)
    expect(paths[0]!.path.map((t) => t.node.id)).toEqual(['a', 'b', 'd'])
    expect(paths[1]!.path.map((t) => t.node.id)).toEqual(['a', 'c', 'd'])
  })
})

// ── filterPaths ────────────────────────────────────────────────────────

describe('filterPaths', () => {
  function pathFromIds(
    nodes: { id: string; title?: string; status?: string }[],
  ): PathEntry {
    return {
      path: nodes.map((n, depth) => ({
        node: { id: n.id, title: n.title ?? n.id, status: n.status ?? 'open' },
        depth,
      })),
    }
  }

  it('returns paths unchanged when hideTerminal is false/omitted', () => {
    const paths = [pathFromIds([{ id: 'a' }, { id: 'b', status: 'confirmed' }])]
    expect(filterPaths(paths)).toBe(paths)
    expect(filterPaths(paths, { hideTerminal: false })).toBe(paths)
  })

  it('prunes terminal nodes when hideTerminal is true', () => {
    const paths = [
      pathFromIds([
        { id: 'a', status: 'open' },
        { id: 'b', status: 'confirmed' },
        { id: 'c', status: 'in-progress' },
      ]),
    ]
    const out = filterPaths(paths, { hideTerminal: true })
    expect(out).toHaveLength(1)
    expect(out[0]!.path.map((t) => t.node.id)).toEqual(['a', 'c'])
    // Depths are re-indexed to stay contiguous after pruning.
    expect(out[0]!.path[0]!.depth).toBe(0)
    expect(out[0]!.path[1]!.depth).toBe(1)
  })

  it('drops paths left empty after pruning', () => {
    const paths = [pathFromIds([{ id: 'a', status: 'confirmed' }])]
    expect(filterPaths(paths, { hideTerminal: true })).toEqual([])
  })

  it('keeps non-empty pruned paths while dropping empty ones', () => {
    const paths = [
      pathFromIds([{ id: 'a', status: 'refuted' }]), // all-terminal → dropped
      pathFromIds([
        { id: 'x', status: 'open' },
        { id: 'y', status: 'cancelled' },
        { id: 'z', status: 'open' },
      ]),
    ]
    const out = filterPaths(paths, { hideTerminal: true })
    expect(out).toHaveLength(1)
    expect(out[0]!.path.map((t) => t.node.id)).toEqual(['x', 'z'])
  })
})

// ── projectDir / projectFilePaths ───────────────────────────────────────

describe('projectDir', () => {
  it('extracts the directory from a nested index path', () => {
    const root = { path: '/r', index: [{ id: 'R-001', path: 'R-001-flaw/brief.md' }], projects: [] }
    expect(projectDir(root, 'R-001')).toBe('R-001-flaw')
  })

  it('returns "" for the flat single-project layout', () => {
    const root = { path: '/r', index: [{ id: 'R-002', path: 'brief.md' }], projects: [] }
    expect(projectDir(root, 'R-002')).toBe('')
  })

  it('returns "" when no matching index entry exists', () => {
    expect(projectDir(undefined, 'R-999')).toBe('')
    const root = { path: '/r', index: [], projects: [] }
    expect(projectDir(root, 'R-001')).toBe('')
  })
})

describe('projectFilePaths', () => {
  it('builds nested paths when a directory is given', () => {
    const p = projectFilePaths('/ws/.research', 'R-001-flaw')
    expect(p.brief).toBe('/ws/.research/R-001-flaw/brief.md')
    expect(p.priorArt).toBe('/ws/.research/R-001-flaw/prior-art.md')
    expect(p.report).toBe('/ws/.research/R-001-flaw/report.md')
    expect(p.graph).toBe('/ws/.research/R-001-flaw/hypotheses/graph.md')
  })

  it('builds flat-root paths when no directory is given', () => {
    const p = projectFilePaths('/ws/.research', '')
    expect(p.brief).toBe('/ws/.research/brief.md')
    expect(p.graph).toBe('/ws/.research/hypotheses/graph.md')
  })
})

// ── mergePathsToTree ────────────────────────────────────────────────────

describe('mergePathsToTree', () => {
  // ── Helper to build a PathEntry from node ids ──
  function pathFromIds(
    nodes: { id: string; title?: string; status?: string }[],
  ): PathEntry {
    return {
      path: nodes.map((n, depth) => ({
        node: { id: n.id, title: n.title ?? n.id, status: n.status ?? 'open' },
        depth,
      })),
    }
  }

  // ── Empty / single-path edge cases ──

  it('returns [] for empty input', () => {
    expect(mergePathsToTree([])).toEqual([])
  })

  it('returns a single-node tree for a single-node path', () => {
    const paths = [pathFromIds([{ id: 'a' }])]
    const tree = mergePathsToTree(paths)
    expect(tree).toHaveLength(1)
    expect(tree[0]!.node.id).toBe('a')
    expect(tree[0]!.children).toEqual([])
  })

  it('returns a linear chain for a single linear path', () => {
    const paths = [pathFromIds([{ id: 'a' }, { id: 'b' }, { id: 'c' }])]
    const tree = mergePathsToTree(paths)
    expect(tree).toHaveLength(1)
    expect(tree[0]!.node.id).toBe('a')
    expect(tree[0]!.children).toHaveLength(1)
    expect(tree[0]!.children[0]!.node.id).toBe('b')
    expect(tree[0]!.children[0]!.children).toHaveLength(1)
    expect(tree[0]!.children[0]!.children[0]!.node.id).toBe('c')
    expect(tree[0]!.children[0]!.children[0]!.children).toEqual([])
  })

  // ── Disjoint paths (no shared prefixes) ──

  it('handles two completely disjoint paths', () => {
    const paths = [
      pathFromIds([{ id: 'a' }, { id: 'b' }]),
      pathFromIds([{ id: 'x' }, { id: 'y' }]),
    ]
    const tree = mergePathsToTree(paths)
    expect(tree).toHaveLength(2)
    const ids = tree.map((n) => n.node.id)
    expect(ids).toContain('a')
    expect(ids).toContain('x')
    // Each root has exactly one child.
    expect(tree.find((n) => n.node.id === 'a')!.children).toHaveLength(1)
    expect(tree.find((n) => n.node.id === 'a')!.children[0]!.node.id).toBe('b')
    expect(tree.find((n) => n.node.id === 'x')!.children).toHaveLength(1)
    expect(tree.find((n) => n.node.id === 'x')!.children[0]!.node.id).toBe('y')
  })

  // ── Paths sharing a single common prefix (root) ──

  it('merges two paths sharing only the root', () => {
    const paths = [
      pathFromIds([{ id: 'root' }, { id: 'left' }]),
      pathFromIds([{ id: 'root' }, { id: 'right' }]),
    ]
    const tree = mergePathsToTree(paths)
    expect(tree).toHaveLength(1)
    expect(tree[0]!.node.id).toBe('root')
    expect(tree[0]!.children).toHaveLength(2)
    const childIds = tree[0]!.children.map((c) => c.node.id).sort()
    expect(childIds).toEqual(['left', 'right'])
  })

  it('merges three paths sharing only the root', () => {
    const paths = [
      pathFromIds([{ id: 'root' }, { id: 'a' }]),
      pathFromIds([{ id: 'root' }, { id: 'b' }]),
      pathFromIds([{ id: 'root' }, { id: 'c' }]),
    ]
    const tree = mergePathsToTree(paths)
    expect(tree).toHaveLength(1)
    expect(tree[0]!.node.id).toBe('root')
    expect(tree[0]!.children).toHaveLength(3)
    expect(tree[0]!.children.map((c) => c.node.id).sort()).toEqual(['a', 'b', 'c'])
  })

  // ── Paths sharing multiple levels of prefixes ──

  it('merges paths sharing a two-level prefix', () => {
    // root → mid → a
    // root → mid → b
    const paths = [
      pathFromIds([{ id: 'root' }, { id: 'mid' }, { id: 'a' }]),
      pathFromIds([{ id: 'root' }, { id: 'mid' }, { id: 'b' }]),
    ]
    const tree = mergePathsToTree(paths)
    expect(tree).toHaveLength(1)
    expect(tree[0]!.node.id).toBe('root')
    expect(tree[0]!.children).toHaveLength(1)
    expect(tree[0]!.children[0]!.node.id).toBe('mid')
    expect(tree[0]!.children[0]!.children).toHaveLength(2)
    const leafIds = tree[0]!.children[0]!.children.map((c) => c.node.id).sort()
    expect(leafIds).toEqual(['a', 'b'])
  })

  it('merges paths sharing a three-level prefix', () => {
    const paths = [
      pathFromIds([{ id: 'a' }, { id: 'b' }, { id: 'c' }, { id: 'd' }]),
      pathFromIds([{ id: 'a' }, { id: 'b' }, { id: 'c' }, { id: 'e' }]),
    ]
    const tree = mergePathsToTree(paths)
    expect(tree).toHaveLength(1)
    // a → b → c → {d, e}
    expect(tree[0]!.node.id).toBe('a')
    expect(tree[0]!.children).toHaveLength(1)
    expect(tree[0]!.children[0]!.node.id).toBe('b')
    expect(tree[0]!.children[0]!.children).toHaveLength(1)
    expect(tree[0]!.children[0]!.children[0]!.node.id).toBe('c')
    expect(tree[0]!.children[0]!.children[0]!.children).toHaveLength(2)
    const leafIds = tree[0]!.children[0]!.children[0]!.children
      .map((c) => c.node.id)
      .sort()
    expect(leafIds).toEqual(['d', 'e'])
  })

  // ── Paths where one is fully contained in another ──

  it('handles a path that is a prefix of another (linear containment)', () => {
    // [a, b] is fully contained in [a, b, c]
    const paths = [
      pathFromIds([{ id: 'a' }, { id: 'b' }]),
      pathFromIds([{ id: 'a' }, { id: 'b' }, { id: 'c' }]),
    ]
    const tree = mergePathsToTree(paths)
    expect(tree).toHaveLength(1)
    expect(tree[0]!.node.id).toBe('a')
    expect(tree[0]!.children).toHaveLength(1)
    expect(tree[0]!.children[0]!.node.id).toBe('b')
    // b has one child: c (the extra leaf from the longer path)
    expect(tree[0]!.children[0]!.children).toHaveLength(1)
    expect(tree[0]!.children[0]!.children[0]!.node.id).toBe('c')
  })

  it('handles multiple paths where one is the longest prefix', () => {
    // [a, b, c] is a prefix of [a, b, c, d]; [a, b] is also a prefix
    const paths = [
      pathFromIds([{ id: 'a' }, { id: 'b' }, { id: 'c' }, { id: 'd' }]),
      pathFromIds([{ id: 'a' }, { id: 'b' }, { id: 'c' }]),
      pathFromIds([{ id: 'a' }, { id: 'b' }]),
    ]
    const tree = mergePathsToTree(paths)
    expect(tree).toHaveLength(1)
    expect(tree[0]!.node.id).toBe('a')
    expect(tree[0]!.children).toHaveLength(1)
    expect(tree[0]!.children[0]!.node.id).toBe('b')
    expect(tree[0]!.children[0]!.children).toHaveLength(1)
    expect(tree[0]!.children[0]!.children[0]!.node.id).toBe('c')
    expect(tree[0]!.children[0]!.children[0]!.children).toHaveLength(1)
    expect(tree[0]!.children[0]!.children[0]!.children[0]!.node.id).toBe('d')
  })

  // ── Diamond DAG (shared descendant) ──

  it('merges a diamond correctly (shared descendant appears once)', () => {
    // a → b → d
    // a → c → d
    // The flat paths are [a,b,d] and [a,c,d]. After merge:
    // a has children b and c; both b and c have one child d (shared).
    const paths = [
      pathFromIds([{ id: 'a' }, { id: 'b' }, { id: 'd' }]),
      pathFromIds([{ id: 'a' }, { id: 'c' }, { id: 'd' }]),
    ]
    const tree = mergePathsToTree(paths)
    expect(tree).toHaveLength(1)
    expect(tree[0]!.node.id).toBe('a')
    expect(tree[0]!.children).toHaveLength(2)
    const childIds = tree[0]!.children.map((c) => c.node.id).sort()
    expect(childIds).toEqual(['b', 'c'])
    // b's child is d
    const bNode = tree[0]!.children.find((c) => c.node.id === 'b')!
    expect(bNode.children).toHaveLength(1)
    expect(bNode.children[0]!.node.id).toBe('d')
    expect(bNode.children[0]!.children).toEqual([])
    // c's child is also d (a separate tree node instance, sharing id)
    const cNode = tree[0]!.children.find((c) => c.node.id === 'c')!
    expect(cNode.children).toHaveLength(1)
    expect(cNode.children[0]!.node.id).toBe('d')
  })

  // ── Complex DAG branches ──

  it('handles a complex DAG with multiple merge points', () => {
    // a → b → d → f
    //    ↘ c → e ↗
    // a → x → y
    // Paths: [a,b,d,f], [a,b,c,e], [a,x,y]
    const paths = [
      pathFromIds([{ id: 'a' }, { id: 'b' }, { id: 'd' }, { id: 'f' }]),
      pathFromIds([{ id: 'a' }, { id: 'b' }, { id: 'c' }, { id: 'e' }]),
      pathFromIds([{ id: 'a' }, { id: 'x' }, { id: 'y' }]),
    ]
    const tree = mergePathsToTree(paths)
    expect(tree).toHaveLength(1)
    expect(tree[0]!.node.id).toBe('a')
    expect(tree[0]!.children).toHaveLength(2) // b, x
    const aChildren = tree[0]!.children.map((c) => c.node.id).sort()
    expect(aChildren).toEqual(['b', 'x'])

    // b has children d and c
    const bNode = tree[0]!.children.find((c) => c.node.id === 'b')!
    expect(bNode.children).toHaveLength(2)
    const bChildren = bNode.children.map((c) => c.node.id).sort()
    expect(bChildren).toEqual(['c', 'd'])

    // d has child f
    const dNode = bNode.children.find((c) => c.node.id === 'd')!
    expect(dNode.children).toHaveLength(1)
    expect(dNode.children[0]!.node.id).toBe('f')

    // c has child e
    const cNode = bNode.children.find((c) => c.node.id === 'c')!
    expect(cNode.children).toHaveLength(1)
    expect(cNode.children[0]!.node.id).toBe('e')

    // x has child y
    const xNode = tree[0]!.children.find((c) => c.node.id === 'x')!
    expect(xNode.children).toHaveLength(1)
    expect(xNode.children[0]!.node.id).toBe('y')
  })

  it('handles a star graph (single root, many leaves)', () => {
    const paths = [
      pathFromIds([{ id: 'root' }, { id: 'l1' }]),
      pathFromIds([{ id: 'root' }, { id: 'l2' }]),
      pathFromIds([{ id: 'root' }, { id: 'l3' }]),
      pathFromIds([{ id: 'root' }, { id: 'l4' }]),
      pathFromIds([{ id: 'root' }, { id: 'l5' }]),
    ]
    const tree = mergePathsToTree(paths)
    expect(tree).toHaveLength(1)
    expect(tree[0]!.node.id).toBe('root')
    expect(tree[0]!.children).toHaveLength(5)
    const leafIds = tree[0]!.children.map((c) => c.node.id).sort()
    expect(leafIds).toEqual(['l1', 'l2', 'l3', 'l4', 'l5'])
  })

  it('handles a full binary tree of depth 3', () => {
    // root → l → ll
    //        ↘ lr
    //    ↘ r → rl
    //        ↘ rr
    const paths = [
      pathFromIds([{ id: 'root' }, { id: 'l' }, { id: 'll' }]),
      pathFromIds([{ id: 'root' }, { id: 'l' }, { id: 'lr' }]),
      pathFromIds([{ id: 'root' }, { id: 'r' }, { id: 'rl' }]),
      pathFromIds([{ id: 'root' }, { id: 'r' }, { id: 'rr' }]),
    ]
    const tree = mergePathsToTree(paths)
    expect(tree).toHaveLength(1)
    expect(tree[0]!.node.id).toBe('root')
    expect(tree[0]!.children).toHaveLength(2)
    const rootChildren = tree[0]!.children.map((c) => c.node.id).sort()
    expect(rootChildren).toEqual(['l', 'r'])

    const lNode = tree[0]!.children.find((c) => c.node.id === 'l')!
    expect(lNode.children).toHaveLength(2)
    expect(lNode.children.map((c) => c.node.id).sort()).toEqual(['ll', 'lr'])

    const rNode = tree[0]!.children.find((c) => c.node.id === 'r')!
    expect(rNode.children).toHaveLength(2)
    expect(rNode.children.map((c) => c.node.id).sort()).toEqual(['rl', 'rr'])
  })

  // ── Preserving node data ──

  it('preserves node title and status in the merged tree', () => {
    const paths = [
      pathFromIds([
        { id: 'a', title: 'Alpha', status: 'confirmed' },
        { id: 'b', title: 'Beta', status: 'open' },
      ]),
    ]
    const tree = mergePathsToTree(paths)
    expect(tree[0]!.node.title).toBe('Alpha')
    expect(tree[0]!.node.status).toBe('confirmed')
    expect(tree[0]!.children[0]!.node.title).toBe('Beta')
    expect(tree[0]!.children[0]!.node.status).toBe('open')
  })

  it('preserves node data through deep merge', () => {
    const paths = [
      pathFromIds([
        { id: 'a', title: 'A', status: 'confirmed' },
        { id: 'b', title: 'B', status: 'in-progress' },
        { id: 'c', title: 'C', status: 'refuted' },
      ]),
    ]
    const tree = mergePathsToTree(paths)
    expect(tree[0]!.node.title).toBe('A')
    expect(tree[0]!.children[0]!.node.title).toBe('B')
    expect(tree[0]!.children[0]!.children[0]!.node.title).toBe('C')
    expect(tree[0]!.node.status).toBe('confirmed')
    expect(tree[0]!.children[0]!.node.status).toBe('in-progress')
    expect(tree[0]!.children[0]!.children[0]!.node.status).toBe('refuted')
  })

  // ── Depth tracking ──

  it('preserves depth values from the original paths', () => {
    const paths = [
      pathFromIds([{ id: 'a' }, { id: 'b' }, { id: 'c' }]),
    ]
    const tree = mergePathsToTree(paths)
    const aNode = tree[0]!
    // Depth is tracked via the TreeNode wrapper in PathEntry,
    // but MergedTreeNode itself doesn't store depth — it's implicit via tree structure.
    // This test verifies the tree structure is correct (depth = nesting level).
    expect(aNode.children).toHaveLength(1)
    expect(aNode.children[0]!.children).toHaveLength(1)
    expect(aNode.children[0]!.children[0]!.children).toHaveLength(0)
  })

  // ── Duplicate path entries ──

  it('handles duplicate paths gracefully (no duplication in tree)', () => {
    const paths = [
      pathFromIds([{ id: 'a' }, { id: 'b' }]),
      pathFromIds([{ id: 'a' }, { id: 'b' }]),
    ]
    const tree = mergePathsToTree(paths)
    expect(tree).toHaveLength(1)
    expect(tree[0]!.node.id).toBe('a')
    expect(tree[0]!.children).toHaveLength(1)
    expect(tree[0]!.children[0]!.node.id).toBe('b')
    expect(tree[0]!.children[0]!.children).toEqual([])
  })

  // ── Paths with same prefix but different lengths ──

  it('handles paths with overlapping but not identical prefixes', () => {
    // [a, b, c] and [a, b, d] share prefix [a, b]
    const paths = [
      pathFromIds([{ id: 'a' }, { id: 'b' }, { id: 'c' }]),
      pathFromIds([{ id: 'a' }, { id: 'b' }, { id: 'd' }]),
      pathFromIds([{ id: 'a' }, { id: 'x' }]),
    ]
    const tree = mergePathsToTree(paths)
    expect(tree).toHaveLength(1)
    expect(tree[0]!.node.id).toBe('a')
    expect(tree[0]!.children).toHaveLength(2)
    const aChildren = tree[0]!.children.map((c) => c.node.id).sort()
    expect(aChildren).toEqual(['b', 'x'])

    const bNode = tree[0]!.children.find((c) => c.node.id === 'b')!
    expect(bNode.children).toHaveLength(2)
    const bChildren = bNode.children.map((c) => c.node.id).sort()
    expect(bChildren).toEqual(['c', 'd'])
  })

  // ── PathEntry with empty path ──

  it('skips PathEntry with empty path array', () => {
    const paths: PathEntry[] = [
      { path: [] },
      pathFromIds([{ id: 'a' }]),
      { path: [] },
    ]
    const tree = mergePathsToTree(paths)
    expect(tree).toHaveLength(1)
    expect(tree[0]!.node.id).toBe('a')
  })

  // ── Integration: mergePathsToTree + findAllRootToLeafPaths ──

  it('produces a merged tree equivalent to the DAG structure', () => {
    // Build a graph: a → b, a → c, b → d, c → d
    // Paths: [a,b,d], [a,c,d]
    // Merged tree: a has children b, c; b has child d; c has child d
    const graph = graphOf(
      [
        { id: 'a' },
        { id: 'b', parents: ['a'] },
        { id: 'c', parents: ['a'] },
        { id: 'd', parents: ['b', 'c'] },
      ],
    )
    const paths = findAllRootToLeafPaths(graph)
    expect(paths).toHaveLength(2)

    const tree = mergePathsToTree(paths)
    expect(tree).toHaveLength(1)
    expect(tree[0]!.node.id).toBe('a')
    expect(tree[0]!.children).toHaveLength(2)

    const childIds = tree[0]!.children.map((c) => c.node.id).sort()
    expect(childIds).toEqual(['b', 'c'])

    const bNode = tree[0]!.children.find((c) => c.node.id === 'b')!
    expect(bNode.children).toHaveLength(1)
    expect(bNode.children[0]!.node.id).toBe('d')

    const cNode = tree[0]!.children.find((c) => c.node.id === 'c')!
    expect(cNode.children).toHaveLength(1)
    expect(cNode.children[0]!.node.id).toBe('d')
  })

  it('produces a single-node tree for a graph with one node', () => {
    const graph = graphOf([{ id: 'solo' }])
    const paths = findAllRootToLeafPaths(graph)
    expect(paths).toHaveLength(1)

    const tree = mergePathsToTree(paths)
    expect(tree).toHaveLength(1)
    expect(tree[0]!.node.id).toBe('solo')
    expect(tree[0]!.children).toEqual([])
  })

  it('produces correct tree for a deep linear chain', () => {
    const nodes = Array.from({ length: 10 }, (_, i) => ({
      id: `n${i}`,
      parents: i > 0 ? [`n${i - 1}`] : undefined,
    }))
    const graph = graphOf(nodes)
    const paths = findAllRootToLeafPaths(graph)
    expect(paths).toHaveLength(1)

    const tree = mergePathsToTree(paths)
    expect(tree).toHaveLength(1)
    expect(tree[0]!.node.id).toBe('n0')
    // Verify chain depth
    let current: MergedTreeNode | null = tree[0]!
    for (let i = 1; i <= 9; i++) {
      expect(current!.children).toHaveLength(1)
      current = current!.children[0]!
      expect(current!.node.id).toBe(`n${i}`)
    }
  })

  it('handles a graph with multiple roots', () => {
    // Two independent chains: a → b and x → y
    const graph = graphOf([
      { id: 'a' },
      { id: 'b', parents: ['a'] },
      { id: 'x' },
      { id: 'y', parents: ['x'] },
    ])
    const paths = findAllRootToLeafPaths(graph)
    expect(paths).toHaveLength(2)

    const tree = mergePathsToTree(paths)
    expect(tree).toHaveLength(2)
    const rootIds = tree.map((n) => n.node.id).sort()
    expect(rootIds).toEqual(['a', 'x'])
  })

  // ── Algorithm correctness: node reuse ──

  it('reuses the same node object when a path revisits an existing prefix', () => {
    // [a, b, c] then [a, b, d] — 'a' and 'b' should be reused, not duplicated
    const paths = [
      pathFromIds([{ id: 'a' }, { id: 'b' }, { id: 'c' }]),
      pathFromIds([{ id: 'a' }, { id: 'b' }, { id: 'd' }]),
    ]
    const tree = mergePathsToTree(paths)
    // 'a' appears once at root
    expect(tree).toHaveLength(1)
    // 'b' appears once as child of 'a'
    expect(tree[0]!.children).toHaveLength(1)
    expect(tree[0]!.children[0]!.node.id).toBe('b')
    // 'b' has two children: c and d
    expect(tree[0]!.children[0]!.children).toHaveLength(2)
    const bChildren = tree[0]!.children[0]!.children.map((c) => c.node.id).sort()
    expect(bChildren).toEqual(['c', 'd'])
  })
})

describe('buildDisplayGraph', () => {
  it('returns the input graph unchanged when hideTerminal is false', () => {
    const g = graphOf([{ id: 'a' }, { id: 'b', parents: ['a'] }])
    expect(buildDisplayGraph(g)).toBe(g)
    expect(buildDisplayGraph(g, {})).toBe(g)
    expect(buildDisplayGraph(g, { hideTerminal: false })).toBe(g)
  })

  it('drops a fully-terminal path when hideTerminal is true', () => {
    const g = graphOf(
      [
        { id: 'a', status: 'confirmed' },
        { id: 'b', status: 'refuted', parents: ['a'] },
      ],
      [{ from: 'a', to: 'b' }],
    )
    const filtered = buildDisplayGraph(g, { hideTerminal: true })
    expect(filtered.nodes).toEqual([])
    expect(filtered.edges).toEqual([])
  })

  it('keeps non-terminal nodes and prunes terminal ones from mixed paths', () => {
    // a(open) → b(confirmed) → c(open): b is terminal and must be pruned,
    // but a and c (non-terminal) survive; the edge a→c is NOT present, so
    // only the nodes remain (no edge connects a→c directly).
    const g = graphOf(
      [
        { id: 'a', status: 'open' },
        { id: 'b', status: 'confirmed', parents: ['a'] },
        { id: 'c', status: 'open', parents: ['b'] },
      ],
      [
        { from: 'a', to: 'b' },
        { from: 'b', to: 'c' },
      ],
    )
    const filtered = buildDisplayGraph(g, { hideTerminal: true })
    expect(filtered.nodes.map((n) => n.id).sort()).toEqual(['a', 'c'])
    // Both original edges touch the pruned terminal node → dropped.
    expect(filtered.edges).toEqual([])
  })

  it('keeps an edge between two surviving non-terminal nodes', () => {
    const g = graphOf(
      [
        { id: 'a', status: 'open' },
        { id: 'b', status: 'in-progress', parents: ['a'] },
      ],
      [{ from: 'a', to: 'b' }],
    )
    const filtered = buildDisplayGraph(g, { hideTerminal: true })
    expect(filtered.nodes.map((n) => n.id).sort()).toEqual(['a', 'b'])
    expect(filtered.edges).toEqual([{ from: 'a', to: 'b' }])
  })
})
