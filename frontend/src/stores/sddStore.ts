// The SDD framework context of the active project — WHICH SDD frameworks are
// installed, WHICH one the operator chose, and the selector's optimistic state.
//
// Two surfaces share it:
//
//   1. the workspace tab strip (WorkspacePanel) — the Specs tab exists only
//      when the sidebar gate is on AND at least one framework is DETECTED, so
//      visibility must be answerable BEFORE the panel mounts (a mount-driven
//      fetch would never run for a hidden tab);
//   2. the panel's framework selector — which has to show the operator's
//      choice IMMEDIATELY while the re-parse it triggers is still in flight.
//
// The parse itself stays in specsStore (one RPC, one authority): specsStore
// pushes each successful status through `applyStatus`, so this store holds
// REFERENCES from the very payload the panel renders — never a second copy of
// the tree and never a second GetSpecsStatus call.
//
// No import of specsStore here: the dependency runs one way
// (specsStore → sddStore) so the switch orchestration — persist the selection,
// then re-parse — has exactly one owner and no cycle.

import { create } from 'zustand'
import type { SDDFramework, SpecsStatus } from '@/types/models'

interface SddState {
  /** The project this framework context belongs to; null before the first
   *  status lands. */
  projectId: string | null
  /** Detected frameworks of that project, in detection default order (the
   *  payload's own slice, so selectors stay referentially stable). */
  frameworks: SDDFramework[]
  /** The selection to RENDER: the optimistic pending id while a switch is in
   *  flight, otherwise the last server-confirmed one. "" when none is
   *  active. */
  activeId: string
  /** The last server-confirmed selection (the payload's `active_id`) — what
   *  a failed switch reverts to. */
  confirmedActiveId: string
  /** More than one framework detected and no persisted choice yet. */
  selectorNeeded: boolean
  /** The payload's visibility answer: gate on AND at least one framework
   *  detected. */
  present: boolean
  /** Non-fatal backend diagnostics (operator DSL faults, read warnings). */
  warnings: string[]
  /** True once a status has been applied for `projectId` — the tab strip's
   *  tri-state: before the first answer the surface cannot be ruled out (the
   *  panel's own parse is what discovers detection), after it `present` is
   *  authoritative. */
  known: boolean
  /** The selection awaiting server confirmation; null when settled. */
  pendingActiveId: string | null
  /** Monotonic ticket on every selection attempt: a superseded attempt's
   *  failure never reverts a newer one. */
  switchSeq: number
  /** A framework switch (persist + re-parse) is in flight. */
  switching: boolean
  /** Last switch error; the panel renders it in its own banner. */
  error: string | null
}

interface SddActions {
  /** Fold one successful status fetch into the framework context. A payload
   *  for a DIFFERENT project replaces the context outright (a project switch
   *  never inherits the previous project's selection); a payload whose
   *  `active_id` matches the pending choice settles that choice. */
  applyStatus: (projectId: string, status: SpecsStatus) => void
  /** Start a switch: render the chosen framework at once, keep the previous
   *  parse on screen until the re-parse lands (the store's LWW ticket owns
   *  that window; this is intent only). */
  beginSwitch: (id: string) => number
  /** Abandon the switch `seq` started: revert to the confirmed selection and
   *  surface the failure. A superseded attempt is a no-op. */
  failSwitch: (seq: number, message: string) => void
  /** Settle the switch `seq` started once its re-parse finished — SUCCESS OR
   *  FAILURE. The switch is the persist + re-parse pair: a re-parse that
   *  ended in an error has still ENDED, and the selector must unlock (the
   *  error banner carries the message). A superseded attempt is a no-op. */
  endSwitch: (seq: number) => void
  /** Clear everything (project switch away, tests). */
  reset: () => void
}

export type SddStore = SddState & SddActions

const initialState: SddState = {
  projectId: null,
  frameworks: [],
  activeId: '',
  confirmedActiveId: '',
  selectorNeeded: false,
  present: false,
  warnings: [],
  known: false,
  pendingActiveId: null,
  switchSeq: 0,
  switching: false,
  error: null,
}

export const useSddStore = create<SddStore>()((set, get) => ({
  ...initialState,

  applyStatus: (projectId, status) => {
    const previous = get()
    const serverActive = status.active_id ?? ''
    // A payload for another project starts a fresh context: nothing of the
    // previous project's frameworks, selection or pending intent survives it.
    if (previous.projectId !== projectId) {
      set({
        projectId,
        frameworks: status.detected,
        activeId: serverActive,
        confirmedActiveId: serverActive,
        selectorNeeded: status.selector_needed,
        present: status.present,
        warnings: status.warnings,
        known: true,
        pendingActiveId: null,
        switching: false,
        error: null,
      })
      return
    }
    // Same project: the pending choice settles as soon as the server reports
    // it; until then the selector keeps showing the operator's intent.
    const target = previous.pendingActiveId
    const settled = target === null || target === serverActive
    set({
      frameworks: status.detected,
      activeId: settled ? serverActive : target,
      confirmedActiveId: serverActive,
      selectorNeeded: status.selector_needed,
      present: status.present,
      warnings: status.warnings,
      known: true,
      pendingActiveId: settled ? null : target,
      switching: settled ? false : previous.switching,
      error: null,
    })
  },

  beginSwitch: (id) => {
    const seq = get().switchSeq + 1
    set({
      switchSeq: seq,
      pendingActiveId: id,
      activeId: id,
      switching: true,
      error: null,
    })
    return seq
  },

  failSwitch: (seq, message) => {
    if (get().switchSeq !== seq) return
    set({
      activeId: get().confirmedActiveId,
      pendingActiveId: null,
      switching: false,
      error: message,
    })
  },

  endSwitch: (seq) => {
    if (get().switchSeq !== seq) return
    set({
      // The re-parse already applied whatever it could (applyStatus settles
      // the pending id against the server's answer); a failed re-parse keeps
      // the server's previous answer and just unlocks the selector.
      activeId: get().pendingActiveId ?? get().activeId,
      pendingActiveId: null,
      switching: false,
    })
  },

  reset: () => set(initialState),
}))
