/**
 * Markdown description pane for the chat input's /-mention autocomplete rows.
 *
 * CodeMirror's completion list offers no hover surface of its own (the native
 * `Completion.info` pane tracks only the keyboard-selected option), so this
 * module owns a small controller that shows the full description of a row as
 * rendered markdown — HOVER ONLY, mirroring how the chat's tool-call tooltips
 * behave (`@/components/ui/tooltip`):
 *
 * - a body-level host div (sibling of CM's tooltip container — the popup
 *   itself renders under `tooltips({ parent: document.body })` in
 *   cmChatExtensions.ts) hosts a persistent React root rendering
 *   {@link MentionInfoTooltip};
 * - `mouseover`/`mouseout` delegation on `document` tracks the hovered row;
 *   the pane opens after the UI-wide standard delay (TOOLTIP_DELAY_MS, the
 *   same single source the chat TooltipProvider uses) and hides immediately
 *   when the pointer leaves — keyboard selection never opens it;
 * - the pane's bottom edge sits ABOVE the POINTER two diamond heights over
 *   it, the diamond pointing its lower corner at the cursor, like
 *   `TooltipContent` (side="top", `Arrow`) — flipped below the pointer
 *   (diamond on the pane's top edge) only when the viewport has no room above.
 *   The pointer is the anchor, not the row: rows can span the whole popup
 *   width, and centering on the row's rect puts the pane far sideways from
 *   the cursor;
 * - the pane hides when the popup closes or a click lands outside it.
 *
 * Geometry is zoom-safe per the UI Scale model — the same conversion
 * `useCursorMenuPosition` performs (the fix behind the model-picker placement
 * bug, where viewport-space coordinates written into fixed-position styles
 * displaced the panel by `coordinate × (zoom − 1)`): the pointer anchor
 * arrives in VISUAL px (`MouseEvent.clientX/clientY`) and is divided by the
 * live `getUiZoomFactor()` before use, because `style.left/top` on a
 * `position: fixed` element is measured in LAYOUT px. Pane sizes come from
 * `offsetWidth/offsetHeight` (already LAYOUT px); the extent is
 * `getLayoutViewport()`.
 *
 * The `.cm-tooltip-autocomplete` popup is looked up in `document` — the chat
 * input's autocompletion is the only one in the app (the file viewer editor
 * enables none; the same assumption the index.css popup block documents).
 */

import { ViewPlugin, type EditorView, type ViewUpdate } from '@codemirror/view'
import { EditorState, type Extension } from '@codemirror/state'
import { completionStatus, currentCompletions, type Completion } from '@codemirror/autocomplete'
import { createElement } from 'react'
import { createRoot, type Root } from 'react-dom/client'
import { getUiZoomFactor } from '@/stores/uiScaleStore'
import { TOOLTIP_DELAY_MS } from '@/components/ui/tooltip'
import { MentionInfoTooltip } from '@/components/chat/MentionInfoTooltip'
import { getLayoutViewport, type BoxSize } from './layoutSpace'
import type { CursorAnchor } from './cursorMenuPosition'

/** A `/`-mention completion carrying its full markdown description. */
export type MentionCompletion = Completion & { mentionMarkdown?: string }

/** Gap between the pane's bottom edge and the pointer — two heights of the
 * pane's `size-2.5` diamond (10px each), so the arrow's lower tip has clear
 * air and reads as pointing straight at the cursor. */
export const MENTION_TOOLTIP_GAP_PX = 20

export interface MentionTooltipPlacement {
  left: number
  top: number
  /**
   * Diamond center X relative to the pane's left edge (layout px) — follows
   * the pointer's X but never leaves the pane.
   */
  arrowX: number
  /** Which side of the pointer the pane occupies — and where its diamond sits. */
  side: 'above' | 'below'
}

/**
 * Pure placement math for the pane (LAYOUT px everywhere).
 *
 * The pane's bottom edge sits ABOVE the pointer, two diamond heights over it
 * (MENTION_TOOLTIP_GAP_PX) — the way the chat's `TooltipContent` renders for
 * its trigger (side="top", `Arrow`) — and flips BELOW the pointer (diamond
 * moving to the pane's top edge) only when the viewport has no room above; when neither side fits, the roomier one
 * wins (never make a bad situation worse — the computeCursorMenuPlacement
 * rule). The result is finally clamped inside the viewport with the
 * collisionPadding margin EllipsisHint's tooltips keep (16). Horizontally the
 * pane centers on the pointer (TooltipContent's default align), also clamped.
 * The diamond tracks the pointer's X, pinned inside the pane.
 */
export function computeMentionTooltipPlacement(args: {
  /** The pointer point, LAYOUT px (visual px ÷ zoom). */
  anchor: CursorAnchor
  /** The pane's offset size, layout px. */
  size: BoxSize
  /** The visible window, layout px. */
  viewport: BoxSize
  /** Gap between the pointer and the pane. */
  gap?: number
  /** Margin kept between the pane and the viewport edges. */
  margin?: number
}): MentionTooltipPlacement {
  const gap = args.gap ?? MENTION_TOOLTIP_GAP_PX
  const margin = args.margin ?? 16

  const roomAbove = args.anchor.y - margin
  const roomBelow = args.viewport.height - margin - args.anchor.y
  const side: MentionTooltipPlacement['side'] =
    roomAbove >= args.size.height
      ? 'above'
      : roomBelow >= args.size.height
        ? 'below'
        : roomAbove >= roomBelow
          ? 'above'
          : 'below'

  const rawTop = side === 'above' ? args.anchor.y - gap - args.size.height : args.anchor.y + gap
  const top = Math.min(
    Math.max(rawTop, margin),
    Math.max(margin, args.viewport.height - margin - args.size.height),
  )

  const left = Math.min(
    Math.max(args.anchor.x - args.size.width / 2, margin),
    Math.max(margin, args.viewport.width - margin - args.size.width),
  )

  // Keep the diamond within the pane's straight-edge span (off the corners).
  const arrowX = Math.min(Math.max(args.anchor.x - left, 12), Math.max(12, args.size.width - 12))
  return { left, top, arrowX, side }
}

/**
 * Absolute index of the completion a row renders, recovered from the row's id.
 *
 * CodeMirror's completion list VIRTUALIZES: `createListBox` assigns every row
 * `li.id = <listId>-<absoluteOptionIndex>` and renders only the current window
 * (`rangeAroundSelected`, default maxRenderedOptions 100), so once the list
 * exceeds the window the rendered rows' DOM positions no longer equal their
 * absolute option indices — the window starts at `range.from > 0`. CodeMirror's
 * own row-click handler recovers the index by parsing that id suffix
 * (`/-(\d+)$/.exec(dom.id)`); we mirror it so a hovered row resolves its OWN
 * completion in both the un-virtualized and virtualized cases. Counting DOM
 * siblings (the previous approach) is off by the window offset and would show a
 * different completion's description once more than 100 entries are offered.
 */
function completionIndex(li: Element): number {
  const match = /-(\d+)$/.exec(li.id)
  return match ? Number(match[1]) : -1
}

function markdownFor(state: EditorState, index: number): string | null {
  const completion = currentCompletions(state)[index] as MentionCompletion | undefined
  return completion?.mentionMarkdown ?? null
}

// The pane host is a shared body-level layer created lazily on the first
// editor mount and kept for the app's lifetime — the same pattern as CM's own
// tooltip container (tooltips({ parent }) removes nothing of ours, and the
// chat input remounts across CHAT↔CODE switches must not churn React roots).
// Unmounting a root from a CM destroy path is what makes React report a
// re-entrant unmount (or an out-of-act update); controllers only ever show
// and hide the shared pane. Not an import-time side effect: nothing runs
// until an editor is actually constructed.
let sharedPane: { container: HTMLDivElement; root: Root } | null = null
function getSharedPane(): { container: HTMLDivElement; root: Root } {
  if (!sharedPane) {
    const container = document.createElement('div')
    container.className = 'cm-mention-tooltip-host'
    // Hidden via visibility, NOT display:none — this is the placement-bug fix:
    // a visibility-hidden subtree keeps its layout, so the very first measure
    // reports the pane's real size. A display:none measure reads 0×0, and a
    // 0×0 placement anchors the pane's top-left corner to the pointer — the
    // whole box then hangs down-right of the cursor instead of above it.
    container.style.visibility = 'hidden'
    document.body.appendChild(container)
    sharedPane = { container, root: createRoot(container) }
  }
  return sharedPane
}

class MentionTooltipView {
  private readonly container: HTMLDivElement
  private readonly root: Root
  private showTimer: number | null = null
  private hoverLi: HTMLElement | null = null
  private anchorLi: HTMLElement | null = null
  /** The pointer point at hover time, VISUAL px — the pane's anchor. */
  private cursorVisual: CursorAnchor | null = null
  private shownMarkdown: string | null = null
  private renderedArrowX: number | null = null
  private renderedSide: 'above' | 'below' | null = null
  private paneWidth = 0
  private paneHeight = 0

  constructor(private readonly view: EditorView) {
    const pane = getSharedPane()
    this.container = pane.container
    this.root = pane.root
    document.addEventListener('mouseover', this.onMouseOver)
    document.addEventListener('mouseout', this.onMouseOut)
    document.addEventListener('mousedown', this.onMouseDown, true)
    document.addEventListener('scroll', this.onReflow, true)
    window.addEventListener('resize', this.onReflow)
  }

  update(update: ViewUpdate): void {
    if (!completionStatus(update.state)) {
      this.dismiss()
      return
    }
    if (this.hoverLi && !this.hoverLi.isConnected) {
      // The hovered row left the DOM (query re-filtered, list rebuilt).
      this.dismiss()
    }
  }

  destroy(): void {
    this.cancelShowTimer()
    document.removeEventListener('mouseover', this.onMouseOver)
    document.removeEventListener('mouseout', this.onMouseOut)
    document.removeEventListener('mousedown', this.onMouseDown, true)
    document.removeEventListener('scroll', this.onReflow, true)
    window.removeEventListener('resize', this.onReflow)
    // The shared host layer outlives editor instances (see getSharedPane):
    // destroying an editor only ever hides the pane, never unmounts the root.
    this.dismissPane()
  }

  private cancelShowTimer(): void {
    if (this.showTimer !== null) {
      window.clearTimeout(this.showTimer)
      this.showTimer = null
    }
  }

  /** Forget the hover target and hide the pane. */
  private dismiss(): void {
    this.hoverLi = null
    this.cancelShowTimer()
    this.dismissPane()
  }

  private dismissPane(): void {
    this.container.style.visibility = 'hidden'
    this.anchorLi = null
    this.cursorVisual = null
    this.shownMarkdown = null
    this.renderedArrowX = null
    this.renderedSide = null
  }

  private onMouseOver = (event: MouseEvent): void => {
    const target = event.target
    if (!(target instanceof Element)) return
    const li = target.closest('.cm-tooltip-autocomplete li[id]')
    if (!(li instanceof HTMLElement) || li === this.hoverLi) return
    this.cancelShowTimer()
    const index = completionIndex(li)
    const markdown = index >= 0 ? markdownFor(this.view.state, index) : null
    if (!markdown) {
      // The row carries no description (file items): nothing to show.
      this.dismiss()
      return
    }
    this.hoverLi = li
    // The anchor is the pointer point (VISUAL px), not the row rect — rows can
    // span the whole popup width, and a row-rect anchor puts the pane far
    // sideways from the cursor.
    this.cursorVisual = { x: event.clientX, y: event.clientY }
    // The UI-wide standard tooltip delay (TOOLTIP_DELAY_MS) — the same single
    // source the chat's TooltipProvider gates its tool-call tooltips with.
    this.showTimer = window.setTimeout(() => {
      this.showTimer = null
      if (this.hoverLi === li && li.isConnected) this.renderPane(li, markdown, null, 'above')
    }, TOOLTIP_DELAY_MS)
  }

  private onMouseOut = (event: MouseEvent): void => {
    if (!this.hoverLi) return
    const from = event.target
    const to = event.relatedTarget
    if (!(from instanceof Element)) return
    if (to instanceof Node && (to === this.hoverLi || this.hoverLi.contains(to))) return
    if (from !== this.hoverLi && !this.hoverLi.contains(from)) return
    // The pointer left the hovered row: hide immediately — the chat tooltip
    // has no dwell time on close either, and no other activation exists that
    // the pane could "hand back" to.
    this.dismiss()
  }

  private onMouseDown = (event: MouseEvent): void => {
    const target = event.target
    // Inside the popup, mousedown is a pick or a scrollbar drag; the pick
    // closes the popup and update() dismisses the pane. Outside, hide it
    // right away so a stale pane never outlives its anchor.
    if (target instanceof Element && target.closest('.cm-tooltip-autocomplete')) return
    this.dismissPane()
  }

  private onReflow = (): void => {
    if (this.container.style.visibility === 'visible') this.placePane()
  }

  private onPaneMeasure = (width: number, height: number): void => {
    this.paneWidth = width
    this.paneHeight = height
    this.placePane()
  }

  private renderPane(
    li: HTMLElement,
    markdown: string,
    arrowX: number | null,
    side: 'above' | 'below',
  ): void {
    this.anchorLi = li
    this.shownMarkdown = markdown
    this.renderedArrowX = arrowX
    this.renderedSide = side
    this.root.render(
      createElement(MentionInfoTooltip, {
        markdown,
        onMeasure: this.onPaneMeasure,
        arrowX: arrowX ?? undefined,
        side,
      }),
    )
  }

  private placePane(): void {
    const li = this.anchorLi
    if (!li || !li.isConnected || !this.cursorVisual) {
      this.dismissPane()
      return
    }
    if (!li.closest('.cm-tooltip-autocomplete')) {
      this.dismissPane()
      return
    }
    const placement = computeMentionTooltipPlacement({
      // Pointer coordinates are VISUAL px; the fixed host's left/top is
      // LAYOUT px — the same live-zoom conversion useCursorMenuPosition
      // performs (the model-picker placement fix).
      anchor: {
        x: this.cursorVisual.x / getUiZoomFactor(),
        y: this.cursorVisual.y / getUiZoomFactor(),
      },
      size: { width: this.paneWidth, height: this.paneHeight },
      viewport: getLayoutViewport(),
    })
    this.container.style.left = `${placement.left}px`
    this.container.style.top = `${placement.top}px`
    this.container.style.visibility = 'visible'
    // The measured size can move the diamond's clamped X, and the flip
    // decision can move the diamond's edge: re-render once with both; the
    // pane re-measures on every commit (no effect deps), so the chain
    // converges — and the placement guard below stops it there.
    if (placement.arrowX !== this.renderedArrowX || placement.side !== this.renderedSide) {
      this.renderPane(li, this.shownMarkdown ?? '', placement.arrowX, placement.side)
    }
  }
}

/**
 * The mention description pane as a CodeMirror extension. Mount it next to
 * `autocompletion(...)` (createChatAutocomplete does); it is inert unless a
 * `/`-mention row with a description is hovered for the standard delay.
 */
export function mentionTooltip(): Extension {
  return ViewPlugin.fromClass(MentionTooltipView)
}
