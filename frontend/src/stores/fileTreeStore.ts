import { create } from 'zustand'
import { logger } from '@/lib/logger'

export interface FileNode {
  name: string
  path: string
  is_dir: boolean
}

export interface GitStatusEntry {
  status: string
  staged: boolean
}

interface FileTreeState {
  projectWorkspacePath: string
  rootPath: string
  entries: Record<string, FileNode[]>
  expandedDirs: Set<string>
  loadingDirs: Set<string>
  recursiveEntries: Record<string, FileNode[]>
  recursiveLoading: boolean
  gitStatus: Record<string, GitStatusEntry>

  initForProject: (workspacePath: string) => void
  clearTree: () => void
  setEntries: (path: string, nodes: FileNode[]) => void
  toggleDir: (path: string) => void
  collapseDir: (path: string) => void
  setLoading: (path: string, loading: boolean) => void
  refreshVisibleDirs: () => void
  fetchRecursiveTree: (rootPath: string) => void
  clearRecursiveEntries: () => void
  fetchGitStatus: () => void
}

async function listDirectory(path: string): Promise<FileNode[]> {
  if (!window?.go?.desktop?.App?.ListDirectory) return []
  try {
    return await window.go.desktop.App.ListDirectory(path)
  } catch (err) {
    logger.error(`[fileTreeStore] Failed to list directory ${path}:`, err)
    return []
  }
}

async function listDirectoryRecursive(path: string): Promise<FileNode[]> {
  if (!window?.go?.desktop?.App?.ListDirectoryRecursive) return []
  try {
    return await window.go.desktop.App.ListDirectoryRecursive(path)
  } catch (err) {
    logger.error(`[fileTreeStore] Failed to list directory recursively ${path}:`, err)
    return []
  }
}

async function watchDirectory(path: string): Promise<void> {
  if (!window?.go?.desktop?.App?.WatchDirectory) return
  try { await window.go.desktop.App.WatchDirectory(path) } catch { /* ignore */ }
}

async function unwatchDirectory(path: string): Promise<void> {
  if (!window?.go?.desktop?.App?.UnwatchDirectory) return
  try { await window.go.desktop.App.UnwatchDirectory(path) } catch { /* ignore */ }
}

async function fetchGitStatusFromBackend(path: string): Promise<Record<string, GitStatusEntry>> {
  if (!window?.go?.desktop?.App?.GetGitStatus) return {}
  try {
    return await window.go.desktop.App.GetGitStatus(path)
  } catch (err) {
    logger.error('[fileTreeStore] Failed to fetch git status:', err)
    return {}
  }
}

export const useFileTreeStore = create<FileTreeState>((set, get) => ({
  projectWorkspacePath: '',
  rootPath: '',
  entries: {},
  expandedDirs: new Set<string>(),
  loadingDirs: new Set<string>(),
  recursiveEntries: {},
  recursiveLoading: false,
  gitStatus: {},

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
      recursiveEntries: {},
      recursiveLoading: false,
      gitStatus: {},
    })

    if (!workspacePath) return

    try {
      set({ rootPath: workspacePath })

      const nodes = await listDirectory(workspacePath)
      if (get().projectWorkspacePath !== workspacePath) return

      set((state) => ({ entries: { ...state.entries, [workspacePath]: nodes } }))
      watchDirectory(workspacePath)
      get().fetchGitStatus()
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
      recursiveEntries: {},
      recursiveLoading: false,
      gitStatus: {},
    })
  },

  setEntries: (path: string, nodes: FileNode[]) =>
    set((state) => ({ entries: { ...state.entries, [path]: nodes } })),

  toggleDir: async (path: string) => {
    const { expandedDirs, projectWorkspacePath } = get()
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
      if (get().projectWorkspacePath !== projectWorkspacePath) return // Stale — project changed
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

  fetchRecursiveTree: async (rootPath: string) => {
    const { projectWorkspacePath, recursiveLoading } = get()
    if (!rootPath || !projectWorkspacePath || recursiveLoading) return

    set({ recursiveLoading: true })
    const flatNodes = await listDirectoryRecursive(rootPath)
    if (get().projectWorkspacePath !== projectWorkspacePath) return // project changed

    // Group flat nodes by parent directory
    const grouped: Record<string, FileNode[]> = {}
    for (const node of flatNodes) {
      const lastSep = node.path.lastIndexOf('/')
      const parent = lastSep > 0 ? node.path.substring(0, lastSep) : rootPath
      if (!grouped[parent]) grouped[parent] = []
      grouped[parent].push(node)
    }
    set({ recursiveEntries: grouped, recursiveLoading: false })
  },

  clearRecursiveEntries: () => {
    set({ recursiveEntries: {}, recursiveLoading: false })
  },

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

    set((state) => {
      const newEntries = { ...state.entries }
      for (const [dir, nodes] of results) {
        newEntries[dir] = nodes
      }
      return { entries: newEntries }
    })

    get().fetchGitStatus()
  },

  fetchGitStatus: async () => {
    const { rootPath, projectWorkspacePath } = get()
    if (!rootPath || !projectWorkspacePath) return
    const status = await fetchGitStatusFromBackend(rootPath)
    if (get().projectWorkspacePath !== projectWorkspacePath) return
    set({ gitStatus: status })
  },
}))
