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
  // project and load it via loadGraph, but only if the project hasn't switched
  // while the fetch was in flight.
  const updateGraph = useCallback(async () => {
    const projectId = activeProjectId
    if (!projectId || !researchEnabled) return

    try {
      const graph = await getResearchGraph(projectId)
      // Guard against stale updates after a project toggle: only commit the
      // result if the active project is still the one this fetch targeted.
      // (researchStore.loadGraph has its own project guard, but only when the
      // root carries active_project_id — this check is authoritative.)
      if (useProjectStore.getState().activeProjectId === projectId) {
        useResearchStore.getState().loadGraph(graph)
        logger.debug(
          '[research] graph updated incrementally',
          'project',
          projectId,
          'nodes',
          graph.graph.nodes.length,
        )
      }

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
      logger.debug('[research] incremental graph update failed:', err)
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
