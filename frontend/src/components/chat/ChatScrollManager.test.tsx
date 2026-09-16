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
import type { ChatVirtualizerHandle } from '@/lib/chatVirtualizer'
import type { ChatMessageUI } from '@/types/messages'

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
  if (!bar || !target) throw new Error('sticky bar and bookmarked block must render for geometry mocking')
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

  it('navigates to a step id containing selector metacharacters (quotes)', () => {
    // Plan step ids are LLM-authored declare_plan payloads and can carry
    // quote characters; the attribute selector must escape them instead of
    // throwing a SyntaxError DOMException inside the click handler (the
    // navigation would silently die with the chat not moving).
    const container = document.createElement('div')
    document.body.appendChild(container)
    const scrollRef: React.RefObject<HTMLDivElement | null> = { current: null }
    root = createRoot(container)
    act(() => {
      root!.render(
        <ScrollProvider>
          <Probe />
          <ChatScrollManager messages={[]} streamingText={undefined} scrollRef={scrollRef}>
            <div data-step-id={'step-"quoted"-9'} />
          </ChatScrollManager>
        </ScrollProvider>,
      )
    })
    const viewport = scrollRef.current!
    const target = viewport.querySelector('[data-step-id]')!
    vi.spyOn(target, 'getBoundingClientRect').mockReturnValue(rect({ top: 3000, height: 400 }))
    vi.spyOn(viewport, 'getBoundingClientRect').mockReturnValue(rect({ top: 100, height: 600 }))
    Object.defineProperty(viewport, 'scrollTop', { value: 500, writable: true, configurable: true })
    const scrollTo = vi.fn()
    viewport.scrollTo = scrollTo as unknown as typeof viewport.scrollTo

    expect(() => act(() => navigateStep!('step-"quoted"-9'))).not.toThrow()
    // No floating bar in this render → plain top alignment: 500 + (3000 - 100).
    expect(scrollTo).toHaveBeenCalledWith({ top: 3400, behavior: 'smooth' })
  })
})

// Finding [27]: an explicit bookmark/step navigation starts a smooth scroll;
// during its first frames the recorded scroll state still says "at bottom", so
// content growth (assistant_chunk) — or even a fresh review prompt — arriving
// in that window must not yank the viewport back to the bottom.
describe('ChatScrollManager navigation suppresses auto-scroll', () => {
  const message = (id: string): ChatMessageUI => ({
    id,
    sessionId: 's1',
    type: 'assistant',
    content: `content ${id}`,
    metadata: {},
    timestamp: 0,
  })

  /** Render the scroll manager at the bottom of a long chat; record every
   *  programmatic `scrollTop` write (jsdom computes no layout, so the getters
   *  are pinned to at-bottom geometry: 9400 + 600 >= 10000 - 50). */
  function renderAtBottomStream() {
    const container = document.createElement('div')
    document.body.appendChild(container)
    const scrollRef: React.RefObject<HTMLDivElement | null> = { current: null }
    root = createRoot(container)
    const rerender = (messages: ChatMessageUI[]) =>
      act(() => {
        root!.render(
          <ScrollProvider>
            <Probe />
            <ChatScrollManager messages={messages} streamingText={undefined} scrollRef={scrollRef}>
              <div>
                <div data-sticky-user-message data-bookmark-id="user-1" />
                <div data-bookmark-id="evt-1" data-step-id="step-9" />
              </div>
            </ChatScrollManager>
          </ScrollProvider>,
        )
      })

    rerender([message('m1')])

    const viewport = scrollRef.current!
    const writes: number[] = []
    const state = { scrollTop: 9400 }
    Object.defineProperty(viewport, 'scrollTop', {
      get: () => state.scrollTop,
      set: (v: number) => {
        state.scrollTop = v
        writes.push(v)
      },
      configurable: true,
    })
    Object.defineProperty(viewport, 'scrollHeight', { get: () => 10_000, configurable: true })
    Object.defineProperty(viewport, 'clientHeight', { get: () => 600, configurable: true })
    const scrollTo = vi.fn()
    viewport.scrollTo = scrollTo as unknown as typeof viewport.scrollTo
    return { rerender, scrollTopWrites: () => [...writes], scrollTo }
  }

  it('content growth within the navigation window does not stick to bottom', () => {
    vi.useFakeTimers()
    const h = renderAtBottomStream()

    act(() => navigateBookmark!('evt-1'))
    expect(h.scrollTo).toHaveBeenCalledTimes(1)

    // assistant_chunk arrives while the smooth scroll is still settling and
    // the recorded baseline still says "was at bottom" — no auto-scroll.
    h.rerender([message('m1'), message('m2')])
    expect(h.scrollTopWrites()).toEqual([])

    // Once the suppression window expires, stick-to-bottom resumes.
    act(() => {
      vi.advanceTimersByTime(600)
    })
    h.rerender([message('m1'), message('m2'), message('m3')])
    expect(h.scrollTopWrites()).toEqual([10_000])

    vi.useRealTimers()
  })

  it('even a fresh review-prompt force-scroll waits out the navigation window', () => {
    vi.useFakeTimers()
    const h = renderAtBottomStream()

    act(() => navigateStep!('step-9'))
    expect(h.scrollTo).toHaveBeenCalledTimes(1)

    const reviewPrompt: ChatMessageUI = {
      id: 'rp-1',
      sessionId: 's1',
      type: 'review_prompt',
      content: 'decide',
      metadata: {},
      timestamp: 0,
    }
    h.rerender([message('m1'), message('m2'), reviewPrompt])

    expect(h.scrollTopWrites()).toEqual([])

    vi.useRealTimers()
  })
})

// Finding #4: the virtualized transcript mounts only visible rows, so the
// DOM-based lookup in ChatScrollManager finds nothing for an off-screen step or
// bookmark. With a virtualizer handle supplied, navigation must scroll the
// virtualizer to the row first, then position it once it has mounted.
describe('ChatScrollManager virtualized navigation', () => {
  function renderVirtualized(
    handle: ChatVirtualizerHandle,
    child: React.ReactNode = <div />,
  ): HTMLElement {
    const container = document.createElement('div')
    document.body.appendChild(container)
    const scrollRef: React.RefObject<HTMLDivElement | null> = { current: null }
    root = createRoot(container)
    act(() => {
      root!.render(
        <ScrollProvider>
          <Probe />
          <ChatScrollManager
            messages={[]}
            streamingText={undefined}
            scrollRef={scrollRef}
            virtualizerRef={{ current: handle }}
          >
            {child}
          </ChatScrollManager>
        </ScrollProvider>,
      )
    })
    return scrollRef.current!
  }

  it('scrolls the virtualizer to an unmounted step, then positions it', () => {
    // Run the deferred positioning synchronously so the assertion is deterministic.
    const rafSpy = vi
      .spyOn(globalThis, 'requestAnimationFrame')
      .mockImplementation((cb) => { cb(0); return 0 as unknown as number })
    const viewportHolder: { current: HTMLElement | null } = { current: null }
    const handle: ChatVirtualizerHandle = {
      scrollToStep: vi.fn((stepId: string) => {
        // The virtualizer mounts the row: model that by inserting the target.
        const el = document.createElement('div')
        el.setAttribute('data-step-id', stepId)
        vi.spyOn(el, 'getBoundingClientRect').mockReturnValue(rect({ top: 3000, height: 400 }))
        viewportHolder.current!.appendChild(el)
        return true
      }),
      scrollToKey: vi.fn(() => false),
    }
    const viewport = renderVirtualized(handle)
    viewportHolder.current = viewport
    vi.spyOn(viewport, 'getBoundingClientRect').mockReturnValue(rect({ top: 100, height: 600 }))
    Object.defineProperty(viewport, 'scrollTop', { value: 500, writable: true, configurable: true })
    const scrollTo = vi.fn()
    viewport.scrollTo = scrollTo as unknown as typeof viewport.scrollTo

    act(() => navigateStep!('step-9'))

    expect(handle.scrollToStep).toHaveBeenCalledWith('step-9')
    // No sticky bar in this render → plain top alignment: 500 + (3000 - 100).
    expect(scrollTo).toHaveBeenCalledWith({ top: 3400, behavior: 'smooth' })
    rafSpy.mockRestore()
  })

  it('positions an already-mounted target without consulting the virtualizer', () => {
    const handle: ChatVirtualizerHandle = { scrollToStep: vi.fn(() => false), scrollToKey: vi.fn(() => false) }
    const viewport = renderVirtualized(handle, <div data-step-id="step-9" />)
    const target = viewport.querySelector('[data-step-id]')!
    vi.spyOn(target, 'getBoundingClientRect').mockReturnValue(rect({ top: 3000, height: 400 }))
    vi.spyOn(viewport, 'getBoundingClientRect').mockReturnValue(rect({ top: 100, height: 600 }))
    Object.defineProperty(viewport, 'scrollTop', { value: 500, writable: true, configurable: true })
    const scrollTo = vi.fn()
    viewport.scrollTo = scrollTo as unknown as typeof viewport.scrollTo

    act(() => navigateStep!('step-9'))

    expect(handle.scrollToStep).not.toHaveBeenCalled()
    expect(scrollTo).toHaveBeenCalledWith({ top: 3400, behavior: 'smooth' })
  })

  it('does nothing when the target is unmounted and the virtualizer cannot find it', () => {
    const handle: ChatVirtualizerHandle = { scrollToStep: vi.fn(() => false), scrollToKey: vi.fn(() => false) }
    const viewport = renderVirtualized(handle)
    Object.defineProperty(viewport, 'scrollTop', { value: 500, writable: true, configurable: true })
    const scrollTo = vi.fn()
    viewport.scrollTo = scrollTo as unknown as typeof viewport.scrollTo

    act(() => navigateStep!('step-9'))

    expect(handle.scrollToStep).toHaveBeenCalledWith('step-9')
    expect(scrollTo).not.toHaveBeenCalled()
  })

  it('navigates to an unmounted bookmark via the virtualizer', () => {
    const rafSpy = vi
      .spyOn(globalThis, 'requestAnimationFrame')
      .mockImplementation((cb) => { cb(0); return 0 as unknown as number })
    const viewportHolder: { current: HTMLElement | null } = { current: null }
    const handle: ChatVirtualizerHandle = {
      scrollToStep: vi.fn(() => false),
      scrollToKey: vi.fn((key: string) => {
        const el = document.createElement('div')
        el.setAttribute('data-bookmark-id', key)
        vi.spyOn(el, 'getBoundingClientRect').mockReturnValue(rect({ top: 2000, height: 400 }))
        viewportHolder.current!.appendChild(el)
        return true
      }),
    }
    const viewport = renderVirtualized(handle)
    viewportHolder.current = viewport
    vi.spyOn(viewport, 'getBoundingClientRect').mockReturnValue(rect({ top: 100, height: 600 }))
    Object.defineProperty(viewport, 'scrollTop', { value: 0, writable: true, configurable: true })
    const scrollTo = vi.fn()
    viewport.scrollTo = scrollTo as unknown as typeof viewport.scrollTo

    act(() => navigateBookmark!('evt-9'))

    expect(handle.scrollToKey).toHaveBeenCalledWith('evt-9')
    expect(scrollTo).toHaveBeenCalledWith({ top: 1900, behavior: 'smooth' })
    rafSpy.mockRestore()
  })
})
