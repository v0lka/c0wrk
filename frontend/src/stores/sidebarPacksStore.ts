import { create } from 'zustand'

/**
 * Sidebar surface gates (config `research.enabled` / `specs.enabled`): the
 * single frontend gate for the workspace panel's Research and Specs tabs.
 * Loaded once from GetConfig by `useSidebarPacks`; after the initial
 * hydration, Settings (`ResearchSpecsSettings`) keeps the store in sync
 * directly via `setResearch`/`setSpecs` after every successful save — the
 * tabs flip in the same beat as the persisted config (the backend also
 * announces `config:updated`, which re-hydrates as a belt-and-braces sync).
 *
 * Both gates default to TRUE — the backend's resolved default for a config
 * that predates the knobs (absent = on, the historical always-seeded
 * behavior). While `loaded` is false (config not yet read — the
 * splash/startup race), the gated tabs therefore render as they always did
 * and snap to the persisted values once the config lands; only an explicit
 * opt-out hides a tab (and unseeds its skill/agent pack, backend-side).
 */
interface SidebarPacksState {
  research: boolean
  specs: boolean
  loaded: boolean
}

interface SidebarPacksActions {
  setResearch: (enabled: boolean) => void
  setSpecs: (enabled: boolean) => void
  setLoaded: (loaded: boolean) => void
}

export const useSidebarPacksStore = create<SidebarPacksState & SidebarPacksActions>((set) => ({
  research: true,
  specs: true,
  loaded: false,
  setResearch: (research) => set({ research }),
  setSpecs: (specs) => set({ specs }),
  setLoaded: (loaded) => set({ loaded }),
}))
