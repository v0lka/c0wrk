import { describe, it, expect } from 'vitest'
import {
  DEFAULT_MAX_SCALE,
  DEFAULT_MIN_SCALE,
  DEFAULT_ZOOM_STEP,
  DRAG_CLICK_THRESHOLD_PX,
  INITIAL_VIEW,
  clampScale,
  computeFitView,
  isHorizontalWheelGesture,
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
