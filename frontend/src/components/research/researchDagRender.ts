// Pure render constants + helpers for the RESEARCH hypothesis DAG.
//
// Modeled on GitPanel/gitGraphRender.ts: no React/DOM dependencies, fully
// unit-testable in isolation. The research workspace renders it through the
// pan/zoom camera in ResearchDagCanvas.tsx (its DagSvg component), consuming
// the deterministic `layoutDag` output so geometry, status colors, and edge
// routing live in one source of truth.
//
// The hypothesis graph is a DAG where each node may have `parents` (the
// hypotheses it builds on). `layoutDag` flows left-to-right: depth columns
// (roots leftmost, descendants rightward) crossed by row slots — leaves take
// consecutive slots in DFS order and internal nodes center on their children,
// so node labels can never overlap by construction.

import type {
  HypothesisGraph,
  HypothesisNode,
  HypothesisStatus,
  ResearchRoot,
} from '@/types/models'

// ── Graph grid geometry ────────────────────────────────────────────────

/** Left padding before the first node center. */
export const LEFT_PAD = 18
/** Top padding above the first row slot. */
export const TOP_PAD = 24
/** Radius of a hypothesis node. */
export const NODE_R = 6
/** Extra right/bottom padding so node labels/edges don't clip the SVG box. */
export const BOX_PAD = 16
/** Vertical pitch between row slots (one label line plus breathing room). */
export const ROW_H = 28
/** Gap between a node circle and its label text (mirrors the DAG view). */
export const LABEL_GAP_X = NODE_R + 4
/** Max characters of label text rendered beside a node (26-char truncate budget). */
export const LABEL_MAX_CHARS = 26
/** Estimated px per label character at the 11px label font size. */
export const LABEL_CHAR_W = 6
/** Breathing room after the label zone before the next column starts. */
export const COLUMN_GAP = 20
/** Horizontal pitch between depth columns: 26-char label budget + gap. */
export const COLUMN_W = LABEL_GAP_X + LABEL_MAX_CHARS * LABEL_CHAR_W + COLUMN_GAP

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

/**
 * True only for the three terminal lifecycle statuses (confirmed, refuted,
 * cancelled). Everything else (`open`, `in-progress`, or unknown) is still in
 * flight and therefore non-terminal.
 */
export function isTerminal(status: string): boolean {
  return status === 'confirmed' || status === 'refuted' || status === 'cancelled'
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

/** X coordinate (px) of a node center: one column per topological depth level. */
export function xFor(level: number): number {
  return LEFT_PAD + level * COLUMN_W
}

/** Y coordinate (px) of a node center for a row slot (fractional slots center internal nodes). */
export function yFor(slot: number): number {
  return TOP_PAD + slot * ROW_H
}

/**
 * Deterministic left-to-right layered layout for a hypothesis DAG.
 *
 * Columns follow topological depth (roots leftmost, via `xFor`). Rows come
 * from leaf ordering: a DFS hands each leaf (a node without children) the
 * next row slot; internal nodes are then placed in reverse topological (DFS
 * post-) order at the mean of their children's slots, so each subtree owns a
 * contiguous vertical band and labels cannot overlap by construction.
 * Cycles are broken by the DFS guard — the first placed node of a cyclic
 * component finds all of its children unplaced and falls back to its own
 * row slot instead of averaging nothing.
 * Pure — no DOM, safe to unit-test.
 */
export function layoutDag(graph: HypothesisGraph): DagLayout {
  if (graph.nodes.length === 0) {
    return { nodes: [], edges: [], width: 0, height: 0 }
  }

  const levels = assignLevels(graph)
  const parentMap = buildParentMap(graph)

  // Invert child→parents into deduped parent→children lists (input order).
  const childrenOf = new Map<string, string[]>()
  for (const node of graph.nodes) {
    const parents = parentMap.get(node.id)
    if (!parents) continue
    for (const pid of parents) {
      const list = childrenOf.get(pid)
      if (list) {
        if (!list.includes(node.id)) list.push(node.id)
      } else {
        childrenOf.set(pid, [node.id])
      }
    }
  }

  // DFS post-order = children before parents (reverse topological order).
  // Start from parentless roots, then sweep any leftover nodes (pure cycles
  // have no root) in input order so every node is visited exactly once.
  const slots = new Map<string, number>()
  const postOrder: string[] = []
  const visited = new Set<string>()
  let nextSlot = 0

  const dfs = (id: string): void => {
    if (visited.has(id)) return
    visited.add(id)
    for (const child of childrenOf.get(id) ?? []) dfs(child)
    postOrder.push(id)
    // A leaf (no children) claims the next row slot in DFS order.
    if (!childrenOf.has(id)) slots.set(id, nextSlot++)
  }

  for (const node of graph.nodes) {
    if (!parentMap.has(node.id)) dfs(node.id)
  }
  for (const node of graph.nodes) dfs(node.id)

  // Internal nodes: mean of their children's slots (children are placed
  // already). Cycle guard: if every child is an unplaced back-edge, claim
  // the next slot rather than averaging nothing.
  for (const id of postOrder) {
    if (slots.has(id)) continue
    let sum = 0
    let count = 0
    for (const child of childrenOf.get(id) ?? []) {
      const slot = slots.get(child)
      if (slot === undefined) continue
      sum += slot
      count++
    }
    slots.set(id, count > 0 ? sum / count : nextSlot++)
  }

  const maxLevel = Math.max(0, ...levels.values())

  // Assign x/y to every node.
  const pos = new Map<string, PositionedNode>()
  const nodes: PositionedNode[] = []
  for (const node of graph.nodes) {
    const lvl = levels.get(node.id) ?? 0
    const slot = slots.get(node.id) ?? 0
    const positioned: PositionedNode = {
      id: node.id,
      title: node.title,
      status: node.status,
      level: lvl,
      x: xFor(lvl),
      y: yFor(slot),
      result: node.result,
    }
    nodes.push(positioned)
    pos.set(node.id, positioned)
  }

  // Emit edges (parent → child), parent right-center to child left-center.
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
        x1: parent.x + NODE_R,
        y1: parent.y,
        x2: child.x - NODE_R,
        y2: child.y,
      })
    }
  }

  // Box: last column plus a full label budget; last row slot plus padding.
  const width = xFor(maxLevel) + LABEL_GAP_X + LABEL_MAX_CHARS * LABEL_CHAR_W + BOX_PAD
  const height = TOP_PAD + Math.max(0, nextSlot - 1) * ROW_H + BOX_PAD
  return { nodes, edges, width: Math.max(width, 0), height: Math.max(height, 0) }
}

// ── Edge path ──────────────────────────────────────────────────────────

/** Cubic-bezier path between two points (horizontal-leaning curve). */
export function edgePathH(x1: number, y1: number, x2: number, y2: number): string {
  const xm = (x1 + x2) / 2
  return `M ${x1} ${y1} C ${xm} ${y1} ${xm} ${y2} ${x2} ${y2}`
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

// ── Merged tree (shared-prefix collapse) ──────────────────────────────

/**
 * A node in the merged hypothesis tree. Shares a single `node` instance
 * across all paths that pass through it, and collects its descendants in
 * `children` — eliminating the duplication that the flat path list produces.
 */
export interface MergedTreeNode {
  node: HypothesisNode
  children: MergedTreeNode[]
}

/**
 * Convert a flat list of root-to-leaf paths into a single merged tree
 * where shared prefixes are collapsed into one node.
 *
 * **Algorithm:** Walk each path depth-first, inserting nodes into the tree.
 * When a node with the same `id` already exists at the expected depth,
 * reuse it (the depth is identical because the DAG guarantees a unique
 * topological level for each node). Otherwise create a new child node.
 *
 * **Complexity:** O(Σ path_length × branching_factor) — proportional to the
 * total number of path entries, not O(N²). Each insertion is O(1) because
 * we traverse the existing tree depth-by-depth and match by node id at each
 * level.
 *
 * Pure — no DOM — and unit-tested.
 */
export function mergePathsToTree(paths: PathEntry[]): MergedTreeNode[] {
  if (paths.length === 0) return []

  const roots: MergedTreeNode[] = []

  for (const entry of paths) {
    const path = entry.path
    if (path.length === 0) continue

    let depth = 0
    let parent: MergedTreeNode | null = null

    for (const treeNode of path) {
      const nodeId = treeNode.node.id

      if (depth === 0) {
        // Root level: look for an existing root with this id.
        let found = roots.find((r) => r.node.id === nodeId)
        if (!found) {
          found = { node: treeNode.node, children: [] }
          roots.push(found)
        }
        parent = found
        depth = 1
      } else {
        // Non-root: look for this node among parent's children at the
        // expected depth.
        let child: MergedTreeNode | undefined = parent!.children.find((c) => c.node.id === nodeId)
        if (!child) {
          child = { node: treeNode.node, children: [] }
          parent!.children.push(child)
        }
        parent = child
        depth++
      }
    }
  }

  return roots
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

// ── Incomplete-path filtering ─────────────────────────────────────────

/**
 * Enumerate only the *incomplete* root-to-leaf paths — those that still
 * contain at least one non-terminal node (`open` / `in-progress`). Paths whose
 * nodes are all terminal (`confirmed` / `refuted` / `cancelled`) are fully
 * worked and are dropped. Empty graph → `[]`.
 *
 * Reuses `findAllRootToLeafPaths`; pure and unit-tested.
 */
export function findIncompletePaths(graph: HypothesisGraph): PathEntry[] {
  return findAllRootToLeafPaths(graph).filter((entry) =>
    entry.path.some((t) => !isTerminal(t.node.status)),
  )
}

/** Options for `filterPaths`. */
export interface FilterPathsOptions {
  /**
   * When true, terminal (`confirmed` / `refuted` / `cancelled`) nodes are
   * pruned from each path, and paths that become empty are dropped. Defaults
   * to false (paths returned unchanged).
   */
  hideTerminal?: boolean
}

/**
 * Filter an already-enumerated path list for rendering. With
 * `hideTerminal: true`, terminal nodes are removed from each path (depths are
 * re-indexed so the pruned path stays a valid `PathEntry`). Paths left empty
 * by pruning are dropped. With `hideTerminal` false/omitted the input is
 * returned unchanged. Pure — no DOM — and unit-tested.
 */
export function filterPaths(
  paths: PathEntry[],
  options: FilterPathsOptions = {},
): PathEntry[] {
  if (!options.hideTerminal) return paths
  const result: PathEntry[] = []
  for (const entry of paths) {
    const kept = entry.path.filter((t) => !isTerminal(t.node.status))
    if (kept.length === 0) continue
    result.push({ path: kept.map((t, depth) => ({ node: t.node, depth })) })
  }
  return result
}

// ── Display-graph filtering (terminal-hide toggle) ────────────────────

/** Options for `buildDisplayGraph`. */
export interface FilterGraphOptions {
  /** When true, hide terminal (completed) hypotheses from the display graph. */
  hideTerminal?: boolean
}

/**
 * Build the graph to render, optionally hiding terminal (completed) nodes.
 *
 * With `hideTerminal: true` the input graph is reduced to the *incomplete
 * frontier*: fully-terminal root→leaf paths are dropped (via
 * `findIncompletePaths`) and terminal nodes inside the remaining mixed paths
 * are pruned (via `filterPaths`), so completed hypotheses disappear from the
 * active front. The surviving node ids become the filtered node set, and only
 * edges whose both endpoints survive are kept. With `hideTerminal`
 * false/omitted the input graph is returned unchanged (reference equality).
 *
 * Pure — no DOM — and unit-tested.
 */
export function buildDisplayGraph(
  graph: HypothesisGraph,
  options: FilterGraphOptions = {},
): HypothesisGraph {
  if (!options.hideTerminal) return graph
  const paths = filterPaths(findIncompletePaths(graph), { hideTerminal: true })
  const ids = new Set<string>()
  for (const entry of paths) {
    for (const t of entry.path) ids.add(t.node.id)
  }
  const nodes = graph.nodes.filter((n) => ids.has(n.id))
  const edges = graph.edges.filter((e) => ids.has(e.from) && ids.has(e.to))
  return { nodes, edges }
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
 * "R-002-project/brief.md" or, for the flat single-project layout, just
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
