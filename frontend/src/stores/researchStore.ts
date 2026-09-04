import { create } from 'zustand'
import type {
  ResearchProject,
  ResearchStatus,
  ResearchGraphResponse,
  ResearchNextStep,
  ResearchLogEntry,
  HypothesisDraft,
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
  /** Hypothesis selected on the workspace DAG (null = none). Stored here —
   *  not in component state — so the selection survives workspace remounts:
   *  the floating file viewer auto-collapses on outside focus and back,
   *  unmounting/re-mounting ResearchWorkspace, and switching to a sibling
   *  viewer tab (e.g. a hypothesis markdown card) does the same. */
  selectedHypothesisId: string | null
  /** The research project (R-NNN) the selection belongs to. Node ids are
   *  generic (H-001…) and collide across research projects, so the id alone
   *  cannot identify a card: the workspace resolves (and allows saving) the
   *  selection only while this matches the store's ACTIVE research project.
   *  A same-workspace-project active-project transition (research-init
   *  created a newer R-NNN and PickActiveProject switched to it) then leaves
   *  the stale selection unrendered instead of silently rebinding it — and
   *  an unsaved Save — to another project's same-id hypothesis. */
  selectedHypothesisProjectId: string | null
  /** Edit draft for the selected hypothesis card. Snapshotted from the node
   *  at selection time and updated by user edits only — background graph
   *  refreshes never clobber an in-progress edit. Cleared together with the
   *  selection. */
  hypothesisDraft: HypothesisDraft | null
  /** The workspace DAG's "Hide completed" filter toggle. Survives remounts
   *  for the same reason as the selection. */
  hideTerminal: boolean
  /** Current width (px) of the workspace's detail sidebar. Survives remounts
   *  for the same reason as the selection. */
  sidebarWidth: number
  /** Wall-clock ms of the last successful graph sync (loadGraph or the full
   *  loadStatus). The status-events hook's research-scoped watchdog compares
   *  it against its own scheduling time to decide whether the incremental
   *  path landed — if it did not, a full refetch runs as a fallback so the
   *  panel always converges (see useResearchStatusEvents). */
  lastGraphSyncAt: number
}

interface ResearchActions {
  /** Replace the parsed status, stamping it with the projectId it belongs to. */
  loadStatus: (status: ResearchStatus, projectId: string) => void
  /** Replace the recommended next research action for the active project. */
  loadNextStep: (nextStep: ResearchNextStep) => void
  /** Select a hypothesis on the workspace DAG (or clear with null). The
   *  caller supplies the draft snapshotted from the node at click time (or
   *  null when clearing) so the store stays a plain state container. The
   *  selection is stamped with the research project (R-NNN) it was made in —
   *  see selectedHypothesisProjectId. */
  selectHypothesis: (id: string | null, draft: HypothesisDraft | null) => void
  /** Update the selected hypothesis card's edit draft. */
  setHypothesisDraft: (draft: HypothesisDraft | null) => void
  /** Toggle the workspace DAG's "Hide completed" filter. */
  setHideTerminal: (hide: boolean) => void
  /** Resize the workspace's detail sidebar (px, clamped by the caller). */
  setSidebarWidth: (width: number) => void
  /** Incrementally update only the active project's graph, metrics, brief,
   *  and has_report fields. Preserves status, projectId, isLoading, error.
   *  Used by the file-change update path to avoid full refetches.
   *  `fetchedAt` (optional, wall-clock ms) is when the caller STARTED
   *  fetching this snapshot; when given and older than the store's last
   *  successful sync (lastGraphSyncAt), the snapshot is stale — a slow
   *  incremental fetch resolving after a newer sync (e.g. the watchdog's
   *  full refresh) — and is rejected wholesale so it can neither regress
   *  the panel nor re-stamp itself fresh (which would defuse the watchdog).
   *  Returns true when the update was applied. Returns false when it cannot
   *  be applied incrementally (no root yet, a stale fetchedAt, or the
   *  response naming a research project the store has never seen — e.g. a
   *  brand-new R-NNN created since the last full load): the caller must
   *  then run a full status refetch so the panel converges instead of
   *  freezing on stale data. */
  loadGraph: (graphResponse: ResearchGraphResponse, fetchedAt?: number) => boolean
  setLoading: (loading: boolean) => void
  setToggling: (toggling: boolean) => void
  setError: (error: string | null) => void
  /** Clear everything (e.g. on project switch / No-Project mode). */
  reset: () => void
}

export type ResearchStore = ResearchState & ResearchActions

// --- Initial state (used by both create and reset) ---

/** Detail-sidebar width bounds (px) for the Research workspace. The default
 *  matches the former w-72 (288px); the split is user-resizable via the drag
 *  handle / arrow keys. Exported for the workspace component's useResize. */
export const RESEARCH_SIDEBAR_DEFAULT_WIDTH = 288
export const RESEARCH_SIDEBAR_MIN_WIDTH = 220
export const RESEARCH_SIDEBAR_MAX_WIDTH = 560

const initialState: ResearchState = {
  status: null,
  isLoading: false,
  isToggling: false,
  error: null,
  projectId: null,
  nextStep: null,
  selectedHypothesisId: null,
  selectedHypothesisProjectId: null,
  hypothesisDraft: null,
  hideTerminal: false,
  sidebarWidth: RESEARCH_SIDEBAR_DEFAULT_WIDTH,
  lastGraphSyncAt: 0,
}

// --- Store ---

export const useResearchStore = create<ResearchStore>((set) => ({
  ...initialState,

  loadStatus: (status, projectId) =>
    set((state) => ({
      status,
      projectId,
      isLoading: false,
      error: null,
      lastGraphSyncAt: Date.now(),
      // A different project's status never inherits the previous project's
      // hypothesis selection: node ids (H-001…) are generic and could collide
      // across projects, silently opening the wrong card. Same-project
      // reloads (research:changed refreshes, toggle) keep the selection —
      // same-project active-R-NNN transitions are handled by the composite
      // selection key (selectedHypothesisProjectId) instead.
      ...(state.projectId && state.projectId !== projectId
        ? {
            selectedHypothesisId: null,
            selectedHypothesisProjectId: null,
            hypothesisDraft: null,
          }
        : {}),
    })),

  loadNextStep: (nextStep) => set({ nextStep }),

  selectHypothesis: (id, draft) =>
    set((state) => ({
      selectedHypothesisId: id,
      hypothesisDraft: draft,
      // Stamp the research project the selection belongs to (null when
      // clearing, or when nothing is active — an unstamped selection never
      // resolves to a card).
      selectedHypothesisProjectId:
        id === null ? null : selectActiveProject(state)?.id ?? null,
    })),

  setHypothesisDraft: (draft) => set({ hypothesisDraft: draft }),

  setHideTerminal: (hide) => set({ hideTerminal: hide }),

  setSidebarWidth: (width) => set({ sidebarWidth: width }),

  loadGraph: (graphResponse, fetchedAt) => {
    // Returns false when a full refetch is required instead (see interface).
    // The anti-stale-project guard intentionally compares the response's
    // R-NNN against the projects the STORE knows — not against the store's
    // cached active_project_id. The cached id is only a parse snapshot: when
    // the backend's fresh PickActiveProject lands on a different (e.g. newly
    // created) R-NNN, that is a legitimate active-project transition, and the
    // update must follow it. Comparing against the stale snapshot rejected
    // every incremental update forever — combined with the research-scoped
    // skip of the full refetch this froze the whole panel (log, metrics,
    // graph) until a remount or project switch.
    let applied = false
    set((state) => {
      const root = state.status?.root
      if (!root || root.projects.length === 0) return state

      // Staleness guard (see the interface doc): reject snapshots whose
      // fetch STARTED before the store's last successful sync — applying one
      // would both regress the panel to older data and stamp it fresh,
      // defusing the watchdog. Signal the caller to converge via a full
      // status refetch instead.
      if (fetchedAt !== undefined && fetchedAt < state.lastGraphSyncAt) {
        return state
      }

      // The response must name a research project the store already knows.
      // A brand-new R-NNN (created since the last full load) carries no
      // brief/index context here, so the incremental path cannot render it —
      // signal the caller to run a full status refetch instead.
      const known = root.projects.some((p) => p.id === graphResponse.project_id)
      if (!known) return state

      applied = true
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

      // Follow the backend's fresh active-project choice (the response's
      // project was selected by PickActiveProject on a fresh parse — a newer
      // source of truth than the store's cached active_project_id).
      const newRoot = { ...root, projects, active_project_id: graphResponse.project_id }
      const newStatus = { ...state.status!, root: newRoot }
      return { status: newStatus, lastGraphSyncAt: Date.now() }
    })
    return applied
  },

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
