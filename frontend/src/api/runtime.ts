// Wails runtime wrapper — single access point for window.go and window.runtime

import type { SessionEventKey, SessionEventMap } from '@/types/events'

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
    }
  }
}

/** Check if the Wails runtime is available */
export function isWailsReady(): boolean {
  return typeof window !== 'undefined' && !!window.runtime && !!window.go?.desktop?.App
}

/** Get the Wails runtime; throws if not ready */
export function getRuntime() {
  if (typeof window === 'undefined' || !window.runtime) {
    throw new Error('Wails runtime is not available')
  }
  return window.runtime
}

/**
 * Get the desktop App RPC proxy; throws if not ready.
 * Returns a loosely-typed proxy — each API module casts the result.
 */
// eslint-disable-next-line @typescript-eslint/no-explicit-any
export function getApp(): any {
  if (typeof window === 'undefined' || !window.go?.desktop?.App) {
    throw new Error('Wails App bindings are not available')
  }
  // eslint-disable-next-line @typescript-eslint/no-explicit-any
  return window.go.desktop.App as any
}

/** Subscribe to a Wails event; returns an unsubscribe function */
export function subscribe(eventName: string, callback: (...data: unknown[]) => void): () => void {
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
  callback: (data: SessionEventMap[K]) => void,
): () => void {
  const eventName = `session:${sessionId}:${event}`
  return subscribe(eventName, (data: unknown) => {
    // Filter null/undefined payloads at the boundary to protect against
    // backend schema drift or malformed emissions.
    if (data === null || data === undefined) {
      callback(undefined as SessionEventMap[K])
      return
    }
    callback(data as SessionEventMap[K])
  })
}
