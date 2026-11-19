import { Tooltip, TooltipTrigger, TooltipContent } from '@/components/ui/tooltip'
import { formatRelativeTime } from '@/lib/formatters'
import { shortSha, type GraphNode } from '@/lib/gitGraphLayout'
import { statColorClass } from '@/lib/commitStats'
import { ROW_SPACING, refColor } from './gitGraphRender'
import type { CommitStats } from '@/types/models'

interface GitHistoryRowProps {
  /** Lane-laid-out graph node (sha, message, refs, merge flag). */
  node: GraphNode
  /** Commit author name (from the unified history payload). */
  author: string
  /** Commit date string (from the unified history payload); shown in full inside the hover tooltip. */
  date: string
  /**
   * Per-commit changed-file counts from GetGitHistory (`commits.stats`):
   * added / modified / deleted, rendered as a compact colored cell at the
   * end of the row. Optional so a stale-bindings backend that omits the
   * field simply renders rows without the cell.
   */
  stats?: CommitStats
  /** Open the read-only commit-diff review page for this commit's SHA. */
  onShaClick?: (sha: string) => void
}

/**
 * A single commit row in the unified history+graph view. A fixed
 * ROW_SPACING-pixel two-line row (message + refs + short SHA + stats on the
 * first line, author + relative time on the second) so the SVG lane node
 * stays aligned with the message line — rows never expand inline, so the
 * lane layout is constant. Hovering reveals a tooltip carrying the full
 * commit date and the commit subject. Clicking the short SHA opens the
 * commit's read-only diff review in the file viewer; the row body itself
 * is inert.
 */
export function GitHistoryRow({
  node,
  author,
  date,
  stats,
  onShaClick,
}: GitHistoryRowProps) {
  const handleShaClick = () => {
    onShaClick?.(node.sha)
  }

  const handleShaKeyDown = (e: React.KeyboardEvent) => {
    if (e.key === 'Enter' || e.key === ' ') {
      e.preventDefault()
      onShaClick?.(node.sha)
    }
  }

  return (
    <div style={{ height: ROW_SPACING }} data-commit-row>
      <Tooltip>
        <TooltipTrigger asChild>
          <div className="flex h-full w-full flex-col justify-start gap-0.5 px-2 pt-1 text-left">
            <div className="flex items-center gap-2 text-sm leading-none">
              <span className="min-w-0 truncate flex-1 select-text">{node.message}</span>
              {node.refs.map((ref) => (
                <span
                  key={ref}
                  className={`shrink-0 rounded bg-muted/40 px-1 py-px text-xs font-medium leading-none ${refColor(ref)}`}
                >
                  {ref.replace(/^tag:\s*/, '')}
                </span>
              ))}
              {onShaClick ? (
                <span
                  role="button"
                  tabIndex={0}
                  onClick={handleShaClick}
                  onKeyDown={handleShaKeyDown}
                  title="View commit changes"
                  className="shrink-0 font-mono text-xs text-info cursor-pointer rounded px-0.5 hover:bg-info/15 hover:text-info hover:underline focus-visible:outline-none focus-visible:ring-1 focus-visible:ring-info/40"
                >
                  {shortSha(node.sha)}
                </span>
              ) : (
                <span className="shrink-0 font-mono text-xs text-info select-text">{shortSha(node.sha)}</span>
              )}
              {stats && (
                <span
                  className="flex shrink-0 items-center gap-1 font-mono text-xs leading-none select-none"
                  title={`${stats.added} added, ${stats.modified} modified, ${stats.deleted} deleted`}
                >
                  <span className={statColorClass('added')}>+{stats.added}</span>
                  <span className={statColorClass('modified')}>~{stats.modified}</span>
                  <span className={statColorClass('deleted')}>-{stats.deleted}</span>
                </span>
              )}
            </div>
            <div className="flex items-center text-xs leading-none text-muted-foreground">
              <span className="min-w-0 truncate select-text">
                {author} · {formatRelativeTime(date)}
              </span>
            </div>
          </div>
        </TooltipTrigger>
        <TooltipContent className="max-w-xs">
          <div className="flex flex-col gap-1">
            <span className="opacity-70">{date}</span>
            <span className="text-balance">{node.message}</span>
          </div>
        </TooltipContent>
      </Tooltip>
    </div>
  )
}
