// Subscribes to the global self-update events and reflects them into
// updateStore, and loads the running build version on mount.
//
// Mount this hook once at the app root (App.tsx) so the subscriptions are
// active for the whole session. All five update events are wired: the typed
// helpers in @/api/updater validate each payload before invoking the callback,
// so malformed emissions are dropped rather than corrupting the store.
//
// NOTE: this hook NO LONGER performs an automatic check on mount. The
// automatic background check is the sole responsibility of the Go backend
// (FrontendAPI.RunBackgroundUpdateCheck), which honours both the operator gate
// (config.yaml updates.enabled) and the user gate (update-settings.json
// enabled + auto_check), respects the check interval, and caches the result so
// a discovered update is immediately downloadable. The frontend only needs to
// listen for the resulting update:available event (wired below) and surface the
// running version. A user can still trigger a manual check from the Settings
// panel.

import { useEffect } from 'react'
import {
  onUpdateAvailable,
  onUpdateNone,
  onUpdateProgress,
  onUpdateDownloaded,
  onUpdateError,
  getUpdateSettings,
} from '@/api/updater'
import { useUpdateStore } from '@/stores/updateStore'
import { logger } from '@/lib/logger'

export function useUpdateChecker(): void {
  // ── Load running version on mount ─────────────────────────────────────
  // The running version is always loaded so the Settings panel and the toast
  // can show "вы на vX.Y.Z" regardless of whether an update is available.
  useEffect(() => {
    let cancelled = false
    getUpdateSettings()
      .then((settings) => {
        if (cancelled) return
        useUpdateStore.getState().setCurrentVersion(settings.current_version)
      })
      .catch((err) => {
        // Settings unavailable (e.g. backend not ready yet) — non-fatal; the
        // Settings button still lets the user check manually.
        logger.warn('Could not load update settings:', err)
      })
    return () => {
      cancelled = true
    }
  }, [])

  // ── Wire the five global update events into the store ─────────────────
  useEffect(() => {
    const offAvailable = onUpdateAvailable((data) => {
      useUpdateStore.getState().onAvailable(data)
    })
    const offNone = onUpdateNone(() => {
      useUpdateStore.getState().onNone()
    })
    const offProgress = onUpdateProgress((data) => {
      useUpdateStore.getState().onProgress(data.done, data.total)
    })
    const offDownloaded = onUpdateDownloaded(() => {
      useUpdateStore.getState().onDownloaded()
    })
    const offError = onUpdateError((data) => {
      useUpdateStore.getState().onError(data.message)
    })
    return () => {
      offAvailable()
      offNone()
      offProgress()
      offDownloaded()
      offError()
    }
  }, [])
}
