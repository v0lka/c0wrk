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
import { useUIStore, selectWorkspaceTab, selectResearchSegment, SIDEBAR_MIN, SIDEBAR_MAX } from '@/stores/uiStore'

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

describe('per-project workspace tab persistence', () => {
  beforeEach(() => {
    localStorage.clear()
    useUIStore.setState({ workspaceTabByProject: {} })
  })

  it('setWorkspaceTab changes only the targeted project', () => {
    useUIStore.setState({ workspaceTabByProject: { p1: 'explorer', p2: 'explorer' } })

    useUIStore.getState().setWorkspaceTab('p1', 'git')

    const map = useUIStore.getState().workspaceTabByProject
    expect(map.p1).toBe('git')
    expect(map.p2).toBe('explorer')
  })

  it('selectWorkspaceTab defaults to "explorer" for unknown projects', () => {
    expect(selectWorkspaceTab({ workspaceTabByProject: {} }, 'missing')).toBe('explorer')

    useUIStore.setState({ workspaceTabByProject: { p1: 'semantics' } })
    expect(selectWorkspaceTab(useUIStore.getState(), 'p1')).toBe('semantics')
    expect(selectWorkspaceTab(useUIStore.getState(), 'p2')).toBe('explorer')
  })

  it('dropProjectTabs removes exactly one project', () => {
    useUIStore.setState({ workspaceTabByProject: { p1: 'git', p2: 'research' } })

    useUIStore.getState().dropProjectTabs('p1')

    expect(useUIStore.getState().workspaceTabByProject).toEqual({ p2: 'research' })
  })

  it('persist version matches the current schema', () => {
    expect(useUIStore.persist.getOptions().version).toBe(6)
  })

  it('same-version corrupt tab values are dropped on rehydrate (merge, not migrate)', async () => {
    // A corrupt/unknown tab in SAME-version storage (hand-edited
    // localStorage) never reaches `migrate` (version matches), so validation
    // must live in `merge` — otherwise the workspace panel renders blank for
    // that project on every launch. Mirrors mergeGitPanel.
    localStorage.setItem(
      SIDEBAR_STORAGE_KEY,
      JSON.stringify({
        state: {
          sidebarCollapsed: false,
          sidebarWidth: 240,
          chatSessionListRatio: 0.5,
          showSessionStats: false,
          workspaceTabByProject: { p1: 'git', p2: 'graph' },
        },
        version: 6,
      }),
    )

    await useUIStore.persist.rehydrate()

    const state = useUIStore.getState()
    expect(state.workspaceTabByProject).toEqual({ p1: 'git' })
    expect(selectWorkspaceTab(state, 'p2')).toBe('explorer')
  })

  it('rehydrate falls back to defaults for wrong-typed scalar fields', async () => {
    useUIStore.setState({
      sidebarCollapsed: false,
      sidebarWidth: 300,
      chatSessionListRatio: 0.5,
      showSessionStats: false,
      workspaceTabByProject: {},
    })
    localStorage.setItem(
      SIDEBAR_STORAGE_KEY,
      JSON.stringify({
        state: {
          sidebarCollapsed: 'yes',
          sidebarWidth: 'wide',
          chatSessionListRatio: null,
          showSessionStats: 1,
          workspaceTabByProject: 'nope',
        },
        version: 5,
      }),
    )

    await useUIStore.persist.rehydrate()

    const state = useUIStore.getState()
    expect(state.sidebarCollapsed).toBe(false)
    expect(state.sidebarWidth).toBe(300)
    expect(state.chatSessionListRatio).toBe(0.5)
    expect(state.showSessionStats).toBe(false)
    expect(state.workspaceTabByProject).toEqual({})
  })

  it('migrate validates workspaceTabByProject entry-by-entry and drops unknown tab values', async () => {
    // A payload written by a hypothetical newer build (version > 6) carrying
    // an unknown tab ('graph') and a non-string value alongside valid ones:
    // the migration must keep only the known values, mirroring the
    // fail-closed mergeGitPanel contract — an unknown tab matches no
    // TabsTrigger/TabsContent and would render a blank workspace panel.
    localStorage.setItem(
      SIDEBAR_STORAGE_KEY,
      JSON.stringify({
        state: {
          sidebarCollapsed: true,
          sidebarWidth: 250,
          chatSessionListRatio: 0.4,
          showSessionStats: true,
          workspaceTabByProject: {
            p1: 'git',
            p2: 'graph',
            p3: 'research',
            p4: 42,
          },
        },
        version: 7,
      }),
    )

    await useUIStore.persist.rehydrate()

    const state = useUIStore.getState()
    expect(state.sidebarCollapsed).toBe(true)
    expect(state.sidebarWidth).toBe(250)
    expect(state.workspaceTabByProject).toEqual({ p1: 'git', p3: 'research' })
    // The dropped project falls back to the default tab via the selector.
    expect(selectWorkspaceTab(state, 'p2')).toBe('explorer')
  })

  it('migrates a v4 payload: keeps sidebar fields and yields an empty tab map', async () => {
    localStorage.setItem(
      SIDEBAR_STORAGE_KEY,
      JSON.stringify({
        state: {
          sidebarCollapsed: true,
          sidebarWidth: 250,
          chatSessionListRatio: 0.3,
          showSessionStats: true,
        },
        version: 4,
      }),
    )

    await useUIStore.persist.rehydrate()

    const state = useUIStore.getState()
    expect(state.sidebarCollapsed).toBe(true)
    expect(state.sidebarWidth).toBe(250)
    expect(state.chatSessionListRatio).toBe(0.3)
    expect(state.showSessionStats).toBe(true)
    expect(state.workspaceTabByProject).toEqual({})
  })

  it('persist round-trip preserves the per-project tab map', async () => {
    useUIStore.getState().setWorkspaceTab('p1', 'git')
    useUIStore.getState().setWorkspaceTab('p2', 'research')

    const persisted = localStorage.getItem(SIDEBAR_STORAGE_KEY)
    expect(JSON.parse(persisted ?? '{}').state.workspaceTabByProject).toEqual({
      p1: 'git',
      p2: 'research',
    })

    useUIStore.setState({ workspaceTabByProject: {} })
    localStorage.setItem(SIDEBAR_STORAGE_KEY, persisted!)

    await useUIStore.persist.rehydrate()

    expect(useUIStore.getState().workspaceTabByProject).toEqual({
      p1: 'git',
      p2: 'research',
    })
  })
})

describe('per-project research segment persistence', () => {
  beforeEach(() => {
    localStorage.clear()
    useUIStore.setState({ researchSegmentByProject: {} })
  })

  it('setResearchSegment changes only the targeted project', () => {
    useUIStore.setState({
      researchSegmentByProject: { p1: 'dashboard', p2: 'dashboard' },
    })

    useUIStore.getState().setResearchSegment('p1', 'papers')

    const map = useUIStore.getState().researchSegmentByProject
    expect(map.p1).toBe('papers')
    expect(map.p2).toBe('dashboard')
  })

  it('selectResearchSegment defaults to "dashboard"', () => {
    expect(selectResearchSegment({ researchSegmentByProject: {} }, 'missing')).toBe('dashboard')
    expect(selectResearchSegment({ researchSegmentByProject: {} }, null)).toBe('dashboard')

    useUIStore.setState({ researchSegmentByProject: { p1: 'papers' } })
    expect(selectResearchSegment(useUIStore.getState(), 'p1')).toBe('papers')
    expect(selectResearchSegment(useUIStore.getState(), 'p2')).toBe('dashboard')
  })

  it('setResearchSegment is a no-op when the segment is unchanged', () => {
    useUIStore.setState({ researchSegmentByProject: { p1: 'papers' } })
    const before = useUIStore.getState().researchSegmentByProject

    useUIStore.getState().setResearchSegment('p1', 'papers')

    expect(useUIStore.getState().researchSegmentByProject).toBe(before)
  })

  it("dropProjectTabs also forgets a project's research segment", () => {
    useUIStore.setState({
      workspaceTabByProject: { p1: 'research', p2: 'research' },
      researchSegmentByProject: { p1: 'papers', p2: 'dashboard' },
    })

    useUIStore.getState().dropProjectTabs('p1')

    expect(useUIStore.getState().workspaceTabByProject).toEqual({ p2: 'research' })
    expect(useUIStore.getState().researchSegmentByProject).toEqual({ p2: 'dashboard' })
  })

  it('same-version corrupt segment values are dropped on rehydrate (merge)', async () => {
    localStorage.setItem(
      SIDEBAR_STORAGE_KEY,
      JSON.stringify({
        state: {
          sidebarCollapsed: false,
          sidebarWidth: 240,
          chatSessionListRatio: 0.5,
          showSessionStats: false,
          workspaceTabByProject: { p1: 'research' },
          researchSegmentByProject: { p1: 'papers', p2: 'reader', p3: 7 },
        },
        version: 6,
      }),
    )

    await useUIStore.persist.rehydrate()

    const state = useUIStore.getState()
    expect(state.researchSegmentByProject).toEqual({ p1: 'papers' })
    // The dropped project falls back to the default segment via the selector.
    expect(selectResearchSegment(state, 'p2')).toBe('dashboard')
  })

  it('migrates a v5 payload: keeps sidebar fields and yields an empty segment map', async () => {
    localStorage.setItem(
      SIDEBAR_STORAGE_KEY,
      JSON.stringify({
        state: {
          sidebarCollapsed: true,
          sidebarWidth: 250,
          chatSessionListRatio: 0.3,
          showSessionStats: true,
          workspaceTabByProject: { p1: 'research' },
        },
        version: 5,
      }),
    )

    await useUIStore.persist.rehydrate()

    const state = useUIStore.getState()
    expect(state.sidebarCollapsed).toBe(true)
    expect(state.workspaceTabByProject).toEqual({ p1: 'research' })
    expect(state.researchSegmentByProject).toEqual({})
  })

  it('persist round-trip preserves the per-project segment map', async () => {
    useUIStore.getState().setResearchSegment('p1', 'papers')
    useUIStore.getState().setResearchSegment('p2', 'dashboard')

    const persisted = localStorage.getItem(SIDEBAR_STORAGE_KEY)
    expect(JSON.parse(persisted ?? '{}').state.researchSegmentByProject).toEqual({
      p1: 'papers',
      p2: 'dashboard',
    })

    useUIStore.setState({ researchSegmentByProject: {} })
    localStorage.setItem(SIDEBAR_STORAGE_KEY, persisted!)

    await useUIStore.persist.rehydrate()

    expect(useUIStore.getState().researchSegmentByProject).toEqual({
      p1: 'papers',
      p2: 'dashboard',
    })
  })
})
