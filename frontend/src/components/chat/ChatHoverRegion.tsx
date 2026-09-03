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
