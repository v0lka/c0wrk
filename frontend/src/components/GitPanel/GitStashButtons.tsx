import { useState, useCallback } from 'react'
import { Archive, ArchiveRestore, Loader2 } from 'lucide-react'
import { Button } from '@/components/ui/button'
import { stashCreate, stashPop } from '@/api/git'

interface GitStashButtonsProps {
  /** Report an error message to the parent for shared display. */
  onError: (message: string) => void
}

/**
 * Stash create / pop-latest icon button group (Phase 5).
 *
 * Both operations emit `git:status_changed` on the backend, which
 * `useGitStatusEvents` picks up — no manual refresh is needed here.
 */
export function GitStashButtons({ onError }: GitStashButtonsProps) {
  const [isStashing, setIsStashing] = useState(false)
  const [isPopping, setIsPopping] = useState(false)
  const isStashBusy = isStashing || isPopping

  const handleStashCreate = useCallback(async () => {
    setIsStashing(true)
    try {
      // Empty message → git uses its default stash message.
      await stashCreate('')
    } catch (err) {
      onError(err instanceof Error ? err.message : 'Failed to stash changes')
    } finally {
      setIsStashing(false)
    }
  }, [onError])

  const handleStashPop = useCallback(async () => {
    setIsPopping(true)
    try {
      // Pop the most recent stash (stash@{0}).
      await stashPop(0)
    } catch (err) {
      onError(err instanceof Error ? err.message : 'Failed to pop stash')
    } finally {
      setIsPopping(false)
    }
  }, [onError])

  return (
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
    </div>
  )
}
