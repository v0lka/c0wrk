import { create } from 'zustand'
import { persist, createJSONStorage } from 'zustand/middleware'
import type { IndexPhase, SearchMode, VectorIndexStatus, VectorStoreEntry } from '@/types/models'

// --- State types ---

interface VectorIndexState {
  status: VectorIndexStatus
  entries: VectorStoreEntry[]
  isLoading: boolean
  query: string
  topK: number
  filePattern: string
  mustMatch: string[]
  mode: SearchMode
  phase?: IndexPhase
}

interface VectorIndexActions {
  setStatus: (status: VectorIndexStatus) => void
  setEntries: (entries: VectorStoreEntry[]) => void
  setLoading: (loading: boolean) => void
  setQuery: (query: string) => void
  setTopK: (topK: number) => void
  setFilePattern: (pattern: string) => void
  setMustMatch: (tokens: string[]) => void
  addMustMatch: (token: string) => void
  removeMustMatch: (token: string) => void
  setMode: (mode: SearchMode) => void
  setPhase: (phase: IndexPhase | undefined) => void
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
const DEFAULT_MODE: SearchMode = 'hybrid'

// Fallback storage shim for non-browser environments (e.g. vitest 'node'
// runner) so the persist middleware doesn't log warnings when
// localStorage is undefined.
const inMemoryStorage: Storage = (() => {
  const map = new Map<string, string>()
  return {
    get length() { return map.size },
    clear: () => { map.clear() },
    getItem: (k) => map.get(k) ?? null,
    setItem: (k, v) => { map.set(k, v) },
    removeItem: (k) => { map.delete(k) },
    key: (i) => Array.from(map.keys())[i] ?? null,
  }
})()

function resolveStorage(): Storage {
  try {
    if (typeof window !== 'undefined' && window.localStorage) {
      return window.localStorage
    }
  } catch {
    // access to localStorage can throw in some sandboxed environments
  }
  return inMemoryStorage
}

// --- Store ---

// Only `mode` is persisted — it's a user preference that should survive reloads.
// Status/entries/filters are session-scoped and reset on project switch.
export const useVectorIndexStore = create<VectorIndexState & VectorIndexActions>()(
  persist(
    (set, get) => ({
      status: { ...defaultStatus },
      entries: [],
      isLoading: false,
      query: '',
      topK: DEFAULT_TOP_K,
      filePattern: '',
      mustMatch: [],
      mode: DEFAULT_MODE,
      phase: undefined,

      setStatus: (status) => set({ status }),

      setEntries: (entries) => set({ entries }),

      setLoading: (loading) => set({ isLoading: loading }),

      setQuery: (query) => set({ query }),

      setTopK: (topK) => set({ topK }),

      setFilePattern: (pattern) => set({ filePattern: pattern }),

      setMustMatch: (tokens) => set({ mustMatch: tokens }),

      addMustMatch: (token) => {
        const trimmed = token.trim()
        if (!trimmed) return
        const existing = get().mustMatch
        if (existing.includes(trimmed)) return
        set({ mustMatch: [...existing, trimmed] })
      },

      removeMustMatch: (token) => {
        const existing = get().mustMatch
        const next = existing.filter((t) => t !== token)
        if (next.length === existing.length) return
        set({ mustMatch: next })
      },

      setMode: (mode) => set({ mode }),

      setPhase: (phase) => set({ phase }),

      clearFilter: () => set({ query: '', filePattern: '', mustMatch: [] }),

      reset: () => set({
        status: { ...defaultStatus },
        entries: [],
        isLoading: false,
        query: '',
        topK: DEFAULT_TOP_K,
        filePattern: '',
        mustMatch: [],
        phase: undefined,
      }),
    }),
    {
      name: 'c0wrk-vector-index',
      partialize: (state) => ({ mode: state.mode }),
      storage: createJSONStorage(resolveStorage),
    },
  ),
)
