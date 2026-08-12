import { create } from 'zustand'
import type { ResearchProject, ResearchStatus } from '@/types/models'

// --- State types ---

interface ResearchState {
  /** Parsed research status (graph + metrics + brief) for the active project,
   *  or null when not yet loaded / RESEARCH off. */
  status: ResearchStatus | null
  /** True while GetResearchStatus is in flight (initial load + event refresh). */
  isLoading: boolean
  /** True while EnableResearch / DisableResearch is in flight (blocks the toggle). */
  isToggling: boolean
  /** Last error from a status fetch or toggle; null when clean. */
  error: string | null
  /** The projectId the current `status` belongs to. Guards against stale data
   *  when the user switches projects before an in-flight fetch resolves. */
  projectId: string | null
}

interface ResearchActions {
  /** Replace the parsed status, stamping it with the projectId it belongs to. */
  loadStatus: (status: ResearchStatus, projectId: string) => void
  setLoading: (loading: boolean) => void
  setToggling: (toggling: boolean) => void
  setError: (error: string | null) => void
  /** Clear everything (e.g. on project switch / No-Project mode). */
  reset: () => void
}

export type ResearchStore = ResearchState & ResearchActions

// --- Initial state (used by both create and reset) ---

const initialState: ResearchState = {
  status: null,
  isLoading: false,
  isToggling: false,
  error: null,
  projectId: null,
}

// --- Store ---

export const useResearchStore = create<ResearchStore>((set) => ({
  ...initialState,

  loadStatus: (status, projectId) =>
    set({ status, projectId, isLoading: false, error: null }),

  setLoading: (loading) => set({ isLoading: loading }),

  setToggling: (toggling) => set({ isToggling: toggling }),

  setError: (error) => set({ error, isLoading: false }),

  reset: () => set(initialState),
}))

// --- Selectors (pure functions; stable references — never allocate inside a
// useStore(selector) call so React 19's useSyncExternalStore never loops) ---

/** True when RESEARCH is enabled for the loaded project. */
export function selectEnabled(state: ResearchStore): boolean {
  return state.status?.enabled ?? false
}

/** The active research project (brief + graph + metrics) for the metrics row
 *  and DAG, or null when off / not yet loaded. Uses the backend-computed
 *  active_project_id (the latest index entry) as the single source of truth —
 *  the same value the orchestrator's research-awareness context points at.
 *  A direct store reference — no allocation — so it is safe as a selector
 *  return value. */
export function selectActiveProject(state: ResearchStore): ResearchProject | null {
  const root = state.status?.root
  if (!root || root.projects.length === 0) {
    return null
  }
  if (root.active_project_id) {
    const found = root.projects.find((p) => p.id === root.active_project_id)
    if (found) return found
  }
  // Fallback: no active_project_id set (e.g. a root with projects but no
  // index). Use the highest-numbered project (last after ascending sort),
  // mirroring research.PickActiveProject's fallback.
  return root.projects[root.projects.length - 1] ?? null
}
