// Pure render constants + helpers for the RESEARCH hypothesis DAG.
//
// Modeled on GitPanel/gitGraphRender.ts: no React/DOM dependencies, fully
// unit-testable in isolation. The SVG DAG (ResearchDag.tsx) consumes the
// deterministic `layoutDag` output so geometry, status colors, and edge
// routing live in one source of truth.
//
// The hypothesis graph is a DAG where each node may have `parents` (the
// hypotheses it builds on). Roots (no parents) sit at the top; descendants
// flow downward by topological depth.

import type {
  HypothesisGraph,
  HypothesisNode,
  HypothesisStatus,
  ResearchRoot,
} from '@/types/models'

// ── Graph grid geometry ────────────────────────────────────────────────

/** Horizontal pitch between sibling nodes within the same depth level. */
export const NODE_SPACING_X = 96
/** Vertical pitch between depth levels (parent → child generations). */
export const NODE_SPACING_Y = 76
/** Left padding before the first node center. */
export const LEFT_PAD = 18
/** Top padding above the first (root) level. */
export const TOP_PAD = 24
/** Radius of a hypothesis node. */
export const NODE_R = 6
/** Extra right/bottom padding so node labels/edges don't clip the SVG box. */
export const BOX_PAD = 16

// ── Status colors (design-token CSS variables) ─────────────────────────

/** CSS variable (or fallback token) for a hypothesis status. */
export function statusColorVar(status: string): string {
  switch (status as HypothesisStatus | string) {
    case 'open':
      return 'var(--color-info)'
    case 'in-progress':
      return 'var(--color-warning)'
    case 'confirmed':
      return 'var(--color-success)'
    case 'refuted':
      return 'var(--color-destructive)'
    case 'cancelled':
      return 'var(--color-muted-foreground)'
    default:
      return 'var(--color-muted-foreground)'
  }
}

/** A stable Tailwind text-color class mirroring `statusColorVar`. */
export function statusTextClass(status: string): string {
  switch (status as HypothesisStatus | string) {
    case 'open':
      return 'text-info'
    case 'in-progress':
      return 'text-warning'
    case 'confirmed':
      return 'text-success'
    case 'refuted':
      return 'text-destructive'
    case 'cancelled':
    default:
      return 'text-muted-foreground'
  }
}

// ── Adjacency ──────────────────────────────────────────────────────────

/**
 * Build a `childId → parentId[]` map for the graph. Edges use `from` (parent)
 * → `to` (child); when there are no explicit edges, node `parents` arrays are
 * used instead. Unknown parent ids are dropped so a dangling reference never
 * crashes the layout.
 */
export function buildParentMap(graph: HypothesisGraph): Map<string, string[]> {
  const known = new Set(graph.nodes.map((n) => n.id))
  const map = new Map<string, string[]>()

  if (graph.edges.length > 0) {
    for (const edge of graph.edges) {
      if (!known.has(edge.from) || !known.has(edge.to)) continue
      const list = map.get(edge.to)
      if (list) {
        if (!list.includes(edge.from)) list.push(edge.from)
      } else {
        map.set(edge.to, [edge.from])
      }
    }
    return map
  }

  // Fall back to node.parents.
  for (const node of graph.nodes) {
    if (!node.parents || node.parents.length === 0) continue
    const parents = node.parents.filter((p) => known.has(p))
    if (parents.length > 0) map.set(node.id, parents)
  }
  return map
}

// ── Level assignment (longest path from a root) ────────────────────────

/**
 * Assign each node a depth level (0 for roots, parent.level + 1 otherwise).
 * Uses the longest-parent-path so a deep descendant always renders below its
 * ancestors. Guards against cycles by marking nodes currently on the DFS
 * stack — a back-edge node gets level 0 (treated as a root) instead of
 * recursing forever.
 */
export function assignLevels(graph: HypothesisGraph): Map<string, number> {
  const parentMap = buildParentMap(graph)
  const levels = new Map<string, number>()
  const onStack = new Set<string>()

  const levelOf = (id: string): number => {
    const cached = levels.get(id)
    if (cached !== undefined) return cached
    // Cycle guard: if we revisit a node mid-traversal, treat it as a root
    // rather than following the back-edge into infinite recursion.
    if (onStack.has(id)) return 0
    onStack.add(id)
    const parents = parentMap.get(id)
    let depth = 0
    if (parents) {
      for (const p of parents) {
        depth = Math.max(depth, levelOf(p) + 1)
      }
    }
    onStack.delete(id)
    levels.set(id, depth)
    return depth
  }

  for (const node of graph.nodes) levelOf(node.id)
  return levels
}

// ── Layout ─────────────────────────────────────────────────────────────

export interface PositionedNode {
  id: string
  title: string
  status: string
  level: number
  x: number
  y: number
  result?: string
}

export interface PositionedEdge {
  from: string
  to: string
  x1: number
  y1: number
  x2: number
  y2: number
}

export interface DagLayout {
  nodes: PositionedNode[]
  edges: PositionedEdge[]
  width: number
  height: number
}

/** X coordinate (px) of a node center given its row index and row breadth. */
export function xFor(index: number, rowBreadth: number, maxBreadth: number): number {
  const startOffset = ((maxBreadth - rowBreadth) / 2) * NODE_SPACING_X
  return LEFT_PAD + startOffset + index * NODE_SPACING_X
}

/** Y coordinate (px) of a node center given its depth level. */
export function yFor(level: number): number {
  return TOP_PAD + level * NODE_SPACING_Y
}

/**
 * Deterministic layered layout for a hypothesis DAG. Groups nodes by depth
 * level, centers each row within the widest level, and emits positioned
 * edges (parent bottom → child top). Pure — no DOM, safe to unit-test.
 */
export function layoutDag(graph: HypothesisGraph): DagLayout {
  if (graph.nodes.length === 0) {
    return { nodes: [], edges: [], width: 0, height: 0 }
  }

  const levels = assignLevels(graph)
  const maxLevel = Math.max(0, ...levels.values())

  // Group node ids by level, preserving input order for stability.
  const byLevel = new Map<number, string[]>()
  for (const node of graph.nodes) {
    const lvl = levels.get(node.id) ?? 0
    const row = byLevel.get(lvl)
    if (row) row.push(node.id)
    else byLevel.set(lvl, [node.id])
  }

  const maxBreadth = Math.max(1, ...[...byLevel.values()].map((r) => r.length))

  // Assign x/y to every node.
  const pos = new Map<string, PositionedNode>()
  const nodes: PositionedNode[] = []
  for (const node of graph.nodes) {
    const lvl = levels.get(node.id) ?? 0
    const row = byLevel.get(lvl) ?? []
    const index = row.indexOf(node.id)
    nodes.push({
      id: node.id,
      title: node.title,
      status: node.status,
      level: lvl,
      x: xFor(index, row.length, maxBreadth),
      y: yFor(lvl),
      result: node.result,
    })
    pos.set(node.id, nodes[nodes.length - 1]!)
  }

  // Emit edges (parent → child), parent bottom-center to child top-center.
  const parentMap = buildParentMap(graph)
  const edges: PositionedEdge[] = []
  for (const node of graph.nodes) {
    const child = pos.get(node.id)
    if (!child) continue
    const parents = parentMap.get(node.id)
    if (!parents) continue
    for (const pid of parents) {
      const parent = pos.get(pid)
      if (!parent) continue
      edges.push({
        from: pid,
        to: node.id,
        x1: parent.x,
        y1: parent.y + NODE_R,
        x2: child.x,
        y2: child.y - NODE_R,
      })
    }
  }

  const width = LEFT_PAD + maxBreadth * NODE_SPACING_X - (NODE_SPACING_X - BOX_PAD)
  const height = TOP_PAD + maxLevel * NODE_SPACING_Y + BOX_PAD
  return { nodes, edges, width: Math.max(width, 0), height: Math.max(height, 0) }
}

// ── Edge path ──────────────────────────────────────────────────────────

/** Cubic-bezier path between two points (vertical-leaning curve). */
export function edgePath(x1: number, y1: number, x2: number, y2: number): string {
  const ym = (y1 + y2) / 2
  return `M ${x1} ${y1} C ${x1} ${ym} ${x2} ${ym} ${x2} ${y2}`
}

// ── Metrics formatting ─────────────────────────────────────────────────

/**
 * Format the confirmation rate (0..1) as a percentage string. Pure, so the
 * metrics row and its tests share one formatter.
 */
export function formatRate(rate: number): string {
  if (!Number.isFinite(rate) || rate < 0) return '0%'
  return `${Math.round(rate * 100)}%`
}

// ── Hypothesis paths (root-to-leaf enumeration) ───────────────────────

/** A hypothesis plus its depth in the path (0 for roots). */
export interface TreeNode {
  node: HypothesisNode
  depth: number
}

/**
 * A single root-to-leaf path through the hypothesis DAG.
 * Each entry in `path` is a `{ node, depth }` where depth is the index
 * within the path (0 = most-general ancestor, path.length - 1 = leaf).
 */
export interface PathEntry {
  /** Ordered sequence of nodes from root (index 0) to leaf (last index). */
  path: TreeNode[]
}

/**
 * Enumerate all root-to-leaf paths in the hypothesis DAG.
 *
 * A *root* is any node with no parents (in-degree 0). A *leaf* is any node
 * with no children (out-degree 0). Every maximal chain root → … → leaf is
 * emitted as a `PathEntry`.
 *
 * **Algorithm:** DFS from every root, tracking an on-path visited set to
 * break cycles. When a node has no unvisited children, the current path is
 * a complete root-to-leaf path and is appended to the result.
 *
 * **Diamond-safe:** because the visited set is cleared on backtrack, a
 * node that sits at the convergence of multiple branches appears in
 * *every* path that reaches it — which is the correct enumeration semantics.
 *
 * **Cycle-safe:** the on-path set prevents infinite recursion on malformed
 * graphs with back-edges.
 *
 * Pure — no DOM — and unit-tested.
 */
export function findAllRootToLeafPaths(graph: HypothesisGraph): PathEntry[] {
  if (graph.nodes.length === 0) return []

  const known = new Set(graph.nodes.map((n) => n.id))
  const childrenOf = new Map<string, string[]>()
  const addChild = (parent: string, child: string) => {
    if (parent === child) return
    const arr = childrenOf.get(parent)
    if (arr) {
      if (!arr.includes(child)) arr.push(child)
    } else {
      childrenOf.set(parent, [child])
    }
  }
  // Union of explicit edges and declared parents (matches BuildGraph's edge
  // reconciliation) so the tree is complete for partial graphs.
  for (const e of graph.edges) {
    if (known.has(e.from) && known.has(e.to)) addChild(e.from, e.to)
  }
  for (const n of graph.nodes) {
    for (const p of n.parents ?? []) {
      if (known.has(p)) addChild(p, n.id)
    }
  }

  const hasParent = new Set<string>()
  for (const kids of childrenOf.values()) {
    for (const k of kids) hasParent.add(k)
  }

  // Roots: nodes with no parent.
  const roots = graph.nodes
    .filter((n) => !hasParent.has(n.id))
    .map((n) => n.id)
    .sort()

  // If no roots exist (all nodes have parents → cycle), treat all nodes as
  // potential starting points.
  const startingNodes = roots.length > 0 ? roots : graph.nodes.map((n) => n.id).sort()

  const result: PathEntry[] = []

  // DFS from each root, collecting complete root→leaf paths.
  const onPath = new Set<string>()
  const currentPath: string[] = []

  const dfs = (id: string) => {
    onPath.add(id)
    currentPath.push(id)

    const kids = (childrenOf.get(id) ?? []).slice().sort()
    const unvisitedKids = kids.filter((c) => !onPath.has(c))

    if (unvisitedKids.length === 0) {
      // Leaf (or all children already on-path → cycle boundary).
      // Emit the current path.
      result.push({
        path: currentPath.map((nid, depth) => ({
          node: graph.nodes.find((n) => n.id === nid)!,
          depth,
        })),
      })
    } else {
      for (const c of unvisitedKids) {
        dfs(c)
      }
    }

    currentPath.pop()
    onPath.delete(id)
  }

  for (const r of startingNodes) {
    if (!onPath.has(r)) dfs(r)
  }

  // Defensive: append any orphan not reached from a root as a single-node path.
  const reached = new Set<string>()
  for (const entry of result) {
    for (const tn of entry.path) reached.add(tn.node.id)
  }
  for (const n of graph.nodes) {
    if (!reached.has(n.id)) {
      result.push({
        path: [{ node: n, depth: 0 }],
      })
    }
  }

  return result
}

// ── Research file paths (derive artifact locations for "open in viewer") ─

/** Absolute artifact file paths for one research project, for the viewer. */
export interface ResearchFilePaths {
  brief: string
  priorArt: string
  report: string
  /** The hypothesis catalog / Mermaid graph (where new hypotheses land). */
  graph: string
}

/**
 * Resolve the project's directory prefix relative to the research root from
 * the root index entry (whose `path` is a brief.md link like
 * "R-002-flawgate/brief.md" or, for the flat single-project layout, just
 * "brief.md"). Returns "" for the flat layout (artifacts live at the root).
 * When no index entry matches, "" is returned (flat fallback).
 */
export function projectDir(root: ResearchRoot | undefined, projectId: string): string {
  const entry = root?.index?.find((e) => e.id === projectId)
  if (!entry?.path) return ''
  const idx = entry.path.lastIndexOf('/')
  return idx >= 0 ? entry.path.slice(0, idx) : ''
}

/**
 * Build absolute artifact paths for a research project so the panel's quick
 * links can open them in the file viewer. `rootPath` is the absolute research
 * root (ResearchRoot.path); `dir` is the project subdirectory (from
 * `projectDir`, "" for the flat layout). Pure and unit-tested.
 */
export function projectFilePaths(rootPath: string, dir: string): ResearchFilePaths {
  const base = dir ? `${rootPath}/${dir}` : rootPath
  return {
    brief: `${base}/brief.md`,
    priorArt: `${base}/prior-art.md`,
    report: `${base}/report.md`,
    graph: `${base}/hypotheses/graph.md`,
  }
}
