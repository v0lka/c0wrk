import { useLayoutEffect, useRef } from 'react'
import { Markdown } from '@/lib/markdownConfig'
import { cn } from '@/lib/utils'

interface MentionInfoTooltipProps {
  /** Full description of the mentioned capability, as markdown. */
  markdown: string
  /** Called after each content commit with the pane's offset size (layout px). */
  onMeasure?: (width: number, height: number) => void
  /**
   * Diamond anchor X relative to the pane's left edge, layout px — where the
   * arrow points at the pointer (the controller clamps it to the pane).
   * Defaults to the pane's horizontal center.
   */
  arrowX?: number
  /**
   * Which side of the pointer the pane hangs on: 'above' puts the diamond on
   * the pane's bottom edge (pointing down at the pointer), 'below' — on the
   * top edge (pointing up). The controller's flip decision picks it.
   */
  side: 'above' | 'below'
}

/**
 * Markdown description pane for the chat input's /-mention autocomplete rows
 * (subagents, MCP servers, skills).
 *
 * It is mounted by the `cmMentionTooltip` controller into a body-level host
 * element — a sibling of CodeMirror's tooltip container, outside #root — so it
 * needs no React context: styling comes from global utility classes and design
 * tokens. Content always goes through the sanctioned sanitized `Markdown`
 * pipeline (descriptions are workspace/MCP-authored, untrusted-ish input).
 *
 * The chrome mirrors the project's `TooltipContent` (ui/tooltip.tsx — the
 * surface every chat tool-call tooltip renders through): same box, border,
 * shadow, and entry animation, plus its `Arrow` — a rotated square diamond
 * tucked half under the pane's straight edge, pointing at the pointer (bottom
 * edge when the pane hangs above the pointer, top edge when flipped below).
 *
 * The host keeps `pointer-events: none`, so the pane can never steal hover
 * from the completion list underneath. The scroll area's max height derives
 * from the `--ui-vh` primitive (the zoom-safe viewport unit), never a raw
 * `vh`. The diamond lives on the outer box because the scroll area's
 * `overflow-y-auto` would clip it.
 */
export function MentionInfoTooltip({ markdown, onMeasure, arrowX, side }: MentionInfoTooltipProps) {
  const ref = useRef<HTMLDivElement>(null)

  // Measure on EVERY commit (no deps array): the controller's convergence
  // re-render (diamond X / flip side) must re-report the size too, and the
  // very first commit — into the visibility-hidden host — is the one the
  // placement is computed from, so it must never be skipped. Reading layout
  // here is cheap: the pane is small and hover-only.
  useLayoutEffect(() => {
    const el = ref.current
    if (el && onMeasure) onMeasure(el.offsetWidth, el.offsetHeight)
  })

  return (
    <div
      ref={ref}
      role="tooltip"
      className="relative w-max max-w-md rounded-md border border-border bg-background px-3 py-1.5 text-xs text-foreground shadow-[0_4px_12px_var(--color-shadow)] animate-in fade-in-0 zoom-in-95"
    >
      <div className="max-h-[calc(var(--ui-vh)*0.5)] overflow-y-auto custom-scrollbar whitespace-normal break-words text-left">
        <Markdown content={markdown} compact />
      </div>
      <span
        aria-hidden
        className={cn(
          'pointer-events-none absolute z-50 size-2.5 rotate-45 rounded-[2px] bg-background',
          side === 'above' ? 'bottom-[-5px]' : 'top-[-5px]',
        )}
        style={{ left: arrowX === undefined ? '50%' : `${arrowX}px`, transform: 'translateX(-50%) rotate(45deg)' }}
      />
    </div>
  )
}
