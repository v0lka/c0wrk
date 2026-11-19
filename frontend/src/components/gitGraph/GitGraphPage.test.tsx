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
// Same mock as GitHistoryTab.test: every item with correct offsets.
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

import { GitGraphPage } from './GitGraphPage'
import { cardHeight, expandedCardHeight, DIFF_ROW_HEIGHT, type CardExpansionState } from './gitGraphCardHeights'
import { useReviewStore } from '@/stores/reviewStore'

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
  it('renders the header with the shared diff-view toggle and the lane graph', async () => {
    renderPage()
    await flush()
    await flush()

    expect(container.textContent).toContain('Git History')
    expect(container.querySelector('[data-testid="diff-view-unified"]')).toBeTruthy()
    expect(container.querySelector('[data-testid="diff-view-split"]')).toBeTruthy()
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
