// Side-effect-only hook that keeps paperStore in sync with the backend.
//
// Fetches the active project's paper library on mount / project switch and
// invalidates it on every `papers:changed` event (50ms debounce, so a burst of
// watcher callbacks coalesces into ONE refetch — mirroring
// useResearchStatusEvents). Mounted ONCE at the App root by ResearchEventBridge
// — PapersView is a pure view over paperStore.
//
// The paper library always exists at the canonical research root of a real
// project, so this hook never gates on any research state. It only skips the No Project
// pseudo-project (the GetPapers RPC requires a real project).
//
// The event path always refetches the ACTIVE project (never the loaded one):
// during a project switch the store's `projectId` still names the departed
// project, so scoping an event to it would let a late old-project event start a
// fetch that supersedes the new project's load. An event for a project other
// than the active one is ignored outright.
//
// There is no convergence watchdog: the library is refreshed only on a project
// switch and on a RECEIVED `papers:changed`, so a dropped watcher event is not
// recovered until the next event or switch (see `lastSyncAt` in paperStore).

import { useCallback, useEffect, useRef } from 'react'
import { subscribe } from '@/api/runtime'
import { isPapersChangedPayload } from '@/api/papers'
import { useProjectStore, selectIsNoProject } from '@/stores/projectStore'
import { fetchPaperLibrary, usePaperStore } from '@/stores/paperStore'

/** Coalesce a burst of `papers:changed` events into one refetch. */
const DEBOUNCE_MS = 50

export function usePapersEvents(): void {
  const activeProjectId = useProjectStore((s) => s.activeProjectId)
  const isNoProject = useProjectStore(selectIsNoProject)

  const debounceRef = useRef<ReturnType<typeof setTimeout> | null>(null)
  // The debounced callback fires on a timer (and the event handler reads the
  // active project), so both must see the LATEST values rather than the closure
  // captured when the effect ran.
  const activeRef = useRef(activeProjectId)
  const isNoProjectRef = useRef(isNoProject)
  useEffect(() => {
    activeRef.current = activeProjectId
    isNoProjectRef.current = isNoProject
  })

  // Stable refresh: (re)load the library for the ACTIVE project, resetting the
  // store when no real project is active.
  const refresh = useCallback(async () => {
    const projectId = activeRef.current
    if (isNoProjectRef.current || projectId === null) {
      usePaperStore.getState().reset()
      return
    }
    await fetchPaperLibrary(projectId)
  }, [])

  // --- Initial load + reload on project switch ---
  useEffect(() => {
    void refresh()
  }, [refresh, activeProjectId, isNoProject])

  // --- Subscribe to papers:changed (debounced) ---
  useEffect(() => {
    const debouncedRefresh = () => {
      if (debounceRef.current !== null) clearTimeout(debounceRef.current)
      debounceRef.current = setTimeout(() => {
        debounceRef.current = null
        void refresh()
      }, DEBOUNCE_MS)
    }

    const unsubscribe = subscribe('papers:changed', (data: unknown) => {
      // Scope a well-formed event to the ACTIVE project: an event for a project
      // the user just left (a queued fsnotify callback from the departed
      // project's watcher) must not trigger a fetch that supersedes the new
      // project's load. A malformed payload carries no project id and falls
      // through to the debounced refresh of the active project.
      if (isPapersChangedPayload(data) && data.project_id !== activeRef.current) return
      debouncedRefresh()
    })

    return () => {
      unsubscribe()
      if (debounceRef.current !== null) {
        clearTimeout(debounceRef.current)
        debounceRef.current = null
      }
    }
  }, [refresh])
}
