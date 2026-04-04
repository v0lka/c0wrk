import { create } from 'zustand'

interface ScrollStore {
  scrollToStep: ((stepId: string) => void) | null
  setScrollToStep: (fn: ((stepId: string) => void) | null) => void
}

export const useScrollStore = create<ScrollStore>((set) => ({
  scrollToStep: null,
  setScrollToStep: (fn) => set({ scrollToStep: fn }),
}))
