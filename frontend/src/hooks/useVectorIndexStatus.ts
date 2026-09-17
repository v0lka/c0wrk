import { useEffect } from 'react'
import { subscribe } from '@/api/runtime'
import { getVectorIndexStatus } from '@/api/vector'
import { useProjectStore } from '@/stores/projectStore'
import { useVectorIndexStore } from '@/stores/vectorIndexStore'
import { isVectorIndexPayload } from '@/types/events'
import { logger } from '@/lib/logger'

/**
 * Keeps `vectorIndexStore.status` truthful for the whole app lifetime.
 *
 * Why a seed is required: the store's default is `idle`, and before this hook
 * it was ONLY ever filled by `vector_index:status` push events. The backend
 * emits those while a pass runs (and for No Project / init failure), but it
 * emits nothing for an already-built index when a project is restored at
 * startup — so the status bar rendered nothing at all (IndexingStatus hides
 * itself on the `idle` default) and the vector search panel claimed
 * "Select a project to search" while a project WAS selected.
 *
 * The getter `GetVectorIndexStatus` already reports the truthful state
 * (ready / indexing / reindexing / unavailable) for the active project, and is
 * cheap (an in-memory snapshot). It is fetched on mount, whenever the active
 * project changes, and again once the backend signals readiness — the mount
 * fetch can land before `backend:ready`.
 *
 * The push subscription stays alongside it so live progress still streams in.
 */
export function useVectorIndexStatus(): void {
  const activeProjectId = useProjectStore((s) => s.activeProjectId)

  useEffect(() => {
    let cancelled = false

    const refresh = async (): Promise<void> => {
      try {
        const status = await getVectorIndexStatus()
        if (cancelled) return
        if (!isVectorIndexPayload(status)) return
        useVectorIndexStore.getState().setStatus(status)
      } catch (err) {
        // Backend not ready yet (or no manager wired): the push event / the
        // next project change re-seeds this. Never surface as an app error.
        logger.debug('[vector] initial index status fetch failed:', err)
      }
    }

    void refresh()

    return () => {
      cancelled = true
    }
  }, [activeProjectId])

  useEffect(() => {
    return subscribe('backend:ready', () => {
      void getVectorIndexStatus()
        .then((status) => {
          if (!isVectorIndexPayload(status)) return
          useVectorIndexStore.getState().setStatus(status)
        })
        .catch((err: unknown) => {
          logger.debug('[vector] index status refresh on backend:ready failed:', err)
        })
    })
  }, [])

  useEffect(() => {
    return subscribe('vector_index:status', (data: unknown) => {
      if (!isVectorIndexPayload(data)) return
      useVectorIndexStore.getState().setStatus(data)
    })
  }, [])
}
