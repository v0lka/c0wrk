import { create } from 'zustand'
import { persist } from 'zustand/middleware'

export type ExecutionMode = 'normal' | 'advanced'

interface ExecutionModeState {
  mode: ExecutionMode
  setMode: (mode: ExecutionMode) => void
}

export const useExecutionModeStore = create<ExecutionModeState>()(
  persist(
    (set) => ({
      mode: 'normal',
      setMode: (mode) => set({ mode }),
    }),
    {
      name: 'c0wrk-execution-mode',
    }
  )
)
