import { create } from 'zustand'
import { persist } from 'zustand/middleware'
import type { HunkDiffInfo } from '@/types/models'
import { RESEARCH_TAB_PATH } from '@/stores/researchStore'

// --- State types ---

export interface FileData {
  content: string
  diff?: string
  /** Structured per-hunk diff info with staging status (null = not yet fetched). */
  hunks?: HunkDiffInfo[]
  language?: string
  loading: boolean
  error?: string
  isBinary?: boolean
  /**
   * Virtual files are not backed by a path on disk (e.g. a blackboard
   * attachment opened as markdown). The file-viewer data loader skips them so
   * it never tries to read or diff a non-existent path; content is supplied
   * directly via setFileContent.
   */
  virtual?: boolean
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
  /**
   * When `true` the viewer is docked in-flow as a fixed,
   * horizontally-resizable, collapsible panel — the classic layout.
   *
   * When `false` the viewer "floats": it becomes an absolute overlay above the
   * chat area, defaulting to 3/5 of its width, still resizable and collapsible,
   * and auto-collapses when focus moves outside it. Designed for narrow
   * displays where a permanent docked panel steals too much horizontal space.
   */
  pinned: boolean
  highlightLine: number | null // transient: line to scroll to and highlight
  fileIcons: Record<string, FileIconData>
}

interface FileViewerActions {
  openFile: (path: string) => void
  openFileAtLine: (path: string, line: number) => void
  openVirtualFile: (path: string, language?: string) => void
  openResearch: () => void
  closeFile: (path: string) => void
  closeOthersFiles: (keepPath: string) => void
  setActiveFile: (path: string) => void
  setFileContent: (path: string, content: string, language?: string) => void
  setFileDiff: (path: string, diff: string) => void
  setFileHunks: (path: string, hunks: HunkDiffInfo[]) => void
  setFileError: (path: string, error: string) => void
  setFileBinary: (path: string) => void
  setFileLoading: (path: string, loading: boolean) => void
  setWidth: (width: number) => void
  setCollapsed: (collapsed: boolean) => void
  setPinned: (pinned: boolean) => void
  setFileIcon: (path: string, icon: string, iconColor: string) => void
  setHighlightLine: (line: number) => void
  clearHighlightLine: () => void
  closeAllFiles: () => void
  restoreProjectFiles: (openTabs: string[], activeFile: string | null) => void
}

// --- Constants ---

const VIEWER_MIN = 250
const VIEWER_MAX = 900

function getDefaultViewerWidth(): number {
  const screenWidth = typeof window !== 'undefined' ? window.innerWidth : 1440
  return Math.max(VIEWER_MIN, Math.min(VIEWER_MAX, Math.round(screenWidth * 2 / 5)))
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
      // Default UNPINNED (floating): on first start the viewer does not claim
      // permanent docked space and recedes to the slim rail until a file is
      // opened. Users who pin it get their choice persisted and restored.
      pinned: false,
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

      openVirtualFile: (path, language) => {
        const { openTabs } = get()
        // Already open: re-activate and mark loading so the caller's fresh
        // fetch (setFileContent) visibly refreshes content rather than leaving
        // a stale copy in view. Existing content is kept to avoid a flash.
        if (openTabs.includes(path)) {
          set((s) => ({
            activeFile: path,
            collapsed: false,
            files: {
              ...s.files,
              [path]: {
                ...s.files[path],
                loading: true,
                error: undefined,
              } as FileData,
            },
          }))
          return
        }
        set((s) => ({
          openTabs: [...s.openTabs, path],
          activeFile: path,
          collapsed: false,
          files: {
            ...s.files,
            [path]: {
              content: '',
              loading: true,
              virtual: true,
              ...(language !== undefined ? { language } : {}),
            },
          },
        }))
      },

      // Open the Research workspace tab as a synthetic pseudo-path. It is
      // virtual (not backed by a file on disk), so the file-viewer data loader
      // never tries to read it, and FileViewerContent renders the
      // ResearchWorkspace component instead of a code viewer.
      openResearch: () => {
        get().openVirtualFile(RESEARCH_TAB_PATH)
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
          // An unpinned (floating) viewer auto-collapses once it has no open
          // tabs — an empty floating panel would just obscure the chat, so it
          // recedes to the slim docked rail until a file is opened again. When
          // pinned or tabs remain, `collapsed` is left untouched.
          ...(!s.pinned && newTabs.length === 0 ? { collapsed: true } : {}),
        }
      }),

      closeOthersFiles: (keepPath) => set((s) => {
        const keepFile = s.files[keepPath]
        const keepIcon = s.fileIcons[keepPath]
        return {
          openTabs: [keepPath],
          activeFile: keepPath,
          files: keepFile ? { [keepPath]: keepFile } : {},
          fileIcons: keepIcon ? { [keepPath]: keepIcon } : {},
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

      setFileHunks: (path, hunks) => set((s) => {
        const existing = s.files[path]
        if (!existing) return s
        return {
          files: { ...s.files, [path]: { ...existing, hunks } },
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

      setPinned: (pinned) => set({ pinned }),

      setFileIcon: (path, icon, iconColor) => set((s) => ({
        fileIcons: { ...s.fileIcons, [path]: { icon, iconColor } },
      })),

      setHighlightLine: (line) => set({ highlightLine: line }),

      clearHighlightLine: () => set({ highlightLine: null }),

      closeAllFiles: () => set((s) => ({
        files: {},
        openTabs: [],
        activeFile: null,
        fileIcons: {},
        // Same empty-auto-collapse rule as closeFile: an unpinned viewer with
        // no tabs recedes to the docked rail instead of floating empty.
        ...(!s.pinned ? { collapsed: true } : {}),
      })),

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
          // Empty-auto-collapse rule (matches closeFile/closeAllFiles): an
          // unpinned viewer restoring zero tabs recedes to the docked rail.
          ...(!s.pinned && uniqueTabs.length === 0 ? { collapsed: true } : {}),
        }
      }),
    }),
    {
      name: 'c0wrk-file-viewer',
      version: 4,
      migrate: (persistedState, _version) => {
        const state = persistedState as Partial<FileViewerState & FileViewerActions>
        if (_version < 2) {
          state.width = getDefaultViewerWidth()
        }
        if (_version < 3) {
          // Pre-pin viewers default to docked (pinned) — preserves the
          // classic layout for existing users on upgrade.
          state.pinned = true
        }
        // v4: the default for `pinned` changed to false (unpinned floating) so
        // the right viewer does not claim permanent docked space on first
        // start. This migration intentionally does NOT overwrite the persisted
        // value — existing users keep their chosen pin state; only fresh
        // installs (no persisted state) get the new false default from the
        // store initializer above.
        return state as FileViewerState & FileViewerActions
      },
      partialize: (state) => ({
        // Virtual tabs (e.g. blackboard attachments) are ephemeral: their
        // content lives only in memory, so never persist them — otherwise a
        // restart would rehydrate a tab whose content is gone and that the
        // data loader would mistake for a real on-disk file.
        openTabs: state.openTabs.filter(p => !state.files[p]?.virtual),
        activeFile: state.activeFile && state.files[state.activeFile]?.virtual ? null : state.activeFile,
        width: state.width,
        collapsed: state.collapsed,
        pinned: state.pinned,
        fileIcons: state.fileIcons,
      }),
    }
  )
)
