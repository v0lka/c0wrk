/**
 * Outside-interaction collapse logic for the floating (unpinned) file viewer.
 *
 * Extracted from AppLayout so the decision — "does this document-level
 * pointer/focus event collapse the floating viewer?" — is a focused handler
 * unit-testable in isolation (see floatingViewerOutside.test.ts) without
 * rendering the app shell.
 */

/**
 * Resolve the control that opened a Radix popper portal (dropdown menu, select,
 * popover, …). Radix renders such popovers in a portal at `document.body`, so a
 * plain `node.contains(target)` check can't tell that the popover logically
 * belongs to a control inside the floating viewer — which would collapse the
 * viewer the moment one of its popovers opens (e.g. the review/file hunk
 * comboboxes). The portal wrapper holds the content element; Radix pairs that
 * content with its trigger via `id` / `aria-controls`, so we walk back to the
 * trigger and re-test containment against the viewer.
 */
function resolveRadixPortalTrigger(target: Node | null): Element | null {
  const element = target instanceof Element ? target : (target?.parentElement ?? null)
  const wrapper = element?.closest('[data-radix-popper-content-wrapper]')
  if (!wrapper) return null
  const content = wrapper.firstElementChild
  if (!(content instanceof HTMLElement) || !content.id) return null
  return document.querySelector(`[aria-controls="${content.id}"]`)
}

/** Whether a node is the document root (`<body>`/`<html>`). */
function isDocumentRoot(node: Node): boolean {
  return node === document.body || node === document.documentElement
}

/** Extract viewport client coordinates from a pointer/mouse event, if present. */
function getClientPoint(event: Event): { x: number; y: number } | null {
  const e = event as { clientX?: number; clientY?: number }
  if (typeof e.clientX === 'number' && typeof e.clientY === 'number') {
    return { x: e.clientX, y: e.clientY }
  }
  return null
}

/**
 * Whether an open Radix popper (menu/select/popover) whose trigger lives inside
 * the viewer is currently mounted. While one is open, Radix's modal layer sets
 * `pointer-events: none` on `<body>` and, on dismissal, briefly moves focus to
 * the document root before restoring it to the trigger — both surface as events
 * whose target is `<body>`/`<html>`, which must not collapse the viewer.
 */
function hasOpenRadixPopupInside(viewer: HTMLElement): boolean {
  const wrappers = document.querySelectorAll('[data-radix-popper-content-wrapper]')
  for (const wrapper of wrappers) {
    const content = wrapper.firstElementChild
    if (!(content instanceof HTMLElement) || !content.id) continue
    if (content.getAttribute('data-state') !== 'open') continue
    const trigger = document.querySelector(`[aria-controls="${content.id}"]`)
    if (trigger && viewer.contains(trigger)) return true
  }
  return false
}

/**
 * Whether a pointer/focus event is considered "inside" the floating viewer:
 *  - directly in its DOM subtree, or
 *  - inside a Radix portal whose trigger (the `aria-controls` owner) is inside
 *    the viewer, or
 *  - a `pointerdown` whose coordinates land over the viewer — Radix modal
 *    popovers redirect the target to the document root by disabling pointer
 *    events on `<body>`, so the *target* is unreliable while the *point* is
 *    still over the viewer (this is what makes a re-click on a hunk combobox
 *    collapse the panel), or
 *  - a `focusin` on the document root while a viewer-anchored popup is open
 *    (Radix restores focus to the trigger a tick later; the root focus is a
 *    transient artefact of closing the popup).
 */
export function isInsideViewer(target: Node, viewer: HTMLElement, event?: Event): boolean {
  if (viewer.contains(target)) return true

  const trigger = resolveRadixPortalTrigger(target)
  if (trigger !== null && viewer.contains(trigger)) return true

  if (event?.type === 'pointerdown') {
    const point = getClientPoint(event)
    if (point) {
      const rect = viewer.getBoundingClientRect()
      if (
        point.x >= rect.left &&
        point.x <= rect.right &&
        point.y >= rect.top &&
        point.y <= rect.bottom
      ) {
        return true
      }
    }
  }

  if (event?.type === 'focusin' && isDocumentRoot(target) && hasOpenRadixPopupInside(viewer)) {
    return true
  }

  return false
}

/**
 * Create the document-level handler that collapses the floating (unpinned)
 * viewer when the user works outside it: any outside pointerdown always
 * collapses, and an outside focusin collapses only once the viewer has
 * actually held focus/interaction since it expanded ("focus moved out").
 *
 * The focus guard exists because Radix menus (dropdown/context menus) render
 * in a portal and, when an item is selected, restore focus to their trigger
 * through a deferred `setTimeout(0)` in FocusScope's unmount hook — strictly
 * AFTER the item's `onSelect` has already expanded the floating viewer (e.g.
 * picking an artifact in the Research panel's "View artifacts" dropdown calls
 * `openFile` first). The document listeners are attached by then, so the
 * trigger re-focus looks like "focus is outside" and would collapse the
 * freshly opened viewer a tick later — the viewer flashed open and instantly
 * vanished. Focus that never left the outside world (the menu trigger) is not
 * an exit from the viewer, so it is ignored until the user actually interacts
 * with the viewer and then moves focus away.
 *
 * A fresh handler starts with `viewerHadFocus` false: AppLayout recreates it
 * every time the viewer re-expands (the effect depends on `floating`).
 */
export function createFloatingViewerOutsideHandler(
  getViewer: () => HTMLElement | null,
  collapseViewer: () => void,
): (event: Event) => void {
  let viewerHadFocus = false
  return (event: Event) => {
    const viewer = getViewer()
    const target = event.target as Node | null
    if (!viewer || !target) return
    if (isInsideViewer(target, viewer, event)) {
      // Any pointer/focus interaction inside the viewer (or its anchored
      // popups) marks it as holding focus from here on.
      viewerHadFocus = true
      return
    }
    if (event.type === 'focusin' && !viewerHadFocus) return
    collapseViewer()
  }
}
