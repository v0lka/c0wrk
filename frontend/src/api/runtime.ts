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
      WindowSetTitle(title: string): void
      LogError(message: string): void
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
  WindowSetTitle(title: string): void
  LogError(message: string): void
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

/**
 * Wails event name under which the backend delivers BATCHED frontend events.
 * MUST match eventBatchEventName in desktop/event_batcher.go. The desktop layer
 * coalesces transient streaming events and drains the rest as one envelope per
 * ~16ms flush, so the AppKit main thread performs one evaluateJavaScript call
 * per flush instead of one per event. The payload is a BatchEnvelope:
 * `{events: [{name, args}, ...]}`.
 */
export const EVENT_BATCH_NAME = 'c0wrk:events:batch'

interface BatchEnvelopeEntry {
  name: string
  args?: unknown[]
}

interface BatchEnvelope {
  events?: BatchEnvelopeEntry[]
}

/**
 * Per-event callback registry. Every `subscribe` callback is registered here so
 * an event arriving inside a batch envelope can be fanned out to the exact same
 * handlers — preserving per-event semantics (order, payload, null filtering)
 * while collapsing the transport to one call per flush.
 */
const batchListeners = new Map<string, Set<(...data: unknown[]) => void>>()
let batchListenerInstalled = false

function dispatchBatchedEvent(name: string, args: unknown[]): void {
  const handlers = batchListeners.get(name)
  if (!handlers || handlers.size === 0) return
  // Snapshot: a handler may unsubscribe (session switch) during iteration.
  for (const handler of Array.from(handlers)) {
    try {
      handler(...args)
    } catch (err) {
      logger.warn(`[events] handler for batched event "${name}" threw`, err)
    }
  }
}

/** Install the single envelope listener the first time anything subscribes. */
function installBatchListener(): void {
  if (batchListenerInstalled) return
  if (typeof window === 'undefined' || !window.runtime) return
  batchListenerInstalled = true
  window.runtime.EventsOn(EVENT_BATCH_NAME, (...data: unknown[]) => {
    const envelope = data[0] as BatchEnvelope | undefined
    if (!envelope || !Array.isArray(envelope.events)) {
      logger.warn('[events] dropped malformed batch envelope', envelope)
      return
    }
    for (const entry of envelope.events) {
      if (!entry || typeof entry.name !== 'string') continue
      dispatchBatchedEvent(entry.name, entry.args ?? [])
    }
  })
}

/** Subscribe to a Wails event; returns an unsubscribe function */
export function subscribe(eventName: string, callback: (...data: unknown[]) => void): () => void {
  if (typeof window === 'undefined' || !window.runtime) {
    // No-op unsubscribe so hooks can subscribe unconditionally without
    // throwing in environments where the Wails runtime is absent (vitest, SSR).
    return () => {}
  }
  const rt = getRuntime()

  // Register in the fan-out registry so events delivered inside a
  // c0wrk:events:batch envelope reach this callback, and make sure the single
  // envelope listener exists.
  let handlers = batchListeners.get(eventName)
  if (!handlers) {
    handlers = new Set()
    batchListeners.set(eventName, handlers)
  }
  handlers.add(callback)
  installBatchListener()

  // Also register directly with the Wails runtime: individually-emitted events
  // (every non-batched global event) still reach the callback. Batched session
  // events are never emitted individually, so a callback fires exactly once per
  // event — never twice.
  const unsubscribeDirect = rt.EventsOn(eventName, callback)
  return () => {
    handlers?.delete(callback)
    unsubscribeDirect()
  }
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

/** Set the native window title.
 *
 *  Unlike the other wrappers here this one NO-OPS when the runtime is absent
 *  instead of throwing, mirroring `subscribe`: the title is a purely cosmetic
 *  side effect driven from a `useEffect`, and every hook test runs under
 *  jsdom without `window.runtime`. A throw would turn a decorative update
 *  into a render-time failure. */
export function setWindowTitle(title: string): void {
  if (typeof window === 'undefined' || !window.runtime) return
  getRuntime().WindowSetTitle(title)
}

/** Forward a diagnostic message to the Go process log (`wails.log`).
 *
 *  Mirrors the Wails runtime's `LogError`, which routes through the Go
 *  `logger.Logger` wired into `wails.Run` (`Logger: wlog`, see main.go →
 *  desktop/wails_logger.go) and lands in a persistent `<logDir>/wails.log`.
 *  The webview console is NOT persisted in the packaged app (WebKitGTK writes
 *  no console output to disk), so a crash that only reaches `console.error`
 *  leaves no trace once the process exits — this is the channel that survives.
 *
 *  Unlike the RPC/runtime wrappers that throw, this NO-OPS when the runtime is
 *  absent (vitest, SSR), mirroring `setWindowTitle`: a diagnostics call made
 *  from an error path must never itself turn into a failure. */
export function logError(message: string): void {
  if (typeof window === 'undefined' || !window.runtime) return
  getRuntime().LogError(message)
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

/** Confirm quitting the app despite active sessions (close guard).
 *
 * Counterpart to the global `app:exit_requested` event: the backend's
 * OnBeforeClose guard intercepts a quit while sessions have live work, emits
 * that event, and waits for the user's decision. This RPC arms the backend
 * exit-confirmed flag and re-issues the graceful quit so the normal Shutdown
 * sequence still runs. */
export async function confirmExit(): Promise<void> {
  const app = getApp()
  await app.ConfirmExit()
}
