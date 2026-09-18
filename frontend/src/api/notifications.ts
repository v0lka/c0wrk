// System-notification desktop bindings — the single import path for the Go
// notification transport (desktop/notifications.go).
//
// Transport decision (documented per the step spec): ALL notification runtime
// calls route through the Go bindings (`App.InitNotifications` /
// `App.SendSystemNotification` / `App.ShowTestNotification`), never through
// `window.runtime.SendNotification`. One accountable path matters because the
// click round-trip is Go-owned: SendSystemNotification's `data` map is what
// the Wails click callback returns as `UserInfo`, and only the notification
// the Go layer registered the callback against emits `notification_clicked`.
// A banner sent through `window.runtime` would still arrive at the same
// callback (both transports hit the same Wails frontend), but the ids and the
// send/init bookkeeping would live in two places — exactly the split-brain
// this wrapper exists to prevent.

import { getApp, onGlobalEvent, reportDroppedEvent } from './runtime'
import { logger } from '@/lib/logger'
import type { NotificationClickedData } from '@/types/events'
import { isNotificationClickedData } from '@/types/events'

/** Data map forwarded through the Go binding; keys `sessionId` and
 *  `projectId` are the click-routing contract (returned as UserInfo by the
 *  Wails click callback, extracted into notification_clicked). Values are
 *  plain strings — `undefined` entries are dropped by sendSystemNotification
 *  so the Go `map[string]string` binding never receives a null value. */
export interface SystemNotificationData {
  sessionId?: string
  projectId?: string
  [key: string]: string | undefined
}

/**
 * Initialize the OS notification bridge on the Go side: Wails
 * InitializeNotifications + the single OnNotificationResponse callback +
 * the macOS authorization prompt (denial logged, not fatal). Idempotent —
 * a second call is a no-op on the backend; a FAILED init is retried, so the
 * caller may call this again later (e.g. on retry after a transient
 * failure).
 */
export async function initSystemNotifications(): Promise<void> {
  const app = getApp()
  await app.InitNotifications()
}

/**
 * Send one native notification through the Go transport. The `data` map is
 * the click-routing context: on activation the backend emits
 * `notification_clicked` with session/project ids extracted from it.
 * Rejects on RPC failure (the lib wrapper catches and downgrades to warn).
 */
export async function sendSystemNotification(
  title: string,
  body: string,
  data?: SystemNotificationData,
): Promise<void> {
  const app = getApp()
  // Drop undefined entries: the Go binding unmarshals into map[string]string
  // and a null value would fail the call.
  const clean: Record<string, string> = {}
  for (const [key, value] of Object.entries(data ?? {})) {
    if (value !== undefined) clean[key] = value
  }
  await app.SendSystemNotification(title, body, Object.keys(clean).length > 0 ? clean : undefined)
}

/**
 * Read the current OS-level notification authorization WITHOUT prompting.
 * macOS answers from UNNotificationCenter; Linux/Windows always grant (no
 * authorization concept), so the Settings hint never renders there. An RPC
 * failure resolves to true — a transient transport error must not paint a
 * misleading "disabled in macOS Settings" hint.
 */
export async function checkNotificationAuthorization(): Promise<boolean> {
  const app = getApp()
  try {
    return await app.CheckNotificationAuthorization()
  } catch (err) {
    logger.warn('[system-notifications] authorization check failed; assuming granted', err)
    return true
  }
}

/** Settings "preview" button: an unattributed banner — a click focuses the
 *  window (Go-side showWindow) but never navigates. */
export async function showTestNotification(): Promise<void> {
  const app = getApp()
  await app.ShowTestNotification()
}

/**
 * Subscribe to notification activations. The payload is validated with
 * `isNotificationClickedData`; a malformed one is reported through
 * `reportDroppedEvent` and never reaches the callback. Returns an
 * unsubscribe function (a no-op when the Wails runtime is absent).
 */
export function onNotificationClicked(
  callback: (data: NotificationClickedData) => void,
): () => void {
  return onGlobalEvent('notification_clicked', (data) => {
    if (data === undefined) return
    if (!isNotificationClickedData(data)) {
      reportDroppedEvent('notification_clicked', data)
      return
    }
    callback(data)
  })
}
