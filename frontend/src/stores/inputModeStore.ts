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
  /** Transient: set by "Open in Terminal" file-tree action, consumed by Terminal, cleared after use. */
  pendingTerminalDir: string | null
  /** Per-message model override. null = use global default_model. Persisted. */
  selectedModel: string | null
  /** Per-message reasoning override. null = use family default (= max). Persisted. */
  selectedReasoning: string | null
  /**
   * Goal-mode toggle for the next sent message. When true, the backend enables
   * goal mode for the first message of a task (OR-ed with any /goal prefix).
   * Persisted.
   */
  goalEnabled: boolean
  /**
   * Goal budget override for the next sent message. A JSON string of the
   * goal.GoalBudget fields (e.g. `{"max_turns":5}`) tightening the goal's
   * resource caps below the config defaults; empty string = use config
   * defaults (unlimited). Persisted.
   */
  goalBudget: string
}

interface InputModeActions {
  setMode: (mode: InputMode) => void
  setHeight: (height: number) => void
  toggleExpanded: () => void
  /** Queue a string for programmatic insertion into the chat editor. */
  insertTextIntoInput: (text: string) => void
  /** Clear the pending insertion after it has been consumed. */
  clearPendingInsertion: () => void
  /** Queue a working directory for the next terminal start. */
  setPendingTerminalDir: (dir: string) => void
  /** Clear the pending terminal directory after it has been consumed. */
  clearPendingTerminalDir: () => void
  /** Set the per-message model override. null = use global default. */
  setSelectedModel: (model: string | null) => void
  /** Set the per-message reasoning override. null = use family default. */
  setSelectedReasoning: (value: string | null) => void
  /** Toggle goal mode for the next sent message. */
  setGoalEnabled: (enabled: boolean) => void
  /** Set the goal budget override for the next sent message (JSON or empty). */
  setGoalBudget: (budget: string) => void
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
      pendingTerminalDir: null,
      selectedModel: null,
      selectedReasoning: null,
      goalEnabled: false,
      goalBudget: '',

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
      setPendingTerminalDir: (dir) => set({ pendingTerminalDir: dir }),
      clearPendingTerminalDir: () => set({ pendingTerminalDir: null }),
      setSelectedModel: (model) => set({ selectedModel: model }),
      setSelectedReasoning: (value) => set({ selectedReasoning: value }),
      setGoalEnabled: (enabled) => set({ goalEnabled: enabled }),
      setGoalBudget: (budget) => set({ goalBudget: budget }),
    }),
    {
      name: 'c0wrk-input-mode',
      version: 4,
      migrate: (persistedState, version) => {
        // v1→v2: added selectedReasoning, default to null.
        // v2→v3: added goalEnabled (false) and goalBudget ('').
        // v3→v4: STOPPED persisting goalEnabled/goalBudget (goal mode is
        //        first-message-only and per-task, so persisting the toggle
        //        silently re-activated goal mode on every fresh session). Strip
        //        any stale goal flags from older persisted state so an old
        //        `goalEnabled: true` does not leak back into the in-memory store.
        const state = persistedState as Record<string, unknown>
        if (version < 2 && state.selectedReasoning === undefined) {
          state.selectedReasoning = null
        }
        delete state.goalEnabled
        delete state.goalBudget
        return state as typeof persistedState
      },
      partialize: (state) => ({
        mode: state.mode,
        height: state.height,
        collapsedHeight: state.collapsedHeight,
        isExpanded: state.isExpanded,
        selectedModel: state.selectedModel,
        selectedReasoning: state.selectedReasoning,
        // goalEnabled/goalBudget are intentionally NOT persisted: goal mode is
        // first-message-only and per-task, so persisting the toggle would
        // silently activate goal mode on every fresh session. They live in
        // memory only (reset to their defaults on reload).
      }),
    }
  )
)
