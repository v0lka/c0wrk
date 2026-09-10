/**
 * Sticky-aware chat scrolling.
 *
 * Every user turn's message is rendered as an opaque "floating" bar with
 * `position: sticky; top: 0` INSIDE the chat scroll container (see
 * `UserMessage`). While its turn is in view the bar covers the top
 * ~70-80px of the scrollport (bubble + paddings + gradient fade strip).
 *
 * `Element.scrollIntoView({ block: 'start' })` aligns the target with the
 * scrollport's geometric top and knows nothing about overlays, so the
 * beginning of the target block ends up hidden underneath the floating bar —
 * the chat visibly "lands mid-block". These helpers scroll manually instead,
 * subtracting the height of the floating bar so the block starts right below
 * it.
 */

/** Selector for the floating sticky user-message bar (see `UserMessage`). */
export const STICKY_USER_MESSAGE_SELECTOR = '[data-sticky-user-message]'

/**
 * The floating bar that will overlay the scrollport top once `target` is
 * scrolled to the top: the nearest sticky bar that precedes the target in
 * document order. Because turns are sequential and each bar sits at the start
 * of its turn, that bar belongs to the target's own turn, and its wrapper
 * still intersects the viewport after the scroll — so it is guaranteed to be
 * stuck at `top: 0`, covering the content beneath it.
 *
 * Returns null when the target itself is (or is inside) a sticky bar — e.g. a
 * bookmark on the pinned user message: aligning it to the top is already
 * fully visible because the bar itself sticks there.
 */
export function stickyBarOverlaying(viewport: HTMLElement, target: Element): Element | null {
  // The target itself being a sticky bar (a bookmark anchored ON a pinned
  // user message — UserMessage puts `data-bookmark-id` on the sticky div)
  // needs no overlay compensation: aligned to the top, the bar is already
  // fully visible. This must be decided BEFORE the loop: comparing a bar with
  // itself yields 0 (neither PRECEDING nor FOLLOWING), so the loop would break
  // out early while `nearest` still holds the PREVIOUS turn's bar — returning
  // that sibling would subtract a foreign bar's height (finding [50]).
  if (target.matches(STICKY_USER_MESSAGE_SELECTOR)) return null
  let nearest: Element | null = null
  const bars = viewport.querySelectorAll(STICKY_USER_MESSAGE_SELECTOR)
  for (const bar of Array.from(bars)) {
    // Node.DOCUMENT_POSITION_FOLLOWING: `target` comes after `bar` in document
    // order, i.e. the bar precedes the target.
    if (bar.compareDocumentPosition(target) & Node.DOCUMENT_POSITION_FOLLOWING) {
      nearest = bar
    } else {
      break // bars arrive in document order; none after this precedes the target
    }
  }
  return nearest && nearest.contains(target) ? null : nearest
}

/**
 * Scroll `viewport` so the target block's top edge lands at the top of the
 * scrollport — or just below the floating sticky user-message bar when one
 * will be stuck over that spot. Smooth-animated; clamped to the scroll range
 * start.
 *
 * The bar's height is measured live at call time, so both states of the
 * floating message are handled: the collapsed one-line preview and the
 * expanded full message (sticky positioning only translates the bar, never
 * resizes it, so the current rect equals the post-scroll height).
 */
export function scrollBlockStartIntoView(viewport: HTMLElement, target: Element): void {
  const overlay = stickyBarOverlaying(viewport, target)
  let overlayHeight = overlay ? overlay.getBoundingClientRect().height : 0
  if (viewport.clientHeight > 0) {
    // An expanded pinned message can be taller than the scrollport itself.
    // Cap the offset so at least a sliver of the target stays visible below
    // the oversized bar instead of being pushed fully below the fold.
    overlayHeight = Math.min(overlayHeight, Math.max(0, viewport.clientHeight - MIN_TARGET_VISIBLE_PX))
  }
  const targetTop = target.getBoundingClientRect().top
  const viewportTop = viewport.getBoundingClientRect().top
  const top = Math.max(0, viewport.scrollTop + targetTop - viewportTop - overlayHeight)
  viewport.scrollTo({ top, behavior: 'smooth' })
}

/** Minimum slice of the target block kept on screen when the floating bar is
 *  taller than the scrollport (see `scrollBlockStartIntoView`). */
const MIN_TARGET_VISIBLE_PX = 80
