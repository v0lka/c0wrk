// Tests for the panel default-width helpers (#138 zoom-safety): both helpers
// derive their default from window.innerWidth, which is VISUAL px under the
// UI Scale (CSS zoom on <html>), while the value feeds a LAYOUT-px panel
// width. Each helper must divide by the live zoom factor so the default is
// correct at any scale — and identical to the raw value at 100 %, where the
// compensation is an identity.
//
// getUiZoomFactor is mocked (keeping the rest of the real module via
// importOriginal) so the zoom is controlled per case without touching the
// uiScaleStore's persisted state.

// @vitest-environment jsdom
import { describe, it, expect, vi, beforeEach, afterEach } from 'vitest'

const { zoomRef } = vi.hoisted(() => ({ zoomRef: { value: 1 } }))

vi.mock('@/stores/uiScaleStore', async (importOriginal) => {
  const actual = await importOriginal<typeof import('@/stores/uiScaleStore')>()
  return {
    ...actual,
    getUiZoomFactor: () => zoomRef.value,
  }
})

import { getDefaultSidebarWidth, SIDEBAR_MIN, SIDEBAR_MAX } from './uiStore'
import { getDefaultViewerWidth } from './fileViewerStore'

describe('panel default widths (UI-scale zoom safety)', () => {
  const realInnerWidth = window.innerWidth

  beforeEach(() => {
    zoomRef.value = 1
  })

  afterEach(() => {
    window.innerWidth = realInnerWidth
  })

  it('at 100 % zoom the defaults are the documented fractions of the visual width', () => {
    window.innerWidth = 1440
    expect(getDefaultSidebarWidth()).toBe(288) // 1440 / 5
    expect(getDefaultViewerWidth()).toBe(576) // 1440 × 2/5
  })

  it('at 150 % zoom the defaults are computed from LAYOUT px, not VISUAL px', () => {
    window.innerWidth = 1440 // VISUAL px = layout 960 × 1.5
    zoomRef.value = 1.5
    expect(getDefaultSidebarWidth()).toBe(192) // 960 / 5 — NOT 288
    expect(getDefaultViewerWidth()).toBe(384) // 960 × 2/5 — NOT 576
  })

  it('at 50 % zoom the visual width is scaled UP to layout px before use', () => {
    window.innerWidth = 1440 // VISUAL px = layout 2880 × 0.5
    zoomRef.value = 0.5
    expect(getDefaultSidebarWidth()).toBe(SIDEBAR_MAX) // 2880/5 clamps high
    expect(getDefaultViewerWidth()).toBe(900)
  })

  it('the clamps still apply in both directions', () => {
    zoomRef.value = 1
    window.innerWidth = 500
    expect(getDefaultSidebarWidth()).toBe(SIDEBAR_MIN) // 100 → floor 180
    expect(getDefaultViewerWidth()).toBe(250) // 200 → floor 250
    window.innerWidth = 20000
    expect(getDefaultSidebarWidth()).toBe(SIDEBAR_MAX) // 4000 → ceiling 500
    expect(getDefaultViewerWidth()).toBe(900) // 8000 → ceiling 900
  })

  it('rounds to integer layout px at a fractional zoom', () => {
    window.innerWidth = 1000 // layout 909.09… × 1.1
    zoomRef.value = 1.1
    expect(Number.isInteger(getDefaultSidebarWidth())).toBe(true)
    expect(Number.isInteger(getDefaultViewerWidth())).toBe(true)
    expect(getDefaultSidebarWidth()).toBe(Math.round(1000 / 1.1 / 5))
    expect(getDefaultViewerWidth()).toBe(Math.round((1000 / 1.1) * 2 / 5))
  })
})
