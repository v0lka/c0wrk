// Crash diagnostics bridge — persists a structured dump of every React render
// crash caught by an ErrorBoundary to BOTH delivery channels the desktop app
// offers, so a crash stays diagnosable even when the webview console is gone:
//   1. logError() → the Wails runtime → the Go logger → <logDir>/wails.log
//      (append-only, survives app restarts)
//   2. localStorage key `c0wrk-crash-dump` (bounded ring: last 5 dumps)
//
// Why this exists: the webview console (logger.error → console.error) is NOT
// persistent in the packaged app — WebKitGTK writes no console output to any
// file — so a crash caught by a boundary leaves no trace unless it is actively
// pushed to the Go process. This module is that push.
//
// Everything here is best-effort: each step is wrapped so a broken diagnostics
// path can never itself crash the crash reporting.

import { logError } from '@/api/runtime'

const DUMP_STORAGE_KEY = 'c0wrk-crash-dump'
const MAX_DUMPS = 5
const INPUT_MODE_STORAGE_KEY = 'c0wrk-input-mode'

/** Serialize an unknown thrown value with a best-effort stack. */
function serializeError(err: unknown): { name: string; message: string; stack: string | null } {
  if (err instanceof Error) {
    return {
      name: err.name,
      message: err.message,
      stack: typeof err.stack === 'string' ? err.stack : null,
    }
  }
  // React can throw non-Error values (strings, objects) from render.
  return {
    name: typeof err,
    message: String(err),
    stack: null,
  }
}

/**
 * Input-shell state snapshot relevant to input/terminal crashes: the persisted
 * input mode gates the input shell's render and is exactly the data that
 * reproduces a crash after a restart. Read straight from localStorage rather
 * than the zustand store so the crash path never imports a store (avoiding an
 * import cycle store → diagnostics → store inside the error handler).
 */
function inputStateSnapshot(): Record<string, unknown> {
  const snapshot: Record<string, unknown> = {
    href: typeof location !== 'undefined' ? location.href : null,
  }
  try {
    snapshot.c0wrk_input_mode = localStorage.getItem(INPUT_MODE_STORAGE_KEY)
  } catch {
    snapshot.c0wrk_input_mode = null
  }
  return snapshot
}

/** Append a dump to the bounded localStorage ring (older entries dropped). */
function appendToLocalStorage(dump: Record<string, unknown>): void {
  try {
    const existing = localStorage.getItem(DUMP_STORAGE_KEY)
    let list: unknown[] = []
    if (existing) {
      try {
        const parsed = JSON.parse(existing)
        if (Array.isArray(parsed)) list = parsed
      } catch {
        list = []
      }
    }
    list.push(dump)
    while (list.length > MAX_DUMPS) list.shift()
    localStorage.setItem(DUMP_STORAGE_KEY, JSON.stringify(list))
  } catch {
    // localStorage may be unavailable (quota, disabled) — the wails.log
    // channel is the primary one anyway.
  }
}

export interface CrashDiagnosticsOptions {
  /** Logical mount point of the boundary (e.g. 'input-shell'). */
  boundary?: string
  /** Extra context merged into the dump. */
  context?: Record<string, unknown>
}

/**
 * Report a React render crash. Call from ErrorBoundary.componentDidCatch (or
 * anywhere a crash must leave a persistent trace). Fire-and-forget: never
 * throws, never blocks.
 */
export function reportCrash(
  error: unknown,
  info?: { componentStack?: string | null },
  options?: CrashDiagnosticsOptions,
): void {
  const entry = {
    kind: 'ui-crash',
    boundary: options?.boundary ?? 'unknown',
    at: new Date().toISOString(),
    error: serializeError(error),
    componentStack: info?.componentStack ?? null,
    context: { ...(options?.context ?? {}), ...inputStateSnapshot() },
  }
  // Primary channel: the Go process log (wails.log on disk).
  try {
    logError(`[ui-crash] ${JSON.stringify(entry)}`)
  } catch {
    // Serialization of a circular context, or a dead runtime bridge — the
    // localStorage channel below still captures the dump.
  }
  // Secondary channel: bounded localStorage ring.
  appendToLocalStorage(entry)
  // Always keep the console copy too (devtools / `wails dev` console).
  console.error('[ui-crash]', entry)
}
