import { create } from 'zustand'
import { persist } from 'zustand/middleware'

// --- Constants ---

export const SIDEBAR_MIN = 180
export const SIDEBAR_MAX = 500

function clamp(value: number, lo: number, hi: number): number {
  return Math.max(lo, Math.min(hi, value))
}

/** Default sidebar width: 1/5 of the screen, clamped to [SIDEBAR_MIN, SIDEBAR_MAX]. */
export function getDefaultSidebarWidth(): number {
  const screenWidth = typeof window !== 'undefined' ? window.innerWidth : 1440
  return clamp(Math.round(screenWidth / 5), SIDEBAR_MIN, SIDEBAR_MAX)
}

// --- State types ---

export type WorkspaceTab = 'explorer' | 'git' | 'semantics' | 'research'

/** Active workspace tab per project id. Persisted so each project remembers its own tab. */
export type WorkspaceTabByProject = Record<string, WorkspaceTab>

/**
 * The Research panel's inner segment: the research control Dashboard vs the
 * literature ("papers") library. The paper library always lives at the
 * canonical research root of a real project — so
 * the segment is remembered per project just like the workspace tab.
 */
export type ResearchSegment = 'dashboard' | 'papers'

/** Active research segment per project id. Persisted so each project remembers its own segment. */
export type ResearchSegmentByProject = Record<string, ResearchSegment>

/**
 * Valid WorkspaceTab values — used by the persist `merge` (and the `migrate`
 * version step) to validate persisted state entry-by-entry (mirrors
 * GIT_PANEL_TAB_VALUES in gitPanelStore): an unknown/corrupt tab value
 * (hand-edited localStorage or a future shape written by a newer build)
 * matches no TabsTrigger/TabsContent and would render a blank workspace
 * panel, so it is dropped rather than trusted and the project falls back to
 * the default tab.
 */
const WORKSPACE_TAB_VALUES = new Set<WorkspaceTab>(['explorer', 'git', 'semantics', 'research'])

/**
 * Valid ResearchSegment values — validated entry-by-entry on every rehydrate
 * (same fail-closed contract as WORKSPACE_TAB_VALUES): an unknown/corrupt
 * segment value falls back to the default rather than rendering a blank panel.
 */
const RESEARCH_SEGMENT_VALUES = new Set<ResearchSegment>(['dashboard', 'papers'])

interface UIState {
  sidebarCollapsed: boolean
  /**
   * Active workspace panel tab, keyed by project id. Persisted so switching
   * projects restores each project's last-viewed tab.
   */
  workspaceTabByProject: WorkspaceTabByProject
  /**
   * Active Research-panel segment, keyed by project id. Persisted so switching
   * projects restores each project's last-viewed segment (research Dashboard vs
   * Papers library).
   */
  researchSegmentByProject: ResearchSegmentByProject
  /**
   * Sidebar width in pixels (the expanded width; a collapsed sidebar renders at
   * a fixed rail width). Persisted so the user's chosen proportion survives
   * reloads.
   */
  sidebarWidth: number
  /**
   * Vertical split ratio (0..1) between the flat session list and the workspace
   * panel in CHAT (No Project) mode. 0.5 = even split. Persisted so the user's
   * chosen proportion survives reloads.
   */
  chatSessionListRatio: number
  /**
   * Whether the per-run session statistics row (finish state, steps, output
   * tokens, loop-detector counters) renders under the chat. Display-only:
   * metrics are always collected and persisted; this toggle just hides or
   * shows the summary. Off by default. Persisted.
   */
  showSessionStats: boolean
}

interface UIActions {
  setSidebarCollapsed: (collapsed: boolean) => void
  toggleSidebarCollapsed: () => void
  setWorkspaceTab: (projectId: string, tab: WorkspaceTab) => void
  /** Set the Research panel's inner segment for a project (dashboard | papers). */
  setResearchSegment: (projectId: string, segment: ResearchSegment) => void
  /** Forget the remembered tab for a single project (e.g. on project delete). */
  dropProjectTabs: (projectId: string) => void
  setSidebarWidth: (width: number) => void
  setChatSessionListRatio: (ratio: number) => void
  setShowSessionStats: (show: boolean) => void
}

/** Default workspace tab when a project has no remembered tab yet. */
export const DEFAULT_WORKSPACE_TAB: WorkspaceTab = 'explorer'

/**
 * Pure selector: resolve a project's active workspace tab, defaulting to
 * `'explorer'` when the project has no remembered tab or when no project is
 * active (CHAT / No Project mode) — safe to call with a null/undefined id.
 */
export function selectWorkspaceTab(
  state: Pick<UIState, 'workspaceTabByProject'>,
  projectId: string | null | undefined,
): WorkspaceTab {
  if (projectId === null || projectId === undefined) return DEFAULT_WORKSPACE_TAB
  return state.workspaceTabByProject[projectId] ?? DEFAULT_WORKSPACE_TAB
}

/** Default Research-panel segment when a project has no remembered segment yet. */
export const DEFAULT_RESEARCH_SEGMENT: ResearchSegment = 'dashboard'

/**
 * Pure selector: resolve a project's Research-panel segment, defaulting to
 * `'dashboard'` when the project has no remembered segment or when no project
 * is active — safe to call with a null/undefined id.
 */
export function selectResearchSegment(
  state: Pick<UIState, 'researchSegmentByProject'>,
  projectId: string | null | undefined,
): ResearchSegment {
  if (projectId === null || projectId === undefined) return DEFAULT_RESEARCH_SEGMENT
  return state.researchSegmentByProject[projectId] ?? DEFAULT_RESEARCH_SEGMENT
}

// --- Store ---

/**
 * Rehydrate persisted state into the current state. Zustand's persist
 * middleware calls `merge` on EVERY rehydrate (version match or not), so the
 * per-project workspace-tab validation lives HERE — the `migrate` hook runs
 * only when the stored version differs, and a same-version corrupt value
 * (hand-edited localStorage, or a future build writing a new tab name without
 * bumping `version`) would otherwise rehydrate verbatim and render a blank
 * workspace panel. Mirrors mergeGitPanel's `activeTabByProject` contract.
 * Every scalar field falls back to the current (default) value when its
 * persisted value is missing or has the wrong type.
 */
export function mergeUIStore(
  persisted: unknown,
  current: UIState & UIActions,
): UIState & UIActions {
  const p = (persisted ?? {}) as {
    sidebarCollapsed?: unknown
    sidebarWidth?: unknown
    chatSessionListRatio?: unknown
    showSessionStats?: unknown
    workspaceTabByProject?: unknown
    researchSegmentByProject?: unknown
  }
  const workspaceTabByProject: WorkspaceTabByProject = {}
  if (
    p.workspaceTabByProject !== null &&
    typeof p.workspaceTabByProject === 'object'
  ) {
    for (const [projectId, tab] of Object.entries(
      p.workspaceTabByProject as Record<string, unknown>,
    )) {
      if (WORKSPACE_TAB_VALUES.has(tab as WorkspaceTab)) {
        workspaceTabByProject[projectId] = tab as WorkspaceTab
      }
    }
  }
  const researchSegmentByProject: ResearchSegmentByProject = {}
  if (
    p.researchSegmentByProject !== null &&
    typeof p.researchSegmentByProject === 'object'
  ) {
    for (const [projectId, segment] of Object.entries(
      p.researchSegmentByProject as Record<string, unknown>,
    )) {
      if (RESEARCH_SEGMENT_VALUES.has(segment as ResearchSegment)) {
        researchSegmentByProject[projectId] = segment as ResearchSegment
      }
    }
  }
  return {
    ...current,
    sidebarCollapsed:
      typeof p.sidebarCollapsed === 'boolean' ? p.sidebarCollapsed : current.sidebarCollapsed,
    sidebarWidth:
      typeof p.sidebarWidth === 'number' ? p.sidebarWidth : current.sidebarWidth,
    chatSessionListRatio:
      typeof p.chatSessionListRatio === 'number'
        ? p.chatSessionListRatio
        : current.chatSessionListRatio,
    showSessionStats:
      typeof p.showSessionStats === 'boolean' ? p.showSessionStats : current.showSessionStats,
    workspaceTabByProject,
    researchSegmentByProject,
  }
}

export const useUIStore = create<UIState & UIActions>()(
  persist(
    (set) => ({
      sidebarCollapsed: false,
      workspaceTabByProject: {},
      researchSegmentByProject: {},
      sidebarWidth: getDefaultSidebarWidth(),
      chatSessionListRatio: 0.5,
      showSessionStats: false,

      setSidebarCollapsed: (collapsed) => set({ sidebarCollapsed: collapsed }),

      toggleSidebarCollapsed: () => set((s) => ({
        sidebarCollapsed: !s.sidebarCollapsed,
      })),

      setWorkspaceTab: (projectId, tab) => set((s) => (
        s.workspaceTabByProject[projectId] === tab
          ? s
          : { workspaceTabByProject: { ...s.workspaceTabByProject, [projectId]: tab } }
      )),

      setResearchSegment: (projectId, segment) => set((s) => (
        s.researchSegmentByProject[projectId] === segment
          ? s
          : { researchSegmentByProject: { ...s.researchSegmentByProject, [projectId]: segment } }
      )),

      dropProjectTabs: (projectId) => set((s) => {
        const hasTab = projectId in s.workspaceTabByProject
        const hasSegment = projectId in s.researchSegmentByProject
        if (!hasTab && !hasSegment) return s
        let workspaceTabByProject = s.workspaceTabByProject
        if (hasTab) {
          workspaceTabByProject = { ...workspaceTabByProject }
          delete workspaceTabByProject[projectId]
        }
        let researchSegmentByProject = s.researchSegmentByProject
        if (hasSegment) {
          researchSegmentByProject = { ...researchSegmentByProject }
          delete researchSegmentByProject[projectId]
        }
        return { workspaceTabByProject, researchSegmentByProject }
      }),

      setSidebarWidth: (width) => set({ sidebarWidth: clamp(width, SIDEBAR_MIN, SIDEBAR_MAX) }),

      setChatSessionListRatio: (ratio) => set({
        chatSessionListRatio: Math.max(0, Math.min(1, ratio)),
      }),

      setShowSessionStats: (show) => set({ showSessionStats: show }),
    }),
    {
      name: 'c0wrk-sidebar-collapsed',
      version: 6,
      // Bump version and implement migration when adding/removing/renaming persisted fields.
      migrate: (persistedState, _version) => {
        const prev = (persistedState ?? {}) as {
          sidebarCollapsed?: boolean
          chatSessionListRatio?: number
          sidebarWidth?: number
          showSessionStats?: boolean
          workspaceTabByProject?: WorkspaceTabByProject
          researchSegmentByProject?: ResearchSegmentByProject
        }
        // v1→v2 added chatSessionListRatio; v2→v3 added sidebarWidth;
        // v3→v4 added showSessionStats (default off — the stats row is
        // opt-in); v4→v5 replaced the transient workspaceTab scalar with the
        // per-project workspaceTabByProject map; v5→v6 added the per-project
        // researchSegmentByProject map (Research panel: dashboard | papers).
        // On every migration (and fresh
        // installs) missing fields take their creator default; persist's
        // shallow merge already covers fresh installs, but explicit defaults
        // here make the migration resilient to partial data.
        // workspaceTabByProject is validated entry-by-entry against
        // WORKSPACE_TAB_VALUES (same fail-closed contract as mergeGitPanel's
        // activeTabByProject): unknown tab values are dropped so the store
        // never rehydrates a tab no panel can render.
        const workspaceTabByProject: WorkspaceTabByProject = {}
        if (
          prev.workspaceTabByProject !== null &&
          typeof prev.workspaceTabByProject === 'object'
        ) {
          for (const [projectId, tab] of Object.entries(prev.workspaceTabByProject)) {
            if (WORKSPACE_TAB_VALUES.has(tab as WorkspaceTab)) {
              workspaceTabByProject[projectId] = tab as WorkspaceTab
            }
          }
        }
        const researchSegmentByProject: ResearchSegmentByProject = {}
        if (
          prev.researchSegmentByProject !== null &&
          typeof prev.researchSegmentByProject === 'object'
        ) {
          for (const [projectId, segment] of Object.entries(prev.researchSegmentByProject)) {
            if (RESEARCH_SEGMENT_VALUES.has(segment as ResearchSegment)) {
              researchSegmentByProject[projectId] = segment as ResearchSegment
            }
          }
        }
        return {
          sidebarCollapsed: prev.sidebarCollapsed ?? false,
          chatSessionListRatio: prev.chatSessionListRatio ?? 0.5,
          sidebarWidth: prev.sidebarWidth ?? getDefaultSidebarWidth(),
          showSessionStats: prev.showSessionStats ?? false,
          workspaceTabByProject,
          researchSegmentByProject,
        }
      },
      // merge (not just migrate) validates on every rehydrate — see mergeUIStore.
      merge: mergeUIStore,
      partialize: (state) => ({
        sidebarCollapsed: state.sidebarCollapsed,
        sidebarWidth: state.sidebarWidth,
        chatSessionListRatio: state.chatSessionListRatio,
        showSessionStats: state.showSessionStats,
        workspaceTabByProject: state.workspaceTabByProject,
        researchSegmentByProject: state.researchSegmentByProject,
      }),
    }
  )
)
