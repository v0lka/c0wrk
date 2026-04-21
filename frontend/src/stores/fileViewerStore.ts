import { create } from 'zustand'
import { logger } from '@/lib/logger'
import { detectLanguage, registerLanguages } from '@/lib/hljsLanguages'

// Ensure hljs languages are registered on first import
registerLanguages()

// -- Types -------------------------------------------------------------------

export interface OpenFile {
  path: string       // absolute path (used as key)
  name: string       // filename only
  content: string    // file content from ReadFile
  diff: string       // raw unified diff from GetFileDiff
  language: string   // detected language for highlight.js
  isBinary: boolean  // detected by content inspection
  isLoading: boolean
  error: string | null
}

interface PersistedState {
  openFiles: Array<{ path: string; name: string }>
  activeFilePath: string | null
  panelWidth: number
  isCollapsed: boolean
}

interface FileViewerState {
  openFiles: OpenFile[]
  activeFilePath: string | null
  panelWidth: number
  isCollapsed: boolean

  openFile: (path: string, name: string) => Promise<void>
  closeFile: (path: string) => void
  setActiveFile: (path: string) => void
  closeAllFiles: () => void
  setPanelWidth: (width: number) => void
  toggleCollapsed: () => void
  refreshFile: (path: string) => Promise<void>
  refreshAllFiles: () => Promise<void>
  silentRefreshAllFiles: () => Promise<void>
}

// -- Constants ---------------------------------------------------------------

const STORAGE_KEY = 'c0wrk-file-viewer'
const DEFAULT_PANEL_WIDTH = 500
const BINARY_CHECK_SIZE = 8192 // check first 8KB for null bytes

// -- Helpers -----------------------------------------------------------------

function isBinaryContent(content: string): boolean {
  // Check first 8KB for null bytes (common binary indicator)
  const checkSlice = content.slice(0, BINARY_CHECK_SIZE)
  for (let i = 0; i < checkSlice.length; i++) {
    if (checkSlice.charCodeAt(i) === 0) return true
  }
  return false
}

async function readFileContent(filePath: string): Promise<{ content: string; error: string | null }> {
  try {
    const content = await window.go.desktop.App.ReadFile(filePath)
    return { content, error: null }
  } catch (err) {
    const msg = err instanceof Error ? err.message : String(err)
    return { content: '', error: msg }
  }
}

async function readFileDiff(filePath: string): Promise<string> {
  try {
    return await window.go.desktop.App.GetFileDiff(filePath)
  } catch {
    return ''
  }
}

// -- Persistence -------------------------------------------------------------

function loadPersistedState(): PersistedState | null {
  try {
    const raw = localStorage.getItem(STORAGE_KEY)
    if (!raw) return null
    return JSON.parse(raw) as PersistedState
  } catch {
    return null
  }
}

function persistState(state: Pick<FileViewerState, 'openFiles' | 'activeFilePath' | 'panelWidth' | 'isCollapsed'>): void {
  try {
    const data: PersistedState = {
      openFiles: state.openFiles.map(f => ({ path: f.path, name: f.name })),
      activeFilePath: state.activeFilePath,
      panelWidth: state.panelWidth,
      isCollapsed: state.isCollapsed,
    }
    localStorage.setItem(STORAGE_KEY, JSON.stringify(data))
  } catch (err) {
    logger.error('[fileViewerStore] Failed to persist state:', err)
  }
}

// -- Store -------------------------------------------------------------------

export const useFileViewerStore = create<FileViewerState>((set, get) => ({
  openFiles: [],
  activeFilePath: null,
  panelWidth: DEFAULT_PANEL_WIDTH,
  isCollapsed: false,

  openFile: async (path: string, name: string) => {
    const { openFiles } = get()

    // If file is already open, just activate it
    const existing = openFiles.find(f => f.path === path)
    if (existing) {
      set({ activeFilePath: path })
      return
    }

    const language = detectLanguage(name)
    const newFile: OpenFile = {
      path,
      name,
      content: '',
      diff: '',
      language,
      isBinary: false,
      isLoading: true,
      error: null,
    }

    // Add file immediately with loading state
    const updatedFiles = [...openFiles, newFile]
    set({ openFiles: updatedFiles, activeFilePath: path, isCollapsed: false })

    // Load content and diff asynchronously
    const [contentResult, diff] = await Promise.all([
      readFileContent(path),
      readFileDiff(path),
    ])

    if (contentResult.error) {
      set((state) => ({
        openFiles: state.openFiles.map(f =>
          f.path === path ? { ...f, isLoading: false, error: contentResult.error } : f
        ),
      }))
      return
    }

    const isBinary = isBinaryContent(contentResult.content)

    set((state) => ({
      openFiles: state.openFiles.map(f =>
        f.path === path
          ? { ...f, content: contentResult.content, diff, isBinary, isLoading: false }
          : f
      ),
    }))

    // Persist after content loaded
    persistState(get())
  },

  closeFile: (path: string) => {
    const { openFiles, activeFilePath } = get()
    const idx = openFiles.findIndex(f => f.path === path)
    const newFiles = openFiles.filter(f => f.path !== path)

    let newActive = activeFilePath
    if (activeFilePath === path) {
      // Activate adjacent tab: right, then left
      if (idx < newFiles.length) {
        newActive = newFiles[idx]?.path ?? null
      } else if (newFiles.length > 0) {
        newActive = newFiles[newFiles.length - 1]!.path
      } else {
        newActive = null
      }
    }

    set({ openFiles: newFiles, activeFilePath: newActive })
    persistState({ ...get(), openFiles: newFiles, activeFilePath: newActive })
  },

  setActiveFile: (path: string) => {
    set({ activeFilePath: path })
    persistState(get())
  },

  closeAllFiles: () => {
    set({ openFiles: [], activeFilePath: null })
    persistState({ openFiles: [], activeFilePath: null, panelWidth: get().panelWidth, isCollapsed: get().isCollapsed })
  },

  setPanelWidth: (width: number) => {
    set({ panelWidth: width })
    persistState(get())
  },

  toggleCollapsed: () => {
    set((state) => {
      const next = !state.isCollapsed
      persistState({ ...get(), isCollapsed: next })
      return { isCollapsed: next }
    })
  },

  refreshFile: async (path: string) => {
    const { openFiles } = get()
    const file = openFiles.find(f => f.path === path)
    if (!file) return

    set((state) => ({
      openFiles: state.openFiles.map(f =>
        f.path === path ? { ...f, isLoading: true, error: null } : f
      ),
    }))

    const [contentResult, diff] = await Promise.all([
      readFileContent(path),
      readFileDiff(path),
    ])

    if (contentResult.error) {
      set((state) => ({
        openFiles: state.openFiles.map(f =>
          f.path === path ? { ...f, isLoading: false, error: contentResult.error } : f
        ),
      }))
      return
    }

    const isBinary = isBinaryContent(contentResult.content)

    set((state) => ({
      openFiles: state.openFiles.map(f =>
        f.path === path
          ? { ...f, content: contentResult.content, diff, isBinary, isLoading: false }
          : f
      ),
    }))
  },

  refreshAllFiles: async () => {
    const { openFiles } = get()
    await Promise.all(openFiles.map(f => get().refreshFile(f.path)))
  },

  // Silent refresh: updates content/diff without showing loading spinner.
  // Used for auto-refresh on file changes to avoid visual disruption.
  silentRefreshAllFiles: async () => {
    const { openFiles } = get()
    const results = await Promise.all(
      openFiles.map(async (file) => {
        const [contentResult, diff] = await Promise.all([
          readFileContent(file.path),
          readFileDiff(file.path),
        ])
        return { path: file.path, contentResult, diff }
      })
    )

    set((state) => {
      const newFiles = state.openFiles.map(f => {
        const result = results.find(r => r.path === f.path)
        if (!result || result.contentResult.error) return f
        const isBinary = isBinaryContent(result.contentResult.content)
        return { ...f, content: result.contentResult.content, diff: result.diff, isBinary }
      })
      return { openFiles: newFiles }
    })
  },
}))

// -- Lazy init from persisted state ------------------------------------------

const persisted = loadPersistedState()
if (persisted) {
  // Set panel width immediately
  if (persisted.panelWidth) {
    useFileViewerStore.setState({ panelWidth: persisted.panelWidth })
  }

  // Restore collapsed state
  if (persisted.isCollapsed) {
    useFileViewerStore.setState({ isCollapsed: persisted.isCollapsed })
  }

  // Re-open persisted files lazily (don't block UI)
  if (persisted.openFiles.length > 0) {
    const store = useFileViewerStore.getState()
    // Queue file opens — each will load content async
    for (const { path, name } of persisted.openFiles) {
      store.openFile(path, name)
    }
    // Restore active file after queueing
    if (persisted.activeFilePath) {
      useFileViewerStore.setState({ activeFilePath: persisted.activeFilePath })
    }
  }
}
