import { useState, useCallback, useEffect, useRef } from 'react'
import { Archive, ArchiveRestore, Loader2, ChevronDown, AlertCircle } from 'lucide-react'
import { Button } from '@/components/ui/button'
import { stashCreate, stashPop, stashDrop, stashList } from '@/api/git'
import type { StashEntry } from '@/types/models'
import { cn } from '@/lib/utils'
import { runGitOperation } from '@/lib/gitOperation'
import { useProjectStore } from '@/stores/projectStore'
import { GitStashList } from './GitStashList'

/**
 * Stash create / pop-latest icon button group (Phase 5) with a list popover
 * (FE-5 / D3) that enumerates stashes via `stashList()` and exposes
 * per-entry Pop (`stashPop(index)`) and Drop (`stashDrop(index)`) actions.
 * The list body is rendered by `GitStashList`.
 *
 * Every mutating operation (create / pop / drop) is recorded via
 * {@link runGitOperation} so the Git panel's operation console reflects its
 * result. A failure loading the stash list is NOT a git mutation — it is
 * surfaced inline inside the popover instead.
 *
 * All operations emit `git:status_changed` on the backend, which
 * `useGitStatusEvents` picks up — no manual refresh is needed here.
 */
export function GitStashButtons() {
  const [isStashing, setIsStashing] = useState(false)
  const [isPopping, setIsPopping] = useState(false)
  const [isListOpen, setIsListOpen] = useState(false)
  const [isLoadingList, setIsLoadingList] = useState(false)
  const [stashEntries, setStashEntries] = useState<StashEntry[]>([])
  const [listError, setListError] = useState<string | null>(null)
  const [busyIndex, setBusyIndex] = useState<number | null>(null)
  const containerRef = useRef<HTMLDivElement>(null)

  const isStashBusy = isStashing || isPopping || busyIndex !== null

  const loadList = useCallback(async () => {
    setIsLoadingList(true)
    setListError(null)
    try {
      const list = await stashList()
      setStashEntries(list)
    } catch (err) {
      setListError(err instanceof Error ? err.message : 'Failed to load stashes')
      setStashEntries([])
    } finally {
      setIsLoadingList(false)
    }
  }, [])

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
    const projectId = useProjectStore.getState().activeProjectId
    if (!projectId) return
    setIsStashing(true)
    try {
      // Empty message → git uses its default stash message.
      await runGitOperation({
        projectId,
        kind: 'stash-create',
        label: 'Stashed changes',
        fn: () => stashCreate(''),
      })
      if (isListOpen) void loadList()
    } finally {
      setIsStashing(false)
    }
  }, [isListOpen, loadList])

  const handleStashPop = useCallback(async () => {
    const projectId = useProjectStore.getState().activeProjectId
    if (!projectId) return
    setIsPopping(true)
    try {
      // Pop the most recent stash (stash@{0}).
      await runGitOperation({
        projectId,
        kind: 'stash-pop',
        label: 'Popped latest stash',
        fn: () => stashPop(0),
      })
      if (isListOpen) void loadList()
    } finally {
      setIsPopping(false)
    }
  }, [isListOpen, loadList])

  const handleEntryAction = useCallback(
    async (index: number, op: 'pop' | 'drop') => {
      const projectId = useProjectStore.getState().activeProjectId
      if (!projectId) return
      setBusyIndex(index)
      try {
        await runGitOperation({
          projectId,
          kind: op === 'pop' ? 'stash-pop' : 'stash-drop',
          label: `${op === 'pop' ? 'Popped' : 'Dropped'} stash@{${index}}`,
          fn: () => (op === 'pop' ? stashPop(index) : stashDrop(index)),
        })
        await loadList()
      } finally {
        setBusyIndex(null)
      }
    },
    [loadList],
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
          {listError && (
            <div className="px-2 py-1.5 text-[10px] text-destructive">{listError}</div>
          )}
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
