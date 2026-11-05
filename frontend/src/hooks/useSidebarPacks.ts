import { useEffect } from 'react'
import { getConfig } from '@/api/config'
import { onGlobalEvent } from '@/api/runtime'
import { logger } from '@/lib/logger'
import { useSidebarPacksStore } from '@/stores/sidebarPacksStore'

/**
 * Reads the sidebar surface gates (`research.enabled` / `specs.enabled`)
 * from the shared store and triggers the one-time fetch from GetConfig —
 * the same hydration shape as useIndexingSettings. The store is the single
 * source of truth after the initial load, and Settings
 * (ResearchSpecsSettings) updates it directly via setResearch/setSpecs
 * after every successful save.
 *
 * The fetch is attempted on mount and, if it fails (e.g. the backend is
 * still starting during the splash phase) or resolves with `loaded=false`
 * (the backend answers RPCs with a zeroed config until Startup finishes),
 * again on the `backend:ready` event and on `config:updated`, emitted by the
 * backend after every persisted config mutation. Once latched, both retries
 * are no-ops — Settings keeps the store in sync directly.
 *
 * Like the indexing switch, the unknown state (`loaded=false`) keeps both
 * gates at their TRUE default — the backend's resolved defaults for configs
 * predating the knobs — so the Research/Specs tabs stay visible during the
 * startup race instead of flashing hidden, and only a definitively persisted
 * opt-out hides them.
 */
export function useSidebarPacks(): { research: boolean; specs: boolean } {
  const research = useSidebarPacksStore((s) => s.research)
  const specs = useSidebarPacksStore((s) => s.specs)

  useEffect(() => {
    let cancelled = false
    let inFlight = false
    let pendingRetry = false

    const load = () => {
      if (useSidebarPacksStore.getState().loaded) return
      if (inFlight) {
        // A retry (e.g. backend:ready) arrived while a fetch is in flight:
        // remember it and re-run after the current attempt settles, otherwise
        // the one-shot backend:ready emission is consumed with no effect.
        pendingRetry = true
        return
      }
      inFlight = true

      getConfig()
        .then((cfg) => {
          if (cancelled) return
          // Startup race: before the backend's Startup finishes, GetConfig
          // SUCCEEDS with loaded=false (config not yet initialized) and
          // zeroed gate sections. Latching that would flip the tabs for the
          // whole session — keep the unknown state so the backend:ready /
          // config:updated retries re-fetch once the config is live.
          if (cfg.loaded === false) return
          // Absent blocks are the resolved defaults: both surfaces on — the
          // historical always-seeded behavior.
          const store = useSidebarPacksStore.getState()
          store.setResearch(cfg.research?.enabled ?? true)
          store.setSpecs(cfg.specs?.enabled ?? true)
          store.setLoaded(true)
        })
        .catch((err) => {
          // Keep the defaults (on) so the tabs stay visible; `loaded` stays
          // false so a retry can still recover.
          logger.error('useSidebarPacks: failed to load config:', err)
        })
        .finally(() => {
          inFlight = false
          if (pendingRetry && !cancelled && !useSidebarPacksStore.getState().loaded) {
            pendingRetry = false
            load()
          }
        })
    }

    load()
    const unsubscribes = [
      onGlobalEvent('backend:ready', load),
      onGlobalEvent('config:updated', load),
    ]

    return () => {
      cancelled = true
      unsubscribes.forEach((unsubscribe) => unsubscribe?.())
    }
  }, [])

  return { research, specs }
}
