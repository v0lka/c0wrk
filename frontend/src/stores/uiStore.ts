import { create } from 'zustand'
import { persist } from 'zustand/middleware'

// --- State types ---

interface UIState {
  sidebarCollapsed: boolean
}

interface UIActions {
  setSidebarCollapsed: (collapsed: boolean) => void
  toggleSidebarCollapsed: () => void
}

// --- Store ---

export const useUIStore = create<UIState & UIActions>()(
  persist(
    (set) => ({
      sidebarCollapsed: false,

      setSidebarCollapsed: (collapsed) => set({ sidebarCollapsed: collapsed }),

      toggleSidebarCollapsed: () => set((s) => ({
        sidebarCollapsed: !s.sidebarCollapsed,
      })),
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
