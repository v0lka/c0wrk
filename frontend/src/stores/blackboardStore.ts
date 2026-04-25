import { create } from 'zustand'
import type { BlackboardState } from '@/types/models'

// --- State types ---

interface BlackboardStoreState {
  state: BlackboardState | null
  loading: boolean
  error: string | null
}

interface BlackboardStoreActions {
  setState: (state: BlackboardState | null) => void
  setLoading: (loading: boolean) => void
  setError: (error: string | null) => void
  clear: () => void
}

// --- Stable selectors (return primitives or direct references) ---

const selectState = (s: BlackboardStoreState): BlackboardState | null => s.state
const selectLoading = (s: BlackboardStoreState): boolean => s.loading
const selectError = (s: BlackboardStoreState): string | null => s.error
const selectHasState = (s: BlackboardStoreState): boolean => s.state !== null

export function useBlackboardState(): BlackboardState | null {
  return useBlackboardStore(selectState)
}

export function useBlackboardLoading(): boolean {
  return useBlackboardStore(selectLoading)
}

export function useBlackboardError(): string | null {
  return useBlackboardStore(selectError)
}

export function useHasBlackboardState(): boolean {
  return useBlackboardStore(selectHasState)
}

// --- Store ---

export const useBlackboardStore = create<BlackboardStoreState & BlackboardStoreActions>((set) => ({
  state: null,
  loading: false,
  error: null,

  setState: (state) => set({ state, error: null }),
  setLoading: (loading) => set({ loading }),
  setError: (error) => set({ error, loading: false }),
  clear: () => set({ state: null, loading: false, error: null }),
}))
