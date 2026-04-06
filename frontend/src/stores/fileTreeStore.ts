import { create } from 'zustand'

export interface FileNode {
  name: string
  path: string
  is_dir: boolean
}

interface FileTreeState {
  projectWorkspacePath: string
  rootPath: string
  entries: Record<string, FileNode[]>
  expandedDirs: Set<string>
  loadingDirs: Set<string>

  initForProject: (workspacePath: string) => void
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
  projectWorkspacePath: '',
  rootPath: '',
  entries: {},
  expandedDirs: new Set<string>(),
  loadingDirs: new Set<string>(),

  initForProject: async (workspacePath: string) => {
    const { rootPath: currentRoot } = get()

    // Skip re-init if same workspace path is already loaded
    if (currentRoot && currentRoot === workspacePath) return

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
      projectWorkspacePath: workspacePath,
      rootPath: '',
      entries: {},
      expandedDirs: new Set<string>(),
      loadingDirs: new Set<string>(),
    })

    if (!workspacePath) return

    try {
      set({ rootPath: workspacePath })

      const nodes = await listDirectory(workspacePath)
      if (get().projectWorkspacePath !== workspacePath) return

      set((state) => ({ entries: { ...state.entries, [workspacePath]: nodes } }))
      watchDirectory(workspacePath)
    } catch {
      // Workspace not available
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
      projectWorkspacePath: '',
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
    const { rootPath, expandedDirs, projectWorkspacePath } = get()
    if (!rootPath || !projectWorkspacePath) return

    const dirsToRefresh = [rootPath, ...expandedDirs]
    const results = await Promise.all(
      dirsToRefresh.map(async (dir) => {
        const nodes = await listDirectory(dir)
        return [dir, nodes] as const
      })
    )

    if (get().projectWorkspacePath !== projectWorkspacePath) return // project changed during refresh

    const newEntries = { ...get().entries }
    for (const [dir, nodes] of results) {
      newEntries[dir] = nodes
    }
    set({ entries: newEntries })
  },
}))
