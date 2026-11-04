import { create } from 'zustand'
import { getSpecsStatus } from '@/api/specs'
import type { SpecsStatus } from '@/types/models'

// --- State types ---

interface SpecsState {
  /** Parsed specs/ status of the active project, or null when not yet
   *  loaded. present=false is a valid neutral state, not an absence of
   *  data. */
  status: SpecsStatus | null
  /** True while a GetSpecsStatus call is in flight. */
  isLoading: boolean
  /** Last error from a status fetch; null when clean. The previous status
   *  (if any) stays rendered alongside an error banner. */
  error: string | null
  /** The projectId the current `status` belongs to. Guards against stale
   *  data when the user switches projects before an in-flight fetch
   *  resolves. */
  projectId: string | null
  /** Monotonic sequence stamped on every fetchStatus START. A response
   *  applies only while the store's counter still equals the ticket
   *  captured at its start — last-write-wins by initiation order, so an
   *  older fetch that resolves after a newer one never overwrites it (the
   *  researchStore graphSyncSeq contract). */
  fetchSeq: number
}

interface SpecsActions {
  /** Fetch the specs/ status of a project (one light read-only parse) and
   *  store it, stamped with the projectId. Superseded responses (a newer
   *  fetch started while this one was in flight) are discarded; a failure
   *  stores its message in `error` and keeps the previous status. */
  fetchStatus: (projectId: string) => Promise<void>
  /** Set/clear the panel's error banner directly. The quick-actions
   *  dispatch path uses it to surface a failed spec-* skill send (the
   *  research store's setError contract — send() rethrows only when the
   *  auto-created session fails, the splash race). */
  setError: (error: string | null) => void
  /** Clear everything (e.g. tests, or a future project-switch hook). */
  reset: () => void
}

export type SpecsStore = SpecsState & SpecsActions

// --- Initial state (used by both create and reset) ---

const initialState: SpecsState = {
  status: null,
  isLoading: false,
  error: null,
  projectId: null,
  fetchSeq: 0,
}

// --- Store ---

export const useSpecsStore = create<SpecsStore>((set, get) => ({
  ...initialState,

  fetchStatus: async (projectId) => {
    set((s) => ({ fetchSeq: s.fetchSeq + 1, isLoading: true, error: null }))
    const seq = get().fetchSeq
    try {
      const status = await getSpecsStatus(projectId)
      if (get().fetchSeq !== seq) return
      set({ status, projectId, isLoading: false, error: null })
    } catch (err) {
      if (get().fetchSeq !== seq) return
      set({
        isLoading: false,
        error: err instanceof Error ? err.message : String(err),
      })
    }
  },

  setError: (error) => set({ error, isLoading: false }),

  reset: () => set(initialState),
}))
