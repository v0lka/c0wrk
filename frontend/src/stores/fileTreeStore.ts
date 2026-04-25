import { create } from 'zustand'
import type { FileEntry, GitStatusEntry } from '@/types/models'

// --- State types ---

interface FileTreeState {
  tree: Record<string, FileEntry[]> // directory path -> children
  expandedDirs: Set<string>
  searchEntries: FileEntry[]
  gitStatus: Record<string, GitStatusEntry>
  rootPath: string | null
  filterText: string
  filterMode: 'glob' | 'regex'
  isSearching: boolean
  loadingDirs: Set<string>
}

interface FileTreeActions {
  setRootPath: (path: string | null) => void
  setEntries: (dirPath: string, entries: FileEntry[]) => void
  toggleDir: (path: string) => void
  setLoading: (path: string, loading: boolean) => void
  setSearchEntries: (entries: FileEntry[]) => void
  setGitStatus: (status: Record<string, GitStatusEntry>) => void
  setFilterText: (text: string) => void
  setFilterMode: (mode: 'glob' | 'regex') => void
  setIsSearching: (searching: boolean) => void
  clearTree: () => void
}

// --- Store ---

export const useFileTreeStore = create<FileTreeState & FileTreeActions>((set, get) => ({
  tree: {},
  expandedDirs: new Set<string>(),
  searchEntries: [],
  gitStatus: {},
  rootPath: null,
  filterText: '',
  filterMode: 'glob',
  isSearching: false,
  loadingDirs: new Set<string>(),

  setRootPath: (path) => set({ rootPath: path }),

  setEntries: (dirPath, entries) => set((s) => ({
    tree: { ...s.tree, [dirPath]: entries },
  })),

  toggleDir: (path) => {
    const { expandedDirs } = get()
    const next = new Set(expandedDirs)
    if (next.has(path)) {
      next.delete(path)
    } else {
      next.add(path)
    }
    set({ expandedDirs: next })
  },

  setLoading: (path, loading) => set((s) => {
    const next = new Set(s.loadingDirs)
    if (loading) next.add(path); else next.delete(path)
    return { loadingDirs: next }
  }),

  setSearchEntries: (entries) => set({ searchEntries: entries }),

  setGitStatus: (status) => set({ gitStatus: status }),

  setFilterText: (text) => set({ filterText: text }),

  setFilterMode: (mode) => set({ filterMode: mode }),

  setIsSearching: (searching) => set({ isSearching: searching }),

  clearTree: () => set({
    tree: {},
    expandedDirs: new Set<string>(),
    searchEntries: [],
    gitStatus: {},
    rootPath: null,
    filterText: '',
    isSearching: false,
    loadingDirs: new Set<string>(),
  }),
}))
