// Embedded local-model UI state.
//
// The single source of truth for the two embedded-LLM surfaces: the Settings
// block (install / per-component progress / remove / load-unload / auto-unload /
// the informational packing label) and the status-bar indicator. The store holds
// exactly what those surfaces render; RPC side effects live in @/api/embedded and
// reach the store through the module-level functions at the bottom
// (`refreshEmbeddedLLMStatus`, `subscribeEmbeddedLLMEvents`), following the
// paperStore convention so the zustand reducer stays pure and trivially testable.
//
// Authority model — deliberately one writer per field, no parallel bookkeeping:
//   - `status` is the authoritative snapshot and is replaced WHOLE by
//     `setStatus` (a `GetEmbeddedLLMStatus` read). The `embedded_llm:state`
//     event is treated as an invalidation trigger, not as a partial patch: the
//     event payload is narrower than the status (no `installing`, no
//     `auto_unload_enabled`, no provider identity), so merging it would keep two
//     half-truths alive. A transition is rare (install end, load start/end,
//     unload, auto-unload change, process death) and the read is local and
//     network-free, so re-reading is both cheaper to reason about and exact.
//   - `installing` means "a background install run is in flight, per the latest
//     information": `setStatus` writes the backend's authoritative flag (and
//     drops the progress bars with it), while `applyProgress`/`beginInstall`
//     raise it — an install-progress event IS the proof of a live run, and it
//     can arrive before a stale snapshot is refreshed.
//   - `progress` is the per-component latest stage/bytes, keyed by component so
//     each artifact keeps its OWN bar (the transport never aggregates them).
//
// Selector stability (React 19 useSyncExternalStore, error #185): every selector
// below returns a PRIMITIVE or a DIRECT store reference — never a freshly
// allocated object/array. Derivations (the ordered progress rows, the display
// error) happen in the consumer's `useMemo`.
//
// One deliberate cross-store side effect lives in `refreshEmbeddedLLMStatus` —
// in the module-level sync function, NOT in a reducer, so the reducers above
// stay pure: an install/remove transition changes which providers `GetConfig`
// reports, so the shared `useConfigData` model cache (the chat toolbar picker's
// source) is invalidated there. That function's comment explains why load/unload
// is excluded.

import { create } from 'zustand'
import {
  getEmbeddedLLMStatus,
  onEmbeddedLLMInstallProgress,
  onEmbeddedLLMState,
  type EmbeddedLLMStatus,
} from '@/api/embedded'
import type { EmbeddedLLMComponent, EmbeddedLLMInstallProgressData } from '@/types/events'
import { invalidateConfigCache } from '@/hooks/useConfigData'
import { logger } from '@/lib/logger'

export type { EmbeddedLLMStatus } from '@/api/embedded'

/** The mutating RPC currently in flight (null = idle). Drives button disabling
 *  and the spinner. `load` is the long one: LoadEmbeddedLLM blocks for the whole
 *  multi-gigabyte weight load. */
export type EmbeddedLLMBusyAction = 'install' | 'remove' | 'load' | 'unload' | 'auto-unload'

/** The latest progress payload per component. A component that never reported
 *  (e.g. `cudart` outside Windows CUDA) is simply absent — the UI must not
 *  invent a pending row for an artifact this install will never fetch. */
export type EmbeddedLLMProgressByComponent = Readonly<
  Partial<Record<EmbeddedLLMComponent, EmbeddedLLMInstallProgressData>>
>

// --- State types ---

interface EmbeddedLLMState {
  /** The last authoritative status snapshot (null = never read). */
  status: EmbeddedLLMStatus | null
  /** True while the first status read is in flight (skeleton/spinner). */
  statusLoading: boolean
  /** True while a background install run is in flight. */
  installing: boolean
  /** Per-component install progress (empty unless `installing`). */
  progress: EmbeddedLLMProgressByComponent
  /** The mutating RPC in flight, if any. */
  busy: EmbeddedLLMBusyAction | null
  /** The message of the last FAILED action taken from this UI (null = none).
   *  Distinct from `status.error`, which is the backend-reported cause of the
   *  last failed install/removal or of the supervisor's error state. */
  error: string | null
}

interface EmbeddedLLMActions {
  /** Replace the snapshot wholesale (a `GetEmbeddedLLMStatus` read). */
  setStatus: (status: EmbeddedLLMStatus) => void
  /** Toggle the initial-read spinner. */
  setStatusLoading: (loading: boolean) => void
  /** Mark a background install run as started: raise the flag, drop the bars of
   *  any previous run and clear the previous failure. */
  beginInstall: () => void
  /** One component's one stage of a live install run. */
  applyProgress: (data: EmbeddedLLMInstallProgressData) => void
  /** Mark the mutating RPC in flight (null clears it). */
  setBusy: (action: EmbeddedLLMBusyAction | null) => void
  /** Record/clear the message of a failed action taken from this UI. */
  setError: (message: string | null) => void
  /** Drop every local field (used by tests and by an app-level teardown). */
  reset: () => void
}

export type EmbeddedLLMStore = EmbeddedLLMState & EmbeddedLLMActions

// --- Defaults ---

const initialState: EmbeddedLLMState = {
  status: null,
  statusLoading: false,
  installing: false,
  progress: {},
  busy: null,
  error: null,
}

// --- Store ---

export const useEmbeddedLLMStore = create<EmbeddedLLMStore>()((set) => ({
  ...initialState,

  setStatus: (status) =>
    set((s) => ({
      status,
      installing: status.installing,
      // A snapshot that reports no live run also retires its bars — but the
      // empty map keeps its identity so an unrelated refresh does not re-render
      // the progress subscriber.
      progress: !status.installing && Object.keys(s.progress).length > 0 ? {} : s.progress,
      statusLoading: false,
    })),

  setStatusLoading: (loading) => set({ statusLoading: loading }),

  beginInstall: () => set({ installing: true, progress: {}, error: null, busy: null }),

  applyProgress: (data) =>
    set((s) => ({
      installing: true,
      error: null,
      progress: { ...s.progress, [data.component]: data },
    })),

  setBusy: (action) => set({ busy: action }),

  setError: (message) => set({ error: message }),

  reset: () => set({ ...initialState }),
}))

// --- Selector hooks (each subscribes to one primitive or one direct ref) ---

/** The authoritative status snapshot, or null before the first read. Direct
 *  store reference — safe. */
export function useEmbeddedLLMStatus(): EmbeddedLLMStatus | null {
  return useEmbeddedLLMStore((s) => s.status)
}

/** Whether a background install run is in flight. Primitive — safe. */
export function useEmbeddedLLMInstalling(): boolean {
  return useEmbeddedLLMStore((s) => s.installing)
}

/** The per-component progress map. Direct store reference — safe. Order and
 *  filtering are the consumer's `useMemo` (see EmbeddedLLMSettings). */
export function useEmbeddedLLMProgress(): EmbeddedLLMProgressByComponent {
  return useEmbeddedLLMStore((s) => s.progress)
}

/** The mutating RPC in flight. Primitive (string | null) — safe. */
export function useEmbeddedLLMBusy(): EmbeddedLLMBusyAction | null {
  return useEmbeddedLLMStore((s) => s.busy)
}

/** The message of the last failed local action. Primitive — safe. */
export function useEmbeddedLLMError(): string | null {
  return useEmbeddedLLMStore((s) => s.error)
}

/** Whether the first status read is in flight. Primitive — safe. */
export function useEmbeddedLLMStatusLoading(): boolean {
  return useEmbeddedLLMStore((s) => s.statusLoading)
}

// --- Backend sync (module-level functions, not actions) ---

/** Re-read the authoritative snapshot. NEVER throws: an unavailable backend
 *  (no Wails bindings — dev-frontend, vitest) or a schema drift is logged and
 *  leaves the previous snapshot in place, so a status surface degrades to "not
 *  installed" instead of unmounting. Returns whether the snapshot was applied. */
export async function refreshEmbeddedLLMStatus(): Promise<boolean> {
  const { setStatusLoading, setStatus } = useEmbeddedLLMStore.getState()
  setStatusLoading(true)
  try {
    const status = await getEmbeddedLLMStatus()
    const prev = useEmbeddedLLMStore.getState().status
    setStatus(status)
    // The install state decides whether the local model EXISTS as a provider at
    // all: the backend generates `llm.openai_compatible.embedded` from it, so an
    // install/remove transition changes `GetConfig().llm.all_models` — the list
    // the chat toolbar's ModelCombobox renders. That list is served from
    // useConfigData's module-level cache, which no other embedded path
    // invalidates (the settings dialog re-reads config on every open, which is
    // why only the chat picker went stale after an install). Invalidate on the
    // transition ONLY: load/unload/auto-unload changes neither add nor remove a
    // selectable model — an unloaded model stays listed, since the first request
    // to it loads it — so invalidating there would refetch config on every load
    // for nothing. The FIRST read (prev === null) is skipped deliberately: at
    // startup the config cache is fetched after the backend has already synced
    // the provider, so there is no stale entry to drop.
    if (prev !== null && prev.installed !== status.installed) {
      invalidateConfigCache()
    }
    return true
  } catch (err) {
    // A failed READ is not a failed action: it must not paint the action-error
    // line (which the user reads as "my install broke"). Log, drop the spinner
    // and leave the previous snapshot in place.
    logger.warn('[embedded-llm] status read failed', err)
    setStatusLoading(false)
    return false
  }
}

/** Live subscription count. Both embedded-LLM surfaces (the Settings block and
 *  the status-bar indicator) can be mounted at once; one shared Wails
 *  subscription keeps a single event from being applied twice. */
let eventSubscribers = 0
let eventUnsubscribe: (() => void) | null = null

/** Subscribe to the two global `embedded_llm:*` events and feed the store.
 *
 *  Refcounted: the returned unsubscribe tears the shared Wails subscription down
 *  only when the LAST consumer goes away, and is idempotent (a double call from
 *  a StrictMode effect cannot drop another consumer's subscription). Safe to
 *  call when the runtime is absent — @/api/runtime's `subscribe` no-ops there. */
export function subscribeEmbeddedLLMEvents(): () => void {
  eventSubscribers += 1
  if (eventUnsubscribe === null) {
    const offState = onEmbeddedLLMState(() => {
      // A transition is an invalidation, not a patch: re-read the authoritative
      // snapshot (which also carries the fields the event payload omits).
      void refreshEmbeddedLLMStatus()
    })
    const offProgress = onEmbeddedLLMInstallProgress((data) => {
      useEmbeddedLLMStore.getState().applyProgress(data)
    })
    eventUnsubscribe = () => {
      offState()
      offProgress()
    }
  }

  let released = false
  return () => {
    if (released) return
    released = true
    eventSubscribers -= 1
    if (eventSubscribers > 0 || eventUnsubscribe === null) return
    eventUnsubscribe()
    eventUnsubscribe = null
  }
}
