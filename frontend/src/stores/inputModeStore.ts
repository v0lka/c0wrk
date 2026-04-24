import { create } from 'zustand'
import { persist } from 'zustand/middleware'

export type InputMode = 'chat' | 'terminal'

interface InputModeState {
  mode: InputMode
  height: number
  collapsedHeight: number
  isExpanded: boolean
}

interface InputModeActions {
  setMode: (mode: InputMode) => void
  setHeight: (height: number) => void
  toggleExpanded: () => void
}

const DEFAULT_HEIGHT = 200
const MIN_HEIGHT = 140
const MAX_HEIGHT = 800
export const EXPANDED_HEIGHT = 550

export const useInputModeStore = create<InputModeState & InputModeActions>()(
  persist(
    (set, get) => ({
      mode: 'chat',
      height: DEFAULT_HEIGHT,
      collapsedHeight: DEFAULT_HEIGHT,
      isExpanded: false,

      setMode: (mode) => set({ mode }),

      setHeight: (height) => {
        const clamped = Math.max(MIN_HEIGHT, Math.min(MAX_HEIGHT, height))
        set({ height: clamped, collapsedHeight: clamped, isExpanded: false })
      },

      toggleExpanded: () => {
        const s = get()
        if (s.isExpanded) {
          set({ height: s.collapsedHeight, isExpanded: false })
        } else {
          set({
            collapsedHeight: s.height,
            height: EXPANDED_HEIGHT,
            isExpanded: true,
          })
        }
      },
    }),
    {
      name: 'c0wrk-input-mode',
      partialize: (state) => ({
        mode: state.mode,
        height: state.height,
        collapsedHeight: state.collapsedHeight,
        isExpanded: state.isExpanded,
      }),
    }
  )
)
