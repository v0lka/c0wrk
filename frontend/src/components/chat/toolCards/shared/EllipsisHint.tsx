import { useEffect, useRef, useState, type ReactNode } from 'react'
import { Tooltip, TooltipTrigger, TooltipContent } from '@/components/ui/tooltip'
import { cn } from '@/lib/utils'

interface EllipsisHintProps {
  /** Full (untruncated) value revealed in the tooltip on hover. */
  fullText: string
  /** Visible content rendered inside the truncating span. */
  children: ReactNode
  /**
   * Classes for the truncating span. Pass `truncate` (and a size like `text-sm`)
   * to enable single-line ellipsis. `min-w-0` is always applied so the span — a
   * flex item in every context — can shrink below its content size and ellipsize.
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
 * - The trigger span carries `min-w-0` (plus the caller's `truncate` / size). It
 *   is ALWAYS rendered as a flex item — a direct child of the card's flex row
 *   (body-less cards) or, for collapsible cards, a direct child of the
 *   `CollapsibleBlock` trigger (which renders node labels WITHOUT its own
 *   wrapper span). As a flex item, `min-w-0` lets it shrink below its content
 *   size so `truncate`'s `overflow:hidden` + `text-overflow:ellipsis` clip and
 *   ellipsize the text, and `scrollWidth > clientWidth` reliably reports
 *   overflow so the tooltip can be gated on it. It must NOT use `inline-block`:
 *   an atomic inline-block child inside a `text-overflow:ellipsis` container is
 *   clipped but never produces the ellipsis glyph (and its `max-width:100%` does
 *   not force the content to reflow), so the title runs past the chat edge, no
 *   overflow is detected, and the tooltip never appears.
 * - By default the tooltip is gated on overflow; pass `alwaysShow` to reveal the
 *   full value on every hover regardless of overflow.
 */
export function EllipsisHint({ fullText, children, className, alwaysShow = false }: EllipsisHintProps) {
  const ref = useRef<HTMLSpanElement>(null)
  const [overflowing, setOverflowing] = useState(false)

  // Measure on mount, when fullText changes, and on element resize.
  // A deps-less useEffect would read scrollWidth/clientWidth on every render,
  // forcing a layout reflow per instance — with many ToolCards visible this
  // freezes the UI for seconds. ResizeObserver catches container-width changes
  // (panel resize, streaming content growth) without per-render reflow.
  //
  // useEffect (not useLayoutEffect): the ResizeObserver fires asynchronously
  // regardless, so a synchronous post-paint measurement adds an extra forced
  // reflow at mount with no offsetting benefit. The initial paint may show the
  // span without the overflow tooltip for one frame; the overflow gate settles
  // on the first ResizeObserver callback, which is imperceptible.
  useEffect(() => {
    const el = ref.current
    if (!el) return

    const measure = () => setOverflowing(el.scrollWidth - el.clientWidth > 1)
    measure()

    const observer = new ResizeObserver(measure)
    observer.observe(el)

    return () => observer.disconnect()
  }, [fullText])

  const measure = () => {
    const el = ref.current
    if (el) setOverflowing(el.scrollWidth - el.clientWidth > 1)
  }

  const merged = cn('min-w-0', className)

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
