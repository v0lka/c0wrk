// @vitest-environment jsdom
//
// Tests for the mutation/reveal menu items (issue #60): New File / New Folder
// (directories only), Rename, Delete, Open Containing Folder dispatch to the
// panel's callbacks; rootMode offers only the two creation actions on the
// workspace root; without callbacks the items are absent (the menu's original
// contract is unchanged).

import { describe, it, expect, vi, beforeEach, afterEach } from 'vitest'
import { act } from 'react'
import { createRoot, type Root } from 'react-dom/client'

// Store mocks — the menu's git/copy handlers reach into them; the clipboard
// and message-sender surfaces stay inert here.
const gitPanelMock = vi.hoisted(() => ({
  isGitRepo: false,
  gitRepoProjectId: null as string | null,
  setActiveTab: vi.fn(),
  setPendingHistoryFilter: vi.fn(),
  setError: vi.fn(),
}))
const projectMock = vi.hoisted(() => ({ activeProjectId: 'p1' as string | null }))
const uiMock = vi.hoisted(() => ({ setWorkspaceTab: vi.fn() }))
const fileViewerMock = vi.hoisted(() => ({ openFile: vi.fn() }))

vi.mock('@/stores/gitPanelStore', () => ({
  useGitPanelStore: Object.assign(
    (selector: (s: typeof gitPanelMock) => unknown) => selector(gitPanelMock),
    { getState: () => gitPanelMock },
  ),
}))
vi.mock('@/stores/projectStore', () => ({
  useProjectStore: Object.assign(
    (selector: (s: typeof projectMock) => unknown) => selector(projectMock),
    { getState: () => projectMock },
  ),
}))
vi.mock('@/stores/uiStore', () => ({
  useUIStore: { getState: () => uiMock },
}))
vi.mock('@/stores/fileViewerStore', () => ({
  useFileViewerStore: { getState: () => fileViewerMock },
}))
vi.mock('@/hooks/useMessageSender', () => ({
  useMessageSender: () => ({ send: vi.fn(), cancel: vi.fn(), isProcessing: false }),
}))
vi.mock('@/lib/logger', () => ({
  logger: { error: vi.fn(), warn: vi.fn(), info: vi.fn(), debug: vi.fn() },
}))

import { FileTreeContextMenu, type RequestCreateFn } from './FileTreeContextMenu'
import type { FileEntry } from '@/types/models'

const dirEntry: FileEntry = { name: 'src', path: '/ws/src', is_dir: true }
const fileEntry: FileEntry = { name: 'main.go', path: '/ws/main.go', is_dir: false }
const rootEntry: FileEntry = { name: '', path: '/ws', is_dir: true }

interface Callbacks {
  onRequestCreate: ReturnType<typeof vi.fn>
  onRequestRename: ReturnType<typeof vi.fn>
  onRequestDelete: ReturnType<typeof vi.fn>
  onRevealContaining: ReturnType<typeof vi.fn>
}

describe('FileTreeContextMenu — mutation and reveal items', () => {
  let container: HTMLElement
  let root: Root
  let callbacks: Callbacks

  beforeEach(() => {
    container = document.createElement('div')
    document.body.replaceChildren(container)
    root = createRoot(container)
    callbacks = {
      onRequestCreate: vi.fn(),
      onRequestRename: vi.fn(),
      onRequestDelete: vi.fn(),
      onRevealContaining: vi.fn(),
    }
  })

  afterEach(() => {
    act(() => root.unmount())
    document.body.replaceChildren()
  })

  function renderMenu(entry: FileEntry, rootMode = false): void {
    act(() => {
      root.render(
        <FileTreeContextMenu
          entry={entry}
          workspaceRoot="/ws"
          position={{ x: 10, y: 10 }}
          onClose={() => {}}
          rootMode={rootMode}
          onRequestCreate={callbacks.onRequestCreate as unknown as RequestCreateFn}
          onRequestRename={callbacks.onRequestRename as (e: FileEntry) => void}
          onRequestDelete={callbacks.onRequestDelete as (e: FileEntry) => void}
          onRevealContaining={callbacks.onRevealContaining as (e: FileEntry) => void}
        />,
      )
    })
  }

  function byTestId(testId: string): HTMLButtonElement {
    const el = container.querySelector<HTMLButtonElement>(`[data-testid="${testId}"]`)
    if (!el) throw new Error(`menu item ${testId} not rendered`)
    return el
  }

  function itemByText(label: string): HTMLButtonElement | undefined {
    return Array.from(container.querySelectorAll<HTMLButtonElement>('[role="menuitem"]')).find(
      (b) => b.textContent?.trim() === label,
    )
  }

  it('offers New File and New Folder on a directory and dispatches the parent dir', () => {
    renderMenu(dirEntry)
    byTestId('file-new-file').dispatchEvent(new MouseEvent('click', { bubbles: true }))
    expect(callbacks.onRequestCreate).toHaveBeenCalledWith('/ws/src', 'file')
    byTestId('file-new-folder').dispatchEvent(new MouseEvent('click', { bubbles: true }))
    expect(callbacks.onRequestCreate).toHaveBeenCalledWith('/ws/src', 'folder')
  })

  it('offers Rename / Delete / Open Containing Folder on a directory', () => {
    renderMenu(dirEntry)
    byTestId('file-rename').dispatchEvent(new MouseEvent('click', { bubbles: true }))
    expect(callbacks.onRequestRename).toHaveBeenCalledWith(dirEntry)
    byTestId('file-delete').dispatchEvent(new MouseEvent('click', { bubbles: true }))
    expect(callbacks.onRequestDelete).toHaveBeenCalledWith(dirEntry)
    byTestId('file-reveal-containing').dispatchEvent(new MouseEvent('click', { bubbles: true }))
    expect(callbacks.onRevealContaining).toHaveBeenCalledWith(dirEntry)
  })

  it('offers Rename / Delete / Open Containing Folder on a file but no creation items', () => {
    renderMenu(fileEntry)
    expect(itemByText('New File')).toBeUndefined()
    expect(itemByText('New Folder')).toBeUndefined()
    byTestId('file-rename').dispatchEvent(new MouseEvent('click', { bubbles: true }))
    expect(callbacks.onRequestRename).toHaveBeenCalledWith(fileEntry)
    byTestId('file-delete').dispatchEvent(new MouseEvent('click', { bubbles: true }))
    expect(callbacks.onRequestDelete).toHaveBeenCalledWith(fileEntry)
    byTestId('file-reveal-containing').dispatchEvent(new MouseEvent('click', { bubbles: true }))
    expect(callbacks.onRevealContaining).toHaveBeenCalledWith(fileEntry)
  })

  it('keeps the original actions alongside the new ones', () => {
    renderMenu(fileEntry)
    expect(itemByText('Open in Viewer')).not.toBeUndefined()
    expect(itemByText('Copy Path')).not.toBeUndefined()
    expect(itemByText('Copy Relative Path')).not.toBeUndefined()
  })

  it('hides the mutation items when no callbacks are installed', () => {
    act(() => {
      root.render(
        <FileTreeContextMenu
          entry={dirEntry}
          workspaceRoot="/ws"
          position={{ x: 10, y: 10 }}
          onClose={() => {}}
        />,
      )
    })
    expect(itemByText('New File')).toBeUndefined()
    expect(itemByText('New Folder')).toBeUndefined()
    expect(itemByText('Rename')).toBeUndefined()
    expect(itemByText('Delete')).toBeUndefined()
    expect(itemByText('Open Containing Folder')).toBeUndefined()
    // The original actions survive.
    expect(itemByText('Open in Terminal')).not.toBeUndefined()
  })

  it('rootMode offers only creation at the workspace root', () => {
    renderMenu(rootEntry, true)
    expect(itemByText('Open in Terminal')).toBeUndefined()
    expect(itemByText('Rename')).toBeUndefined()
    expect(itemByText('Delete')).toBeUndefined()
    expect(itemByText('Copy Path')).toBeUndefined()
    byTestId('file-new-root-file').dispatchEvent(new MouseEvent('click', { bubbles: true }))
    expect(callbacks.onRequestCreate).toHaveBeenCalledWith('/ws', 'file')
    byTestId('file-new-root-folder').dispatchEvent(new MouseEvent('click', { bubbles: true }))
    expect(callbacks.onRequestCreate).toHaveBeenCalledWith('/ws', 'folder')
  })
})
