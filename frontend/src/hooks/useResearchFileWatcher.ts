// Side-effect-only hook that incrementally updates the hypothesis graph when
// files inside the research directory change.
//
// Subscribes to `research:file_changed` events (emitted by the workspace
// watcher when hypothesis cards, brief, prior-art, or graph files are modified).
// Calls GetResearchGraph RPC for a lightweight graph fetch and updates the
// store via loadGraph — avoiding a full status refetch. Uses a 100ms debounce
// to batch rapid consecutive edits. Purely a side-effect hook — returns void.
//
// The full refetch path (useResearchStatusEvents) handles workspace:tree_changed
// for non-research-scoped changes, or when the incremental path is not available
// (e.g. activeResearchRoot was empty when the watcher callback ran). When the
// change IS research-scoped, useResearchStatusEvents skips its refetch so only
// this incremental path runs.

import { useEffect, useCallback, useRef } from 'react'
import { getResearchGraph, getResearchNextStep } from '@/api/research'
import { subscribe } from '@/api/runtime'
import { logger } from '@/lib/logger'
import { useProjectStore } from '@/stores/projectStore'
import { useResearchStore } from '@/stores/researchStore'
import { applyGraphOrRefresh, fullResearchRefresh } from '@/components/research/applyGraphOrRefresh'

/** Type guard for the research:file_changed event payload. The backend emits
 *  { project_id: string, paths: string (comma-separated) }; only project_id is
 *  validated because it is the only field this watcher consumes. */
function isResearchFileChangedPayload(data: unknown): data is { project_id: string } {
  if (typeof data !== 'object' || data === null) return false
  const obj = data as Record<string, unknown>
  return typeof obj['project_id'] === 'string'
}

export function useResearchFileWatcher(): void {
  // Read from projectStore (not researchStore) so the incremental update
  // always targets the currently active project — researchStore.projectId
  // can be null on first render or stale after a project switch.
  const activeProjectId = useProjectStore((s) => s.activeProjectId)
  const researchEnabled = useResearchStore((s) => s.status?.enabled ?? false)

  const debounceRef = useRef<ReturnType<typeof setTimeout> | null>(null)

  // Stable incremental update: fetch only the graph/metrics for the active
  // project and apply it through the SHARED convergence helper
  // (applyGraphOrRefresh — the same path the direct mutation sites use,
  // [18]b), but only if the project hasn't switched while the fetch was in
  // flight. The full-status fallback lives in the helper too
  // (fullResearchRefresh) and also refreshes the recommended next step.
  const updateGraph = useCallback(async () => {
    const projectId = activeProjectId
    if (!projectId || !researchEnabled) return

    // [60] LWW ticket: stamp the sync sequence at fetch START so the store
    // can reject snapshots older than its last successful sync — a slow
    // incremental fetch that resolves after a newer sync landed (the
    // watchdog's full refresh or a direct mutation) can never regress the
    // panel to stale data.
    const startedSeq = useResearchStore.getState().graphSyncSeq
    try {
      const graph = await getResearchGraph(projectId)
      // [18]b: the active-project guard, the incremental apply, and the
      // full-refetch fallback are owned by the shared helper.
      await applyGraphOrRefresh(graph, projectId, startedSeq)

      // A file change can flip the phase (e.g. a status transition), so the
      // recommendation must be refreshed alongside the graph. Best-effort:
      // a failure leaves the previous recommendation in place.
      try {
        const nextStep = await getResearchNextStep(projectId)
        if (useProjectStore.getState().activeProjectId === projectId) {
          useResearchStore.getState().loadNextStep(nextStep)
        }
      } catch (err) {
        logger.debug('[research] incremental next-step fetch failed:', err)
      }
    } catch (err) {
      // Incremental failure (RPC error, boundary validation) must not leave
      // the panel stale: fall back to the full status refetch so the panel
      // still converges (the research-scoped skip in useResearchStatusEvents
      // means nobody else will).
      logger.debug('[research] incremental graph update failed:', err)
      await fullResearchRefresh(projectId)
    }
  }, [activeProjectId, researchEnabled])

  // --- Subscribe to research:file_changed (100ms debounce) ---
  // research:file_changed is emitted only for changes inside the research
  // directory (when activeResearchRoot is set). The payload carries project_id
  // and paths; we consume project_id to guard against stale events that arrive
  // after a project switch (avoids a redundant fetch for a project the user has
  // already navigated away from).
  useEffect(() => {
    const debouncedUpdate = (data: unknown) => {
      // Consume the payload: skip when the event belongs to a different
      // project than the one currently active (e.g. a late event arriving
      // after a project switch).
      if (isResearchFileChangedPayload(data)) {
        const currentProject = useProjectStore.getState().activeProjectId
        if (data.project_id !== currentProject) return
      }

      logger.debug('[research] file_changed event received, scheduling update')
      if (debounceRef.current !== null) {
        clearTimeout(debounceRef.current)
      }
      debounceRef.current = setTimeout(() => {
        debounceRef.current = null
        updateGraph()
      }, 100)
    }

    logger.debug(
      '[research] subscribing to research:file_changed',
      'projectId',
      activeProjectId,
      'enabled',
      researchEnabled,
    )
    const unsubResearch = subscribe('research:file_changed', debouncedUpdate)

    return () => {
      logger.debug('[research] unsubscribing from research:file_changed')
      unsubResearch()
      if (debounceRef.current !== null) {
        clearTimeout(debounceRef.current)
        debounceRef.current = null
      }
    }
  }, [researchEnabled, activeProjectId, updateGraph])
}
