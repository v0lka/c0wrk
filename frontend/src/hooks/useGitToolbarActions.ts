import { useState, useCallback } from 'react'
import { stageAll, unstageAll, abortMerge, abortRebase } from '@/api/git'

interface UseGitToolbarActions {
  isStagingAll: boolean
  isUnstagingAll: boolean
  isAborting: boolean
  isBusy: boolean
  error: string | null
  setError: (message: string | null) => void
  handleStageAll: () => Promise<void>
  handleUnstageAll: () => Promise<void>
  handleAbort: (op: 'merge' | 'rebase') => Promise<void>
}

/**
 * Action handlers + busy/error state for GitPanelToolbar, extracted so the
 * toolbar component stays under 200 lines (AGENTS.md — "Small, focused
 * components. Extract hooks for data loading").
 *
 * All mutations go through `@/api/*` wrappers; the backend emits
 * `git:status_changed` after each so `useGitStatusEvents` refreshes the
 * store automatically — no manual refresh is needed here.
 */
export function useGitToolbarActions(): UseGitToolbarActions {
  const [isStagingAll, setIsStagingAll] = useState(false)
  const [isUnstagingAll, setIsUnstagingAll] = useState(false)
  const [isAborting, setIsAborting] = useState(false)
  const [error, setError] = useState<string | null>(null)

  const isBusy = isStagingAll || isUnstagingAll || isAborting

  const handleStageAll = useCallback(async () => {
    setIsStagingAll(true)
    setError(null)
    try {
      await stageAll()
    } catch (err) {
      setError(err instanceof Error ? err.message : 'Failed to stage all files')
    } finally {
      setIsStagingAll(false)
    }
  }, [])

  const handleUnstageAll = useCallback(async () => {
    setIsUnstagingAll(true)
    setError(null)
    try {
      await unstageAll()
    } catch (err) {
      setError(err instanceof Error ? err.message : 'Failed to unstage all files')
    } finally {
      setIsUnstagingAll(false)
    }
  }, [])

  /** Abort an in-progress merge or rebase (Phase 6). */
  const handleAbort = useCallback(async (op: 'merge' | 'rebase') => {
    setIsAborting(true)
    setError(null)
    try {
      if (op === 'merge') {
        await abortMerge()
      } else {
        await abortRebase()
      }
      // Backend emits git:status_changed → useGitStatusEvents refreshes
      // (and re-fetches merge/rebase state).
    } catch (err) {
      setError(err instanceof Error ? err.message : `Failed to abort ${op}`)
    } finally {
      setIsAborting(false)
    }
  }, [])

  return {
    isStagingAll,
    isUnstagingAll,
    isAborting,
    isBusy,
    error,
    setError,
    handleStageAll,
    handleUnstageAll,
    handleAbort,
  }
}
