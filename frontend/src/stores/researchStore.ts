import { create } from 'zustand'
import type {
  ResearchProject,
  ResearchStatus,
  ResearchGraphResponse,
  ResearchNextStep,
  ResearchLogEntry,
} from '@/types/models'

/** Synthetic pseudo-path rendered by the file viewer as the Research
 *  workspace (the editable hypothesis DAG). Parallels `REVIEW_TAB_PATH`
 *  ('c0wrk:review') in reviewStore. */
export const RESEARCH_TAB_PATH = 'c0wrk:research'

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
  /** The single recommended next research action for the active project, or
   *  null when not yet fetched / RESEARCH off. Pulled via GetResearchNextStep
   *  (separate from `status` because it is derived from phase, not file state). */
  nextStep: ResearchNextStep | null
}

interface ResearchActions {
  /** Replace the parsed status, stamping it with the projectId it belongs to. */
  loadStatus: (status: ResearchStatus, projectId: string) => void
  /** Replace the recommended next research action for the active project. */
  loadNextStep: (nextStep: ResearchNextStep) => void
  /** Incrementally update only the active project's graph, metrics, brief,
   *  and has_report fields. Preserves status, projectId, isLoading, error.
   *  Used by the file-change update path to avoid full refetches. */
  loadGraph: (graphResponse: ResearchGraphResponse) => void
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
  nextStep: null,
}

// --- Store ---

export const useResearchStore = create<ResearchStore>((set) => ({
  ...initialState,

  loadStatus: (status, projectId) =>
    set({ status, projectId, isLoading: false, error: null }),

  loadNextStep: (nextStep) => set({ nextStep }),

  loadGraph: (graphResponse) =>
    set((state) => {
      const root = state.status?.root
      if (!root || root.projects.length === 0) return state

      // Guard: only apply the update if the response belongs to the active
      // project. Prevents stale incremental updates (e.g. from a previous
      // project that was switched away from) from overwriting fresh data.
      const activeProjectId = root.active_project_id
      if (activeProjectId && graphResponse.project_id !== activeProjectId) {
        return state
      }

      const projects = root.projects.map((p) => {
        if (p.id !== graphResponse.project_id) return p
        // Update only the fields that can change from file edits.
        // Brief is preserved by the spread (it doesn't change from hypothesis
        // card edits). Graph, metrics, and has_report are replaced.
        return {
          ...p,
          graph: {
            nodes: graphResponse.graph.nodes,
            edges: graphResponse.graph.edges,
          },
          metrics: graphResponse.metrics,
          has_report: graphResponse.has_report,
          log: graphResponse.log,
        }
      })

      // Return a new root with updated projects (new reference for React).
      const newRoot = { ...root, projects }
      const newStatus = { ...state.status!, root: newRoot }
      return { status: newStatus }
    }),

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

/** Stable empty log reference — returned when the active project has no log,
 *  so the selector never allocates a fresh array on every read. */
const EMPTY_LOG: ResearchLogEntry[] = []

/** The active project's research log (t1 log.md entries), or a stable empty
 *  array when off / not loaded. A direct reference to `project.log` (already
 *  normalized to `[]` at the RPC boundary), so it is safe as a selector return
 *  value and stays in sync via `loadStatus`/`loadGraph`. */
export function selectActiveLog(state: ResearchStore): ResearchLogEntry[] {
  return selectActiveProject(state)?.log ?? EMPTY_LOG
}
