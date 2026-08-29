// Close-guard modal state (the intercepted-quit confirmation dialog).
//
// State lives in a store rather than hook-local useState on purpose: the
// dialog is mounted per app phase (splash / waiting_ready / main branches in
// App.tsx), and a phase transition remounts the component — local state
// would dismiss an unanswered quit confirmation exactly when the backend
// becomes ready. The store survives remounts; `useExitGuard` (App root) is
// the only writer via the app:exit_requested subscription, and
// ExitConfirmDialog is a pure view over it.
//
// Referential stability (React 19 useSyncExternalStore): selectors must
// return primitives or direct store properties. `sessions` is replaced by
// value on each `present` call, never mutated in place.

import { create } from 'zustand'
import type { ExitRequestedSession } from '@/types/events'

export interface ExitGuardState {
  /** Whether the confirmation modal is shown. */
  open: boolean
  /** Sessions with live work, as reported by the backend. An empty list
   *  with `open === true` means the payload failed validation and the
   *  modal renders its generic list-less variant. */
  sessions: readonly ExitRequestedSession[]
  /** True when the intercepted quit belongs to a pending self-update
   *  (ApplyUpdate) — the modal presents restart context. */
  updatePending: boolean
}

export interface ExitGuardActions {
  /** Record an intercepted quit (valid payload): show the modal with the
   *  active-session list. A repeated event while open replaces the list. */
  present: (sessions: readonly ExitRequestedSession[], updatePending: boolean) => void
  /** Show the generic list-less variant — the payload could not be
   *  trusted, but the quit was already prevented and must stay answerable. */
  presentUnknown: () => void
  /** Dismiss the modal (user cancelled). The backend guard stays armed, so
   *  a later quit attempt re-opens it. */
  clear: () => void
}

export type ExitGuardStore = ExitGuardState & ExitGuardActions

const initialState: ExitGuardState = {
  open: false,
  sessions: [],
  updatePending: false,
}

export const useExitGuardStore = create<ExitGuardStore>()((set) => ({
  ...initialState,

  present: (sessions, updatePending) =>
    set({ open: true, sessions, updatePending }),

  presentUnknown: () => set({ open: true, sessions: [], updatePending: false }),

  clear: () => set(initialState),
}))
