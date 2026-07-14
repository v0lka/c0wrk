import { ChevronDown, ChevronRight, Loader2 } from 'lucide-react'
import { Tooltip, TooltipTrigger, TooltipContent } from '@/components/ui/tooltip'
import { formatRelativeTime } from '@/lib/formatters'
import { shortSha, type GraphNode } from '@/lib/gitGraphLayout'
import { cn } from '@/lib/utils'
import { ROW_SPACING, fileStatusColor, refColor } from './gitGraphRender'
import type { CommitFile } from '@/types/models'

interface GitHistoryRowProps {
  /** Lane-laid-out graph node (sha, message, refs, merge flag). */
  node: GraphNode
  /** Commit author name (from the unified history payload). */
  author: string
  /** Commit date string (from the unified history payload); shown in full inside the hover tooltip. */
  date: string
  /** Total pixel height of this row (ROW_SPACING + expansion when open). */
  height: number
  /** Whether this row is currently expanded to show changed files. */
  expanded: boolean
  /** Lazily-fetched changed files, or undefined while not yet fetched. */
  files: CommitFile[] | undefined
  /** True while changed files are being fetched for this commit. */
  loadingFiles: boolean
  /** Toggle expansion (the parent lazy-loads files on first expand). */
  onClick: () => void
  /** Open the read-only commit-diff review page for this commit's SHA. */
  onShaClick?: (sha: string) => void
}

/**
 * A single commit row in the unified history+graph view. The first line
 * (message + refs + short SHA) sits at the top of the ROW_SPACING-pixel row
 * so the SVG lane node stays aligned with it; the author and a relative time
 * (e.g. "3h") render on a second line below. Hovering the commit area
 * reveals a tooltip carrying the full commit date and the commit subject.
 * Expanded changed files render below the two-line header, pushing
 * subsequent rows down.
 */
export function GitHistoryRow({
  node,
  author,
  date,
  height,
  expanded,
  files,
  loadingFiles,
  onClick,
  onShaClick,
}: GitHistoryRowProps) {
  const handleShaClick = (e: React.MouseEvent) => {
    // Prevent the row-level expand/collapse toggle from firing.
    e.stopPropagation()
    onShaClick?.(node.sha)
  }

  const handleShaKeyDown = (e: React.KeyboardEvent) => {
    if (e.key === 'Enter' || e.key === ' ') {
      e.preventDefault()
      e.stopPropagation()
      onShaClick?.(node.sha)
    }
  }

  return (
    <div style={{ height }}>
      <Tooltip delayDuration={400}>
        <TooltipTrigger asChild>
          <button
            type="button"
            onClick={onClick}
            className="flex w-full flex-col justify-start gap-0.5 px-2 pt-1 text-left"
            style={{ height: ROW_SPACING }}
          >
            <div className="flex items-center gap-2 text-sm leading-none">
              {expanded ? (
                <ChevronDown className="size-3.5 shrink-0 text-muted-foreground" />
              ) : (
                <ChevronRight className="size-3.5 shrink-0 text-muted-foreground" />
              )}
              <span className="min-w-0 truncate flex-1 select-text">{node.message}</span>
              {node.refs.map((ref) => (
                <span
                  key={ref}
                  className={`shrink-0 rounded bg-muted/40 px-1 py-px text-[10px] font-medium ${refColor(ref)}`}
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
                  className="shrink-0 font-mono text-[10px] text-info cursor-pointer rounded px-0.5 hover:bg-info/15 hover:text-info hover:underline focus-visible:outline-none focus-visible:ring-1 focus-visible:ring-info/40"
                >
                  {shortSha(node.sha)}
                </span>
              ) : (
                <span className="shrink-0 font-mono text-[10px] text-info select-text">{shortSha(node.sha)}</span>
              )}
            </div>
            <div className="flex items-center pl-7 text-[10px] leading-none text-muted-foreground">
              <span className="min-w-0 truncate select-text">
                {author} · {formatRelativeTime(date)}
              </span>
            </div>
          </button>
        </TooltipTrigger>
        <TooltipContent className="max-w-xs">
          <div className="flex flex-col gap-1">
            <span className="opacity-70">{date}</span>
            <span className="text-balance">{node.message}</span>
          </div>
        </TooltipContent>
      </Tooltip>
      {expanded && (
        <div className="px-2 pt-1 pb-1.5 pl-7">
          {loadingFiles ? (
            <div className="flex items-center gap-1.5 py-1 text-[11px] leading-none text-muted-foreground">
              <Loader2 className="size-3 animate-spin" /> Loading files…
            </div>
          ) : files && files.length > 0 ? (
            files.map((f) => (
              <div
                key={f.path}
                className="flex items-center gap-1.5 py-0.5 text-[11px] leading-none"
              >
                <span className={cn('shrink-0 font-mono font-semibold', fileStatusColor(f.status))}>
                  {f.status}
                </span>
                <span className="min-w-0 truncate text-muted-foreground select-text">{f.path}</span>
              </div>
            ))
          ) : (
            <div className="py-1 text-[11px] leading-none text-muted-foreground">No files</div>
          )}
        </div>
      )}
    </div>
  )
}
