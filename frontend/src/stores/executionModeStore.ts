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
      version: 1,
      // Bump version and implement migration when adding/removing/renaming persisted fields.
      migrate: (persistedState, _version) => persistedState,
    }
  )
)
