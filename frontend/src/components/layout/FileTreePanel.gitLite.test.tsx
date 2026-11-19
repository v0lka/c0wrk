// @vitest-environment jsdom
import { describe, it, expect, vi, beforeEach, afterEach } from 'vitest'
import { act } from 'react'
import { createRoot, type Root } from 'react-dom/client'

// The file tree must not reach the Wails backend: every RPC it makes on
// mount (list / watch / git status) is mocked here. This suite pins the
// LITE status contract: the tree consumes GetGitStatusLite (untracked
// directories collapsed) and inherits the untracked verdict down to the
// entries it lists, and the workspace:ignore_ready delivery reloads the
// listings without touching git status.
vi.mock('@/api/workspace', () => ({
  listDirectory: vi.fn(),
  getGitStatus: vi.fn(async () => ({})),
  getGitStatusLite: vi.fn(async () => ({})),
  watchDirectory: vi.fn(async () => undefined),
  unwatchDirectory: vi.fn(async () => undefined),
  getSessionWorkspace: vi.fn(async () => '/ws'),
}))
const eventHandlers = new Map<string, (...args: unknown[]) => void>()
vi.mock('@/api/runtime', () => ({
  subscribe: vi.fn((name: string, cb: (...args: unknown[]) => void) => {
    eventHandlers.set(name, cb)
    return () => eventHandlers.delete(name)
  }),
}))
vi.mock('@/hooks/useFileSearch', () => ({
  useFileSearch: () => ({
    filterText: '',
    filterMode: 'glob',
    isInvalidFilter: false,
    handleFilterChange: vi.fn(),
    toggleFilterMode: vi.fn(),
  }),
}))
vi.mock('./FileTreeContextMenu', () => ({ FileTreeContextMenu: () => null }))
vi.mock('./FileIcon', () => ({ FileIcon: () => null }))

import { FileTreePanel } from './FileTreePanel'
import { getGitStatusLite, listDirectory } from '@/api/workspace'
import { useFileTreeStore } from '@/stores/fileTreeStore'
import { useProjectStore } from '@/stores/projectStore'
import { useSessionStore } from '@/stores/sessionStore'
import type { FileEntry, ProjectInfo } from '@/types/models'

const listDirectoryMock = vi.mocked(listDirectory)
const getGitStatusLiteMock = vi.mocked(getGitStatusLite)

function makeProject(overrides: Partial<ProjectInfo> = {}): ProjectInfo {
  return {
    id: 'p1',
    name: 'repo',
    workspace_path: '/ws',
    is_external: true,
    is_no_project: false,
    created_at: new Date(0).toISOString(),
    last_active_at: new Date(0).toISOString(),
    ...overrides,
  }
}

function entry(name: string, path: string, isDir: boolean): FileEntry {
  return { name, path, is_dir: isDir, hidden: false, gitignored: false, icon: '', icon_color: '' }
}

describe('FileTreePanel — lite git status and ignore_ready delivery', () => {
  let container: HTMLDivElement
  let root: Root | null = null

  beforeEach(() => {
    container = document.createElement('div')
    document.body.appendChild(container)
    eventHandlers.clear()
    useProjectStore.setState({
      projects: [makeProject()],
      activeProjectId: 'p1',
    })
    useSessionStore.setState({ activeSessionId: 's1' })
    useFileTreeStore.getState().clearTree()
  })

  afterEach(() => {
    act(() => {
      root?.unmount()
    })
    root = null
    container.remove()
    vi.clearAllMocks()
  })

  async function renderPanel(): Promise<void> {
    root = createRoot(container)
    await act(async () => {
      root!.render(<FileTreePanel />)
    })
  }

  function nameSpanFor(name: string): HTMLSpanElement | undefined {
    const row = Array.from(container.querySelectorAll<HTMLDivElement>('div[role="treeitem"]')).find(
      (r) => r.textContent?.includes(name) ?? false,
    )
    return row?.querySelector<HTMLSpanElement>('span.truncate') ?? undefined
  }

  it('consumes GetGitStatusLite and inherits the untracked verdict beneath a collapsed dir', async () => {
    listDirectoryMock.mockImplementation(async (dir: string) => {
      if (dir === '/ws') return [entry('tracked.txt', '/ws/tracked.txt', false), entry('newmod', '/ws/newmod', true)]
      if (dir === '/ws/newmod') return [entry('a.txt', '/ws/newmod/a.txt', false)]
      return []
    })

    await renderPanel()
    await act(async () => {})

    // The tree's status source is the LITE RPC, not the panel's full one.
    expect(getGitStatusLiteMock).toHaveBeenCalledWith('/ws')

    act(() => {
      useFileTreeStore.getState().setGitStatus({
        '/ws/tracked.txt': { status: 'M', staged: false, index_status: '', worktree_status: 'M' },
        '/ws/newmod': { status: 'A', staged: false, index_status: '', worktree_status: '?' },
      })
    })
    await act(async () => {})

    const tracked = nameSpanFor('tracked.txt')
    const newmod = nameSpanFor('newmod')
    expect(tracked?.className).toContain('text-warning')
    expect(newmod?.className).toContain('text-success')

    // Expand the collapsed untracked dir: its children are NOT in the lite
    // map, but the untracked verdict inherits down to the listed entries.
    const newmodRow = Array.from(container.querySelectorAll<HTMLDivElement>('div[role="treeitem"]')).find(
      (r) => r.textContent?.includes('newmod') ?? false,
    )
    await act(async () => {
      newmodRow!.click()
    })
    await act(async () => {})
    const child = nameSpanFor('a.txt')
    expect(child?.className).toContain('text-success')
    const childRow = Array.from(container.querySelectorAll<HTMLDivElement>('div[role="treeitem"]')).find(
      (r) => r.textContent?.includes('a.txt') ?? false,
    )
    expect(childRow?.textContent).toContain('A')
  })

  it('reloads listings (no git status) on workspace:ignore_ready for the active root', async () => {
    listDirectoryMock.mockResolvedValue([entry('tracked.txt', '/ws/tracked.txt', false)])

    await renderPanel()
    await act(async () => {})
    const callsAfterMount = listDirectoryMock.mock.calls.length
    const statusCallsAfterMount = getGitStatusLiteMock.mock.calls.length
    expect(eventHandlers.has('workspace:ignore_ready')).toBe(true)

    await act(async () => {
      eventHandlers.get('workspace:ignore_ready')!('/ws')
    })
    await act(async () => {})
    expect(listDirectoryMock.mock.calls.length).toBeGreaterThan(callsAfterMount)
    expect(getGitStatusLiteMock.mock.calls.length).toBe(statusCallsAfterMount)

    // A stale delivery from another root must not refetch the current tree.
    const callsBeforeStale = listDirectoryMock.mock.calls.length
    await act(async () => {
      eventHandlers.get('workspace:ignore_ready')!('/other-project')
    })
    await act(async () => {})
    expect(listDirectoryMock.mock.calls.length).toBe(callsBeforeStale)
  })
})
