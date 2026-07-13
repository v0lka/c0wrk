import { create } from 'zustand'
import { persist } from 'zustand/middleware'

// --- State types ---

type WorkspaceTab = 'explorer' | 'git' | 'semantics'

interface UIState {
  sidebarCollapsed: boolean
  /** Active workspace panel tab. Transient — not persisted. */
  workspaceTab: WorkspaceTab
}

interface UIActions {
  setSidebarCollapsed: (collapsed: boolean) => void
  toggleSidebarCollapsed: () => void
  setWorkspaceTab: (tab: WorkspaceTab) => void
}

// --- Store ---

export const useUIStore = create<UIState & UIActions>()(
  persist(
    (set) => ({
      sidebarCollapsed: false,
      workspaceTab: 'explorer',

      setSidebarCollapsed: (collapsed) => set({ sidebarCollapsed: collapsed }),

      toggleSidebarCollapsed: () => set((s) => ({
        sidebarCollapsed: !s.sidebarCollapsed,
      })),

      setWorkspaceTab: (tab) => set({ workspaceTab: tab }),
    }),
    {
      name: 'c0wrk-sidebar-collapsed',
      version: 1,
      // Bump version and implement migration when adding/removing/renaming persisted fields.
      migrate: (persistedState, _version) => persistedState,
      partialize: (state) => ({
        sidebarCollapsed: state.sidebarCollapsed,
      }),
    }
  )
)
