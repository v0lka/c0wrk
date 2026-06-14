import { create } from 'zustand'
import { persist } from 'zustand/middleware'

export type InputMode = 'chat' | 'terminal'

interface InputModeState {
  mode: InputMode
  height: number
  collapsedHeight: number
  isExpanded: boolean
  /** Transient: set by insertTextIntoInput, consumed by ChatInput, cleared after dispatch. */
  pendingInsertion: string | null
}

interface InputModeActions {
  setMode: (mode: InputMode) => void
  setHeight: (height: number) => void
  toggleExpanded: () => void
  /** Queue a string for programmatic insertion into the chat editor. */
  insertTextIntoInput: (text: string) => void
  /** Clear the pending insertion after it has been consumed. */
  clearPendingInsertion: () => void
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
      pendingInsertion: null,

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

      insertTextIntoInput: (text) => set({ pendingInsertion: text }),
      clearPendingInsertion: () => set({ pendingInsertion: null }),
    }),
    {
      name: 'c0wrk-input-mode',
      version: 1,
      // Bump version and implement migration when adding/removing/renaming persisted fields.
      migrate: (persistedState, _version) => persistedState,
      partialize: (state) => ({
        mode: state.mode,
        height: state.height,
        collapsedHeight: state.collapsedHeight,
        isExpanded: state.isExpanded,
      }),
    }
  )
)
