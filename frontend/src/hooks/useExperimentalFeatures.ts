import { useEffect } from 'react'
import { getConfig } from '@/api/config'
import { onGlobalEvent } from '@/api/runtime'
import { logger } from '@/lib/logger'
import { useExperimentalStore } from '@/stores/experimentalStore'

/**
 * Reads the master experimental-features switch from the shared store and
 * triggers the one-time fetch from GetConfig. The store is the single source
 * of truth after the initial load, and Settings updates it directly via
 * setEnabled.
 *
 * The fetch is attempted on mount and, if it fails (e.g. the backend is still
 * starting during the splash phase) or resolves with `loaded=false` (the
 * backend answers RPCs with a zeroed config until Startup finishes), again on
 * the `backend:ready` event — the same race App.tsx guards against with its
 * listProjects safety net — and on `config:updated`, emitted by the backend
 * after every persisted config mutation (see specs/contracts/event-catalog.md).
 * That second retry covers the residual case where both earlier attempts
 * failed transiently: the next settings save re-reads the live config
 * instead of leaving the switch "unknown" until an app restart. Once the
 * switch has latched, both retries are no-ops — Settings keeps the store in
 * sync directly.
 */
export function useExperimentalFeatures(): boolean {
  const enabled = useExperimentalStore((s) => s.enabled)

  useEffect(() => {
    let cancelled = false
    let inFlight = false
    let pendingRetry = false

    const load = () => {
      if (useExperimentalStore.getState().loaded) return
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
          // SUCCEEDS with loaded=false (config not yet initialized) and a
          // zeroed experimental section. Latching that zero would flip the
          // switch off for the whole session and permanently consume the
          // one-shot backend:ready retry — the stored `enabled: true` would
          // appear "not read after restart". Keep the unknown state so the
          // backend:ready / config:updated retries re-fetch once the config
          // is live.
          if (cfg.loaded === false) return
          useExperimentalStore.getState().setEnabled(cfg.experimental?.enabled ?? false)
          useExperimentalStore.getState().setLoaded(true)
        })
        .catch((err) => {
          // Fail closed: keep the default (off) so gated features stay hidden.
          // `loaded` stays false so consumers see "unknown" rather than
          // "definitively off", and a retry can still recover.
          logger.error('useExperimentalFeatures: failed to load config:', err)
        })
        .finally(() => {
          inFlight = false
          if (pendingRetry && !cancelled && !useExperimentalStore.getState().loaded) {
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

  return enabled
}
