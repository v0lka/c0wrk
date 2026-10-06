import { useEffect } from 'react'
import { getSystemFonts } from '@/api/fonts'
import { onGlobalEvent } from '@/api/runtime'
import { logger } from '@/lib/logger'
import { useFontStore } from '@/stores/fontStore'

/**
 * Loads the desktop environment's UI + monospaced font families into the
 * font store's session detections (`detectedUIFamily`/`detectedMonoFamily`)
 * so the font settings have candidates to offer. Mounted once at the App
 * root.
 *
 * The fetch is attempted on mount and, if it fails (the mount request can
 * arrive before the backend is ready — e.g. during the splash phase, when
 * getApp() throws or the RPC rejects), again on the `backend:ready` event —
 * the same race useExperimentalFeatures/useModelProfilesGate guard against.
 * A failure never latches, so the one-shot backend:ready emission is never
 * consumed by a doomed attempt; a `backend:ready` arriving while a fetch is
 * in flight is remembered and replayed once that attempt settles. A
 * successful fetch latches for the lifetime of the effect: a null detection
 * (empty gsettings values — non-GNOME desktop) is a definitive answer, not
 * a failure, and retrying it cannot change the outcome.
 *
 * Detection only records the candidates — it deliberately never touches
 * <html>: what is rendered is decided solely by the store's persisted chosen
 * families (`uiFontFamily`/`monoFontFamily`), so a late detection cannot
 * repaint text behind the user's choice.
 */
export function useSystemFonts(): void {
  useEffect(() => {
    let cancelled = false
    let inFlight = false
    let pendingRetry = false
    // Effect-local latch: the store deliberately carries no `loaded` field
    // (its shape is the persisted chosen families + the session detections),
    // so "has a definitive answer" lives here. StrictMode's remount gets a
    // fresh closure and simply re-fetches once — an idempotent read.
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

      getSystemFonts()
        .then((fonts) => {
          if (cancelled) return
          latched = true
          useFontStore.getState().setDetectedFonts(fonts.uiFamily, fonts.monoFamily)
        })
        .catch((err) => {
          // Not latched: keep the retry path armed. No family is written, so
          // the document keeps whatever it had (default font, or the family
          // from an earlier successful session-fetch).
          logger.error('useSystemFonts: failed to load system fonts:', err)
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
