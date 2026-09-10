// @vitest-environment jsdom
import { describe, it, expect, vi, beforeEach, afterEach } from 'vitest'
import { act } from 'react'
import { createRoot, type Root } from 'react-dom/client'

// The file tree must not reach the Wails backend: every RPC it makes on
// mount (list / watch / git status) is mocked here. listDirectory is the
// behavior under test — its mock is re-primed per test.
vi.mock('@/api/workspace', () => ({
  listDirectory: vi.fn(),
  getGitStatus: vi.fn(async () => ({})),
  watchDirectory: vi.fn(async () => undefined),
  unwatchDirectory: vi.fn(async () => undefined),
  getSessionWorkspace: vi.fn(async () => '/ws'),
}))
vi.mock('@/api/runtime', () => ({
  // FileTreePanel subscribes to workspace:tree_changed; tests drive reloads
  // directly, so an inert subscription is enough.
  subscribe: vi.fn(() => () => undefined),
}))
// The filter hook owns its own RPC (flat recursive listing); the error-state
// behavior under test does not depend on it — pin it inert.
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
import { listDirectory } from '@/api/workspace'
import { useFileTreeStore } from '@/stores/fileTreeStore'
import { useProjectStore } from '@/stores/projectStore'
import { useSessionStore } from '@/stores/sessionStore'
import type { ProjectInfo } from '@/types/models'

const listDirectoryMock = vi.mocked(listDirectory)

function makeProject(overrides: Partial<ProjectInfo> = {}): ProjectInfo {
  return {
    id: 'p1',
    name: 'rootfs',
    workspace_path: '/ws',
    is_external: true,
    is_no_project: false,
    research_root: '',
    is_research: false,
    created_at: new Date(0).toISOString(),
    last_active_at: new Date(0).toISOString(),
    ...overrides,
  }
}

describe('FileTreePanel — root load failure surfaces an error state', () => {
  let container: HTMLDivElement
  let root: Root | null = null

  beforeEach(() => {
    container = document.createElement('div')
    document.body.appendChild(container)
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

  it('shows the failure message and a Retry action instead of a silently empty tree', async () => {
    listDirectoryMock.mockRejectedValueOnce(new Error('path outside project workspace'))

    await renderPanel()
    await act(async () => {})

    expect(container.textContent).toContain('path outside project workspace')
    const buttons = Array.from(container.querySelectorAll('button'))
    expect(buttons.some((b) => b.textContent?.includes('Retry'))).toBe(true)
    // The old failure mode cached an empty listing and rendered only the
    // faint empty-workspace placeholder — assert the error state replaced it.
    expect(useFileTreeStore.getState().rootLoadError).toBe('path outside project workspace')
    expect(useFileTreeStore.getState().tree['/ws']).toBeUndefined()
  })

  it('recovers when Retry succeeds: entries render and the error clears', async () => {
    listDirectoryMock.mockRejectedValueOnce(new Error('path outside project workspace'))
    listDirectoryMock.mockResolvedValue([
      { name: 'rootfs', path: '/ws/rootfs', is_dir: true, hidden: false, gitignored: false, icon: '', icon_color: '' },
    ])

    await renderPanel()
    await act(async () => {})

    const retry = Array.from(container.querySelectorAll('button')).find(
      (b) => b.textContent?.includes('Retry'),
    )
    expect(retry).toBeDefined()

    await act(async () => {
      retry!.click()
    })
    await act(async () => {})

    expect(container.textContent).toContain('rootfs')
    expect(container.textContent).not.toContain('path outside project workspace')
    expect(useFileTreeStore.getState().rootLoadError).toBeNull()
    expect(useFileTreeStore.getState().tree['/ws']?.[0]?.name).toBe('rootfs')
  })

  it('manual Refresh re-lists the root and clears a stale error', async () => {
    // First (mount) load fails, so the panel boots into the error state...
    listDirectoryMock.mockRejectedValueOnce(new Error('no active project'))
    await renderPanel()
    await act(async () => {})
    expect(useFileTreeStore.getState().rootLoadError).toBe('no active project')

    // ...then the user clicks Refresh and the load succeeds.
    listDirectoryMock.mockResolvedValue([
      { name: 'custom-x', path: '/ws/custom-x', is_dir: true, hidden: false, gitignored: false, icon: '', icon_color: '' },
    ])
    const refresh = container.querySelector<HTMLButtonElement>('button[title="Refresh file tree"]')
    expect(refresh).not.toBeNull()

    await act(async () => {
      refresh!.click()
    })
    await act(async () => {})

    expect(container.textContent).toContain('custom-x')
    expect(useFileTreeStore.getState().rootLoadError).toBeNull()
  })
})
