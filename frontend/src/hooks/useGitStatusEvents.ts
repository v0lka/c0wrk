// Side-effect-only hook that subscribes to git:status_changed events
// and keeps gitPanelStore.entries in sync with the backend.

import { useEffect, useCallback, useRef } from 'react'
import { getGitStatus } from '@/api/workspace'
import { getCurrentBranch, getRebaseMergeState } from '@/api/git'
import { subscribe } from '@/api/runtime'
import { useProjectStore } from '@/stores/projectStore'
import { useGitPanelStore } from '@/stores/gitPanelStore'
import { toEntries } from '@/lib/gitStatus'

/**
 * Subscribes to `git:status_changed` events and refreshes the git panel store.
 *
 * - Calls GetGitStatus() once on mount (initial load).
 * - Debounces subsequent event-triggered refreshes by 50ms.
 * - Auto-unsubscribes on unmount via useEffect cleanup.
 * - Returns void — purely a side-effect hook.
 */
export function useGitStatusEvents(): void {
  const activeProjectId = useProjectStore((s) => s.activeProjectId)
  const projects = useProjectStore((s) => s.projects)

  const workspacePath =
    activeProjectId && projects
      ? projects.find((p) => p.id === activeProjectId)?.workspace_path ?? null
      : null

  // Debounce timer ref — cleared on unmount to prevent memory leaks
  const debounceRef = useRef<ReturnType<typeof setTimeout> | null>(null)

  // Stable callback: fetches status and updates the store.
  // useCallback dependency on workspacePath ensures the event handler
  // always uses the correct path without unnecessary re-subscriptions.
  const refresh = useCallback(async () => {
    if (!workspacePath) return

    // Fetch git status, current branch, and merge/rebase state in parallel.
    // Promise.allSettled ensures one failure does not prevent the others.
    const [statusResult, branchResult, stateResult] = await Promise.allSettled([
      getGitStatus(workspacePath),
      getCurrentBranch(),
      getRebaseMergeState(),
    ])

    // Always update branch if the call succeeded
    if (branchResult.status === 'fulfilled') {
      useGitPanelStore.getState().setBranch(branchResult.value)
    }

    // Merge/rebase state (Phase 6) — default to "no op in progress" on failure.
    if (stateResult.status === 'fulfilled') {
      useGitPanelStore.getState().setMergeRebaseState(stateResult.value)
    } else {
      useGitPanelStore.getState().setMergeRebaseState({
        is_merging: false,
        is_rebasing: false,
      })
    }

    if (statusResult.status === 'fulfilled') {
      const entries = toEntries(statusResult.value)
      const store = useGitPanelStore.getState()
      store.loadEntries(entries)
      store.setGitRepo(true)
      store.setError(null)
    } else {
      const store = useGitPanelStore.getState()
      store.setGitRepo(false)
      store.loadEntries([])
      store.setError('Failed to load git status')
    }
  }, [workspacePath])

  // --- Initial load on mount ---
  useEffect(() => {
    refresh()
  }, [refresh])

  // --- Subscribe to git:status_changed with 50ms debounce ---
  useEffect(() => {
    const unsub = subscribe('git:status_changed', () => {
      if (debounceRef.current !== null) {
        clearTimeout(debounceRef.current)
      }
      debounceRef.current = setTimeout(() => {
        debounceRef.current = null
        refresh()
      }, 50)
    })

    return () => {
      // Unsubscribe from Wails event
      unsub()
      // Clear any pending debounce timer to prevent memory leaks
      if (debounceRef.current !== null) {
        clearTimeout(debounceRef.current)
        debounceRef.current = null
      }
    }
  }, [refresh])
}
