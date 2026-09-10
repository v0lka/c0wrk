import { create } from 'zustand'

/**
 * Master experimental-features switch, loaded once from GetConfig and updated
 * in place when the user toggles it in Settings. The only gated feature today
 * is the Small-LLM profile: its settings tab reads `enabled` reactively so
 * hiding/revealing happens within the same session without a reload.
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
