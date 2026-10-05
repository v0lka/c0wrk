import { create } from 'zustand'

/**
 * Model Profiles gate state, loaded from GetConfig alone — the effective/resolved
 * ModelProfiles section plus its advisory identity half (`active_profile` /
 * `resolved_profile_id` / `suggested_profile_id`) — see `useModelProfilesGate`.
 * Single source of truth for the goal-mode block and profile identity /
 * recommendation state. `suggestedProfileId` is reserved surface: no component
 * renders it yet (the Bonsai banner resolves its recommendation through the
 * model catalog, not this field); it stays latched so a future recommendation
 * UI starts from live state.
 *
 * `activeProfileId` is the STORED id, verbatim; `resolvedProfileId` is the id
 * the backend resolver resolved it to (retired-predefined alias / soft
 * fallback applied). Value-wise decisions — the Bonsai banner's "already on
 * the preset?" check — compare against `resolvedProfileId`, never the
 * verbatim id: a legacy dashed id that already resolves to the bonsai preset
 * must not re-trigger the advisory.
 *
 * GetModelProfiles is deliberately NOT a source for this store: it is a
 * consuming read that drains the one-shot profile notices Settings displays,
 * so a background refresh must never issue it.
 *
 * This store carries the following facts:
 *   - `enabled` — the resolved Model Profiles master toggle (config
 *     `model_profiles.enabled`).
 *   - `essentialToolsEnabled` — the resolved essential-tools variant
 *     sub-toggle. When on, goal mode is refused: the narrowing is applied only
 *     to the non-goal Conductor path and the E2S branch (both run after goal
 *     mode's early return), so it never narrows a goal run; if it were applied
 *     to a goal run it would hide the goal-loop tooling (propose_goal,
 *     declare_goal_status, declare_verification) and make the loop unrunnable.
 *   - `loaded` — whether a live (non-startup-race) config has been latched yet.
 *
 * The two feature flags are DISTINCT and both must be on for the goal block;
 * the composition lives in exactly one place (`lib/goalGate`) so no call site
 * can substitute one flag for the other. While `loaded` is false the gate is
 * "unknown" and must NOT block — the backend still enforces the invariant.
 */
interface ModelProfilesGateState {
  enabled: boolean
  essentialToolsEnabled: boolean
  /** null until profile metadata has been fetched successfully. */
  activeProfileId: string | null
  /** Backend-resolved counterpart of `activeProfileId` (alias/fallback applied); null until fetched. */
  resolvedProfileId: string | null
  /** Advisory default-model match only; never auto-applied. Reserved surface — nothing renders it yet (see header). */
  suggestedProfileId: string | null
  loaded: boolean
}

interface ModelProfilesGateActions {
  setEnabled: (enabled: boolean) => void
  setEssentialToolsEnabled: (essentialToolsEnabled: boolean) => void
  setLoaded: (loaded: boolean) => void
  setProfileIds: (activeProfileId: string | null, resolvedProfileId: string | null, suggestedProfileId: string | null) => void
}

export const useModelProfilesGateStore = create<ModelProfilesGateState & ModelProfilesGateActions>((set) => ({
  enabled: false,
  essentialToolsEnabled: false,
  activeProfileId: null,
  resolvedProfileId: null,
  suggestedProfileId: null,
  loaded: false,
  setEnabled: (enabled) => set({ enabled }),
  setEssentialToolsEnabled: (essentialToolsEnabled) => set({ essentialToolsEnabled }),
  setLoaded: (loaded) => set({ loaded }),
  setProfileIds: (activeProfileId, resolvedProfileId, suggestedProfileId) =>
    set({ activeProfileId, resolvedProfileId, suggestedProfileId }),
}))
