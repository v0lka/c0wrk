import { create } from 'zustand'
import type { VectorIndexStatus, VectorStoreEntry } from '@/types/models'

// --- State types ---

interface VectorIndexState {
  status: VectorIndexStatus
  entries: VectorStoreEntry[]
  isLoading: boolean
  query: string
  topK: number
  filePattern: string
}

interface VectorIndexActions {
  setStatus: (status: VectorIndexStatus) => void
  setEntries: (entries: VectorStoreEntry[]) => void
  setLoading: (loading: boolean) => void
  setQuery: (query: string) => void
  setTopK: (topK: number) => void
  setFilePattern: (pattern: string) => void
  clearFilter: () => void
  reset: () => void
}

// --- Defaults ---

const defaultStatus: VectorIndexStatus = {
  state: 'idle',
  progress: 0,
  files_indexed: 0,
  total_files: 0,
}

const DEFAULT_TOP_K = 50

// --- Store ---

export const useVectorIndexStore = create<VectorIndexState & VectorIndexActions>((set) => ({
  status: { ...defaultStatus },
  entries: [],
  isLoading: false,
  query: '',
  topK: DEFAULT_TOP_K,
  filePattern: '',

  setStatus: (status) => set({ status }),

  setEntries: (entries) => set({ entries }),

  setLoading: (loading) => set({ isLoading: loading }),

  setQuery: (query) => set({ query }),

  setTopK: (topK) => set({ topK }),

  setFilePattern: (pattern) => set({ filePattern: pattern }),

  clearFilter: () => set({ query: '', filePattern: '' }),

  reset: () => set({
    status: { ...defaultStatus },
    entries: [],
    isLoading: false,
    query: '',
    topK: DEFAULT_TOP_K,
    filePattern: '',
  }),
}))
