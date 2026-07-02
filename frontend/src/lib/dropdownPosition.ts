/**
 * Pure helpers for positioning a portaled dropdown menu relative to its
 * trigger button. Extracted from ModelCombobox so the up/down direction and
 * viewport-clamping logic can be unit-tested without a DOM.
 *
 * The menu is rendered through a React portal with `position: fixed`, so its
 * coordinates are expressed in viewport space (matching the values returned
 * by `Element.getBoundingClientRect()`).
 */

export type DropdownDirection = 'up' | 'down'

/** Viewport-space rect of the trigger button (a subset of DOMRect). */
export interface TriggerRect {
  top: number
  bottom: number
  left: number
  width: number
}

export interface DropdownPosition {
  /** Viewport-space top of the menu, in pixels. */
  top: number
  /** Viewport-space left of the menu, in pixels. */
  left: number
  /** Menu width, in pixels (>= minWidth). */
  width: number
  /** Whether the menu opens above or below the trigger. */
  direction: DropdownDirection
}

export interface ComputeDropdownPositionArgs {
  triggerRect: TriggerRect
  /** Expected/outer height of the menu in pixels (e.g. measured offsetHeight). */
  dropdownHeight: number
  /** Viewport height in pixels (window.innerHeight). */
  viewportHeight: number
  /** Viewport width in pixels (window.innerWidth). */
  viewportWidth: number
  /** Gap between the trigger and the menu, in pixels. */
  gap?: number
  /** Minimum menu width so long labels don't get cramped, in pixels. */
  minWidth?: number
  /** Margin kept between the menu and the viewport edges, in pixels. */
  margin?: number
}

/**
 * Compute a `position: fixed` rect for a dropdown menu anchored to a trigger.
 *
 * Direction strategy: open downward when there is room for the full height
 * below the trigger, otherwise flip upward (as long as that is not worse than
 * downward). The result is clamped so the menu always stays within the viewport
 * horizontally and never slides off the top edge.
 */
export function computeDropdownPosition({
  triggerRect,
  dropdownHeight,
  viewportHeight,
  viewportWidth,
  gap = 6,
  minWidth = 288,
  margin = 8,
}: ComputeDropdownPositionArgs): DropdownPosition {
  const width = Math.max(triggerRect.width, minWidth)

  const spaceBelow = viewportHeight - triggerRect.bottom
  const spaceAbove = triggerRect.top

  // Prefer downward; only flip up when downward lacks room AND upward is at
  // least as generous (so we never make a bad situation worse).
  const openDownward = spaceBelow >= dropdownHeight || spaceBelow >= spaceAbove

  const top = openDownward
    ? triggerRect.bottom + gap
    : Math.max(margin, triggerRect.top - dropdownHeight - gap)

  // Clamp horizontally so the menu stays fully visible.
  let left = triggerRect.left
  if (left + width > viewportWidth - margin) {
    left = Math.max(margin, viewportWidth - width - margin)
  }

  return { top, left, width, direction: openDownward ? 'down' : 'up' }
}
