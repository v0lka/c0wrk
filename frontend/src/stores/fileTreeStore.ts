import { create } from 'zustand'
import type { FileEntry, GitStatusEntry } from '@/types/models'

// --- State types ---

interface FileTreeState {
  tree: Record<string, FileEntry[]> // directory path -> children
  expandedDirs: Record<string, true>
  searchEntries: FileEntry[]
  gitStatus: Record<string, GitStatusEntry>
  rootPath: string | null
  filterText: string
  filterMode: 'glob' | 'regex'
  isSearching: boolean
  loadingDirs: Record<string, true>
  // Recursive flat listing of every entry under rootPath, cached for the
  // search input so debounced keystrokes do not trigger a fresh listDirectory
  // RPC each time. Invalidated on workspace:tree_changed and project switch.
  flatEntries: FileEntry[]
  flatEntriesRoot: string | null
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
  setFlatEntries: (rootPath: string, entries: FileEntry[]) => void
  clearFlatEntries: () => void
  clearTree: () => void
}

// --- Store ---

export const useFileTreeStore = create<FileTreeState & FileTreeActions>((set, get) => ({
  tree: {},
  expandedDirs: {},
  searchEntries: [],
  gitStatus: {},
  rootPath: null,
  filterText: '',
  filterMode: 'glob',
  isSearching: false,
  loadingDirs: {},
  flatEntries: [],
  flatEntriesRoot: null,

  setRootPath: (path) => set({ rootPath: path }),

  setEntries: (dirPath, entries) => set((s) => ({
    tree: { ...s.tree, [dirPath]: entries },
  })),

  toggleDir: (path) => {
    const { expandedDirs } = get()
    const next = { ...expandedDirs }
    if (next[path]) {
      delete next[path]
    } else {
      next[path] = true
    }
    set({ expandedDirs: next })
  },

  setLoading: (path, loading) => set((s) => {
    const next = { ...s.loadingDirs }
    if (loading) { next[path] = true } else { delete next[path] }
    return { loadingDirs: next }
  }),

  setSearchEntries: (entries) => set({ searchEntries: entries }),

  setGitStatus: (status) => set({ gitStatus: status }),

  setFilterText: (text) => set({ filterText: text }),

  setFilterMode: (mode) => set({ filterMode: mode }),

  setIsSearching: (searching) => set({ isSearching: searching }),

  setFlatEntries: (rootPath, entries) => set({ flatEntries: entries, flatEntriesRoot: rootPath }),

  clearFlatEntries: () => set({ flatEntries: [], flatEntriesRoot: null }),

  clearTree: () => set({
    tree: {},
    expandedDirs: {},
    searchEntries: [],
    gitStatus: {},
    rootPath: null,
    filterText: '',
    isSearching: false,
    loadingDirs: {},
    flatEntries: [],
    flatEntriesRoot: null,
  }),
}))
