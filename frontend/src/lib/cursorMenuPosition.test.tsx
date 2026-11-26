// @vitest-environment jsdom
import { describe, it, expect, beforeEach, afterEach } from 'vitest'
import { act, useRef } from 'react'
import { createRoot, type Root } from 'react-dom/client'
import {
  CURSOR_MENU_MARGIN,
  computeCursorMenuPlacement,
  getLayoutViewport,
  useCursorMenuPosition,
} from './cursorMenuPosition'
import { useUiScaleStore } from '@/stores/uiScaleStore'

const VIEWPORT = { width: 1000, height: 800 }
const MENU = { width: 200, height: 160 }

describe('computeCursorMenuPlacement', () => {
  it('opens down-right with the top-left corner on the cursor when it fits', () => {
    const p = computeCursorMenuPlacement({ anchorX: 100, anchorY: 100, menu: MENU, viewport: VIEWPORT })
    expect(p).toEqual({ left: 100, top: 100, horizontal: 'right', vertical: 'down' })
  })

  it('flips to the left of the cursor when the panel would cross the right edge', () => {
    // roomRight = 1000 - 4 - 900 = 96 < 200; roomLeft = 896 -> flip.
    const p = computeCursorMenuPlacement({ anchorX: 900, anchorY: 100, menu: MENU, viewport: VIEWPORT })
    expect(p.horizontal).toBe('left')
    expect(p.left).toBe(700) // right edge of the panel sits on the cursor
    expect(p.vertical).toBe('down')
  })

  it('flips above the cursor when the panel would cross the bottom edge', () => {
    // roomDown = 800 - 4 - 700 = 96 < 160; roomUp = 696 -> flip.
    const p = computeCursorMenuPlacement({ anchorX: 100, anchorY: 700, menu: MENU, viewport: VIEWPORT })
    expect(p.vertical).toBe('up')
    expect(p.top).toBe(540) // bottom edge of the panel sits on the cursor
    expect(p.horizontal).toBe('right')
  })

  it('flips both axes near the bottom-right corner', () => {
    const p = computeCursorMenuPlacement({ anchorX: 960, anchorY: 780, menu: MENU, viewport: VIEWPORT })
    expect(p).toEqual({ left: 760, top: 620, horizontal: 'left', vertical: 'up' })
  })

  it('keeps the preferred side when the opposite side has no more room', () => {
    // A panel taller than the window: neither side fits. The cursor is near the
    // top, so below has more room -> stay down, clamped to the gutter.
    const p = computeCursorMenuPlacement({
      anchorX: 500,
      anchorY: 10,
      menu: { width: 100, height: 900 },
      viewport: VIEWPORT,
    })
    expect(p.vertical).toBe('down')
    expect(p.top).toBe(CURSOR_MENU_MARGIN)
  })

  it('clamps a panel wider than the window to the leading gutter', () => {
    const p = computeCursorMenuPlacement({
      anchorX: 500,
      anchorY: 400,
      menu: { width: 1200, height: 100 },
      viewport: VIEWPORT,
    })
    expect(p.horizontal).toBe('right')
    expect(p.left).toBe(CURSOR_MENU_MARGIN)
  })

  it('honours a custom margin, both for the fit test and the clamp', () => {
    const p = computeCursorMenuPlacement({
      anchorX: 500,
      anchorY: 400,
      menu: MENU,
      viewport: VIEWPORT,
      margin: 24,
    })
    expect(p.left).toBe(500) // still roomy on the right at this gutter

    const tight = computeCursorMenuPlacement({
      anchorX: 1000,
      anchorY: 800,
      menu: MENU,
      viewport: { width: 100, height: 100 },
      margin: 24,
    })
    expect(tight.left).toBe(24)
    expect(tight.top).toBe(24)
  })

  it('treats an unmeasured (0×0) panel as fitting at the cursor', () => {
    const p = computeCursorMenuPlacement({
      anchorX: 10,
      anchorY: 20,
      menu: { width: 0, height: 0 },
      viewport: VIEWPORT,
    })
    expect(p).toEqual({ left: 10, top: 20, horizontal: 'right', vertical: 'down' })
  })
})

/**
 * Minimal consumer of the hook. The panel reports its own measured size through
 * the stubbed `offsetWidth`/`offsetHeight` getters installed per test.
 */
function Panel({ anchor }: { anchor: { x: number; y: number } | null }) {
  const ref = useRef<HTMLDivElement>(null)
  const pos = useCursorMenuPosition(anchor, ref)
  return (
    <div
      data-testid="panel"
      ref={ref}
      style={{
        position: 'fixed',
        left: pos?.left ?? 0,
        top: pos?.top ?? 0,
        visibility: pos ? 'visible' : 'hidden',
      }}
    />
  )
}

describe('useCursorMenuPosition', () => {
  let container: HTMLElement
  let root: Root
  let panelSize: { width: number; height: number }

  const original = {
    offsetWidth: Object.getOwnPropertyDescriptor(HTMLElement.prototype, 'offsetWidth'),
    offsetHeight: Object.getOwnPropertyDescriptor(HTMLElement.prototype, 'offsetHeight'),
    innerWidth: Object.getOwnPropertyDescriptor(window, 'innerWidth'),
    innerHeight: Object.getOwnPropertyDescriptor(window, 'innerHeight'),
  }

  beforeEach(() => {
    panelSize = { width: MENU.width, height: MENU.height }
    // jsdom reports 0 for layout metrics; the hook measures the panel through
    // offsetWidth/offsetHeight, so both are stubbed at the prototype.
    Object.defineProperty(HTMLElement.prototype, 'offsetWidth', {
      configurable: true,
      get: () => panelSize.width,
    })
    Object.defineProperty(HTMLElement.prototype, 'offsetHeight', {
      configurable: true,
      get: () => panelSize.height,
    })
    Object.defineProperty(window, 'innerWidth', {
      configurable: true,
      writable: true,
      value: VIEWPORT.width,
    })
    Object.defineProperty(window, 'innerHeight', {
      configurable: true,
      writable: true,
      value: VIEWPORT.height,
    })
    useUiScaleStore.setState({ scale: 100 })
    container = document.createElement('div')
    document.body.replaceChildren(container)
    root = createRoot(container)
  })

  afterEach(() => {
    act(() => root.unmount())
    document.body.replaceChildren()
    useUiScaleStore.setState({ scale: 100 })
    for (const [key, descriptor] of Object.entries(original)) {
      if (descriptor) {
        Object.defineProperty(
          key === 'offsetWidth' || key === 'offsetHeight' ? HTMLElement.prototype : window,
          key,
          descriptor,
        )
      }
    }
  })

  const panel = (): HTMLElement => container.querySelector('[data-testid="panel"]') as HTMLElement

  const render = (anchor: { x: number; y: number } | null): void => {
    act(() => {
      root.render(<Panel anchor={anchor} />)
    })
  }

  it('renders hidden and unplaced until an anchor is provided', () => {
    render(null)
    expect(panel().style.visibility).toBe('hidden')
    expect(panel().style.left).toBe('0px')
  })

  it('divides the VISUAL pointer anchor by the zoom factor', () => {
    useUiScaleStore.setState({ scale: 150 })
    render({ x: 300, y: 200 })
    // 300 / 1.5 = 200 and 200 / 1.5 = 133.33: the panel lands ON the cursor
    // instead of the ×zoom-displaced position a raw clientX would produce.
    expect(parseFloat(panel().style.left)).toBeCloseTo(200, 5)
    expect(parseFloat(panel().style.top)).toBeCloseTo(200 / 1.5, 5)
    expect(panel().style.visibility).toBe('visible')
  })

  it('flips left/up when the cursor sits in the bottom-right corner', () => {
    render({ x: 960, y: 780 })
    expect(parseFloat(panel().style.left)).toBeCloseTo(760, 5)
    expect(parseFloat(panel().style.top)).toBeCloseTo(620, 5)
  })

  it('re-places an open panel on window resize', () => {
    render({ x: 960, y: 780 })
    expect(parseFloat(panel().style.left)).toBeCloseTo(760, 5)

    act(() => {
      Object.defineProperty(window, 'innerWidth', {
        configurable: true,
        writable: true,
        value: 600,
      })
      window.dispatchEvent(new Event('resize'))
    })

    // 960 - 200 = 760 would overflow a 600px window -> clamped to 600 - 4 - 200.
    expect(parseFloat(panel().style.left)).toBeCloseTo(396, 5)
  })

  it('keeps the panel fully visible for an anchor at every window corner', () => {
    for (const anchor of [
      { x: 4, y: 4 },
      { x: 996, y: 4 },
      { x: 4, y: 796 },
      { x: 996, y: 796 },
    ]) {
      render(anchor)
      const left = parseFloat(panel().style.left)
      const top = parseFloat(panel().style.top)
      expect(left).toBeGreaterThanOrEqual(0)
      expect(top).toBeGreaterThanOrEqual(0)
      expect(left + panelSize.width).toBeLessThanOrEqual(VIEWPORT.width)
      expect(top + panelSize.height).toBeLessThanOrEqual(VIEWPORT.height)
    }
  })
})

describe('getLayoutViewport', () => {
  // jsdom reports no geometry; the viewport extent is stubbed so the
  // conversion can be asserted against concrete numbers.
  const originalInnerWidth = Object.getOwnPropertyDescriptor(window, 'innerWidth')
  const originalInnerHeight = Object.getOwnPropertyDescriptor(window, 'innerHeight')

  const setViewport = (width: number, height: number): void => {
    Object.defineProperty(window, 'innerWidth', { configurable: true, writable: true, value: width })
    Object.defineProperty(window, 'innerHeight', { configurable: true, writable: true, value: height })
  }

  beforeEach(() => {
    setViewport(1000, 800)
    useUiScaleStore.setState({ scale: 100 })
  })

  afterEach(() => {
    useUiScaleStore.setState({ scale: 100 })
    if (originalInnerWidth) Object.defineProperty(window, 'innerWidth', originalInnerWidth)
    if (originalInnerHeight) Object.defineProperty(window, 'innerHeight', originalInnerHeight)
  })

  it('is the identity at scale 100', () => {
    expect(getLayoutViewport()).toEqual({ width: 1000, height: 800 })
  })

  it('divides the VISUAL viewport by the zoom factor', () => {
    // window.innerWidth/innerHeight are visual px; a fixed element's
    // left/top are layout px, so the layout viewport is the inner size / zoom.
    useUiScaleStore.setState({ scale: 150 })
    expect(getLayoutViewport()).toEqual({ width: 1000 / 1.5, height: 800 / 1.5 })

    useUiScaleStore.setState({ scale: 50 })
    expect(getLayoutViewport()).toEqual({ width: 2000, height: 1600 })
  })

  it('accepts an explicit zoom (pure, no store read)', () => {
    expect(getLayoutViewport(2)).toEqual({ width: 500, height: 400 })
  })

  it('falls back to zoom 1 on corrupted persisted scale', () => {
    useUiScaleStore.setState({ scale: Number.NaN })
    expect(getLayoutViewport()).toEqual({ width: 1000, height: 800 })
  })
})
