// @vitest-environment jsdom
import { describe, it, expect, vi, beforeEach, afterEach } from 'vitest'
import { act } from 'react'
import { createRoot, type Root } from 'react-dom/client'

vi.hoisted(() => {
  const g = globalThis as Record<string, unknown>
  g.IS_REACT_ACT_ENVIRONMENT = true
})

/**
 * A store mock that supports BOTH calling styles the component uses:
 *   - hook form:   `useStore((s) => s.foo)`          → applies selector
 *   - getState:    `useStore.getState().foo()`        → returns the state
 * The same object also carries the action spies so getState-based mutations
 * are observable.
 */
function createStoreMock(initialState: Record<string, unknown>) {
  const state = initialState
  const store = Object.assign(
    (selector: (s: Record<string, unknown>) => unknown) => selector(state),
    {
      getState: () => state,
    },
  )
  return store
}

const fileViewerActions = vi.hoisted(() => ({
  closeFile: vi.fn(),
  closeOthersFiles: vi.fn(),
  closeAllFiles: vi.fn(),
}))
const inputModeActions = vi.hoisted(() => ({
  setPendingTerminalDir: vi.fn(),
  setMode: vi.fn(),
}))
const uiActions = vi.hoisted(() => ({
  setWorkspaceTab: vi.fn(),
  setSidebarCollapsed: vi.fn(),
}))
const revealInWorkspaceMock = vi.hoisted(() => vi.fn())

// Default store states (recreated per-test via makeStores).
let fileViewerStoreMock: ReturnType<typeof createStoreMock>
let fileTreeStoreMock: ReturnType<typeof createStoreMock>
let inputModeStoreMock: ReturnType<typeof createStoreMock>
let uiStoreMock: ReturnType<typeof createStoreMock>

function makeStores(virtual = false, rootPath: string | null = '/ws') {
  fileViewerStoreMock = createStoreMock({
    files: virtual ? { '/ws/src/foo.ts': { virtual: true } } : {},
    ...fileViewerActions,
  })
  fileTreeStoreMock = createStoreMock({ rootPath })
  inputModeStoreMock = createStoreMock({ ...inputModeActions })
  uiStoreMock = createStoreMock({ sidebarCollapsed: false, ...uiActions })
}

vi.mock('@/stores/fileViewerStore', () => ({
  useFileViewerStore: Object.assign(
    (selector: (s: Record<string, unknown>) => unknown) => selector(fileViewerStoreMock.getState()),
    { getState: () => fileViewerStoreMock.getState() },
  ),
}))
vi.mock('@/stores/fileTreeStore', () => ({
  useFileTreeStore: Object.assign(
    (selector: (s: Record<string, unknown>) => unknown) => selector(fileTreeStoreMock.getState()),
    { getState: () => fileTreeStoreMock.getState() },
  ),
}))
vi.mock('@/stores/inputModeStore', () => ({
  useInputModeStore: Object.assign(
    (selector: (s: Record<string, unknown>) => unknown) => selector(inputModeStoreMock.getState()),
    { getState: () => inputModeStoreMock.getState() },
  ),
}))
vi.mock('@/stores/uiStore', () => ({
  useUIStore: Object.assign(
    (selector: (s: Record<string, unknown>) => unknown) => selector(uiStoreMock.getState()),
    { getState: () => uiStoreMock.getState() },
  ),
}))
vi.mock('@/lib/revealInWorkspace', () => ({
  revealInWorkspace: revealInWorkspaceMock,
}))

import { FileViewerTabContextMenu } from './FileViewerTabContextMenu'

/**
 * Copy Path / Copy Relative Path must go through the native Wails runtime
 * clipboard (window.runtime.ClipboardSetText), NOT navigator.clipboard —
 * mirroring the FileTreeContextMenu behaviour. These tests guard that
 * contract for the new tab menu.
 */
describe('FileViewerTabContextMenu', () => {
  let container: HTMLElement
  let root: Root
  let clipboardSetText: ReturnType<typeof vi.fn>
  let eventsEmit: ReturnType<typeof vi.fn>

  beforeEach(() => {
    clipboardSetText = vi.fn().mockResolvedValue(true)
    eventsEmit = vi.fn()
    Object.defineProperty(navigator, 'clipboard', {
      value: undefined,
      configurable: true,
    })
    Object.defineProperty(globalThis, 'runtime', {
      configurable: true,
      value: { ClipboardSetText: clipboardSetText, EventsEmit: eventsEmit },
    })
    makeStores()
    fileViewerActions.closeFile.mockClear()
    fileViewerActions.closeOthersFiles.mockClear()
    fileViewerActions.closeAllFiles.mockClear()
    inputModeActions.setPendingTerminalDir.mockClear()
    inputModeActions.setMode.mockClear()
    uiActions.setWorkspaceTab.mockClear()
    uiActions.setSidebarCollapsed.mockClear()
    revealInWorkspaceMock.mockClear()
    revealInWorkspaceMock.mockResolvedValue(undefined)
    container = document.createElement('div')
    document.body.replaceChildren(container)
    root = createRoot(container)
  })

  afterEach(() => {
    act(() => root.unmount())
    document.body.replaceChildren()
  })

  function renderMenu(path: string) {
    act(() => {
      root.render(
        <FileViewerTabContextMenu
          path={path}
          position={{ x: 10, y: 10 }}
          onClose={() => {}}
        />,
      )
    })
  }

  function menuItem(label: string): HTMLButtonElement {
    const items = Array.from(container.querySelectorAll('[role="menuitem"]'))
    const match = items.find((b) => b.textContent?.trim() === label)
    if (!match) throw new Error(`menu item "${label}" not rendered`)
    return match as HTMLButtonElement
  }

  function allMenuItemLabels(): string[] {
    return Array.from(container.querySelectorAll('[role="menuitem"]')).map(
      (b) => b.textContent?.trim() ?? '',
    )
  }

  it('renders all menu items in the required order', () => {
    renderMenu('/ws/src/foo.ts')
    expect(allMenuItemLabels()).toEqual([
      'Close',
      'Close Others',
      'Close All',
      'Copy Path',
      'Copy Relative Path',
      'Reveal In Workspace',
      'Open In Terminal',
    ])
  })

  it('Close calls closeFile with the tab path', async () => {
    renderMenu('/ws/src/foo.ts')
    await act(async () => {
      menuItem('Close').click()
      await Promise.resolve()
    })
    expect(fileViewerActions.closeFile).toHaveBeenCalledWith('/ws/src/foo.ts')
  })

  it('Close Others calls closeOthersFiles with the tab path', async () => {
    renderMenu('/ws/src/foo.ts')
    await act(async () => {
      menuItem('Close Others').click()
      await Promise.resolve()
    })
    expect(fileViewerActions.closeOthersFiles).toHaveBeenCalledWith('/ws/src/foo.ts')
  })

  it('Close All calls closeAllFiles', async () => {
    renderMenu('/ws/src/foo.ts')
    await act(async () => {
      menuItem('Close All').click()
      await Promise.resolve()
    })
    expect(fileViewerActions.closeAllFiles).toHaveBeenCalledTimes(1)
  })

  it('Copy Path writes the absolute path via the native Wails clipboard', async () => {
    renderMenu('/ws/src/foo.ts')
    await act(async () => {
      menuItem('Copy Path').click()
      await Promise.resolve()
    })
    expect(clipboardSetText).toHaveBeenCalledTimes(1)
    expect(clipboardSetText).toHaveBeenCalledWith('/ws/src/foo.ts')
    expect(eventsEmit).not.toHaveBeenCalled()
  })

  it('Copy Relative Path writes the workspace-relative path', async () => {
    renderMenu('/ws/src/foo.ts')
    await act(async () => {
      menuItem('Copy Relative Path').click()
      await Promise.resolve()
    })
    expect(clipboardSetText).toHaveBeenCalledWith('src/foo.ts')
  })

  it('Reveal In Workspace delegates to revealInWorkspace', async () => {
    renderMenu('/ws/src/foo.ts')
    await act(async () => {
      menuItem('Reveal In Workspace').click()
      await Promise.resolve()
    })
    expect(revealInWorkspaceMock).toHaveBeenCalledWith('/ws/src/foo.ts')
  })

  it('Open In Terminal switches to terminal mode with the parent directory as cwd', async () => {
    renderMenu('/ws/src/foo.ts')
    await act(async () => {
      menuItem('Open In Terminal').click()
      await Promise.resolve()
    })
    expect(inputModeActions.setPendingTerminalDir).toHaveBeenCalledWith('/ws/src')
    expect(inputModeActions.setMode).toHaveBeenCalledWith('terminal')
  })

  it('Open In Terminal uses the parent of a nested file as cwd', async () => {
    renderMenu('/ws/src/components/Button.tsx')
    await act(async () => {
      menuItem('Open In Terminal').click()
      await Promise.resolve()
    })
    expect(inputModeActions.setPendingTerminalDir).toHaveBeenCalledWith('/ws/src/components')
  })

  describe('virtual / pseudo-path tabs', () => {
    it('disables path-dependent items for a virtual tab', () => {
      makeStores(true)
      renderMenu('/ws/src/foo.ts')
      expect(menuItem('Copy Path').disabled).toBe(true)
      expect(menuItem('Copy Relative Path').disabled).toBe(true)
      expect(menuItem('Reveal In Workspace').disabled).toBe(true)
      expect(menuItem('Open In Terminal').disabled).toBe(true)
    })

    it('keeps Close actions enabled for a virtual tab', () => {
      makeStores(true)
      renderMenu('/ws/src/foo.ts')
      expect(menuItem('Close').disabled).toBe(false)
      expect(menuItem('Close Others').disabled).toBe(false)
      expect(menuItem('Close All').disabled).toBe(false)
    })

    it('disables path-dependent items for a synthetic c0wrk: pseudo-path', () => {
      renderMenu('c0wrk:review')
      expect(menuItem('Copy Path').disabled).toBe(true)
      expect(menuItem('Reveal In Workspace').disabled).toBe(true)
      expect(menuItem('Open In Terminal').disabled).toBe(true)
      // Close actions still apply to any tab.
      expect(menuItem('Close').disabled).toBe(false)
    })
  })

  it('does not render anything when position is null', () => {
    act(() => {
      root.render(
        <FileViewerTabContextMenu path="/ws/src/foo.ts" position={null} onClose={() => {}} />,
      )
    })
    expect(container.querySelector('[role="menu"]')).toBeNull()
  })
})
