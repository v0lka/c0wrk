import { create } from 'zustand'

type SettingsTab = 'general' | 'llm' | 'search' | 'mcp' | 'security' | 'about'

interface SettingsModalState {
  open: boolean
  activeTab: SettingsTab
  openSettings: (tab?: SettingsTab) => void
  closeSettings: () => void
  setActiveTab: (tab: SettingsTab) => void
}

export const useSettingsStore = create<SettingsModalState>((set) => ({
  open: false,
  activeTab: 'general',
  openSettings: (tab) => set({ open: true, activeTab: tab ?? 'general' }),
  closeSettings: () => set({ open: false }),
  setActiveTab: (tab) => set({ activeTab: tab }),
}))
