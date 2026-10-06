import { useCallback, useEffect, useRef } from 'react'

/**
 * Ref callback for a Radix `DropdownMenuContent` that scrolls the
 * `[data-selected="true"]` option into view — exactly ONCE per menu-open
 * cycle.
 *
 * Why the open-scoped latch (not a plain attach-time scroll): Radix's
 * `useComposedRefs` wraps our ref in a composed callback that is RE-CREATED
 * on every re-render of the menu content (`useCallback(composeRefs(...refs),
 * refs)` receives a fresh rest array as its deps), so React re-invokes the
 * ref after any unrelated update while the menu is open. Scrolling on every
 * re-invocation made the open list snap back to the selected option while
 * the user scrolled it: scroll-under-cursor hover events re-render the
 * content, each re-render re-attaches the ref, each re-attach re-scrolls —
 * a self-sustaining fight against the user. A stable `useCallback` identity
 * on the consumer side cannot prevent this; the instability lives inside
 * Radix. The latch consumes the first attach after the menu opens (commit
 * time — the portaled content and its items already exist then) and is
 * reset whenever the menu closes.
 *
 * jsdom has no scrollIntoView; guard the call.
 */
export function useSelectedOptionScroll(open: boolean): (node: HTMLDivElement | null) => void {
  const scrolledThisOpen = useRef(false)

  // Consume the per-open latch on close, so the next open scrolls again.
  useEffect(() => {
    if (!open) scrolledThisOpen.current = false
  }, [open])

  return useCallback((node: HTMLDivElement | null) => {
    if (!node || scrolledThisOpen.current) return
    scrolledThisOpen.current = true
    const selectedEl = node.querySelector<HTMLElement>('[data-selected="true"]')
    if (selectedEl && typeof selectedEl.scrollIntoView === 'function') {
      selectedEl.scrollIntoView({ block: 'nearest' })
    }
  }, [])
}
