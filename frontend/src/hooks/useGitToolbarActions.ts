import { useState, useCallback } from 'react'
import { stageAll, unstageAll, abortMerge, abortRebase } from '@/api/git'
import { runGitOperation } from '@/lib/gitOperation'
import { useProjectStore } from '@/stores/projectStore'

interface UseGitToolbarActions {
  isStagingAll: boolean
  isUnstagingAll: boolean
  isAborting: boolean
  isBusy: boolean
  handleStageAll: () => Promise<void>
  handleUnstageAll: () => Promise<void>
  handleAbort: (op: 'merge' | 'rebase') => Promise<void>
}

/**
 * Action handlers + busy state for GitPanelToolbar, extracted so the toolbar
 * component stays under 200 lines (AGENTS.md — "Small, focused components.
 * Extract hooks for data loading").
 *
 * All mutations go through `@/api/*` wrappers and are recorded via
 * {@link runGitOperation} so the Git panel's operation console reflects the
 * most recent result — there is no local error state to surface. The backend
 * emits `git:status_changed` after each so `useGitStatusEvents` refreshes the
 * store automatically — no manual refresh is needed here.
 */
export function useGitToolbarActions(): UseGitToolbarActions {
  const [isStagingAll, setIsStagingAll] = useState(false)
  const [isUnstagingAll, setIsUnstagingAll] = useState(false)
  const [isAborting, setIsAborting] = useState(false)

  const isBusy = isStagingAll || isUnstagingAll || isAborting

  const handleStageAll = useCallback(async () => {
    setIsStagingAll(true)
    try {
      const projectId = useProjectStore.getState().activeProjectId
      if (!projectId) return
      await runGitOperation({
        projectId,
        kind: 'stage-all',
        label: 'Staged all changes',
        fn: () => stageAll(),
      })
    } finally {
      setIsStagingAll(false)
    }
  }, [])

  const handleUnstageAll = useCallback(async () => {
    setIsUnstagingAll(true)
    try {
      const projectId = useProjectStore.getState().activeProjectId
      if (!projectId) return
      await runGitOperation({
        projectId,
        kind: 'unstage-all',
        label: 'Unstaged all changes',
        fn: () => unstageAll(),
      })
    } finally {
      setIsUnstagingAll(false)
    }
  }, [])

  /** Abort an in-progress merge or rebase (Phase 6). */
  const handleAbort = useCallback(async (op: 'merge' | 'rebase') => {
    setIsAborting(true)
    try {
      const projectId = useProjectStore.getState().activeProjectId
      if (!projectId) return
      // Backend emits git:status_changed → useGitStatusEvents refreshes
      // (and re-fetches merge/rebase state).
      await runGitOperation({
        projectId,
        kind: op === 'merge' ? 'merge-abort' : 'rebase-abort',
        label: op === 'merge' ? 'Aborted merge' : 'Aborted rebase',
        fn: () => (op === 'merge' ? abortMerge() : abortRebase()),
      })
    } finally {
      setIsAborting(false)
    }
  }, [])

  return {
    isStagingAll,
    isUnstagingAll,
    isAborting,
    isBusy,
    handleStageAll,
    handleUnstageAll,
    handleAbort,
  }
}
