// @vitest-environment jsdom
import { beforeEach, describe, expect, it, vi } from 'vitest'

vi.hoisted(() => {
  const g = globalThis as Record<string, unknown>
  const win = (g.window as Record<string, unknown> | undefined) ?? g
  const map = new Map<string, string>()
  win.localStorage = {
    getItem: (key: string) => map.get(key) ?? null,
    setItem: (key: string, value: string) => { map.set(key, value) },
    removeItem: (key: string) => { map.delete(key) },
    clear: () => map.clear(),
    key: (index: number) => Array.from(map.keys())[index] ?? null,
    get length() { return map.size },
  }
})

import { useFileViewerStore } from '@/stores/fileViewerStore'
import { useUIStore, SIDEBAR_MIN, SIDEBAR_MAX } from '@/stores/uiStore'

const SIDEBAR_STORAGE_KEY = 'c0wrk-sidebar-collapsed'
const FILE_VIEWER_STORAGE_KEY = 'c0wrk-file-viewer'

describe('side panel persistence', () => {
  beforeEach(() => {
    useUIStore.setState({ sidebarCollapsed: false })
    useFileViewerStore.setState({ collapsed: false })
    localStorage.clear()
  })

  it('persists and rehydrates both collapsed states', async () => {
    useUIStore.getState().setSidebarCollapsed(true)
    useFileViewerStore.getState().setCollapsed(true)

    const persistedSidebar = localStorage.getItem(SIDEBAR_STORAGE_KEY)
    const persistedFileViewer = localStorage.getItem(FILE_VIEWER_STORAGE_KEY)

    expect(JSON.parse(persistedSidebar ?? '{}').state.sidebarCollapsed).toBe(true)
    expect(JSON.parse(persistedFileViewer ?? '{}').state.collapsed).toBe(true)

    useUIStore.setState({ sidebarCollapsed: false })
    useFileViewerStore.setState({ collapsed: false })
    localStorage.setItem(SIDEBAR_STORAGE_KEY, persistedSidebar!)
    localStorage.setItem(FILE_VIEWER_STORAGE_KEY, persistedFileViewer!)

    await useUIStore.persist.rehydrate()
    await useFileViewerStore.persist.rehydrate()

    expect(useUIStore.getState().sidebarCollapsed).toBe(true)
    expect(useFileViewerStore.getState().collapsed).toBe(true)
  })
})

describe('sidebar width persistence', () => {
  beforeEach(() => {
    useUIStore.setState({
      sidebarCollapsed: false,
      sidebarWidth: 300,
      chatSessionListRatio: 0.5,
    })
    localStorage.clear()
  })

  it('persists and rehydrates sidebarWidth', async () => {
    useUIStore.getState().setSidebarWidth(375)

    const persisted = localStorage.getItem(SIDEBAR_STORAGE_KEY)
    expect(JSON.parse(persisted ?? '{}').state.sidebarWidth).toBe(375)

    useUIStore.setState({ sidebarWidth: 200 })
    localStorage.setItem(SIDEBAR_STORAGE_KEY, persisted!)

    await useUIStore.persist.rehydrate()

    expect(useUIStore.getState().sidebarWidth).toBe(375)
  })

  it('clamps sidebarWidth to [SIDEBAR_MIN, SIDEBAR_MAX]', () => {
    useUIStore.getState().setSidebarWidth(50)
    expect(useUIStore.getState().sidebarWidth).toBe(SIDEBAR_MIN)

    useUIStore.getState().setSidebarWidth(9999)
    expect(useUIStore.getState().sidebarWidth).toBe(SIDEBAR_MAX)
  })
})

describe('file viewer pin default & empty auto-collapse', () => {
  beforeEach(() => {
    localStorage.clear()
    useFileViewerStore.setState({
      files: {},
      openTabs: [],
      activeFile: null,
      collapsed: false,
      pinned: false,
      highlightLine: null,
      fileIcons: {},
    })
  })

  it('defaults to unpinned (pinned=false) on first start', async () => {
    localStorage.clear()
    await useFileViewerStore.persist.rehydrate()
    expect(useFileViewerStore.getState().pinned).toBe(false)
  })

  it('preserves an existing user pin=true on rehydrate (migration does not force)', async () => {
    // Simulate a previously-persisted v3 store where the user pinned the viewer.
    localStorage.setItem(
      FILE_VIEWER_STORAGE_KEY,
      JSON.stringify({ state: { pinned: true }, version: 3 }),
    )
    await useFileViewerStore.persist.rehydrate()
    expect(useFileViewerStore.getState().pinned).toBe(true)
  })

  it('auto-collapses when unpinned and the last tab is closed', () => {
    useFileViewerStore.getState().openFile('/a.ts')
    useFileViewerStore.setState({ pinned: false, collapsed: false })

    useFileViewerStore.getState().closeFile('/a.ts')

    expect(useFileViewerStore.getState().openTabs).toHaveLength(0)
    expect(useFileViewerStore.getState().collapsed).toBe(true)
  })

  it('does NOT auto-collapse when pinned and the last tab is closed', () => {
    useFileViewerStore.getState().openFile('/a.ts')
    useFileViewerStore.setState({ pinned: true, collapsed: false })

    useFileViewerStore.getState().closeFile('/a.ts')

    expect(useFileViewerStore.getState().openTabs).toHaveLength(0)
    expect(useFileViewerStore.getState().collapsed).toBe(false)
  })

  it('auto-collapses on closeAllFiles when unpinned', () => {
    useFileViewerStore.getState().openFile('/a.ts')
    useFileViewerStore.getState().openFile('/b.ts')
    useFileViewerStore.setState({ pinned: false, collapsed: false })

    useFileViewerStore.getState().closeAllFiles()

    expect(useFileViewerStore.getState().collapsed).toBe(true)
  })

  it('stays expanded on closeAllFiles when pinned', () => {
    useFileViewerStore.getState().openFile('/a.ts')
    useFileViewerStore.setState({ pinned: true, collapsed: false })

    useFileViewerStore.getState().closeAllFiles()

    expect(useFileViewerStore.getState().collapsed).toBe(false)
  })

  it('opening a file expands a collapsed unpinned viewer', () => {
    useFileViewerStore.setState({
      pinned: false,
      collapsed: true,
      openTabs: [],
      activeFile: null,
    })

    useFileViewerStore.getState().openFile('/c.ts')

    expect(useFileViewerStore.getState().collapsed).toBe(false)
    expect(useFileViewerStore.getState().activeFile).toBe('/c.ts')
  })
})
