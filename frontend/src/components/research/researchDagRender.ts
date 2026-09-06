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
// consecutive slots in DFS order and internal nodes center on their children;
// a separation post-pass then enforces a one-slot gap between same-level
// nodes (diamond siblings), so labels can never overlap by construction.

import type {
  HypothesisGraph,
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
/** Estimated px per id character at the 9px id font size. */
export const ID_CHAR_W = 5
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
 * Build the shared `parentId → childId[]` adjacency for a graph, UNIONING
 * explicit `edges` with declared `node.parents` — both encode the same
 * parent→child relation, and a partially-synced graph (cards vs graph.md)
 * may carry only one form, so either source alone would resolve a different
 * DAG. One builder serves layout, level assignment, and path traversal, so
 * every consumer agrees on the graph's shape. References to unknown ids and
 * self-loops are dropped; duplicates are deduped in first-seen order.
 */
export function buildChildrenMap(graph: HypothesisGraph): Map<string, string[]> {
  const known = new Set(graph.nodes.map((n) => n.id))
  const map = new Map<string, string[]>()
  const addChild = (parent: string, child: string): void => {
    if (parent === child || !known.has(parent) || !known.has(child)) return
    const list = map.get(parent)
    if (list) {
      if (!list.includes(child)) list.push(child)
    } else {
      map.set(parent, [child])
    }
  }
  for (const edge of graph.edges) addChild(edge.from, edge.to)
  for (const node of graph.nodes) {
    for (const p of node.parents ?? []) addChild(p, node.id)
  }
  return map
}

/**
 * Build a `childId → parentId[]` map for the graph by inverting the shared
 * adjacency (`buildChildrenMap`): explicit edges UNIONED with node `parents`
 * declarations. Unknown parent ids are dropped by the builder, so a dangling
 * reference never crashes the layout.
 */
export function buildParentMap(graph: HypothesisGraph): Map<string, string[]> {
  const childrenOf = buildChildrenMap(graph)
  const map = new Map<string, string[]>()
  for (const [parent, kids] of childrenOf) {
    for (const kid of kids) {
      const list = map.get(kid)
      if (list) {
        if (!list.includes(parent)) list.push(parent)
      } else {
        map.set(kid, [parent])
      }
    }
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
  /**
   * Left edge of the tight painted-content box, in layout coordinates.
   * Typically negative: id labels hang LEFT of their node
   * (`textAnchor="end"` at `node.x - LABEL_GAP_X`), so the box must open
   * left of x=0 for them to render inside the SVG viewport (an SVG clips
   * its own overflow, so content outside the box is invisible no matter
   * how the camera pans).
   */
  minX: number
  width: number
  height: number
}

/** Estimated painted width of a node id label (9px id font). */
export function idTextWidth(id: string): number {
  return id.length * ID_CHAR_W
}

/** Estimated painted width of a node title after the truncate budget (11px label font). */
export function titleTextWidth(title: string): number {
  return Math.min(title.length, LABEL_MAX_CHARS) * LABEL_CHAR_W
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
 * next row slot; internal nodes are placed in reverse topological (DFS
 * post-) order at the mean of their children's slots, so each subtree owns a
 * contiguous vertical band. Because same-level parents with equal child
 * means — the canonical diamond — would otherwise land on the identical
 * point, a final post-pass walks levels deepest-first, re-centers internal
 * nodes on their children's final slots, and pushes same-level nodes down
 * until every pair is at least one row slot apart, so node labels can never
 * overlap by construction. Cycles are broken by the DFS guard — the first
 * placed node of a cyclic component finds all of its children unplaced and
 * falls back to its own row slot instead of averaging nothing.
 * Pure — no DOM, safe to unit-test.
 */
export function layoutDag(graph: HypothesisGraph): DagLayout {
  if (graph.nodes.length === 0) {
    return { nodes: [], edges: [], minX: 0, width: 0, height: 0 }
  }

  const levels = assignLevels(graph)
  const parentMap = buildParentMap(graph)
  // Shared union adjacency (edges ∪ declared parents) — the same map the
  // path utilities resolve, so layout and traversal agree on the DAG.
  const childrenOf = buildChildrenMap(graph)

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

  // Separation + re-center post-pass. Same-level parents with equal child
  // means — the canonical diamond a→{b,c}→d — would otherwise land on the
  // identical point and visually merge into one node. Walk levels
  // deepest-first so internal nodes re-center on their children's FINAL
  // slots, then push same-level nodes down (minimal displacement, order
  // preserved) until every pair is at least one row slot apart.
  const byLevel = new Map<number, string[]>()
  let maxLevel = 0
  for (const node of graph.nodes) {
    const lvl = levels.get(node.id) ?? 0
    if (lvl > maxLevel) maxLevel = lvl
    const list = byLevel.get(lvl)
    if (list) list.push(node.id)
    else byLevel.set(lvl, [node.id])
  }
  let maxSlot = 0
  for (let lvl = maxLevel; lvl >= 0; lvl--) {
    const ids = byLevel.get(lvl)
    if (!ids) continue
    // Re-center internal nodes on their children's current slots. Deeper
    // levels are final by now; same-level children (cycle back-edges) are
    // read at their pre-sweep values, keeping the pass finite.
    for (const id of ids) {
      if (!childrenOf.has(id)) continue
      let sum = 0
      let count = 0
      for (const child of childrenOf.get(id) ?? []) {
        const slot = slots.get(child)
        if (slot === undefined) continue
        sum += slot
        count++
      }
      if (count > 0) slots.set(id, sum / count)
    }
    // Separate the level: stable sort by slot (ties keep input order), then
    // push each node down to the nearest slot restoring the one-slot gap.
    const order = ids
      .map((id) => ({ id, slot: slots.get(id) ?? 0 }))
      .sort((p, q) => p.slot - q.slot)
    let prev = Number.NEGATIVE_INFINITY
    for (const item of order) {
      if (item.slot < prev + 1) item.slot = prev + 1
      prev = item.slot
      slots.set(item.id, item.slot)
      if (item.slot > maxSlot) maxSlot = item.slot
    }
  }

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

  // Box: hug the painted content. Id labels hang LEFT of their node,
  // truncated titles hang RIGHT (both estimated per character, mirroring the
  // rendered text), with BOX_PAD breathing room on each side. The tight box
  // is what the pan/zoom camera fits and centers: a loose one (e.g. the full
  // 26-char budget after the last column, or a fixed extra allowance) leaves
  // the fitted graph pushed toward the canvas's left edge with dead space on
  // the right, and ids drawn at x < 0 are clipped by the SVG itself.
  let minX = Infinity
  let maxX = -Infinity
  for (const node of nodes) {
    minX = Math.min(minX, node.x - LABEL_GAP_X - idTextWidth(node.id))
    maxX = Math.max(maxX, node.x + LABEL_GAP_X + titleTextWidth(node.title))
  }
  minX -= BOX_PAD
  const width = maxX + BOX_PAD - minX
  // Height follows the deepest FINAL slot: the separation pass can push
  // same-level nodes past the last leaf slot, so nextSlot would undercount.
  const height = TOP_PAD + maxSlot * ROW_H + BOX_PAD
  return {
    nodes,
    edges,
    minX,
    width: Math.max(width, 0),
    height: Math.max(height, 0),
  }
}

/**
 * Cheap geometric signature of a layout: node ids/positions, edge
 * endpoints/anchors, and the fitted box. Two layouts with the same signature
 * paint identically, so a camera auto-fit keyed on this string fires only
 * when the geometry actually changed — not on every content-identical
 * refresh (the store replaces `project.graph` with a fresh object per
 * applied update, so layout IDENTITY changes constantly while the painted
 * graph usually does not). O(N+E) string build, memo-friendly. Pure.
 */
export function layoutSignature(layout: DagLayout): string {
  const nodes = layout.nodes.map((n) => `${n.id}@${n.x},${n.y}`).join('|')
  const edges = layout.edges
    .map((e) => `${e.from}>${e.to}@${e.x1},${e.y1},${e.x2},${e.y2}`)
    .join('|')
  return `${layout.minX},${layout.width},${layout.height};${nodes};${edges}`
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

// ── Display-graph filtering (terminal-hide toggle) ────────────────────

/** Options for `buildDisplayGraph`. */
export interface FilterGraphOptions {
  /** When true, hide terminal (completed) hypotheses from the display graph. */
  hideTerminal?: boolean
}

/**
 * Build the graph to render, optionally hiding terminal (completed) nodes.
 *
 * With `hideTerminal: true` the graph is reduced to the *incomplete
 * frontier* — computed directly in O(N+E) instead of enumerating every
 * root→leaf path (exponential in diamond depth): a path is incomplete iff
 * it contains at least one non-terminal node, and terminal pruning removes
 * exactly the terminal nodes, so a node survives iff it is itself
 * non-terminal (every node lies on at least one maximal chain, and any
 * chain through a non-terminal node is incomplete by definition). Only
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
  const nodes = graph.nodes.filter((n) => !isTerminal(n.status))
  const ids = new Set(nodes.map((n) => n.id))
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
 * Absolute path of a single hypothesis's markdown card
 * (`<base>/hypotheses/<id>.md`) so hypothesis mentions can open the card in
 * the file viewer. `rootPath` is the absolute research root
 * (ResearchRoot.path); `dir` is the project subdirectory (from `projectDir`,
 * "" for the flat layout). Pure and unit-tested.
 */
export function hypothesisCardPath(rootPath: string, dir: string, id: string): string {
  const base = dir ? `${rootPath}/${dir}` : rootPath
  return `${base}/hypotheses/${id}.md`
}

/**
 * Build absolute artifact paths for a research project so the panel's quick
 * links can open them in the file viewer. `rootPath` is the absolute research
 * root (ResearchRoot.path); `dir` is the project subdirectory (from
 * `projectDir`, "" for the flat layout). Pure and unit-tested.
 */
export function projectFilePaths(rootPath: string, dir: string): ResearchFilePaths {
  return {
    brief: `${dir ? `${rootPath}/${dir}` : rootPath}/brief.md`,
    priorArt: `${dir ? `${rootPath}/${dir}` : rootPath}/prior-art.md`,
    report: `${dir ? `${rootPath}/${dir}` : rootPath}/report.md`,
    // The graph "card" shares the hypotheses/ directory with the cards.
    graph: hypothesisCardPath(rootPath, dir, 'graph'),
  }
}
