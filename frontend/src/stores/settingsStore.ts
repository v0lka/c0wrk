import { create } from 'zustand'

type SettingsTab = 'general' | 'llm' | 'search' | 'mcp' | 'security' | 'about'

interface SettingsState {
  open: boolean
  activeTab: SettingsTab
}

interface SettingsActions {
  openSettings: (tab?: SettingsTab) => void
  closeSettings: () => void
  setActiveTab: (tab: SettingsTab) => void
}

export const useSettingsStore = create<SettingsState & SettingsActions>((set) => ({
  open: false,
  activeTab: 'general',
  openSettings: (tab) => set({ open: true, activeTab: tab ?? 'general' }),
  closeSettings: () => set({ open: false }),
  setActiveTab: (tab) => set({ activeTab: tab }),
}))
