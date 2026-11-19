// @vitest-environment jsdom
// useFileViewerData — image files are fetched as data URLs, never read as text
// and never diffed; text loads assemble the overlay diff from the hunks RPC
// alone; tree_changed bursts coalesce into one path-scoped refresh cycle with
// an in-flight dedup and a git-epoch-guarded content short-circuit.

import { describe, it, expect, vi, beforeEach, afterEach } from 'vitest'
import { act } from 'react'
import { createRoot, type Root } from 'react-dom/client'

const { readFileMock, readImageAsDataURLMock, getFileDiffMock, getFileDiffHunksMock } = vi.hoisted(() => ({
  readFileMock: vi.fn(),
  readImageAsDataURLMock: vi.fn(),
  getFileDiffMock: vi.fn(),
  getFileDiffHunksMock: vi.fn().mockResolvedValue([]),
}))

/** Registered onGlobalEvent handlers, keyed by event name (mock seams). */
const eventHandlers = new Map<string, Set<(data: unknown) => void>>()

vi.mock('@/api/workspace', () => ({
  readFile: readFileMock,
  readImageAsDataURL: readImageAsDataURLMock,
  getFileDiff: getFileDiffMock,
}))
vi.mock('@/api/git', () => ({ getFileDiffHunks: getFileDiffHunksMock }))
vi.mock('@/api/runtime', () => ({
  onGlobalEvent: vi.fn((name: string, cb: (data: unknown) => void) => {
    let set = eventHandlers.get(name)
    if (!set) {
      set = new Set()
      eventHandlers.set(name, set)
    }
    set.add(cb)
    return () => {
      set?.delete(cb)
    }
  }),
}))

import { useFileViewerData, TREE_CHANGED_DEBOUNCE_MS } from './useFileViewerData'
import { useFileViewerStore, type FileData } from '@/stores/fileViewerStore'
import { markViewerDiffsStale } from '@/lib/viewerDiffEpoch'

function Probe({ activeFile, openTabs }: { activeFile: string | null; openTabs: string[] }) {
  useFileViewerData(activeFile, openTabs)
  return null
}

let root: Root | null = null
let container: HTMLDivElement | null = null

/** Fire one backend event at every handler registered for it. */
function fire(name: string, payload?: unknown): void {
  for (const cb of eventHandlers.get(name) ?? []) cb(payload)
}

/** Advance past the trailing-debounce window inside act. */
async function flushDebounce(extraMs = 0): Promise<void> {
  await act(async () => {
    await vi.advanceTimersByTimeAsync(TREE_CHANGED_DEBOUNCE_MS + extraMs)
  })
}

async function mountWith(
  activeFile: string | null,
  openTabs: string[],
  files: Record<string, FileData> = {},
): Promise<void> {
  useFileViewerStore.setState({ openTabs, activeFile, files })
  container = document.createElement('div')
  document.body.appendChild(container)
  root = createRoot(container)
  await act(async () => {
    root!.render(<Probe activeFile={activeFile} openTabs={openTabs} />)
  })
}

async function mount(path: string): Promise<void> {
  await mountWith(path, [path])
}

beforeEach(() => {
  vi.useFakeTimers()
  eventHandlers.clear()
  readFileMock.mockReset()
  readImageAsDataURLMock.mockReset()
  getFileDiffMock.mockReset()
  // Call-count assertions on the hunks RPC require a per-test slate; restore
  // the default resolved value mockReset clears.
  getFileDiffHunksMock.mockReset()
  getFileDiffHunksMock.mockResolvedValue([])
})

afterEach(() => {
  act(() => {
    root?.unmount()
  })
  vi.useRealTimers()
  container?.remove()
  root = null
  container = null
})

describe('useFileViewerData — image files', () => {
  it('fetches an image as a data URL and skips the text read + diff', async () => {
    const path = '/ws/assets/plot.png'
    readImageAsDataURLMock.mockResolvedValue('data:image/png;base64,AAA')

    await mount(path)

    expect(readImageAsDataURLMock).toHaveBeenCalledWith(path)
    expect(readFileMock).not.toHaveBeenCalled()
    expect(getFileDiffHunksMock).not.toHaveBeenCalled()

    const file = useFileViewerStore.getState().files[path]
    expect(file?.imageDataUrl).toBe('data:image/png;base64,AAA')
    expect(file?.loading).toBe(false)
  })

  it('records an error when the image read fails', async () => {
    const path = '/ws/assets/broken.png'
    readImageAsDataURLMock.mockRejectedValue(new Error('file too large'))

    await mount(path)

    const file = useFileViewerStore.getState().files[path]
    expect(file?.error).toBe('file too large')
    expect(file?.imageDataUrl).toBeUndefined()
  })

  it('reads non-image files as text (no data URL)', async () => {
    const path = '/ws/src/main.ts'
    readFileMock.mockResolvedValue('const x = 1\n')

    await mount(path)

    expect(readFileMock).toHaveBeenCalledWith(path)
    expect(readImageAsDataURLMock).not.toHaveBeenCalled()

    const file = useFileViewerStore.getState().files[path]
    expect(file?.content).toBe('const x = 1\n')
    expect(file?.imageDataUrl).toBeUndefined()
  })
})

describe('useFileViewerData — virtual pseudo-path tabs', () => {
  it('never reads a virtual pseudo-path — on activation nor on tree_changed refresh', async () => {
    // Any c0wrk:-prefixed virtual tab (the research workspace opens one —
    // see researchStore): virtual → skipped by the loader.
    const path = 'c0wrk:research'
    await mountWith(path, [path], {
      [path]: { content: '', loading: false, virtual: true },
    })

    expect(readFileMock).not.toHaveBeenCalled()
    expect(readImageAsDataURLMock).not.toHaveBeenCalled()
    expect(getFileDiffHunksMock).not.toHaveBeenCalled()

    // A pathless workspace:tree_changed (refresh-everything scope) must not
    // read it either — the refresh loop skips virtual tabs.
    act(() => {
      fire('workspace:tree_changed')
    })
    await flushDebounce()

    expect(readFileMock).not.toHaveBeenCalled()
  })
})

describe('useFileViewerData — diff pipeline', () => {
  it('assembles the overlay diff from the hunks RPC and never calls getFileDiff', async () => {
    const path = '/ws/src/main.ts'
    readFileMock.mockResolvedValue('const x = 1\n')
    getFileDiffHunksMock.mockResolvedValue([
      {
        oldStart: 1, oldCount: 1, newStart: 1, newCount: 2,
        oldChangeStart: 1, newChangeStart: 1, staged: true,
        diff: '@@ -1,1 +1,2 @@\n-old\n+new1',
      },
      {
        oldStart: 5, oldCount: 1, newStart: 6, newCount: 1,
        oldChangeStart: 5, newChangeStart: 6, staged: false,
        diff: '@@ -5,1 +6,1 @@\n ctx\n+added',
      },
    ])

    await mount(path)

    // The unified diff is assembled from the hunk blocks — the legacy
    // getFileDiff RPC (a second pair of git spawns) must stay unused.
    expect(getFileDiffMock).not.toHaveBeenCalled()
    const file = useFileViewerStore.getState().files[path]
    expect(file?.hunks).toHaveLength(2)
    expect(file?.diff).toBe('@@ -1,1 +1,2 @@\n-old\n+new1\n@@ -5,1 +6,1 @@\n ctx\n+added')
  })

  it('skips the diff RPCs when a reload re-reads identical content — until a git mutation moves the epoch', async () => {
    const path = '/ws/src/main.ts'
    readFileMock.mockResolvedValue('same\n')
    getFileDiffHunksMock.mockResolvedValue([
      { oldStart: 1, oldCount: 1, newStart: 1, newCount: 1, oldChangeStart: 1, newChangeStart: 1, staged: false, diff: '@@ -1,1 +1,1 @@\n+x' },
    ])

    await mount(path)
    expect(getFileDiffHunksMock).toHaveBeenCalledTimes(1)

    // Byte-identical silent reload: content re-read, diff NOT re-fetched.
    act(() => {
      fire('workspace:tree_changed')
    })
    await flushDebounce()
    expect(readFileMock).toHaveBeenCalledTimes(2)
    expect(getFileDiffHunksMock).toHaveBeenCalledTimes(1)

    // A staged change moves hunks without touching the bytes: the
    // git:status_changed epoch bump must invalidate the short-circuit.
    markViewerDiffsStale()
    act(() => {
      fire('workspace:tree_changed')
    })
    await flushDebounce()
    expect(getFileDiffHunksMock).toHaveBeenCalledTimes(2)
  })

  it('retries the diff fetch after a hunks failure (the epoch stays unmoved)', async () => {
    const path = '/ws/src/main.ts'
    readFileMock.mockResolvedValue('same\n')
    getFileDiffHunksMock.mockRejectedValueOnce(new Error('git failed'))
    getFileDiffHunksMock.mockResolvedValueOnce([])

    await mount(path)
    const file = useFileViewerStore.getState().files[path]
    expect(file?.hunks).toEqual([])

    act(() => {
      fire('workspace:tree_changed')
    })
    await flushDebounce()
    expect(readFileMock).toHaveBeenCalledTimes(2)
    expect(getFileDiffHunksMock).toHaveBeenCalledTimes(2)
  })
})

describe('useFileViewerData — tree_changed refresh cycle', () => {
  it('refreshes only the tabs intersecting the payload paths', async () => {
    const a = '/ws/src/a.ts'
    const b = '/ws/src/b.ts'
    await mountWith(a, [a, b], {
      [a]: { content: 'a-content', loading: false },
      [b]: { content: 'b-content', loading: false },
    })
    expect(readFileMock).not.toHaveBeenCalled()
    readFileMock.mockImplementation(async (p: string) => (p === a ? 'a-new' : 'b-new'))

    act(() => {
      fire('workspace:tree_changed', { paths: [b] })
    })
    await flushDebounce()

    expect(readFileMock).toHaveBeenCalledWith(b)
    expect(readFileMock).not.toHaveBeenCalledWith(a)
  })

  it('refreshes every tab when the payload carries no paths (agent-write emitter)', async () => {
    const a = '/ws/src/a.ts'
    const b = '/ws/src/b.ts'
    await mountWith(a, [a, b], {
      [a]: { content: 'a-content', loading: false },
      [b]: { content: 'b-content', loading: false },
    })
    readFileMock.mockImplementation(async (p: string) => (p === a ? 'a-new' : 'b-new'))

    act(() => {
      fire('workspace:tree_changed', { research_scoped: false })
    })
    await flushDebounce()

    expect(readFileMock).toHaveBeenCalledWith(a)
    expect(readFileMock).toHaveBeenCalledWith(b)
  })

  it('coalesces a burst of tree_changed events into one refresh cycle', async () => {
    const path = '/ws/src/main.ts'
    readFileMock.mockResolvedValue('x\n')

    await mount(path)
    readFileMock.mockClear()

    act(() => {
      fire('workspace:tree_changed', { paths: [path] })
      fire('workspace:tree_changed', { paths: [path] })
      fire('workspace:tree_changed', { paths: [path] })
    })
    // Not yet — the trailing debounce holds the cycle.
    await act(async () => {
      await vi.advanceTimersByTimeAsync(50)
    })
    expect(readFileMock).not.toHaveBeenCalled()

    await flushDebounce()
    expect(readFileMock).toHaveBeenCalledTimes(1)
  })

  it('defers a refresh racing an in-flight load to one fresh follow-up read', async () => {
    const path = '/ws/src/main.ts'
    let release: ((v: string) => void) | null = null
    readFileMock.mockImplementation(
      () =>
        new Promise<string>((resolve) => {
          release = resolve
        }),
    )

    await mount(path)
    expect(readFileMock).toHaveBeenCalledTimes(1)

    // A refresh cycle firing while the initial load is still in flight must
    // NOT join it — the chain may predate the change that scheduled the
    // refresh — and must not stack a second parallel read either: the cycle
    // leaves readFile untouched until the in-flight chain settles.
    act(() => {
      fire('workspace:tree_changed')
    })
    await flushDebounce()
    expect(readFileMock).toHaveBeenCalledTimes(1)

    // The settled chain applies its (possibly stale) bytes, then the queued
    // follow-up starts — exactly one fresh read, never a parallel stack.
    await act(async () => {
      release?.('stale-content\n')
      await vi.advanceTimersByTimeAsync(0)
    })
    expect(readFileMock).toHaveBeenCalledTimes(2)

    // The follow-up is a fresh read that postdates the change: resolving it
    // lands the fresh bytes (not the settled chain's stale ones).
    await act(async () => {
      release?.('fresh-content\n')
      await vi.advanceTimersByTimeAsync(0)
    })
    const file = useFileViewerStore.getState().files[path]
    expect(file?.content).toBe('fresh-content\n')
    expect(file?.loading).toBe(false)
  })
})
