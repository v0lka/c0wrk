// @vitest-environment jsdom
import { describe, it, expect, vi, beforeEach, afterEach } from 'vitest'
import { act } from 'react'
import { createRoot, type Root } from 'react-dom/client'
import type { ReactNode } from 'react'
import { TooltipProvider } from '@/components/ui/tooltip'

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
import { useUiScaleStore } from '@/stores/uiScaleStore'

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

  it('keeps state identity on a silent re-fetch with identical content (no-op events)', async () => {
    const diff = [
      {
        path: 'a.txt',
        old_path: '',
        hunks: [
          { old_start: 1, new_start: 1, raw: '@@ -1,2 +1,3 @@\n ctx\n-old\n+new\n+added' },
        ],
      },
    ]
    reviewMocks.getReviewDiff.mockResolvedValue(diff)
    // The ReviewHeader renders Radix Tooltips (hunk combobox path labels),
    // which require a provider — the app supplies one at the root.
    render(
      <TooltipProvider>
        <ReviewPage sessionId="s1" />
      </TooltipProvider>,
    )
    await flush()

    // Grab the rendered hunk DOM node and its initial content — the node the
    // user would be selecting text in.
    const hunkEl = document.querySelector('[data-review-hunk]')
    expect(hunkEl).toBeTruthy()
    const contentBefore = hunkEl!.textContent

    // A structurally-identical (but fresh) response object arrives from a
    // background event: setDiff must be skipped entirely, so the DOM node is
    // never re-rendered and an in-progress selection survives.
    const freshEqual = structuredClone(diff)
    reviewMocks.getReviewDiff.mockResolvedValue(freshEqual)
    await act(async () => {
      handlers['workspace:tree_changed']?.()
      vi.advanceTimersByTime(200)
    })
    expect(reviewMocks.getReviewDiff).toHaveBeenCalledTimes(2)
    expect(document.querySelector('[data-review-hunk]')).toBe(hunkEl)
    expect(document.querySelector('[data-review-hunk]')!.textContent).toBe(contentBefore)
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

// ── Hunk navigation: tracker lifecycle + zoom-safe scroll ────────────────
describe('ReviewPage — hunk navigation', () => {
  // Deterministic rAF frames (the scroll tracker coalesces its work into
  // requestAnimationFrame); tests drive frames manually.
  const rafQueue: Array<FrameRequestCallback | undefined> = []
  function flushRaf() {
    const frame = rafQueue.splice(0)
    for (const cb of frame) cb?.(0)
  }

  const twoHunks = () => [
    {
      path: 'a.txt',
      old_path: '',
      hunks: [
        { old_start: 1, new_start: 1, raw: '@@ -1,1 +1,1 @@\n-a\n+b' },
        { old_start: 5, new_start: 5, raw: '@@ -5,1 +5,1 @@\n-c\n+d' },
      ],
    },
  ]

  function scrollContainer(): HTMLElement {
    const el = document.querySelector('.custom-scrollbar')
    if (!el) throw new Error('scroll container not found')
    return el as HTMLElement
  }

  /** jsdom has no layout — pin the rect `top` the component will measure. */
  function stubRect(el: Element, top: number) {
    vi.spyOn(el, 'getBoundingClientRect').mockReturnValue({
      top,
      bottom: top + 10,
      left: 0,
      right: 10,
      width: 10,
      height: 10,
      x: 0,
      y: top,
      toJSON: () => ({}),
    } as DOMRect)
  }

  beforeEach(() => {
    rafQueue.length = 0
    vi.stubGlobal('requestAnimationFrame', (cb: FrameRequestCallback) => {
      rafQueue.push(cb)
      return rafQueue.length
    })
    vi.stubGlobal('cancelAnimationFrame', (id?: number) => {
      if (typeof id === 'number' && id >= 1 && id <= rafQueue.length) delete rafQueue[id - 1]
    })
  })

  afterEach(() => {
    rafQueue.length = 0
    vi.unstubAllGlobals()
    useUiScaleStore.setState({ scale: 100 })
  })

  it('rebinds the scroll tracker when the container remounts on a commit switch with an unchanged hunk count', async () => {
    reviewMocks.getCommitDiff.mockResolvedValueOnce(twoHunks()).mockResolvedValueOnce(twoHunks())
    render(
      <TooltipProvider>
        <ReviewPage commitSha="c1" />
      </TooltipProvider>,
    )
    await flush()
    const firstContainer = scrollContainer()
    expect(firstContainer.querySelectorAll('[data-review-hunk]')).toHaveLength(2)

    // Commit → commit, same hunk count: the non-silent re-fetch flips
    // `loading`, the spinner unmounts the scroll container, and the diff
    // renders into a brand-new node. The tracker must re-bind to it.
    render(
      <TooltipProvider>
        <ReviewPage commitSha="c2" />
      </TooltipProvider>,
    )
    await flush()
    const secondContainer = scrollContainer()
    expect(secondContainer).not.toBe(firstContainer)

    // Scrolled state: hunk 1 is 400 px above the viewport top, hunk 2 is
    // pinned at the top (≤ 8 px threshold) → the tracker must report it
    // active and the header indicator must move to "2/2".
    stubRect(secondContainer, 0)
    const hunkEls = secondContainer.querySelectorAll('[data-review-hunk]')
    stubRect(hunkEls[0]!, -400)
    stubRect(hunkEls[1]!, 5)

    act(() => {
      secondContainer.dispatchEvent(new Event('scroll'))
    })
    act(() => {
      flushRaf()
    })

    expect(document.body.textContent).toContain('2/2')
    expect(document.body.textContent).not.toContain('1/2')
  })

  it('rebinds the scroll tracker across an error flip that never toggles loading (silent re-fetch fails, then recovers)', async () => {
    reviewMocks.getReviewDiff
      .mockResolvedValueOnce(twoHunks())
      // Silent background re-fetch fails: `error` flips without `loading` or
      // the hunk count changing, unmounting the scroll container.
      .mockRejectedValueOnce(new Error('boom'))
      // Recovery arrives the same silent way — still no `loading` toggle and
      // the same hunk count — so the content remounts into a brand-new
      // container node while neither of the other tracker deps ever changes.
      .mockResolvedValueOnce(twoHunks())
    render(
      <TooltipProvider>
        <ReviewPage sessionId="s1" />
      </TooltipProvider>,
    )
    await flush()
    const firstContainer = scrollContainer()

    // Silent re-fetch fails → error UI (no scroll container), loading untouched.
    await act(async () => {
      handlers['git:status_changed']?.()
      vi.advanceTimersByTime(200)
    })
    expect(reviewMocks.getReviewDiff).toHaveBeenCalledTimes(2)
    expect(document.body.textContent).toContain('Failed to load review diff')
    expect(document.querySelector('.custom-scrollbar')).toBeNull()

    // Silent re-fetch recovers → content is back, in a new container node.
    await act(async () => {
      handlers['git:status_changed']?.()
      vi.advanceTimersByTime(200)
    })
    expect(reviewMocks.getReviewDiff).toHaveBeenCalledTimes(3)
    const secondContainer = scrollContainer()
    expect(secondContainer).not.toBe(firstContainer)

    // The tracker must be listening on the NEW node: hunk 2 is pinned at the
    // top (≤ 8 px threshold) → the header indicator must move to "2/2".
    stubRect(secondContainer, 0)
    const hunkEls = secondContainer.querySelectorAll('[data-review-hunk]')
    stubRect(hunkEls[0]!, -400)
    stubRect(hunkEls[1]!, 5)
    act(() => {
      secondContainer.dispatchEvent(new Event('scroll'))
    })
    act(() => {
      flushRaf()
    })

    expect(document.body.textContent).toContain('2/2')
    expect(document.body.textContent).not.toContain('1/2')
  })

  it('divides the hunk scroll delta by the live UI zoom factor (visual → layout px)', async () => {
    useUiScaleStore.setState({ scale: 200 })
    reviewMocks.getCommitDiff.mockResolvedValueOnce(twoHunks())
    render(
      <TooltipProvider>
        <ReviewPage commitSha="c1" />
      </TooltipProvider>,
    )
    await flush()

    const scrollEl = scrollContainer()
    const scrollBy = vi.fn()
    ;(scrollEl as unknown as { scrollBy: unknown }).scrollBy = scrollBy
    stubRect(scrollEl, 0)
    const hunkEls = scrollEl.querySelectorAll('[data-review-hunk]')
    stubRect(hunkEls[0]!, 0)
    // getBoundingClientRect reports VISUAL px (layout × zoom): at UI scale
    // 200 % hunk 2 measures 300 visual px below the viewport top.
    stubRect(hunkEls[1]!, 300)

    // "Next hunk" → goToHunk(1).
    act(() => {
      ;(document.querySelector('[aria-label="Next hunk"]') as HTMLButtonElement).click()
    })

    // 300 visual px ÷ zoom 2 = 150 layout px handed to scrollBy.
    expect(scrollBy).toHaveBeenCalledTimes(1)
    expect(scrollBy).toHaveBeenCalledWith({ top: 150, behavior: 'smooth' })
  })
})
