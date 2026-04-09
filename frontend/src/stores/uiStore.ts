import { useEffect } from 'react'
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
  setTheme: (theme) => set({ theme }),
  logLevel: 'DEBUG',
  setLogLevel: (level) => {
    set({ logLevel: level })
  },
}))

export function useThemeEffect() {
  const theme = useUIStore(s => s.theme)
  useEffect(() => {
    const root = document.documentElement
    if (theme === 'dark') {
      root.classList.remove('light')
      root.classList.add('dark')
    } else if (theme === 'light') {
      root.classList.remove('dark')
      root.classList.add('light')
    } else {
      // system preference
      const prefersDark = window.matchMedia('(prefers-color-scheme: dark)').matches
      root.classList.toggle('dark', prefersDark)
      root.classList.toggle('light', !prefersDark)
    }
  }, [theme])
}
