import { create } from 'zustand'
import { normalizeAutonomyMode } from '@/lib/autonomyModes'
import type { AutonomyMode, SilentModeSettings } from '@/types/models'

/**
 * Autonomy posture (security.autonomy_mode + security.silent_mode) — the
 * app-wide cache of how gated decisions resolve.
 *
 * The backend RPC (GetSecuritySettings / UpdateSecuritySettings) is the source
 * of truth; this store mirrors experimentalStore: it is hydrated once at
 * startup (useAutonomyLoader) and updated in place when the user edits the
 * mode in Settings → Security, so both the loader and the Settings component
 * keep it in sync.
 *
 * The autonomy mode owns liveness: the silent-mode sub-policies are live only
 * while the mode is "silent" — there is no separate master switch. All three
 * sub-policies are enforced entirely in the backend; the mode flag itself is
 * what gates the frontend's one post-task UI interception (the auto-review
 * loop reopen, see isSilentMode).
 */

/** Documented sub-policy defaults — mirror config.SilentModeDefaults. */
export const DEFAULT_SILENT_POLICIES: SilentModeSettings = {
  tool_confirm: { mode: 'judge' },
  step_limit: { mode: 'auto' },
  ask_user: { mode: 'disable' },
}

/** Documented autonomy-mode default — mirrors config.AutonomyModeStandard. */
export const DEFAULT_AUTONOMY_MODE: AutonomyMode = 'standard'

/**
 * The whole posture a GetSecuritySettings read (or a Settings save) carries:
 * the mode plus the silent sub-policies. Every field is optional; the setter
 * fills gaps with the documented defaults.
 */
export interface AutonomyPosture extends SilentModeSettings {
  autonomy_mode: AutonomyMode
}

interface AutonomyState extends AutonomyPosture {
  /** True once an authoritative read (GetSecuritySettings) has landed. */
  loaded: boolean
}

interface AutonomyActions {
  /**
   * Replace the posture (from GetSecuritySettings or a Settings save).
   * Missing sub-policies fall back to the documented defaults and an
   * unknown autonomy mode fails safe to "standard", so a partial or drifting
   * payload can never leave the store in an autonomous posture.
   */
  setAutonomy: (posture: Partial<AutonomyPosture>) => void
  setLoaded: (loaded: boolean) => void
}

export const useAutonomyStore = create<AutonomyState & AutonomyActions>((set) => ({
  autonomy_mode: DEFAULT_AUTONOMY_MODE,
  ...DEFAULT_SILENT_POLICIES,
  loaded: false,
  setAutonomy: (posture) =>
    set({
      autonomy_mode: normalizeAutonomyMode(posture.autonomy_mode ?? DEFAULT_AUTONOMY_MODE),
      tool_confirm: posture.tool_confirm ?? DEFAULT_SILENT_POLICIES.tool_confirm,
      step_limit: posture.step_limit ?? DEFAULT_SILENT_POLICIES.step_limit,
      ask_user: posture.ask_user ?? DEFAULT_SILENT_POLICIES.ask_user,
      loaded: true,
    }),
  setLoaded: (loaded) => set({ loaded }),
}))

/**
 * Whether the app runs in silent (unattended) mode — no post-task UI
 * interception at all. Read imperatively by event handlers (the flag is
 * global, hydrated at startup). In the standard and assisted modes the
 * post-task review reopen behaves exactly as before.
 */
export function isSilentMode(): boolean {
  return useAutonomyStore.getState().autonomy_mode === 'silent'
}
