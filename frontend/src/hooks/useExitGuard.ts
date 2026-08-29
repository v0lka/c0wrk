import { useEffect } from 'react'
import { onGlobalEvent, reportDroppedEvent } from '@/api/runtime'
import { isExitRequestedData } from '@/types/events'
import { useExitGuardStore } from '@/stores/exitGuardStore'

/**
 * Close-confirmation guard subscription for the intercepted-quit modal.
 *
 * The backend Wails OnBeforeClose hook (desktop/exit_guard.go) intercepts a
 * quit while sessions have live work, emits the global `app:exit_requested`
 * event with the active-session list, and prevents the close. This hook is
 * the single writer of `exitGuardStore`: it turns the event into modal state
 * and owns payload validation, so the dialog stays a pure view.
 *
 * Mount it ONCE at the app root (App.tsx), above the per-phase renders —
 * the dialog component itself is remounted on app-phase transitions and
 * must not own the subscription or the state.
 *
 * Malformed payloads are reported (never silently dropped) and still open
 * the modal in its generic list-less variant: the backend has already
 * prevented the quit, so swallowing the event would leave the app
 * unclosable with no dialog to answer. The user decision travels back via
 * the ConfirmExit RPC (see ExitConfirmDialog).
 */
export function useExitGuard(): void {
  useEffect(() => {
    return onGlobalEvent('app:exit_requested', (data) => {
      if (!isExitRequestedData(data)) {
        reportDroppedEvent('app:exit_requested', data)
        useExitGuardStore.getState().presentUnknown()
        return
      }
      useExitGuardStore.getState().present(data.sessions, data.update_pending ?? false)
    })
  }, [])
}
