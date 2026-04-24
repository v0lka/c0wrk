import { create } from 'zustand'
import { persist } from 'zustand/middleware'

// --- State types ---

type LogLevel = 'DEBUG' | 'INFO' | 'WARN' | 'ERROR'

interface UIState {
  sidebarCollapsed: boolean
  logLevel: LogLevel
}

interface UIActions {
  setSidebarCollapsed: (collapsed: boolean) => void
  toggleSidebarCollapsed: () => void
  setLogLevel: (level: LogLevel) => void
}

// --- Store ---

export const useUIStore = create<UIState & UIActions>()(
  persist(
    (set) => ({
      sidebarCollapsed: false,
      logLevel: 'DEBUG',

      setSidebarCollapsed: (collapsed) => set({ sidebarCollapsed: collapsed }),

      toggleSidebarCollapsed: () => set((s) => ({
        sidebarCollapsed: !s.sidebarCollapsed,
      })),

      setLogLevel: (level) => set({ logLevel: level }),
    }),
    {
      name: 'c0wrk-sidebar-collapsed',
      partialize: (state) => ({
        sidebarCollapsed: state.sidebarCollapsed,
      }),
    }
  )
)
