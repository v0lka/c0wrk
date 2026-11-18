import { describe, it, expect } from 'vitest'
import {
  EDGE_EPSILON,
  canScrollLeft,
  canScrollRight,
  clampScrollLeft,
  isOverflowing,
  nextTabLeft,
  prevTabLeft,
} from './tabStripScroll'
import type { StripMetrics, TabSpan } from './tabStripScroll'

function metrics(scrollLeft: number, clientWidth: number, scrollWidth: number): StripMetrics {
  return { scrollLeft, clientWidth, scrollWidth }
}

// Four 80px tabs with 10px gaps inside a 400px scrollable strip
// (clientWidth 100, max scrollLeft 300).
const clientWidth = 100
const scrollWidth = 400
const maxScrollLeft = scrollWidth - clientWidth // 300
const fourTabs: TabSpan[] = [
  { start: 0, end: 80 },
  { start: 90, end: 170 },
  { start: 180, end: 260 },
  { start: 270, end: 350 },
]

describe('tabStripScroll — overflow state', () => {
  it('reports no scrolling possible without overflow', () => {
    const m = metrics(0, 200, 200)
    expect(isOverflowing(m)).toBe(false)
    expect(canScrollLeft(m)).toBe(false)
    expect(canScrollRight(m)).toBe(false)
  })

  it('treats an exact fit as no overflow even with tabs present', () => {
    const m = metrics(0, 300, 300)
    const spans: TabSpan[] = [
      { start: 0, end: 100 },
      { start: 100, end: 200 },
      { start: 200, end: 300 },
    ]
    expect(isOverflowing(m)).toBe(false)
    expect(canScrollLeft(m)).toBe(false)
    expect(canScrollRight(m)).toBe(false)
    expect(nextTabLeft(m, spans)).toBe(0)
    expect(prevTabLeft(m, spans)).toBe(0)
  })

  it('ignores sub-epsilon overflow', () => {
    expect(isOverflowing(metrics(0, 300, 300 + EDGE_EPSILON))).toBe(false)
    expect(isOverflowing(metrics(0, 300, 300 + EDGE_EPSILON + 1))).toBe(true)
  })

  it('pins the epsilon contract at 1px', () => {
    expect(EDGE_EPSILON).toBe(1)
  })
})

describe('tabStripScroll — edge directions', () => {
  it('is false at the left extreme (scrollLeft 0)', () => {
    expect(canScrollLeft(metrics(0, clientWidth, scrollWidth))).toBe(false)
    expect(canScrollLeft(metrics(EDGE_EPSILON, clientWidth, scrollWidth))).toBe(false)
    expect(canScrollLeft(metrics(EDGE_EPSILON + 1, clientWidth, scrollWidth))).toBe(true)
  })

  it('is false at the right extreme (scrollLeft max)', () => {
    expect(canScrollRight(metrics(maxScrollLeft, clientWidth, scrollWidth))).toBe(false)
    expect(canScrollRight(metrics(maxScrollLeft - EDGE_EPSILON, clientWidth, scrollWidth))).toBe(
      false,
    )
    expect(canScrollRight(metrics(maxScrollLeft - EDGE_EPSILON - 1, clientWidth, scrollWidth))).toBe(
      true,
    )
  })

  it('is true mid-scroll in both directions', () => {
    const mid = metrics(150, clientWidth, scrollWidth)
    expect(canScrollLeft(mid)).toBe(true)
    expect(canScrollRight(mid)).toBe(true)
  })
})

describe('tabStripScroll — nextTabLeft (right edge)', () => {
  it('reveals the first tab clipped at the right edge', () => {
    // At scrollLeft 0 the viewport [0,100) clips the tab at [90,170);
    // aligning its right edge puts the viewport at [70,170).
    expect(nextTabLeft(metrics(0, clientWidth, scrollWidth), fourTabs)).toBe(70)
  })

  it('picks the smallest clipped end among several candidates', () => {
    // From 70 the viewport [70,170) shows the tab [90,170) fully; the next
    // clipped tab is [180,260), reached at 160.
    expect(nextTabLeft(metrics(70, clientWidth, scrollWidth), fourTabs)).toBe(160)
  })

  it('is order-independent over the span list', () => {
    const shuffled: TabSpan[] = [...fourTabs.slice(2), ...fourTabs.slice(0, 2)]
    expect(nextTabLeft(metrics(0, clientWidth, scrollWidth), shuffled)).toBe(70)
  })

  it('is a no-op when no tab sticks out past the right edge', () => {
    // At scrollLeft 250 every tab end (350 max) is within [250,350).
    expect(nextTabLeft(metrics(250, clientWidth, scrollWidth), fourTabs)).toBe(250)
    expect(nextTabLeft(metrics(150, clientWidth, scrollWidth), [])).toBe(150)
  })
})

describe('tabStripScroll — prevTabLeft (left edge)', () => {
  it('reveals the last tab clipped at the left edge', () => {
    // At scrollLeft 170 the viewport [170,270) clips the tab at [90,170);
    // aligning its left edge puts the viewport at [90,190).
    expect(prevTabLeft(metrics(170, clientWidth, scrollWidth), fourTabs)).toBe(90)
  })

  it('picks the largest clipped start among several candidates', () => {
    expect(prevTabLeft(metrics(100, clientWidth, scrollWidth), fourTabs)).toBe(90)
    expect(prevTabLeft(metrics(300, clientWidth, scrollWidth), fourTabs)).toBe(270)
  })

  it('is order-independent over the span list', () => {
    const shuffled: TabSpan[] = [...fourTabs.slice(2), ...fourTabs.slice(0, 2)]
    expect(prevTabLeft(metrics(170, clientWidth, scrollWidth), shuffled)).toBe(90)
  })

  it('is a no-op when no tab is clipped at the left edge', () => {
    expect(prevTabLeft(metrics(0, clientWidth, scrollWidth), fourTabs)).toBe(0)
    expect(prevTabLeft(metrics(150, clientWidth, scrollWidth), [])).toBe(150)
  })
})

describe('tabStripScroll — a single tab wider than the viewport', () => {
  const wide: TabSpan[] = [{ start: 0, end: 300 }]
  const strip = metrics(0, 100, 300) // max scrollLeft 200

  it('scrolls right to the clamped target (end - clientWidth = max)', () => {
    expect(nextTabLeft(strip, wide)).toBe(200)
    expect(canScrollLeft(metrics(0, 100, 300))).toBe(false)
    expect(canScrollRight(metrics(0, 100, 300))).toBe(true)
  })

  it('scrolls back left to 0', () => {
    const atEnd = metrics(200, 100, 300)
    expect(prevTabLeft(atEnd, wide)).toBe(0)
    expect(canScrollLeft(atEnd)).toBe(true)
    expect(canScrollRight(atEnd)).toBe(false)
  })
})

describe('tabStripScroll — clampScrollLeft', () => {
  it('clamps values beyond both bounds', () => {
    expect(clampScrollLeft(-40, metrics(0, clientWidth, scrollWidth))).toBe(0)
    expect(clampScrollLeft(maxScrollLeft + 100, metrics(0, clientWidth, scrollWidth))).toBe(
      maxScrollLeft,
    )
    expect(clampScrollLeft(123, metrics(0, clientWidth, scrollWidth))).toBe(123)
  })

  it('clamps to 0 on an exact-fit strip', () => {
    expect(clampScrollLeft(50, metrics(0, 300, 300))).toBe(0)
  })

  it('defends against a degenerate strip where scrollWidth < clientWidth', () => {
    expect(clampScrollLeft(50, metrics(0, 300, 200))).toBe(0)
  })
})
