import { useMemo } from 'react'
import { Loader2, AlertCircle, GitCommit } from 'lucide-react'
import { computeGraphLayout, shortSha, type GraphNode } from '@/lib/gitGraphLayout'
import { useGitGraph } from '@/hooks/useGitGraph'

const ROW_SPACING = 28
const LANE_SPACING = 22
const LEFT_PAD = 12
const NODE_R = 4
const MERGE_R = 5

/** Design-token CSS variables cycled per lane for branch colors. */
const LANE_VARS = [
  '--color-info',
  '--color-success',
  '--color-warning',
  '--color-highlight',
  '--color-hljs-keyword',
  '--color-hljs-literal',
]

function laneVar(lane: number): string {
  return LANE_VARS[lane % LANE_VARS.length]!
}

function xFor(lane: number): number {
  return LEFT_PAD + lane * LANE_SPACING
}

function yFor(row: number): number {
  return row * ROW_SPACING + ROW_SPACING / 2
}

/** Cubic-bezier path between two grid points (vertical-leaning curve). */
function edgePath(x1: number, y1: number, x2: number, y2: number): string {
  const ym = (y1 + y2) / 2
  return `M ${x1} ${y1} C ${x1} ${ym} ${x2} ${ym} ${x2} ${y2}`
}

/** Color a ref decoration: HEAD → highlight, tag → warning, branch → info. */
function refColor(ref: string): string {
  if (ref.startsWith('HEAD')) return 'text-highlight'
  if (ref.startsWith('tag:')) return 'text-warning'
  return 'text-info'
}

/**
 * Commit graph tab (Phase 6). Loads the graph one page at a time from the
 * backend (server-side pagination via GetGitGraph(limit, skip)) and appends
 * older commits on "Load more" (FE-2 / B5). Data loading lives in
 * `useGitGraph`; this component is purely rendering + layout.
 */
export function GitGraph() {
  const { commits, hasMore, isLoading, isLoadingMore, error, loadMore } = useGitGraph()

  // Layout is computed over the accumulated commits; all are rendered.
  const nodes = useMemo(() => computeGraphLayout(commits), [commits])
  const shaToNode = useMemo(() => {
    const map = new Map<string, GraphNode>()
    for (const n of nodes) map.set(n.sha, n)
    return map
  }, [nodes])

  const visibleNodes = nodes
  const maxLane = nodes.reduce((m, n) => Math.max(m, n.lane), 0)
  const svgWidth = xFor(maxLane) + LANE_SPACING
  const svgHeight = visibleNodes.length * ROW_SPACING + ROW_SPACING / 2

  if (isLoading) {
    return (
      <div className="flex flex-1 items-center justify-center min-h-0">
        <Loader2 className="size-4 animate-spin text-muted-foreground" />
      </div>
    )
  }

  if (error && commits.length === 0) {
    return (
      <div className="flex flex-1 items-center justify-center min-h-0 px-4 text-center">
        <div className="flex flex-col items-center gap-2 text-muted-foreground">
          <AlertCircle className="size-6 opacity-50" />
          <span className="text-xs">{error}</span>
        </div>
      </div>
    )
  }

  if (commits.length === 0) {
    return (
      <div className="flex flex-1 items-center justify-center min-h-0">
        <span className="text-sm text-muted-foreground select-none">No commits yet</span>
      </div>
    )
  }

  return (
    <div className="flex flex-col min-h-0 flex-1 overflow-y-auto custom-scrollbar">
      <div className="flex">
        <svg width={svgWidth} height={svgHeight} className="shrink-0" aria-hidden="true">
          {visibleNodes.map((node) =>
            node.parents.map((edge) => {
              const parent = shaToNode.get(edge.sha)
              const targetRow = parent ? parent.row : nodes.length
              return (
                <path
                  key={`${node.sha}-${edge.sha}`}
                  d={edgePath(xFor(node.lane), yFor(node.row), xFor(edge.lane), yFor(targetRow))}
                  fill="none"
                  strokeWidth={1.5}
                  style={{ stroke: `var(${laneVar(edge.lane)})` }}
                />
              )
            }),
          )}
          {visibleNodes.map((node) => (
            <circle
              key={node.sha}
              cx={xFor(node.lane)}
              cy={yFor(node.row)}
              r={node.isMerge ? MERGE_R : NODE_R}
              style={{
                fill: `var(${laneVar(node.lane)})`,
                stroke: node.isMerge ? 'var(--color-background)' : 'none',
                strokeWidth: node.isMerge ? 1.5 : 0,
              }}
            />
          ))}
        </svg>

        <div className="flex-1 min-w-0">
          {visibleNodes.map((node) => (
            <div
              key={node.sha}
              className="flex items-center gap-2 px-2 text-sm leading-none"
              style={{ height: ROW_SPACING }}
            >
              <GitCommit className="size-3.5 shrink-0 text-muted-foreground" />
              <span className="min-w-0 truncate flex-1">{node.message}</span>
              {node.refs.map((ref) => (
                <span
                  key={ref}
                  className={`shrink-0 rounded bg-muted/40 px-1 py-px text-[10px] font-medium ${refColor(ref)}`}
                >
                  {ref.replace(/^tag:\s*/, '')}
                </span>
              ))}
              <span className="shrink-0 font-mono text-[10px] text-info">{shortSha(node.sha)}</span>
            </div>
          ))}
        </div>
      </div>

      {hasMore && (
        <button
          type="button"
          onClick={loadMore}
          disabled={isLoadingMore}
          className="flex items-center justify-center gap-1.5 py-2 text-xs text-muted-foreground hover:bg-muted/50 transition-colors"
        >
          {isLoadingMore && <Loader2 className="size-3.5 animate-spin" />}
          Load more
        </button>
      )}
    </div>
  )
}
