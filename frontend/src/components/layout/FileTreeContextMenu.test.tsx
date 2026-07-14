// @vitest-environment jsdom
import { describe, it, expect, vi, beforeEach, afterEach } from 'vitest'
import { act } from 'react'
import { createRoot, type Root } from 'react-dom/client'

vi.hoisted(() => {
  const g = globalThis as Record<string, unknown>
  g.IS_REACT_ACT_ENVIRONMENT = true
})

// Mock the Zustand stores the menu's "View History" / "Add to .gitignore"
// handlers reach into. Mocking (rather than using the real persisted stores)
// avoids the zustand `persist` middleware, which crashes under jsdom when
// `window.localStorage` is undefined. Only the methods the component calls
// are surfaced; the clipboard tests don't touch them, so the mock is inert
// there.
const gitPanelMock = vi.hoisted(() => ({
  setActiveTab: vi.fn(),
  setPendingHistoryFilter: vi.fn(),
  setError: vi.fn(),
}))
const uiMock = vi.hoisted(() => ({
  setWorkspaceTab: vi.fn(),
}))
const fileViewerMock = vi.hoisted(() => ({
  openFile: vi.fn(),
}))

vi.mock('@/stores/gitPanelStore', () => ({
  useGitPanelStore: { getState: () => gitPanelMock },
}))
vi.mock('@/stores/uiStore', () => ({
  useUIStore: { getState: () => uiMock },
}))
vi.mock('@/stores/fileViewerStore', () => ({
  useFileViewerStore: { getState: () => fileViewerMock },
}))

import { FileTreeContextMenu } from './FileTreeContextMenu'
import type { FileEntry } from '@/types/models'

/**
 * Copy Path / Copy Relative Path must go through the native Wails runtime
 * clipboard (window.runtime.ClipboardSetText), NOT navigator.clipboard.
 * The Web Clipboard API is unavailable inside the Wails webview —
 * navigator.clipboard is undefined in production builds (non-secure origin)
 * and rejects with NotAllowedError under WKWebView's gesture rules — which is
 * why "Copy Path" failed for some entries. These tests guard the regression.
 */
describe('FileTreeContextMenu — clipboard', () => {
  let container: HTMLElement
  let root: Root
  let clipboardSetText: ReturnType<typeof vi.fn>
  let eventsEmit: ReturnType<typeof vi.fn>

  const dirEntry: FileEntry = { name: 'src', path: '/ws/src', is_dir: true }

  beforeEach(() => {
    clipboardSetText = vi.fn().mockResolvedValue(true)
    eventsEmit = vi.fn()
    // Mirror the production Wails webview: the native runtime is present,
    // but navigator.clipboard is NOT. Use defineProperty (rather than delete)
    // so the test is resilient to future jsdom versions that may add
    // navigator.clipboard as an inherited read-only property — delete cannot
    // remove inherited properties, but defineProperty shadows them.
    Object.defineProperty(navigator, 'clipboard', {
      value: undefined,
      configurable: true,
    })
    Object.defineProperty(globalThis, 'runtime', {
      configurable: true,
      value: { ClipboardSetText: clipboardSetText, EventsEmit: eventsEmit },
    })
    container = document.createElement('div')
    document.body.replaceChildren(container)
    root = createRoot(container)
  })

  afterEach(() => {
    act(() => root.unmount())
    document.body.replaceChildren()
  })

  function renderMenu(entry: FileEntry, workspaceRoot: string | null) {
    act(() => {
      root.render(
        <FileTreeContextMenu
          entry={entry}
          workspaceRoot={workspaceRoot}
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

  it('Copy Path writes the absolute path via the native Wails clipboard', async () => {
    renderMenu(dirEntry, '/ws')

    await act(async () => {
      menuItem('Copy Path').click()
      await Promise.resolve()
    })

    expect(clipboardSetText).toHaveBeenCalledTimes(1)
    expect(clipboardSetText).toHaveBeenCalledWith('/ws/src')
    // Success must not surface a runtime_error toast.
    expect(eventsEmit).not.toHaveBeenCalled()
  })

  it('Copy Relative Path writes the workspace-relative path via the native Wails clipboard', async () => {
    renderMenu(dirEntry, '/ws')

    await act(async () => {
      menuItem('Copy Relative Path').click()
      await Promise.resolve()
    })

    expect(clipboardSetText).toHaveBeenCalledTimes(1)
    expect(clipboardSetText).toHaveBeenCalledWith('src')
  })

  it('works even though navigator.clipboard is unavailable (the original bug)', async () => {
    // navigator.clipboard is explicitly removed in beforeEach to mirror
    // the production Wails webview, where the Web Clipboard API is
    // unavailable. Copy Path must still succeed through the native runtime
    // — this is exactly the scenario that produced "Failed to copy path to
    // clipboard" before the fix.
    expect(navigator.clipboard).toBeUndefined()

    renderMenu(dirEntry, '/ws')

    await act(async () => {
      menuItem('Copy Path').click()
      await Promise.resolve()
    })

    expect(clipboardSetText).toHaveBeenCalledWith('/ws/src')
    expect(eventsEmit).not.toHaveBeenCalled()
  })
})

describe('FileTreeContextMenu — View History', () => {
  let container: HTMLElement
  let root: Root

  const dirEntry: FileEntry = { name: 'components', path: '/ws/src/components', is_dir: true }
  const fileEntry: FileEntry = { name: 'foo.ts', path: '/ws/src/foo.ts', is_dir: false }

  // Mirror the component's platform-aware separator so the assertion stays
  // correct regardless of the jsdom platform string.
  const PATH_SEP = navigator.platform.includes('Win') ? '\\' : '/'

  beforeEach(() => {
    // The menu's clipboard/error paths need the native Wails runtime present,
    // even though View History itself doesn't use them.
    Object.defineProperty(globalThis, 'runtime', {
      configurable: true,
      value: { ClipboardSetText: vi.fn().mockResolvedValue(true), EventsEmit: vi.fn() },
    })
    gitPanelMock.setActiveTab.mockClear()
    gitPanelMock.setPendingHistoryFilter.mockClear()
    uiMock.setWorkspaceTab.mockClear()
    container = document.createElement('div')
    document.body.replaceChildren(container)
    root = createRoot(container)
  })

  afterEach(() => {
    act(() => root.unmount())
    document.body.replaceChildren()
  })

  function renderMenu(entry: FileEntry, workspaceRoot: string | null) {
    act(() => {
      root.render(
        <FileTreeContextMenu
          entry={entry}
          workspaceRoot={workspaceRoot}
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

  it('appends the OS path separator to the history filter for a directory', async () => {
    renderMenu(dirEntry, '/ws')

    await act(async () => {
      menuItem('View History').click()
      await Promise.resolve()
    })

    // A trailing separator ensures the glob "contains" match stays scoped to
    // files inside the directory and doesn't bleed into a sibling sharing the
    // same prefix (e.g. "src/components-extra").
    expect(gitPanelMock.setPendingHistoryFilter).toHaveBeenCalledTimes(1)
    expect(gitPanelMock.setPendingHistoryFilter).toHaveBeenCalledWith('src/components' + PATH_SEP)
  })

  it('does not append a separator to the history filter for a file', async () => {
    renderMenu(fileEntry, '/ws')

    await act(async () => {
      menuItem('View History').click()
      await Promise.resolve()
    })

    expect(gitPanelMock.setPendingHistoryFilter).toHaveBeenCalledTimes(1)
    expect(gitPanelMock.setPendingHistoryFilter).toHaveBeenCalledWith('src/foo.ts')
  })
})

describe('FileTreeContextMenu — Open in Viewer', () => {
  let container: HTMLElement
  let root: Root

  const dirEntry: FileEntry = { name: 'src', path: '/ws/src', is_dir: true }
  const fileEntry: FileEntry = { name: 'foo.ts', path: '/ws/src/foo.ts', is_dir: false }

  beforeEach(() => {
    Object.defineProperty(globalThis, 'runtime', {
      configurable: true,
      value: { ClipboardSetText: vi.fn().mockResolvedValue(true), EventsEmit: vi.fn() },
    })
    fileViewerMock.openFile.mockClear()
    container = document.createElement('div')
    document.body.replaceChildren(container)
    root = createRoot(container)
  })

  afterEach(() => {
    act(() => root.unmount())
    document.body.replaceChildren()
  })

  function renderMenu(entry: FileEntry, workspaceRoot: string | null) {
    act(() => {
      root.render(
        <FileTreeContextMenu
          entry={entry}
          workspaceRoot={workspaceRoot}
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

  it('renders "Open in Viewer" for a file', () => {
    renderMenu(fileEntry, '/ws')
    expect(() => menuItem('Open in Viewer')).not.toThrow()
  })

  it('does not render "Open in Viewer" for a directory', () => {
    renderMenu(dirEntry, '/ws')
    expect(() => menuItem('Open in Viewer')).toThrow()
  })

  it('opens the file in the viewer on click', async () => {
    renderMenu(fileEntry, '/ws')

    await act(async () => {
      menuItem('Open in Viewer').click()
      await Promise.resolve()
    })

    expect(fileViewerMock.openFile).toHaveBeenCalledTimes(1)
    expect(fileViewerMock.openFile).toHaveBeenCalledWith('/ws/src/foo.ts')
  })

  it('is the first menu item for a file', () => {
    renderMenu(fileEntry, '/ws')
    const items = Array.from(container.querySelectorAll('[role="menuitem"]'))
    expect(items[0]?.textContent?.trim()).toBe('Open in Viewer')
  })
})
