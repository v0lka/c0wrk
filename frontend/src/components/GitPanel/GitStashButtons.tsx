import { useState, useCallback, useEffect, useRef } from 'react'
import { Archive, ArchiveRestore, Loader2, ChevronDown, AlertCircle } from 'lucide-react'
import { Button } from '@/components/ui/button'
import { stashCreate, stashPop, stashDrop, stashList } from '@/api/git'
import type { StashEntry } from '@/types/models'
import { cn } from '@/lib/utils'
import { GitStashList } from './GitStashList'

interface GitStashButtonsProps {
  /** Report an error message to the parent for shared display. */
  onError: (message: string) => void
}

/**
 * Stash create / pop-latest icon button group (Phase 5) with a list popover
 * (FE-5 / D3) that enumerates stashes via `stashList()` and exposes
 * per-entry Pop (`stashPop(index)`) and Drop (`stashDrop(index)`) actions.
 * The list body is rendered by `GitStashList`.
 *
 * All operations emit `git:status_changed` on the backend, which
 * `useGitStatusEvents` picks up — no manual refresh is needed here.
 */
export function GitStashButtons({ onError }: GitStashButtonsProps) {
  const [isStashing, setIsStashing] = useState(false)
  const [isPopping, setIsPopping] = useState(false)
  const [isListOpen, setIsListOpen] = useState(false)
  const [isLoadingList, setIsLoadingList] = useState(false)
  const [stashEntries, setStashEntries] = useState<StashEntry[]>([])
  const [busyIndex, setBusyIndex] = useState<number | null>(null)
  const containerRef = useRef<HTMLDivElement>(null)

  const isStashBusy = isStashing || isPopping || busyIndex !== null

  const loadList = useCallback(async () => {
    setIsLoadingList(true)
    try {
      const list = await stashList()
      setStashEntries(list)
    } catch (err) {
      onError(err instanceof Error ? err.message : 'Failed to load stashes')
      setStashEntries([])
    } finally {
      setIsLoadingList(false)
    }
  }, [onError])

  // Open the popover: (re)fetch the stash list each time it is shown.
  const toggleList = useCallback(() => {
    setIsListOpen((open) => {
      const next = !open
      if (next) void loadList()
      return next
    })
  }, [loadList])

  // Close the popover on outside click.
  useEffect(() => {
    if (!isListOpen) return
    const handler = (e: MouseEvent) => {
      if (containerRef.current && !containerRef.current.contains(e.target as Node)) {
        setIsListOpen(false)
      }
    }
    document.addEventListener('mousedown', handler)
    return () => document.removeEventListener('mousedown', handler)
  }, [isListOpen])

  const handleStashCreate = useCallback(async () => {
    setIsStashing(true)
    try {
      // Empty message → git uses its default stash message.
      await stashCreate('')
      if (isListOpen) void loadList()
    } catch (err) {
      onError(err instanceof Error ? err.message : 'Failed to stash changes')
    } finally {
      setIsStashing(false)
    }
  }, [onError, isListOpen, loadList])

  const handleStashPop = useCallback(async () => {
    setIsPopping(true)
    try {
      // Pop the most recent stash (stash@{0}).
      await stashPop(0)
      if (isListOpen) void loadList()
    } catch (err) {
      onError(err instanceof Error ? err.message : 'Failed to pop stash')
    } finally {
      setIsPopping(false)
    }
  }, [onError, isListOpen, loadList])

  const handleEntryAction = useCallback(
    async (index: number, op: 'pop' | 'drop') => {
      setBusyIndex(index)
      try {
        if (op === 'pop') {
          await stashPop(index)
        } else {
          await stashDrop(index)
        }
        await loadList()
      } catch (err) {
        onError(
          err instanceof Error ? err.message : `Failed to ${op} stash@{${index}}`,
        )
      } finally {
        setBusyIndex(null)
      }
    },
    [loadList, onError],
  )

  return (
    <div ref={containerRef} className="relative flex items-center">
      <div className="flex items-center rounded-md border border-border/50 overflow-hidden">
        <Button
          variant="ghost"
          size="icon-xs"
          disabled={isStashBusy}
          onClick={handleStashCreate}
          title="Stash changes"
          aria-label="Stash changes"
        >
          {isStashing ? (
            <Loader2 className="size-3.5 animate-spin" />
          ) : (
            <Archive className="size-3.5" />
          )}
        </Button>
        <div className="w-px h-4 bg-border/50" />
        <Button
          variant="ghost"
          size="icon-xs"
          disabled={isStashBusy}
          onClick={handleStashPop}
          title="Pop latest stash"
          aria-label="Pop latest stash"
        >
          {isPopping ? (
            <Loader2 className="size-3.5 animate-spin" />
          ) : (
            <ArchiveRestore className="size-3.5" />
          )}
        </Button>
        <div className="w-px h-4 bg-border/50" />
        <Button
          variant="ghost"
          size="icon-xs"
          disabled={isStashBusy}
          onClick={toggleList}
          title="Stash list"
          aria-label="Stash list"
          aria-expanded={isListOpen}
        >
          <ChevronDown className={cn('size-3.5 transition-transform', isListOpen && 'rotate-180')} />
        </Button>
      </div>

      {isListOpen && (
        <div className="absolute left-0 top-full z-50 mt-1 w-72 rounded-md border border-border bg-popover p-1 shadow-md">
          <GitStashList
            entries={stashEntries}
            isLoading={isLoadingList}
            busyIndex={busyIndex}
            onAction={handleEntryAction}
          />
          <div className="flex items-center gap-1.5 px-2 py-1.5 text-[10px] text-muted-foreground">
            <AlertCircle className="size-3 shrink-0" />
            Pop applies &amp; removes; Drop discards.
          </div>
        </div>
      )}
    </div>
  )
}
