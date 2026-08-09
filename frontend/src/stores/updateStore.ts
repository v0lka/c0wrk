// Self-update UI state.
//
// A small state machine driving the update toast/dialog. The store is
// intentionally narrow: it only holds the data the UI needs to render, and
// every action is a pure transition that replaces state slices by value. RPC
// side effects (checkForUpdates / downloadUpdate / applyUpdate / skipVersion)
// live in @/api/updater; this store is updated exclusively through the event
// handlers wired in @/hooks/useUpdateChecker.
//
// Referential stability (React 19 useSyncExternalStore): every selector below
// must return either a primitive or a direct store property reference. Never
// allocate arrays/objects inside a selector — that creates a fresh reference on
// every snapshot read and causes an infinite re-render loop (React error #185).
// The provided selector hooks (useUpdatePhase, useUpdateProgress, …) each
// subscribe to a single primitive slice and are safe to use directly.

import { create } from 'zustand'
import type { UpdateInfo } from '@/api/updater'

/** Update UI phase. Drives which toast surface is shown. */
export type UpdatePhase = 'idle' | 'available' | 'downloading' | 'downloaded' | 'error'

/** Download progress snapshot. `total === 0` means the size is unknown. */
export interface UpdateProgress {
  readonly done: number
  readonly total: number
}

export interface UpdateState {
  /** Current UI phase. */
  phase: UpdatePhase
  /** The check result for the available release (null unless phase !== idle). */
  info: UpdateInfo | null
  /** Running version, populated from GetUpdateSettings on mount. */
  currentVersion: string
  /** Download progress (null unless phase === 'downloading'). */
  progress: UpdateProgress | null
  /** Human-readable error message (null unless phase === 'error'). */
  errorMessage: string | null
  /** True while an explicit check is in flight (Settings button feedback). */
  isChecking: boolean
  /** True while a download is being initiated (button disable feedback). */
  isDownloading: boolean
}

export interface UpdateActions {
  /** A newer release was reported (update:available). */
  onAvailable: (info: UpdateInfo) => void
  /** Download progress tick (update:progress). */
  onProgress: (done: number, total: number) => void
  /** Archive downloaded & verified (update:downloaded). */
  onDownloaded: () => void
  /** An update step failed (update:error). */
  onError: (message: string) => void
  /** No newer release found (update:none). */
  onNone: () => void
  /** Record the running build version (from GetUpdateSettings). */
  setCurrentVersion: (version: string) => void
  /** Toggle the explicit-check spinner (Settings button feedback). */
  setChecking: (checking: boolean) => void
  /** Toggle the download-initiation busy flag. */
  setDownloading: (downloading: boolean) => void
  /** Hide the toast and return to idle (user dismissed). */
  dismiss: () => void
}

export type UpdateStore = UpdateState & UpdateActions

// --- Defaults ---

const initialState: UpdateState = {
  phase: 'idle',
  info: null,
  currentVersion: '',
  progress: null,
  errorMessage: null,
  isChecking: false,
  isDownloading: false,
}

// --- Store ---

export const useUpdateStore = create<UpdateStore>()((set) => ({
  ...initialState,

  onAvailable: (info) =>
    set({
      phase: 'available',
      info,
      progress: null,
      errorMessage: null,
      isChecking: false,
      isDownloading: false,
    }),

  onProgress: (done, total) =>
    set({
      phase: 'downloading',
      progress: { done, total },
      errorMessage: null,
    }),

  onDownloaded: () =>
    set({
      phase: 'downloaded',
      progress: null,
      errorMessage: null,
      isDownloading: false,
    }),

  onError: (message) =>
    set({
      phase: 'error',
      errorMessage: message,
      isChecking: false,
      isDownloading: false,
    }),

  onNone: () =>
    set({
      phase: 'idle',
      info: null,
      progress: null,
      errorMessage: null,
      isChecking: false,
    }),

  setCurrentVersion: (version) => set({ currentVersion: version }),

  setChecking: (checking) => set({ isChecking: checking }),

  setDownloading: (downloading) => set({ isDownloading: downloading }),

  dismiss: () =>
    set({
      phase: 'idle',
      info: null,
      progress: null,
      errorMessage: null,
    }),
}))

// --- Selector hooks (each subscribes to a single primitive slice) ---
//
// These exist to make referential stability explicit and ergonomic. Each
// returns a primitive or a direct store reference, so useSyncExternalStore's
// snapshot comparison never sees a fresh allocation. For composite needs,
// subscribe to the individual hooks and derive with useMemo in the component.

/** The current update phase. Primitive — safe. */
export function useUpdatePhase(): UpdatePhase {
  return useUpdateStore((s) => s.phase)
}

/** The release info, or null. Direct store reference — safe. */
export function useUpdateInfo(): UpdateInfo | null {
  return useUpdateStore((s) => s.info)
}

/** The running build version. Primitive — safe. */
export function useCurrentVersion(): string {
  return useUpdateStore((s) => s.currentVersion)
}

/** Download progress, or null. Direct store reference — safe. */
export function useUpdateProgress(): UpdateProgress | null {
  return useUpdateStore((s) => s.progress)
}

/** Error message, or null. Primitive — safe. */
export function useUpdateError(): string | null {
  return useUpdateStore((s) => s.errorMessage)
}

/** Explicit-check spinner flag. Primitive — safe. */
export function useUpdateChecking(): boolean {
  return useUpdateStore((s) => s.isChecking)
}

/** Download-initiation busy flag. Primitive — safe. */
export function useUpdateDownloading(): boolean {
  return useUpdateStore((s) => s.isDownloading)
}
