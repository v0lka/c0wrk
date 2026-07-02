import { create } from 'zustand'
import { persist } from 'zustand/middleware'

// --- Types ---

export interface GitPanelEntry {
  path: string
  /** Git status code: M=modified, A=added, R=renamed, C=copied, U=unmerged */
  status: string
  staged: boolean
  diffStat: { added: number; deleted: number } | null
}

// --- State types ---

interface GitPanelState {
  viewMode: 'flat' | 'tree'
  entries: GitPanelEntry[]
  commitMessage: string
  branch: string
  expandedDirs: Set<string>
  isLoading: boolean
  isGitRepo: boolean
  error: string | null
}

interface GitPanelActions {
  setViewMode: (mode: 'flat' | 'tree') => void
  setCommitMessage: (message: string) => void
  loadEntries: (entries: GitPanelEntry[]) => void
  toggleStage: (path: string) => void
  setBranch: (branch: string) => void
  setGitRepo: (isRepo: boolean) => void
  setLoading: (loading: boolean) => void
  setError: (error: string | null) => void
  toggleExpandedDir: (dir: string) => void
  reset: () => void
}

// --- Initial state (used by both create and reset) ---

const initialState: GitPanelState = {
  viewMode: 'flat',
  entries: [],
  commitMessage: '',
  branch: '',
  expandedDirs: new Set<string>(),
  isLoading: false,
  isGitRepo: false,
  error: null,
}

// --- Store ---

export const useGitPanelStore = create<GitPanelState & GitPanelActions>()(
  persist(
    (set) => ({
      ...initialState,

      setViewMode: (mode) => set({ viewMode: mode }),

      setCommitMessage: (message) => set({ commitMessage: message }),

      setLoading: (loading) => set({ isLoading: loading }),

      setError: (error) => set({ error }),

      loadEntries: (entries) => set({ entries, isLoading: false, error: null }),

      toggleStage: (path) =>
        set((s) => ({
          entries: s.entries.map((entry) =>
            entry.path === path
              ? { ...entry, staged: !entry.staged }
              : entry,
          ),
        })),

      setBranch: (branch) => set({ branch }),

      setGitRepo: (isRepo) => set({ isGitRepo: isRepo }),

      toggleExpandedDir: (dir) =>
        set((s) => {
          const next = new Set(s.expandedDirs)
          if (next.has(dir)) {
            next.delete(dir)
          } else {
            next.add(dir)
          }
          return { expandedDirs: next }
        }),

      reset: () => set({ ...initialState, expandedDirs: new Set<string>() }),
    }),
    {
      name: 'git-panel-settings',
      partialize: (state) => ({
        viewMode: state.viewMode,
        expandedDirs: Array.from(state.expandedDirs),
      }),
      merge: (persisted, current) => {
        const p = persisted as {
          viewMode?: 'flat' | 'tree'
          expandedDirs?: string[]
        }
        return {
          ...current,
          viewMode: p.viewMode ?? current.viewMode,
          expandedDirs: new Set(p.expandedDirs ?? []),
        }
      },
    },
  ),
)
