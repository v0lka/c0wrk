// @vitest-environment jsdom
import { describe, it, expect, vi, beforeEach, afterEach } from 'vitest'
import { act } from 'react'
import { createRoot, type Root } from 'react-dom/client'

// --- Mock the API wrappers so tests never touch the Wails backend ---
const { apiMocks } = vi.hoisted(() => ({
  apiMocks: {
    // '@/api/git' — used by useGitHistory, useCommitFiles and the page.
    getGitHistory: vi.fn(),
    getCommitFiles: vi.fn(),
    getCommitFilesBatch: vi.fn(),
    getCommitMessage: vi.fn(),
    GIT_HISTORY_PAGE_SIZE: 300,
    // '@/api/review' — the page's one-getCommitDiff-per-commit fetch.
    getCommitDiff: vi.fn(),
  },
}))

vi.mock('@/api/git', () => apiMocks)
vi.mock('@/api/review', () => apiMocks)
vi.mock('@/lib/logger', () => ({ logger: { error: vi.fn() } }))

// Mock the virtualizer so all rows render in jsdom (no layout engine).
// Same mock shape as the former GitHistoryTab.test: every item with correct offsets.
vi.mock('@tanstack/react-virtual', () => ({
  useVirtualizer: (options: {
    count: number
    estimateSize: (i: number) => number
    getItemKey: (i: number) => string | number
  }) => {
    const items = Array.from({ length: options.count }, (_, i) => ({
      index: i,
      key: options.getItemKey(i),
      start: Array.from({ length: i }, (_, j) => options.estimateSize(j)).reduce((a, b) => a + b, 0),
      size: options.estimateSize(i),
    }))
    const totalSize = items.reduce((sum, item) => sum + item.size, 0)
    return { getVirtualItems: () => items, getTotalSize: () => totalSize }
  },
}))

// Mock the file viewer store so the test can assert how the per-file
// "Open in Viewer" action opens the commit-file pseudo-path tab.
const { fileViewerMocks } = vi.hoisted(() => ({
  fileViewerMocks: {
    openFile: vi.fn(),
    openVirtualFile: vi.fn(),
    setCollapsed: vi.fn(),
  },
}))
vi.mock('@/stores/fileViewerStore', () => ({
  useFileViewerStore: {
    getState: () => ({
      openFile: fileViewerMocks.openFile,
      openVirtualFile: fileViewerMocks.openVirtualFile,
      setCollapsed: fileViewerMocks.setCollapsed,
    }),
  },
}))

import { GitGraphPage } from './GitGraphPage'
import { cardHeight, expandedCardHeight, DIFF_ROW_HEIGHT, type CardExpansionState } from './gitGraphCardHeights'
import { useReviewStore } from '@/stores/reviewStore'
import { useGitGraphStore } from '@/stores/gitGraphStore'
import { useProjectStore } from '@/stores/projectStore'
import { useGitPanelStore } from '@/stores/gitPanelStore'

let container: HTMLDivElement
let root: Root

// merge 'mmm' → normal 'nnn' → root 'ooo' (newest-first).
const COMMITS = [
  { sha: 'mmm', parents: ['nnn', 'ooo'], author: 'Jane', email: 'j@x', date: '2026-07-10', message: 'merge pr', refs: [] },
  { sha: 'nnn', parents: ['ooo'], author: 'Jane', email: 'j@x', date: '2026-07-09', message: 'feat: x', refs: ['HEAD -> main'] },
  { sha: 'ooo', parents: [], author: 'Jane', email: 'j@x', date: '2026-07-08', message: 'init', refs: [] },
]

const FILES: Record<string, { path: string; status: string }[]> = {
  mmm: [],
  nnn: [
    { path: 'src/a.ts', status: 'A' },
    { path: 'README.md', status: 'M' },
  ],
  ooo: [],
}

const FULL_MESSAGE = 'feat: x\n\nLong body describing the change.'

/** One file's hunk slice as the backend's ParseReviewDiff would emit it. */
const COMMIT_DIFF = [
  {
    path: 'src/a.ts',
    hunks: [{ raw: '+export const x = 1\n-const x = 2', old_start: 1, old_count: 1, new_start: 1, new_count: 1 }],
  },
  {
    path: 'README.md',
    hunks: [{ raw: ' # Title\n+new line', old_start: 1, old_count: 1, new_start: 1, new_count: 2 }],
  },
]

function makePage(commits: typeof COMMITS, nextSkip: number, hasMore: boolean) {
  return { commits, next_skip: nextSkip, has_more: hasMore }
}

beforeEach(() => {
  localStorage.clear()
  useReviewStore.setState({ diffViewMode: 'unified' })
  // The expansion store is app-session scoped module state — reset it so
  // tests start all-collapsed (and don't leak expansions into each other).
  useGitGraphStore.setState({ byProject: {} })
  // The page scopes its expansion decisions to the active project.
  useProjectStore.setState({ activeProjectId: 'proj-1' })
  // No queued history filter unless a test sets one explicitly.
  useGitPanelStore.setState({ pendingHistoryFilter: null })
  fileViewerMocks.openFile.mockReset()
  fileViewerMocks.openVirtualFile.mockReset()
  fileViewerMocks.setCollapsed.mockReset()
  apiMocks.getGitHistory.mockReset()
  apiMocks.getCommitFiles.mockReset()
  apiMocks.getCommitFilesBatch.mockReset()
  apiMocks.getCommitMessage.mockReset()
  apiMocks.getCommitDiff.mockReset()
  apiMocks.getGitHistory.mockResolvedValue(makePage(COMMITS, COMMITS.length, false))
  apiMocks.getCommitFiles.mockImplementation((sha: string) => Promise.resolve(FILES[sha] ?? []))
  apiMocks.getCommitFilesBatch.mockImplementation((shas: string[]) =>
    Promise.resolve(Object.fromEntries(shas.map((sha) => [sha, FILES[sha] ?? []]))),
  )
  apiMocks.getCommitMessage.mockImplementation((sha: string) =>
    Promise.resolve(sha === 'nnn' ? FULL_MESSAGE : sha),
  )
  apiMocks.getCommitDiff.mockResolvedValue(COMMIT_DIFF)

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
  localStorage.clear()
})

/** Flush microtasks so async effects resolve. */
function flush(): Promise<void> {
  return act(async () => {
    await Promise.resolve()
    await Promise.resolve()
    await Promise.resolve()
  })
}

function renderPage() {
  act(() => {
    root.render(<GitGraphPage />)
  })
}

/** Swap the page out for a placeholder and back — a genuine unmount/remount
 * in the same root: every page-local cache dies, only the app-session store
 * (and the mocked stores) survive. */
function remountPage() {
  act(() => {
    root.render(<div />)
  })
  renderPage()
}

/** The card header button carrying the given commit message. */
function cardHeader(message: string): HTMLButtonElement {
  const btn = Array.from(container.querySelectorAll('button')).find((b) =>
    (b.textContent ?? '').includes(message),
  )
  expect(btn).toBeTruthy()
  return btn as HTMLButtonElement
}

/** Row wrapper (absolutely positioned by the virtualizer) of the given message. */
function rowWrapper(message: string): HTMLElement {
  const wrap = Array.from(container.querySelectorAll('div[style]')).find((el) =>
    (el.textContent ?? '').includes(message) && (el as HTMLElement).style.top !== '',
  )
  expect(wrap).toBeTruthy()
  return wrap as HTMLElement
}

/** Circles inside the lane-gutter svg only (the header icon svg excluded). */
function gutterCircles(): number {
  const wrapper = Array.from(container.querySelectorAll('div')).find((el) => el.style.position === 'relative')
  expect(wrapper).toBeTruthy()
  return wrapper!.querySelectorAll('circle').length
}

/** The file-row button carrying the given path. */
function fileRow(path: string): HTMLButtonElement {
  const btn = Array.from(container.querySelectorAll('button')).find((b) =>
    (b.textContent ?? '').includes(path),
  )
  expect(btn).toBeTruthy()
  return btn as HTMLButtonElement
}

describe('GitGraphPage — shell', () => {
  it('renders the header with the filter bar + diff-view toggle and the lane graph, with no in-page title', async () => {
    renderPage()
    await flush()
    await flush()

    // No in-page title: the viewer tab ("Git History") names the page.
    expect(container.textContent).not.toContain('Git History')
    expect(container.querySelector('[data-testid="diff-view-unified"]')).toBeTruthy()
    expect(container.querySelector('[data-testid="diff-view-split"]')).toBeTruthy()
    // The filter input + mode toggle render in the toolbar row.
    expect(container.querySelector('input[placeholder^="Filter by file"]')).toBeTruthy()
    expect(container.querySelector('button[title="Switch to regex"]')).toBeTruthy()
    // One SVG circle per commit — the lane gutter rendered.
    expect(gutterCircles()).toBe(3)
    expect(container.textContent).toContain('merge pr')
    expect(container.textContent).toContain('feat: x')
    expect(container.textContent).toContain('init')
  })

  it('shows the empty state when the repository has no commits', async () => {
    apiMocks.getGitHistory.mockResolvedValue(makePage([], 0, false))
    renderPage()
    await flush()
    await flush()
    expect(container.textContent).toContain('No commits yet')
  })

  it('shows the error state when the first page fails', async () => {
    apiMocks.getGitHistory.mockRejectedValue(new Error('boom'))
    renderPage()
    await flush()
    await flush()
    expect(container.textContent).toContain('boom')
  })
})

describe('GitGraphPage — pending history filter', () => {
  it('consumes gitPanelStore.pendingHistoryFilter into the file filter once, then clears it', async () => {
    // The "View History" file-tree action queues the filter before (or while)
    // opening this page; the page applies it on arrival and clears the store
    // so it never re-applies on subsequent renders.
    act(() => {
      useGitPanelStore.getState().setPendingHistoryFilter('src/foo.ts')
    })

    renderPage()
    await flush()
    await flush()

    const input = container.querySelector<HTMLInputElement>('input[placeholder^="Filter by file"]')
    expect(input?.value).toBe('src/foo.ts')
    expect(useGitPanelStore.getState().pendingHistoryFilter).toBeNull()
  })

  it('leaves the filter untouched when no pending filter is queued', async () => {
    renderPage()
    await flush()
    await flush()

    const input = container.querySelector<HTMLInputElement>('input[placeholder^="Filter by file"]')
    expect(input?.value).toBe('')
    expect(useGitPanelStore.getState().pendingHistoryFilter).toBeNull()
  })
})

describe('GitGraphPage — file filter', () => {
  /** Set a controlled input's value in a way React detects. */
  function setInputValue(input: HTMLInputElement, value: string) {
    const setter = Object.getOwnPropertyDescriptor(HTMLInputElement.prototype, 'value')!.set!
    setter.call(input, value)
    input.dispatchEvent(new Event('input', { bubbles: true }))
  }

  it('narrows to matching commits and hides the lane gutter while filtering', async () => {
    renderPage()
    await flush()
    await flush()

    const input = container.querySelector('input') as HTMLInputElement
    await act(async () => { setInputValue(input, '*.ts') })
    await flush()
    await flush()

    // Only the commit that touched src/a.ts; the gutter is hidden (a
    // filtered subset is not a connected graph).
    expect(gutterCircles()).toBe(0)
    expect(container.textContent).toContain('feat: x')
    expect(container.textContent).not.toContain('merge pr')
    expect(container.textContent).not.toContain('init')
  })

  it('shows "No matching commits" and "Invalid regex" states', async () => {
    renderPage()
    await flush()
    await flush()

    const input = container.querySelector('input') as HTMLInputElement
    await act(async () => { setInputValue(input, '*.rs') })
    await flush()
    await flush()
    expect(container.textContent).toContain('No matching commits')

    // '[' is VALID glob syntax — flip to regex mode first, as the panel
    // filter does.
    const toggle = container.querySelector('button[title="Switch to regex"]') as HTMLButtonElement
    await act(async () => { toggle.click() })
    await act(async () => { setInputValue(input, '[') })
    await flush()
    expect(container.textContent).toContain('Invalid regex')
    expect(container.textContent).not.toContain('No matching commits')
  })

  it('restores the full history and the gutter when the filter is cleared', async () => {
    renderPage()
    await flush()
    await flush()

    const input = container.querySelector('input') as HTMLInputElement
    await act(async () => { setInputValue(input, '*.ts') })
    await flush()
    await flush()
    expect(gutterCircles()).toBe(0)

    await act(async () => { setInputValue(input, '') })
    await flush()
    await flush()
    expect(gutterCircles()).toBe(3)
    expect(container.textContent).toContain('merge pr')
    expect(container.textContent).toContain('init')
  })
})

describe('GitGraphPage — commit context menu', () => {
  it('right-clicking a card opens the shared commit context menu', async () => {
    renderPage()
    await flush()
    await flush()

    const wrapper = rowWrapper('feat: x')
    await act(async () => {
      wrapper.dispatchEvent(
        new MouseEvent('contextmenu', { bubbles: true, cancelable: true, clientX: 12, clientY: 12 }),
      )
    })
    await flush()

    // The shared GitHistoryContextMenu component; the page owns this instance.
    const menu = container.querySelector('[role="menu"][aria-label="Commit actions"]')
    expect(menu).toBeTruthy()
    expect(menu!.textContent).toContain('View Commit')
    expect(menu!.textContent).toContain('Reset HEAD to This Commit')

    // Escape closes it.
    await act(async () => {
      document.dispatchEvent(new KeyboardEvent('keydown', { key: 'Escape' }))
    })
    expect(container.querySelector('[role="menu"][aria-label="Commit actions"]')).toBeNull()
  })
})

describe('GitGraphPage — card expansion', () => {
  it('expanding a card lazily fetches files + full message once and renders them', async () => {
    renderPage()
    await flush()
    await flush()

    act(() => {
      cardHeader('feat: x').click()
    })
    await flush()
    await flush()

    expect(apiMocks.getCommitFiles).toHaveBeenCalledWith('nnn')
    expect(apiMocks.getCommitMessage).toHaveBeenCalledTimes(1)
    expect(apiMocks.getCommitMessage).toHaveBeenCalledWith('nnn')
    // Full message + file list rendered.
    expect(container.textContent).toContain('Long body describing the change.')
    expect(container.textContent).toContain('src/a.ts')
    expect(container.textContent).toContain('README.md')
    // Backend stats triple (absent here) is derived from the file list.
    expect(container.textContent).toContain('+1')
  })

  it('fetches the commit diff ONCE per commit when files expand (cached for siblings)', async () => {
    renderPage()
    await flush()
    await flush()

    act(() => {
      cardHeader('feat: x').click()
    })
    await flush()
    await flush()

    act(() => {
      fileRow('src/a.ts').click()
    })
    await flush()
    await flush()

    expect(apiMocks.getCommitDiff).toHaveBeenCalledTimes(1)
    expect(apiMocks.getCommitDiff).toHaveBeenCalledWith('nnn')
    // The first file's slice rendered (add line visible).
    expect(container.textContent).toContain('export const x = 1')

    // The second file of the SAME commit hits the cache — still one RPC.
    act(() => {
      fileRow('README.md').click()
    })
    await flush()
    await flush()

    expect(apiMocks.getCommitDiff).toHaveBeenCalledTimes(1)
    expect(container.textContent).toContain('new line')
  })

  it('collapse + re-expand does not re-fetch cached message or files', async () => {
    renderPage()
    await flush()
    await flush()

    act(() => {
      cardHeader('feat: x').click()
    })
    await flush()
    await flush()
    act(() => {
      cardHeader('feat: x').click()
    })
    await flush()
    act(() => {
      cardHeader('feat: x').click()
    })
    await flush()
    await flush()

    expect(apiMocks.getCommitMessage).toHaveBeenCalledTimes(1)
    expect(apiMocks.getCommitFiles).toHaveBeenCalledTimes(1)
    expect(container.textContent).toContain('Long body describing the change.')
  })

  it('shows "No diff (merge commit)" and "No files (root commit)" empty states', async () => {
    renderPage()
    await flush()
    await flush()

    act(() => {
      cardHeader('merge pr').click()
    })
    await flush()
    await flush()
    expect(container.textContent).toContain('No diff (merge commit)')

    act(() => {
      cardHeader('merge pr').click() // collapse
    })
    await flush()

    act(() => {
      cardHeader('init').click()
    })
    await flush()
    await flush()
    expect(container.textContent).toContain('No files (root commit)')
  })

  it('renders the per-file diff placeholder while the commit diff is in flight', async () => {
    let resolveDiff: ((v: unknown) => void) | undefined
    apiMocks.getCommitDiff.mockImplementationOnce(
      () => new Promise((resolve) => {
        resolveDiff = resolve
      }),
    )
    renderPage()
    await flush()
    await flush()

    act(() => {
      cardHeader('feat: x').click()
    })
    await flush()
    act(() => {
      fileRow('src/a.ts').click()
    })
    await flush()

    expect(container.textContent).toContain('Loading diff…')

    await act(async () => {
      resolveDiff?.(COMMIT_DIFF)
      await Promise.resolve()
      await Promise.resolve()
      await Promise.resolve()
    })
    expect(container.textContent).toContain('export const x = 1')
  })

  it('shows the diff error placeholder when the commit diff fetch fails', async () => {
    apiMocks.getCommitDiff.mockRejectedValueOnce(new Error('diff failed'))
    renderPage()
    await flush()
    await flush()

    act(() => {
      cardHeader('feat: x').click()
    })
    await flush()
    act(() => {
      fileRow('src/a.ts').click()
    })
    await flush()
    await flush()

    expect(container.textContent).toContain('Failed to load diff')
  })
})

describe('GitGraphPage — deterministic heights', () => {
  it('row wrapper heights equal cardHeight(state, expanded) from the pure math', async () => {
    renderPage()
    await flush()
    await flush()

    // Collapsed: 32px rows.
    const collapsed = rowWrapper('feat: x')
    expect(collapsed.style.height).toBe('32px')

    // Expanded with loaded message + files, no file diff open.
    act(() => {
      cardHeader('feat: x').click()
    })
    await flush()
    await flush()

    const state: CardExpansionState = {
      messageLoading: false,
      messageFailed: false,
      fullMessage: FULL_MESSAGE,
      filesLoading: false,
      files: FILES['nnn'],
      fileDiffs: {},
    }
    const expected = cardHeight(state, true)
    expect(rowWrapper('feat: x').style.height).toBe(`${expected}px`)
    expect(expected).toBe(32 + expandedCardHeight(state))
    // 3-line message (8 + 3*16) + top pad 4 + 2 files (32) + bottom pad 6.
    expect(expandedCardHeight(state)).toBe(8 + 3 * 16 + 4 + 32 + 6)

    // Opening a file diff grows the row by exactly the slice's row math.
    act(() => {
      fileRow('src/a.ts').click()
    })
    await flush()
    await flush()

    const stateWithDiff: CardExpansionState = {
      ...state,
      fileDiffs: {
        'src/a.ts': {
          expanded: true,
          loading: false,
          failed: false,
          diffViewMode: 'unified',
          slice: COMMIT_DIFF[0],
        },
      },
    }
    expect(rowWrapper('feat: x').style.height).toBe(`${cardHeight(stateWithDiff, true)}px`)
  })

  it('re-computes the expanded row height per diff mode (split zips the add/del pair)', async () => {
    renderPage()
    await flush()
    await flush()

    act(() => {
      cardHeader('feat: x').click()
    })
    await flush()
    act(() => {
      fileRow('src/a.ts').click()
    })
    await flush()
    await flush()

    // Unified: the add+del pair renders as TWO rows.
    const unifiedH = Number.parseFloat(rowWrapper('feat: x').style.height)

    // Flip the SHARED toggle to split — the live page follows the store and
    // the zipped pair renders as ONE row, shrinking the reserved height by
    // exactly one diff row.
    act(() => {
      useReviewStore.getState().setDiffViewMode('split')
    })
    await flush()

    const splitH = Number.parseFloat(rowWrapper('feat: x').style.height)
    expect(splitH).toBe(unifiedH - DIFF_ROW_HEIGHT)
  })
})

describe('GitGraphPage — per-file context menu', () => {
  it('right-clicking a changed file opens "Open in Viewer", which opens the commit-file diff tab', async () => {
    renderPage()
    await flush()
    await flush()

    // Expand the card carrying src/a.ts.
    await act(async () => { cardHeader('feat: x').click() })
    await flush()
    await flush()

    const row = fileRow('src/a.ts')
    await act(async () => {
      row.dispatchEvent(
        new MouseEvent('contextmenu', { bubbles: true, cancelable: true, clientX: 8, clientY: 8 }),
      )
    })
    await flush()

    // The FILE menu opened — and the card-level commit menu did NOT win.
    const menu = container.querySelector('[role="menu"][aria-label="File actions"]')
    expect(menu).toBeTruthy()
    expect(menu!.textContent).toContain('Open in Viewer')
    expect(container.querySelector('[role="menu"][aria-label="Commit actions"]')).toBeNull()

    // The action opens the file's COMMIT-VERSION diff as a virtual tab.
    await act(async () => {
      ;(menu!.querySelector('button[role="menuitem"]') as HTMLButtonElement).click()
    })
    expect(fileViewerMocks.openVirtualFile).toHaveBeenCalledWith('c0wrk:commit:nnn/src/a.ts')
    expect(fileViewerMocks.setCollapsed).toHaveBeenCalledWith(false)
  })
})

describe('GitGraphPage — pagination', () => {
  it('loads the next page on "Load more" without replacing loaded commits', async () => {
    apiMocks.getGitHistory
      .mockResolvedValueOnce(makePage(COMMITS.slice(0, 2), 2, true))
      .mockResolvedValueOnce(makePage(COMMITS.slice(2), 3, false))
    renderPage()
    await flush()
    await flush()

    const more = Array.from(container.querySelectorAll('button')).find(
      (b) => (b.textContent ?? '').trim() === 'Load more',
    ) as HTMLButtonElement
    expect(more).toBeTruthy()
    expect(gutterCircles()).toBe(2)

    await act(async () => {
      more.click()
      await Promise.resolve()
      await Promise.resolve()
      await Promise.resolve()
    })
    await flush()

    // Requested at the previous page's next_skip; the root commit appended.
    expect(apiMocks.getGitHistory).toHaveBeenNthCalledWith(2, 300, 2)
    expect(gutterCircles()).toBe(3)
    expect(container.textContent).toContain('init')
    // Exhausted → button gone.
    expect(Array.from(container.querySelectorAll('button')).some((b) => (b.textContent ?? '').trim() === 'Load more')).toBe(false)
  })
})

describe('GitGraphPage — expansion persistence (gitGraphStore, app-session scope)', () => {
  it('restores the expanded card after unmount/remount within the app session', async () => {
    renderPage()
    await flush()
    await flush()

    act(() => {
      cardHeader('feat: x').click()
    })
    await flush()
    await flush()
    expect(container.textContent).toContain('Long body describing the change.')
    const messageCallsAfterExpand = apiMocks.getCommitMessage.mock.calls.length

    remountPage()
    await flush()
    await flush()

    // The fresh instance mounts expanded: full message + file list back.
    expect(rowWrapper('feat: x').style.height).not.toBe('32px')
    expect(container.textContent).toContain('Long body describing the change.')
    expect(container.textContent).toContain('src/a.ts')
    // Payload caches are page-local — the restored card re-fetches them.
    expect(apiMocks.getCommitMessage.mock.calls.length).toBe(messageCallsAfterExpand + 1)
    expect(apiMocks.getCommitMessage).toHaveBeenLastCalledWith('nnn')
    expect(apiMocks.getCommitFiles).toHaveBeenLastCalledWith('nnn')
  })

  it('restores the collapsed state after unmount/remount too', async () => {
    renderPage()
    await flush()
    await flush()

    act(() => {
      cardHeader('feat: x').click()
    })
    await flush()
    await flush()
    act(() => {
      cardHeader('feat: x').click() // collapse
    })
    await flush()

    remountPage()
    await flush()
    await flush()

    expect(rowWrapper('feat: x').style.height).toBe('32px')
    expect(container.textContent).not.toContain('Long body describing the change.')
    // No expansion on the fresh instance → no lazy fetches there.
    const callsAfterRemount = apiMocks.getCommitMessage.mock.calls.length
    expect(callsAfterRemount).toBe(1) // only the pre-remount expand fetched
  })

  it('scopes the expansion per project', async () => {
    renderPage()
    await flush()
    await flush()

    act(() => {
      cardHeader('feat: x').click()
    })
    await flush()
    await flush()

    // Switch to a project with no persisted expansion → all collapsed.
    act(() => {
      useProjectStore.setState({ activeProjectId: 'proj-2' })
    })
    await flush()
    expect(rowWrapper('feat: x').style.height).toBe('32px')
    expect(container.textContent).not.toContain('Long body describing the change.')

    // Back to the original project → expanded again, WITHOUT re-fetching
    // the payloads (this page instance's caches never died).
    const messageCalls = apiMocks.getCommitMessage.mock.calls.length
    act(() => {
      useProjectStore.setState({ activeProjectId: 'proj-1' })
    })
    await flush()
    await flush()
    expect(rowWrapper('feat: x').style.height).not.toBe('32px')
    expect(container.textContent).toContain('Long body describing the change.')
    expect(apiMocks.getCommitMessage.mock.calls.length).toBe(messageCalls)
  })

  it('restores open file diffs of the expanded card (fetches the commit diff)', async () => {
    renderPage()
    await flush()
    await flush()

    act(() => {
      cardHeader('feat: x').click()
    })
    await flush()
    await flush()
    act(() => {
      fileRow('src/a.ts').click()
    })
    await flush()
    await flush()
    const diffCallsBeforeRemount = apiMocks.getCommitDiff.mock.calls.length

    remountPage()
    await flush()
    await flush()

    // The restored card re-fetches the commit diff for its open file
    // (one fresh RPC on the new instance) and renders the slice.
    expect(apiMocks.getCommitDiff.mock.calls.length).toBe(diffCallsBeforeRemount + 1)
    expect(apiMocks.getCommitDiff).toHaveBeenLastCalledWith('nnn')
    expect(container.textContent).toContain('export const x = 1')
  })

  it('keeps a persisted SHA beyond the first page dormant until its page loads', async () => {
    apiMocks.getGitHistory
      .mockResolvedValueOnce(makePage(COMMITS.slice(0, 2), 2, true))
      .mockResolvedValueOnce(makePage(COMMITS.slice(2), 3, false))
    useGitGraphStore.setState({
      byProject: { 'proj-1': { expandedSha: 'ooo', expandedFiles: {} } },
    })
    renderPage()
    await flush()
    await flush()

    // 'ooo' is not in the first page — no lazy fetches, no expansion.
    expect(apiMocks.getCommitFiles).not.toHaveBeenCalled()
    expect(apiMocks.getCommitMessage).not.toHaveBeenCalled()

    const more = Array.from(container.querySelectorAll('button')).find(
      (b) => (b.textContent ?? '').trim() === 'Load more',
    ) as HTMLButtonElement
    await act(async () => {
      more.click()
      await Promise.resolve()
      await Promise.resolve()
      await Promise.resolve()
    })
    await flush()
    await flush()

    // The SHA arrived via loadMore → the restore effect fires its lazy
    // fetches and the card renders expanded.
    expect(apiMocks.getCommitFiles).toHaveBeenCalledWith('ooo')
    expect(apiMocks.getCommitMessage).toHaveBeenCalledWith('ooo')
    expect(rowWrapper('init').style.height).not.toBe('32px')
  })

  it('never fetches for a persisted SHA absent from the history', async () => {
    useGitGraphStore.setState({
      byProject: { 'proj-1': { expandedSha: 'zzz', expandedFiles: {} } },
    })
    renderPage()
    await flush()
    await flush()

    expect(apiMocks.getCommitFiles).not.toHaveBeenCalled()
    expect(apiMocks.getCommitMessage).not.toHaveBeenCalled()
    expect(apiMocks.getCommitDiff).not.toHaveBeenCalled()
    expect(rowWrapper('feat: x').style.height).toBe('32px')
  })
})
