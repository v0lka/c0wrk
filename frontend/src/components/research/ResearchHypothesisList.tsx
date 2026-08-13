import { useMemo } from 'react'
import { ChevronRight } from 'lucide-react'
import type { HypothesisGraph } from '@/types/models'
import { cn } from '@/lib/utils'
import {
  findAllRootToLeafPaths,
  statusColorVar,
  statusTextClass,
  type PathEntry,
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
 * Research hypothesis paths — a forest of root-to-leaf chains through the
 * hypothesis DAG.
 *
 * Unlike the old flattened tree (DFS preorder), each top-level entry in the
 * list is now a *complete path* from the most general ancestor to a leaf
 * node. Nodes within a path are indented by their depth (position in the
 * path), preserving the ancestor-to-leaf sequence.
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
  const paths = useMemo(() => findAllRootToLeafPaths(graph), [graph])

  if (paths.length === 0) {
    return (
      <div className="flex flex-1 items-center justify-center py-8 text-xs text-muted-foreground select-none">
        No hypotheses yet
      </div>
    )
  }

  return (
    <ul className="flex flex-col gap-0.5 py-1 select-none" role="tree" aria-label="Hypothesis paths">
      {paths.map((entry, pathIndex) => (
        <PathGroup
          key={pathIndex}
          entry={entry}
          selectedId={selectedId}
          onSelectNode={onSelectNode}
          isFirst={pathIndex === 0}
        />
      ))}
    </ul>
  )
}

/** A single root-to-leaf path rendered as an indented group. */
interface PathGroupProps {
  entry: PathEntry
  selectedId?: string
  onSelectNode?: (id: string) => void
  isFirst: boolean
}

function PathGroup({ entry, selectedId, onSelectNode, isFirst }: PathGroupProps) {
  const { path } = entry

  return (
    <>
      {!isFirst && (
        <li className="py-1">
          <hr className="border-border/60" />
        </li>
      )}
      {path.map(({ node, depth }) => (
        <HypothesisRow
          key={node.id}
          entry={{ node, depth }}
          selected={node.id === selectedId}
          onSelect={onSelectNode}
          isRoot={depth === 0}
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
