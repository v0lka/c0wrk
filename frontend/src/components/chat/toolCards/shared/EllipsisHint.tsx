import { useEffect, useRef, useState, type ReactNode } from 'react'
import { cn } from '@/lib/utils'

interface EllipsisHintProps {
  /** Full (untruncated) value revealed via the native `title` on hover. */
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
   * Defaults to `false`, meaning the title only appears when the visible
   * content actually overflows.
   */
  alwaysShow?: boolean
}

/**
 * Single-line ellipsis container that reveals its full value on hover via a
 * native `title` on the truncating span.
 *
 * - Native title instead of a Radix popup: the browser draws it unclipped
 *   outside any DOM overflow, so the chat column cannot clip it and no portal
 *   is needed; the revealed content is identical.
 * - The span carries `min-w-0` (plus the caller's `truncate` / size). It
 *   is ALWAYS rendered as a flex item — a direct child of the card's flex row
 *   (body-less cards) or, for collapsible cards, a direct child of the
 *   `CollapsibleBlock` trigger (which renders node labels WITHOUT its own
 *   wrapper span). As a flex item, `min-w-0` lets it shrink below its content
 *   size so `truncate`'s `overflow:hidden` + `text-overflow:ellipsis` clip and
 *   ellipsize the text, and `scrollWidth > clientWidth` reliably reports
 *   overflow so the title can be gated on it. It must NOT use `inline-block`:
 *   an atomic inline-block child inside a `text-overflow:ellipsis` container is
 *   clipped but never produces the ellipsis glyph (and its `max-width:100%` does
 *   not force the content to reflow), so the visible value runs past the chat
 *   edge, no overflow is detected, and no title is set.
 * - By default the title is gated on overflow; pass `alwaysShow` to reveal the
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
  // span without the overflow title for one frame; the overflow gate settles
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

  // Nothing to reveal: render a plain truncating span without a title.
  if (!fullText) {
    return <span className={merged}>{children}</span>
  }

  return (
    <span
      ref={ref}
      className={merged}
      title={(alwaysShow || overflowing) ? fullText : undefined}
      // Re-measure at hover/focus time (focus may bubble from focusable
      // children) so the gate state behind the title is never stale.
      onMouseEnter={measure}
      onFocus={measure}
    >
      {children}
    </span>
  )
}
