import { useEffect } from 'react'
import { getConfig } from '@/api/config'
import { subscribe } from '@/api/runtime'
import { logger } from '@/lib/logger'
import { useExperimentalStore } from '@/stores/experimentalStore'

/**
 * Reads the master experimental-features switch from the shared store and
 * triggers the one-time fetch from GetConfig. The store is the single source
 * of truth after the initial load, and Settings updates it directly via
 * setEnabled.
 *
 * The fetch is attempted on mount and, if it fails (e.g. the backend is still
 * starting during the splash phase), again on the `backend:ready` event — the
 * same race App.tsx guards against with its listProjects safety net. Without
 * the retry, a transient startup failure would leave experimental features
 * permanently disabled for the whole session.
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
    const unsubscribe = subscribe('backend:ready', load)

    return () => {
      cancelled = true
      unsubscribe?.()
    }
  }, [])

  return enabled
}
