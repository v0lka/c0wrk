// @vitest-environment jsdom
//
// Tests for the file-tree mutation wiring (issue #60): the context menu's
// callbacks drive the panel-owned inline input and the delete-confirmation
// dialog; commits hit the api wrappers with the right paths; failures keep
// the edit open with a surfaced message; the toolbar's two reveal buttons
// mirror the tree's root/pending semantics. The context menu itself is
// replaced by a prop-capturing fake (its own behaviors live in
// FileTreeContextMenu*.test.tsx).

import { describe, it, expect, vi, beforeEach, afterEach } from 'vitest'
import { act } from 'react'
import { createRoot, type Root } from 'react-dom/client'

vi.mock('@/api/workspace', () => ({
  listDirectory: vi.fn(),
  getGitStatus: vi.fn(async () => ({})),
  getGitStatusLite: vi.fn(async () => ({})),
  watchDirectory: vi.fn(async () => undefined),
  unwatchDirectory: vi.fn(async () => undefined),
  getSessionWorkspace: vi.fn(),
  createFile: vi.fn(async () => undefined),
  createDirectory: vi.fn(async () => undefined),
  renameEntry: vi.fn(async () => undefined),
  deleteEntry: vi.fn(async () => undefined),
  revealInFileManager: vi.fn(async () => undefined),
}))
vi.mock('@/api/runtime', () => ({
  subscribe: vi.fn(() => () => undefined),
  emit: vi.fn(),
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
// Capture the menu props so tests drive the panel's callbacks directly.
let capturedMenuProps: Record<string, unknown> | null = null
vi.mock('./FileTreeContextMenu', () => ({
  FileTreeContextMenu: (props: Record<string, unknown>) => {
    capturedMenuProps = props
    return null
  },
}))
vi.mock('./FileIcon', () => ({ FileTree: () => null, FileIcon: () => null }))
// The failure-path tests below deliberately drive the panel's logger.error —
// intercept at the source so the expected diagnostics stay out of the output.
vi.mock('@/lib/logger', () => ({
  logger: { error: vi.fn(), warn: vi.fn(), info: vi.fn(), debug: vi.fn() },
}))

import { FileTreePanel } from './FileTreePanel'
import {
  listDirectory,
  getSessionWorkspace,
  createFile,
  createDirectory,
  renameEntry,
  deleteEntry,
  revealInFileManager,
} from '@/api/workspace'
import { useFileTreeStore } from '@/stores/fileTreeStore'
import { useProjectStore } from '@/stores/projectStore'
import { useSessionStore } from '@/stores/sessionStore'
import type { ProjectInfo, FileEntry } from '@/types/models'

const listDirectoryMock = vi.mocked(listDirectory)
const getSessionWorkspaceMock = vi.mocked(getSessionWorkspace)
const createFileMock = vi.mocked(createFile)
const createDirectoryMock = vi.mocked(createDirectory)
const renameEntryMock = vi.mocked(renameEntry)
const deleteEntryMock = vi.mocked(deleteEntry)
const revealMock = vi.mocked(revealInFileManager)

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

function entry(path: string, isDir = false): FileEntry {
  return { name: path.split('/').pop() ?? path, path, is_dir: isDir, hidden: false, gitignored: false, icon: '', icon_color: '' }
}

describe('FileTreePanel — tree mutations', () => {
  let container: HTMLDivElement
  let root: Root | null = null

  beforeEach(() => {
    document.body.innerHTML = ''
    container = document.createElement('div')
    document.body.appendChild(container)
    useProjectStore.setState({ projects: [makeProject()], activeProjectId: 'p1' })
    useSessionStore.setState({ activeSessionId: 's1' })
    useFileTreeStore.getState().clearTree()
    capturedMenuProps = null
    // Path-aware listing: expandAndLoad('/ws/sub') must return DIFFERENT
    // children than the root's, or the tree would recurse into itself.
    listDirectoryMock.mockImplementation(async (dir: string) => {
      if (dir === '/ws') return [entry('/ws/a.txt'), entry('/ws/sub', true)]
      if (dir === '/ws/sub') return [entry('/ws/sub/inner.txt')]
      return []
    })
    getSessionWorkspaceMock.mockResolvedValue('/ws')
    vi.clearAllMocks()
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

  function requireMenuProps(): Record<string, unknown> {
    if (!capturedMenuProps) throw new Error('context menu props not captured')
    return capturedMenuProps
  }

  function inlineInput(): HTMLInputElement {
    const el = container.querySelector<HTMLInputElement>('[data-testid="file-tree-inline-input"] input')
    if (!el) throw new Error('inline input not rendered')
    return el
  }

  function type(value: string): void {
    // React's value tracker swallows programmatic input events — go through
    // the native setter (the house input-typing pattern).
    const setter = Object.getOwnPropertyDescriptor(window.HTMLInputElement.prototype, 'value')!.set!
    act(() => {
      setter.call(inlineInput(), value)
      inlineInput().dispatchEvent(new Event('input', { bubbles: true }))
    })
  }

  function pressEnter(): void {
    act(() => {
      inlineInput().dispatchEvent(new KeyboardEvent('keydown', { key: 'Enter', bubbles: true }))
    })
  }

  // Radix Dialog portals content into document.body.
  function buttonByText(label: string): HTMLButtonElement | undefined {
    return Array.from(document.body.querySelectorAll<HTMLButtonElement>('button')).find(
      (b) => b.textContent?.trim() === label,
    )
  }

  it('creates a file at the root through the inline input', async () => {
    await renderPanel()
    await act(async () => { (requireMenuProps().onRequestCreate as (dir: string, kind: string) => void)('/ws', 'file') })

    type('new.txt')
    pressEnter()
    await act(async () => {})

    expect(createFileMock).toHaveBeenCalledWith('s1', '/ws/new.txt')
    // The commit clears the edit; the watcher-driven reload refreshes the tree.
    expect(container.querySelector('[data-testid="file-tree-inline-input"]')).toBeNull()
  })

  it('creates a folder inside a collapsed directory, expanding it first', async () => {
    await renderPanel()
    await act(async () => { (requireMenuProps().onRequestCreate as (dir: string, kind: string) => void)('/ws/sub', 'folder') })
    await act(async () => {})

    // expandAndLoad listed the target dir so the input can render inside it.
    expect(listDirectoryMock).toHaveBeenCalledWith('/ws/sub')
    type('inner')
    pressEnter()
    await act(async () => {})

    expect(createDirectoryMock).toHaveBeenCalledWith('s1', '/ws/sub/inner')
  })

  it('renames an entry in place via the inline input', async () => {
    await renderPanel()
    await act(async () => { (requireMenuProps().onRequestRename as (entry: FileEntry) => void)(entry('/ws/a.txt')) })

    // The current name is pre-filled; typing replaces it.
    type('b.txt')
    pressEnter()
    await act(async () => {})

    expect(renameEntryMock).toHaveBeenCalledWith('s1', '/ws/a.txt', '/ws/b.txt')
    expect(container.querySelector('[data-testid="file-tree-inline-input"]')).toBeNull()
  })

  it('blocks a colliding name client-side with a visible message', async () => {
    await renderPanel()
    await act(async () => { (requireMenuProps().onRequestCreate as (dir: string, kind: string) => void)('/ws', 'file') })

    type('a.txt')
    pressEnter()
    await act(async () => {})

    expect(createFileMock).not.toHaveBeenCalled()
    expect(container.querySelector('[role="alert"]')?.textContent).toContain('already exists')
  })

  it('keeps the input open and surfaces a backend failure', async () => {
    createFileMock.mockRejectedValueOnce(new Error('path already exists'))
    await renderPanel()
    await act(async () => { (requireMenuProps().onRequestCreate as (dir: string, kind: string) => void)('/ws', 'file') })

    type('other.txt')
    pressEnter()
    await act(async () => {})

    expect(createFileMock).toHaveBeenCalledWith('s1', '/ws/other.txt')
    expect(container.querySelector('[data-testid="file-tree-inline-input"]')).not.toBeNull()
    expect(container.querySelector('[role="alert"]')?.textContent).toBe('path already exists')
  })

  it('deletes only after the confirmation dialog confirms; cancel is a no-op', async () => {
    await renderPanel()
    await act(async () => { (requireMenuProps().onRequestDelete as (entry: FileEntry) => void)(entry('/ws/a.txt')) })

    // Cancel: dialog closes, nothing is deleted.
    buttonByText('Cancel')!.dispatchEvent(new MouseEvent('click', { bubbles: true }))
    await act(async () => {})
    expect(deleteEntryMock).not.toHaveBeenCalled()
    expect(document.body.querySelector('[role="dialog"]')).toBeNull()

    // Re-open and confirm.
    await act(async () => { (requireMenuProps().onRequestDelete as (entry: FileEntry) => void)(entry('/ws/a.txt')) })
    buttonByText('Delete')!.dispatchEvent(new MouseEvent('click', { bubbles: true }))
    await act(async () => {})

    expect(deleteEntryMock).toHaveBeenCalledWith('s1', '/ws/a.txt')
    expect(document.body.querySelector('[role="dialog"]')).toBeNull()
  })

  it('surfaces a failed delete inside the dialog', async () => {
    deleteEntryMock.mockRejectedValueOnce(new Error('failed to delete'))
    await renderPanel()
    await act(async () => { (requireMenuProps().onRequestDelete as (entry: FileEntry) => void)(entry('/ws/a.txt')) })

    buttonByText('Delete')!.dispatchEvent(new MouseEvent('click', { bubbles: true }))
    await act(async () => {})

    expect(document.body.querySelector('[role="dialog"]')).not.toBeNull()
    expect(document.body.querySelector('[role="alert"]')?.textContent).toBe('failed to delete')
  })
})

describe('FileTreePanel — workspace/temp toolbar buttons', () => {
  let container: HTMLDivElement
  let root: Root | null = null

  beforeEach(() => {
    document.body.innerHTML = ''
    container = document.createElement('div')
    document.body.appendChild(container)
    useProjectStore.setState({ projects: [makeProject()], activeProjectId: 'p1' })
    useFileTreeStore.getState().clearTree()
    capturedMenuProps = null
    vi.clearAllMocks()
    listDirectoryMock.mockResolvedValue([entry('/ws/a.txt')])
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

  function toolbarButton(title: string): HTMLButtonElement {
    const el = container.querySelector<HTMLButtonElement>(`button[title="${title}"]`)
    if (!el) throw new Error(`toolbar button ${title} not rendered`)
    return el
  }

  it('reveals the session workspace and the session temp', async () => {
    useSessionStore.setState({ activeSessionId: 's1' })
    getSessionWorkspaceMock.mockResolvedValue('/ws/.worktrees/s-1')
    await renderPanel()

    toolbarButton('Open workspace in file manager').dispatchEvent(new MouseEvent('click', { bubbles: true }))
    toolbarButton('Open session temp directory in file manager').dispatchEvent(
      new MouseEvent('click', { bubbles: true }),
    )
    await act(async () => {})

    expect(revealMock).toHaveBeenCalledWith('s1', 'workspace', '')
    expect(revealMock).toHaveBeenCalledWith('s1', 'temp', '')
  })

  it('disables the workspace button while the root is pending and the temp button without a session', async () => {
    useSessionStore.setState({ activeSessionId: 's1' })
    // A session whose root RPC never settles: the tree (and the button) stay
    // pending — no checkout fallback.
    getSessionWorkspaceMock.mockImplementation(
      () => new Promise<string>(() => {}),
    )
    await renderPanel()

    expect(toolbarButton('Open workspace in file manager').disabled).toBe(true)
    expect(toolbarButton('Open session temp directory in file manager').disabled).toBe(false)

    // No active session: temp disabled, workspace resolves via the project.
    await act(async () => {
      useSessionStore.setState({ activeSessionId: null })
    })
    await act(async () => {})

    expect(toolbarButton('Open session temp directory in file manager').disabled).toBe(true)
    expect(toolbarButton('Open workspace in file manager').disabled).toBe(false)
    toolbarButton('Open workspace in file manager').dispatchEvent(new MouseEvent('click', { bubbles: true }))
    await act(async () => {})
    expect(revealMock).toHaveBeenCalledWith('', 'workspace', '')
  })
})
