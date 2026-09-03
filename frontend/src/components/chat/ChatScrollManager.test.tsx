// @vitest-environment jsdom
// Integration tests: the bookmark/step navigation callbacks registered by
// ChatScrollManager must land the target block BELOW the floating sticky
// user-message bar (which covers the scrollport top while its turn is in
// view), not at the geometric top where the bar hides the block's beginning.
import { act } from 'react'
import { createRoot, type Root } from 'react-dom/client'
import { afterEach, beforeEach, describe, expect, it, vi } from 'vitest'
import { ChatScrollManager } from './ChatScrollManager'
import { ScrollProvider, useScrollContext } from './ScrollContext'

let root: Root | null = null

// Context callbacks captured by the probe so tests can trigger navigation the
// same way BookmarksPanel / plan panels do.
let navigateBookmark: ((key: string) => void) | null = null
let navigateStep: ((stepId: string) => void) | null = null

function Probe() {
  const ctx = useScrollContext()
  navigateBookmark = ctx.scrollToBookmark
  navigateStep = ctx.scrollToStep
  return null
}

function rect({ top, height }: { top: number; height: number }): DOMRect {
  return {
    top,
    height,
    bottom: top + height,
    left: 0,
    right: 0,
    width: 0,
    x: 0,
    y: top,
    toJSON: () => ({}),
  } as DOMRect
}

interface Geometry {
  viewportTop: number
  barHeight: number
  targetTop: number
  scrollTop: number
}

/** Render the scroll manager with a floating user-message bar followed by a
 *  bookmarkable block, then mock the geometry jsdom cannot compute. */
function renderWithChatStream({ viewportTop, barHeight, targetTop, scrollTop }: Geometry) {
  const container = document.createElement('div')
  document.body.appendChild(container)
  const scrollRef: React.RefObject<HTMLDivElement | null> = { current: null }
  root = createRoot(container)
  act(() => {
    root!.render(
      <ScrollProvider>
        <Probe />
        <ChatScrollManager messages={[]} streamingText={undefined} scrollRef={scrollRef}>
          <div>
            {/* Floating pinned user message of the current turn. */}
            <div data-sticky-user-message data-bookmark-id="user-1" />
            {/* The bookmarked event later in the same turn. */}
            <div data-bookmark-id="evt-1" data-step-id="step-9" />
          </div>
        </ChatScrollManager>
      </ScrollProvider>,
    )
  })

  const viewport = scrollRef.current!
  const [bar, target] = Array.from(viewport.querySelectorAll('[data-bookmark-id]'))
  vi.spyOn(bar, 'getBoundingClientRect').mockReturnValue(rect({ top: viewportTop + 20, height: barHeight }))
  vi.spyOn(target, 'getBoundingClientRect').mockReturnValue(rect({ top: targetTop, height: 400 }))
  vi.spyOn(viewport, 'getBoundingClientRect').mockReturnValue(rect({ top: viewportTop, height: 600 }))
  Object.defineProperty(viewport, 'scrollTop', { value: scrollTop, writable: true, configurable: true })
  const scrollTo = vi.fn()
  viewport.scrollTo = scrollTo as unknown as typeof viewport.scrollTo
  return { viewport, scrollTo, target }
}

beforeEach(() => {
  document.body.replaceChildren()
  vi.restoreAllMocks()
  navigateBookmark = null
  navigateStep = null
})

afterEach(() => {
  act(() => root?.unmount())
  root = null
})

describe('ChatScrollManager bookmark/step navigation under the floating bar', () => {
  it('lands the bookmarked block below the collapsed one-line bar', () => {
    const { scrollTo } = renderWithChatStream({ viewportTop: 100, barHeight: 80, targetTop: 3000, scrollTop: 500 })

    navigateBookmark!('evt-1')

    // Plain top alignment would be 500 + (3000 - 100) = 3400; the bar's 80px
    // are subtracted so the block's beginning is not hidden underneath it.
    expect(scrollTo).toHaveBeenCalledWith({ top: 3400 - 80, behavior: 'smooth' })
  })

  it('lands the bookmarked block below the expanded full-message bar', () => {
    const { scrollTo } = renderWithChatStream({ viewportTop: 100, barHeight: 320, targetTop: 3000, scrollTop: 500 })

    navigateBookmark!('evt-1')

    expect(scrollTo).toHaveBeenCalledWith({ top: 3400 - 320, behavior: 'smooth' })
  })

  it('applies the same offset when navigating to a plan step', () => {
    const { scrollTo } = renderWithChatStream({ viewportTop: 100, barHeight: 80, targetTop: 3000, scrollTop: 500 })

    navigateStep!('step-9')

    expect(scrollTo).toHaveBeenCalledWith({ top: 3400 - 80, behavior: 'smooth' })
  })

  it('does not scroll when the bookmarked event is not rendered', () => {
    const { scrollTo } = renderWithChatStream({ viewportTop: 100, barHeight: 80, targetTop: 3000, scrollTop: 500 })

    navigateBookmark!('missing-event')

    expect(scrollTo).not.toHaveBeenCalled()
  })
})
