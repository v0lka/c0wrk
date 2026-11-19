// @vitest-environment jsdom
// Unit tests for useGitHistoryFilter — the glob/regex commit filter over the
// shared changed-files cache (useCommitFiles). Locks the behavior contract:
// no filter → passthrough with NO fetches; filter → one batch RPC for the
// uncached window, unmatched/unresolved commits excluded; invalid regex →
// no matches and no fetches; reference-stable passthrough (React #185 safe).

import { describe, it, expect, vi, beforeEach, afterEach } from 'vitest'
import { act, createElement } from 'react'
import { createRoot, type Root } from 'react-dom/client'

// --- Mock the API boundary so tests never touch the Wails runtime ---
const { gitMocks } = vi.hoisted(() => ({
  gitMocks: {
    getCommitFiles: vi.fn(),
    getCommitFilesBatch: vi.fn(),
  },
}))

vi.mock('@/api/git', () => ({
  getCommitFiles: gitMocks.getCommitFiles,
  getCommitFilesBatch: gitMocks.getCommitFilesBatch,
}))

import { useGitHistoryFilter } from './useGitHistoryFilter'
import type { CommitFile, GitHistoryCommit } from '@/types/models'

type HookValue = ReturnType<typeof useGitHistoryFilter>

/** sha → changed files; the batch RPC answers this map. */
const C1_FILES: CommitFile[] = [
  { path: 'src/a.ts', status: 'A' },
  { path: 'src/b.ts', status: 'M' },
]
const FILES: Record<string, CommitFile[]> = {
  c1: C1_FILES,
  c2: [{ path: 'README.md', status: 'M' }],
  c3: [{ path: 'docs/guide.md', status: 'D' }],
}

function commit(sha: string): GitHistoryCommit {
  return { sha, parents: [], author: '', email: '', date: '', message: sha, refs: [] }
}
const COMMITS = [commit('c1'), commit('c2'), commit('c3')]

let latest: HookValue | null = null
let root: Root | null = null
let container: HTMLDivElement | null = null

function Harness({ commits }: { commits: GitHistoryCommit[] }): null {
  latest = useGitHistoryFilter(commits)
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

function rerenderWith(commits: GitHistoryCommit[]): void {
  act(() => {
    root?.render(createElement(Harness, { commits }))
  })
}

/** Let in-flight promise chains settle past the hook's state writes. */
async function flush(): Promise<void> {
  await act(async () => {
    for (let i = 0; i < 10; i++) await Promise.resolve()
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
  gitMocks.getCommitFiles.mockReset()
  gitMocks.getCommitFilesBatch.mockReset()
})

afterEach(() => {
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

describe('useGitHistoryFilter', () => {
  it('without a filter passes the commits through untouched and fetches nothing', () => {
    renderHookWith(COMMITS)
    expect(hook().isFiltering).toBe(false)
    expect(hook().filteredCommits).toBe(COMMITS)
    expect(gitMocks.getCommitFiles).not.toHaveBeenCalled()
    expect(gitMocks.getCommitFilesBatch).not.toHaveBeenCalled()
  })

  it('the passthrough keeps reference stability across parent re-renders (React #185 safe)', () => {
    renderHookWith(COMMITS)
    const first = hook()
    rerenderWith(COMMITS)
    expect(hook().filteredCommits).toBe(COMMITS)
    expect(hook().filteredCommits).toBe(first.filteredCommits)
    expect(hook().filesBySha).toBe(first.filesBySha)
    expect(hook().pendingShas).toBe(first.pendingShas)
    expect(hook().fetchFiles).toBe(first.fetchFiles)
    expect(hook().setFilterText).toBe(first.setFilterText)
  })

  it('a glob filter batch-fetches the loaded window once and keeps only matching commits', async () => {
    gitMocks.getCommitFilesBatch.mockResolvedValue(FILES)
    renderHookWith(COMMITS)

    act(() => {
      hook().setFilterText('a.ts') // basename match → only c1's src/a.ts
    })
    expect(hook().isFiltering).toBe(true)
    expect(gitMocks.getCommitFilesBatch).toHaveBeenCalledTimes(1)
    expect(gitMocks.getCommitFilesBatch).toHaveBeenCalledWith(['c1', 'c2', 'c3'])

    await flush()
    expect(hook().filteredCommits.map((c) => c.sha)).toEqual(['c1'])
    expect(hook().isResolvingFiles).toBe(false)
  })

  it('excludes unresolved commits while files are in flight and flips isResolvingFiles', async () => {
    const d = deferred<Record<string, CommitFile[]>>()
    gitMocks.getCommitFilesBatch.mockReturnValue(d.promise)
    renderHookWith(COMMITS)

    act(() => {
      hook().setFilterText('a.ts')
    })
    expect(hook().isResolvingFiles).toBe(true)
    expect(hook().filteredCommits).toEqual([])

    await act(async () => {
      d.resolve(FILES)
    })
    await flush()
    expect(hook().isResolvingFiles).toBe(false)
    expect(hook().filteredCommits.map((c) => c.sha)).toEqual(['c1'])
  })

  it('a non-matching filter keeps zero commits once every file list is resolved', async () => {
    gitMocks.getCommitFilesBatch.mockResolvedValue(FILES)
    renderHookWith(COMMITS)

    act(() => {
      hook().setFilterText('nomatch.xyz')
    })
    await flush()
    expect(hook().filteredCommits).toEqual([])
    expect(hook().isResolvingFiles).toBe(false)
  })

  it('an invalid regex flags isInvalidFilter, matches nothing and fetches nothing', () => {
    renderHookWith(COMMITS)

    act(() => {
      hook().toggleFilterMode() // glob → regex first
    })
    act(() => {
      hook().setFilterText('[') // invalid as a regex
    })
    expect(hook().isFiltering).toBe(true)
    expect(hook().isInvalidFilter).toBe(true)
    expect(hook().filteredCommits).toEqual([])
    expect(gitMocks.getCommitFilesBatch).not.toHaveBeenCalled()
  })

  it('exposed fetchFiles shares the cache — already-cached commits skip the batch RPC', async () => {
    gitMocks.getCommitFiles.mockResolvedValue(C1_FILES)
    gitMocks.getCommitFilesBatch.mockResolvedValue({
      c2: [{ path: 'README.md', status: 'M' }],
      c3: [{ path: 'docs/guide.md', status: 'D' }],
    })
    renderHookWith(COMMITS)

    await act(async () => {
      await hook().fetchFiles('c1')
    })
    expect(hook().filesBySha['c1']).toEqual(C1_FILES)

    act(() => {
      hook().setFilterText('a.ts')
    })
    await flush()
    // c1 was already cached, so the batch effect only had to fetch the rest.
    expect(gitMocks.getCommitFilesBatch).toHaveBeenCalledWith(['c2', 'c3'])
    expect(hook().filteredCommits.map((c) => c.sha)).toEqual(['c1'])
  })

  it('growing the loaded window fetches only the new commits', async () => {
    gitMocks.getCommitFilesBatch.mockResolvedValue(FILES)
    renderHookWith(COMMITS)
    act(() => {
      hook().setFilterText('a.ts')
    })
    await flush()
    expect(gitMocks.getCommitFilesBatch).toHaveBeenCalledTimes(1)

    const page2 = [commit('c4')]
    gitMocks.getCommitFilesBatch.mockResolvedValue({ c4: [{ path: 'src/a.ts', status: 'M' }] })
    rerenderWith([...COMMITS, ...page2])
    await flush()
    expect(gitMocks.getCommitFilesBatch).toHaveBeenCalledTimes(2)
    expect(gitMocks.getCommitFilesBatch).toHaveBeenLastCalledWith(['c4'])
    expect(hook().filteredCommits.map((c) => c.sha)).toEqual(['c1', 'c4'])
  })
})
