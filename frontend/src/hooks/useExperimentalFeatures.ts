import { useEffect } from 'react'
import { getConfig } from '@/api/config'
import { logger } from '@/lib/logger'
import { useExperimentalStore } from '@/stores/experimentalStore'

/** Load-once guard so every mounted consumer does not re-fetch GetConfig. */
let loadStarted = false

/**
 * Reads the master experimental-features switch from the shared store and
 * triggers the one-time fetch from GetConfig on first mount. The hook is safe
 * to call from any component; the store is the single source of truth after
 * the initial load, and Settings updates it directly via setEnabled.
 */
export function useExperimentalFeatures(): boolean {
  const enabled = useExperimentalStore((s) => s.enabled)

  useEffect(() => {
    if (loadStarted) return
    loadStarted = true

    getConfig()
      .then((cfg) => {
        useExperimentalStore.getState().setEnabled(cfg.experimental?.enabled ?? false)
      })
      .catch((err) => {
        // Fail closed: keep the default (off) so gated features stay hidden.
        logger.error('useExperimentalFeatures: failed to load config:', err)
      })
      .finally(() => {
        useExperimentalStore.getState().setLoaded(true)
      })
  }, [])

  return enabled
}
