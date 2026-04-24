import { create } from 'zustand'
import { persist } from 'zustand/middleware'

// --- State types ---

export interface FileData {
  content: string
  diff?: string
  language?: string
  loading: boolean
  error?: string
  isBinary?: boolean
}

interface FileIconData {
  icon: string
  iconColor: string
}

interface FileViewerState {
  files: Record<string, FileData> // keyed by file path
  openTabs: string[] // ordered file paths
  activeFile: string | null
  width: number
  collapsed: boolean
  highlightLine: number | null // transient: line to scroll to and highlight
  fileIcons: Record<string, FileIconData>
}

interface FileViewerActions {
  openFile: (path: string) => void
  openFileAtLine: (path: string, line: number) => void
  closeFile: (path: string) => void
  setActiveFile: (path: string) => void
  setFileContent: (path: string, content: string, language?: string) => void
  setFileDiff: (path: string, diff: string) => void
  setFileError: (path: string, error: string) => void
  setFileBinary: (path: string) => void
  setFileLoading: (path: string, loading: boolean) => void
  setWidth: (width: number) => void
  setCollapsed: (collapsed: boolean) => void
  setFileIcon: (path: string, icon: string, iconColor: string) => void
  clearHighlightLine: () => void
  refreshAllFiles: () => void
  closeAllFiles: () => void
}

// --- Constants ---

const DEFAULT_WIDTH = 500

// --- Store ---

export const useFileViewerStore = create<FileViewerState & FileViewerActions>()(
  persist(
    (set, get) => ({
      files: {},
      openTabs: [],
      activeFile: null,
      width: DEFAULT_WIDTH,
      collapsed: false,
      highlightLine: null,
      fileIcons: {},

      openFile: (path) => {
        const { openTabs } = get()
        if (openTabs.includes(path)) {
          set({ activeFile: path, collapsed: false })
          return
        }
        set((s) => ({
          openTabs: [...s.openTabs, path],
          activeFile: path,
          collapsed: false,
          files: {
            ...s.files,
            [path]: { content: '', loading: true },
          },
        }))
      },

      openFileAtLine: (path, line) => {
        const { openTabs } = get()
        if (openTabs.includes(path)) {
          set({ activeFile: path, collapsed: false, highlightLine: line })
          return
        }
        set((s) => ({
          openTabs: [...s.openTabs, path],
          activeFile: path,
          collapsed: false,
          highlightLine: line,
          files: {
            ...s.files,
            [path]: { content: '', loading: true },
          },
        }))
      },

      closeFile: (path) => set((s) => {
        const idx = s.openTabs.indexOf(path)
        const newTabs = s.openTabs.filter(p => p !== path)
        const { [path]: _, ...restFiles } = s.files
        const { [path]: __, ...restIcons } = s.fileIcons

        let newActive = s.activeFile
        if (s.activeFile === path) {
          if (idx < newTabs.length) {
            newActive = newTabs[idx] ?? null
          } else if (newTabs.length > 0) {
            newActive = newTabs[newTabs.length - 1] ?? null
          } else {
            newActive = null
          }
        }

        return {
          openTabs: newTabs,
          activeFile: newActive,
          files: restFiles,
          fileIcons: restIcons,
        }
      }),

      setActiveFile: (path) => set({ activeFile: path }),

      setFileContent: (path, content, language) => set((s) => {
        const existing = s.files[path]
        return {
          files: {
            ...s.files,
            [path]: {
              ...existing,
              content,
              loading: false,
              error: undefined,
              ...(language !== undefined ? { language } : {}),
            } as FileData,
          },
        }
      }),

      setFileDiff: (path, diff) => set((s) => {
        const existing = s.files[path]
        if (!existing) return s
        return {
          files: { ...s.files, [path]: { ...existing, diff } },
        }
      }),

      setFileError: (path, error) => set((s) => {
        const existing = s.files[path]
        return {
          files: {
            ...s.files,
            [path]: { ...existing, loading: false, error } as FileData,
          },
        }
      }),

      setFileBinary: (path) => set((s) => {
        const existing = s.files[path]
        return {
          files: {
            ...s.files,
            [path]: { ...existing, loading: false, isBinary: true } as FileData,
          },
        }
      }),

      setFileLoading: (path, loading) => set((s) => {
        const existing = s.files[path]
        if (!existing) return s
        return {
          files: { ...s.files, [path]: { ...existing, loading } },
        }
      }),

      setWidth: (width) => set({ width }),

      setCollapsed: (collapsed) => set({ collapsed }),

      setFileIcon: (path, icon, iconColor) => set((s) => ({
        fileIcons: { ...s.fileIcons, [path]: { icon, iconColor } },
      })),

      clearHighlightLine: () => set({ highlightLine: null }),

      refreshAllFiles: () => set((s) => {
        const updated: Record<string, FileData> = {}
        for (const [path, data] of Object.entries(s.files)) {
          updated[path] = { ...data, loading: true }
        }
        return { files: updated }
      }),

      closeAllFiles: () => set({
        files: {},
        openTabs: [],
        activeFile: null,
        fileIcons: {},
      }),
    }),
    {
      name: 'c0wrk-file-viewer',
      partialize: (state) => ({
        openTabs: state.openTabs,
        activeFile: state.activeFile,
        width: state.width,
        collapsed: state.collapsed,
        fileIcons: state.fileIcons,
      }),
    }
  )
)
