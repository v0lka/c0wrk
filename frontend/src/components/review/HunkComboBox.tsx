import { useMemo } from 'react'
import { Check, ChevronDown } from 'lucide-react'
import {
  DropdownMenu,
  DropdownMenuTrigger,
  DropdownMenuContent,
  DropdownMenuItem,
} from '@/components/ui/dropdown-menu'
import { Tooltip, TooltipTrigger, TooltipContent } from '@/components/ui/tooltip'
import { cn } from '@/lib/utils'
import type { ReviewHunk } from '@/api/review'
import { summarizeHunk, hunkLineRangeLabel } from './diffParsing'

/**
 * One selectable row in the review-hunk combobox: a file path paired with a
 * single hunk. The `globalIndex` is the hunk's flat position across all files
 * in document order — the same index the review pane's prev/next navigation
 * and `[data-review-hunk]` scroll targets use.
 */
export interface HunkEntry {
  globalIndex: number
  filePath: string
  hunk: ReviewHunk
}

interface HunkComboBoxProps {
  /** All hunks across all files, in navigation (document) order. */
  entries: HunkEntry[]
  /** Currently-focused hunk (flat index) shown in the closed trigger. */
  currentIndex: number
  /** Jump the review pane to the hunk at the given flat index. */
  onSelect: (index: number) => void
  className?: string
}

/**
 * A combobox in the review header for jumping directly to any hunk.
 *
 * The trigger shows the *current* hunk; the dropdown lists every hunk across
 * every file. Each row carries three pieces of info:
 *  - the changed file's relative path, **left-truncated** with an ellipsis
 *    (so the filename — the most distinctive part — stays visible) and a
 *    tooltip revealing the full path;
 *  - the hunk's changed-line range in the new file (`LX` / `LX-Y`);
 *  - colored added / removed line counts, pinned to the right edge.
 */
export function HunkComboBox({
  entries,
  currentIndex,
  onSelect,
  className,
}: HunkComboBoxProps) {
  // Pre-compute each hunk's change summary once per entries change. Parsing
  // is cheap, but the list can be long and the dropdown re-renders on open.
  const summaries = useMemo(
    () =>
      entries.map((e) =>
        summarizeHunk(e.hunk.raw, e.hunk.old_start, e.hunk.new_start),
      ),
    [entries],
  )

  if (entries.length === 0) return null

  const current = entries[currentIndex]
  // `current` may briefly be out of range during a silent re-fetch that shrank
  // the diff; fall back to the first entry so the trigger never renders blank.
  const safeIndex = current ? currentIndex : 0
  const safeCurrent = current ?? entries[0]
  const safeSummary = summaries[safeIndex] ?? summaries[0]
  if (!safeCurrent || !safeSummary) return null

  return (
    <DropdownMenu>
      <DropdownMenuTrigger asChild>
        <button
          type="button"
          aria-label="Jump to hunk"
          className={cn(
            'group inline-flex h-6 w-60 items-center gap-1 rounded-md border border-border/60 bg-background/40 pl-2 pr-1 text-xs',
            'hover:bg-muted/50 focus-visible:outline-none data-[state=open]:bg-muted/50',
            className,
          )}
        >
          <HunkRow
            entry={safeCurrent}
            summary={safeSummary}
            className="flex-1 min-w-0"
          />
          <ChevronDown className="size-3 shrink-0 text-muted-foreground transition-transform group-data-[state=open]:rotate-180" />
        </button>
      </DropdownMenuTrigger>
      <DropdownMenuContent
        align="start"
        // Match the dropdown's width to the trigger so left-truncated paths
        // behave identically in the list and the closed combobox.
        style={{ minWidth: 'var(--radix-dropdown-menu-trigger-width)' }}
        className="max-h-[min(60vh,24rem)]"
      >
        {entries.map((entry, i) => {
          const isCurrent = i === safeIndex
          const summary = summaries[i]
          if (!summary) return null
          return (
            <DropdownMenuItem
              key={`${entry.filePath}-${entry.hunk.new_start}-${entry.hunk.old_start}`}
              onSelect={() => onSelect(entry.globalIndex)}
              className={cn('gap-2 px-2', isCurrent && 'bg-muted/50')}
            >
              <HunkRow entry={entry} summary={summary} className="w-full min-w-0" />
              {isCurrent && (
                <Check className="size-3 shrink-0 text-muted-foreground" />
              )}
            </DropdownMenuItem>
          )
        })}
      </DropdownMenuContent>
    </DropdownMenu>
  )
}

interface HunkRowProps {
  entry: HunkEntry
  summary: ReturnType<typeof summarizeHunk>
  className?: string
}

/**
 * A single hunk descriptor row, shared by the combobox trigger and each
 * dropdown item: left-truncated path (full path in tooltip) · `LX-Y` range ·
 * right-aligned colored +/- counts.
 */
function HunkRow({ entry, summary, className }: HunkRowProps) {
  const range = hunkLineRangeLabel(summary, entry.hunk.new_start)
  return (
    <div className={cn('flex w-full items-center gap-2', className)}>
      {/* Left cluster: the left-truncated path immediately followed by the
          hunk's line range. Both stay flush to the left edge while the colored
          counts below are pushed right. Wrapping path + range in a `min-w-0`
          flex group lets the path ellipsize (keeping its end — the filename —
          and the range visible) when space runs out, without the range ever
          drifting toward the right edge. */}
      <div className="flex min-w-0 items-center gap-1.5">
        {/* Left-truncated path: keep the filename (end) visible, ellipsize the
            leading directories. `direction: rtl` makes the overflow land on
            the left; `<bdi>` isolates the LTR path so neutral chars (/ . -)
            don't reorder under the rtl flow. */}
        <Tooltip delayDuration={400}>
          <TooltipTrigger asChild>
            <span
              className="min-w-0 overflow-hidden whitespace-nowrap text-ellipsis text-foreground/90"
              style={{ direction: 'rtl' }}
            >
              <bdi>{entry.filePath}</bdi>
            </span>
          </TooltipTrigger>
          <TooltipContent
            align="start"
            sideOffset={6}
            collisionPadding={16}
            className="max-w-md whitespace-normal break-words text-left"
          >
            {entry.filePath}
          </TooltipContent>
        </Tooltip>

        <code className="shrink-0 tabular-nums text-muted-foreground">
          {range}
        </code>
      </div>

      {/* Colored add/remove counts, pinned to the right edge. */}
      <span className="ml-auto flex shrink-0 items-center gap-1 tabular-nums">
        {summary.added > 0 && (
          <span className="text-success">+{summary.added}</span>
        )}
        {summary.removed > 0 && (
          <span className="text-destructive">-{summary.removed}</span>
        )}
      </span>
    </div>
  )
}
