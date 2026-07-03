import { useCallback, useState } from 'react'
import { Plus, Loader2 } from 'lucide-react'
import { Button } from '@/components/ui/button'
import { stageHunks } from '@/api/git'
import { useGitPanelStore } from '@/stores/gitPanelStore'
import { hunkToRange } from './diffHunkRange'
import type { DiffHunk } from '@/lib/diffParser'

interface DiffHunkStageBarProps {
  /** Absolute file path (passed to the StageHunks RPC). */
  filePath: string
  /** Parsed diff hunks for the file. When empty, nothing is rendered. */
  hunks: DiffHunk[]
}

/**
 * A per-hunk "Stage Hunk" control bar rendered above a diff view (Phase 6).
 * Each hunk gets its own button; staging is async with a per-hunk spinner.
 * Only relevant for unstaged hunks — the consumer gates rendering on diff
 * presence and a tracked, uncommitted file.
 */
export function DiffHunkStageBar({ filePath, hunks }: DiffHunkStageBarProps) {
  const [stagingIds, setStagingIds] = useState<Set<string>>(new Set())

  const handleStage = useCallback(
    async (hunk: DiffHunk) => {
      setStagingIds((prev) => new Set(prev).add(hunk.id))
      try {
        await stageHunks(filePath, [hunkToRange(hunk)])
        // Backend emits git:status_changed → useGitStatusEvents refreshes.
      } catch (err) {
        useGitPanelStore.getState().setError(
          err instanceof Error ? err.message : 'Failed to stage hunk',
        )
      } finally {
        setStagingIds((prev) => {
          const next = new Set(prev)
          next.delete(hunk.id)
          return next
        })
      }
    },
    [filePath],
  )

  if (hunks.length === 0) return null

  return (
    <div className="flex flex-col gap-px border-b border-border bg-secondary/20">
      {hunks.map((hunk) => {
        const isStaging = stagingIds.has(hunk.id)
        return (
          <div
            key={hunk.id}
            className="flex items-center justify-between px-3 py-0.5 text-[11px] text-muted-foreground"
          >
            <span className="font-mono truncate">
              @@ -{hunk.oldStart},{hunk.oldCount} +{hunk.newStart},{hunk.newCount} @@
            </span>
            <Button
              variant="ghost"
              size="xs"
              disabled={isStaging}
              onClick={() => void handleStage(hunk)}
              title="Stage this hunk"
              aria-label="Stage this hunk"
              className="text-success hover:text-success"
            >
              {isStaging ? <Loader2 className="animate-spin" /> : <Plus />}
              Stage Hunk
            </Button>
          </div>
        )
      })}
    </div>
  )
}
