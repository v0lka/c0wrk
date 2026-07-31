// Side-effect-only hook that subscribes to git:status_changed events
// and keeps gitPanelStore.entries in sync with the backend.

import { useEffect, useCallback, useRef } from 'react'
import { getGitStatus } from '@/api/workspace'
import { getCurrentBranch, getRebaseMergeState, getDiffStats } from '@/api/git'
import { subscribe } from '@/api/runtime'
import { useProjectStore } from '@/stores/projectStore'
import { useGitPanelStore } from '@/stores/gitPanelStore'
import { toEntries } from '@/lib/gitStatus'
import type { GitPanelEntry } from '@/stores/gitPanelStore'
import type { DiffStat } from '@/types/models'

/**
 * Enrich raw `toEntries` output with per-file DiffStat (+N/-M indicators).
 *
 * `getDiffStats()` performs a single batched `git diff --numstat HEAD` call
 * whose keys are absolute paths — the same key convention used by GitStatus —
 * so a direct path comparison populates `entry.diffStat`. Failures (e.g. no
 * repo) leave the original entries untouched with `diffStat: null`.
 */
async function enrichWithDiffStats(entries: GitPanelEntry[]): Promise<GitPanelEntry[]> {
  if (entries.length === 0) return entries
  let stats: Record<string, DiffStat>
  try {
    stats = await getDiffStats()
  } catch {
    return entries
  }
  if (!stats || Object.keys(stats).length === 0) return entries
  return entries.map((entry) => {
    const stat = stats[entry.path]
    return stat ? { ...entry, diffStat: { added: stat.added, deleted: stat.deleted } } : entry
  })
}

/**
 * Subscribes to `git:status_changed` and `workspace:tree_changed` events and
 * refreshes the git panel store.
 *
 * - Calls GetGitStatus() once on mount (initial load).
 * - `git:status_changed` is emitted by the backend immediately after a
 *   UI-initiated git operation (stage/unstage/commit/checkout/…), giving
 *   instant feedback.
 * - `workspace:tree_changed` is emitted by the workspace watcher (which also
 *   watches `.git/`) ~200ms after filesystem changes — this catches external
 *   mutations (terminal `git` commands, other editors, branch switches from
 *   external tools) that `git:status_changed` would miss.
 * - Both events share a single 50ms debounce timer so near-simultaneous
 *   firings coalesce into one refresh.
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

  // Note: this hook is only mounted inside GitPanel, which WorkspacePanel
  // renders exclusively in CODE mode (the No-Project / CHAT path early-returns
  // to FileTreePanel). So no separate No-Project guard is needed here.

  // Debounce timer ref — cleared on unmount to prevent memory leaks
  const debounceRef = useRef<ReturnType<typeof setTimeout> | null>(null)

  // Stable callback: fetches status and updates the store.
  // useCallback dependency on workspacePath ensures the event handler always
  // uses the correct path without unnecessary re-subscriptions.
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
      const rawEntries = toEntries(statusResult.value)
      const store = useGitPanelStore.getState()
      store.setGitRepo(true)
      store.setError(null)
      // Enrich with batched +N/-M DiffStat before loading so GitFileEntry's
      // conditional rendering becomes live (FE-3 / D1). toEntries stays pure.
      const enriched = await enrichWithDiffStats(rawEntries)
      store.loadEntries(enriched)
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

  // --- Subscribe to git:status_changed + workspace:tree_changed (50ms debounce) ---
  // Both events share a single debounce timer: when a UI git operation fires
  // `git:status_changed` and the watcher subsequently fires
  // `workspace:tree_changed` for the same `.git/` mutation, only one refresh
  // runs. The watcher's 200ms Go-side debounce means `git:status_changed`
  // usually fires first, so UI-initiated ops still feel instant.
  useEffect(() => {
    const debouncedRefresh = () => {
      if (debounceRef.current !== null) {
        clearTimeout(debounceRef.current)
      }
      debounceRef.current = setTimeout(() => {
        debounceRef.current = null
        refresh()
      }, 50)
    }

    const unsubGit = subscribe('git:status_changed', debouncedRefresh)
    const unsubWorkspace = subscribe('workspace:tree_changed', debouncedRefresh)

    return () => {
      unsubGit()
      unsubWorkspace()
      // Clear any pending debounce timer to prevent memory leaks
      if (debounceRef.current !== null) {
        clearTimeout(debounceRef.current)
        debounceRef.current = null
      }
    }
  }, [refresh])
}
