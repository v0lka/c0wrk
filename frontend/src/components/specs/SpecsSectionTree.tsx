// One declared TREE section of the active framework: a real nested tree over
// the payload's flat `nodes` forest. Every label, every nesting level and the
// set of sections come from the framework's OWN definition — nothing here
// knows a layout, a section name or a concept name.
//
// Expansion is caller-owned: `expandedNodes` maps node ids to their expanded
// flag, an ABSENT key meaning the caller's `defaultExpanded` (false — the
// sidebar panel's everything-collapsed default). Without `onToggleNode` every
// node renders as a plain always-expanded label (the preview/test shape).

import { useMemo, type ReactNode } from 'react'
import { AlertTriangle, ChevronDown, ChevronRight, FileText, FolderOpen } from 'lucide-react'
import { cn } from '@/lib/utils'
import type { SDDAction, SDDDocument, SDDNode, SDDView } from '@/types/models'
import {
  actionToDispatch,
  nodeActionEnabled,
  nodeActionReason,
  nodeActionsFor,
  type SpecsDispatch,
} from './specsActions'
import { documentDatesByPath } from './specsPaths'

/** Depth-first document count per node id — the header count chip of a folder
 *  or directory node (a change folder reports how many documents it holds).
 *
 *  `memberPaths` is the SECTION's own membership: a node shared by two tree
 *  sections (every single-root framework anchors its instances at the same
 *  top folder) must report the documents THAT section renders, not the whole
 *  forest — otherwise both sections print the same wrong total beside a
 *  correctly filtered child list. */
function documentCounts(nodes: SDDNode[], memberPaths?: Set<string>): Map<string, number> {
  const byId = new Map<string, SDDNode>()
  for (const node of nodes) byId.set(node.id, node)
  const counts = new Map<string, number>()
  const visit = (node: SDDNode, seen: Set<string>): number => {
    if (seen.has(node.id)) return 0
    seen.add(node.id)
    let total = node.kind === 'document' ? 1 : 0
    for (const childId of node.children) {
      if (memberPaths !== undefined && !memberPaths.has(childId)) continue
      const child = byId.get(childId)
      if (child !== undefined) total += visit(child, seen)
    }
    counts.set(node.id, total)
    return total
  }
  for (const node of nodes) {
    if (!counts.has(node.id)) visit(node, new Set())
  }
  return counts
}

function nodeIcon(node: SDDNode) {
  return node.kind === 'document' ? FileText : FolderOpen
}

/**
 * One node row: the label (a document opens in the file viewer), the date
 * column a dated document carries, the missing-companion warning of a
 * directory instance, and the actions the framework declared FOR THIS
 * concept — each dispatching its declared skill with `{path}`/`{name}`
 * substituted by this node.
 */
function NodeRow({
  node,
  depth,
  count,
  date,
  actions,
  expanded,
  collapsible,
  onToggle,
  onOpenDocument,
  onDispatch,
}: {
  node: SDDNode
  depth: number
  count: number
  date?: string
  actions: SDDAction[]
  expanded: boolean
  collapsible: boolean
  onToggle?: () => void
  onOpenDocument: (modelPath: string) => void
  onDispatch: (dispatch: SpecsDispatch, newSession: boolean) => void
}) {
  const Icon = nodeIcon(node)
  const accent = depth === 0
  const missing = node.missing ?? []
  return (
    <div className="flex items-center gap-1">
      {collapsible ? (
        <button
          type="button"
          data-testid="specs-tree-group"
          data-node={node.id}
          aria-expanded={expanded}
          title={expanded ? `Collapse ${node.label}` : `Expand ${node.label}`}
          onClick={onToggle}
          className={cn(
            'flex min-w-0 flex-1 items-center gap-1 rounded px-0.5 py-0.5 text-left text-xs transition-colors hover:bg-muted',
            accent ? 'font-semibold uppercase tracking-wide text-foreground' : 'text-foreground',
          )}
        >
          {expanded ? (
            <ChevronDown className="size-3 shrink-0" />
          ) : (
            <ChevronRight className="size-3 shrink-0" />
          )}
          <Icon className="size-3 shrink-0 text-muted-foreground" />
          <span className="min-w-0 truncate">{node.label}</span>
          <span className="shrink-0 text-muted-foreground/60">({count})</span>
        </button>
      ) : node.kind === 'document' ? (
        <button
          type="button"
          data-testid="specs-node"
          data-path={node.path}
          data-concept={node.concept ?? ''}
          title={node.path}
          onClick={() => onOpenDocument(node.path)}
          className={cn(
            'flex min-w-0 flex-1 items-center gap-1 rounded px-0.5 py-0.5 text-left text-xs transition-colors hover:bg-muted',
            accent ? 'font-semibold uppercase tracking-wide text-foreground' : 'text-foreground',
          )}
        >
          <Icon className="size-3 shrink-0 text-muted-foreground" />
          <span className="min-w-0 flex-1 truncate">{node.label}</span>
        </button>
      ) : (
        // A childless folder/directory is a LABEL, not a file link: opening
        // it in the file viewer would show a directory error for a click the
        // row never promised to be an open gesture.
        <span
          data-testid="specs-node"
          data-path={node.path}
          data-concept={node.concept ?? ''}
          title={node.path}
          className={cn(
            'flex min-w-0 flex-1 items-center gap-1 rounded px-0.5 py-0.5 text-left text-xs',
            accent ? 'font-semibold uppercase tracking-wide text-foreground' : 'text-foreground',
          )}
        >
          <Icon className="size-3 shrink-0 text-muted-foreground" />
          <span className="min-w-0 flex-1 truncate">{node.label}</span>
        </span>
      )}
      {missing.length > 0 && (
        <span
          data-testid="specs-node-missing"
          title={`Missing: ${missing.join(', ')}`}
          className="shrink-0 text-warning"
        >
          <AlertTriangle className="size-3" />
        </span>
      )}
      {date !== undefined && (
        <span
          data-testid="specs-node-date"
          className="w-[64px] shrink-0 text-right text-xs leading-none tabular-nums text-info/80"
          title={`Added ${date}`}
        >
          {date}
        </span>
      )}
      {actions.map((action) => {
        const dispatch = actionToDispatch(action, node)
        if (dispatch === null) return null
        // An unmet requirement renders DISABLED with the reason — the next
        // workflow step stays visible (the toolbar's contract), it just does
        // not run. The per-node gates (companion:/absent:) refine that gate to
        // THIS node, so a feature that already has its plan.md loses Plan while
        // an unplanned feature keeps it.
        const enabled = nodeActionEnabled(action, node)
        return (
          <button
            key={action.kind}
            type="button"
            data-testid="specs-node-action"
            data-action={action.kind}
            data-node={node.path}
            data-skill={dispatch.skill}
            disabled={!enabled}
            title={
              enabled
                ? `${action.label} (${dispatch.skill}) — Shift = new session`
                : nodeActionReason(action, node)
            }
            onClick={(e) => {
              if (!enabled) return
              onDispatch(dispatch, e.shiftKey)
            }}
            className="shrink-0 rounded border border-border bg-background px-1 text-xs text-foreground transition-colors hover:bg-muted disabled:pointer-events-none disabled:opacity-50"
          >
            {action.label}
          </button>
        )
      })}
    </div>
  )
}

/**
 * The tree section: `section.nodes` names the top-level entries of the
 * framework's forest; each entry renders its `children` recursively (one
 * indent step per level, in absolute units so it scales with the UI zoom).
 */
export function SpecsSectionTree({
  section,
  nodes,
  documents,
  actions,
  expandedNodes,
  onToggleNode,
  defaultExpanded = false,
  memberPaths,
  onOpenDocument,
  onDispatch,
}: {
  section: SDDView
  /** The payload's flat forest — nodes reference each other by id. */
  nodes: SDDNode[]
  /** Parsed documents: the source of the date column (never re-parsed). */
  documents: SDDDocument[]
  /** The framework's declared actions; the ones targeting a node's concept
   *  render on that node. */
  actions: SDDAction[]
  expandedNodes?: Record<string, boolean>
  onToggleNode?: (id: string) => void
  defaultExpanded?: boolean
  /** The node ids THIS section owns (its concepts' instances plus their
   *  ancestor folders), from the backend's per-section membership. When
   *  provided, a section renders only its own subtree: two sections sharing
   *  an ancestor folder stop bleeding each other's concepts through it. */
  memberPaths?: Set<string>
  onOpenDocument: (modelPath: string) => void
  onDispatch: (dispatch: SpecsDispatch, newSession: boolean) => void
}) {
  const byId = useMemo(() => {
    const map = new Map<string, SDDNode>()
    for (const node of nodes) map.set(node.id, node)
    return map
  }, [nodes])
  const counts = useMemo(() => documentCounts(nodes, memberPaths), [nodes, memberPaths])
  const dates = useMemo(() => documentDatesByPath(documents), [documents])

  if (section.nodes.length === 0) return null

  const renderNode = (id: string, depth: number): ReactNode => {
    const node = byId.get(id)
    if (node === undefined) return null // a view naming an absent node
    const children =
      memberPaths === undefined
        ? node.children
        : node.children.filter((childId) => memberPaths.has(childId))
    const collapsible = children.length > 0
    const expanded = collapsible
      ? (expandedNodes?.[node.id] ?? defaultExpanded)
      : true
    return (
      <div key={node.id}>
        <NodeRow
          node={node}
          depth={depth}
          count={counts.get(node.id) ?? 0}
          date={node.kind === 'document' ? dates[node.path] : undefined}
          actions={nodeActionsFor(actions, node.concept ?? '')}
          expanded={expanded}
          collapsible={collapsible}
          onToggle={collapsible ? () => onToggleNode?.(node.id) : undefined}
          onOpenDocument={onOpenDocument}
          onDispatch={onDispatch}
        />
        {expanded && children.length > 0 && (
          // One 16px indent step per nesting level (zoom-safe: absolute
          // lengths are meant to scale with the UI zoom).
          <div className="ml-4 flex flex-col gap-0.5">
            {children.map((childId) => renderNode(childId, depth + 1))}
          </div>
        )}
      </div>
    )
  }

  return (
    <section data-testid="specs-view" data-view={section.id} className="flex flex-col gap-1">
      <div className="px-0.5 text-xs uppercase tracking-wide text-muted-foreground">
        {section.label}
      </div>
      {section.nodes.map((id) => renderNode(id, 0))}
    </section>
  )
}
