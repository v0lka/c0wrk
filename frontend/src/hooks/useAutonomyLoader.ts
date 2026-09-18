import { useEffect } from 'react'
import { getSecuritySettings } from '@/api/config'
import { onGlobalEvent } from '@/api/runtime'
import { normalizeAutonomyMode } from '@/lib/autonomyModes'
import { logger } from '@/lib/logger'
import { DEFAULT_SILENT_POLICIES, useAutonomyStore } from '@/stores/autonomyStore'

/**
 * Hydrates the app-wide autonomy posture (autonomyStore) from
 * GetSecuritySettings, mirroring useExperimentalFeatures.
 *
 * Mounted once at the App root so the review-prompt gate (useChatEvents) always
 * has a current posture, regardless of whether Settings has ever been opened.
 * The read is attempted on mount and re-run on `backend:ready` (the backend
 * answers with canonical defaults until Startup has loaded config.yaml, so a
 * mount-time read can predate the real posture) and on `config:updated`
 * (emitted after every persisted config mutation, including a Settings →
 * Security save), so a later change is picked up without an app restart.
 */
export function useAutonomyLoader(): void {
  useEffect(() => {
    let cancelled = false
    let inFlight = false
    let pendingRetry = false

    const load = () => {
      if (inFlight) {
        // A retry arrived while a fetch is in flight: remember it and re-run
        // after the current attempt settles, so a one-shot backend:ready
        // emission is not consumed with no effect.
        pendingRetry = true
        return
      }
      inFlight = true

      getSecuritySettings()
        .then((settings) => {
          if (cancelled) return
          // A response without silent_mode (older payload) falls back to the
          // documented sub-policy defaults, and the mode fails safe to
          // "standard" on an unrecognized value — never an autonomous
          // posture the config loader would reject.
          useAutonomyStore.getState().setAutonomy({
            autonomy_mode: normalizeAutonomyMode(settings.autonomy_mode),
            ...(settings.silent_mode ?? DEFAULT_SILENT_POLICIES),
          })
        })
        .catch((err) => {
          // Fail closed: keep the defaults (standard mode). `loaded` stays
          // false so consumers see "unknown" rather than "definitively
          // standard", and the backend:ready / config:updated retries can
          // still recover.
          logger.error('useAutonomyLoader: failed to load security settings:', err)
        })
        .finally(() => {
          inFlight = false
          if (pendingRetry && !cancelled) {
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
}
