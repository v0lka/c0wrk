import { create } from 'zustand'

/**
 * Experimental-features switch, loaded once from GetConfig and updated in place
 * when the user toggles it in Settings. Its gated features are the E2S
 * execution mode, the embedded local model's frontend surfaces (the
 * Settings → LLM block plus the model's entries in BOTH model pickers — the
 * default-model picker and the chat toolbar's ModelCombobox; backend RPCs stay
 * ungated), and two worktree/focus controls hidden for VISIBILITY only (their
 * functionality is untouched): the chat toolbar's branch/workspace picker
 * (`SessionWorkspaceSelector`) and the Git panel's focus crosshair
 * (`GitFocusButton`). They read availability reactively so hiding/revealing
 * happens within the same session without a reload. Model Profiles is a
 * first-class feature and is NOT gated by this switch.
 *
 * This switch is the SOLE availability gate for E2S — there is no
 * per-feature config toggle. E2S availability is exactly `enabled`, which is
 * DISTINCT from `inputModeStore.e2sEnabled`, the user's per-message ARMED
 * toggle: availability is a config fact, arming is a user preference, and a
 * fail-closed gate must combine BOTH. The composition lives in exactly one
 * place (`lib/e2sGate`) so no call site can substitute one flag for the other.
 */
interface ExperimentalState {
  enabled: boolean
  loaded: boolean
}

interface ExperimentalActions {
  setEnabled: (enabled: boolean) => void
  setLoaded: (loaded: boolean) => void
}

export const useExperimentalStore = create<ExperimentalState & ExperimentalActions>((set) => ({
  enabled: false,
  loaded: false,
  setEnabled: (enabled) => set({ enabled }),
  setLoaded: (loaded) => set({ loaded }),
}))
