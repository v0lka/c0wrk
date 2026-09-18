import { create } from 'zustand'
import { persist } from 'zustand/middleware'

/**
 * System (OS-level) notifications are a frontend-only concern: banners are
 * dispatched through the Wails runtime (see `lib/systemNotifications.ts`),
 * so they render through each OS's native notification center and need no
 * webview assets.
 *
 * The preference is persisted to localStorage exactly like the sound store —
 * a desktop UX setting that does not belong in the backend config.yaml.
 */
interface SystemNotificationState {
  /** Master toggle for ALL system notifications. Default: enabled (opt-out,
   *  mirroring the sound pipeline). */
  enabled: boolean
}

interface SystemNotificationActions {
  setEnabled: (enabled: boolean) => void
  toggle: () => void
}

/** Storage key mirrors the persisted-store convention (c0wrk-*). */
const STORAGE_KEY = 'c0wrk-system-notifications'

export const useSystemNotificationStore = create<SystemNotificationState & SystemNotificationActions>()(
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
