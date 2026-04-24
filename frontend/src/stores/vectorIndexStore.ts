import { create } from 'zustand'
import type { VectorIndexStatus } from '@/types/models'

// --- State types ---

interface VectorIndexState {
  status: VectorIndexStatus
}

interface VectorIndexActions {
  setStatus: (status: VectorIndexStatus) => void
  reset: () => void
}

// --- Defaults ---

const defaultStatus: VectorIndexStatus = {
  state: 'idle',
  progress: 0,
  files_indexed: 0,
  total_files: 0,
}

// --- Store ---

export const useVectorIndexStore = create<VectorIndexState & VectorIndexActions>((set) => ({
  status: { ...defaultStatus },

  setStatus: (status) => set({ status }),

  reset: () => set({ status: { ...defaultStatus } }),
}))
