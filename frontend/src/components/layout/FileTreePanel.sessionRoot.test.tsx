// @vitest-environment jsdom
import { describe, it, expect, vi, beforeEach, afterEach } from 'vitest'
import { act } from 'react'
import { createRoot, type Root } from 'react-dom/client'

// The explorer follows the ACTIVE SESSION's execution root (ADR-080
// workspace-coherence step): a managed-worktree session lists its own tree,
// not the project checkout; a delayed answer from a previous session must
// never install that session's root after a switch. All backend RPCs are
// mocked; getSessionWorkspace is the behavior under test.
vi.mock('@/api/workspace', () => ({
  listDirectory: vi.fn(),
  getGitStatus: vi.fn(async () => ({})),
  watchDirectory: vi.fn(async () => undefined),
  unwatchDirectory: vi.fn(async () => undefined),
  getSessionWorkspace: vi.fn(),
}))
vi.mock('@/api/runtime', () => ({
  subscribe: vi.fn(() => () => undefined),
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
vi.mock('./FileIcon', () => ({ FileTree: () => null, FileIcon: () => null }))

import { FileTreePanel } from './FileTreePanel'
import { listDirectory, getSessionWorkspace } from '@/api/workspace'
import { useFileTreeStore } from '@/stores/fileTreeStore'
import { useProjectStore } from '@/stores/projectStore'
import { useSessionStore } from '@/stores/sessionStore'
import type { ProjectInfo, FileEntry } from '@/types/models'

const listDirectoryMock = vi.mocked(listDirectory)
const getSessionWorkspaceMock = vi.mocked(getSessionWorkspace)

function makeProject(): ProjectInfo {
  return {
    id: 'p1',
    name: 'repo',
    workspace_path: '/ws',
    is_external: true,
    is_no_project: false,
    created_at: new Date(0).toISOString(),
    last_active_at: new Date(0).toISOString(),
  }
}

function entry(path: string): FileEntry {
  return { name: path.split('/').pop() ?? path, path, is_dir: false, hidden: false, gitignored: false, icon: '', icon_color: '' }
}

describe('FileTreePanel — the explorer root follows the active session', () => {
  let container: HTMLDivElement
  let root: Root | null = null

  beforeEach(() => {
    container = document.createElement('div')
    document.body.appendChild(container)
    useProjectStore.setState({ projects: [makeProject()], activeProjectId: 'p1' })
    useSessionStore.setState({ activeSessionId: 's1' })
    useFileTreeStore.getState().clearTree()
    listDirectoryMock.mockReset()
    getSessionWorkspaceMock.mockReset()
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
    await act(async () => {})
  }

  it('lists the managed session tree, not the project checkout', async () => {
    getSessionWorkspaceMock.mockResolvedValue('/ws/.worktrees/s-1')
    listDirectoryMock.mockResolvedValue([entry('/ws/.worktrees/s-1/main.go')])

    await renderPanel()

    expect(getSessionWorkspaceMock).toHaveBeenCalledWith('s1')
    expect(listDirectoryMock).toHaveBeenCalledWith('/ws/.worktrees/s-1')
    expect(useFileTreeStore.getState().rootPath).toBe('/ws/.worktrees/s-1')
    expect(container.textContent).toContain('main.go')
  })

  it('falls back to the project workspace while no session is active', async () => {
    useSessionStore.setState({ activeSessionId: null })
    listDirectoryMock.mockResolvedValue([entry('/ws/README.md')])

    await renderPanel()

    expect(getSessionWorkspaceMock).not.toHaveBeenCalled()
    expect(listDirectoryMock).toHaveBeenCalledWith('/ws')
    expect(useFileTreeStore.getState().rootPath).toBe('/ws')
  })

  it('a delayed answer from a previous session cannot install its root after a switch', async () => {
    // Session s1's root RPC never resolves on its own — we hold it.
    let resolveS1: (v: string) => void = () => {}
    getSessionWorkspaceMock.mockImplementationOnce(
      () => new Promise<string>(resolve => { resolveS1 = resolve }),
    )
    listDirectoryMock.mockResolvedValue([])

    await renderPanel()

    // Switch the active session before s1's answer arrives; s2 is a local
    // session whose root is the checkout.
    getSessionWorkspaceMock.mockResolvedValueOnce('/ws')
    await act(async () => {
      useSessionStore.setState({ activeSessionId: 's2' })
    })
    await act(async () => {})

    // The stale s1 answer lands now — after the switch.
    await act(async () => {
      resolveS1('/ws/.worktrees/s-1')
    })
    await act(async () => {})

    expect(useFileTreeStore.getState().rootPath).toBe('/ws')
    expect(listDirectoryMock).not.toHaveBeenCalledWith('/ws/.worktrees/s-1')
  })
})
