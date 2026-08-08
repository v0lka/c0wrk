import { useState, useMemo, useCallback } from 'react'
import { ChevronUp, ChevronDown, Check } from 'lucide-react'
import { Button } from '@/components/ui/button'
import {
  DropdownMenu,
  DropdownMenuTrigger,
  DropdownMenuContent,
  DropdownMenuItem,
} from '@/components/ui/dropdown-menu'
import { cn } from '@/lib/utils'
import { useFileViewerStore } from '@/stores/fileViewerStore'
import { clampHunkIndex, buildFileHunkEntries, type FileHunkEntry } from './diffHunkNav'
import type { HunkDiffInfo } from '@/types/models'

interface DiffHunkNavBarProps {
  /** Structured per-hunk diff info for the active file. */
  hunks: HunkDiffInfo[]
}

/**
 * A compact hunk navigation bar rendered above the diff view — the file-viewer
 * analogue of the review header's chunk navigation.
 *
 * Unlike the old DiffHunkStageBar (a vertical list of per-hunk Stage/Discard
 * rows), this bar only *navigates*: prev/next arrow buttons with a
 * `current/total` indicator, plus a combobox to jump to any hunk. Selecting a
 * hunk centers the editor on its first changed line via `setHighlightLine`.
 *
 * Per-hunk Stage/Discard was removed; this bar is navigation-only. Per-file
 * staging remains available in the Git panel.
 */
export function DiffHunkNavBar({ hunks }: DiffHunkNavBarProps) {
  const setHighlightLine = useFileViewerStore((s) => s.setHighlightLine)
  const [current, setCurrent] = useState(0)

  const total = hunks.length
  const entries = useMemo(() => buildFileHunkEntries(hunks), [hunks])
  // Clamp the cursor against the live hunk count: a silent diff refresh can
  // shrink the hunk set, and without this the indicator would point past the
  // last hunk until the user navigates again.
  const safeIndex = clampHunkIndex(current, total)

  const goTo = useCallback(
    (index: number) => {
      const clamped = clampHunkIndex(index, total)
      setCurrent(clamped)
      const hunk = hunks[clamped]
      // new_change_start excludes context lines, so it points to the actual
      // first '+' or '-' line — the same anchor the old bar used.
      if (hunk) setHighlightLine(hunk.new_change_start)
    },
    [hunks, total, setHighlightLine],
  )

  if (total === 0) return null

  const entry = entries[safeIndex]

  return (
    <div className="flex shrink-0 items-center gap-1 border-b border-border bg-secondary/20 px-2 py-1">
      <div className="flex items-center gap-0.5">
        <span className="mr-0.5 text-xs tabular-nums text-muted-foreground">
          {safeIndex + 1}/{total}
        </span>
        <Button
          variant="ghost"
          size="icon-xs"
          onClick={() => goTo(safeIndex - 1)}
          disabled={safeIndex === 0}
          title="Previous hunk"
          aria-label="Previous hunk"
        >
          <ChevronUp className="h-3 w-3" />
        </Button>
        <Button
          variant="ghost"
          size="icon-xs"
          onClick={() => goTo(safeIndex + 1)}
          disabled={safeIndex >= total - 1}
          title="Next hunk"
          aria-label="Next hunk"
        >
          <ChevronDown className="h-3 w-3" />
        </Button>
      </div>

      {entry && (
        <HunkComboBox
          entries={entries}
          currentIndex={safeIndex}
          onSelect={goTo}
        />
      )}
    </div>
  )
}

interface HunkComboBoxProps {
  entries: FileHunkEntry[]
  currentIndex: number
  onSelect: (index: number) => void
}

/**
 * Dropdown combobox for jumping directly to any hunk. The closed trigger shows
 * the current hunk's range + add/remove counts (+ a "staged" tag when staged);
 * the dropdown lists every hunk the same way, marking the active one.
 */
function HunkComboBox({ entries, currentIndex, onSelect }: HunkComboBoxProps) {
  const current = entries[currentIndex] ?? entries[0]
  if (!current) return null

  return (
    <DropdownMenu>
      <DropdownMenuTrigger asChild>
        <button
          type="button"
          aria-label="Jump to hunk"
          className={cn(
            'group inline-flex h-6 items-center gap-1 rounded-md border border-border/60 bg-background/40 pl-2 pr-1 text-xs',
            'hover:bg-muted/50 focus-visible:outline-none data-[state=open]:bg-muted/50',
          )}
        >
          <HunkComboRow entry={current} className="min-w-0" />
          <ChevronDown className="size-3 shrink-0 text-muted-foreground transition-transform group-data-[state=open]:rotate-180" />
        </button>
      </DropdownMenuTrigger>
      <DropdownMenuContent
        align="start"
        style={{ minWidth: 'var(--radix-dropdown-menu-trigger-width)' }}
        className="max-h-[min(60vh,24rem)]"
      >
        {entries.map((entry) => {
          const isCurrent = entry.index === currentIndex
          return (
            <DropdownMenuItem
              key={entry.index}
              onSelect={() => onSelect(entry.index)}
              className={cn('gap-2 px-2', isCurrent && 'bg-muted/50')}
            >
              <HunkComboRow entry={entry} className="w-full min-w-0" />
              {isCurrent && <Check className="size-3 shrink-0 text-muted-foreground" />}
            </DropdownMenuItem>
          )
        })}
      </DropdownMenuContent>
    </DropdownMenu>
  )
}

interface HunkComboRowProps {
  entry: FileHunkEntry
  className?: string
}

/**
 * A single hunk descriptor shared by the combobox trigger and each dropdown
 * item: changed-line range (new-file coordinates) · colored add/remove counts ·
 * an optional "staged" tag.
 */
function HunkComboRow({ entry, className }: HunkComboRowProps) {
  return (
    <div className={cn('flex items-center gap-2', className)}>
      <code className="shrink-0 tabular-nums text-muted-foreground">
        {entry.rangeLabel}
      </code>
      <span className="flex shrink-0 items-center gap-1 tabular-nums">
        {entry.summary.added > 0 && (
          <span className="text-success">+{entry.summary.added}</span>
        )}
        {entry.summary.removed > 0 && (
          <span className="text-destructive">-{entry.summary.removed}</span>
        )}
      </span>
      {entry.staged && (
        <span className="ml-auto shrink-0 text-[10px] uppercase tracking-wide text-warning">
          staged
        </span>
      )}
    </div>
  )
}
