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
import { useUIStore } from '@/stores/uiStore'

const SIDEBAR_STORAGE_KEY = 'c0wrk-sidebar-collapsed'
const FILE_VIEWER_STORAGE_KEY = 'c0wrk-file-viewer'

describe('side panel persistence', () => {
  beforeEach(() => {
    localStorage.clear()
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
