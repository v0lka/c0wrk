import { create } from 'zustand'

type LogLevel = 'DEBUG' | 'INFO' | 'WARN' | 'ERROR'

interface UIState {
  logLevel: LogLevel
  setLogLevel: (level: LogLevel) => void
}

export const useUIStore = create<UIState>((set) => ({
  logLevel: 'DEBUG',
  setLogLevel: (level) => {
    set({ logLevel: level })
  },
}))
