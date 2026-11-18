// @vitest-environment jsdom
//
// useTabStripOverflow — the tab-strip measurement seam.
//
// jsdom lays out nothing: every geometry number is stubbed per element with
// Object.defineProperty (clientWidth/scrollWidth/scrollLeft on the scroller,
// offsetLeft/offsetWidth plus a scrollIntoView spy on each [role="tab"] row).
// The scroller and row stubs are applied from ref callbacks, so they are in
// place BEFORE the mount effect measures. All synchronisation is synchronous —
// prop changes through rerender, "scrolling" through a dispatched scroll
// event, "resizing" through the captured mock ResizeObserver callback — no
// timers anywhere.

import { describe, it, expect, vi, beforeEach, afterEach, type Mock } from 'vitest'
import { act } from 'react'
import { createRoot, type Root } from 'react-dom/client'

import { useTabStripOverflow, type UseTabStripOverflowReturn } from './useTabStripOverflow'

// --- mock ResizeObserver (jsdom ships none) ---------------------------------

class MockResizeObserver {
  static instances: MockResizeObserver[] = []
  callback: ResizeObserverCallback
  observed: Element[] = []
  disconnected = false

  constructor(callback: ResizeObserverCallback) {
    this.callback = callback
    MockResizeObserver.instances.push(this)
  }

  observe(target: Element): void {
    this.observed.push(target)
  }

  unobserve(_target: Element): void {}

  disconnect(): void {
    this.disconnected = true
  }
}

// --- geometry fixtures ------------------------------------------------------

const TAB_IDS = ['t1', 't2', 't3', 't4', 't5', 't6']
const TAB_WIDTH = 150

interface StripGeometry {
  metrics: { scrollLeft: number; clientWidth: number; scrollWidth: number }
  spans: Record<string, [number, number]>
}

/** Six 150px tabs: the whole strip is 900px wide against a 400px viewport. */
function overflowingGeometry(ids: readonly string[], scrollLeft = 0): StripGeometry {
  const spans: Record<string, [number, number]> = {}
  ids.forEach((id, index) => {
    spans[id] = [index * TAB_WIDTH, TAB_WIDTH]
  })
  return { metrics: { scrollLeft, clientWidth: 400, scrollWidth: ids.length * TAB_WIDTH }, spans }
}

/** Same tabs, but the viewport fits them all (scrollWidth clamps to clientWidth). */
function fittingGeometry(ids: readonly string[]): StripGeometry {
  return { ...overflowingGeometry(ids), metrics: { scrollLeft: 0, clientWidth: 400, scrollWidth: 400 } }
}

let geometry: StripGeometry
let scrollToSpy: Mock

// --- stubs (installed from ref callbacks, i.e. before effects run) ----------

function stubScroller(el: HTMLDivElement): void {
  const metrics = geometry.metrics
  Object.defineProperty(el, 'scrollLeft', { configurable: true, get: () => metrics.scrollLeft })
  Object.defineProperty(el, 'clientWidth', { configurable: true, get: () => metrics.clientWidth })
  Object.defineProperty(el, 'scrollWidth', { configurable: true, get: () => metrics.scrollWidth })
  scrollToSpy = vi.fn()
  Object.defineProperty(el, 'scrollTo', { configurable: true, value: scrollToSpy })
}

function stubRow(el: Element, span: [number, number]): void {
  Object.defineProperty(el, 'offsetLeft', { configurable: true, value: span[0] })
  Object.defineProperty(el, 'offsetWidth', { configurable: true, value: span[1] })
  Object.defineProperty(el, 'scrollIntoView', { configurable: true, value: vi.fn() })
}

// --- harness ----------------------------------------------------------------

let exposed: UseTabStripOverflowReturn
let container: HTMLDivElement
let root: Root

function Harness({ tabIds, activeTabId }: { tabIds: string[]; activeTabId?: string }) {
  exposed = useTabStripOverflow({ tabIds, activeTabId })
  return (
    <div
      ref={(el) => {
        exposed.scrollerRef.current = el
        if (el) stubScroller(el)
      }}
    >
      <div role="tablist">
        {tabIds.map((id) => (
          <div
            key={id}
            ref={(el) => {
              const span = geometry.spans[id]
              if (el && span) stubRow(el, span)
            }}
            role="tab"
            data-tab-id={id}
          >
            {id}
          </div>
        ))}
      </div>
    </div>
  )
}

function renderStrip(props: { tabIds?: string[]; activeTabId?: string } = {}): void {
  container = document.createElement('div')
  document.body.appendChild(container)
  root = createRoot(container)
  act(() => {
    root.render(<Harness tabIds={props.tabIds ?? [...TAB_IDS]} activeTabId={props.activeTabId} />)
  })
}

function rerenderStrip(props: { tabIds: string[]; activeTabId?: string }): void {
  act(() => {
    root.render(<Harness tabIds={props.tabIds} activeTabId={props.activeTabId} />)
  })
}

function scroller(): HTMLDivElement {
  const el = exposed.scrollerRef.current
  if (!el) throw new Error('scroller ref is not attached')
  return el
}

function rows(): NodeListOf<HTMLElement> {
  return scroller().querySelectorAll<HTMLElement>('[role="tab"]')
}

function fireScroll(): void {
  act(() => {
    scroller().dispatchEvent(new Event('scroll'))
  })
}

beforeEach(() => {
  MockResizeObserver.instances = []
  geometry = fittingGeometry(TAB_IDS)
  vi.stubGlobal('ResizeObserver', MockResizeObserver)
})

afterEach(() => {
  act(() => {
    root?.unmount()
  })
  container?.remove()
  document.body.innerHTML = ''
  vi.unstubAllGlobals()
  vi.restoreAllMocks()
  MockResizeObserver.instances = []
})

// --- measurement triggers ---------------------------------------------------

describe('useTabStripOverflow — measurement triggers', () => {
  it('measures on mount from the stubbed geometry', () => {
    geometry = overflowingGeometry(TAB_IDS)
    renderStrip()
    expect(exposed.overflowing).toBe(true)
    expect(exposed.canScrollLeft).toBe(false)
    expect(exposed.canScrollRight).toBe(true)
  })

  it('transitions from fitting to overflowing when the metrics grow', () => {
    renderStrip()
    expect(exposed.overflowing).toBe(false)
    expect(exposed.canScrollLeft).toBe(false)
    expect(exposed.canScrollRight).toBe(false)

    geometry.metrics.scrollWidth = TAB_IDS.length * TAB_WIDTH
    fireScroll()
    expect(exposed.overflowing).toBe(true)
    expect(exposed.canScrollLeft).toBe(false)
    expect(exposed.canScrollRight).toBe(true)
  })

  it('refreshes both edge flags from the scroll position, epsilon boundary included', () => {
    geometry = overflowingGeometry(TAB_IDS)
    renderStrip()

    geometry.metrics.scrollLeft = 500 // max scroll = 900 - 400
    fireScroll()
    expect(exposed.canScrollLeft).toBe(true)
    expect(exposed.canScrollRight).toBe(false)

    // 0.5px hidden to the right — within EDGE_EPSILON of the edge, so "at" it.
    geometry.metrics.scrollLeft = 499.5
    fireScroll()
    expect(exposed.canScrollLeft).toBe(true)
    expect(exposed.canScrollRight).toBe(false)

    geometry.metrics.scrollLeft = 200
    fireScroll()
    expect(exposed.canScrollLeft).toBe(true)
    expect(exposed.canScrollRight).toBe(true)
  })

  it('re-measures when the tab list changes and re-arms the ResizeObserver', () => {
    renderStrip({ tabIds: ['t1', 't2', 't3', 't4'] })
    expect(exposed.overflowing).toBe(false)

    geometry = overflowingGeometry(['t1', 't2', 't3', 't4', 't5', 't6'])
    rerenderStrip({ tabIds: ['t1', 't2', 't3', 't4', 't5', 't6'] })
    expect(exposed.overflowing).toBe(true)
    expect(exposed.canScrollRight).toBe(true)

    expect(MockResizeObserver.instances).toHaveLength(2)
    expect(MockResizeObserver.instances[0]!.disconnected).toBe(true)
    expect(MockResizeObserver.instances[1]!.observed).toEqual([scroller(), scroller().querySelector('[role="tablist"]')])
  })

  it('re-measures when the active tab id changes', () => {
    renderStrip({ activeTabId: 't1' })
    expect(exposed.overflowing).toBe(false)

    geometry.metrics.scrollWidth = TAB_IDS.length * TAB_WIDTH
    rerenderStrip({ tabIds: [...TAB_IDS], activeTabId: 't2' })
    expect(exposed.overflowing).toBe(true)
  })
})

// --- ResizeObserver wiring --------------------------------------------------

describe('useTabStripOverflow — ResizeObserver', () => {
  it('observes BOTH the scroller and the tablist', () => {
    renderStrip()
    expect(MockResizeObserver.instances).toHaveLength(1)
    expect(MockResizeObserver.instances[0]!.observed).toEqual([scroller(), scroller().querySelector('[role="tablist"]')])
  })

  it('re-measures from the observer callback', () => {
    renderStrip()
    expect(exposed.canScrollLeft).toBe(false)
    const observer = MockResizeObserver.instances[0]!

    geometry.metrics.scrollLeft = 500
    act(() => {
      observer.callback([], observer as unknown as ResizeObserver)
    })
    expect(exposed.canScrollLeft).toBe(true)
    expect(exposed.canScrollRight).toBe(false)
  })

  it('renders without a ResizeObserver (jsdom without the polyfill)', () => {
    vi.stubGlobal('ResizeObserver', undefined)
    renderStrip()
    expect(MockResizeObserver.instances).toHaveLength(0)

    // The scroll listener is independent of the observer and still measures.
    geometry.metrics.scrollWidth = TAB_IDS.length * TAB_WIDTH
    geometry.metrics.scrollLeft = 500
    fireScroll()
    expect(exposed.canScrollLeft).toBe(true)
    expect(exposed.canScrollRight).toBe(false)
  })
})

// --- scroll steps -----------------------------------------------------------

describe('useTabStripOverflow — scroll steps', () => {
  it('scrollNext targets the first tab clipped past the right edge', () => {
    geometry = overflowingGeometry(TAB_IDS)
    renderStrip()
    act(() => {
      exposed.scrollNext()
    })
    // Viewport [0, 400]: the first span end beyond 400 is t3's (450) → 450 - 400.
    expect(scrollToSpy).toHaveBeenCalledTimes(1)
    expect(scrollToSpy).toHaveBeenCalledWith({ left: 50, behavior: 'instant' })
  })

  it('scrollNext at the end leaves the scroll position unchanged', () => {
    geometry = overflowingGeometry(TAB_IDS, 500)
    renderStrip()
    act(() => {
      exposed.scrollNext()
    })
    // No span end lies beyond [500, 900]: the clamped current position comes back.
    expect(scrollToSpy).toHaveBeenCalledWith({ left: 500, behavior: 'instant' })
  })

  it('scrollPrev targets the last tab clipped at the left edge', () => {
    geometry = overflowingGeometry(TAB_IDS, 450)
    renderStrip()
    act(() => {
      exposed.scrollPrev()
    })
    // The largest span start before 450 is t3's (300).
    expect(scrollToSpy).toHaveBeenCalledWith({ left: 300, behavior: 'instant' })
  })

  it('scrollPrev at the start leaves the scroll position unchanged', () => {
    renderStrip()
    act(() => {
      exposed.scrollPrev()
    })
    expect(scrollToSpy).toHaveBeenCalledWith({ left: 0, behavior: 'instant' })
  })
})

// --- revealTab --------------------------------------------------------------

describe('useTabStripOverflow — revealTab', () => {
  it('scrolls exactly the matching tab row into view', () => {
    geometry = overflowingGeometry(TAB_IDS)
    renderStrip()
    act(() => {
      exposed.revealTab('t5')
    })
    for (const row of rows()) {
      const spy = row.scrollIntoView as unknown as Mock
      if (row.dataset.tabId === 't5') {
        expect(spy).toHaveBeenCalledTimes(1)
        expect(spy).toHaveBeenCalledWith({ block: 'nearest', inline: 'nearest' })
      } else {
        expect(spy).not.toHaveBeenCalled()
      }
    }
  })

  it('ignores an unknown tab id', () => {
    renderStrip()
    expect(() => exposed.revealTab('missing')).not.toThrow()
    for (const row of rows()) {
      expect(row.scrollIntoView as unknown as Mock).not.toHaveBeenCalled()
    }
  })

  it('is a no-op when scrollIntoView is unavailable', () => {
    geometry = overflowingGeometry(TAB_IDS)
    renderStrip()
    const row = scroller().querySelector<HTMLElement>('[data-tab-id="t3"]')!
    Object.defineProperty(row, 'scrollIntoView', { configurable: true, value: undefined })

    expect(() => exposed.revealTab('t3')).not.toThrow()
    const others = scroller().querySelectorAll<HTMLElement>('[role="tab"]:not([data-tab-id="t3"])')
    for (const other of others) {
      expect(other.scrollIntoView as unknown as Mock).not.toHaveBeenCalled()
    }
  })
})

// --- cleanup ----------------------------------------------------------------

describe('useTabStripOverflow — cleanup', () => {
  it('removes the scroll listener and disconnects the observer on unmount', () => {
    const addSpy = vi.spyOn(EventTarget.prototype, 'addEventListener')
    const removeSpy = vi.spyOn(EventTarget.prototype, 'removeEventListener')
    const errorSpy = vi.spyOn(console, 'error').mockImplementation(() => {})

    renderStrip()
    const observer = MockResizeObserver.instances[0]!
    const el = scroller()

    act(() => {
      root.unmount()
    })

    expect(observer.disconnected).toBe(true)
    expect(addSpy).toHaveBeenCalledWith('scroll', expect.any(Function), { passive: true })
    expect(removeSpy).toHaveBeenCalledWith('scroll', expect.any(Function))

    // A scroll after unmount must reach nothing: no leaked listener, no
    // stray state update, no act() complaint.
    geometry.metrics.scrollWidth = TAB_IDS.length * TAB_WIDTH
    el.dispatchEvent(new Event('scroll'))
    expect(errorSpy).not.toHaveBeenCalled()
  })
})
