import { useCallback, useState, useMemo } from 'react'
import { Plus, Minus, Trash2, Loader2 } from 'lucide-react'
import { Button } from '@/components/ui/button'
import {
  Tooltip,
  TooltipTrigger,
  TooltipContent,
  TooltipProvider,
} from '@/components/ui/tooltip'
import { stageHunks, unstageHunks, discardHunks } from '@/api/git'
import { useGitPanelStore } from '@/stores/gitPanelStore'
import { useFileViewerStore } from '@/stores/fileViewerStore'
import { hunkToRange } from './diffHunkRange'
import { highlightHunkDiff } from './hunkDiffHighlight'
import type { HunkDiffInfo } from '@/types/models'

interface DiffHunkStageBarProps {
  /** Absolute file path (passed to the staging RPCs). */
  filePath: string
  /** Structured per-hunk diff info with staging status. */
  hunks: HunkDiffInfo[]
}

/**
 * A per-hunk control bar rendered above a diff view. Each hunk gets its own
 * row with a dimmed "@@" header (clickable to jump to the first changed line,
 * hoverable for a syntax-highlighted diff tooltip) and action buttons:
 *  - Unstaged hunks: green "+ Stage Hunk" + red "Discard Hunk"
 *  - Staged hunks:   yellow "- Unstage Hunk"
 * All actions are async with a per-hunk spinner.
 */
export function DiffHunkStageBar({ filePath, hunks }: DiffHunkStageBarProps) {
  const [pendingIds, setPendingIds] = useState<Set<string>>(new Set())

  const markPending = useCallback((id: string) => {
    setPendingIds((prev) => new Set(prev).add(id))
  }, [])
  const clearPending = useCallback((id: string) => {
    setPendingIds((prev) => {
      const next = new Set(prev)
      next.delete(id)
      return next
    })
  }, [])

  const handleStage = useCallback(
    async (hunk: HunkDiffInfo) => {
      const id = hunkKey(hunk)
      markPending(id)
      try {
        await stageHunks(filePath, [hunkToRange(hunk)])
      } catch (err) {
        useGitPanelStore.getState().setError(
          err instanceof Error ? err.message : 'Failed to stage hunk',
        )
      } finally {
        clearPending(id)
      }
    },
    [filePath, markPending, clearPending],
  )

  const handleUnstage = useCallback(
    async (hunk: HunkDiffInfo) => {
      const id = hunkKey(hunk)
      markPending(id)
      try {
        await unstageHunks(filePath, [hunkToRange(hunk)])
      } catch (err) {
        useGitPanelStore.getState().setError(
          err instanceof Error ? err.message : 'Failed to unstage hunk',
        )
      } finally {
        clearPending(id)
      }
    },
    [filePath, markPending, clearPending],
  )

  const handleDiscard = useCallback(
    async (hunk: HunkDiffInfo) => {
      const id = hunkKey(hunk)
      markPending(id)
      try {
        await discardHunks(filePath, [hunkToRange(hunk)])
      } catch (err) {
        useGitPanelStore.getState().setError(
          err instanceof Error ? err.message : 'Failed to discard hunk',
        )
      } finally {
        clearPending(id)
      }
    },
    [filePath, markPending, clearPending],
  )

  const handleJumpToHunk = useCallback((hunk: HunkDiffInfo) => {
    // Position the file viewer on the first changed line of the block.
    // newChangeStart excludes context lines, so it points to the actual
    // first '+' or '-' line, not the preceding context.
    useFileViewerStore.getState().setHighlightLine(hunk.new_change_start)
  }, [])

  if (hunks.length === 0) return null

  return (
    <TooltipProvider delayDuration={300}>
      <div className="flex flex-col gap-px border-b border-border bg-secondary/20 max-h-[150px] overflow-y-auto custom-scrollbar">
        {hunks.map((hunk) => {
          const id = hunkKey(hunk)
          const isPending = pendingIds.has(id)
          return (
            <HunkRow
              key={id}
              hunk={hunk}
              isPending={isPending}
              onStage={() => void handleStage(hunk)}
              onUnstage={() => void handleUnstage(hunk)}
              onDiscard={() => void handleDiscard(hunk)}
              onJump={() => handleJumpToHunk(hunk)}
            />
          )
        })}
      </div>
    </TooltipProvider>
  )
}

/** Stable key for a hunk (staged + unstaged hunks can share old/new starts). */
function hunkKey(hunk: HunkDiffInfo): string {
  return `${hunk.staged ? 's' : 'u'}-${hunk.old_start}-${hunk.new_start}`
}

interface HunkRowProps {
  hunk: HunkDiffInfo
  isPending: boolean
  onStage: () => void
  onUnstage: () => void
  onDiscard: () => void
  onJump: () => void
}

function HunkRow({ hunk, isPending, onStage, onUnstage, onDiscard, onJump }: HunkRowProps) {
  // Pre-compute the highlighted diff HTML once per hunk.
  const highlightedDiff = useMemo(
    () => highlightHunkDiff(hunk.diff),
    [hunk.diff],
  )

  return (
    <div className="flex items-center justify-between px-3 py-0.5 text-[11px] text-muted-foreground">
      <Tooltip>
        <TooltipTrigger asChild>
          <button
            type="button"
            onClick={onJump}
            className="font-mono truncate cursor-pointer hover:text-foreground transition-colors text-left"
            title="Click to jump to the first changed line"
          >
            <span className="text-muted-foreground/50">@@</span>
            {' '}
            -{hunk.old_change_start},{hunk.old_count}
            {' '}
            +{hunk.new_change_start},{hunk.new_count}
            {' '}
            <span className="text-muted-foreground/50">@@</span>
          </button>
        </TooltipTrigger>
        <TooltipContent side="bottom" className="max-w-[600px] p-0 overflow-hidden">
          <pre
            className="text-[10px] font-mono leading-tight max-h-[300px] overflow-auto custom-scrollbar p-2 hljs"
            dangerouslySetInnerHTML={{ __html: highlightedDiff }}
          />
        </TooltipContent>
      </Tooltip>

      <div className="flex items-center gap-1 shrink-0">
        {isPending && <Loader2 className="h-3 w-3 animate-spin text-muted-foreground" />}
        {hunk.staged ? (
          // Staged hunk → offer unstage (yellow)
          <Button
            variant="ghost"
            size="xs"
            disabled={isPending}
            onClick={onUnstage}
            title="Unstage this hunk"
            aria-label="Unstage this hunk"
            className="text-warning hover:text-warning"
          >
            {!isPending && <Minus />}
            Unstage Hunk
          </Button>
        ) : (
          // Unstaged hunk → offer stage (green) + discard (red)
          <>
            <Button
              variant="ghost"
              size="xs"
              disabled={isPending}
              onClick={onStage}
              title="Stage this hunk"
              aria-label="Stage this hunk"
              className="text-success hover:text-success"
            >
              {!isPending && <Plus />}
              Stage Hunk
            </Button>
            <Button
              variant="ghost"
              size="xs"
              disabled={isPending}
              onClick={onDiscard}
              title="Discard this hunk"
              aria-label="Discard this hunk"
              className="text-destructive hover:text-destructive"
            >
              {!isPending && <Trash2 />}
              Discard Hunk
            </Button>
          </>
        )}
      </div>
    </div>
  )
}
