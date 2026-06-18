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
  /** Per-message model override. null = use global default_model. Persisted. */
  selectedModel: string | null
  /** Per-message reasoning override. null = use family default (= max). Persisted. */
  selectedReasoning: string | null
}

interface InputModeActions {
  setMode: (mode: InputMode) => void
  setHeight: (height: number) => void
  toggleExpanded: () => void
  /** Queue a string for programmatic insertion into the chat editor. */
  insertTextIntoInput: (text: string) => void
  /** Clear the pending insertion after it has been consumed. */
  clearPendingInsertion: () => void
  /** Set the per-message model override. null = use global default. */
  setSelectedModel: (model: string | null) => void
  /** Set the per-message reasoning override. null = use family default. */
  setSelectedReasoning: (value: string | null) => void
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
      selectedModel: null,
      selectedReasoning: null,

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
      setSelectedModel: (model) => set({ selectedModel: model }),
      setSelectedReasoning: (value) => set({ selectedReasoning: value }),
    }),
    {
      name: 'c0wrk-input-mode',
      version: 2,
      migrate: (persistedState, _version) => {
        // v1→v2: added selectedReasoning, default to null.
        const state = persistedState as Record<string, unknown>
        if (state.selectedReasoning === undefined) {
          state.selectedReasoning = null
        }
        return state as typeof persistedState
      },
      partialize: (state) => ({
        mode: state.mode,
        height: state.height,
        collapsedHeight: state.collapsedHeight,
        isExpanded: state.isExpanded,
        selectedModel: state.selectedModel,
        selectedReasoning: state.selectedReasoning,
      }),
    }
  )
)
