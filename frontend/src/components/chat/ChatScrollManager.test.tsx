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
import { useChatStore } from '@/stores/chatStore'
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
        <ChatScrollManager sessionId={null} messages={[]} streamingText={undefined} scrollRef={scrollRef}>
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
          <ChatScrollManager sessionId={null} messages={[]} streamingText={undefined} scrollRef={scrollRef}>
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
            <ChatScrollManager sessionId={null} messages={messages} streamingText={undefined} scrollRef={scrollRef}>
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

  it('a step navigation holds even when a new decision-type message arrives mid-window', () => {
    vi.useFakeTimers()
    const h = renderAtBottomStream()

    act(() => navigateStep!('step-9'))
    expect(h.scrollTo).toHaveBeenCalledTimes(1)

    // A newly-appearing message mid-navigation must not snap the viewport to
    // the bottom while the smooth scroll is still settling.
    h.rerender([message('m1'), message('m2'), message('m3')])

    expect(h.scrollTopWrites()).toEqual([])

    vi.useRealTimers()
  })
})

// Session-switch scroll persistence: the reading position is saved to
// chatStore when a session's viewport unmounts and restored on that session's
// next initial mount; a session with no saved position opens pinned to the
// bottom; content growth (async markdown/highlight layout, images decoding,
// streamed text) re-pins tail-followers to the bottom but
// never jerks a reader who scrolled up.
describe('ChatScrollManager session-switch scroll persistence', () => {
  const message = (id: string): ChatMessageUI => ({
    id,
    sessionId: 's1',
    type: 'assistant',
    content: `content ${id}`,
    metadata: {},
    timestamp: 0,
  })

  // jsdom computes no layout, and the initial-mount layout effect runs DURING
  // the first render — before a test could touch the freshly created element.
  // The geometry the manager reads is therefore pinned at the prototype level
  // (restored by the file-level vi.restoreAllMocks in beforeEach).
  function pinGeometry(scrollHeight: number, clientHeight: number) {
    vi.spyOn(window.Element.prototype, 'scrollHeight', 'get').mockReturnValue(scrollHeight)
    vi.spyOn(window.Element.prototype, 'clientHeight', 'get').mockReturnValue(clientHeight)
  }

  // jsdom lacks ResizeObserver. This stub records instances so a test can
  // deliver synthetic resize notifications at will.
  class ResizeObserverStub {
    static instances: ResizeObserverStub[] = []
    readonly targets: Element[] = []
    private callback: ResizeObserverCallback
    constructor(callback: ResizeObserverCallback) {
      this.callback = callback
      ResizeObserverStub.instances.push(this)
    }
    observe(target: Element): void { this.targets.push(target) }
    unobserve(): void { /* unused in these tests */ }
    disconnect(): void { /* unused in these tests */ }
    fire(): void { this.callback([], this as unknown as ResizeObserver) }
  }

  beforeEach(() => {
    useChatStore.setState({
      scrollPositions: {},
      taskActive: {},
      taskFlagsEventAt: {},
      unfinishedTaskStatus: {},
    })
    ResizeObserverStub.instances = []
    vi.stubGlobal('ResizeObserver', ResizeObserverStub)
  })

  afterEach(() => {
    vi.unstubAllGlobals()
  })

  function renderViewport(sessionId: string | null): HTMLElement {
    const container = document.createElement('div')
    document.body.appendChild(container)
    const scrollRef: React.RefObject<HTMLDivElement | null> = { current: null }
    root = createRoot(container)
    act(() => {
      root!.render(
        <ScrollProvider>
          <ChatScrollManager
            sessionId={sessionId}
            messages={[message('m1')]}
            streamingText={undefined}
            scrollRef={scrollRef}
          >
            <div data-transcript-content />
          </ChatScrollManager>
        </ScrollProvider>,
      )
    })
    return scrollRef.current!
  }

  it('saves the last tracked scroll state on unmount', () => {
    pinGeometry(10_000, 600)
    const viewport = renderViewport('s1')
    // The user scrolled up mid-transcript and stopped there.
    act(() => {
      viewport.scrollTop = 4_200
      viewport.dispatchEvent(new Event('scroll'))
    })
    act(() => root!.unmount())
    root = null
    expect(useChatStore.getState().scrollPositions['s1']).toEqual({ scrollTop: 4_200, scrollHeight: 10_000 })
  })

  it('restores the saved reading position on the next initial mount', () => {
    pinGeometry(10_000, 600)
    useChatStore.getState().saveScrollPosition('s1', { scrollTop: 4_200, scrollHeight: 9_000 })
    const viewport = renderViewport('s1')
    // jsdom does no clamping; a real browser clamps the restored offset
    // against the freshly mounted content (see the comment in the manager).
    expect(viewport.scrollTop).toBe(4_200)
    // A restored mid-content position is NOT at the bottom, so a later content
    // growth must not jerk it (the reader had scrolled up before switching).
    pinGeometry(12_000, 600)
    const ro = ResizeObserverStub.instances[ResizeObserverStub.instances.length - 1]!
    act(() => { ro.fire() })
    expect(viewport.scrollTop).toBe(4_200)
  })

  it('pins to the bottom on switch when the session task is running, even with a saved position', () => {
    pinGeometry(10_000, 600)
    useChatStore.getState().saveScrollPosition('s1', { scrollTop: 4_200, scrollHeight: 9_000 })
    act(() => { useChatStore.getState().setTaskActive('s1', true) })
    const viewport = renderViewport('s1')
    // A running session keeps producing output at the bottom: switching to it
    // must reveal the live tail, not the stale reading position.
    expect(viewport.scrollTop).toBe(10_000)
  })

  // Rerender-capable variant of renderViewport for tests that grow the
  // transcript after mount (late history merge, streamed tail).
  function renderViewportWithRerender(sessionId: string | null): {
    viewport: HTMLElement
    rerender: (messages: ChatMessageUI[]) => void
  } {
    const container = document.createElement('div')
    document.body.appendChild(container)
    const scrollRef: React.RefObject<HTMLDivElement | null> = { current: null }
    root = createRoot(container)
    const rerender = (messages: ChatMessageUI[]) =>
      act(() => {
        root!.render(
          <ScrollProvider>
            <ChatScrollManager
              sessionId={sessionId}
              messages={messages}
              streamingText={undefined}
              scrollRef={scrollRef}
            >
              <div data-transcript-content />
            </ChatScrollManager>
          </ScrollProvider>,
        )
      })
    rerender([message('m1')])
    return { viewport: scrollRef.current!, rerender }
  }

  it('re-pins to the live tail when the task flag is corrected to running after mount', () => {
    pinGeometry(10_000, 600)
    useChatStore.getState().saveScrollPosition('s1', { scrollTop: 4_200, scrollHeight: 9_000 })
    // Stale-false in-memory flag at mount: the mount-time decision restores
    // the saved reading position...
    const { viewport, rerender } = renderViewportWithRerender('s1')
    expect(viewport.scrollTop).toBe(4_200)
    // ...then the asynchronous switch-time corrector (useTaskFlagRestore's
    // status RPC, or reconcileRuntimeStatus after the history merge) flips
    // the flag to running — the viewport must jump to the live tail,
    // mirroring the mount-time running-session pin.
    act(() => { useChatStore.getState().setTaskActive('s1', true) })
    expect(viewport.scrollTop).toBe(10_000)
    // The refreshed baseline keeps later content growth stuck to the bottom
    // instead of raising the "New activity" pill over a pinned viewport.
    pinGeometry(11_000, 600)
    rerender([message('m1'), message('m2')])
    expect(viewport.scrollTop).toBe(11_000)
  })

  it('keeps sticking to the bottom after a banner jump-to-bottom', () => {
    pinGeometry(10_000, 600)
    const { viewport, rerender } = renderViewportWithRerender('s2')
    expect(viewport.scrollTop).toBe(10_000)
    // The user scrolled up to read earlier output.
    act(() => {
      viewport.scrollTop = 3_000
      viewport.dispatchEvent(new Event('scroll'))
    })
    // New output lands while scrolled away: no yank, the pill appears.
    pinGeometry(10_500, 600)
    rerender([message('m1'), message('m2')])
    expect(viewport.scrollTop).toBe(3_000)
    const banner = viewport.querySelector('button[aria-label="Jump to new activity"]')
    expect(banner).not.toBeNull()
    act(() => { banner!.dispatchEvent(new MouseEvent('click', { bubbles: true })) })
    expect(viewport.scrollTop).toBe(10_500)
    // The jump refreshed the wasAt-bottom baseline: the NEXT message keeps
    // sticking (a stale baseline would skip the write and re-raise the pill).
    pinGeometry(11_200, 600)
    rerender([message('m1'), message('m2'), message('m3')])
    expect(viewport.scrollTop).toBe(11_200)
  })

  // Regression (finding 21): a session switch restores a saved mid-transcript
  // position, and ChatArea's load effect then always re-merges the newest
  // page — a fresh messages array with IDENTICAL content. "Not at the
  // bottom" must not be mistaken for "the user scrolled away from new
  // output": an unchanged recognized tail (same last id, same count, same
  // streaming text) must not raise the "New activity" pill.
  it('does not raise the "New activity" pill when a content-identical history re-merge follows a position restore', () => {
    pinGeometry(10_000, 600)
    useChatStore.getState().saveScrollPosition('s1', { scrollTop: 4_200, scrollHeight: 9_000 })
    const { viewport, rerender } = renderViewportWithRerender('s1')
    // The mount branch restored the saved mid-transcript position (not at
    // the bottom) and opened pill-free.
    expect(viewport.scrollTop).toBe(4_200)
    expect(viewport.querySelector('button[aria-label="Jump to new activity"]')).toBeNull()

    // The newest-page history RPC resolves and re-merges the SAME message in
    // a NEW array (mergeHistoryMessages unconditionally writes a fresh
    // messageOrder). Nothing new appeared — still no pill.
    rerender([{ ...message('m1') }])
    expect(viewport.querySelector('button[aria-label="Jump to new activity"]')).toBeNull()

    // A genuinely new tail message still raises the pill.
    rerender([message('m1'), message('m2')])
    expect(viewport.querySelector('button[aria-label="Jump to new activity"]')).not.toBeNull()
  })

  it('pins to the bottom when the session has no saved position', () => {
    pinGeometry(10_000, 600)
    const viewport = renderViewport('s2')
    expect(viewport.scrollTop).toBe(10_000)
  })

  it('re-pins to the bottom when the content grows while following the tail', () => {
    pinGeometry(10_000, 600)
    const viewport = renderViewport('s2')
    expect(viewport.scrollTop).toBe(10_000)
    // The observer watches the transcript content wrapper, not the viewport.
    const ro = ResizeObserverStub.instances[ResizeObserverStub.instances.length - 1]!
    expect(ro.targets[0]?.hasAttribute('data-transcript-content')).toBe(true)
    // Late async layout grows the content height.
    pinGeometry(12_000, 600)
    act(() => { ro.fire() })
    expect(viewport.scrollTop).toBe(12_000)
  })

  // The browser delivers a programmatic scrollTop write's scroll event
  // asynchronously, at its rendering steps — by which time the content may
  // have grown past the write's target (async markdown/highlight layout, an
  // image decoding).
  // That delivered event must not be mistaken for the user scrolling away:
  // recomputing "at bottom" against the grown content would poison the
  // baseline and permanently disable stick-to-bottom for the rest of the run.
  it('keeps following the tail when content grows between the pin write and its scroll-event delivery', () => {
    pinGeometry(10_000, 600)
    const viewport = renderViewport('s2')
    expect(viewport.scrollTop).toBe(10_000)
    // Growth lands BEFORE the queued scroll event from the mount's pin write
    // is delivered.
    pinGeometry(10_700, 600)
    act(() => { viewport.dispatchEvent(new Event('scroll')) })
    // The own-write event did not poison the at-bottom flag: the next growth
    // still re-pins the viewport to the new bottom.
    const ro = ResizeObserverStub.instances[ResizeObserverStub.instances.length - 1]!
    pinGeometry(11_400, 600)
    act(() => { ro.fire() })
    expect(viewport.scrollTop).toBe(11_400)
  })

  it('does not jerk the viewport when the content grows after the user scrolled up', () => {
    pinGeometry(10_000, 600)
    const viewport = renderViewport('s2')
    act(() => {
      viewport.scrollTop = 3_000
      viewport.dispatchEvent(new Event('scroll'))
    })
    pinGeometry(12_000, 600)
    const ro = ResizeObserverStub.instances[ResizeObserverStub.instances.length - 1]!
    act(() => { ro.fire() })
    expect(viewport.scrollTop).toBe(3_000)
  })
})
