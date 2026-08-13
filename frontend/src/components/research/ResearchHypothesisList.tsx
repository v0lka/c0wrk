import { useMemo } from 'react'
import { ChevronRight } from 'lucide-react'
import type { HypothesisGraph } from '@/types/models'
import { cn } from '@/lib/utils'
import {
  findAllRootToLeafPaths,
  mergePathsToTree,
  statusColorVar,
  statusTextClass,
  type MergedTreeNode,
} from './researchDagRender'

interface ResearchHypothesisListProps {
  /** The hypothesis graph to render as root-to-leaf paths. */
  graph: HypothesisGraph
  /** Currently selected node id (highlighted). */
  selectedId?: string
  /** Called when a hypothesis row is clicked. */
  onSelectNode?: (id: string) => void
}

/**
 * Research hypothesis tree — a merged hierarchy from the hypothesis DAG.
 *
 * Shared prefixes (e.g. a diamond DAG where two paths converge on a common
 * ancestor) are collapsed into a single node with diverging children,
 * eliminating the duplication that the flat path list produced.
 *
 * Each row is clickable (toggles selection), shows a status dot, the ID, the
 * title, and — when selected — expands an inline detail panel (status badge,
 * timebox, result, parents). Selection is local state owned by the parent so
 * only one detail panel is open at a time.
 */
export function ResearchHypothesisList({
  graph,
  selectedId,
  onSelectNode,
}: ResearchHypothesisListProps) {
  const tree = useMemo(() => mergePathsToTree(findAllRootToLeafPaths(graph)), [graph])

  if (tree.length === 0) {
    return (
      <div className="flex flex-1 items-center justify-center py-8 text-xs text-muted-foreground select-none">
        No hypotheses yet
      </div>
    )
  }

  return (
    <ul className="flex flex-col gap-0.5 py-1 select-none" role="tree" aria-label="Hypothesis tree">
      {tree.map((node, index) => (
        <MergedTreeNodeRow
          key={node.node.id}
          node={node}
          selectedId={selectedId}
          onSelectNode={onSelectNode}
          depth={0}
          isFirst={index === 0}
        />
      ))}
    </ul>
  )
}

/** Recursive render for a merged tree node and all its descendants. */
interface MergedTreeNodeRowProps {
  node: MergedTreeNode
  selectedId?: string
  onSelectNode?: (id: string) => void
  depth: number
  isFirst: boolean
}

function MergedTreeNodeRow({
  node,
  selectedId,
  onSelectNode,
  depth,
  isFirst,
}: MergedTreeNodeRowProps) {
  const { children } = node

  return (
    <>
      {!isFirst && depth === 0 && (
        <li className="py-1">
          <hr className="border-border/60" />
        </li>
      )}
      <HypothesisRow
        entry={{ node: node.node, depth }}
        selected={node.node.id === selectedId}
        onSelect={onSelectNode}
        isRoot={depth === 0}
      />
      {children.map((child, childIndex) => (
        <MergedTreeNodeRow
          key={child.node.id}
          node={child}
          selectedId={selectedId}
          onSelectNode={onSelectNode}
          depth={depth + 1}
          isFirst={childIndex === 0}
        />
      ))}
    </>
  )
}

interface HypothesisRowProps {
  entry: { node: { id: string; title: string; status: string; parents?: string[]; timebox?: string; result?: string }; depth: number }
  selected: boolean
  onSelect?: (id: string) => void
  isRoot?: boolean
}

function HypothesisRow({ entry, selected, onSelect, isRoot }: HypothesisRowProps) {
  const { node, depth } = entry
  const isClickable = !!onSelect

  return (
    <li role="treeitem" aria-expanded={selected} aria-selected={selected}>
      <div
        onClick={isClickable ? () => onSelect!(node.id) : undefined}
        onKeyDown={
          isClickable
            ? (e) => {
                if (e.key === 'Enter' || e.key === ' ') {
                  e.preventDefault()
                  onSelect!(node.id)
                }
              }
            : undefined
        }
        tabIndex={isClickable ? 0 : undefined}
        className={cn(
          'group flex items-start gap-1.5 rounded-sm px-1.5 py-1 text-xs transition-colors',
          isClickable && 'cursor-pointer hover:bg-muted',
          selected && 'bg-muted',
          isRoot && 'border-l-2 border-success/40 pl-1',
        )}
        style={{ paddingLeft: (isRoot ? 1 : 6) + depth * 14 }}
      >
        {/* Depth indicator: chevron for non-root, spacer for root. */}
        {depth > 0 ? (
          <span
            className="mt-[3px] shrink-0 text-muted-foreground/50"
            aria-hidden
          >
            <ChevronRight className="size-3" />
          </span>
        ) : null}

        {/* Status dot — colored by lifecycle. */}
        <span
          className="mt-[5px] size-2 shrink-0 rounded-full"
          style={{ backgroundColor: statusColorVar(node.status) }}
          aria-hidden
        />

        <div className="min-w-0 flex-1">
          <div className="flex items-baseline gap-1.5">
            <span className="shrink-0 font-mono text-[10px] text-muted-foreground">
              {node.id}
            </span>
            <span className={cn('truncate', isRoot && 'font-semibold')}>
              {node.title}
            </span>
          </div>

          {/* Inline detail (only for the selected row). */}
          {selected && <HypothesisDetail node={node} />}
        </div>
      </div>
    </li>
  )
}

function HypothesisDetail({ node }: { node: { id: string; title: string; status: string; parents?: string[]; timebox?: string; result?: string } }) {
  const parents = (node.parents ?? []).join(', ')
  const hasExtras = node.result || node.timebox || parents
  return (
    <div className="mt-1 flex flex-col gap-1 text-[11px] leading-relaxed text-muted-foreground">
      <div className="flex flex-wrap items-center gap-1.5">
        <span
          className={cn(
            'inline-flex items-center gap-1 rounded px-1 py-0.5 text-[10px] font-medium',
            statusTextClass(node.status),
          )}
        >
          <span
            className="size-1.5 rounded-full"
            style={{ backgroundColor: statusColorVar(node.status) }}
          />
          {node.status}
        </span>
        {node.timebox && (
          <span className="text-muted-foreground/80">⏱ {node.timebox}</span>
        )}
      </div>
      {parents && (
        <div>
          <span className="text-muted-foreground/60">parents:</span>{' '}
          <span className="font-mono text-[10px]">{parents}</span>
        </div>
      )}
      {node.result && (
        <div className="rounded bg-background/60 px-1.5 py-1 text-foreground/80">
          <span className="font-medium text-muted-foreground">result:</span>{' '}
          {node.result}
        </div>
      )}
      {!hasExtras && (
        <span className="italic text-muted-foreground/60">No details recorded</span>
      )}
    </div>
  )
}
