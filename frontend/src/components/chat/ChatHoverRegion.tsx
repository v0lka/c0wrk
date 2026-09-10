import { useCallback } from 'react'
import { chatHoverStore } from './chatHoverStore'

/**
 * Wraps the chat message stream and drives the "only one bookmark icon and only
 * one collapse chevron visible at a time" contract (see {@link chatHoverStore}).
 *
 * The reveal is fully programmatic — no CSS `:hover` / `group-hover` /
 * Tailwind opacity. On every `mouseover` the region resolves the NEAREST block
 * under the pointer via `closest('[data-bookmark-id]')` /
 * `closest('[data-chevron-reveal-id]')` and writes both targets to the store in
 * one atomic call, which automatically hides whatever was shown before. Moving
 * to blank space (no matching ancestor) clears the targets; leaving the region
 * (`onMouseLeave`) clears them too.
 *
 * Nesting is handled by `closest`, which always returns the innermost matching
 * ancestor: hovering an inner row activates only that row, hovering a parent
 * header activates only the single parent block.
 *
 * Known trade-off (accepted, finding [28]): reveal is driven ONLY by
 * `mouseover` — there is no re-resolution on `scroll`. When content scrolls
 * under a stationary cursor, the lit star/chevron stays with the previously
 * resolved row until the pointer moves again (any 1px movement self-heals,
 * because the next `mouseover` re-resolves the nearest block). The
 * alternative — a rAF re-resolve loop on scroll — was considered and rejected:
 * it adds a persistent per-frame hit for a cosmetic staleness that the next
 * mouse twitch fixes, and the pointer-driven behavior in the Wails/WebKit
 * webview has not been verified to even mis-resolve without movement. If
 * scroll-driven staleness ever becomes a real user complaint, add a scroll
 * listener that re-runs the same `closest` resolution with the last known
 * pointer coordinates — the store API already supports it.
 */
export function ChatHoverRegion({
  className,
  children,
}: {
  className?: string
  children: React.ReactNode
}) {
  const onMouseOver = useCallback((e: React.MouseEvent<HTMLDivElement>) => {
    const target = e.target as HTMLElement
    const bookmarkEl = target.closest<HTMLElement>('[data-bookmark-id]')
    const chevronEl = target.closest<HTMLElement>('[data-chevron-reveal-id]')
    chatHoverStore.set({
      bookmark: bookmarkEl?.dataset.bookmarkId ?? null,
      chevron: chevronEl?.dataset.chevronRevealId ?? null,
    })
  }, [])

  const onMouseLeave = useCallback(() => {
    chatHoverStore.clear()
  }, [])

  return (
    <div className={className} onMouseOver={onMouseOver} onMouseLeave={onMouseLeave}>
      {children}
    </div>
  )
}
