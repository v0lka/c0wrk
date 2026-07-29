import { create } from 'zustand'
import type { FileEntry, GitStatusEntry } from '@/types/models'
import type { FilterMode } from '@/lib/pathFilter'

// --- State types ---

interface FileTreeState {
  tree: Record<string, FileEntry[]> // directory path -> children
  expandedDirs: Record<string, true>
  searchEntries: FileEntry[]
  gitStatus: Record<string, GitStatusEntry>
  rootPath: string | null
  filterText: string
  filterMode: FilterMode
  isSearching: boolean
  loadingDirs: Record<string, true>
  // Recursive flat listing of every entry under rootPath, cached for the
  // search input so debounced keystrokes do not trigger a fresh listDirectory
  // RPC each time. Invalidated on workspace:tree_changed and project switch.
  flatEntries: FileEntry[]
  flatEntriesRoot: string | null
  /** Transient: the tree entry currently highlighted as "current" — set by
   *  "Reveal in Workspace" so the target node is scrolled into view and
   *  visually marked. Cleared on user interaction in the tree. */
  selectedPath: string | null
}

interface FileTreeActions {
  setRootPath: (path: string | null) => void
  setEntries: (dirPath: string, entries: FileEntry[]) => void
  toggleDir: (path: string) => void
  setLoading: (path: string, loading: boolean) => void
  setSearchEntries: (entries: FileEntry[]) => void
  setGitStatus: (status: Record<string, GitStatusEntry>) => void
  setFilterText: (text: string) => void
  setFilterMode: (mode: FilterMode) => void
  setIsSearching: (searching: boolean) => void
  setFlatEntries: (rootPath: string, entries: FileEntry[]) => void
  clearFlatEntries: () => void
  clearTree: () => void
  /** Mark the given file path as the "current" tree selection (highlight +
   *  scroll target). Pass null to clear. */
  setSelectedPath: (path: string | null) => void
  /** Ensure every directory in `dirs` is expanded (idempotent — never
   *  collapses an already-expanded dir, unlike toggleDir). Used by
   *  "Reveal in Workspace" to open the ancestor chain. */
  expandDirs: (dirs: string[]) => void
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
  selectedPath: null,

  setRootPath: (path) => set({ rootPath: path }),

  setEntries: (dirPath, entries) => set((s) => ({
    tree: { ...s.tree, [dirPath]: entries },
  })),

  toggleDir: (path) => {
    const { expandedDirs, tree, loadingDirs } = get()
    const next = { ...expandedDirs }
    if (next[path]) {
      // Collapse: remove from expandedDirs and invalidate the cached children
      // for this directory AND all descendants. Without this, re-expanding
      // reuses stale cached data — newly added/removed/renamed entries inside
      // the folder never appear until a full project switch (clearTree).
      // Descendant caches must also be cleared because collapsing a parent
      // hides its children; leaving their caches stale means re-expanding the
      // parent and then a child shows outdated contents.
      delete next[path]
      const prefix = path + '/'
      const nextTree = { ...tree }
      const nextLoading = { ...loadingDirs }
      for (const key of Object.keys(nextTree)) {
        if (key === path || key.startsWith(prefix)) {
          delete nextTree[key]
        }
      }
      for (const key of Object.keys(next)) {
        if (key.startsWith(prefix)) {
          delete next[key]
        }
      }
      for (const key of Object.keys(nextLoading)) {
        if (key === path || key.startsWith(prefix)) {
          delete nextLoading[key]
        }
      }
      set({ expandedDirs: next, tree: nextTree, loadingDirs: nextLoading })
    } else {
      next[path] = true
      set({ expandedDirs: next })
    }
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
    selectedPath: null,
  }),

  setSelectedPath: (path) => set({ selectedPath: path }),

  expandDirs: (dirs) => set((s) => {
    const next = { ...s.expandedDirs }
    let changed = false
    for (const dir of dirs) {
      if (dir && !next[dir]) {
        next[dir] = true
        changed = true
      }
    }
    return changed ? { expandedDirs: next } : s
  }),
}))
