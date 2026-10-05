import { useEffect } from 'react'
import { getSystemUIFont } from '@/api/systemFont'
import { onGlobalEvent } from '@/api/runtime'
import { logger } from '@/lib/logger'
import { useSystemFontStore } from '@/stores/systemFontStore'

/**
 * Loads the desktop environment's UI font family into the systemFont store
 * so the "follow system font" switch has something to apply. Mounted once at
 * the App root.
 *
 * The fetch is attempted on mount and, if it fails (the mount request can
 * arrive before the backend is ready — e.g. during the splash phase, when
 * getApp() throws or the RPC rejects), again on the `backend:ready` event —
 * the same race useExperimentalFeatures/useModelProfilesGate guard against.
 * A failure never latches, so the one-shot backend:ready emission is never
 * consumed by a doomed attempt; a `backend:ready` arriving while a fetch is
 * in flight is remembered and replayed once that attempt settles. A
 * successful fetch latches for the lifetime of the effect: `available=false`
 * is a definitive answer (no gsettings / non-GNOME desktop), not a failure,
 * and retrying it cannot change the outcome.
 *
 * The store's setSystemFontFamily applies the family to <html> only while
 * followSystemFont is on, so this hook is unconditional — it merely feeds the
 * cache; the user's opt-in switch decides whether anything is rendered with
 * it.
 */
export function useSystemFont(): void {
  useEffect(() => {
    let cancelled = false
    let inFlight = false
    let pendingRetry = false
    // Effect-local latch: the store deliberately carries no `loaded` field
    // (its shape is the persisted flag + the session family), so "has a
    // definitive answer" lives here. StrictMode's remount gets a fresh
    // closure and simply re-fetches once — an idempotent read.
    let latched = false

    const load = () => {
      if (latched) return
      if (inFlight) {
        // A backend:ready retry arrived while a fetch is in flight: remember
        // it and re-run after the current attempt settles, otherwise the
        // one-shot emission is consumed with no effect.
        pendingRetry = true
        return
      }
      inFlight = true

      getSystemUIFont()
        .then((font) => {
          if (cancelled) return
          latched = true
          useSystemFontStore.getState().setSystemFontFamily(font ? font.family : null)
        })
        .catch((err) => {
          // Not latched: keep the retry path armed. No family is written, so
          // the document keeps whatever it had (default font, or the family
          // from an earlier successful session-fetch).
          logger.error('useSystemFont: failed to load system UI font:', err)
        })
        .finally(() => {
          inFlight = false
          if (pendingRetry && !cancelled && !latched) {
            pendingRetry = false
            load()
          }
        })
    }

    load()
    const unsubscribe = onGlobalEvent('backend:ready', load)

    return () => {
      cancelled = true
      unsubscribe?.()
    }
  }, [])
}
