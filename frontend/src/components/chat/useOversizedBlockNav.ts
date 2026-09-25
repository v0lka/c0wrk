import { useCallback, useEffect, useMemo, useRef, useState } from 'react'
import type { RefObject } from 'react'

import { collapsibleRegistry } from './collapsibleRegistry'
import { scrollBlockStartIntoView } from '@/lib/chatScroll'

/**
 * Navigation state for the chat transcript's expandable (collapsible) blocks,
 * focused on the OVERSIZED expanded ones — open blocks whose height exceeds
 * the scrollport and therefore cannot be seen in one screen.
 *
 * The hook derives everything from the viewport; it never mutates a block's
 * open state except through {@link OversizedBlockNavApi.collapse}, which only
 * ever collapses (never expands) via {@link collapsibleRegistry}.
 */

/** Every expandable block (Radix root carries the reveal id, open or closed). */
const EXPANDABLE_BLOCK_SELECTOR = '[data-chevron-reveal-id]'

/** The currently-expanded subset (Radix toggles `data-state` on the root). */
const OPEN_BLOCK_SELECTOR = "[data-chevron-reveal-id][data-state='open']"

// Same rationale as ChatScrollManager's NAVIGATION_AUTO_SCROLL_SUPPRESS_MS:
// during a smooth navigation's first frames the derived state still describes
// the pre-scroll geometry, so anything reacting to scroll events mid-flight
// would fight the navigation. The window covers the animation's start; after
// it expires, scans re-derive from the settled viewport.
const NAVIGATION_SUPPRESS_MS = 500

export interface OversizedBlockNavOptions {
  /**
   * The scroll manager's mutable at-bottom flag. A navigation moves the
   * viewport off the bottom by intent, so it writes `false` — exactly what
   * ChatScrollManager's own step/bookmark navigation does — otherwise a
   * streaming `assistant_chunk` would yank the viewport straight back down.
   */
  isAtBottomRef?: RefObject<boolean>
  /**
   * The scroll manager's mutable auto-scroll suppression deadline. A
   * navigation extends it by {@link NAVIGATION_SUPPRESS_MS} so stick-to-bottom
   * logic holds off while the smooth scroll settles.
   */
  suppressAutoScrollUntilRef?: RefObject<number>
}

export interface OversizedBlockNavApi {
  /**
   * Reveal id of the CURRENT oversized block: the topmost (document order)
   * block that is expanded, taller than the viewport, and intersecting it.
   * Null when no such block is on screen — {@link collapse} is a no-op then.
   */
  readonly activeRevealId: string | null
  /** A previous navigation target exists (see {@link OversizedBlockNavApi.goPrev}). */
  readonly hasPrev: boolean
  /** A next navigation target exists (see {@link OversizedBlockNavApi.goNext}). */
  readonly hasNext: boolean
  /**
   * Collapse the current oversized block via {@link collapsibleRegistry}.
   * Only ever collapses — this hook never expands a target. The shrink is
   * picked up by the content ResizeObserver, which rescans and moves
   * `activeRevealId` to the next oversized block (or null).
   */
  collapse: () => void
  /**
   * Scroll the previous expandable block into view. Targets are NEVER
   * expanded — a collapsed target stays collapsed; only its position moves.
   */
  goPrev: () => void
  /** Scroll the next expandable block into view (never expands the target). */
  goNext: () => void
  /** Scroll the first expandable block into view (never expands the target). */
  goFirst: () => void
  /** Scroll the last expandable block into view (never expands the target). */
  goLast: () => void
}

interface BlockEntry {
  el: Element
  id: string
}

/** Enumerate the expandable blocks in document (= visual vertical) order. */
function enumerateBlocks(viewport: HTMLElement): BlockEntry[] {
  const out: BlockEntry[] = []
  for (const el of Array.from(viewport.querySelectorAll(EXPANDABLE_BLOCK_SELECTOR))) {
    const id = el.getAttribute('data-chevron-reveal-id')
    if (id) out.push({ el, id })
  }
  return out
}

/**
 * Scroll geometry contract (see specs/domains/frontend/ui-scale.md):
 * `offsetHeight`/`clientHeight` are LAYOUT px — unaffected by the ancestor
 * CSS zoom — so comparing them needs no conversion, while
 * `getBoundingClientRect()` returns VISUAL px. Intersection is therefore
 * checked rect-vs-rect (both VISUAL, same space) instead of mixing a layout
 * offset into a visual rect.
 */
function intersectsViewport(el: Element, vpTop: number, vpBottom: number): boolean {
  const rect = el.getBoundingClientRect()
  return rect.bottom > vpTop && rect.top < vpBottom
}

export function useOversizedBlockNav(
  viewportRef: RefObject<HTMLElement | null>,
  options?: OversizedBlockNavOptions,
): OversizedBlockNavApi {
  const [activeRevealId, setActiveRevealId] = useState<string | null>(null)
  const [hasPrev, setHasPrev] = useState(false)
  const [hasNext, setHasNext] = useState(false)

  // Mirror of `activeRevealId` for the []-deps callbacks (collapse()).
  const activeRevealIdRef = useRef<string | null>(null)
  // The navigation anchor: id of the block the prev/next steps move from.
  // Derived by scan() from the viewport, except during the navigation window
  // where goPrev/goNext/goFirst/goLast pin it to the target they scrolled to.
  const anchorIdRef = useRef<string | null>(null)
  const anchorFreezeUntilRef = useRef(0)
  const rafRef = useRef<number | null>(null)
  // Options are refs-owned by the caller (ChatScrollManager); keep the latest
  // without re-creating the navigation callbacks.
  const optionsRef = useRef(options)
  optionsRef.current = options

  const scan = useCallback(() => {
    const viewport = viewportRef.current
    if (!viewport) return

    const vpHeight = viewport.clientHeight
    const vpRect = viewport.getBoundingClientRect()
    const vpTop = vpRect.top
    const vpBottom = vpRect.bottom

    // Current oversized block: the TOPMOST expanded block that is taller than
    // the viewport and intersecting it. querySelectorAll yields document
    // order, so the first surviving match is the topmost — break there.
    let nextActive: string | null = null
    for (const el of Array.from(viewport.querySelectorAll(OPEN_BLOCK_SELECTOR))) {
      const id = el.getAttribute('data-chevron-reveal-id')
      if (!id) continue
      // `offsetHeight` is an HTMLElement property (LAYOUT px); the Radix root
      // renders as a div, so non-HTMLElement hits cannot occur in practice.
      if (!(el instanceof HTMLElement)) continue
      if (el.offsetHeight > vpHeight && intersectsViewport(el, vpTop, vpBottom)) {
        nextActive = id
        break
      }
    }

    // Navigation anchor: the topmost expandable block intersecting the
    // viewport. When none does (the viewport sits on plain text between
    // blocks), prev/next fall back to geometry: is there any block entirely
    // above / entirely below the viewport? The above/below flags are only
    // consulted in that no-anchor case — and then the loop below never broke
    // early, so every block was classified and both flags are complete.
    let nextAnchorId: string | null = null
    let aboveExists = false
    let belowExists = false
    const blocks = enumerateBlocks(viewport)
    for (const { el, id } of blocks) {
      const rect = el.getBoundingClientRect()
      if (rect.bottom > vpTop && rect.top < vpBottom) {
        nextAnchorId = id
        break
      }
      if (rect.bottom <= vpTop) aboveExists = true
      else belowExists = true
    }

    // Respect the navigation window: while a programmatic smooth scroll is
    // settling, the scroll events it fires must not re-anchor to the
    // transient blocks crossing the viewport top — prev/next would jump to
    // inconsistent targets mid-flight. The anchor stays pinned to the last
    // navigation target until the window expires.
    if (Date.now() >= anchorFreezeUntilRef.current) {
      anchorIdRef.current = nextAnchorId
    }

    // Resolve the (possibly frozen) anchor against the CURRENT block list —
    // blocks mount/unmount between scans, so the stored id may be stale.
    const anchorId = anchorIdRef.current
    const anchorIdx = anchorId === null
      ? -1
      : blocks.findIndex(b => b.id === anchorId)

    const nextHasPrev = anchorIdx !== -1 ? anchorIdx > 0 : aboveExists
    const nextHasNext = anchorIdx !== -1 ? anchorIdx < blocks.length - 1 : belowExists

    // Identity-guarded writes: scans run per animation frame on scroll; only
    // real changes should re-render the consumer.
    setActiveRevealId(prev => (prev === nextActive ? prev : nextActive))
    setHasPrev(prev => (prev === nextHasPrev ? prev : nextHasPrev))
    setHasNext(prev => (prev === nextHasNext ? prev : nextHasNext))
    activeRevealIdRef.current = nextActive
  }, [viewportRef])

  // Shared navigation core: sticky-bar-aware scroll to the target's start,
  // plus the same auto-scroll semantics ChatScrollManager applies to its
  // step/bookmark navigation — leave the bottom (isAtBottomRef=false) and
  // suppress stick-to-bottom for the suppression window.
  const navigateTo = useCallback((target: BlockEntry) => {
    const viewport = viewportRef.current
    if (!viewport) return
    scrollBlockStartIntoView(viewport, target.el)
    anchorIdRef.current = target.id
    anchorFreezeUntilRef.current = Date.now() + NAVIGATION_SUPPRESS_MS
    const opts = optionsRef.current
    if (opts?.isAtBottomRef) opts.isAtBottomRef.current = false
    if (opts?.suppressAutoScrollUntilRef) {
      opts.suppressAutoScrollUntilRef.current = Date.now() + NAVIGATION_SUPPRESS_MS
    }
    // Rescan immediately so hasPrev/hasNext reflect the new anchor (the
    // anchor is frozen, so mid-flight geometry cannot distort it).
    scan()
  }, [viewportRef, scan])

  const goNext = useCallback(() => {
    const viewport = viewportRef.current
    if (!viewport) return
    const blocks = enumerateBlocks(viewport)
    if (blocks.length === 0) return
    const anchorId = anchorIdRef.current
    const idx = anchorId === null ? -1 : blocks.findIndex(b => b.id === anchorId)
    let target: BlockEntry | undefined
    if (idx !== -1) {
      target = idx < blocks.length - 1 ? blocks[idx + 1] : undefined
    } else {
      // No anchor (nothing intersects): advance to the first block entirely
      // below the viewport.
      const vpBottom = viewport.getBoundingClientRect().bottom
      target = blocks.find(b => b.el.getBoundingClientRect().top >= vpBottom)
    }
    if (target) navigateTo(target)
  }, [viewportRef, navigateTo])

  const goPrev = useCallback(() => {
    const viewport = viewportRef.current
    if (!viewport) return
    const blocks = enumerateBlocks(viewport)
    if (blocks.length === 0) return
    const anchorId = anchorIdRef.current
    const idx = anchorId === null ? -1 : blocks.findIndex(b => b.id === anchorId)
    let target: BlockEntry | undefined
    if (idx !== -1) {
      target = idx > 0 ? blocks[idx - 1] : undefined
    } else {
      // No anchor: step back to the LAST block entirely above the viewport
      // (the closest one above). Array.prototype.findLast is ES2023; the
      // tsconfig lib is ES2020 — walk from the end instead.
      const vpTop = viewport.getBoundingClientRect().top
      for (let i = blocks.length - 1; i >= 0; i--) {
        const block = blocks[i]!
        if (block.el.getBoundingClientRect().bottom <= vpTop) {
          target = block
          break
        }
      }
    }
    if (target) navigateTo(target)
  }, [viewportRef, navigateTo])

  const goFirst = useCallback(() => {
    const viewport = viewportRef.current
    if (!viewport) return
    const first = enumerateBlocks(viewport)[0]
    if (first) navigateTo(first)
  }, [viewportRef, navigateTo])

  const goLast = useCallback(() => {
    const viewport = viewportRef.current
    if (!viewport) return
    const blocks = enumerateBlocks(viewport)
    const last = blocks.length > 0 ? blocks[blocks.length - 1] : undefined
    if (last) navigateTo(last)
  }, [viewportRef, navigateTo])

  const collapse = useCallback(() => {
    const id = activeRevealIdRef.current
    if (!id) return
    // Collapse-only by contract: the hook NEVER expands a target.
    collapsibleRegistry.get(id)?.(false)
  }, [])

  // Rescan triggers: scroll (rAF-throttled — one scan per frame regardless of
  // how many scroll events fire inside it), content resizes (a block
  // expanding/collapsing changes the transcript content height — observe the
  // content wrapper, the same element ChatScrollManager observes), and window
  // resizes (viewport height feeds the oversized comparison).
  useEffect(() => {
    const viewport = viewportRef.current
    if (!viewport) return

    const scheduleScan = () => {
      if (rafRef.current !== null) return
      rafRef.current = requestAnimationFrame(() => {
        rafRef.current = null
        scan()
      })
    }

    viewport.addEventListener('scroll', scheduleScan, { passive: true })
    window.addEventListener('resize', scheduleScan)

    let observer: ResizeObserver | null = null
    const content = viewport.firstElementChild
    if (content && typeof ResizeObserver !== 'undefined') {
      observer = new ResizeObserver(scheduleScan)
      observer.observe(content)
    }

    // Initial derivation for the freshly mounted viewport.
    scan()

    return () => {
      viewport.removeEventListener('scroll', scheduleScan)
      window.removeEventListener('resize', scheduleScan)
      observer?.disconnect()
      if (rafRef.current !== null) {
        cancelAnimationFrame(rafRef.current)
        rafRef.current = null
      }
    }
  }, [viewportRef, scan])

  return useMemo<OversizedBlockNavApi>(() => ({
    activeRevealId,
    hasPrev,
    hasNext,
    collapse,
    goPrev,
    goNext,
    goFirst,
    goLast,
  }), [activeRevealId, hasPrev, hasNext, collapse, goPrev, goNext, goFirst, goLast])
}
