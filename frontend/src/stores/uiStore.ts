import { create } from 'zustand'

type Theme = 'dark' | 'light' | 'system'
type LogLevel = 'DEBUG' | 'INFO' | 'WARN' | 'ERROR'

interface UIState {
  theme: Theme
  setTheme: (theme: Theme) => void
  logLevel: LogLevel
  setLogLevel: (level: LogLevel) => void
}

export const useUIStore = create<UIState>((set) => ({
  theme: 'dark',
  setTheme: (theme) => {
    set({ theme })
    // Apply to DOM
    const root = document.documentElement
    if (theme === 'dark') {
      root.classList.add('dark')
    } else if (theme === 'light') {
      root.classList.remove('dark')
    } else {
      // system
      const prefersDark = window.matchMedia('(prefers-color-scheme: dark)').matches
      root.classList.toggle('dark', prefersDark)
    }
  },
  logLevel: 'DEBUG',
  setLogLevel: (level) => {
    set({ logLevel: level })
  },
}))
