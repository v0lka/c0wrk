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
  closeAllFiles: () => void
  restoreProjectFiles: (openTabs: string[], activeFile: string | null) => void
}

// --- Constants ---

const VIEWER_MIN = 250
const VIEWER_MAX = 900

function getDefaultViewerWidth(): number {
  const screenWidth = typeof window !== 'undefined' ? window.innerWidth : 1440
  return Math.max(VIEWER_MIN, Math.min(VIEWER_MAX, Math.round(screenWidth * 3 / 6)))
}

// --- Store ---

export const useFileViewerStore = create<FileViewerState & FileViewerActions>()(
  persist(
    (set, get) => ({
      files: {},
      openTabs: [],
      activeFile: null,
      width: getDefaultViewerWidth(),
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

      closeAllFiles: () => set({
        files: {},
        openTabs: [],
        activeFile: null,
        fileIcons: {},
      }),

      restoreProjectFiles: (openTabs, activeFile) => set((s) => {
        const uniqueTabs = openTabs.filter((tab, idx) => openTabs.indexOf(tab) === idx)
        const normalizedActive = activeFile && uniqueTabs.includes(activeFile) ? activeFile : (uniqueTabs[0] ?? null)

        const nextFiles: Record<string, FileData> = {}
        for (const tab of uniqueTabs) {
          const existing = s.files[tab]
          nextFiles[tab] = existing ?? { content: '', loading: true }
        }

        const nextIcons: Record<string, FileIconData> = {}
        for (const tab of uniqueTabs) {
          const icon = s.fileIcons[tab]
          if (icon) nextIcons[tab] = icon
        }

        if (
          s.openTabs.length === uniqueTabs.length
          && s.openTabs.every((tab, i) => tab === uniqueTabs[i])
          && s.activeFile === normalizedActive
        ) {
          return s
        }

        return {
          openTabs: uniqueTabs,
          activeFile: normalizedActive,
          files: nextFiles,
          fileIcons: nextIcons,
          collapsed: uniqueTabs.length === 0 ? s.collapsed : false,
        }
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
