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
  /** Per-message reasoning override. null = send nothing; the provider's server-side default applies. Persisted. */
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
  /**
   * E2S-mode toggle for the next sent message. When true, the backend runs
   * the explicit-execution-state loop instead of the default ReAct flow.
   * Mutually exclusive with goalEnabled (enabling either disables the other
   * — see setE2sEnabled/setGoalEnabled). Unlike goalEnabled it IS persisted:
   * E2S is an experimental workflow preference the user keeps armed across
   * reloads; useMessageSender still resets it after each E2S send.
   */
  e2sEnabled: boolean
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
  /** Set the per-message reasoning override. null = send nothing; the provider's server-side default applies. */
  setSelectedReasoning: (value: string | null) => void
  /** Toggle goal mode for the next sent message. Enabling it disables E2S. */
  setGoalEnabled: (enabled: boolean) => void
  /**
   * Disarm goal mode because the Model Profiles feature has made it unavailable
   * (master toggle AND essential-tools variant both on — see lib/goalGate).
   * Idempotent (a no-op when goal mode is already off). Mirrors the E2S
   * disarm: a gate closing must clear any stale arming rather than leave a
   * toggle armed that the backend would refuse.
   */
  disarmGoal: () => void
  /** Set the goal budget override for the next sent message (JSON or empty). */
  setGoalBudget: (budget: string) => void
  /**
   * Toggle E2S mode for the next sent message. Enabling it disables goal
   * mode (the two modes are mutually exclusive execution strategies).
   */
  setE2sEnabled: (enabled: boolean) => void
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
      e2sEnabled: false,

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
      setGoalEnabled: (enabled) => set(
        // Mutual exclusion: arming goal mode disarms E2S (and vice versa) —
        // the two are alternative execution strategies for the next task, so
        // exactly one may be armed at a time.
        enabled ? { goalEnabled: true, e2sEnabled: false } : { goalEnabled: false },
      ),
      disarmGoal: () => {
        if (get().goalEnabled) set({ goalEnabled: false })
      },
      setGoalBudget: (budget) => set({ goalBudget: budget }),
      setE2sEnabled: (enabled) => set(
        enabled ? { e2sEnabled: true, goalEnabled: false } : { e2sEnabled: false },
      ),
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
        e2sEnabled: state.e2sEnabled,
        // goalEnabled/goalBudget are intentionally NOT persisted: goal mode is
        // first-message-only and per-task, so persisting the toggle would
        // silently activate goal mode on every fresh session. They live in
        // memory only (reset to their defaults on reload). e2sEnabled IS
        // persisted (an experimental workflow preference), and the
        // goal↔E2S mutual exclusion keeps the two from ever being armed
        // together across reloads (goalEnabled always re-arms as false).
      }),
    }
  )
)
