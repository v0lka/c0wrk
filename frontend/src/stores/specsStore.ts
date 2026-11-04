import { create } from 'zustand'
import { persist } from 'zustand/middleware'
import { getSpecsStatus } from '@/api/specs'
import { useProjectStore } from '@/stores/projectStore'
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
  /** The projectId the current `status` belongs to, stamped at fetch START:
   *  together with the panel's active-project guard it keeps a stale
   *  cross-project status from ever rendering when the user switches
   *  projects while a fetch is in flight. */
  projectId: string | null
  /** Monotonic sequence stamped on every fetchStatus START. A response
   *  applies only while the store's counter still equals the ticket
   *  captured at its start — last-write-wins by initiation order, so an
   *  older fetch that resolves after a newer one never overwrites it (the
   *  researchStore graphSyncSeq contract). */
  fetchSeq: number
  /** The SIDEBAR tree's expanded nodes, keyed by projectId and then by the
   *  tree node id (the directory path relative to the specs root, '' for
   *  the root files group) — true = expanded. An absent key is collapsed:
   *  the whole tree starts collapsed. Keyed per project because the parse
   *  (and the node ids) belong to one project's specs tree; a foreign
   *  project must never inherit another project's expansion. The whole map
   *  is the store's ONLY persisted slice (zustand persist): the panel
   *  unmounts on every tab switch, and localStorage is the only state that
   *  survives an app restart. */
  treeExpandedNodes: Record<string, Record<string, boolean>>
}

interface SpecsActions {
  /** Fetch the specs/ status of a project (one light read-only parse) and
   *  store it, stamped with the projectId. A fetch for a DIFFERENT project
   *  clears the previous project's status up front — it must neither
   *  render during the flight nor survive this flight's error path; a
   *  same-project refetch keeps the old status rendered (no flicker on
   *  tree_changed). Superseded responses (a newer fetch started while this
   *  one was in flight) are discarded; a failure stores its message in
   *  `error` and keeps the previous status. */
  fetchStatus: (projectId: string) => Promise<void>
  /** Set/clear the panel's error banner directly. The quick-actions
   *  dispatch path uses it to surface a failed spec-* skill send (the
   *  research store's setError contract — send() rethrows only when the
   *  auto-created session fails, the splash race). */
  setError: (error: string | null) => void
  /** Expand/collapse one SIDEBAR tree node for the ACTIVE project (keyed
   *  by node id). A no-op without an active project — there is nothing to
   *  key the expansion on. */
  toggleTreeNode: (nodeId: string) => void
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
  treeExpandedNodes: {},
}

// --- Store ---
//
// The persist middleware carries exactly ONE slice — `treeExpandedNodes`
// (the sidebar tree's per-project expansion; the panel unmounts on every
// tab switch and localStorage is what survives a restart). Everything else
// (the parse and the fetch bookkeeping) is session state: a persisted
// parse would go stale.

export const useSpecsStore = create<SpecsStore>()(
  persist(
    (set, get) => ({
  ...initialState,

  fetchStatus: async (projectId) => {
    set((s) => ({
      fetchSeq: s.fetchSeq + 1,
      isLoading: true,
      error: null,
      // A fetch for a DIFFERENT project starts from a clean slate: the
      // previous project's status must neither render during the flight
      // nor survive this flight's error path. A same-project refetch
      // keeps the previous status rendered (no flicker on tree_changed).
      status: s.projectId === projectId ? s.status : null,
      projectId,
    }))
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

  toggleTreeNode: (nodeId) => {
    // Keyed on the ACTIVE project at toggle time — the expansion belongs to
    // the project whose tree is rendered, so a foreign project never picks
    // up another project's expanded set.
    const projectId = useProjectStore.getState().activeProjectId
    if (projectId === null) return
    set((s) => ({
      treeExpandedNodes: {
        ...s.treeExpandedNodes,
        [projectId]: {
          ...s.treeExpandedNodes[projectId],
          // Absent key = collapsed, so first toggle expands.
          [nodeId]: !(s.treeExpandedNodes[projectId]?.[nodeId] ?? false),
        },
      },
    }))
  },

  reset: () => set(initialState),
    }),
    {
      name: 'c0wrk-specs-view',
      version: 1,
      // Bump version and implement migration when adding/removing/renaming
      // persisted fields.
      migrate: (persistedState, _version) => persistedState,
      partialize: (state) => ({ treeExpandedNodes: state.treeExpandedNodes }),
    },
  ),
)
