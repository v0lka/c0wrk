import { create } from 'zustand'
import { persist } from 'zustand/middleware'

// --- State types ---

type WorkspaceTab = 'explorer' | 'git' | 'semantics'

interface UIState {
  sidebarCollapsed: boolean
  /** Active workspace panel tab. Transient — not persisted. */
  workspaceTab: WorkspaceTab
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
  setChatSessionListRatio: (ratio: number) => void
}

// --- Store ---

export const useUIStore = create<UIState & UIActions>()(
  persist(
    (set) => ({
      sidebarCollapsed: false,
      workspaceTab: 'explorer',
      chatSessionListRatio: 0.5,

      setSidebarCollapsed: (collapsed) => set({ sidebarCollapsed: collapsed }),

      toggleSidebarCollapsed: () => set((s) => ({
        sidebarCollapsed: !s.sidebarCollapsed,
      })),

      setWorkspaceTab: (tab) => set({ workspaceTab: tab }),

      setChatSessionListRatio: (ratio) => set({
        chatSessionListRatio: Math.max(0, Math.min(1, ratio)),
      }),
    }),
    {
      name: 'c0wrk-sidebar-collapsed',
      version: 2,
      // Bump version and implement migration when adding/removing/renaming persisted fields.
      migrate: (persistedState, _version) => {
        const prev = (persistedState ?? {}) as { sidebarCollapsed?: boolean }
        return {
          sidebarCollapsed: prev.sidebarCollapsed ?? false,
          chatSessionListRatio: 0.5,
        }
      },
      partialize: (state) => ({
        sidebarCollapsed: state.sidebarCollapsed,
        chatSessionListRatio: state.chatSessionListRatio,
      }),
    }
  )
)
