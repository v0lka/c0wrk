import { create } from 'zustand'
import { persist } from 'zustand/middleware'

/**
 * Sound notifications are a frontend-only concern: tones are synthesized in
 * the webview via the Web Audio API (see `lib/sound.ts`), so they are
 * byte-identical across macOS / Windows / Linux and need no audio assets.
 *
 * The preference is persisted to localStorage exactly like the theme store —
 * a desktop UX setting that does not belong in the backend config.yaml.
 */
interface SoundState {
  /** Master toggle for ALL sound notifications. Default: enabled. */
  enabled: boolean
}

interface SoundActions {
  setEnabled: (enabled: boolean) => void
  toggle: () => void
}

/** Storage key mirrors the persisted-store convention (c0wrk-*). */
const STORAGE_KEY = 'c0wrk-sound'

export const useSoundStore = create<SoundState & SoundActions>()(
  persist(
    (set, get) => ({
      enabled: true,

      setEnabled: (enabled) => set({ enabled }),
      toggle: () => set({ enabled: !get().enabled }),
    }),
    {
      name: STORAGE_KEY,
      version: 1,
      // Bump version and implement migration when adding/removing/renaming persisted fields.
      migrate: (persistedState, _version) => persistedState,
      partialize: (state) => ({ enabled: state.enabled }),
    },
  ),
)
