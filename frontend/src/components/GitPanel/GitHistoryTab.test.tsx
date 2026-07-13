// @vitest-environment jsdom
import { describe, it, expect, vi, beforeEach, afterEach } from 'vitest'
import { act } from 'react'
import { createRoot, type Root } from 'react-dom/client'

// jsdom lacks window.localStorage, which zustand's persist middleware
// captures at store-creation time. Install an in-memory polyfill before any
// store module is imported (mirrors BranchPicker.test.tsx).
vi.hoisted(() => {
  const g = globalThis as Record<string, unknown>
  const win = (g.window as Record<string, unknown> | undefined) ?? g
  g.IS_REACT_ACT_ENVIRONMENT = true
  win.IS_REACT_ACT_ENVIRONMENT = true
  const map = new Map<string, string>()
  win.localStorage = {
    getItem: (k: string) => map.get(k) ?? null,
    setItem: (k: string, v: string) => { map.set(k, v) },
    removeItem: (k: string) => { map.delete(k) },
    clear: () => map.clear(),
    key: (i: number) => Array.from(map.keys())[i] ?? null,
    get length() { return map.size },
  }
})

// --- Mock the git API wrappers so tests never touch the Wails backend ---
const { gitMocks } = vi.hoisted(() => ({
  gitMocks: {
    getGitHistory: vi.fn(),
    getCommitFiles: vi.fn(),
    getCommitFilesBatch: vi.fn(),
  },
}))

vi.mock('@/api/git', () => gitMocks)
vi.mock('@/lib/logger', () => ({ logger: { error: vi.fn() } }))

import { GitHistoryTab } from './GitHistoryTab'
import { TooltipProvider } from '@/components/ui/tooltip'

let container: HTMLDivElement
let root: Root

const COMMITS = [
  { sha: 'aaa', parents: ['bbb'], author: 'Jane', email: 'j@x', date: '2026-07-10', message: 'feat: x', refs: ['HEAD -> main'] },
  { sha: 'bbb', parents: ['ccc'], author: 'Jane', email: 'j@x', date: '2026-07-09', message: 'docs: readme', refs: [] },
  { sha: 'ccc', parents: [], author: 'Jane', email: 'j@x', date: '2026-07-08', message: 'init', refs: [] },
]

const FILES: Record<string, { path: string; status: string }[]> = {
  aaa: [{ path: 'src/a.ts', status: 'A' }, { path: 'README.md', status: 'M' }],
  bbb: [{ path: 'README.md', status: 'M' }],
  ccc: [{ path: 'src/main.go', status: 'A' }],
}

beforeEach(() => {
  gitMocks.getGitHistory.mockReset()
  gitMocks.getCommitFiles.mockReset()
  gitMocks.getCommitFilesBatch.mockReset()
  gitMocks.getGitHistory.mockResolvedValue(COMMITS)
  gitMocks.getCommitFiles.mockImplementation((sha: string) =>
    Promise.resolve(FILES[sha] ?? []),
  )
  gitMocks.getCommitFilesBatch.mockImplementation((shas: string[]) =>
    Promise.resolve(
      Object.fromEntries(shas.map((sha) => [sha, FILES[sha] ?? []])),
    ),
  )
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
  return Array.from(container.querySelectorAll('button'))
    .map((b) => b.textContent ?? '')
    .filter((t) => COMMITS.some((c) => t.includes(c.message)))
    .map((t) => COMMITS.find((c) => t.includes(c.message))!.message)
}

/** The lane graph renders one <circle> per commit; it is unique to the gutter. */
function graphNodes(): number {
  return container.querySelectorAll('svg circle').length
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

describe('GitHistoryTab "Load more" pagination', () => {
  /** Generate a page of `count` commits with unique SHAs. */
  function makePage(prefix: string, count: number, firstParent?: string) {
    return Array.from({ length: count }, (_, i) => ({
      sha: `${prefix}-${i}`,
      parents: i > 0 ? [`${prefix}-${i - 1}`] : firstParent ? [firstParent] : [],
      author: 'Jane',
      email: 'j@x',
      date: '2026-07-10',
      message: `${prefix} commit ${i}`,
      refs: [] as string[],
    }))
  }

  /** Find the "Load more" button by text content. */
  function loadMoreButton(): HTMLButtonElement | undefined {
    return Array.from(container.querySelectorAll('button')).find(
      (b) => b.textContent?.includes('Load more') || b.textContent?.includes('Loading'),
    )
  }

  it('loads the next page on "Load more" click', async () => {
    const page1 = makePage('p1', 25)
    const page2 = makePage('p2', 25, 'p1-24')
    gitMocks.getGitHistory.mockImplementation((_limit: number, skip: number) => {
      if (skip === 0) return Promise.resolve(page1)
      if (skip === 25) return Promise.resolve(page2)
      return Promise.resolve([])
    })

    renderTab()
    await flush()
    await flush()
    expect(graphNodes()).toBe(25)

    const btn = loadMoreButton()
    expect(btn).toBeTruthy()
    await act(async () => { btn!.click() })
    await flush()
    await flush()

    expect(graphNodes()).toBe(50)
  })

  it('prevents duplicate fetches on rapid double-click (ref guard)', async () => {
    const page1 = makePage('p1', 25)
    const page2 = makePage('p2', 25, 'p1-24')
    gitMocks.getGitHistory.mockImplementation((_limit: number, skip: number) => {
      if (skip === 0) return Promise.resolve(page1)
      if (skip === 25) return Promise.resolve(page2)
      return Promise.resolve([])
    })

    renderTab()
    await flush()
    await flush()
    expect(graphNodes()).toBe(25)

    const btn = loadMoreButton()
    expect(btn).toBeTruthy()
    // Rapid double-click before React can disable the button
    await act(async () => {
      btn!.click()
      btn!.click()
    })
    await flush()
    await flush()

    // Only one fetch for skip=25 — not two
    const skip25Calls = gitMocks.getGitHistory.mock.calls.filter(([, s]) => s === 25)
    expect(skip25Calls).toHaveLength(1)
    // 50 nodes, not 75 (no duplicate page appended)
    expect(graphNodes()).toBe(50)
  })

  it('renders root commits with empty parents array', async () => {
    // The backend sends [] (not null) for root commits via
    // parents := []string{}. The type guard rejects null arrays.
    gitMocks.getGitHistory.mockResolvedValue([
      { sha: 'aaa', parents: ['bbb'], author: 'Jane', email: 'j@x', date: 'd', message: 'feat', refs: [] },
      { sha: 'bbb', parents: [], author: 'Jane', email: 'j@x', date: 'd', message: 'root', refs: [] },
    ])

    renderTab()
    await flush()
    await flush()
    // Both commits should render — the root commit has empty parents
    expect(graphNodes()).toBe(2)
  })
})

describe('GitHistoryTab filter auto-load', () => {
  /** Generate a page of `count` commits with unique SHAs. */
  function makePage(prefix: string, count: number, firstParent?: string) {
    return Array.from({ length: count }, (_, i) => ({
      sha: `${prefix}-${i}`,
      parents: i > 0 ? [`${prefix}-${i - 1}`] : firstParent ? [firstParent] : [],
      author: 'Jane',
      email: 'j@x',
      date: '2026-07-10',
      message: `${prefix} commit ${i}`,
      refs: [] as string[],
    }))
  }

  /** Find the "Load more" button by text content. */
  function loadMoreButton(): HTMLButtonElement | undefined {
    return Array.from(container.querySelectorAll('button')).find(
      (b) => b.textContent?.includes('Load more') || b.textContent?.includes('Loading'),
    )
  }

  /** Count rendered commit rows in the filtered view (each has a short-SHA span). */
  function commitRowCount(): number {
    return container.querySelectorAll('button span.text-info').length
  }

  /** Files mock: `matchCount` commits per page have `src/a.ts`, the rest have `other.txt`. */
  function filesMock(matchCount: Record<string, number>) {
    return (shas: string[]): Promise<Record<string, { path: string; status: string }[]>> => {
      const result: Record<string, { path: string; status: string }[]> = {}
      for (const sha of shas) {
        const [prefix, idxStr] = sha.split('-') as [string, string]
        const idx = parseInt(idxStr, 10)
        const limit = matchCount[prefix] ?? 0
        result[sha] =
          idx < limit
            ? [{ path: 'src/a.ts', status: 'M' }]
            : [{ path: 'other.txt', status: 'M' }]
      }
      return Promise.resolve(result)
    }
  }

  it('does not auto-load when no filter is active', async () => {
    gitMocks.getGitHistory.mockImplementation((_limit: number, skip: number) => {
      if (skip === 0) return Promise.resolve(makePage('p1', 25))
      if (skip === 25) return Promise.resolve(makePage('p2', 25, 'p1-24'))
      return Promise.resolve([])
    })

    renderTab()
    await flush()
    await flush()
    for (let i = 0; i < 5; i++) await flush()

    // Only the initial load (skip=0) — no auto-loading without a filter
    expect(gitMocks.getGitHistory).toHaveBeenCalledTimes(1)
    expect(loadMoreButton()).toBeTruthy()
  })

  it('auto-loads more pages until enough matches are found', async () => {
    gitMocks.getGitHistory.mockImplementation((_limit: number, skip: number) => {
      if (skip === 0) return Promise.resolve(makePage('p1', 25))
      if (skip === 25) return Promise.resolve(makePage('p2', 25, 'p1-24'))
      if (skip === 50) return Promise.resolve(makePage('p3', 25, 'p2-24'))
      return Promise.resolve([])
    })
    // 3 matches in page 1 + 22 in page 2 = 25 (target reached)
    gitMocks.getCommitFilesBatch.mockImplementation(filesMock({ p1: 3, p2: 22 }))

    renderTab()
    await flush()
    await flush()

    const input = container.querySelector('input') as HTMLInputElement
    await act(async () => { setInputValue(input, 'src/a.ts') })
    for (let i = 0; i < 8; i++) await flush()

    // Auto-loaded page 2 (skip=25) but NOT page 3 (skip=50)
    const skip25Calls = gitMocks.getGitHistory.mock.calls.filter(([, s]) => s === 25)
    expect(skip25Calls).toHaveLength(1)
    const skip50Calls = gitMocks.getGitHistory.mock.calls.filter(([, s]) => s === 50)
    expect(skip50Calls).toHaveLength(0)

    // 25 matched commits rendered
    expect(commitRowCount()).toBe(25)
  })

  it('stops auto-loading when the log is exhausted', async () => {
    gitMocks.getGitHistory.mockImplementation((_limit: number, skip: number) => {
      if (skip === 0) return Promise.resolve(makePage('p1', 25))
      // 10 < page size → hasMore becomes false
      if (skip === 25) return Promise.resolve(makePage('p2', 10, 'p1-24'))
      return Promise.resolve([])
    })
    // 3 matches in page 1 + 2 in page 2 = 5 (below target, but log exhausted)
    gitMocks.getCommitFilesBatch.mockImplementation(filesMock({ p1: 3, p2: 2 }))

    renderTab()
    await flush()
    await flush()

    const input = container.querySelector('input') as HTMLInputElement
    await act(async () => { setInputValue(input, 'src/a.ts') })
    for (let i = 0; i < 8; i++) await flush()

    // Loaded page 2 (skip=25) but NOT page 3 (skip=50)
    const skip50Calls = gitMocks.getGitHistory.mock.calls.filter(([, s]) => s === 50)
    expect(skip50Calls).toHaveLength(0)

    // 5 matched commits, no "Load more" (log exhausted)
    expect(commitRowCount()).toBe(5)
    expect(loadMoreButton()).toBeUndefined()
  })

  it('shows "Load more" when the safety cap is reached', async () => {
    // 9+ pages of 25 commits, none match the filter.
    // After 8 pages (200 commits) the cap stops auto-loading.
    gitMocks.getGitHistory.mockImplementation((_limit: number, skip: number) => {
      if (skip >= 225) return Promise.resolve([])
      const pageIndex = skip / 25
      const firstParent = pageIndex > 0 ? `p${pageIndex - 1}-24` : undefined
      return Promise.resolve(makePage(`p${pageIndex}`, 25, firstParent))
    })
    gitMocks.getCommitFilesBatch.mockImplementation(filesMock({}))

    renderTab()
    await flush()
    await flush()

    const input = container.querySelector('input') as HTMLInputElement
    await act(async () => { setInputValue(input, 'src/a.ts') })

    // Flush until "Load more" appears (cap reached) or max attempts
    for (let i = 0; i < 40; i++) {
      await flush()
      if (loadMoreButton()?.textContent?.includes('Load more')) break
    }

    // "Load more" visible — cap reached, hasMore still true
    const btn = loadMoreButton()
    expect(btn).toBeTruthy()
    expect(btn?.textContent).toContain('Load more')

    // 8 calls total: 1 initial + 7 auto-loads (skip 0, 25, …, 175)
    expect(gitMocks.getGitHistory).toHaveBeenCalledTimes(8)
    // NOT called with skip=200 (cap prevents the 8th auto-load)
    const skip200Calls = gitMocks.getGitHistory.mock.calls.filter(([, s]) => s === 200)
    expect(skip200Calls).toHaveLength(0)
  })
})
