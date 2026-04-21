import { create } from 'zustand'

// -- Sidebar collapsed persistence --------------------------------------------

const SIDEBAR_COLLAPSED_KEY = 'c0wrk-sidebar-collapsed'

function loadSidebarCollapsed(): boolean {
  try {
    return localStorage.getItem(SIDEBAR_COLLAPSED_KEY) === 'true'
  } catch {
    return false
  }
}

function persistSidebarCollapsed(collapsed: boolean): void {
  try {
    localStorage.setItem(SIDEBAR_COLLAPSED_KEY, String(collapsed))
  } catch {
    // ignore storage errors
  }
}

// -- Store -------------------------------------------------------------------

type LogLevel = 'DEBUG' | 'INFO' | 'WARN' | 'ERROR'

interface UIState {
  logLevel: LogLevel
  sidebarCollapsed: boolean
  setLogLevel: (level: LogLevel) => void
  toggleSidebarCollapsed: () => void
  setSidebarCollapsed: (collapsed: boolean) => void
}

export const useUIStore = create<UIState>((set) => ({
  logLevel: 'DEBUG',
  sidebarCollapsed: loadSidebarCollapsed(),
  setLogLevel: (level) => {
    set({ logLevel: level })
  },
  toggleSidebarCollapsed: () => {
    set((state) => {
      const next = !state.sidebarCollapsed
      persistSidebarCollapsed(next)
      return { sidebarCollapsed: next }
    })
  },
  setSidebarCollapsed: (collapsed) => {
    persistSidebarCollapsed(collapsed)
    set({ sidebarCollapsed: collapsed })
  },
}))
