import { describe, it, expect } from 'vitest'
import type { HypothesisGraph } from '@/types/models'
import {
  NODE_R,
  COLUMN_W,
  ROW_H,
  LABEL_GAP_X,
  LABEL_MAX_CHARS,
  LABEL_CHAR_W,
  BOX_PAD,
  LEFT_PAD,
  TOP_PAD,
  buildParentMap,
  assignLevels,
  layoutDag,
  xFor,
  yFor,
  idTextWidth,
  titleTextWidth,
  edgePathH,
  statusColorVar,
  formatRate,
  buildChildrenMap,
  buildDisplayGraph,
  layoutSignature,
  projectDir,
  hypothesisCardPath,
  projectFilePaths,
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

  it('unions explicit edges with declared parents (partial graphs)', () => {
    // Only the H1→H2 edge exists while H3 merely declares parents:['H1']:
    // both relations must resolve so layout and path traversal agree.
    const g = graphOf(
      [{ id: 'H1' }, { id: 'H2' }, { id: 'H3', parents: ['H1'] }],
      [{ from: 'H1', to: 'H2' }],
    )
    expect(buildParentMap(g)).toEqual(
      new Map([
        ['H2', ['H1']],
        ['H3', ['H1']],
      ]),
    )
  })

  it('dedupes a parent declared both as an edge and in node.parents', () => {
    const g = graphOf(
      [{ id: 'a' }, { id: 'b', parents: ['a'] }],
      [{ from: 'a', to: 'b' }],
    )
    expect(buildParentMap(g)).toEqual(new Map([['b', ['a']]]))
  })
})

describe('buildChildrenMap', () => {
  it('unions edges and declared parents, deduped in first-seen order', () => {
    const g = graphOf(
      [{ id: 'a' }, { id: 'b', parents: ['a'] }, { id: 'c' }, { id: 'd', parents: ['c', 'a'] }],
      [{ from: 'a', to: 'c' }],
    )
    expect(buildChildrenMap(g)).toEqual(
      new Map([
        // Edges register before declared parents (first-seen order).
        ['a', ['c', 'b', 'd']],
        ['c', ['d']],
      ]),
    )
  })

  it('drops self-loops and references to unknown ids', () => {
    const g = graphOf(
      [{ id: 'a' }, { id: 'b', parents: ['ghost', 'b'] }],
      [
        { from: 'a', to: 'ghost' },
        { from: 'a', to: 'a' },
      ],
    )
    expect(buildChildrenMap(g)).toEqual(new Map())
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

  it('returns minX 0 for an empty graph', () => {
    expect(layoutDag(graphOf([])).minX).toBe(0)
  })

  it('opens the box left of x=0 so left-hanging id labels are not clipped', () => {
    // Ids render with textAnchor="end" at node.x - LABEL_GAP_X: for the
    // leftmost column they extend into negative x. The SVG clips its own
    // overflow, so the box (minX) must include their full painted width.
    const l = layoutDag(graphOf([{ id: 'H-001', title: 'Root' }]))
    expect(l.minX).toBe(LEFT_PAD - LABEL_GAP_X - idTextWidth('H-001') - BOX_PAD)
  })

  it('hugs the painted title extent instead of the full truncate budget', () => {
    // A short title must produce a tight box — the camera fits and centers
    // the box, so leftover budget space would push the fitted graph left.
    const title = 'Root hypothesis'
    const l = layoutDag(graphOf([{ id: 'a', title }]))
    expect(title.length).toBeLessThan(LABEL_MAX_CHARS)
    expect(l.width).toBe(
      LEFT_PAD + LABEL_GAP_X + titleTextWidth(title) + BOX_PAD - l.minX,
    )
  })

  it('caps the title extent at the truncate budget for long titles', () => {
    const l = layoutDag(graphOf([{ id: 'a', title: 'x'.repeat(LABEL_MAX_CHARS + 10) }]))
    expect(l.width).toBe(
      LEFT_PAD + LABEL_GAP_X + LABEL_MAX_CHARS * LABEL_CHAR_W + BOX_PAD - l.minX,
    )
  })

  it('uses the deepest right-hand extent across all columns', () => {
    // Sibling children of one root: the longer title defines the box.
    const long = 'a'.repeat(20)
    const l = layoutDag(
      graphOf([
        { id: 'a', title: 'x' },
        { id: 'b', title: 'x', parents: ['a'] },
        { id: 'c', title: long, parents: ['a'] },
      ]),
    )
    expect(l.width).toBe(
      LEFT_PAD + COLUMN_W + LABEL_GAP_X + titleTextWidth(long) + BOX_PAD - l.minX,
    )
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
    // Separation post-pass: b and c share child d, so their raw child means
    // coincide at d's slot; the pass keeps b on the shared slot, pushes c one
    // slot down (minimal one-slot gap), then re-centers a on the final pair.
    const d = byId.get('d')!
    const b = byId.get('b')!
    const c = byId.get('c')!
    expect(b.y).toBe(d.y)
    expect(c.y).toBe(b.y + ROW_H)
    expect(byId.get('a')!.y).toBe((b.y + c.y) / 2)
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

  // ── Regression ([67]): same-level nodes never share a row band ──

  /** Invariant helper: nodes in the same depth column are ≥ ROW_H apart. */
  function expectNoSameLevelOverlap(l: ReturnType<typeof layoutDag>): void {
    const byLevel = new Map<number, number[]>()
    for (const n of l.nodes) {
      const ys = byLevel.get(n.level)
      if (ys) ys.push(n.y)
      else byLevel.set(n.level, [n.y])
    }
    for (const ys of byLevel.values()) {
      for (let i = 0; i < ys.length; i++) {
        for (let j = i + 1; j < ys.length; j++) {
          expect(Math.abs(ys[i]! - ys[j]!)).toBeGreaterThanOrEqual(ROW_H)
        }
      }
    }
  }

  it('separates diamond siblings so their labels never overlap', () => {
    // Regression: two parents sharing the same children used to land on the
    // identical point (b.y === c.y === d.y) and visually merge into one node.
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
    expect(Math.abs(byId.get('b')!.y - byId.get('c')!.y)).toBeGreaterThanOrEqual(ROW_H)
    expectNoSameLevelOverlap(l)
  })

  it('separates a fan of same-mean siblings and grows the box to fit them', () => {
    // r → {p1,p2,p3} → d: all three parents average d's single leaf slot.
    // They must be pushed apart, and the height must cover the pushed-down
    // tail even though only one leaf slot was ever claimed.
    const l = layoutDag(
      graphOf(
        [
          { id: 'r' },
          { id: 'p1' },
          { id: 'p2' },
          { id: 'p3' },
          { id: 'd', parents: ['p1', 'p2', 'p3'] },
        ],
        [
          { from: 'r', to: 'p1' },
          { from: 'r', to: 'p2' },
          { from: 'r', to: 'p3' },
        ],
      ),
    )
    const byId = new Map(l.nodes.map((n) => [n.id, n]))
    expect(byId.get('p2')!.y).toBe(byId.get('p1')!.y + ROW_H)
    expect(byId.get('p3')!.y).toBe(byId.get('p1')!.y + 2 * ROW_H)
    // r re-centers on the separated fan.
    expect(byId.get('r')!.y).toBe((byId.get('p1')!.y + byId.get('p3')!.y) / 2)
    // The box must extend past the pushed-down sibling (max slot 2, not the
    // single leaf slot) so the whole fan stays reachable by the fitted camera.
    expect(l.height).toBe(TOP_PAD + 2 * ROW_H + BOX_PAD)
    expectNoSameLevelOverlap(l)
  })

  it('keeps the no-overlap invariant on a layered-diamond cluster', () => {
    // Two stacked diamonds a→{b,c}→{e,f}→g: every internal node's raw child
    // mean is 0; re-centering and separation must interleave correctly.
    const l = layoutDag(
      graphOf(
        [{ id: 'a' }, { id: 'b' }, { id: 'c' }, { id: 'e' }, { id: 'f' }, { id: 'g' }],
        [
          { from: 'a', to: 'b' },
          { from: 'a', to: 'c' },
          { from: 'b', to: 'e' },
          { from: 'c', to: 'e' },
          { from: 'b', to: 'f' },
          { from: 'c', to: 'f' },
          { from: 'e', to: 'g' },
          { from: 'f', to: 'g' },
        ],
      ),
    )
    expectNoSameLevelOverlap(l)
    // All four same-level pairs are genuinely separated.
    const byId = new Map(l.nodes.map((n) => [n.id, n]))
    expect(Math.abs(byId.get('b')!.y - byId.get('c')!.y)).toBeGreaterThanOrEqual(ROW_H)
    expect(Math.abs(byId.get('e')!.y - byId.get('f')!.y)).toBeGreaterThanOrEqual(ROW_H)
  })
})

describe('layoutSignature', () => {
  it('is stable across content-identical layouts built as fresh objects', () => {
    const build = () => layoutDag(graphOf([{ id: 'a' }, { id: 'b', parents: ['a'] }]))
    expect(layoutSignature(build())).toBe(layoutSignature(build()))
  })

  it('changes when the painted geometry changes', () => {
    const base = layoutDag(graphOf([{ id: 'a' }, { id: 'b', parents: ['a'] }]))
    const wider = layoutDag(graphOf([{ id: 'a' }, { id: 'b', parents: ['a'] }, { id: 'c', parents: ['a'] }]))
    expect(layoutSignature(wider)).not.toBe(layoutSignature(base))
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

describe('hypothesisCardPath', () => {
  it('builds a nested card path when a directory is given', () => {
    expect(hypothesisCardPath('/ws/.research', 'R-001-flaw', 'H-002')).toBe(
      '/ws/.research/R-001-flaw/hypotheses/H-002.md',
    )
  })

  it('builds a flat-root card path when no directory is given', () => {
    expect(hypothesisCardPath('/ws/.research', '', 'H-001')).toBe(
      '/ws/.research/hypotheses/H-001.md',
    )
  })

  it('resolves the graph catalog as the "graph" card', () => {
    expect(hypothesisCardPath('/ws/.research', 'R-001', 'graph')).toBe(
      '/ws/.research/R-001/hypotheses/graph.md',
    )
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

  it('computes the incomplete frontier of a diamond without path enumeration', () => {
    // a(open) → {b,c}(terminal) → d(open): the frontier is {a, d}; every
    // explicit edge touches a pruned node, so none survive. Computed
    // directly, in O(N+E).
    const g = graphOf(
      [
        { id: 'a', status: 'open' },
        { id: 'b', status: 'confirmed', parents: ['a'] },
        { id: 'c', status: 'refuted', parents: ['a'] },
        { id: 'd', status: 'in-progress', parents: ['b', 'c'] },
      ],
      [
        { from: 'a', to: 'b' },
        { from: 'a', to: 'c' },
        { from: 'b', to: 'd' },
        { from: 'c', to: 'd' },
      ],
    )
    const filtered = buildDisplayGraph(g, { hideTerminal: true })
    expect(filtered.nodes.map((n) => n.id).sort()).toEqual(['a', 'd'])
    expect(filtered.edges).toEqual([])
  })

  it('handles a deep layered diamond in linear time (no path blow-up)', () => {
    // 14 stacked diamonds: 2^14 = 16384 root→leaf paths for the former
    // enumeration. The direct frontier computation stays O(N+E); the test
    // doubles as a guard against reintroducing path enumeration here.
    const nodes: { id: string; status?: string; parents?: string[] }[] = [{ id: 'L0a' }]
    const edges: { from: string; to: string }[] = []
    for (let i = 0; i < 14; i++) {
      const [prevX, prevY] = [`L${i}a`, `L${i}b`]
      const [nextX, nextY] = [`L${i + 1}a`, `L${i + 1}b`]
      nodes.push(
        { id: nextX, status: i % 2 === 0 ? 'confirmed' : 'open', parents: [prevX, prevY] },
        { id: nextY, status: i % 2 === 0 ? 'open' : 'confirmed', parents: [prevX, prevY] },
      )
      edges.push(
        { from: prevX, to: nextX },
        { from: prevX, to: nextY },
        { from: prevY, to: nextX },
        { from: prevY, to: nextY },
      )
    }
    const g = graphOf(nodes, edges)
    const filtered = buildDisplayGraph(g, { hideTerminal: true })
    // Every node sits in a mixed chain: open nodes survive, terminal drop.
    const openIds = nodes.filter((n) => n.status !== 'confirmed').map((n) => n.id)
    expect(filtered.nodes.map((n) => n.id).sort()).toEqual(openIds.slice().sort())
  })
})
