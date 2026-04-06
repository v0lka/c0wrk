import { create } from 'zustand'

export interface FileNode {
  name: string
  path: string
  is_dir: boolean
}

interface FileTreeState {
  sessionId: string
  rootPath: string
  entries: Record<string, FileNode[]>
  expandedDirs: Set<string>
  loadingDirs: Set<string>

  initForSession: (sessionId: string) => void
  clearTree: () => void
  setEntries: (path: string, nodes: FileNode[]) => void
  toggleDir: (path: string) => void
  collapseDir: (path: string) => void
  setLoading: (path: string, loading: boolean) => void
  refreshVisibleDirs: () => void
}

async function listDirectory(path: string): Promise<FileNode[]> {
  if (!window?.go?.main?.App?.ListDirectory) return []
  try {
    return await window.go.main.App.ListDirectory(path)
  } catch {
    return []
  }
}

async function watchDirectory(path: string): Promise<void> {
  if (!window?.go?.main?.App?.WatchDirectory) return
  try { await window.go.main.App.WatchDirectory(path) } catch { /* ignore */ }
}

async function unwatchDirectory(path: string): Promise<void> {
  if (!window?.go?.main?.App?.UnwatchDirectory) return
  try { await window.go.main.App.UnwatchDirectory(path) } catch { /* ignore */ }
}

export const useFileTreeStore = create<FileTreeState>((set, get) => ({
  sessionId: '',
  rootPath: '',
  entries: {},
  expandedDirs: new Set<string>(),
  loadingDirs: new Set<string>(),

  initForSession: async (sessionId: string) => {
    // Unwatch all previously watched dirs
    const { expandedDirs, rootPath } = get()
    if (rootPath) {
      unwatchDirectory(rootPath)
      for (const dir of expandedDirs) {
        unwatchDirectory(dir)
      }
    }

    // Reset state
    set({
      sessionId,
      rootPath: '',
      entries: {},
      expandedDirs: new Set<string>(),
      loadingDirs: new Set<string>(),
    })

    if (!sessionId) return
    if (!window?.go?.main?.App?.GetSessionWorkspace) return

    try {
      const root = await window.go.main.App.GetSessionWorkspace(sessionId)
      if (!root || get().sessionId !== sessionId) return

      set({ rootPath: root })

      const nodes = await listDirectory(root)
      if (get().sessionId !== sessionId) return

      set((state) => ({ entries: { ...state.entries, [root]: nodes } }))
      watchDirectory(root)
    } catch {
      // Session workspace not available
    }
  },

  clearTree: () => {
    const { expandedDirs, rootPath } = get()
    if (rootPath) {
      unwatchDirectory(rootPath)
      for (const dir of expandedDirs) {
        unwatchDirectory(dir)
      }
    }
    set({
      sessionId: '',
      rootPath: '',
      entries: {},
      expandedDirs: new Set<string>(),
      loadingDirs: new Set<string>(),
    })
  },

  setEntries: (path: string, nodes: FileNode[]) =>
    set((state) => ({ entries: { ...state.entries, [path]: nodes } })),

  toggleDir: async (path: string) => {
    const { expandedDirs } = get()
    if (expandedDirs.has(path)) {
      const next = new Set(expandedDirs)
      next.delete(path)
      set({ expandedDirs: next })
      unwatchDirectory(path)
    } else {
      const next = new Set(expandedDirs)
      next.add(path)
      set({ expandedDirs: next })
      get().setLoading(path, true)
      const nodes = await listDirectory(path)
      get().setEntries(path, nodes)
      get().setLoading(path, false)
      watchDirectory(path)
    }
  },

  collapseDir: (path: string) => {
    const next = new Set(get().expandedDirs)
    next.delete(path)
    set({ expandedDirs: next })
    unwatchDirectory(path)
  },

  setLoading: (path: string, loading: boolean) =>
    set((state) => {
      const next = new Set(state.loadingDirs)
      if (loading) next.add(path); else next.delete(path)
      return { loadingDirs: next }
    }),

  refreshVisibleDirs: async () => {
    const { rootPath, expandedDirs, sessionId } = get()
    if (!rootPath || !sessionId) return

    const dirsToRefresh = [rootPath, ...expandedDirs]
    const results = await Promise.all(
      dirsToRefresh.map(async (dir) => {
        const nodes = await listDirectory(dir)
        return [dir, nodes] as const
      })
    )

    if (get().sessionId !== sessionId) return // session changed during refresh

    const newEntries = { ...get().entries }
    for (const [dir, nodes] of results) {
      newEntries[dir] = nodes
    }
    set({ entries: newEntries })
  },
}))
