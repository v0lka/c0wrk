// @vitest-environment jsdom
// Unit tests for the sticky-aware chat scroll helper. The chat renders each
// turn's user message as an opaque `position: sticky; top: 0` bar INSIDE the
// scroll container; scrollBlockStartIntoView must subtract that bar's height
// so the target block's beginning lands below the overlay instead of hidden
// beneath it.
import { describe, it, expect, vi, beforeEach } from 'vitest'
import { scrollBlockStartIntoView, stickyBarOverlaying, STICKY_USER_MESSAGE_SELECTOR } from './chatScroll'

interface RectInit {
  top: number
  height: number
}

function withRect(el: Element, { top, height }: RectInit): Element {
  vi.spyOn(el, 'getBoundingClientRect').mockReturnValue({
    top,
    height,
    bottom: top + height,
    left: 0,
    right: 0,
    width: 0,
    x: 0,
    y: top,
    toJSON: () => ({}),
  } as DOMRect)
  return el
}

interface Tree {
  viewport: HTMLElement
  scrollTo: ReturnType<typeof vi.fn>
}

/** Build a scroll viewport with `scrollTop` and a spied `scrollTo`. */
function makeViewport(scrollTop = 0): Tree {
  const viewport = document.createElement('div')
  Object.defineProperty(viewport, 'scrollTop', {
    value: scrollTop,
    writable: true,
    configurable: true,
  })
  const scrollTo = vi.fn()
  viewport.scrollTo = scrollTo as unknown as typeof viewport.scrollTo
  return { viewport, scrollTo }
}

function stickyBar(): HTMLElement {
  const bar = document.createElement('div')
  bar.setAttribute('data-sticky-user-message', '')
  return bar
}

function bookmarkRow(key: string): HTMLElement {
  const row = document.createElement('div')
  row.setAttribute('data-bookmark-id', key)
  return row
}

beforeEach(() => {
  vi.restoreAllMocks()
})

describe('stickyBarOverlaying', () => {
  it('returns the nearest sticky bar preceding the target', () => {
    const { viewport } = makeViewport()
    const bar1 = stickyBar()
    const bar2 = stickyBar()
    const target = bookmarkRow('evt-1')
    const laterBar = stickyBar()
    viewport.append(bar1, bar2, target, laterBar)

    expect(stickyBarOverlaying(viewport, target)).toBe(bar2)
  })

  it('returns null when no sticky bar precedes the target', () => {
    const { viewport } = makeViewport()
    const target = bookmarkRow('evt-1')
    const laterBar = stickyBar()
    viewport.append(target, laterBar)

    expect(stickyBarOverlaying(viewport, target)).toBeNull()
  })

  it('returns null when the target is inside the sticky bar (bookmark on the pinned message)', () => {
    const { viewport } = makeViewport()
    const bar = stickyBar()
    bar.setAttribute('data-bookmark-id', 'user-1')
    // The bookmarked row for a pinned user message IS the sticky bar; render it
    // as the bar itself wrapping its bubble to exercise the contains() check
    // (a node compared with itself never reports DOCUMENT_POSITION_FOLLOWING).
    const target = bookmarkRow('user-1')
    bar.appendChild(target)
    viewport.append(bar)

    expect(stickyBarOverlaying(viewport, bar)).toBeNull()
    expect(stickyBarOverlaying(viewport, target)).toBeNull()
  })

  it('exposes the marker selector used to find floating bars', () => {
    expect(STICKY_USER_MESSAGE_SELECTOR).toBe('[data-sticky-user-message]')
  })
})

describe('scrollBlockStartIntoView', () => {
  it('subtracts the collapsed one-line bar height from the top alignment', () => {
    const { viewport, scrollTo } = makeViewport(500)
    const bar = withRect(stickyBar(), { top: 100, height: 80 })
    const target = withRect(bookmarkRow('evt-1'), { top: 3000, height: 400 })
    withRect(viewport, { top: 100, height: 600 })
    viewport.append(bar, target)

    scrollBlockStartIntoView(viewport, target)

    // Aligning the target top with the viewport top: 500 + (3000 - 100) = 3400;
    // then push down below the overlaying bar: 3400 - 80 = 3320.
    expect(scrollTo).toHaveBeenCalledWith({ top: 3320, behavior: 'smooth' })
  })

  it('subtracts the expanded full-message bar height when the pin is expanded', () => {
    const { viewport, scrollTo } = makeViewport(500)
    const bar = withRect(stickyBar(), { top: 100, height: 320 })
    const target = withRect(bookmarkRow('evt-1'), { top: 3000, height: 400 })
    withRect(viewport, { top: 100, height: 600 })
    viewport.append(bar, target)

    scrollBlockStartIntoView(viewport, target)

    expect(scrollTo).toHaveBeenCalledWith({ top: 500 + 2900 - 320, behavior: 'smooth' })
  })

  it('caps the offset when the expanded bar is taller than the scrollport', () => {
    const { viewport, scrollTo } = makeViewport(500)
    // Expanded pinned message 1500px tall in a 600px-tall scrollport.
    const bar = withRect(stickyBar(), { top: 100, height: 1500 })
    const target = withRect(bookmarkRow('evt-1'), { top: 3000, height: 400 })
    withRect(viewport, { top: 100, height: 600 })
    viewport.append(bar, target)
    Object.defineProperty(viewport, 'clientHeight', { value: 600, configurable: true })

    scrollBlockStartIntoView(viewport, target)

    // Cap: 600 - 80 = 520 subtracted instead of 1500, so an 80px sliver of the
    // target stays visible below the oversized bar.
    expect(scrollTo).toHaveBeenCalledWith({ top: 500 + 2900 - 520, behavior: 'smooth' })
  })

  it('aligns to the scrollport top when no sticky bar precedes the target', () => {
    const { viewport, scrollTo } = makeViewport(120)
    const target = withRect(bookmarkRow('evt-1'), { top: 900, height: 200 })
    withRect(viewport, { top: 80, height: 600 })
    viewport.append(target)

    scrollBlockStartIntoView(viewport, target)

    expect(scrollTo).toHaveBeenCalledWith({ top: 120 + (900 - 80), behavior: 'smooth' })
  })

  it('does not subtract anything when the target is the sticky bar itself', () => {
    const { viewport, scrollTo } = makeViewport(50)
    const bar = withRect(stickyBar(), { top: 150, height: 80 })
    withRect(viewport, { top: 100, height: 600 })
    viewport.append(bar)

    scrollBlockStartIntoView(viewport, bar)

    expect(scrollTo).toHaveBeenCalledWith({ top: 50 + (150 - 100), behavior: 'smooth' })
  })

  it('clamps the resulting scroll position at zero', () => {
    const { viewport, scrollTo } = makeViewport(10)
    const bar = withRect(stickyBar(), { top: 0, height: 80 })
    const target = withRect(bookmarkRow('evt-1'), { top: 5, height: 40 })
    withRect(viewport, { top: 0, height: 600 })
    viewport.append(bar, target)

    scrollBlockStartIntoView(viewport, target)

    expect(scrollTo).toHaveBeenCalledWith({ top: 0, behavior: 'smooth' })
  })
})
