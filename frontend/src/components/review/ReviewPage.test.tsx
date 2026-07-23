// @vitest-environment jsdom
import { describe, it, expect, vi, beforeEach, afterEach } from 'vitest'
import { act } from 'react'
import { createRoot, type Root } from 'react-dom/client'
import type { ReactNode } from 'react'

// jsdom in this environment does not expose `window.localStorage`, which
// zustand's `persist` middleware captures at store-creation time (via
// createJSONStorage(() => window.localStorage)). Install an in-memory
// polyfill before any store module is imported so reviewStore works.
vi.hoisted(() => {
  const g = globalThis as Record<string, unknown>
  const win = (g.window as Record<string, unknown> | undefined) ?? g
  g.IS_REACT_ACT_ENVIRONMENT = true
  win.IS_REACT_ACT_ENVIRONMENT = true
  const map = new Map<string, string>()
  win.localStorage = {
    getItem: (k: string) => map.get(k) ?? null,
    setItem: (k: string, v: string) => {
      map.set(k, v)
    },
    removeItem: (k: string) => {
      map.delete(k)
    },
    clear: () => map.clear(),
    key: (i: number) => Array.from(map.keys())[i] ?? null,
    get length() {
      return map.size
    },
  }
})

// vi.mock factories are hoisted, so the mock objects must be created via
// vi.hoisted() to be accessible inside the factory.
const { reviewMocks, runtimeMocks, gitMocks, chatMocks } = vi.hoisted(() => ({
  reviewMocks: {
    getReviewDiff: vi.fn(),
    getCommitDiff: vi.fn(),
    getReview: vi.fn(),
    saveReviewGeneralComment: vi.fn(),
    saveReviewFileComment: vi.fn(),
    saveReviewHunkComment: vi.fn(),
    deleteReviewComment: vi.fn(),
    clearReview: vi.fn(),
    clearReviewComments: vi.fn(),
    setReviewStatus: vi.fn(),
    saveReviewPrompt: vi.fn(),
    resolveReviewPrompt: vi.fn(),
  },
  runtimeMocks: {
    subscribe: vi.fn(),
  },
  gitMocks: {
    stageAll: vi.fn(),
  },
  chatMocks: {
    sendMessage: vi.fn(),
  },
}))

vi.mock('@/api/review', () => reviewMocks)
vi.mock('@/api/runtime', () => runtimeMocks)
vi.mock('@/api/git', () => gitMocks)
vi.mock('@/api/chat', () => chatMocks)
vi.mock('@/lib/logger', () => ({ logger: { error: vi.fn(), warn: vi.fn() } }))

import { ReviewPage } from './ReviewPage'

let container: HTMLDivElement
let root: Root
/** Captured event handlers keyed by event name (latest registration wins). */
const handlers: Record<string, (...args: unknown[]) => void> = {}

beforeEach(() => {
  vi.clearAllMocks()
  for (const k of Object.keys(handlers)) delete handlers[k]

  // Capture the latest subscriber for each event so the test can emit it.
  runtimeMocks.subscribe.mockImplementation(
    (event: string, cb: (...args: unknown[]) => void) => {
      handlers[event] = cb
      return () => {
        delete handlers[event]
      }
    },
  )

  reviewMocks.getReviewDiff.mockResolvedValue([])
  reviewMocks.getCommitDiff.mockResolvedValue([])
  reviewMocks.getReview.mockResolvedValue({
    session_id: 's1',
    status: 'active',
    general_comment: '',
    hunk_comments: [],
    file_comments: [],
    updated_at: '',
  })

  vi.useFakeTimers()

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
  vi.useRealTimers()
})

function render(node: ReactNode) {
  act(() => {
    root.render(node)
  })
}

/** Flush all pending microtasks inside act() so async state updates settle. */
async function flush() {
  await act(async () => {
    await Promise.resolve()
    await Promise.resolve()
    await Promise.resolve()
  })
}

describe('ReviewPage — working-tree sync with the Git panel "Changes" section', () => {
  it('re-fetches the working-tree diff when git:status_changed fires', async () => {
    render(<ReviewPage sessionId="s1" />)
    await flush()
    expect(reviewMocks.getReviewDiff).toHaveBeenCalledTimes(1)

    // Simulate a stage/unstage/discard/commit performed in the Git panel.
    act(() => {
      handlers['git:status_changed']?.()
      vi.advanceTimersByTime(149) // still inside the 150 ms debounce window
    })
    expect(reviewMocks.getReviewDiff).toHaveBeenCalledTimes(1)

    // Past the debounce — the silent background re-fetch fires.
    await act(async () => {
      vi.advanceTimersByTime(1)
    })
    expect(reviewMocks.getReviewDiff).toHaveBeenCalledTimes(2)
  })

  it('coalesces git:status_changed + workspace:tree_changed into one re-fetch', async () => {
    render(<ReviewPage sessionId="s1" />)
    await flush()
    expect(reviewMocks.getReviewDiff).toHaveBeenCalledTimes(1)

    // Both events fire near-simultaneously (UI op + watcher) — only one fetch.
    await act(async () => {
      handlers['git:status_changed']?.()
      handlers['workspace:tree_changed']?.()
      vi.advanceTimersByTime(200)
    })
    expect(reviewMocks.getReviewDiff).toHaveBeenCalledTimes(2)
  })

  it('does NOT re-fetch in commit-review mode (the commit diff is immutable)', async () => {
    render(<ReviewPage commitSha="abc1234" />)
    await flush()

    expect(reviewMocks.getCommitDiff).toHaveBeenCalledTimes(1)
    expect(reviewMocks.getReviewDiff).not.toHaveBeenCalled()
    // Commit-review mode never subscribes to live git status events.
    expect(runtimeMocks.subscribe).not.toHaveBeenCalled()

    await act(async () => {
      // No handler was ever registered in commit mode → no-op.
      handlers['git:status_changed']?.()
      vi.advanceTimersByTime(200)
    })
    expect(reviewMocks.getCommitDiff).toHaveBeenCalledTimes(1)
  })
})
