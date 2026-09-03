// @vitest-environment jsdom
import { describe, it, expect, vi, beforeEach, afterEach } from 'vitest'
import {
  isInsideViewer,
  createFloatingViewerOutsideHandler,
} from './floatingViewerOutside'

// --- Fixtures -----------------------------------------------------------------

/** A floating-viewer-like container appended to <body>, with outside/inside nodes. */
function setupDOM() {
  document.body.innerHTML = ''
  const viewer = document.createElement('div')
  const inside = document.createElement('button')
  viewer.appendChild(inside)
  document.body.appendChild(viewer)

  // An element outside the viewer — e.g. the Research panel's
  // "View artifacts" DropdownMenuTrigger living in the workspace panel.
  const outsideTrigger = document.createElement('button')
  document.body.appendChild(outsideTrigger)

  return { viewer, inside, outsideTrigger }
}

/** Synthetic document-level event with a pinned target (not dispatched). */
function makeEvent(type: 'pointerdown' | 'focusin', target: Node, point?: { x: number; y: number }): Event {
  const event = new Event(type)
  Object.defineProperty(event, 'target', { value: target })
  if (point !== undefined) {
    Object.defineProperty(event, 'clientX', { value: point.x })
    Object.defineProperty(event, 'clientY', { value: point.y })
  }
  return event
}

/** Mount a fake open Radix popper whose trigger lives inside the viewer. */
function mountViewerAnchoredPopup(viewer: HTMLElement) {
  const trigger = document.createElement('button')
  trigger.setAttribute('aria-controls', 'radix-menu-1')
  viewer.appendChild(trigger)

  const wrapper = document.createElement('div')
  wrapper.setAttribute('data-radix-popper-content-wrapper', '')
  const content = document.createElement('div')
  content.id = 'radix-menu-1'
  content.setAttribute('data-state', 'open')
  wrapper.appendChild(content)
  document.body.appendChild(wrapper)

  return { trigger, wrapper, content }
}

// --- isInsideViewer (existing heuristics, unchanged behavior) -----------------

describe('isInsideViewer', () => {
  afterEach(() => {
    document.body.innerHTML = ''
  })

  it('treats nodes in the viewer subtree as inside', () => {
    const { viewer, inside } = setupDOM()
    expect(isInsideViewer(inside, viewer)).toBe(true)
  })

  it('treats outside nodes as outside', () => {
    const { viewer, outsideTrigger } = setupDOM()
    expect(isInsideViewer(outsideTrigger, viewer)).toBe(false)
  })

  it('treats a pointerdown whose point lands over the viewer rect as inside', () => {
    const { viewer, outsideTrigger } = setupDOM()
    vi.spyOn(viewer, 'getBoundingClientRect').mockReturnValue({
      left: 100, right: 300, top: 50, bottom: 400,
    } as DOMRect)

    const over = makeEvent('pointerdown', outsideTrigger, { x: 200, y: 100 })
    expect(isInsideViewer(outsideTrigger, viewer, over)).toBe(true)

    const notOver = makeEvent('pointerdown', outsideTrigger, { x: 50, y: 100 })
    expect(isInsideViewer(outsideTrigger, viewer, notOver)).toBe(false)
  })

  it('follows Radix portals back to a trigger inside the viewer', () => {
    const { viewer } = setupDOM()
    const { content } = mountViewerAnchoredPopup(viewer)
    const itemInsidePortal = document.createElement('div')
    content.appendChild(itemInsidePortal)

    expect(isInsideViewer(itemInsidePortal, viewer)).toBe(true)
  })

  it('treats root focusin as inside while a viewer-anchored popup is open', () => {
    const { viewer } = setupDOM()
    mountViewerAnchoredPopup(viewer)

    expect(isInsideViewer(document.body, viewer, makeEvent('focusin', document.body))).toBe(true)
  })
})

// --- createFloatingViewerOutsideHandler (collapse decision) --------------------

describe('createFloatingViewerOutsideHandler', () => {
  beforeEach(() => {
    document.body.innerHTML = ''
  })

  it('collapses on a pointerdown outside the viewer', () => {
    const { viewer, outsideTrigger } = setupDOM()
    const collapse = vi.fn()
    const handler = createFloatingViewerOutsideHandler(() => viewer, collapse)

    handler(makeEvent('pointerdown', outsideTrigger))

    expect(collapse).toHaveBeenCalledTimes(1)
  })

  it('does not collapse on a pointerdown inside the viewer', () => {
    const { viewer, inside } = setupDOM()
    const collapse = vi.fn()
    const handler = createFloatingViewerOutsideHandler(() => viewer, collapse)

    handler(makeEvent('pointerdown', inside))

    expect(collapse).not.toHaveBeenCalled()
  })

  // Regression: picking an artifact in the Research panel's "View artifacts"
  // dropdown (or any Radix menu that opens a file) expands the floating viewer
  // from the item's onSelect, then the menu's FocusScope unmount hook — a
  // deferred setTimeout(0) — re-focuses the dropdown trigger OUTSIDE the
  // viewer. That focusin must not collapse the freshly opened viewer: focus
  // never left the outside world, so it is not an exit from the viewer.
  // Before the guard the viewer appeared and instantly vanished.
  it('does NOT collapse on outside focusin when the viewer never held focus (Radix focus-restore)', () => {
    const { viewer, outsideTrigger } = setupDOM()
    const collapse = vi.fn()
    const handler = createFloatingViewerOutsideHandler(() => viewer, collapse)

    handler(makeEvent('focusin', outsideTrigger))

    expect(collapse).not.toHaveBeenCalled()
  })

  it('keeps the viewer open across several outside focusins until it held focus', () => {
    const { viewer, outsideTrigger } = setupDOM()
    const collapse = vi.fn()
    const handler = createFloatingViewerOutsideHandler(() => viewer, collapse)

    handler(makeEvent('focusin', outsideTrigger))
    handler(makeEvent('focusin', document.body))

    expect(collapse).not.toHaveBeenCalled()
  })

  it('collapses on outside focusin only after the viewer held focus (real focus exit)', () => {
    const { viewer, inside, outsideTrigger } = setupDOM()
    const collapse = vi.fn()
    const handler = createFloatingViewerOutsideHandler(() => viewer, collapse)

    // Focus enters the viewer, then moves back out → collapse.
    handler(makeEvent('focusin', inside))
    handler(makeEvent('focusin', outsideTrigger))

    expect(collapse).toHaveBeenCalledTimes(1)
  })

  it('arms the viewer-held-focus state from a pointerdown inside the viewer', () => {
    const { viewer, inside, outsideTrigger } = setupDOM()
    const collapse = vi.fn()
    const handler = createFloatingViewerOutsideHandler(() => viewer, collapse)

    handler(makeEvent('pointerdown', inside))
    handler(makeEvent('focusin', outsideTrigger))

    expect(collapse).toHaveBeenCalledTimes(1)
  })

  it('resets the held-focus state when recreated (viewer re-expanded)', () => {
    const { viewer, inside, outsideTrigger } = setupDOM()

    // First session: user interacts with the viewer, then focus exits.
    const firstCollapse = vi.fn()
    const first = createFloatingViewerOutsideHandler(() => viewer, firstCollapse)
    first(makeEvent('pointerdown', inside))
    first(makeEvent('focusin', outsideTrigger))
    expect(firstCollapse).toHaveBeenCalledTimes(1)

    // Viewer re-expands (e.g. another artifact opened): a fresh handler must
    // again ignore the Radix focus-restore, not inherit the old state.
    const secondCollapse = vi.fn()
    const second = createFloatingViewerOutsideHandler(() => viewer, secondCollapse)
    second(makeEvent('focusin', outsideTrigger))
    expect(secondCollapse).not.toHaveBeenCalled()
  })

  it('ignores events when the viewer is not mounted (null)', () => {
    const outside = document.createElement('button')
    document.body.appendChild(outside)
    const collapse = vi.fn()
    const handler = createFloatingViewerOutsideHandler(() => null, collapse)

    handler(makeEvent('pointerdown', outside))
    handler(makeEvent('focusin', outside))

    expect(collapse).not.toHaveBeenCalled()
  })
})
