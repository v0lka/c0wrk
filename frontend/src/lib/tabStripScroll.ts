/**
 * Pure scroll geometry for the workspace tab strip: overflow state and the
 * one-tab scroll target that fully reveals a tab clipped at either edge.
 * No DOM, store, or React dependency — callers measure the strip element and
 * pass numbers in.
 */

/** Distances within this many px of an edge count as "at the edge" (absorbs fractional px widths). */
export const EDGE_EPSILON = 1

/** The strip's scroll geometry as measured on its scroll container element. */
export interface StripMetrics {
  scrollLeft: number
  clientWidth: number
  scrollWidth: number
}

/** A tab's horizontal extent in the strip's scroll coordinate space. */
export interface TabSpan {
  start: number
  end: number
}

/** True when hidden content extends meaningfully (beyond {@link EDGE_EPSILON}) past the visible width. */
export function isOverflowing(m: StripMetrics): boolean {
  return m.scrollWidth > m.clientWidth + EDGE_EPSILON
}

/** True when hidden content remains to the left of the viewport. */
export function canScrollLeft(m: StripMetrics): boolean {
  return m.scrollLeft > EDGE_EPSILON
}

/** True when hidden content remains to the right of the viewport. */
export function canScrollRight(m: StripMetrics): boolean {
  return m.scrollWidth - m.scrollLeft - m.clientWidth > EDGE_EPSILON
}

/**
 * scrollLeft that fully reveals the first tab sticking out past the right
 * edge: the smallest `end` beyond `scrollLeft + clientWidth`, aligned with
 * the viewport's right edge. When no tab is clipped there (including an
 * empty span list), the current scrollLeft is returned unchanged.
 */
export function nextTabLeft(m: StripMetrics, spans: readonly TabSpan[]): number {
  let best: number | undefined
  for (const s of spans) {
    if (s.end > m.scrollLeft + m.clientWidth && (best === undefined || s.end < best)) {
      best = s.end
    }
  }
  if (best === undefined) {
    return clampScrollLeft(m.scrollLeft, m)
  }
  return clampScrollLeft(best - m.clientWidth, m)
}

/**
 * scrollLeft that fully reveals the last tab clipped at the left edge: the
 * largest `start` before `scrollLeft`, aligned with the viewport's left
 * edge. When no tab is clipped there (including an empty span list), the
 * current scrollLeft is returned unchanged.
 */
export function prevTabLeft(m: StripMetrics, spans: readonly TabSpan[]): number {
  let best: number | undefined
  for (const s of spans) {
    if (s.start < m.scrollLeft && (best === undefined || s.start > best)) {
      best = s.start
    }
  }
  if (best === undefined) {
    return clampScrollLeft(m.scrollLeft, m)
  }
  return clampScrollLeft(best, m)
}

/** Clamp a scrollLeft into the strip's valid range `[0, scrollWidth - clientWidth]`. */
export function clampScrollLeft(left: number, m: StripMetrics): number {
  const max = Math.max(0, m.scrollWidth - m.clientWidth)
  return Math.min(Math.max(left, 0), max)
}
