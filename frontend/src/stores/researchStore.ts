import { create } from 'zustand'
import type {
  ResearchProject,
  ResearchRoot,
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
  /** The dashboard's CURRENT hypothesis card (null = none) — the card the
   *  recommended next step is scoped to (getResearchNextStep's hypothesisId
   *  param). Unlike the workspace DAG selection above, this is an
   *  auto-repairing cursor rather than a sticky editing selection: every
   *  successful loadStatus/loadGraph reconciles it against the ACTIVE
   *  research project, and a selection that no longer resolves (the active
   *  R-NNN switched, or the card was deleted) falls back to the active
   *  front's leading hypothesis instead of dangling. */
  activeHypothesisId: string | null
  /** The research project (R-NNN) the dashboard's current hypothesis belongs
   *  to — the composite key that makes the generic node id (H-001…)
   *  unambiguous across research projects, mirroring
   *  selectedHypothesisProjectId. The reconciliation and the next-step call
   *  sites (via selectActiveHypothesisId) resolve the selection only while
   *  this stamp matches the ACTIVE research project. */
  activeHypothesisResearchId: string | null
  /** Pinned research projects — brief paths, research-root-relative with
   *  forward slashes — mirrored from the status payload's pinned_research.
   *  Updated by loadStatus (the pin RPCs resolve with a status refresh);
   *  preserved by loadGraph, because pins live in the persisted project
   *  record and never change through research file edits. */
  pinnedResearch: string[]
  /** Pinned hypothesis cards keyed by hypothesis id (H-NNN): each entry
   *  lists the pinned card paths — the same H-NNN exists across R-NNN
   *  projects, so one key can carry cards from several research projects.
   *  Mirrored from the status payload's pinned_hypotheses. */
  pinnedHypotheses: Record<string, string[]>
  /** The workspace DAG's "Hide completed" filter toggle. Survives remounts
   *  for the same reason as the selection. */
  hideTerminal: boolean
  /** Current height (px) of the workspace's bottom hypothesis-card panel.
   *  Survives remounts for the same reason as the selection. */
  cardHeight: number
  /** Wall-clock ms of the last successful graph sync (loadGraph or the full
   *  loadStatus). The status-events hook's research-scoped watchdog compares
   *  it against its own scheduling time to decide whether the incremental
   *  path landed — if it did not, a full refetch runs as a fallback so the
   *  panel always converges (see useResearchStatusEvents). */
  lastGraphSyncAt: number
  /** Monotonic sequence stamped on every successful graph-bearing write
   *  (loadStatus and loadGraph alike). Callers capture it as a "ticket" when
   *  a fetch or mutation STARTS and pass it back; the store rejects any
   *  write whose ticket is older than the current counter — last-write-wins
   *  by initiation order. Wall-clock stamps cannot express this: three
   *  overlapping sources (the status-events watchdog's full refresh, the
   *  file-watcher's fallback fullRefresh, and direct hypothesis mutations)
   *  can start and finish out of order, and a fetch that started BEFORE a
   *  newer sync but resolves AFTER it must never overwrite the newer data. */
  graphSyncSeq: number
}

interface ResearchActions {
  /** Replace the parsed status, stamping it with the projectId it belongs
   *  to. `startedSeq` (optional) is the store's graphSyncSeq captured when
   *  the caller STARTED fetching; when given and older than the store's
   *  current sequence, a newer sync landed while the fetch was in flight and
   *  this (older) payload is rejected wholesale — last-write-wins. Returns
   *  true when applied, false when rejected as stale. */
  loadStatus: (status: ResearchStatus, projectId: string, startedSeq?: number) => boolean
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
  /** Set the dashboard's current hypothesis card (or clear with null). The
   *  selection is stamped with the ACTIVE research project (R-NNN) it was
   *  made in — see activeHypothesisResearchId. The next loadStatus/loadGraph
   *  keeps it only while it still resolves (its project stays active and the
   *  card exists); otherwise the reconciliation falls back to the active
   *  front's leading hypothesis. */
  setActiveHypothesis: (id: string | null) => void
  /** Toggle the workspace DAG's "Hide completed" filter. */
  setHideTerminal: (hide: boolean) => void
  /** Resize the workspace's bottom hypothesis-card panel (px, clamped by the
   *  caller). */
  setCardHeight: (height: number) => void
  /** Incrementally update only the active project's graph, metrics, brief,
   *  and has_report fields. Preserves status, projectId, the workspace
   *  selection, and the persisted pins; reconciles the dashboard's current
   *  hypothesis (see activeHypothesisId); clears error and isLoading (a
   *  successful incremental sync is a successful sync — leaving a stale
   *  error or spinner stuck is worse than clearing them). Used by the
   *  file-change update path and direct hypothesis mutations to avoid full
   *  refetches.
   *  `startedSeq` (optional) is the store's graphSyncSeq captured when the
   *  caller STARTED the fetch/mutation; when given and older than the
   *  store's current sequence, a newer sync landed while this one was in
   *  flight and the snapshot is rejected wholesale — last-write-wins (it
   *  must neither regress the panel nor re-stamp itself fresh, which would
   *  defuse the watchdog).
   *  Returns true when the update was applied. Returns false when it cannot
   *  be applied incrementally (no root yet, a stale startedSeq, or the
   *  response naming a research project the store has never seen — e.g. a
   *  brand-new R-NNN created since the last full load): the caller must
   *  then run a full status refetch so the panel converges instead of
   *  freezing on stale data. */
  loadGraph: (graphResponse: ResearchGraphResponse, startedSeq?: number) => boolean
  setLoading: (loading: boolean) => void
  setToggling: (toggling: boolean) => void
  setError: (error: string | null) => void
  /** Clear everything (e.g. on project switch / No-Project mode). */
  reset: () => void
}

export type ResearchStore = ResearchState & ResearchActions

// --- Initial state (used by both create and reset) ---

/** Bottom hypothesis-card panel height bounds (px) for the Research
 *  workspace's horizontal (graph-above / card-below) split. The default
 *  leaves the DAG the dominant share; the split is user-resizable via the
 *  drag handle / arrow keys. Exported for the workspace component's
 *  useResize. */
export const RESEARCH_CARD_DEFAULT_HEIGHT = 300
export const RESEARCH_CARD_MIN_HEIGHT = 120
export const RESEARCH_CARD_MAX_HEIGHT = 720

/** Stable empty pin collections — the initial/reset state references so
 *  selectors returning them never allocate a fresh array/object per read. */
const EMPTY_PINNED_RESEARCH: string[] = []
const EMPTY_PINNED_HYPOTHESES: Record<string, string[]> = {}

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
  activeHypothesisId: null,
  activeHypothesisResearchId: null,
  pinnedResearch: EMPTY_PINNED_RESEARCH,
  pinnedHypotheses: EMPTY_PINNED_HYPOTHESES,
  hideTerminal: false,
  cardHeight: RESEARCH_CARD_DEFAULT_HEIGHT,
  lastGraphSyncAt: 0,
  graphSyncSeq: 0,
}

// --- Active-hypothesis reconciliation (the dashboard's current card) ---

/** The active research project of a parsed root — the same resolution rule
 *  as the selectActiveProject selector (active_project_id when it names a
 *  known project, else the highest-numbered project), extracted as a pure
 *  function over the root so the reconciliation helper can resolve against
 *  a fresh status payload instead of the whole store. */
function activeProjectOfRoot(root: ResearchRoot | undefined): ResearchProject | null {
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

/** Resolve the dashboard's current hypothesis against a fresh root: keep
 *  the current selection only while it still names an existing node of the
 *  ACTIVE research project; otherwise fall back to the active front's
 *  leading hypothesis (the same card the project-level recommendation
 *  targets), or null when there is no active project or no front. Front
 *  entries that no longer resolve to a node (e.g. the RPC boundary dropped
 *  a malformed node entry) are skipped in favor of the next valid one. */
function reconcileActiveHypothesis(
  currentId: string | null,
  currentResearchId: string | null,
  root: ResearchRoot | undefined,
): { id: string | null; researchId: string | null } {
  const project = activeProjectOfRoot(root)
  if (!project) {
    return { id: null, researchId: null }
  }
  if (
    currentId !== null &&
    currentResearchId === project.id &&
    project.graph.nodes.some((n) => n.id === currentId)
  ) {
    return { id: currentId, researchId: currentResearchId }
  }
  const nodeIds = new Set(project.graph.nodes.map((n) => n.id))
  const frontId = project.metrics.active_front?.find((id) => nodeIds.has(id)) ?? null
  return { id: frontId, researchId: frontId !== null ? project.id : null }
}

// --- Store ---

export const useResearchStore = create<ResearchStore>((set) => ({
  ...initialState,

  loadStatus: (status, projectId, startedSeq) => {
    // Returns false when a newer sync already landed (stale payload) — see
    // the interface doc.
    let applied = false
    set((state) => {
      if (startedSeq !== undefined && startedSeq < state.graphSyncSeq) {
        return state
      }
      applied = true
      // A different project's status never inherits the previous project's
      // hypothesis selection: node ids (H-001…) are generic and could collide
      // across projects, silently opening the wrong card. The recommended
      // next step is dropped too — it is phase-derived per project, and the
      // next-step fetch is best-effort (a failure would otherwise leave the
      // OLD project's recommendation rendering indefinitely). Same-project
      // reloads (research:changed refreshes, toggle) keep both —
      // same-project active-R-NNN transitions are handled by the composite
      // selection keys (selectedHypothesisProjectId /
      // activeHypothesisResearchId) instead.
      const crossProject = state.projectId !== null && state.projectId !== projectId
      // Reconcile the dashboard's current card against the fresh payload —
      // from a clean slate on a cross-project load (the previous project's
      // R-NNN stamp is meaningless here even when the ids collide).
      const activeHypothesis = reconcileActiveHypothesis(
        crossProject ? null : state.activeHypothesisId,
        crossProject ? null : state.activeHypothesisResearchId,
        status.root,
      )
      return {
        status,
        projectId,
        isLoading: false,
        error: null,
        lastGraphSyncAt: Date.now(),
        graphSyncSeq: state.graphSyncSeq + 1,
        // Mirror the persisted pins (absent on older payloads → empty).
        pinnedResearch: status.pinned_research ?? EMPTY_PINNED_RESEARCH,
        pinnedHypotheses: status.pinned_hypotheses ?? EMPTY_PINNED_HYPOTHESES,
        activeHypothesisId: activeHypothesis.id,
        activeHypothesisResearchId: activeHypothesis.researchId,
        ...(crossProject
          ? {
              selectedHypothesisId: null,
              selectedHypothesisProjectId: null,
              hypothesisDraft: null,
              nextStep: null,
            }
          : {}),
      }
    })
    return applied
  },

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

  setActiveHypothesis: (id) =>
    set((state) => ({
      activeHypothesisId: id,
      // Stamp the research project the selection belongs to (null when
      // clearing, or when nothing is active — an unstamped selection never
      // resolves for the next-step scoping).
      activeHypothesisResearchId:
        id === null ? null : activeProjectOfRoot(state.status?.root)?.id ?? null,
    })),

  setHideTerminal: (hide) => set({ hideTerminal: hide }),

  setCardHeight: (height) => set({ cardHeight: height }),

  loadGraph: (graphResponse, startedSeq) => {
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
      // fetch/mutation STARTED before the store's last successful sync —
      // applying one would both regress the panel to older data and stamp it
      // fresh, defusing the watchdog. Signal the caller to converge via a
      // full status refetch instead.
      if (startedSeq !== undefined && startedSeq < state.graphSyncSeq) {
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
      // Reconcile the dashboard's current card against the updated graph:
      // the active R-NNN may have switched (the response names the fresh
      // PickActiveProject choice) and cards may have been deleted since the
      // last sync. Pins are deliberately untouched here — they live in the
      // persisted project record and change only through the pin RPCs
      // (whose callers refresh the full status), never through file edits.
      const activeHypothesis = reconcileActiveHypothesis(
        state.activeHypothesisId,
        state.activeHypothesisResearchId,
        newRoot,
      )
      return {
        status: newStatus,
        lastGraphSyncAt: Date.now(),
        graphSyncSeq: state.graphSyncSeq + 1,
        activeHypothesisId: activeHypothesis.id,
        activeHypothesisResearchId: activeHypothesis.researchId,
        // A successful incremental sync IS a successful sync: clear any
        // error left behind by a previously failed fetch and any in-flight
        // loading flag, or they stick forever — nothing else clears them
        // once the panel is already rendering fresh graph data.
        error: null,
        isLoading: false,
      }
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
  return activeProjectOfRoot(state.status?.root)
}

/** The dashboard's current hypothesis id, resolved against the ACTIVE
 *  research project. Returns '' when there is no current card — or when its
 *  R-NNN stamp no longer matches the active project (e.g. an unstamped
 *  selection made while nothing was active) — which is exactly the
 *  getResearchNextStep "project-level" token: callers pass the result
 *  straight through as the RPC's hypothesisId. A primitive return value, so
 *  it is safe as a selector. */
export function selectActiveHypothesisId(state: ResearchStore): string {
  if (state.activeHypothesisId === null) return ''
  const project = activeProjectOfRoot(state.status?.root)
  if (!project || state.activeHypothesisResearchId !== project.id) return ''
  return state.activeHypothesisId
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
