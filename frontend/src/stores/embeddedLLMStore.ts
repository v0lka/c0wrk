// Embedded local-model UI state.
//
// The single source of truth for the two embedded-LLM surfaces: the Settings
// block (install / per-component progress / remove / load-unload / auto-unload /
// the informational packing label) and the status-bar indicator. The store holds
// exactly what those surfaces render. This file is PURE state + selectors: the
// RPC side effects live in @/api/embedded / @/api/embeddedTuning and reach the
// store through the module-level sync functions in ./embeddedLLMSync
// (`refreshEmbeddedLLMStatus`, `refreshEmbeddedLLMTuning`,
// `runEmbeddedLLMAction`, `subscribeEmbeddedLLMEvents`), following the
// paperStore convention so the zustand reducers stay pure and trivially
// testable. Those four are re-exported at the bottom, so the historical import
// path (`@/stores/embeddedLLMStore`) keeps working for every consumer.
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
// One deliberate cross-store side effect belongs to this store's sync module —
// `refreshEmbeddedLLMStatus` in ./embeddedLLMSync, NOT a reducer, so the
// reducers below stay pure: an install/remove transition changes which
// providers `GetConfig` reports, so the shared `useConfigData` model cache (the
// chat toolbar picker's source) is invalidated there. That function's comment
// explains why load/unload is excluded.

import { create } from 'zustand'
import type { EmbeddedLLMStatus } from '@/api/embedded'
import type { EmbeddedLLMTuning } from '@/api/embeddedTuning'
import type { EmbeddedLLMComponent, EmbeddedLLMInstallProgressData } from '@/types/events'

export type { EmbeddedLLMStatus } from '@/api/embedded'
export type { EmbeddedLLMTuning } from '@/api/embeddedTuning'

/** The mutating RPC currently in flight (null = idle). Drives button disabling
 *  and the spinner. `load` is the long one: LoadEmbeddedLLM blocks for the whole
 *  multi-gigabyte weight load; `tuning` is a quick config write whose re-read
 *  still has to settle before the controls un-disable; `cancel` is the install
 *  stop request — a fast RPC whose window covers only the request + read-back
 *  (the run's actual unwind is asynchronous and reported by the state event). */
export type EmbeddedLLMBusyAction =
  | 'install'
  | 'remove'
  | 'load'
  | 'unload'
  | 'auto-unload'
  | 'tuning'
  | 'cancel'

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
  /** True while the FIRST status read is in flight (skeleton/spinner).
   *  `refreshEmbeddedLLMStatus` arms it only while `status === null`: an
   *  event-driven re-read or a post-RPC read-back refreshes the snapshot in
   *  place and must NOT re-raise it, or every helper text rendered from this
   *  flag blinks on each refresh. The in-flight window of an action is `busy`. */
  statusLoading: boolean
  /** True while a background install run is in flight. */
  installing: boolean
  /** Per-component install progress (empty unless `installing`). */
  progress: EmbeddedLLMProgressByComponent
  /** The mutating RPC in flight, if any. Named by the FIRST action of an
   *  overlapping set and cleared only when the last one has read back — see
   *  `runEmbeddedLLMAction`, the sole owner. */
  busy: EmbeddedLLMBusyAction | null
  /** The message of the last FAILED action taken from this UI (null = none).
   *  Distinct from `status.error`, which is the backend-reported cause of the
   *  last failed install/removal or of the supervisor's error state. */
  error: string | null
  /** The persisted tuning overrides (null = never read). Replaced WHOLE by
   *  `setTuning` — the one writer is `refreshEmbeddedLLMTuning`, exactly the
   *  `status`/`setStatus` authority model: a partial merge could render a
   *  half-updated override set, and this UI is the only tuning writer, so the
   *  shared `embedded_llm:state` invalidation (which re-reads the STATUS)
   *  cannot leave the slice stale. */
  tuning: EmbeddedLLMTuning | null
  /** True while a tuning read is in flight — the first one AND each post-commit
   *  re-read (`refreshEmbeddedLLMTuning` arms it on every read, unlike
   *  `statusLoading`). Consumed by the tuning controls' `disabled` derivation:
   *  until the snapshot lands every knob renders its fallback, so a commit in
   *  that window would persist a patch derived from values the user never saw.
   *  The sync module counts the pending reads, so an OVERLAPPING pair of commits
   *  keeps it set until the LAST snapshot has landed — one early clear would
   *  re-enable the controls against a snapshot the sibling write predates. */
  tuningLoading: boolean
}

interface EmbeddedLLMActions {
  /** Replace the snapshot wholesale (a `GetEmbeddedLLMStatus` read). */
  setStatus: (status: EmbeddedLLMStatus) => void
  /** Toggle the FIRST-read spinner (see the `statusLoading` field). */
  setStatusLoading: (loading: boolean) => void
  /** Mark a background install run as started: raise the flag, drop the bars of
   *  any previous run and clear the previous failure. `busy` is deliberately
   *  NOT touched — `runEmbeddedLLMAction` owns it end-to-end, and the install
   *  flow calls this from INSIDE its own busy window, so clearing it here would
   *  re-enable every control before the read-back snapshot lands. */
  beginInstall: () => void
  /** One component's one stage of a live install run. */
  applyProgress: (data: EmbeddedLLMInstallProgressData) => void
  /** Mark the mutating RPC in flight (null clears it). */
  setBusy: (action: EmbeddedLLMBusyAction | null) => void
  /** Record/clear the message of a failed action taken from this UI. */
  setError: (message: string | null) => void
  /** Replace the tuning snapshot wholesale (a `GetEmbeddedLLMTuning` read). */
  setTuning: (tuning: EmbeddedLLMTuning) => void
  /** Toggle the tuning-read flag (see the `tuningLoading` field). */
  setTuningLoading: (loading: boolean) => void
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
  tuning: null,
  tuningLoading: false,
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

  beginInstall: () => set({ installing: true, progress: {}, error: null }),

  applyProgress: (data) =>
    set((s) => ({
      installing: true,
      error: null,
      progress: { ...s.progress, [data.component]: data },
    })),

  setBusy: (action) => set({ busy: action }),

  setError: (message) => set({ error: message }),

  setTuning: (tuning) => set({ tuning, tuningLoading: false }),

  setTuningLoading: (loading) => set({ tuningLoading: loading }),

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

/** Whether the FIRST status read is in flight (never re-raised by a later
 *  re-read). Primitive — safe. */
export function useEmbeddedLLMStatusLoading(): boolean {
  return useEmbeddedLLMStore((s) => s.statusLoading)
}

/** The persisted tuning overrides, or null before the first read. Direct store
 *  reference — safe. */
export function useEmbeddedLLMTuningSnapshot(): EmbeddedLLMTuning | null {
  return useEmbeddedLLMStore((s) => s.tuning)
}

/** Whether a tuning read is in flight. Primitive — safe. */
export function useEmbeddedLLMTuningLoading(): boolean {
  return useEmbeddedLLMStore((s) => s.tuningLoading)
}

// --- Backend sync ---
//
// The four module-level sync functions (the two authoritative re-reads, the one
// shared mutating runner, the refcounted event subscription) live in
// ./embeddedLLMSync so this file stays pure state + selectors. They are
// re-exported here so every existing consumer keeps importing them from
// `@/stores/embeddedLLMStore`.
export {
  refreshEmbeddedLLMStatus,
  refreshEmbeddedLLMTuning,
  runEmbeddedLLMAction,
  subscribeEmbeddedLLMEvents,
} from './embeddedLLMSync'
