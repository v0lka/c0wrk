// @vitest-environment jsdom
import { describe, it, expect, vi, beforeEach, afterEach } from 'vitest'
import { act } from 'react'
import { createRoot, type Root } from 'react-dom/client'

// --- Mock the git API + operation runner + stores so tests never touch Wails ---
const { gitMocks } = vi.hoisted(() => ({
  gitMocks: {
    discardChanges: vi.fn(),
    appendToGitignore: vi.fn(),
    stageFile: vi.fn(),
    unstageFile: vi.fn(),
  },
}))
vi.mock('@/api/git', () => gitMocks)

const { runGitOperationMock } = vi.hoisted(() => ({ runGitOperationMock: vi.fn() }))
vi.mock('@/lib/gitOperation', () => ({ runGitOperation: runGitOperationMock }))

const { fileViewerMocks } = vi.hoisted(() => ({
  fileViewerMocks: {
    openFile: vi.fn(),
  },
}))
vi.mock('@/stores/fileViewerStore', () => ({
  useFileViewerStore: {
    getState: () => ({ openFile: fileViewerMocks.openFile }),
  },
}))

import { GitFileContextMenu } from './GitFileContextMenu'
import { useProjectStore } from '@/stores/projectStore'
import type { GitPanelEntry } from '@/stores/gitPanelStore'

/** Long enough to need wrap opportunities, with 8 separators to count. */
const LONG_PATH =
  'src/features/git/panel/components/deeply/nested/folders/GitPanelRowRenderer.view.config.tsx'
const SEPARATOR_COUNT = (LONG_PATH.match(/[/\\]/g) ?? []).length

function makeEntry(path: string): GitPanelEntry {
  return {
    path,
    status: 'M',
    staged: false,
    diffStat: { added: 1, deleted: 0 },
    indexStatus: ' ',
    worktreeStatus: 'M',
  }
}

let container: HTMLDivElement
let root: Root

beforeEach(() => {
  Object.values(gitMocks).forEach((m) => m.mockReset())
  gitMocks.discardChanges.mockResolvedValue(undefined)
  gitMocks.appendToGitignore.mockResolvedValue(undefined)
  gitMocks.stageFile.mockResolvedValue(undefined)
  gitMocks.unstageFile.mockResolvedValue(undefined)
  // The runner wraps fn() in bookkeeping; the mock just executes it so the
  // real API mock below records the call.
  runGitOperationMock.mockReset()
  runGitOperationMock.mockImplementation(async (args: { fn: () => Promise<unknown> }) =>
    args.fn(),
  )
  fileViewerMocks.openFile.mockReset()
  // Git operations run against the ACTIVE project.
  useProjectStore.setState({ activeProjectId: 'p1' })
  container = document.createElement('div')
  document.body.appendChild(container)
  root = createRoot(container)
})

afterEach(() => {
  act(() => {
    root.unmount()
  })
  container.remove()
  document.body.innerHTML = ''
})

/** Render the menu open at 10,10 for the given entry. */
function renderMenu(props: Partial<React.ComponentProps<typeof GitFileContextMenu>> = {}) {
  const defaultProps: React.ComponentProps<typeof GitFileContextMenu> = {
    entry: makeEntry(LONG_PATH),
    side: 'worktree',
    position: { x: 10, y: 10 },
    onClose: vi.fn(),
  }
  act(() => {
    root.render(<GitFileContextMenu {...defaultProps} {...props} />)
  })
}

/** Wait for microtasks so async handlers settle. */
function flush(): Promise<void> {
  return act(async () => {
    await Promise.resolve()
    await Promise.resolve()
  })
}

/** Click a root-menu item by trimmed-label substring. */
function clickMenuItem(text: string) {
  const items = Array.from(document.body.querySelectorAll('[role="menuitem"]')) as HTMLElement[]
  const match = items.find((el) => (el.textContent ?? '').trim().includes(text))
  if (!match) {
    throw new Error(
      `menu item "${text}" not found. Available: ${items.map((i) => i.textContent).join(' | ')}`,
    )
  }
  act(() => {
    match.dispatchEvent(new MouseEvent('click', { bubbles: true }))
  })
}

/** Open the menu and click "Discard Changes"; returns the dialog content. */
function openDiscardDialog(entry: GitPanelEntry = makeEntry(LONG_PATH)): HTMLElement {
  renderMenu({ entry })
  clickMenuItem('Discard Changes')
  const content = document.body.querySelector<HTMLElement>('[data-slot="dialog-content"]')
  expect(content).not.toBeNull()
  return content!
}

/** The single span inside the dialog whose text is exactly the display path. */
function discardPathSpan(content: HTMLElement): HTMLSpanElement {
  const spans = Array.from(content.querySelectorAll('span')).filter(
    (s) => (s.textContent ?? '') === LONG_PATH,
  )
  expect(spans).toHaveLength(1)
  return spans[0] as HTMLSpanElement
}

describe('GitFileContextMenu — Discard changes dialog', () => {
  it('renders the path char-for-char with one <wbr> after every separator', () => {
    const content = openDiscardDialog()
    const span = discardPathSpan(content)

    // Copy-safety: the accessible text is byte-identical to the path — a copy
    // of the dialog text must never gain or lose a character. (textContent
    // equality also rules out zero-width wrap characters.)
    expect(span.textContent).toBe(LONG_PATH)
    expect([...(span.textContent ?? '')]).not.toContain('\u200b')

    // One <wbr> per separator, each immediately after its separator, in order.
    const wbrs = span.querySelectorAll('wbr')
    expect(wbrs.length).toBe(SEPARATOR_COUNT)
    expect(SEPARATOR_COUNT).toBeGreaterThan(0)
    let text = ''
    for (const node of Array.from(span.childNodes)) {
      if (node.nodeType === Node.TEXT_NODE) {
        text += node.textContent ?? ''
        continue
      }
      expect(node.nodeName.toLowerCase()).toBe('wbr')
      expect(text).toMatch(/[/\\]$/)
    }
    expect(text).toBe(LONG_PATH)

    // The display classes moved from the old plain span onto PathWithBreaks.
    expect(span.classList.contains('font-mono')).toBe(true)
    expect(span.classList.contains('text-foreground')).toBe(true)
  })

  it('dialog content carries overflow-wrap anywhere so the long path cannot push the footer out', () => {
    const content = openDiscardDialog()
    expect(content.style.overflowWrap).toBe('anywhere')
  })

  it('footer with Cancel and Discard renders inside the dialog content', () => {
    const content = openDiscardDialog()
    const footer = content.querySelector('[data-slot="dialog-footer"]')
    expect(footer).not.toBeNull()
    expect(content.contains(footer)).toBe(true)
    const labels = Array.from(footer!.querySelectorAll('button')).map((b) =>
      (b.textContent ?? '').trim(),
    )
    expect(labels).toContain('Cancel')
    expect(labels).toContain('Discard')
  })

  it('Cancel closes the dialog without discarding', () => {
    const content = openDiscardDialog()
    const cancel = Array.from(content.querySelectorAll('button')).find(
      (b) => (b.textContent ?? '').trim() === 'Cancel',
    )
    expect(cancel).toBeTruthy()
    act(() => {
      cancel!.dispatchEvent(new MouseEvent('click', { bubbles: true }))
    })
    expect(document.body.querySelector('[data-slot="dialog-content"]')).toBeNull()
    expect(gitMocks.discardChanges).not.toHaveBeenCalled()
  })

  it('Discard discards the ABSOLUTE entry path and closes the dialog', async () => {
    const content = openDiscardDialog()
    const discard = Array.from(content.querySelectorAll('button')).find(
      (b) => (b.textContent ?? '').trim() === 'Discard',
    )
    expect(discard).toBeTruthy()
    await act(async () => {
      discard!.dispatchEvent(new MouseEvent('click', { bubbles: true }))
      await flush()
    })
    // The RPC keeps the absolute path even though the dialog shows the
    // display (relative) one.
    expect(gitMocks.discardChanges).toHaveBeenCalledWith(LONG_PATH)
    expect(document.body.querySelector('[data-slot="dialog-content"]')).toBeNull()
  })
})
