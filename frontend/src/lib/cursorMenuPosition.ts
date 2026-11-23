/**
 * Placement of a pointer-anchored floating panel (context menu, cursor menu).
 *
 * Two problems are solved here, both caused by the app-wide UI scale being
 * applied as CSS `zoom` on `<html>`:
 *
 * 1. **Unit mismatch.** `MouseEvent.clientX/clientY` reports VISUAL px
 *    (layout px × zoom), while `style.left/top` on a `position: fixed`
 *    element — and `offsetWidth/offsetHeight` — are LAYOUT px. Writing a
 *    pointer coordinate straight into `style.left` therefore placed the panel
 *    at `coordinate × zoom`: away from the cursor by `coordinate × (zoom − 1)`,
 *    growing with the distance from the window origin (verified in headless
 *    Chromium — at zoom 1.5 `left: 300px` renders at visual x = 450 while the
 *    cursor was at 300). The anchor is converted to layout px here, so the
 *    panel lands exactly under the cursor at any scale and is byte-identical
 *    to the previous behaviour at 100%.
 *
 * 2. **No viewport check.** The panel is opened with its top-left corner at
 *    the pointer, so near the right/bottom edge it used to run off-screen.
 *    {@link computeCursorMenuPlacement} measures it against the visible window
 *    and opens it to the left of / above the cursor when that is the side with
 *    room, then clamps as a last resort so the panel stays fully visible.
 *
 * Measured coordinate model (headless Chromium, `html { zoom: Z }`, window
 * 756 × 469) — established for the manual trigger-anchored placement layer
 * this file originally accompanied (since replaced by the Radix primitives of
 * `components/ui/popover` / `components/ui/dropdown-menu`); the visual→layout
 * conversion the pointer-anchored path still needs lives here:
 *
 * | quantity                                              | space      |
 * | ----------------------------------------------------- | ---------- |
 * | `MouseEvent.clientX/clientY`                          | VISUAL px  |
 * | `Element.getBoundingClientRect()`                     | VISUAL px  |
 * | `window.innerWidth/innerHeight`                       | VISUAL px  |
 * | `document.documentElement.clientWidth/clientHeight`   | VISUAL px  |
 * | `style.left/top` on a `position: fixed` element       | LAYOUT px  |
 * | `Element.offsetWidth/offsetHeight`                    | LAYOUT px  |
 * | `transform: translate3d(Npx, …)`                      | LAYOUT px  |
 * | `100vw`/`100vh` (resolve to the ICB = unzoomed size)  | LAYOUT px  |
 * | `100%` on a fixed element (containing block = 1/Z box) | LAYOUT px |
 *
 * VISUAL px = LAYOUT px × Z. At Z = 1.5 a `left: 300px` element renders at
 * visual x = 450, and a pointer at visual x = 300 reports `clientX = 300`.
 * Consequently, any geometry measured in VISUAL px (a pointer position) must
 * be divided by `Z` before it is written into `style.left/top` or compared
 * against a measured `offsetWidth/offsetHeight`.
 */

import { useLayoutEffect, useState, type RefObject } from 'react'
import { getUiZoomFactor } from '@/stores/uiScaleStore'

/** A width/height pair. */
export interface BoxSize {
  width: number
  height: number
}

/**
 * The visible window in LAYOUT px — what a `position: fixed` element's
 * `left/top` and `offsetWidth/offsetHeight` are measured against. The zoom
 * factor is read live through {@link getUiZoomFactor} at every call — never
 * cached, so a scale change is picked up by the next recompute.
 */
export function getLayoutViewport(zoom: number = getUiZoomFactor()): BoxSize {
  return { width: window.innerWidth / zoom, height: window.innerHeight / zoom }
}

/** Viewport-space pointer coordinates from `MouseEvent.clientX/clientY` — VISUAL px. */
export interface CursorAnchor {
  x: number
  y: number
}

/** Which side of the cursor the panel occupies. */
export type HorizontalPlacement = 'right' | 'left'
/** Whether the panel hangs below or above the cursor. */
export type VerticalPlacement = 'down' | 'up'

export interface CursorMenuPlacement {
  /** `position: fixed` left, in LAYOUT px. */
  left: number
  /** `position: fixed` top, in LAYOUT px. */
  top: number
  /** `right` = panel's left edge on the cursor; `left` = flipped, right edge on the cursor. */
  horizontal: HorizontalPlacement
  /** `down` = panel's top edge on the cursor; `up` = flipped, bottom edge on the cursor. */
  vertical: VerticalPlacement
}

/**
 * Gutter kept between the panel and the window edge before a flip is deemed
 * necessary, in LAYOUT px. Absolute lengths intentionally scale with the UI
 * scale (like `px`/`rem` elsewhere) — only viewport-derived geometry is
 * zoom-corrected.
 */
export const CURSOR_MENU_MARGIN = 4

export interface ComputeCursorMenuPlacementArgs {
  /** Cursor x, LAYOUT px. */
  anchorX: number
  /** Cursor y, LAYOUT px. */
  anchorY: number
  /** Measured panel size, LAYOUT px. */
  menu: BoxSize
  /** Visible window size, LAYOUT px. */
  viewport: BoxSize
  /** Edge gutter, LAYOUT px. Defaults to {@link CURSOR_MENU_MARGIN}. */
  margin?: number
}

/** Clamp `value` into `[min, max]` (callers guarantee `max >= min`). */
function clamp(value: number, min: number, max: number): number {
  return Math.min(Math.max(value, min), max)
}

/**
 * Decide where a cursor-anchored panel goes.
 *
 * Strategy: prefer opening down-right (top-left corner on the cursor, the
 * conventional context-menu placement). Flip horizontally/vertically when the
 * preferred side cannot hold the full panel and the opposite side has at least
 * as much room — so a bad situation is never made worse. The result is finally
 * clamped inside the viewport, which only bites when the panel is larger than
 * the window (nothing can fully fit then); pinning the leading edge at the
 * gutter keeps the visible part usable.
 *
 * All inputs and outputs are in the same unit (layout px in production). Pure,
 * so the flip/room logic is unit-testable without a DOM.
 */
export function computeCursorMenuPlacement({
  anchorX,
  anchorY,
  menu,
  viewport,
  margin = CURSOR_MENU_MARGIN,
}: ComputeCursorMenuPlacementArgs): CursorMenuPlacement {
  const roomRight = viewport.width - margin - anchorX
  const roomLeft = anchorX - margin
  const horizontal: HorizontalPlacement =
    roomRight >= menu.width || roomRight >= roomLeft ? 'right' : 'left'

  const roomDown = viewport.height - margin - anchorY
  const roomUp = anchorY - margin
  const vertical: VerticalPlacement =
    roomDown >= menu.height || roomDown >= roomUp ? 'down' : 'up'

  const rawLeft = horizontal === 'right' ? anchorX : anchorX - menu.width
  const rawTop = vertical === 'down' ? anchorY : anchorY - menu.height

  return {
    left: clamp(rawLeft, margin, Math.max(margin, viewport.width - margin - menu.width)),
    top: clamp(rawTop, margin, Math.max(margin, viewport.height - margin - menu.height)),
    horizontal,
    vertical,
  }
}

/**
 * Resolve a pointer anchor into `position: fixed` coordinates (LAYOUT px) for
 * the element held by `menuRef`, so the panel opens next to the cursor and
 * entirely inside the visible window.
 *
 * Returns `null` until the panel has been measured; callers render it with
 * `visibility: hidden` in that case. The measuring `useLayoutEffect` runs
 * before paint, so the hidden state is never actually painted — no flicker.
 * Recomputes on window `resize`, which the UI-scale store also dispatches
 * after a scale change (`applyScaleToDocument`) re-laid out the document.
 *
 * The anchor is the state object owned by the caller; its identity changes per
 * open, which is exactly the recompute trigger.
 */
export function useCursorMenuPosition(
  anchor: CursorAnchor | null,
  menuRef: RefObject<HTMLElement | null>,
  margin: number = CURSOR_MENU_MARGIN,
): { left: number; top: number } | null {
  const [placement, setPlacement] = useState<CursorMenuPlacement | null>(null)

  useLayoutEffect(() => {
    if (!anchor) {
      setPlacement(null)
      return
    }

    const place = () => {
      const menu = menuRef.current
      if (!menu) return
      const zoom = getUiZoomFactor()
      const next = computeCursorMenuPlacement({
        // Pointer coordinates are VISUAL px; the panel is styled in LAYOUT px.
        anchorX: anchor.x / zoom,
        anchorY: anchor.y / zoom,
        menu: { width: menu.offsetWidth, height: menu.offsetHeight },
        viewport: getLayoutViewport(zoom),
        margin,
      })
      // Identity is preserved when nothing moved, so the extra layout-effect
      // pass converges instead of re-rendering forever.
      setPlacement((prev) =>
        prev && prev.left === next.left && prev.top === next.top ? prev : next,
      )
    }

    place()
    window.addEventListener('resize', place)
    return () => window.removeEventListener('resize', place)
  }, [anchor, menuRef, margin])

  return placement ? { left: placement.left, top: placement.top } : null
}
