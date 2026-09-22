import { useLayoutEffect, useRef, useState, type CSSProperties, type RefObject } from 'react'
import { createPortal } from 'react-dom'
import { CheckCircle2, FileTerminal, XCircle } from 'lucide-react'
import { cn } from '@/lib/utils'
import { computeDropdownPosition } from '@/lib/dropdownPosition'
import { getLayoutViewport, toLayoutTriggerRect } from '@/lib/layoutSpace'
import type { GitOperationRecord } from '@/stores/gitPanelStore'

/** Panel height (layout px) used until the real content has been measured. */
const FALLBACK_PANEL_HEIGHT = 320
/** Upper bound on the panel height (layout px) so it never dominates the screen. */
const MAX_PANEL_HEIGHT = 360
/** Smallest usable panel height (layout px). */
const MIN_PANEL_HEIGHT = 80
/** Resting panel width (layout px) — the same visual size as the former `w-96`. */
const MIN_PANEL_WIDTH = 384
/** Widest the panel may be, so a very narrow window still fits it (layout px). */
const MIN_VIEWPORT_WIDTH = 200
/** Gap between the trigger and the panel (layout px). */
const GAP = 6
/** Margin kept between the panel and the viewport edges (layout px). */
const MARGIN = 8
const Z_INDEX = 50

export interface GitOperationPopoverProps {
  /** The active project's last operation record, or undefined when none. */
  record: GitOperationRecord | undefined
  /** Document-unique id the trigger references via `aria-controls`. */
  id: string
  /** The trigger button the panel is anchored to (measured for placement). */
  triggerRef: RefObject<HTMLButtonElement | null>
  /** The panel's own node — owned by the footer's dropdown hook (outside-click). */
  panelRef: RefObject<HTMLDivElement | null>
}

/** Header text: the operation's label, falling back to its kind. */
function headline(record: GitOperationRecord | undefined): string {
  if (record === undefined) return 'No git operations yet'
  return record.label.trim() || record.kind
}

/**
 * Body text: the captured stdout+stderr, or the failure message when the
 * operation produced no output (a failed spawn records an empty `output` and
 * the message in `error`), or a placeholder when there is nothing to show.
 */
function capturedText(record: GitOperationRecord | undefined): string {
  if (record === undefined) return 'No output'
  const captured = record.output.trim().length > 0 ? record.output : (record.error ?? '')
  return captured.trim().length > 0 ? captured : 'No output'
}

/**
 * Anchored log panel for the last git operation: a status header (icon + the
 * operation label tinted green on success / red on failure) above a
 * scrollable, pre-wrapped `<pre>` of the captured output.
 *
 * Rendered through a React portal to `document.body` with `position: fixed`,
 * so it is never clipped by the Git panel's `overflow-hidden` ancestor — and,
 * crucially, never makes that ancestor SCROLL: an in-place panel wider than the
 * (resizable, 180–500px) sidebar overflowed it, and focusing the panel scrolled
 * the clipped container, shifting the whole Git panel sideways. Placement is
 * computed from the trigger's rect in LAYOUT px (`toLayoutTriggerRect` +
 * `getLayoutViewport`) via `computeDropdownPosition`, which opens it upward and
 * left-aligned to the trigger — i.e. up and to the right — then clamps it to
 * the visible window (mirrors `BudgetCombobox`; see [ui-scale.md]). Purely
 * presentational — the footer owns open state and dismissal.
 */
export function GitOperationPopover({
  record,
  id,
  triggerRef,
  panelRef,
}: GitOperationPopoverProps) {
  const tone =
    record === undefined
      ? 'text-muted-foreground'
      : record.ok
        ? 'text-success'
        : 'text-destructive'

  const [style, setStyle] = useState<CSSProperties | null>(null)
  // Focus the panel once, as soon as it is positioned (a `visibility: hidden`
  // node cannot take focus, so this must wait for the first placement). The
  // `preventScroll` guard keeps the focus from scrolling any ancestor.
  const focusedRef = useRef(false)

  // Position the portaled panel whenever it mounts, the record changes (the
  // content height may change), or the window resizes / anything scrolls.
  useLayoutEffect(() => {
    const recompute = () => {
      const trigger = triggerRef.current
      if (!trigger) return
      const rect = trigger.getBoundingClientRect()
      const trigger4 = toLayoutTriggerRect({
        top: rect.top,
        bottom: rect.bottom,
        left: rect.left,
        width: rect.width,
      })
      const viewport = getLayoutViewport()
      const panel = panelRef.current
      const naturalHeight =
        panel && panel.offsetHeight > 0 ? panel.offsetHeight : FALLBACK_PANEL_HEIGHT
      // Never taller than the room above the trigger (so the panel hugs it),
      // nor the window itself.
      const maxHeight = Math.max(
        MIN_PANEL_HEIGHT,
        Math.min(
          MAX_PANEL_HEIGHT,
          trigger4.top - MARGIN - GAP,
          viewport.height - 2 * MARGIN,
        ),
      )
      const dropdownHeight = Math.min(naturalHeight, maxHeight)
      const position = computeDropdownPosition({
        triggerRect: trigger4,
        dropdownHeight,
        viewportHeight: viewport.height,
        viewportWidth: viewport.width,
        gap: GAP,
        minWidth: MIN_PANEL_WIDTH,
        margin: MARGIN,
      })
      setStyle({
        position: 'fixed',
        top: position.top,
        left: position.left,
        width: position.width,
        maxWidth: Math.max(MIN_VIEWPORT_WIDTH, viewport.width - 2 * MARGIN),
        maxHeight,
        zIndex: Z_INDEX,
        visibility: 'visible',
      })
    }

    recompute()
    window.addEventListener('resize', recompute)
    window.addEventListener('scroll', recompute, true)
    return () => {
      window.removeEventListener('resize', recompute)
      window.removeEventListener('scroll', recompute, true)
    }
  }, [record, triggerRef, panelRef])

  useLayoutEffect(() => {
    if (!style || focusedRef.current) return
    focusedRef.current = true
    panelRef.current?.focus({ preventScroll: true })
  }, [style, panelRef])

  return createPortal(
    <div
      id={id}
      ref={panelRef}
      tabIndex={-1}
      role="dialog"
      aria-modal="false"
      aria-label="Git operation log"
      className="flex flex-col overflow-hidden rounded-md border border-border bg-background text-foreground shadow-md outline-none"
      style={
        style ?? {
          position: 'fixed',
          top: 0,
          left: 0,
          width: MIN_PANEL_WIDTH,
          zIndex: Z_INDEX,
          visibility: 'hidden',
        }
      }
    >
      <div className="flex shrink-0 items-center gap-1.5 border-b border-border px-2 py-1.5">
        {record === undefined ? (
          <FileTerminal className={cn('size-3.5 shrink-0', tone)} />
        ) : record.ok ? (
          <CheckCircle2 className={cn('size-3.5 shrink-0', tone)} />
        ) : (
          <XCircle className={cn('size-3.5 shrink-0', tone)} />
        )}
        <span className={cn('truncate text-xs font-medium', tone)}>{headline(record)}</span>
      </div>
      <pre className="custom-scrollbar min-h-0 flex-1 overflow-auto whitespace-pre-wrap break-words p-2 font-mono text-[10px] leading-tight text-muted-foreground">
        {capturedText(record)}
      </pre>
    </div>,
    document.body,
  )
}
