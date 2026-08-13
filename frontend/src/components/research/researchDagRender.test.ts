import { describe, it, expect } from 'vitest'
import type { HypothesisGraph } from '@/types/models'
import {
  NODE_R,
  NODE_SPACING_X,
  NODE_SPACING_Y,
  LEFT_PAD,
  TOP_PAD,
  buildParentMap,
  assignLevels,
  layoutDag,
  xFor,
  yFor,
  edgePath,
  statusColorVar,
  formatRate,
  findAllRootToLeafPaths,
  projectDir,
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
  it('centers a row within the widest level', () => {
    // 2 nodes in a row, max breadth 4 → row offset centers them.
    const center = xFor(0, 2, 4)
    expect(center).toBe(LEFT_PAD + ((4 - 2) / 2) * NODE_SPACING_X)
  })

  it('places the first node of a full row at LEFT_PAD', () => {
    expect(xFor(0, 3, 3)).toBe(LEFT_PAD)
  })

  it('steps x by NODE_SPACING_X per index', () => {
    expect(xFor(1, 3, 3) - xFor(0, 3, 3)).toBe(NODE_SPACING_X)
  })

  it('steps y by NODE_SPACING_Y per level', () => {
    expect(yFor(0)).toBe(TOP_PAD)
    expect(yFor(1) - yFor(0)).toBe(NODE_SPACING_Y)
  })
})

describe('edgePath', () => {
  it('emits an SVG cubic-bezier path string', () => {
    const p = edgePath(0, 10, 20, 30)
    expect(p).toBe('M 0 10 C 0 20 20 20 20 30')
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

  it('emits parent→child edges with parent-bottom / child-top anchors', () => {
    const l = layoutDag(
      graphOf([{ id: 'a' }, { id: 'b', parents: ['a'] }]),
    )
    expect(l.edges).toHaveLength(1)
    const e = l.edges[0]!
    expect(e.from).toBe('a')
    expect(e.to).toBe('b')
    // child y anchor = child center − radius (top of the node)
    const child = l.nodes.find((n) => n.id === 'b')!
    expect(e.y2).toBe(child.y - NODE_R)
    // parent y anchor = parent center + radius (bottom of the node)
    const parent = l.nodes.find((n) => n.id === 'a')!
    expect(e.y1).toBe(parent.y + NODE_R)
    // parent is directly above child (same x), so x anchors coincide.
    expect(e.x1).toBe(e.x2)
  })

  it('stacks generations vertically by level', () => {
    const l = layoutDag(
      graphOf([
        { id: 'a' },
        { id: 'b', parents: ['a'] },
        { id: 'c', parents: ['b'] },
      ]),
    )
    const a = l.nodes.find((n) => n.id === 'a')!
    const c = l.nodes.find((n) => n.id === 'c')!
    expect(c.y).toBeGreaterThan(a.y)
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
