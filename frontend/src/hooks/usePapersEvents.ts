// Side-effect-only hook that keeps paperStore in sync with the backend.
//
// Fetches the active project's paper library on mount / project switch and
// invalidates it on every `papers:changed` event (50ms debounce, mirroring
// useResearchStatusEvents). Mounted ONCE at the App root by
// ResearchEventBridge — PapersView is a pure view over paperStore.
//
// The paper library is watched independently of the RESEARCH toggle (hybrid
// mode), so this hook never gates on RESEARCH. It only skips the No Project
// pseudo-project (the GetPapers RPC requires a real project).

import { useCallback, useEffect, useRef } from 'react'
import { subscribe } from '@/api/runtime'
import { isPapersChangedPayload } from '@/api/papers'
import { useProjectStore, selectIsNoProject } from '@/stores/projectStore'
import { fetchPaperLibrary, applyPapersChanged, usePaperStore } from '@/stores/paperStore'

/** Coalesce a burst of `papers:changed` events into one refetch. */
const DEBOUNCE_MS = 50

export function usePapersEvents(): void {
  const activeProjectId = useProjectStore((s) => s.activeProjectId)
  const isNoProject = useProjectStore(selectIsNoProject)

  const debounceRef = useRef<ReturnType<typeof setTimeout> | null>(null)

  // Stable refresh: (re)load the library for the active project, resetting the
  // store when no real project is active.
  const refresh = useCallback(async () => {
    if (isNoProject || !activeProjectId) {
      usePaperStore.getState().reset()
      return
    }
    await fetchPaperLibrary(activeProjectId)
  }, [activeProjectId, isNoProject])

  // --- Initial load + reload on project switch ---
  useEffect(() => {
    void refresh()
  }, [refresh])

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
      // A well-formed event for the loaded project invalidates the library
      // through the store's own guard (another project's event is ignored);
      // a malformed payload falls back to a plain debounced refetch.
      if (isPapersChangedPayload(data)) {
        void applyPapersChanged(data)
        return
      }
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
