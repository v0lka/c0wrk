import { useCallback, useEffect, useMemo, useRef, useState, type RefObject } from 'react'
import {
  canScrollLeft as canScrollLeftOf,
  canScrollRight as canScrollRightOf,
  isOverflowing,
  nextTabLeft,
  prevTabLeft,
  type StripMetrics,
  type TabSpan,
} from '@/lib/tabStripScroll'

/**
 * Inputs for {@link useTabStripOverflow}. Neither field is read for its value
 * at measure time — the DOM is — but both tell the hook WHEN the strip's
 * content may have changed shape (tabs added/removed, active-tab styling
 * shifting tab widths) and therefore when a fresh measurement is due.
 */
export interface UseTabStripOverflowOptions {
  /** Ordered tab ids currently in the strip; a changed list re-measures. */
  tabIds: readonly string[]
  /** The active tab id; a change re-measures (active styling can shift widths). */
  activeTabId?: string
}

/** Scroll affordances for the workspace tab strip. */
export interface UseTabStripOverflowReturn {
  /** Attach to the element that clips the tab strip (the scroll container). */
  scrollerRef: RefObject<HTMLDivElement | null>
  /** True when hidden content extends meaningfully past the visible width. */
  overflowing: boolean
  /** True when hidden content remains to the left of the viewport. */
  canScrollLeft: boolean
  /** True when hidden content remains to the right of the viewport. */
  canScrollRight: boolean
  /** Scroll so the tab clipped past the right edge is fully revealed. */
  scrollNext: () => void
  /** Scroll so the tab clipped at the left edge is fully revealed. */
  scrollPrev: () => void
  /** Scroll the tab with this id into view; no-op when absent or unsupported. */
  revealTab: (tabId: string) => void
}

interface StripEdges {
  overflowing: boolean
  canScrollLeft: boolean
  canScrollRight: boolean
}

/** A strip that fits its viewport: no overflow, nothing hidden on either side. */
const FITTING: StripEdges = { overflowing: false, canScrollLeft: false, canScrollRight: false }

function readMetrics(scroller: HTMLElement): StripMetrics {
  return {
    scrollLeft: scroller.scrollLeft,
    clientWidth: scroller.clientWidth,
    scrollWidth: scroller.scrollWidth,
  }
}

/** Tab extents in the scroller's scroll coordinate space, in DOM order. */
function readTabSpans(scroller: HTMLElement): TabSpan[] {
  const rows = scroller.querySelectorAll<HTMLElement>('[role="tab"]')
  const spans: TabSpan[] = []
  for (const row of rows) {
    spans.push({ start: row.offsetLeft, end: row.offsetLeft + row.offsetWidth })
  }
  return spans
}

function edgesOf(scroller: HTMLElement): StripEdges {
  const metrics = readMetrics(scroller)
  return {
    overflowing: isOverflowing(metrics),
    canScrollLeft: canScrollLeftOf(metrics),
    canScrollRight: canScrollRightOf(metrics),
  }
}

function sameEdges(a: StripEdges, b: StripEdges): boolean {
  return (
    a.overflowing === b.overflowing && a.canScrollLeft === b.canScrollLeft && a.canScrollRight === b.canScrollRight
  )
}

/**
 * Measures the workspace tab strip and exposes scroll affordances for it,
 * converting live DOM geometry through the pure helpers in
 * `lib/tabStripScroll`.
 *
 * Re-measurement happens on mount, when the tab list or the active tab id
 * changes, on `scroll` (a passive listener — scrolling only moves the two
 * edge flags), and on resizes of BOTH the scroller and the tablist via one
 * ResizeObserver (guarded: environments without the constructor, like jsdom
 * without the polyfill, skip it instead of throwing). Identical measurements
 * bail out of the state update, so a resize storm costs no re-renders when
 * nothing actually moved.
 */
export function useTabStripOverflow({ tabIds, activeTabId }: UseTabStripOverflowOptions): UseTabStripOverflowReturn {
  const scrollerRef = useRef<HTMLDivElement | null>(null)
  const [edges, setEdges] = useState<StripEdges>(FITTING)

  const measure = useCallback(() => {
    const scroller = scrollerRef.current
    if (!scroller) return
    const next = edgesOf(scroller)
    setEdges((prev) => (sameEdges(prev, next) ? prev : next))
  }, [])

  // Collapse the id list to a primitive so the effects below do not re-run on
  // every caller render that re-creates the array. Tab ids are engine-generated
  // and never contain a newline, so the join is unambiguous.
  const tabListKey = tabIds.join('\n')

  // Mount + tab list / active tab changes.
  useEffect(() => {
    measure()
  }, [measure, tabListKey, activeTabId])

  // Resizes: the scroller (the visible viewport) and the tablist (tab widths
  // react to labels, task-status dots and the active style) can change the
  // geometry independently, so both are observed. The tablist is re-resolved
  // when the tab list changes, in case its container remounts with the list.
  useEffect(() => {
    const scroller = scrollerRef.current
    if (!scroller || typeof ResizeObserver === 'undefined') return undefined
    const tablist = scroller.querySelector<HTMLElement>('[role="tablist"]')
    const observer = new ResizeObserver(() => measure())
    observer.observe(scroller)
    if (tablist) observer.observe(tablist)
    return () => {
      observer.disconnect()
    }
  }, [measure, tabListKey])

  // Passive scroll listener: never blocks the scroll itself.
  useEffect(() => {
    const scroller = scrollerRef.current
    if (!scroller) return undefined
    const onScroll = () => {
      measure()
    }
    scroller.addEventListener('scroll', onScroll, { passive: true })
    return () => {
      scroller.removeEventListener('scroll', onScroll)
    }
  }, [measure])

  const scrollNext = useCallback(() => {
    const scroller = scrollerRef.current
    if (!scroller) return
    const metrics = readMetrics(scroller)
    scroller.scrollTo({ left: nextTabLeft(metrics, readTabSpans(scroller)), behavior: 'instant' })
  }, [])

  const scrollPrev = useCallback(() => {
    const scroller = scrollerRef.current
    if (!scroller) return
    const metrics = readMetrics(scroller)
    scroller.scrollTo({ left: prevTabLeft(metrics, readTabSpans(scroller)), behavior: 'instant' })
  }, [])

  const revealTab = useCallback((tabId: string) => {
    const scroller = scrollerRef.current
    if (!scroller) return
    const rows = scroller.querySelectorAll<HTMLElement>('[role="tab"]')
    for (const row of rows) {
      if (row.dataset.tabId !== tabId) continue
      // jsdom (and some embedded webviews) ship no scrollIntoView; skip rather
      // than throw — the caller has no recovery either way.
      if (typeof row.scrollIntoView === 'function') {
        row.scrollIntoView({ block: 'nearest', inline: 'nearest' })
      }
      return
    }
  }, [])

  return useMemo(
    () => ({
      scrollerRef,
      overflowing: edges.overflowing,
      canScrollLeft: edges.canScrollLeft,
      canScrollRight: edges.canScrollRight,
      scrollNext,
      scrollPrev,
      revealTab,
    }),
    [edges, scrollNext, scrollPrev, revealTab],
  )
}
