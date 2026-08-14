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

type WorkspaceTab = 'explorer' | 'git' | 'semantics' | 'research'

interface UIState {
  sidebarCollapsed: boolean
  /** Active workspace panel tab. Transient — not persisted. */
  workspaceTab: WorkspaceTab
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
}

interface UIActions {
  setSidebarCollapsed: (collapsed: boolean) => void
  toggleSidebarCollapsed: () => void
  setWorkspaceTab: (tab: WorkspaceTab) => void
  setSidebarWidth: (width: number) => void
  setChatSessionListRatio: (ratio: number) => void
}

// --- Store ---

export const useUIStore = create<UIState & UIActions>()(
  persist(
    (set) => ({
      sidebarCollapsed: false,
      workspaceTab: 'explorer',
      sidebarWidth: getDefaultSidebarWidth(),
      chatSessionListRatio: 0.5,

      setSidebarCollapsed: (collapsed) => set({ sidebarCollapsed: collapsed }),

      toggleSidebarCollapsed: () => set((s) => ({
        sidebarCollapsed: !s.sidebarCollapsed,
      })),

      setWorkspaceTab: (tab) => set({ workspaceTab: tab }),

      setSidebarWidth: (width) => set({ sidebarWidth: clamp(width, SIDEBAR_MIN, SIDEBAR_MAX) }),

      setChatSessionListRatio: (ratio) => set({
        chatSessionListRatio: Math.max(0, Math.min(1, ratio)),
      }),
    }),
    {
      name: 'c0wrk-sidebar-collapsed',
      version: 3,
      // Bump version and implement migration when adding/removing/renaming persisted fields.
      migrate: (persistedState, _version) => {
        const prev = (persistedState ?? {}) as {
          sidebarCollapsed?: boolean
          chatSessionListRatio?: number
          sidebarWidth?: number
        }
        // v1→v2 added chatSessionListRatio; v2→v3 added sidebarWidth. On every
        // migration (and fresh installs) missing fields take their creator
        // default; persist's shallow merge already covers fresh installs, but
        // explicit defaults here make the migration resilient to partial data.
        return {
          sidebarCollapsed: prev.sidebarCollapsed ?? false,
          chatSessionListRatio: prev.chatSessionListRatio ?? 0.5,
          sidebarWidth: prev.sidebarWidth ?? getDefaultSidebarWidth(),
        }
      },
      partialize: (state) => ({
        sidebarCollapsed: state.sidebarCollapsed,
        sidebarWidth: state.sidebarWidth,
        chatSessionListRatio: state.chatSessionListRatio,
      }),
    }
  )
)
