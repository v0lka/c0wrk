import { useLayoutEffect, useRef, useState, type ReactNode } from 'react'
import { Tooltip, TooltipTrigger, TooltipContent } from '@/components/ui/tooltip'
import { cn } from '@/lib/utils'

interface EllipsisHintProps {
  /** Full (untruncated) value revealed in the tooltip on hover. */
  fullText: string
  /** Visible content rendered inside the truncating span. */
  children: ReactNode
  /**
   * Classes for the truncating span. Pass `truncate` (and a size like `text-sm`)
   * to enable single-line ellipsis. `block` + `min-w-0` are always applied so the
   * span becomes the width-constrained, truncating element.
   */
  className?: string
  /**
   * Reveal the full value on every hover regardless of overflow — e.g. a full
   * file path that is always more informative than the displayed basename.
   * Defaults to `false`, meaning the tooltip only appears when the visible
   * content actually overflows.
   */
  alwaysShow?: boolean
}

/**
 * Single-line ellipsis container that reveals its full value on hover via a
 * viewport-aware, wrapping tooltip.
 *
 * - The tooltip renders through a Radix portal (`document.body`), so it is never
 *   clipped by the chat column's overflow / width.
 * - Its content is capped (`max-w-md`) and wraps long / unbreakable strings
 *   (`break-words`), so very long titles fold instead of running off-screen.
 * - The span is forced to `block` + `min-w-0` so it is always the
 *   width-constrained, truncating element — whether it is a direct flex child
 *   (body-less tool cards) or nested inside another truncating wrapper
 *   (`CollapsibleBlock` headers). Overflow is then detected via
 *   `scrollWidth > clientWidth`.
 * - By default the tooltip is gated on overflow; pass `alwaysShow` to reveal the
 *   full value on every hover regardless of overflow.
 */
export function EllipsisHint({ fullText, children, className, alwaysShow = false }: EllipsisHintProps) {
  const ref = useRef<HTMLSpanElement>(null)
  const [overflowing, setOverflowing] = useState(false)

  // Re-measure after every DOM commit so the overflow gate stays accurate as
  // content or layout changes (e.g. streaming updates that shorten the title).
  // Deliberately no dependency array — we want to measure on every render.
  // setOverflowing is a stable setter and a no-op when the value is unchanged,
  // so this cannot cause a render loop.
  // eslint-disable-next-line react-hooks/exhaustive-deps
  useLayoutEffect(() => {
    const el = ref.current
    if (el) setOverflowing(el.scrollWidth - el.clientWidth > 1)
  })

  const measure = () => {
    const el = ref.current
    if (el) setOverflowing(el.scrollWidth - el.clientWidth > 1)
  }

  const merged = cn('block min-w-0', className)

  // Nothing to reveal: render a plain truncating span, no tooltip wrapper.
  if (!fullText) {
    return <span className={merged}>{children}</span>
  }

  return (
    <Tooltip delayDuration={300}>
      <TooltipTrigger asChild>
        <span ref={ref} className={merged} onMouseEnter={measure} onFocus={measure}>
          {children}
        </span>
      </TooltipTrigger>
      {(alwaysShow || overflowing) && (
        <TooltipContent
          align="start"
          sideOffset={6}
          collisionPadding={16}
          avoidCollisions
          updatePositionStrategy="always"
          className="max-w-md w-max whitespace-normal break-words text-left"
        >
          {fullText}
        </TooltipContent>
      )}
    </Tooltip>
  )
}
