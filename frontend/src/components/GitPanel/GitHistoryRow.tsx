import { ChevronDown, ChevronRight, GitCommit, Loader2 } from 'lucide-react'
import { cn } from '@/lib/utils'
import { shortSha, type GraphNode } from '@/lib/gitGraphLayout'
import { ROW_SPACING, fileStatusColor, refColor } from './gitGraphRender'
import type { CommitFile } from '@/types/models'

interface GitHistoryRowProps {
  /** Lane-laid-out graph node (sha, message, refs, merge flag). */
  node: GraphNode
  /** Commit author name (from the unified history payload). */
  author: string
  /** Commit date string (from the unified history payload). */
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
}

/**
 * A single commit row in the unified history+graph view. The first line
 * (message + refs + short SHA) sits at the top of the ROW_SPACING-pixel row
 * so the SVG lane node stays aligned with it; author and date render on a
 * second line below. Expanded changed files render below the two-line
 * header, pushing subsequent rows down.
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
}: GitHistoryRowProps) {
  return (
    <div style={{ height }}>
      <button
        type="button"
        onClick={onClick}
        className="flex w-full flex-col justify-start gap-0.5 px-2 pt-1"
        style={{ height: ROW_SPACING }}
      >
        <div className="flex items-center gap-2 text-sm leading-none">
          {expanded ? (
            <ChevronDown className="size-3.5 shrink-0 text-muted-foreground" />
          ) : (
            <ChevronRight className="size-3.5 shrink-0 text-muted-foreground" />
          )}
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
        <div className="flex items-center pl-7 text-[10px] leading-none text-muted-foreground">
          <span className="min-w-0 truncate">
            {author} · {date}
          </span>
        </div>
      </button>
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
                <span className="min-w-0 truncate text-muted-foreground">{f.path}</span>
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
