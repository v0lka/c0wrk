// @vitest-environment jsdom
import { describe, it, expect, vi, beforeEach, afterEach } from 'vitest'
import { act, createElement } from 'react'
import { createRoot, type Root } from 'react-dom/client'
import {
  DEFAULT_MAX_SCALE,
  DEFAULT_MIN_SCALE,
  DEFAULT_ZOOM_STEP,
  DRAG_CLICK_THRESHOLD_PX,
  INITIAL_VIEW,
  clampScale,
  computeFitView,
  isHorizontalWheelGesture,
  usePanZoom,
  zoomToPoint,
} from './usePanZoom'

describe('clampScale', () => {
  it('passes in-range values through', () => {
    expect(clampScale(1, DEFAULT_MIN_SCALE, DEFAULT_MAX_SCALE)).toBe(1)
    expect(clampScale(2.5, DEFAULT_MIN_SCALE, DEFAULT_MAX_SCALE)).toBe(2.5)
    expect(clampScale(0.75, DEFAULT_MIN_SCALE, DEFAULT_MAX_SCALE)).toBe(0.75)
  })

  it('clamps below the minimum', () => {
    expect(clampScale(0.1, DEFAULT_MIN_SCALE, DEFAULT_MAX_SCALE)).toBe(0.2)
    expect(clampScale(0, 0.2, 5)).toBe(0.2)
  })

  it('clamps above the maximum', () => {
    expect(clampScale(7, DEFAULT_MIN_SCALE, DEFAULT_MAX_SCALE)).toBe(5)
    expect(clampScale(100, 0.2, 5)).toBe(5)
  })

  it('keeps exact boundary values', () => {
    expect(clampScale(0.2, 0.2, 5)).toBe(0.2)
    expect(clampScale(5, 0.2, 5)).toBe(5)
  })

  it('supports custom limits', () => {
    expect(clampScale(0.5, 1, 2)).toBe(1)
    expect(clampScale(3, 1, 2)).toBe(2)
  })
})

describe('zoomToPoint', () => {
  it('zooms in while keeping the anchor point fixed', () => {
    // Anchor (100, 50) on an identity view.
    const next = zoomToPoint(INITIAL_VIEW, DEFAULT_ZOOM_STEP, 100, 50, DEFAULT_MIN_SCALE, DEFAULT_MAX_SCALE)
    expect(next).not.toBeNull()
    // scale 1 -> 1.25; x = 100 - 1.25*100 = -25; y = 50 - 1.25*50 = -12.5
    expect(next!.scale).toBeCloseTo(1.25)
    expect(next!.x).toBeCloseTo(-25)
    expect(next!.y).toBeCloseTo(-12.5)
  })

  it('preserves the content point under the anchor across zoom levels', () => {
    const prev = { scale: 0.8, x: -37, y: 12 }
    const factor = 1.6
    const cx = 240
    const cy = 95
    const next = zoomToPoint(prev, factor, cx, cy, DEFAULT_MIN_SCALE, DEFAULT_MAX_SCALE)
    expect(next).not.toBeNull()
    // The content coordinate under the anchor must be identical before/after.
    const before = { x: (cx - prev.x) / prev.scale, y: (cy - prev.y) / prev.scale }
    const after = { x: (cx - next!.x) / next!.scale, y: (cy - next!.y) / next!.scale }
    expect(after.x).toBeCloseTo(before.x)
    expect(after.y).toBeCloseTo(before.y)
  })

  it('zooms out around a translated view anchor', () => {
    const prev = { scale: 2, x: -120, y: -60 }
    const next = zoomToPoint(prev, 1 / DEFAULT_ZOOM_STEP, 200, 100, DEFAULT_MIN_SCALE, DEFAULT_MAX_SCALE)
    expect(next).not.toBeNull()
    expect(next!.scale).toBeCloseTo(2 / DEFAULT_ZOOM_STEP)
    const k = next!.scale / prev.scale
    expect(next!.x).toBeCloseTo(200 - k * (200 - prev.x))
    expect(next!.y).toBeCloseTo(100 - k * (100 - prev.y))
  })

  it('clamps to the maximum scale while anchoring', () => {
    const prev = { scale: 4, x: -50, y: -25 }
    const next = zoomToPoint(prev, 2, 100, 50, DEFAULT_MIN_SCALE, DEFAULT_MAX_SCALE)
    expect(next).not.toBeNull()
    expect(next!.scale).toBe(DEFAULT_MAX_SCALE)
    const k = DEFAULT_MAX_SCALE / prev.scale
    expect(next!.x).toBeCloseTo(100 - k * (100 - prev.x))
    expect(next!.y).toBeCloseTo(50 - k * (50 - prev.y))
  })

  it('clamps to the minimum scale while anchoring', () => {
    const prev = { scale: 0.3, x: 40, y: 20 }
    const next = zoomToPoint(prev, 0.5, 100, 50, DEFAULT_MIN_SCALE, DEFAULT_MAX_SCALE)
    expect(next).not.toBeNull()
    expect(next!.scale).toBe(DEFAULT_MIN_SCALE)
    const k = DEFAULT_MIN_SCALE / prev.scale
    expect(next!.x).toBeCloseTo(100 - k * (100 - prev.x))
    expect(next!.y).toBeCloseTo(50 - k * (50 - prev.y))
  })

  it('returns null when already at the maximum (no-op keeps the previous object)', () => {
    const prev = { scale: DEFAULT_MAX_SCALE, x: -99, y: 5 }
    expect(zoomToPoint(prev, 2, 100, 50, DEFAULT_MIN_SCALE, DEFAULT_MAX_SCALE)).toBeNull()
    expect(zoomToPoint(prev, DEFAULT_ZOOM_STEP, 0, 0, DEFAULT_MIN_SCALE, DEFAULT_MAX_SCALE)).toBeNull()
  })

  it('returns null when already at the minimum (no-op keeps the previous object)', () => {
    const prev = { scale: DEFAULT_MIN_SCALE, x: 10, y: -4 }
    expect(zoomToPoint(prev, 0.5, 100, 50, DEFAULT_MIN_SCALE, DEFAULT_MAX_SCALE)).toBeNull()
  })

  it('returns null when the clamped scale is unchanged for any reason', () => {
    // A factor of exactly 1 changes nothing even away from the limits.
    const prev = { scale: 1.4, x: 3, y: 3 }
    expect(zoomToPoint(prev, 1, 100, 50, DEFAULT_MIN_SCALE, DEFAULT_MAX_SCALE)).toBeNull()
  })
})

describe('computeFitView', () => {
  it('downscales wide content to the canvas width and centers it', () => {
    // natural 1000x200 into 500x400 -> scale 0.5, scaled 500x100
    const view = computeFitView(500, 400, 1000, 200)
    expect(view).toEqual({ scale: 0.5, x: 0, y: 150 })
  })

  it('never upscales content narrower than the canvas', () => {
    // natural 200x100 into 500x400 -> scale stays 1, centered on both axes.
    const view = computeFitView(500, 400, 200, 100)
    expect(view).toEqual({ scale: 1, x: 150, y: 150 })
  })

  it('top-aligns content taller than the viewport (y never goes negative)', () => {
    // natural 400x320 into 500x160 -> scale 1, scaled height 320 > 160.
    const view = computeFitView(500, 160, 400, 320)
    expect(view).toEqual({ scale: 1, x: 50, y: 0 })
  })

  it('centers content that fits exactly', () => {
    const view = computeFitView(500, 400, 500, 400)
    expect(view).toEqual({ scale: 1, x: 0, y: 0 })
  })

  it('returns null for unmeasurable content', () => {
    expect(computeFitView(500, 400, 0, 100)).toBeNull()
    expect(computeFitView(500, 400, 100, 0)).toBeNull()
  })

  it('returns null for a zero-width canvas', () => {
    expect(computeFitView(0, 400, 100, 100)).toBeNull()
  })
})

describe('isHorizontalWheelGesture', () => {
  it('detects horizontal-dominant gestures', () => {
    expect(isHorizontalWheelGesture(10, 2)).toBe(true)
    expect(isHorizontalWheelGesture(-8, 1)).toBe(true)
  })

  it('treats vertical-dominant gestures as zoom input', () => {
    expect(isHorizontalWheelGesture(2, 10)).toBe(false)
    expect(isHorizontalWheelGesture(0, -12)).toBe(false)
  })

  it('treats equal deltas as vertical (zoom)', () => {
    expect(isHorizontalWheelGesture(5, 5)).toBe(false)
    expect(isHorizontalWheelGesture(0, 0)).toBe(false)
  })
})

describe('extracted defaults', () => {
  // Lock the hook defaults to the values MermaidBlock used before the
  // extraction, so the refactor cannot silently change zoom behavior.
  it('matches the previous MermaidBlock constants', () => {
    expect(DEFAULT_MIN_SCALE).toBe(0.2)
    expect(DEFAULT_MAX_SCALE).toBe(5)
    expect(DEFAULT_ZOOM_STEP).toBe(1.25)
    expect(DRAG_CLICK_THRESHOLD_PX).toBe(4)
    expect(INITIAL_VIEW).toEqual({ scale: 1, x: 0, y: 0 })
  })
})

// ── Hook interaction: lazy pointer capture ─────────────────────────────
//
// Regression guard: pointer capture must NOT be engaged on pointerdown.
// While capture is active, browsers retarget every event of that pointer —
// including the compatibility `click` — to the capture element (the canvas
// itself), so a plain click on interactive content inside the canvas (the
// Research DAG hypothesis nodes) would never reach its onClick handler.
// Capture is engaged only once a gesture crosses DRAG_CLICK_THRESHOLD_PX
// and becomes a pan; plain clicks stay uncaptured and hit their target.

interface Probe {
  root: Root
  host: HTMLElement
  canvas: HTMLDivElement
  captureSpy: ReturnType<typeof vi.fn>
}

let cleanup: (() => void) | null = null
afterEach(() => {
  cleanup?.()
  cleanup = null
})

// ── Deterministic rAF frames ────────────────────────────────────────────
// The drag path coalesces React state writes into requestAnimationFrame
// flushes ([26]b); tests drive frames manually instead of relying on jsdom
// timers firing between assertions.
const rafQueue: Array<FrameRequestCallback | undefined> = []

beforeEach(() => {
  vi.stubGlobal('requestAnimationFrame', (cb: FrameRequestCallback) => {
    rafQueue.push(cb)
    return rafQueue.length
  })
  vi.stubGlobal('cancelAnimationFrame', (id?: number) => {
    if (typeof id === 'number' && id >= 1 && id <= rafQueue.length) delete rafQueue[id - 1]
  })
})

afterEach(() => {
  rafQueue.length = 0
  vi.unstubAllGlobals()
})

/**
 * Run exactly the callbacks scheduled so far (one frame); anything they in
 * turn schedule lands in the next frame.
 */
function flushRaf() {
  const frame = rafQueue.splice(0)
  for (const cb of frame) cb?.(0)
}

/** Frames still waiting to run (cancelled slots leave holes, not length). */
function scheduledFrames(): number {
  return rafQueue.reduce((n, cb) => (cb ? n + 1 : n), 0)
}

/** PanProbe renders since mount (for re-render-count assertions). */
let probeRenders = 0

/** Minimal canvas wired to the hook; view/didDrag mirror into data attrs. */
function PanProbe() {
  const {
    view,
    canvasRef,
    didDragRef,
    onPointerDown,
    onPointerMove,
    onPointerUp,
    onPointerCancel,
  } = usePanZoom()
  probeRenders += 1
  return createElement('div', {
    ref: canvasRef,
    'data-testid': 'pan-canvas',
    'data-x': String(view.x),
    'data-scale': String(view.scale),
    'data-drag': didDragRef.current ? '1' : '0',
    onPointerDown,
    onPointerMove,
    onPointerUp,
    onPointerCancel,
  })
}

function renderProbe(): Probe {
  const host = document.createElement('div')
  document.body.appendChild(host)
  const root = createRoot(host)
  act(() => {
    root.render(createElement(PanProbe))
  })
  const canvas = host.querySelector<HTMLDivElement>('[data-testid="pan-canvas"]')!
  // jsdom does not implement pointer capture; spy on the element API so the
  // tests can assert exactly when the hook engages it.
  const captureSpy = vi.fn()
  canvas.setPointerCapture = captureSpy as unknown as typeof canvas.setPointerCapture
  canvas.releasePointerCapture = vi.fn() as unknown as typeof canvas.releasePointerCapture
  canvas.hasPointerCapture = (() => false) as unknown as typeof canvas.hasPointerCapture
  probeRenders = 0
  cleanup = () => {
    act(() => {
      root.unmount()
    })
    host.remove()
  }
  return { root, host, canvas, captureSpy }
}

interface FirePointerInit {
  /** Bitmask of depressed buttons at event time. */
  buttons?: number
  /** PointerEvent-only flag; grafted onto the MouseEvent (default true). */
  isPrimary?: boolean
}

/**
 * Dispatch a pointer-typed mouse event (React keys off the event type).
 * Defaults mirror real pointer input: the primary button reads as held
 * during pointerdown/pointermove and released for pointerup/pointercancel.
 */
function firePointer(
  el: Element,
  type: string,
  x: number,
  y: number,
  button = 0,
  init: FirePointerInit = {},
) {
  const buttons = init.buttons ?? (type === 'pointerup' || type === 'pointercancel' ? 0 : 1)
  const ev = new MouseEvent(type, { bubbles: true, button, buttons, clientX: x, clientY: y })
  // jsdom's MouseEvent lacks the PointerEvent-only `isPrimary` property;
  // graft it so the hook's multi-pointer guard is exercisable.
  Object.defineProperty(ev, 'isPrimary', { value: init.isPrimary ?? true })
  el.dispatchEvent(ev)
}

describe('usePanZoom lazy pointer capture', () => {
  it('never captures during a plain (non-dragged) click', () => {
    const { canvas, captureSpy } = renderProbe()
    act(() => {
      firePointer(canvas, 'pointerdown', 100, 100)
      firePointer(canvas, 'pointermove', 102, 100) // 2px: still a click
      firePointer(canvas, 'pointerup', 102, 100)
    })
    expect(captureSpy).not.toHaveBeenCalled()
    expect(canvas.getAttribute('data-drag')).toBe('0')
  })

  it('ignores non-left-button presses entirely', () => {
    const { canvas, captureSpy } = renderProbe()
    act(() => {
      firePointer(canvas, 'pointerdown', 100, 100, 2) // right button
      firePointer(canvas, 'pointermove', 120, 100)
      firePointer(canvas, 'pointerup', 120, 100, 2)
    })
    expect(captureSpy).not.toHaveBeenCalled()
    // No drag state was armed, so the view never moved.
    expect(canvas.getAttribute('data-x')).toBe('0')
  })

  it('engages capture exactly once when the drag threshold is crossed', () => {
    const { canvas, captureSpy } = renderProbe()
    act(() => {
      firePointer(canvas, 'pointerdown', 100, 100)
      firePointer(canvas, 'pointermove', 103, 100) // 3px: below threshold
    })
    expect(captureSpy).not.toHaveBeenCalled()
    act(() => {
      firePointer(canvas, 'pointermove', 106, 100) // 6px: now a pan
      firePointer(canvas, 'pointermove', 110, 100) // further panning
    })
    expect(captureSpy).toHaveBeenCalledTimes(1)
    // The pan commits are rAF-coalesced: flush the frame to see the drag
    // flag and the moved view in the DOM.
    act(() => flushRaf())
    expect(canvas.getAttribute('data-drag')).toBe('1')
    expect(canvas.getAttribute('data-x')).toBe('10')
  })

  it('re-arms cleanly for the next gesture after pointerup', () => {
    const { canvas, captureSpy } = renderProbe()
    act(() => {
      firePointer(canvas, 'pointerdown', 100, 100)
      firePointer(canvas, 'pointermove', 106, 100) // pan
      firePointer(canvas, 'pointerup', 106, 100)
    })
    expect(captureSpy).toHaveBeenCalledTimes(1)
    // The next gesture starts fresh: a plain click must not capture again.
    act(() => {
      firePointer(canvas, 'pointerdown', 100, 100)
      firePointer(canvas, 'pointermove', 102, 100)
      firePointer(canvas, 'pointerup', 102, 100)
    })
    expect(captureSpy).toHaveBeenCalledTimes(1)
    expect(canvas.getAttribute('data-drag')).toBe('0')
  })
})

describe('usePanZoom stale-drag guards', () => {
  it('clears the armed drag when the button is released outside the canvas (sub-threshold, pre-capture)', () => {
    const { canvas, captureSpy } = renderProbe()
    act(() => {
      firePointer(canvas, 'pointerdown', 100, 100)
      firePointer(canvas, 'pointermove', 102, 100) // 2px: below threshold, no capture yet
    })
    // The release lands on a different element (floating zoom toolbar /
    // adjacent panel): the canvas itself never sees the pointerup, but the
    // hook's window-level fallback must clear the armed drag.
    const outsider = document.createElement('div')
    document.body.appendChild(outsider)
    act(() => {
      firePointer(outsider, 'pointerup', 140, 100)
    })
    outsider.remove()
    // Hovering back over the canvas (no buttons held) must not pan.
    act(() => {
      firePointer(canvas, 'pointermove', 160, 100, 0, { buttons: 0 })
      firePointer(canvas, 'pointermove', 200, 100, 0, { buttons: 0 })
    })
    act(() => flushRaf())
    // Only the pre-release wiggle (2px) was ever committed; no hover pan.
    expect(canvas.getAttribute('data-x')).toBe('2')
    expect(canvas.getAttribute('data-drag')).toBe('0')
    expect(captureSpy).not.toHaveBeenCalled()
  })

  it('does not pan when a buttonless hover move arrives for a stale drag', () => {
    // Belt-and-braces ([7]a): even when no pointerup arrives at all — e.g.
    // the button was released outside the OS window — a hover move
    // (buttons === 0) clears the armed drag instead of panning the canvas.
    const { canvas, captureSpy } = renderProbe()
    act(() => {
      firePointer(canvas, 'pointerdown', 100, 100)
      firePointer(canvas, 'pointermove', 300, 100, 0, { buttons: 0 })
    })
    expect(canvas.getAttribute('data-drag')).toBe('0')
    expect(captureSpy).not.toHaveBeenCalled()
    act(() => flushRaf())
    expect(canvas.getAttribute('data-x')).toBe('0')
    // The guard must not break the next real gesture.
    act(() => {
      firePointer(canvas, 'pointerdown', 100, 100)
      firePointer(canvas, 'pointermove', 110, 100)
      firePointer(canvas, 'pointerup', 110, 100)
    })
    expect(canvas.getAttribute('data-x')).toBe('10')
  })

  it('keeps the pan alive when an auxiliary button is released mid-drag', () => {
    const { canvas } = renderProbe()
    act(() => {
      firePointer(canvas, 'pointerdown', 100, 100)
      firePointer(canvas, 'pointermove', 110, 100) // pan engaged
      // Right button clicked and released while LMB stays held: the RMB
      // pointerup (buttons still includes the primary) must not end the pan.
      firePointer(canvas, 'pointerup', 110, 100, 2, { buttons: 1 })
      firePointer(canvas, 'pointermove', 120, 100)
      firePointer(canvas, 'pointerup', 120, 100) // primary released: ends it
    })
    expect(canvas.getAttribute('data-x')).toBe('20')
  })

  it('ignores non-primary pointerdowns (second touch point)', () => {
    const { canvas, captureSpy } = renderProbe()
    act(() => {
      firePointer(canvas, 'pointerdown', 100, 100, 0, { isPrimary: false })
      firePointer(canvas, 'pointermove', 140, 100) // would pan 40px if armed
      firePointer(canvas, 'pointerup', 140, 100)
    })
    act(() => flushRaf())
    expect(canvas.getAttribute('data-x')).toBe('0')
    expect(captureSpy).not.toHaveBeenCalled()
  })
})

describe('usePanZoom wheel handling', () => {
  it('ignores zero-delta wheel events instead of zooming out', () => {
    const { canvas } = renderProbe()
    const zero = new WheelEvent('wheel', {
      cancelable: true,
      deltaX: 0,
      deltaY: 0,
      clientX: 50,
      clientY: 50,
    })
    act(() => {
      canvas.dispatchEvent(zero)
    })
    expect(zero.defaultPrevented).toBe(false)
    expect(canvas.getAttribute('data-scale')).toBe('1')
  })

  it('still zooms and prevents default for real vertical wheel events', () => {
    const { canvas } = renderProbe()
    const wheel = new WheelEvent('wheel', {
      cancelable: true,
      deltaX: 0,
      deltaY: -100,
      clientX: 50,
      clientY: 50,
    })
    act(() => {
      canvas.dispatchEvent(wheel)
    })
    expect(wheel.defaultPrevented).toBe(true)
    expect(canvas.getAttribute('data-scale')).toBe(String(DEFAULT_ZOOM_STEP))
  })

  it('lets a direct zoom mid-drag win over the pending pan frame', () => {
    const { canvas } = renderProbe()
    act(() => {
      firePointer(canvas, 'pointerdown', 100, 100)
      firePointer(canvas, 'pointermove', 120, 100) // pending pan frame: x = 20
    })
    // A wheel zoom lands before the frame flushes (jsdom rects are 0x0, so
    // the anchor is the raw clientX/clientY). It composes on the pending pan
    // via viewRef — 1.25x anchored at (10, 10) over {x: 20} gives x = 22.5 —
    // and cancels the stale frame.
    act(() => {
      canvas.dispatchEvent(
        new WheelEvent('wheel', { cancelable: true, deltaX: 0, deltaY: -100, clientX: 10, clientY: 10 }),
      )
    })
    expect(canvas.getAttribute('data-scale')).toBe('1.25')
    expect(canvas.getAttribute('data-x')).toBe('22.5')
    act(() => flushRaf())
    // The cancelled pan frame must not overwrite the zoom one frame later.
    expect(canvas.getAttribute('data-x')).toBe('22.5')
  })
})

describe('usePanZoom rAF-coalesced pan commits', () => {
  it('coalesces a move burst into one animation frame and one re-render', () => {
    const { canvas } = renderProbe()
    act(() => {
      firePointer(canvas, 'pointerdown', 100, 100)
    })
    act(() => {
      firePointer(canvas, 'pointermove', 110, 100)
      firePointer(canvas, 'pointermove', 120, 100)
      firePointer(canvas, 'pointermove', 130, 100)
    })
    // A single frame is scheduled; nothing hit React state yet.
    expect(scheduledFrames()).toBe(1)
    expect(probeRenders).toBe(0)
    expect(canvas.getAttribute('data-x')).toBe('0')
    // Flushing the frame commits the final position in exactly one render.
    act(() => flushRaf())
    expect(scheduledFrames()).toBe(0)
    expect(probeRenders).toBe(1)
    expect(canvas.getAttribute('data-x')).toBe('30')
    // Moves in the next frame schedule a fresh frame.
    act(() => {
      firePointer(canvas, 'pointermove', 135, 100)
    })
    expect(scheduledFrames()).toBe(1)
  })

  it('commits the pending frame synchronously at gesture end', () => {
    const { canvas } = renderProbe()
    act(() => {
      firePointer(canvas, 'pointerdown', 100, 100)
      firePointer(canvas, 'pointermove', 110, 100)
      firePointer(canvas, 'pointerup', 110, 100)
    })
    // pointerup flushed the frame: the final position is committed without
    // waiting for an animation frame.
    expect(canvas.getAttribute('data-x')).toBe('10')
    expect(scheduledFrames()).toBe(0)
  })
})
