// @vitest-environment jsdom
// Unit tests for useCommitFiles — the shared per-SHA changed-files cache
// (single + batch fetch, dedupe, error-to-empty caching, reference stability).

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

import { useCommitFiles } from './useCommitFiles'
import type { CommitFile } from '@/types/models'

type HookValue = ReturnType<typeof useCommitFiles>

let latest: HookValue | null = null
let root: Root | null = null
let container: HTMLDivElement | null = null

function Harness(): null {
  latest = useCommitFiles()
  return null
}

function hook(): HookValue {
  if (!latest) throw new Error('hook not rendered')
  return latest
}

function renderHook(): void {
  container = document.createElement('div')
  document.body.appendChild(container)
  const r = createRoot(container)
  root = r
  act(() => {
    r.render(createElement(Harness))
  })
}

/** Re-render the SAME root (a parent-driven re-render with no state change). */
function rerender(): void {
  act(() => {
    root?.render(createElement(Harness))
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

describe('useCommitFiles — fetchFiles', () => {
  it('fetches one commit, caches it and tracks the pending set', async () => {
    const d = deferred<CommitFile[]>()
    gitMocks.getCommitFiles.mockReturnValue(d.promise)
    renderHook()

    expect(gitMocks.getCommitFiles).not.toHaveBeenCalled()
    let p: Promise<void> | undefined
    act(() => {
      p = hook().fetchFiles('aaa')
    })
    expect(gitMocks.getCommitFiles).toHaveBeenCalledWith('aaa')
    expect(hook().pendingShas.has('aaa')).toBe(true)
    expect(hook().filesBySha['aaa']).toBeUndefined()

    await act(async () => {
      d.resolve([{ path: 'src/a.ts', status: 'A' }])
      await p
    })
    expect(hook().filesBySha['aaa']).toEqual([{ path: 'src/a.ts', status: 'A' }])
    expect(hook().pendingShas.size).toBe(0)
  })

  it('dedupes concurrent fetches of the same sha into one RPC', async () => {
    const d = deferred<CommitFile[]>()
    gitMocks.getCommitFiles.mockReturnValue(d.promise)
    renderHook()

    act(() => {
      void hook().fetchFiles('aaa')
      void hook().fetchFiles('aaa')
    })
    expect(gitMocks.getCommitFiles).toHaveBeenCalledTimes(1)

    await act(async () => {
      d.resolve([])
    })
    await flush()
    expect(hook().pendingShas.size).toBe(0)
  })

  it('is a no-op when the sha is already cached', async () => {
    gitMocks.getCommitFiles.mockResolvedValue([{ path: 'a.ts', status: 'M' }])
    renderHook()

    await act(async () => {
      await hook().fetchFiles('aaa')
    })
    expect(gitMocks.getCommitFiles).toHaveBeenCalledTimes(1)

    await act(async () => {
      await hook().fetchFiles('aaa')
    })
    expect(gitMocks.getCommitFiles).toHaveBeenCalledTimes(1)
  })

  it('caches [] on rejection — the promise resolves, nothing surfaces', async () => {
    gitMocks.getCommitFiles.mockRejectedValue(new Error('boom'))
    renderHook()

    let p: Promise<void> | undefined
    act(() => {
      p = hook().fetchFiles('aaa')
    })
    await act(async () => {
      await p
    })
    expect(hook().filesBySha['aaa']).toEqual([])
    expect(hook().pendingShas.size).toBe(0)
  })
})

describe('useCommitFiles — fetchMany', () => {
  it('batch-fetches uncached shas in one RPC and defaults missing entries to []', async () => {
    gitMocks.getCommitFilesBatch.mockResolvedValue({ aaa: [{ path: 'a.ts', status: 'A' }] })
    renderHook()

    let p: Promise<void> | undefined
    act(() => {
      p = hook().fetchMany(['aaa', 'bbb'])
    })
    expect(gitMocks.getCommitFilesBatch).toHaveBeenCalledTimes(1)
    expect(gitMocks.getCommitFilesBatch).toHaveBeenCalledWith(['aaa', 'bbb'])
    expect([...hook().pendingShas].sort()).toEqual(['aaa', 'bbb'])

    await act(async () => {
      await p
    })
    expect(hook().filesBySha['aaa']).toEqual([{ path: 'a.ts', status: 'A' }])
    expect(hook().filesBySha['bbb']).toEqual([])
    expect(hook().pendingShas.size).toBe(0)
  })

  it('dedupes cached and in-flight shas — only uncached ones go over the wire', async () => {
    const single = deferred<CommitFile[]>()
    gitMocks.getCommitFiles.mockReturnValue(single.promise)
    gitMocks.getCommitFilesBatch.mockResolvedValue({ ccc: [{ path: 'c.ts', status: 'M' }] })
    renderHook()

    // aaa already cached (resolved immediately)…
    gitMocks.getCommitFiles.mockResolvedValueOnce([{ path: 'a.ts', status: 'A' }])
    await act(async () => {
      await hook().fetchFiles('aaa')
    })
    // …bbb in flight (the deferred mockReturnValue above)…
    act(() => {
      void hook().fetchFiles('bbb')
    })
    // …so a fetchMany over all three must request only ccc.
    let p: Promise<void> | undefined
    act(() => {
      p = hook().fetchMany(['aaa', 'bbb', 'ccc'])
    })
    expect(gitMocks.getCommitFilesBatch).toHaveBeenCalledTimes(1)
    expect(gitMocks.getCommitFilesBatch).toHaveBeenCalledWith(['ccc'])

    await act(async () => {
      await p
    })
    await act(async () => {
      single.resolve([{ path: 'b.ts', status: 'D' }])
    })
    await flush()
    expect(hook().filesBySha['aaa']).toEqual([{ path: 'a.ts', status: 'A' }])
    expect(hook().filesBySha['bbb']).toEqual([{ path: 'b.ts', status: 'D' }])
    expect(hook().filesBySha['ccc']).toEqual([{ path: 'c.ts', status: 'M' }])
    expect(hook().pendingShas.size).toBe(0)
  })

  it('caches [] for every requested sha on rejection', async () => {
    gitMocks.getCommitFilesBatch.mockRejectedValue(new Error('boom'))
    renderHook()

    let p: Promise<void> | undefined
    act(() => {
      p = hook().fetchMany(['aaa', 'bbb'])
    })
    await act(async () => {
      await p
    })
    expect(hook().filesBySha['aaa']).toEqual([])
    expect(hook().filesBySha['bbb']).toEqual([])
    expect(hook().pendingShas.size).toBe(0)
  })
})

describe('useCommitFiles — reference stability (React #185 safe)', () => {
  it('keeps stable callback references and does not mint new cache/pending objects on unrelated re-renders', async () => {
    gitMocks.getCommitFiles.mockResolvedValue([{ path: 'a.ts', status: 'A' }])
    renderHook()
    const first = hook()

    await act(async () => {
      await hook().fetchFiles('aaa')
    })
    // The cache write replaced filesBySha, but the stable callbacks survive:
    expect(hook().fetchFiles).toBe(first.fetchFiles)
    expect(hook().fetchMany).toBe(first.fetchMany)

    // A parent-driven re-render without any state change must hand back the
    // SAME cache and pending references (useSyncExternalStore snapshot rule).
    const filesBefore = hook().filesBySha
    const pendingBefore = hook().pendingShas
    rerender()
    expect(hook().filesBySha).toBe(filesBefore)
    expect(hook().pendingShas).toBe(pendingBefore)
  })
})
