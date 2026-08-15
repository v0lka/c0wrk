// @vitest-environment jsdom
import { describe, it, expect, vi, beforeEach, afterEach } from 'vitest'
import { act } from 'react'
import { createRoot, type Root } from 'react-dom/client'

// --- Mock the git API + runtime + stores so tests never touch Wails ---
const { gitMocks } = vi.hoisted(() => ({
  gitMocks: {
    createTag: vi.fn(),
    deleteTag: vi.fn(),
    pushTag: vi.fn(),
    deleteRemoteTag: vi.fn(),
    resetToCommit: vi.fn(),
  },
}))
vi.mock('@/api/git', () => gitMocks)

const { runtimeMocks } = vi.hoisted(() => ({
  runtimeMocks: {
    clipboardSetText: vi.fn(),
    emit: vi.fn(),
  },
}))
vi.mock('@/api/runtime', () => runtimeMocks)

vi.mock('@/lib/logger', () => ({ logger: { debug: vi.fn(), info: vi.fn(), warn: vi.fn(), error: vi.fn() } }))

const { fileViewerMocks } = vi.hoisted(() => ({
  fileViewerMocks: {
    openFile: vi.fn(),
    setCollapsed: vi.fn(),
  },
}))
vi.mock('@/stores/fileViewerStore', () => ({
  useFileViewerStore: {
    getState: () => ({
      openFile: fileViewerMocks.openFile,
      setCollapsed: fileViewerMocks.setCollapsed,
    }),
  },
}))

const { gitPanelStoreMock } = vi.hoisted(() => ({
  gitPanelStoreMock: {
    getState: vi.fn(),
  },
}))
vi.mock('@/stores/gitPanelStore', () => ({
  useGitPanelStore: gitPanelStoreMock,
}))

import { GitHistoryContextMenu } from './GitHistoryContextMenu'

let container: HTMLDivElement
let root: Root

beforeEach(() => {
  Object.values(gitMocks).forEach((m) => m.mockReset())
  Object.values(runtimeMocks).forEach((m) => m.mockReset())
  Object.values(fileViewerMocks).forEach((m) => m.mockReset())
  gitMocks.createTag.mockResolvedValue(undefined)
  gitMocks.deleteTag.mockResolvedValue(undefined)
  gitMocks.pushTag.mockResolvedValue('pushed')
  gitMocks.deleteRemoteTag.mockResolvedValue('deleted')
  gitMocks.resetToCommit.mockResolvedValue(undefined)
  runtimeMocks.clipboardSetText.mockResolvedValue(true)
  const setPendingBranchBase = vi.fn()
  const openBranchPicker = vi.fn()
  const setError = vi.fn()
  gitPanelStoreMock.getState.mockReturnValue({
    setPendingBranchBase,
    openBranchPicker,
    setError,
  })
  container = document.createElement('div')
  document.body.appendChild(container)
  root = createRoot(container)
})

afterEach(() => {
  act(() => { root.unmount() })
  container.remove()
  document.body.innerHTML = ''
})

/** Render the menu with sensible defaults (already open at 10,10). */
function renderMenu(
  props: Partial<React.ComponentProps<typeof GitHistoryContextMenu>> = {},
) {
  const defaultProps: React.ComponentProps<typeof GitHistoryContextMenu> = {
    sha: 'abc1234',
    refs: ['HEAD -> main', 'tag: v1.0'],
    currentBranch: 'main',
    position: { x: 10, y: 10 },
    onClose: vi.fn(),
    onAfterMutation: vi.fn(),
  }
  act(() => {
    root.render(<GitHistoryContextMenu {...defaultProps} {...props} />)
  })
}

/** Wait for microtasks so async handlers settle. */
function flush(): Promise<void> {
  return act(async () => {
    await Promise.resolve()
    await Promise.resolve()
  })
}

/** All currently-mounted menu items (across the menu + open submenus). */
function items(): HTMLElement[] {
  return Array.from(document.body.querySelectorAll('[role="menuitem"]')) as HTMLElement[]
}

/** The root menu element: the first (outermost) [role="menu"] in the body. */
function rootMenu(): HTMLElement {
  return document.body.querySelector('[role="menu"]') as HTMLElement
}

/** Find a menu item by visible text (trimmed-label substring match),
 *  scoped to the root menu only. */
function findItem(text: string): HTMLElement {
  const all = Array.from(rootMenu().querySelectorAll('[role="menuitem"]')) as HTMLElement[]
  const match = all.find((el) => (el.textContent ?? '').trim().includes(text))
  if (!match) {
    throw new Error(
      `menu item "${text}" not found. Available: ${all.map((i) => i.textContent).join(' | ')}`,
    )
  }
  return match
}

/** Find a menu item by exact trimmed text, searching ALL mounted menuitems
 *  (root + open submenus). Returns the last match so deeply-nested items
 *  (e.g. a per-tag "Copy") win over same-named root items. */
function findItemDeep(text: string): HTMLElement {
  const all = items()
  const matches = all.filter((el) => (el.textContent ?? '').trim() === text)
  const match = matches[matches.length - 1]
  if (!match) {
    throw new Error(
      `menu item "${text}" not found. Available: ${all.map((i) => i.textContent).join(' | ')}`,
    )
  }
  return match
}

/** Click a menu item by label (root menu only). */
function clickItem(text: string) {
  act(() => {
    findItem(text).dispatchEvent(new MouseEvent('click', { bubbles: true }))
  })
}

/** Click a menu item by exact label, deepest match (for nested items). */
function clickItemDeep(text: string) {
  act(() => {
    findItemDeep(text).dispatchEvent(new MouseEvent('click', { bubbles: true }))
  })
}

/** Hover a submenu trigger to reveal its nested content.
 *  React implements onMouseEnter via the bubbling native `mouseover` event
 *  (mouseenter itself does not bubble), so we dispatch `mouseover`. */
function hoverSubTrigger(text: string) {
  act(() => {
    findItem(text).dispatchEvent(new MouseEvent('mouseover', { bubbles: true }))
  })
}

describe('GitHistoryContextMenu — View Commit', () => {
  it('opens a read-only commit review page via the file viewer', async () => {
    renderMenu({ refs: [] })
    clickItem('View Commit')
    expect(fileViewerMocks.openFile).toHaveBeenCalledWith('c0wrk:commit:abc1234')
    expect(fileViewerMocks.setCollapsed).toHaveBeenCalledWith(false)
  })
})

describe('GitHistoryContextMenu — Copy', () => {
  it('copies the commit SHA to the clipboard', async () => {
    renderMenu({ refs: [] })
    clickItem('Copy')
    await flush()
    expect(runtimeMocks.clipboardSetText).toHaveBeenCalledWith('abc1234')
  })
})

describe('GitHistoryContextMenu — Create › Branch', () => {
  it('opens the Switch Branch dialog with the commit preselected as base', async () => {
    renderMenu({ refs: [] })
    hoverSubTrigger('Create')
    clickItem('Branch')
    const { setPendingBranchBase, openBranchPicker } = gitPanelStoreMock.getState()
    expect(setPendingBranchBase).toHaveBeenCalledWith('abc1234')
    expect(openBranchPicker).toHaveBeenCalled()
  })
})

describe('GitHistoryContextMenu — Create › Tag', () => {
  it('opens a dialog, then creates a tag and reloads history', async () => {
    const onAfterMutation = vi.fn()
    renderMenu({ refs: [], onAfterMutation })
    hoverSubTrigger('Create')
    clickItem('Tag')
    // The create-tag dialog should now be visible with an input.
    const input = document.body.querySelector('input') as HTMLInputElement
    expect(input).toBeTruthy()
    act(() => {
      const setter = Object.getOwnPropertyDescriptor(
        HTMLInputElement.prototype,
        'value',
      )!.set!
      setter.call(input, 'v2.0')
      input.dispatchEvent(new Event('input', { bubbles: true }))
    })
    const createBtn = Array.from(document.body.querySelectorAll('[role="dialog"] button')).find(
      (b) => b.textContent === 'Create',
    ) as HTMLButtonElement
    expect(createBtn).toBeTruthy()
    await act(async () => {
      createBtn.click()
      await flush()
    })
    expect(gitMocks.createTag).toHaveBeenCalledWith('v2.0', 'abc1234')
    expect(onAfterMutation).toHaveBeenCalled()
  })

  it('disables the create button while the name is empty', async () => {
    renderMenu({ refs: [] })
    hoverSubTrigger('Create')
    clickItem('Tag')
    const createBtn = Array.from(document.body.querySelectorAll('[role="dialog"] button')).find(
      (b) => b.textContent === 'Create',
    ) as HTMLButtonElement
    expect(createBtn.hasAttribute('disabled')).toBe(true)
  })
})

describe('GitHistoryContextMenu — Reset', () => {
  it('performs a soft reset and reloads history', async () => {
    const onAfterMutation = vi.fn()
    renderMenu({ refs: [], onAfterMutation })
    hoverSubTrigger('Reset')
    clickItem('Soft')
    await flush()
    expect(gitMocks.resetToCommit).toHaveBeenCalledWith('abc1234', 'soft')
    expect(onAfterMutation).toHaveBeenCalled()
  })

  it('performs a mixed reset and reloads history', async () => {
    const onAfterMutation = vi.fn()
    renderMenu({ refs: [], onAfterMutation })
    hoverSubTrigger('Reset')
    clickItem('Mixed')
    await flush()
    expect(gitMocks.resetToCommit).toHaveBeenCalledWith('abc1234', 'mixed')
    expect(onAfterMutation).toHaveBeenCalled()
  })

  it('opens a confirmation dialog for a hard reset before executing', async () => {
    renderMenu({ refs: [] })
    hoverSubTrigger('Reset')
    clickItem('Hard')
    // Confirmation dialog should appear (not yet reset).
    expect(gitMocks.resetToCommit).not.toHaveBeenCalled()
    expect(document.body.textContent).toContain('Hard reset')
    const confirmBtn = Array.from(document.body.querySelectorAll('button')).find(
      (b) => b.textContent?.includes('Hard Reset'),
    ) as HTMLButtonElement
    expect(confirmBtn).toBeTruthy()
    await act(async () => {
      confirmBtn.click()
      await flush()
    })
    expect(gitMocks.resetToCommit).toHaveBeenCalledWith('abc1234', 'hard')
  })

  it('labels the Reset submenu with the current branch name', () => {
    renderMenu({ refs: [], currentBranch: 'feature' })
    expect(findItem('Reset feature to This Commit')).toBeTruthy()
  })
})

describe('GitHistoryContextMenu — Tag submenu', () => {
  it('does not render the top-level Tag submenu when the commit has no tags', () => {
    renderMenu({ refs: ['HEAD -> main'] })
    expect(() => findItem('Tag')).toThrow()
  })

  it('renders a per-tag submenu when the commit carries a tag', () => {
    renderMenu({ refs: ['HEAD -> main', 'tag: v1.0'] })
    expect(findItem('Tag')).toBeTruthy()
  })

  it('copies a tag name from the per-tag Copy action', async () => {
    renderMenu({ refs: ['tag: v1.0'] })
    hoverSubTrigger('Tag')
    hoverSubTrigger('v1.0')
    clickItemDeep('Copy')
    await flush()
    expect(runtimeMocks.clipboardSetText).toHaveBeenCalledWith('v1.0')
  })

  it('deletes a local tag from the per-tag submenu', async () => {
    const onAfterMutation = vi.fn()
    renderMenu({ refs: ['tag: v1.0'], onAfterMutation })
    hoverSubTrigger('Tag')
    hoverSubTrigger('v1.0')
    clickItem('Delete Tag local')
    await flush()
    expect(gitMocks.deleteTag).toHaveBeenCalledWith('v1.0')
    expect(onAfterMutation).toHaveBeenCalled()
  })

  it('pushes a tag to the remote from the per-tag submenu', async () => {
    const onAfterMutation = vi.fn()
    renderMenu({ refs: ['tag: v1.0'], onAfterMutation })
    hoverSubTrigger('Tag')
    hoverSubTrigger('v1.0')
    clickItem('Push Tag')
    await flush()
    expect(gitMocks.pushTag).toHaveBeenCalledWith('v1.0', '')
    expect(onAfterMutation).toHaveBeenCalled()
  })

  it('deletes a remote tag from the per-tag submenu', async () => {
    const onAfterMutation = vi.fn()
    renderMenu({ refs: ['tag: v1.0'], onAfterMutation })
    hoverSubTrigger('Tag')
    hoverSubTrigger('v1.0')
    clickItem('Delete Tag remote')
    await flush()
    expect(gitMocks.deleteRemoteTag).toHaveBeenCalledWith('v1.0', '')
    expect(onAfterMutation).toHaveBeenCalled()
  })

  it('opens the Switch Branch dialog with the tag as base', async () => {
    renderMenu({ refs: ['tag: v1.0'] })
    hoverSubTrigger('Tag')
    hoverSubTrigger('v1.0')
    clickItem('Create Branch')
    const { setPendingBranchBase, openBranchPicker } = gitPanelStoreMock.getState()
    expect(setPendingBranchBase).toHaveBeenCalledWith('v1.0')
    expect(openBranchPicker).toHaveBeenCalled()
  })

  it('renders a separate submenu per tag when multiple tags exist', () => {
    renderMenu({ refs: ['tag: v1.0', 'tag: v2.0'] })
    hoverSubTrigger('Tag')
    // Both per-tag submenus should be present as items.
    expect(findItem('v1.0')).toBeTruthy()
    expect(findItem('v2.0')).toBeTruthy()
  })

  it('reveals all per-tag actions when the level-3 submenu opens on hover', () => {
    renderMenu({ refs: ['tag: v1.0'] })
    // Level 1 → hover "Tag" (level-2 submenu trigger).
    hoverSubTrigger('Tag')
    // Level 2 → hover the per-tag trigger "v1.0" (level-3 submenu trigger).
    hoverSubTrigger('v1.0')
    // All five per-tag actions should now be mounted in the DOM.
    const actions = ['Create Branch', 'Push Tag', 'Delete Tag local', 'Delete Tag remote', 'Copy']
    for (const action of actions) {
      expect(findItemDeep(action)).toBeTruthy()
    }
  })
})

describe('GitHistoryContextMenu — visibility', () => {
  it('renders nothing when position is null', () => {
    renderMenu({ position: null })
    expect(document.body.querySelector('[role="menu"]')).toBeNull()
  })
})
