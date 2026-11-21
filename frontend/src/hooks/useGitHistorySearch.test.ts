// @vitest-environment jsdom
// Unit tests for useGitHistorySearch — the debounced pickaxe search behind
// the history page's search bar. Locks the behavior contract: empty pattern
// → no RPC; rapid edits coalesce into one debounced call; the mode toggle
// flips the -S/-G flag; a stale in-flight page cannot land after the pattern
// changed (epoch discard); metadata matches surface from the loaded window;
// the pickaxe ∪ metadata union is deduped by sha; invalid regex blocks the
// RPC; loadMore pages the pickaxe stream and stops when exhausted.

import { describe, it, expect, vi, beforeEach, afterEach } from 'vitest'
import { act, createElement } from 'react'
import { createRoot, type Root } from 'react-dom/client'

// --- Mock the API boundary so tests never touch the Wails runtime ---
const { gitMocks } = vi.hoisted(() => ({
  gitMocks: {
    searchGitHistory: vi.fn(),
    GIT_HISTORY_PAGE_SIZE: 300,
  },
}))

vi.mock('@/api/git', () => ({
  searchGitHistory: gitMocks.searchGitHistory,
  GIT_HISTORY_PAGE_SIZE: gitMocks.GIT_HISTORY_PAGE_SIZE,
}))

import { useGitHistorySearch } from './useGitHistorySearch'
import type { GitHistoryCommit, GitHistoryPage } from '@/types/models'

type HookValue = ReturnType<typeof useGitHistorySearch>

function commit(sha: string, over: Partial<GitHistoryCommit> = {}): GitHistoryCommit {
  return {
    sha,
    parents: [],
    author: 'Alice',
    email: '',
    date: '2026-07-01',
    message: sha,
    refs: [],
    ...over,
  }
}
const WINDOW = [
  commit('w1', { message: 'feat: zz thing' }),
  commit('w2', { author: 'Bob', message: 'bob changes' }),
  commit('w3'),
]

function page(commits: GitHistoryCommit[], nextSkip: number, hasMore: boolean): GitHistoryPage {
  return { commits, next_skip: nextSkip, has_more: hasMore }
}

let latest: HookValue | null = null
let root: Root | null = null
let container: HTMLDivElement | null = null

function Harness({ commits }: { commits: GitHistoryCommit[] }): null {
  latest = useGitHistorySearch(commits)
  return null
}

function hook(): HookValue {
  if (!latest) throw new Error('hook not rendered')
  return latest
}

function renderHookWith(commits: GitHistoryCommit[]): void {
  container = document.createElement('div')
  document.body.appendChild(container)
  const r = createRoot(container)
  root = r
  act(() => {
    r.render(createElement(Harness, { commits }))
  })
}

/** Let in-flight promise chains settle past the hook's state writes. */
async function flush(): Promise<void> {
  await act(async () => {
    for (let i = 0; i < 10; i++) await Promise.resolve()
  })
}

/** Type into the search field. */
function type(text: string): void {
  act(() => {
    hook().setSearchPattern(text)
  })
}

/** Advance past the debounce window so the effective search settles. */
function settle(): void {
  act(() => {
    vi.advanceTimersByTime(320)
  })
}

function deferred<T>(): { promise: Promise<T>; resolve: (v: T) => void } {
  let resolve!: (v: T) => void
  const promise = new Promise<T>((res) => {
    resolve = res
  })
  return { promise, resolve }
}

beforeEach(() => {
  vi.useFakeTimers()
  gitMocks.searchGitHistory.mockReset()
})

afterEach(() => {
  vi.useRealTimers()
  if (root) {
    const r = root
    root = null
    act(() => {
      r.unmount()
    })
  }
  container?.remove()
  container = null
  latest = null
})

describe('useGitHistorySearch', () => {
  it('an empty pattern never fires the search RPC', () => {
    renderHookWith(WINDOW)
    settle()
    settle()
    expect(hook().isSearching).toBe(false)
    expect(hook().searchResults).toEqual([])
    expect(gitMocks.searchGitHistory).not.toHaveBeenCalled()
  })

  it('debounces rapid edits into ONE search call with the final pattern', async () => {
    gitMocks.searchGitHistory.mockResolvedValue(page([], 0, false))
    renderHookWith(WINDOW)

    type('a')
    act(() => {
      vi.advanceTimersByTime(100)
    })
    type('al')
    act(() => {
      vi.advanceTimersByTime(100)
    })
    type('alpha')
    expect(gitMocks.searchGitHistory).not.toHaveBeenCalled()

    settle()
    await flush()
    expect(gitMocks.searchGitHistory).toHaveBeenCalledTimes(1)
    expect(gitMocks.searchGitHistory).toHaveBeenCalledWith('alpha', false, 300, 0)
    expect(hook().isSearching).toBe(true)
  })

  it('the mode toggle re-runs the search with the regex flag', async () => {
    gitMocks.searchGitHistory.mockResolvedValue(page([], 0, false))
    renderHookWith(WINDOW)

    type('al.*a')
    settle()
    await flush()
    expect(gitMocks.searchGitHistory).toHaveBeenCalledWith('al.*a', false, 300, 0)

    act(() => {
      hook().toggleSearchMode()
    })
    settle()
    await flush()
    expect(gitMocks.searchGitHistory).toHaveBeenCalledTimes(2)
    expect(gitMocks.searchGitHistory).toHaveBeenLastCalledWith('al.*a', true, 300, 0)
  })

  it('a stale in-flight page cannot land after the pattern changed', async () => {
    const stale = deferred<GitHistoryPage>()
    gitMocks.searchGitHistory.mockReturnValueOnce(stale.promise) // pattern 'alpha'
    gitMocks.searchGitHistory.mockResolvedValueOnce(
      page([commit('beta-hit')], 0, false), // pattern 'beta'
    )
    renderHookWith(WINDOW)

    type('alpha')
    settle()
    expect(gitMocks.searchGitHistory).toHaveBeenCalledTimes(1)

    type('beta')
    settle()
    await flush()
    // The newer pattern's page landed; the stale request is still pending.
    expect(hook().searchResults.map((c) => c.sha)).toEqual(['beta-hit'])

    // The stale response resolves late — it must be discarded wholesale.
    await act(async () => {
      stale.resolve(page([commit('alpha-hit')], 0, false))
    })
    await flush()
    expect(hook().searchResults.map((c) => c.sha)).toEqual(['beta-hit'])
  })

  it('a metadata-only match surfaces from the loaded window', async () => {
    gitMocks.searchGitHistory.mockResolvedValue(page([], 0, false))
    renderHookWith(WINDOW)

    type('Bob') // author of w2; no pickaxe hits at all
    settle()
    await flush()
    expect(hook().searchResults.map((c) => c.sha)).toEqual(['w2'])
  })

  it('the pickaxe ∪ metadata union is deduped by sha and stays newest-first', async () => {
    // w1 matches the pattern only in metadata, w3 is a pickaxe hit INSIDE
    // the window (no metadata match), deep is a pickaxe hit beyond it.
    gitMocks.searchGitHistory.mockResolvedValue(
      page([commit('deep', { message: 'unrelated' }), commit('w3')], 0, false),
    )
    renderHookWith(WINDOW)

    type('zz') // matches w1's message only
    settle()
    await flush()
    expect(hook().searchResults.map((c) => c.sha)).toEqual(['w1', 'w3', 'deep'])
  })

  it('an invalid regex blocks the RPC and reports the state', () => {
    renderHookWith(WINDOW)

    act(() => {
      hook().toggleSearchMode()
    })
    type('[')
    settle()
    expect(hook().isSearching).toBe(true)
    expect(hook().isInvalidSearch).toBe(true)
    expect(hook().searchResults).toEqual([])
    expect(gitMocks.searchGitHistory).not.toHaveBeenCalled()
  })

  it('a failed first search surfaces the error and drops the results', async () => {
    gitMocks.searchGitHistory.mockRejectedValue(new Error('boom'))
    renderHookWith(WINDOW)

    type('alpha')
    settle()
    await flush()
    expect(hook().error).toBe('boom')
    expect(hook().hasMore).toBe(false)
    expect(hook().isSearchLoading).toBe(false)
  })

  it('loadMore appends pickaxe pages and stops when exhausted', async () => {
    gitMocks.searchGitHistory.mockResolvedValueOnce(
      page([commit('p1')], 1, true), // first page
    )
    renderHookWith(WINDOW)
    type('alpha')
    settle()
    await flush()
    expect(hook().searchResults.map((c) => c.sha)).toEqual(['p1'])

    gitMocks.searchGitHistory.mockResolvedValueOnce(
      page([commit('p2')], 2, false), // second page
    )
    act(() => {
      hook().loadMore()
    })
    await flush()
    expect(gitMocks.searchGitHistory).toHaveBeenLastCalledWith('alpha', false, 300, 1)
    expect(hook().searchResults.map((c) => c.sha)).toEqual(['p1', 'p2'])
    expect(hook().hasMore).toBe(false)

    // Exhausted: loadMore is a no-op.
    act(() => {
      hook().loadMore()
    })
    await flush()
    expect(gitMocks.searchGitHistory).toHaveBeenCalledTimes(2)
  })

  it('clearing the pattern resets the search without another RPC', async () => {
    gitMocks.searchGitHistory.mockResolvedValue(page([commit('p1')], 0, false))
    renderHookWith(WINDOW)
    type('alpha')
    settle()
    await flush()
    expect(hook().searchResults).toHaveLength(1)

    type('')
    settle()
    await flush()
    expect(hook().isSearching).toBe(false)
    expect(hook().searchResults).toEqual([])
    expect(gitMocks.searchGitHistory).toHaveBeenCalledTimes(1)
  })

  it('a pattern change reclaims the loading flags of an abandoned loadMore', async () => {
    // The loadMore of the OLD pattern never settles: its finally block can
    // never run for the new epoch, so the new first page must clear both
    // loading flags itself (otherwise "Searching…" sticks forever and the
    // Load-more button stays disabled).
    const abandoned = deferred<GitHistoryPage>()
    gitMocks.searchGitHistory
      .mockResolvedValueOnce(page([commit('p1')], 1, true))
      .mockReturnValueOnce(abandoned.promise)
      .mockResolvedValueOnce(page([commit('b1')], 0, false))
    renderHookWith(WINDOW)

    type('alpha')
    settle()
    await flush()
    expect(hook().hasMore).toBe(true)

    act(() => {
      hook().loadMore()
    })
    expect(hook().isSearchLoadingMore).toBe(true)

    type('beta')
    settle()
    await flush()
    expect(hook().isSearching).toBe(true)
    expect(hook().isSearchLoading).toBe(false)
    expect(hook().isSearchLoadingMore).toBe(false)
    expect(hook().searchResults.map((c) => c.sha)).toEqual(['b1'])

    // The abandoned page surfacing late changes nothing.
    await act(async () => {
      abandoned.resolve(page([commit('p2')], 2, false))
    })
    await flush()
    expect(hook().searchResults.map((c) => c.sha)).toEqual(['b1'])
  })

  it('clearing the pattern while the first page is in flight clears its loading state', async () => {
    const firstPage = deferred<GitHistoryPage>()
    gitMocks.searchGitHistory.mockReturnValueOnce(firstPage.promise)
    renderHookWith(WINDOW)

    type('alpha')
    settle()
    expect(hook().isSearchLoading).toBe(true)

    type('')
    settle()
    await flush()
    expect(hook().isSearching).toBe(false)
    expect(hook().isSearchLoading).toBe(false)
    expect(hook().isSearchLoadingMore).toBe(false)

    await act(async () => {
      firstPage.resolve(page([commit('a-hit')], 0, false))
    })
    await flush()
    expect(hook().searchResults).toEqual([])
  })

  it('a failed loadMore keeps the loaded pages and is cleared by the retry', async () => {
    gitMocks.searchGitHistory
      .mockResolvedValueOnce(page([commit('p1')], 1, true))
      .mockRejectedValueOnce(new Error('page boom'))
      .mockResolvedValueOnce(page([commit('p2')], 2, false))
    renderHookWith(WINDOW)

    type('alpha')
    settle()
    await flush()

    act(() => {
      hook().loadMore()
    })
    await flush()
    // The failure is reported, the page already loaded survives.
    expect(hook().error).toBe('page boom')
    expect(hook().searchResults.map((c) => c.sha)).toEqual(['p1'])
    expect(hook().hasMore).toBe(true)

    // The retry starts from a clean slate and appends the next page.
    act(() => {
      hook().loadMore()
    })
    await flush()
    expect(hook().error).toBe(null)
    expect(hook().searchResults.map((c) => c.sha)).toEqual(['p1', 'p2'])
  })

  it('refresh re-runs the current pattern from the first page', async () => {
    gitMocks.searchGitHistory.mockResolvedValue(page([commit('p1')], 1, true))
    renderHookWith(WINDOW)

    type('alpha')
    settle()
    await flush()
    act(() => {
      hook().loadMore()
    })
    await flush()
    expect(gitMocks.searchGitHistory).toHaveBeenCalledTimes(2)

    // A history mutation invalidates the pages: restart at skip 0.
    act(() => {
      hook().refresh()
    })
    await flush()
    expect(gitMocks.searchGitHistory).toHaveBeenCalledTimes(3)
    expect(gitMocks.searchGitHistory).toHaveBeenLastCalledWith('alpha', false, 300, 0)
  })
})
