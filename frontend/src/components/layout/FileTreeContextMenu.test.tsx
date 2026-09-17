// @vitest-environment jsdom
import { describe, it, expect, vi, beforeEach, afterEach } from 'vitest'
import { act } from 'react'
import { createRoot, type Root } from 'react-dom/client'

// Mock the Zustand stores the menu's "View History" / "Add to .gitignore"
// handlers reach into. Mocking (rather than using the real persisted stores)
// avoids the zustand `persist` middleware, which crashes under jsdom when
// `window.localStorage` is undefined. Only the methods the component calls
// are surfaced; the clipboard tests don't touch them, so the mock is inert
// there.
const gitPanelMock = vi.hoisted(() => ({
  isGitRepo: false,
  gitRepoProjectId: null as string | null,
  setActiveTab: vi.fn(),
  setPendingHistoryFilter: vi.fn(),
  setError: vi.fn(),
}))
const projectMock = vi.hoisted(() => ({
  activeProjectId: 'p1' as string | null,
}))
const uiMock = vi.hoisted(() => ({
  setWorkspaceTab: vi.fn(),
}))
const fileViewerMock = vi.hoisted(() => ({
  openFile: vi.fn(),
}))

// Study-paper dispatch goes through the shared message sender; mock it so the
// PDF menu's dispatch can be asserted without a running Wails runtime (and so
// the real hook's store graph never loads under jsdom).
const messageSenderMock = vi.hoisted(() => ({ send: vi.fn() }))
vi.mock('@/hooks/useMessageSender', () => ({
  useMessageSender: () => ({
    send: messageSenderMock.send,
    cancel: vi.fn(),
    isProcessing: false,
  }),
}))

vi.mock('@/stores/gitPanelStore', () => ({
  // The component both subscribes (selector call) and imperatively reads
  // (getState) — the mock must be callable AND expose getState.
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

import { FileTreeContextMenu } from './FileTreeContextMenu'
import {
  STUDY_PAPER_SKILL,
  buildStudyPrompt,
} from '@/components/papers/paperActions'
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
    // View History is a git-only action: the tests in this describe run
    // against a project whose workspace IS a git repository.
    gitPanelMock.isGitRepo = true
    gitPanelMock.gitRepoProjectId = 'p1'
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

  it('switches the workspace to the Git panel on the history action', async () => {
    renderMenu(fileEntry, '/ws')

    await act(async () => {
      menuItem('View History').click()
      await Promise.resolve()
    })

    expect(uiMock.setWorkspaceTab).toHaveBeenCalledWith('p1', 'git')
    expect(gitPanelMock.setActiveTab).toHaveBeenCalledWith('p1', 'history')
  })

  it('hides git-only actions when the workspace is not a git repository', () => {
    gitPanelMock.isGitRepo = false
    gitPanelMock.gitRepoProjectId = 'p1'

    renderMenu(fileEntry, '/ws')

    expect(() => menuItem('Add to .gitignore')).toThrow()
    expect(() => menuItem('View History')).toThrow()
    // The separator before the git-only group must be gone as well.
    const items = Array.from(container.querySelectorAll('[role="menuitem"]'))
    expect(items.map((b) => b.textContent?.trim())).toEqual([
      'Open in Viewer',
      'Copy Path',
      'Copy Relative Path',
    ])
  })

  it('hides git-only actions when the repo check belongs to a different project (stale pairing)', () => {
    gitPanelMock.isGitRepo = true
    gitPanelMock.gitRepoProjectId = 'other-project'

    renderMenu(fileEntry, '/ws')

    expect(() => menuItem('Add to .gitignore')).toThrow()
    expect(() => menuItem('View History')).toThrow()
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

describe('FileTreeContextMenu — Study this paper (PDF only)', () => {
  let container: HTMLElement
  let root: Root

  const pdfEntry: FileEntry = {
    name: 'attention.pdf',
    path: '/ws/papers/attention.pdf',
    is_dir: false,
  }
  const docEntry: FileEntry = { name: 'notes.md', path: '/ws/notes.md', is_dir: false }
  const dirEntry: FileEntry = { name: 'papers', path: '/ws/papers', is_dir: true }

  beforeEach(() => {
    messageSenderMock.send.mockReset()
    messageSenderMock.send.mockResolvedValue(undefined)
    Object.defineProperty(globalThis, 'runtime', {
      configurable: true,
      value: { ClipboardSetText: vi.fn().mockResolvedValue(true), EventsEmit: vi.fn() },
    })
    container = document.createElement('div')
    document.body.replaceChildren(container)
    root = createRoot(container)
  })

  afterEach(() => {
    act(() => root.unmount())
    document.body.replaceChildren()
  })

  function renderMenu(entry: FileEntry) {
    act(() => {
      root.render(
        <FileTreeContextMenu
          entry={entry}
          workspaceRoot="/ws"
          position={{ x: 10, y: 10 }}
          onClose={() => {}}
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
    return Array.from(
      container.querySelectorAll<HTMLButtonElement>('[role="menuitem"]'),
    ).find((b) => b.textContent?.trim() === label)
  }

  it('offers "Study this paper…" for a PDF file', () => {
    renderMenu(pdfEntry)
    expect(itemByText('Study this paper…')).not.toBeUndefined()
    // It is offered alongside the standard file actions, not in place of them.
    expect(itemByText('Open in Viewer')).not.toBeUndefined()
  })

  it('does not offer Study for a non-PDF file (no dispatch surface)', () => {
    renderMenu(docEntry)
    expect(itemByText('Study this paper…')).toBeUndefined()
  })

  it('does not offer Study for a directory', () => {
    renderMenu(dirEntry)
    expect(itemByText('Study this paper…')).toBeUndefined()
  })

  it('opens the depth picker and dispatches study-paper with the chosen mode', async () => {
    renderMenu(pdfEntry)

    await act(async () => {
      byTestId('file-study-open').click()
    })

    // The picker lists every reading-depth mode.
    expect(container.querySelector('[data-testid="file-study-mode-auto"]')).not.toBeNull()
    expect(container.querySelector('[data-testid="file-study-mode-review"]')).not.toBeNull()
    expect(container.querySelector('[data-testid="file-study-mode-teach"]')).not.toBeNull()
    // The main items are replaced while picking.
    expect(itemByText('Copy Path')).toBeUndefined()

    await act(async () => {
      byTestId('file-study-mode-review').click()
      await new Promise((r) => setTimeout(r, 0))
    })

    expect(messageSenderMock.send).toHaveBeenCalledTimes(1)
    expect(messageSenderMock.send).toHaveBeenCalledWith(
      buildStudyPrompt('/ws/papers/attention.pdf', 'review'),
      [STUDY_PAPER_SKILL],
      undefined,
      undefined,
      { newSession: false },
    )
  })

  it('does not dispatch when the depth picker is dismissed', async () => {
    renderMenu(pdfEntry)

    await act(async () => {
      byTestId('file-study-open').click()
    })
    await act(async () => {
      byTestId('file-study-back').click()
    })

    // Back restores the main items and never dispatches.
    expect(itemByText('Copy Path')).not.toBeUndefined()
    expect(messageSenderMock.send).not.toHaveBeenCalled()
  })
})
