// @vitest-environment jsdom
import { describe, it, expect, vi, beforeEach, afterEach } from 'vitest'
import { act } from 'react'
import { createRoot, type Root } from 'react-dom/client'

// --- Mock the git API wrappers so tests never touch the Wails backend ---
const { gitMocks } = vi.hoisted(() => ({
  gitMocks: {
    getGitHistory: vi.fn(),
    getCommitFiles: vi.fn(),
    getCommitFilesBatch: vi.fn(),
    GIT_HISTORY_PAGE_SIZE: 300,
  },
}))

vi.mock('@/api/git', () => gitMocks)
vi.mock('@/lib/logger', () => ({ logger: { error: vi.fn() } }))

// Mock the file viewer store so we can assert how pseudo-path tabs are
// opened: the SHA click goes through openFile (pre-existing), the graph
// button through openVirtualFile (virtual — never backed by a disk file).
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

// Mock the virtualizer so all rows render in jsdom (which has no layout
// engine, so the scroll container's height is 0 and the real virtualizer
// would mount zero items). The mock returns every item with correct
// absolute offsets so the component's positioning logic is still exercised.
vi.mock('@tanstack/react-virtual', () => ({
  useVirtualizer: (options: {
    count: number
    estimateSize: (i: number) => number
    getItemKey: (i: number) => string | number
  }) => {
    const items = Array.from({ length: options.count }, (_, i) => ({
      index: i,
      key: options.getItemKey(i),
      start: Array.from({ length: i }, (_, j) => options.estimateSize(j)).reduce(
        (a, b) => a + b,
        0,
      ),
      size: options.estimateSize(i),
    }))
    const totalSize = items.reduce((sum, item) => sum + item.size, 0)
    return {
      getVirtualItems: () => items,
      getTotalSize: () => totalSize,
    }
  },
}))

import { GitHistoryTab } from './GitHistoryTab'
import { TooltipProvider } from '@/components/ui/tooltip'

let container: HTMLDivElement
let root: Root

/** Local commit shape mirroring the wire model (`stats` optional — a stale backend may omit it). */
interface TestCommit {
  sha: string
  parents: string[]
  author: string
  email: string
  date: string
  message: string
  refs: string[]
  stats?: { added: number; modified: number; deleted: number }
}

const COMMITS: TestCommit[] = [
  { sha: 'aaa', parents: ['bbb'], author: 'Jane', email: 'j@x', date: '2026-07-10', message: 'feat: x', refs: ['HEAD -> main'], stats: { added: 1, modified: 1, deleted: 0 } },
  { sha: 'bbb', parents: ['ccc'], author: 'Jane', email: 'j@x', date: '2026-07-09', message: 'docs: readme', refs: [], stats: { added: 0, modified: 1, deleted: 0 } },
  { sha: 'ccc', parents: [], author: 'Jane', email: 'j@x', date: '2026-07-08', message: 'init', refs: [], stats: { added: 1, modified: 0, deleted: 0 } },
]

const FILES: Record<string, { path: string; status: string }[]> = {
  aaa: [{ path: 'src/a.ts', status: 'A' }, { path: 'README.md', status: 'M' }],
  bbb: [{ path: 'README.md', status: 'M' }],
  ccc: [{ path: 'src/main.go', status: 'A' }],
}

/** Build a paginated GetGitHistory response for the mocks. */
function makePage(commits: TestCommit[], nextSkip: number, hasMore: boolean) {
  return { commits, next_skip: nextSkip, has_more: hasMore }
}

beforeEach(() => {
  gitMocks.getGitHistory.mockReset()
  gitMocks.getCommitFiles.mockReset()
  gitMocks.getCommitFilesBatch.mockReset()
  gitMocks.getGitHistory.mockResolvedValue(makePage(COMMITS, COMMITS.length, false))
  gitMocks.getCommitFiles.mockImplementation((sha: string) =>
    Promise.resolve(FILES[sha] ?? []),
  )
  gitMocks.getCommitFilesBatch.mockImplementation((shas: string[]) =>
    Promise.resolve(
      Object.fromEntries(shas.map((sha) => [sha, FILES[sha] ?? []])),
    ),
  )
  fileViewerMocks.openFile.mockReset()
  fileViewerMocks.openVirtualFile.mockReset()
  fileViewerMocks.setCollapsed.mockReset()
  container = document.createElement('div')
  document.body.appendChild(container)
  root = createRoot(container)
})

afterEach(() => {
  act(() => { root.unmount() })
  container.remove()
  document.body.innerHTML = ''
})

/** Set a controlled input's value in a way React detects (see BranchPicker.test). */
function setInputValue(input: HTMLInputElement, value: string) {
  const setter = Object.getOwnPropertyDescriptor(HTMLInputElement.prototype, 'value')!.set!
  setter.call(input, value)
  input.dispatchEvent(new Event('input', { bubbles: true }))
}

/** Flush microtasks so async effects (getGitHistory / getCommitFiles) resolve. */
function flush(): Promise<void> {
  return act(async () => {
    await Promise.resolve()
    await Promise.resolve()
    await Promise.resolve()
  })
}

function renderTab() {
  act(() => {
    root.render(
      <TooltipProvider>
        <GitHistoryTab />
      </TooltipProvider>,
    )
  })
}

/** Commit messages currently rendered, in DOM order (newest-first). */
function commitMessages(): string[] {
  return Array.from(container.querySelectorAll('[data-commit-row]'))
    .map((el) => el.textContent ?? '')
    .map((t) => COMMITS.find((c) => t.includes(c.message))?.message)
    .filter((m): m is string => m !== undefined)
}

/** The lane graph renders one <circle> per commit; it is unique to the gutter
 *  (the filter row's GitGraph icon button also draws an svg — excluded by its
 *  lucide class so the count stays per-commit). */
function graphNodes(): number {
  return container.querySelectorAll('svg:not([class*="lucide"]) circle').length
}

describe('GitHistoryTab file filter', () => {
  it('renders the lane graph and all commits before filtering', async () => {
    renderTab()
    await flush()
    await flush()
    expect(graphNodes()).toBe(3)
    expect(commitMessages()).toEqual(['feat: x', 'docs: readme', 'init'])
  })

  it('hides the graph and narrows to matching commits when filtering', async () => {
    renderTab()
    await flush()
    await flush()
    const input = container.querySelector('input') as HTMLInputElement
    await act(async () => { setInputValue(input, '*.ts') })
    await flush()
    await flush()
    // Graph hidden while filtering; only the commit that touched src/a.ts.
    expect(graphNodes()).toBe(0)
    expect(commitMessages()).toEqual(['feat: x'])
  })

  it('shows "No matching commits" when nothing matches', async () => {
    renderTab()
    await flush()
    await flush()
    const input = container.querySelector('input') as HTMLInputElement
    await act(async () => { setInputValue(input, '*.rs') })
    await flush()
    await flush()
    expect(container.textContent).toContain('No matching commits')
    expect(graphNodes()).toBe(0)
  })

  it('restores the graph and all commits when the filter is cleared', async () => {
    renderTab()
    await flush()
    await flush()
    const input = container.querySelector('input') as HTMLInputElement
    await act(async () => { setInputValue(input, '*.ts') })
    await flush()
    await flush()
    expect(graphNodes()).toBe(0)
    await act(async () => { setInputValue(input, '') })
    await flush()
    await flush()
    expect(graphNodes()).toBe(3)
    expect(commitMessages()).toEqual(['feat: x', 'docs: readme', 'init'])
  })

  it('supports regex mode', async () => {
    renderTab()
    await flush()
    await flush()
    const toggle = container.querySelector('button[title="Switch to regex"]') as HTMLButtonElement
    await act(async () => { toggle.click() })
    const input = container.querySelector('input') as HTMLInputElement
    await act(async () => { setInputValue(input, 'src/.*\\.ts') })
    await flush()
    await flush()
    expect(graphNodes()).toBe(0)
    expect(commitMessages()).toEqual(['feat: x'])
  })

  it('shows "Invalid regex" for a syntactically broken regex', async () => {
    renderTab()
    await flush()
    await flush()
    const toggle = container.querySelector('button[title="Switch to regex"]') as HTMLButtonElement
    await act(async () => { toggle.click() })
    const input = container.querySelector('input') as HTMLInputElement
    await act(async () => { setInputValue(input, '[') })
    await flush()
    expect(container.textContent).toContain('Invalid regex')
    expect(container.textContent).not.toContain('No matching commits')
    expect(graphNodes()).toBe(0)
  })
})

describe('GitHistoryTab rendering', () => {
  it('renders root commits with empty parents array', async () => {
    // The backend sends [] (not null) for root commits via
    // parents := []string{}. The type guard rejects null arrays.
    gitMocks.getGitHistory.mockResolvedValue(makePage([
      { sha: 'aaa', parents: ['bbb'], author: 'Jane', email: 'j@x', date: 'd', message: 'feat', refs: [] },
      { sha: 'bbb', parents: [], author: 'Jane', email: 'j@x', date: 'd', message: 'root', refs: [] },
    ], 2, false))

    renderTab()
    await flush()
    await flush()
    // Both commits should render — the root commit has empty parents
    expect(graphNodes()).toBe(2)
  })
})

describe('GitHistoryTab commit SHA click', () => {
  it('opens a read-only commit-review page when the SHA is clicked', async () => {
    renderTab()
    await flush()
    await flush()

    // The SHA renders as a role="button" span with title "View commit changes".
    const shaButton = container.querySelector('span[title="View commit changes"]') as HTMLSpanElement
    expect(shaButton).toBeTruthy()
    expect(shaButton.textContent).toBe('aaa')

    await act(async () => { shaButton.click() })

    // The file viewer should be asked to open the synthetic commit path
    // and expand so the review is visible.
    expect(fileViewerMocks.openFile).toHaveBeenCalledWith('c0wrk:commit:aaa')
    expect(fileViewerMocks.setCollapsed).toHaveBeenCalledWith(false)
  })

  it('rows are inert: no chevrons, no inline expansion, and a body click opens nothing', async () => {
    renderTab()
    await flush()
    await flush()

    const rows = container.querySelectorAll('[data-commit-row]')
    expect(rows).toHaveLength(3)
    // No chevron glyphs (inline expansion was removed) and no changed-file
    // lists — the diff review lives in the file viewer via the SHA click.
    expect(container.querySelector('.lucide-chevron-right')).toBeNull()
    expect(container.querySelector('.lucide-chevron-down')).toBeNull()
    expect(container.textContent).not.toContain('src/a.ts')
    expect(container.textContent).not.toContain('Loading files')

    // Clicking the row body (not the SHA) must not open anything.
    await act(async () => { (rows[0] as HTMLElement).click() })
    expect(fileViewerMocks.openFile).not.toHaveBeenCalled()
    expect(fileViewerMocks.openVirtualFile).not.toHaveBeenCalled()
    expect(fileViewerMocks.setCollapsed).not.toHaveBeenCalled()
  })
})

describe('GitHistoryTab commit graph button', () => {
  it('opens the commit-graph tab in the file viewer and expands it', async () => {
    renderTab()
    await flush()
    await flush()

    // The graph button sits left of the filter bar, labelled only by its
    // title/aria attributes (icon-only control).
    const graphButton = container.querySelector('button[title="Open commit graph"]') as HTMLButtonElement
    expect(graphButton).toBeTruthy()
    expect(graphButton.getAttribute('aria-label')).toBe('Open commit graph')

    await act(async () => { graphButton.click() })

    // The viewer is asked to open the synthetic graph pseudo-path as a
    // VIRTUAL file (openVirtualFile — the canonical openResearch/openPaper
    // pattern) and expand, so the graph is visible. It must NOT go through
    // openFile: a non-virtual entry would make the viewer's data loader fire
    // a guaranteed-failing ReadFile RPC for the pseudo-path, repeat it on
    // every pathless tree_changed burst, and persist the tab across restarts.
    expect(fileViewerMocks.openVirtualFile).toHaveBeenCalledWith('c0wrk:graph')
    expect(fileViewerMocks.openFile).not.toHaveBeenCalled()
    expect(fileViewerMocks.setCollapsed).toHaveBeenCalledWith(false)
  })
})

describe('GitHistoryTab commit stats cell', () => {
  it('renders the per-commit changed-file stats at the end of each row', async () => {
    renderTab()
    await flush()
    await flush()

    // Row aaa: 1 added, 1 modified, 0 deleted.
    const firstRow = container.querySelector('[data-commit-row]') as HTMLElement
    expect(firstRow.textContent).toContain('+1')
    expect(firstRow.textContent).toContain('~1')
    expect(firstRow.textContent).toContain('-0')
  })

  it('omits the stats cell when the backend sent no stats (stale bindings)', async () => {
    gitMocks.getGitHistory.mockResolvedValue(makePage(
      [{ sha: 'aaa', parents: [], author: 'Jane', email: 'j@x', date: 'd', message: 'no stats', refs: [] }],
      1,
      false,
    ))
    renderTab()
    await flush()
    await flush()

    const row = container.querySelector('[data-commit-row]') as HTMLElement
    expect(row.textContent).not.toContain('+0')
    expect(row.textContent).toContain('no stats')
  })
})

describe('GitHistoryTab pagination', () => {
  // Two pages: page 1 = [aaa, bbb] (saturated), page 2 = [ccc] (remainder).
  const PAGE_1 = [
    { sha: 'aaa', parents: ['bbb'], author: 'Jane', email: 'j@x', date: '2026-07-10', message: 'feat: x', refs: ['HEAD -> main'] },
    { sha: 'bbb', parents: ['ccc'], author: 'Jane', email: 'j@x', date: '2026-07-09', message: 'docs: readme', refs: [] },
  ]
  const PAGE_2 = [
    { sha: 'ccc', parents: [], author: 'Jane', email: 'j@x', date: '2026-07-08', message: 'init', refs: [] },
  ]

  function loadMoreButton(): HTMLButtonElement | null {
    return (
      (Array.from(container.querySelectorAll('button')).find(
        (b) => (b.textContent ?? '').trim() === 'Load more',
      ) as HTMLButtonElement | undefined) ?? null
    )
  }

  it('loads only the first page on mount and exposes Load more when has_more', async () => {
    gitMocks.getGitHistory.mockResolvedValue(makePage(PAGE_1 as typeof COMMITS, 2, true))
    renderTab()
    await flush()
    await flush()

    // Only the first page was requested, at the default size + offset 0.
    expect(gitMocks.getGitHistory).toHaveBeenCalledTimes(1)
    expect(gitMocks.getGitHistory).toHaveBeenCalledWith(300, 0)
    expect(commitMessages()).toEqual(['feat: x', 'docs: readme'])
    expect(graphNodes()).toBe(2)
    expect(loadMoreButton()).toBeTruthy()
  })

  it('hides Load more when the history is exhausted (has_more=false)', async () => {
    gitMocks.getGitHistory.mockResolvedValue(makePage(COMMITS, 3, false))
    renderTab()
    await flush()
    await flush()
    expect(loadMoreButton()).toBeNull()
  })

  it('appends the next page on Load more without replacing already-loaded commits', async () => {
    gitMocks.getGitHistory
      .mockResolvedValueOnce(makePage(PAGE_1 as typeof COMMITS, 2, true))
      .mockResolvedValueOnce(makePage(PAGE_2 as typeof COMMITS, 3, false))

    renderTab()
    await flush()
    await flush()

    // Capture a first-page row to prove it survives the append (same DOM node).
    const firstRowBefore = Array.from(container.querySelectorAll('[data-commit-row]')).find(
      (b) => (b.textContent ?? '').includes('feat: x'),
    ) as HTMLElement
    expect(firstRowBefore).toBeTruthy()

    const btn = loadMoreButton()
    expect(btn).toBeTruthy()
    await act(async () => { btn!.click() })
    await flush()
    await flush()

    // The second page is requested at the previous page's next_skip.
    expect(gitMocks.getGitHistory).toHaveBeenNthCalledWith(2, 300, 2)
    // All three commits are present, in order — the first page was appended
    // to, never rewritten.
    expect(commitMessages()).toEqual(['feat: x', 'docs: readme', 'init'])
    expect(graphNodes()).toBe(3)
    // The first-page row is still the SAME DOM node (no unmount/remount).
    expect(container.contains(firstRowBefore)).toBe(true)
    // Exhausted → Load more disappears.
    expect(loadMoreButton()).toBeNull()
  })

  it('ignores repeated Load more clicks while a page is in flight', async () => {
    let resolveSecond: ((v: unknown) => void) | undefined
    gitMocks.getGitHistory
      .mockResolvedValueOnce(makePage(PAGE_1 as typeof COMMITS, 2, true))
      .mockImplementationOnce(() => new Promise((resolve) => { resolveSecond = resolve }))
      .mockResolvedValueOnce(makePage(PAGE_2 as typeof COMMITS, 3, false))

    renderTab()
    await flush()
    await flush()

    const btn = loadMoreButton()
    expect(btn).toBeTruthy()
    await act(async () => { btn!.click(); btn!.click() })
    // Mount + a single in-flight second page; the second click was a no-op.
    expect(gitMocks.getGitHistory).toHaveBeenCalledTimes(2)

    await act(async () => { resolveSecond?.(makePage(PAGE_2 as typeof COMMITS, 3, false)) })
    await flush()
    await flush()
    expect(commitMessages()).toEqual(['feat: x', 'docs: readme', 'init'])
  })

  it('keeps Load more below the whole history (list wrapper opts out of flex-shrink)', async () => {
    gitMocks.getGitHistory.mockResolvedValue(makePage(PAGE_1 as typeof COMMITS, 2, true))
    renderTab()
    await flush()
    await flush()

    const btn = loadMoreButton()
    expect(btn).toBeTruthy()

    // The button lives inside the scroll container (a flex column). Walk up
    // to that container.
    let scroll: HTMLElement | null = btn!.parentElement
    while (scroll && !scroll.classList.contains('overflow-y-auto')) {
      scroll = scroll.parentElement
    }
    expect(scroll).toBeTruthy()

    // The virtualized list wrapper is a DIRECT child of the scroll container
    // and must carry `shrink-0`: without it flex collapses the wrapper to the
    // viewport height (its only children are absolutely positioned), which
    // drags the in-flow button up to the bottom of the visible area where the
    // overflowing rows paint over it.
    const listWrapper = Array.from(scroll!.children).find((c) =>
      c.classList.contains('shrink-0'),
    )
    expect(listWrapper).toBeTruthy()

    // The button flows AFTER the full-height list (not inside it), so it only
    // becomes visible once the user scrolls to the very bottom.
    expect(listWrapper!.contains(btn!)).toBe(false)
    expect(scroll!.lastElementChild).toBe(btn!.parentElement)
  })
})
