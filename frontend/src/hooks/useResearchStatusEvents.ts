// Side-effect-only hook that keeps researchStore in sync with the backend.
//
// Modeled on useGitStatusEvents: fetches GetResearchStatus once on mount, then
// subscribes to `research:changed` (emitted by EnableResearch/DisableResearch
// and external artifact writes) and `workspace:tree_changed` (the workspace
// watcher, which sees hypothesis/brief/prior-art file edits) with a shared
// 50ms debounce. Purely a side-effect hook — returns void.

import { useEffect, useCallback, useRef } from 'react'
import { getResearchStatus, getResearchNextStep } from '@/api/research'
import { subscribe } from '@/api/runtime'
import { logger } from '@/lib/logger'
import { useProjectStore } from '@/stores/projectStore'
import { useResearchStore } from '@/stores/researchStore'

export function useResearchStatusEvents(): void {
  const activeProjectId = useProjectStore((s) => s.activeProjectId)

  const debounceRef = useRef<ReturnType<typeof setTimeout> | null>(null)

  // Stable refresh: fetch status for the active project and load it, but only
  // if the project hasn't switched while the fetch was in flight.
  const refresh = useCallback(async () => {
    const projectId = activeProjectId
    if (!projectId) {
      useResearchStore.getState().reset()
      return
    }
    useResearchStore.getState().setLoading(true)
    try {
      const status = await getResearchStatus(projectId)
      // Guard against stale fetches after a project switch.
      if (useProjectStore.getState().activeProjectId === projectId) {
        useResearchStore.getState().loadStatus(status, projectId)
      }
    } catch (err) {
      // Stale-guard the error path too: a project switch while the fetch was
      // in flight must not surface the old project's failure after a newer
      // project has already started loading.
      if (useProjectStore.getState().activeProjectId === projectId) {
        useResearchStore.getState().setError(
          err instanceof Error ? err.message : 'Failed to load research status',
        )
      }
    }

    // The recommended next step is a separate lightweight RPC. A failure here
    // must not surface as a status error — the recommendation is best-effort,
    // and the dashboard falls back to a muted empty card.
    try {
      const nextStep = await getResearchNextStep(projectId)
      if (useProjectStore.getState().activeProjectId === projectId) {
        useResearchStore.getState().loadNextStep(nextStep)
      }
    } catch (err) {
      logger.debug('[research] next-step fetch failed:', err)
    }
  }, [activeProjectId])

  // --- Initial load + reload on project switch ---
  useEffect(() => {
    refresh()
  }, [refresh])

  // --- Subscribe to research:changed + workspace:tree_changed (50ms debounce) ---
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

    const unsubResearch = subscribe('research:changed', debouncedRefresh)
    const unsubWorkspace = subscribe('workspace:tree_changed', (data: unknown) => {
      // Skip the full refetch when the change was research-scoped: the
      // incremental path (useResearchFileWatcher via research:file_changed)
      // handles it, so a full status fetch would be redundant. The backend
      // annotates the event with { research_scoped: true } for research dir
      // changes; non-research changes (or the nil/null payload from older
      // emitters) fall through to the normal debounced refresh.
      if (isResearchScopedChange(data)) return
      debouncedRefresh()
    })

    return () => {
      unsubResearch()
      unsubWorkspace()
      if (debounceRef.current !== null) {
        clearTimeout(debounceRef.current)
        debounceRef.current = null
      }
    }
  }, [refresh])
}

/** Type guard: returns true when a workspace:tree_changed payload indicates
 *  the change was research-scoped (all changed paths were inside the research
 *  directory). The backend annotates such events with { research_scoped: true }
 *  so this hook can defer to the incremental update path. */
function isResearchScopedChange(data: unknown): boolean {
  if (typeof data !== 'object' || data === null) return false
  return (data as Record<string, unknown>)['research_scoped'] === true
}
