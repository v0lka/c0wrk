// Wails runtime wrapper — single access point for window.go and window.runtime

import type { GlobalEventKey, GlobalEventMap, SessionEventKey, SessionEventMap } from '@/types/events'
import { logger } from '@/lib/logger'

/**
 * Report a session event whose payload failed its type guard and was dropped.
 * Malformed events must never disappear silently — a dropped `error` or
 * `task_complete` leaves the UI in a state that no longer matches the backend.
 */
export function reportDroppedEvent(event: string, data: unknown): void {
  logger.warn(`[events] dropped malformed session event "${event}"`, data)
}

// Extend Window to include Wails runtime bindings
declare global {
  interface Window {
    go: {
      desktop: {
        // eslint-disable-next-line @typescript-eslint/no-explicit-any
        App: Record<string, (...args: any[]) => Promise<any>>
      }
    }
    runtime: {
      EventsOn(eventName: string, callback: (...data: unknown[]) => void): () => void
      EventsEmit(eventName: string, ...data: unknown[]): void
      ClipboardSetText(text: string): Promise<boolean>
      BrowserOpenURL(url: string): void
    }
  }
}

/** Check if the Wails runtime is available */
export function isWailsReady(): boolean {
  return typeof window !== 'undefined' && !!window.runtime && !!window.go?.desktop?.App
}

/** Get the Wails runtime; throws if not ready */
export function getRuntime(): {
  EventsOn(eventName: string, callback: (...data: unknown[]) => void): () => void
  EventsEmit(eventName: string, ...data: unknown[]): void
  ClipboardSetText(text: string): Promise<boolean>
  BrowserOpenURL(url: string): void
} {
  if (typeof window === 'undefined' || !window.runtime) {
    throw new Error('Wails runtime is not available')
  }
  return window.runtime
}

/**
 * Get the desktop App RPC proxy; throws if not ready.
 * Returns `any` because Wails generates method bindings dynamically at
 * build time — there is no static type for the full App API surface.
 * Each API module (api/*.ts) wraps calls with runtime validation.
 */
// eslint-disable-next-line @typescript-eslint/no-explicit-any
export function getApp(): any {
  if (typeof window === 'undefined' || !window.go?.desktop?.App) {
    throw new Error('Wails App bindings are not available')
  }
  return window.go.desktop.App
}

/** Subscribe to a Wails event; returns an unsubscribe function */
export function subscribe(eventName: string, callback: (...data: unknown[]) => void): () => void {
  if (typeof window === 'undefined' || !window.runtime) {
    // No-op unsubscribe so hooks can subscribe unconditionally without
    // throwing in environments where the Wails runtime is absent (vitest, SSR).
    return () => {}
  }
  const rt = getRuntime()
  return rt.EventsOn(eventName, callback)
}

/** Emit a Wails event */
export function emit(eventName: string, data?: unknown): void {
  const rt = getRuntime()
  if (data !== undefined) {
    rt.EventsEmit(eventName, data)
  } else {
    rt.EventsEmit(eventName)
  }
}

/** Copy text to the system clipboard via the native Wails runtime.
 *
 *  Use this INSTEAD of `navigator.clipboard.writeText`. The Web Clipboard API
 *  is unreliable inside the Wails webview: `navigator.clipboard` is `undefined`
 *  in production builds (the webview origin is not a secure context — see
 *  wailsapp/wails#1670) and, even when present, `writeText` rejects with
 *  `NotAllowedError` under WKWebView's strict transient-activation rules for
 *  clipboard writes triggered from context-menu item clicks. The native
 *  runtime writes through Go and is unaffected by those restrictions, so it
 *  works consistently for every entry. */
export function clipboardSetText(text: string): Promise<boolean> {
  const rt = getRuntime()
  return rt.ClipboardSetText(text)
}

/** Open a URL in the user's default system browser.
 *
 *  Use this INSTEAD of relying on `<a target="_blank">` navigation. The Wails
 *  webview has no concept of a "default browser": a plain anchor with
 *  `target="_blank"` is either silently ignored or opens inside the webview
 *  itself (which cannot render arbitrary web pages). The native runtime
 *  dispatches the URL to the OS — `open` on macOS, `xdg-open` on Linux, and
 *  the default protocol handler on Windows — so external links open in the
 *  system browser consistently across all three platforms. */
export function openExternalURL(url: string): void {
  const rt = getRuntime()
  rt.BrowserOpenURL(url)
}

/**
 * Subscribe to a typed session-scoped event.
 * Auto-prefixes with `session:${sessionId}:${event}`.
 * Returns an unsubscribe function.
 *
 * For events with non-void payloads, the callback receives the raw data
 * from the backend. Callers MUST validate with the matching `is*Data`
 * guard from `@/types/events` before accessing properties.
 * See useChatEvents.ts for the correct pattern.
 *
 * Null/undefined payloads are filtered at this boundary so handlers
 * don't need to guard against missing data from backend schema drift.
 */
export function onSessionEvent<K extends SessionEventKey>(
  sessionId: string,
  event: K,
  callback: (data: SessionEventMap[K] | undefined) => void,
): () => void {
  const eventName = `session:${sessionId}:${event}`
  return subscribe(eventName, (data: unknown) => {
    // Filter null/undefined payloads at the boundary to protect against
    // backend schema drift or malformed emissions.
    if (data === null || data === undefined) {
      callback(undefined)
      return
    }
    callback(data as SessionEventMap[K])
  })
}

/**
 * Subscribe to a typed global event (non-session-scoped).
 * Returns an unsubscribe function.
 *
 * For events with non-void payloads, the callback receives the raw data
 * from the backend. Callers MUST validate with the matching `is*` guard
 * from `@/types/events` before accessing properties.
 */
export function onGlobalEvent<K extends GlobalEventKey>(
  event: K,
  callback: (data: GlobalEventMap[K] | undefined) => void,
): () => void {
  return subscribe(event, (data: unknown) => {
    if (data === null || data === undefined) {
      callback(undefined)
      return
    }
    callback(data as GlobalEventMap[K])
  })
}
