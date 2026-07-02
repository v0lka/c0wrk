import { describe, it, expect } from 'vitest'
import { computeDropdownPosition } from './dropdownPosition'

// All rects are in viewport space (pixels), matching getBoundingClientRect().
const TRIGGER_HEIGHT = 30

function trigger({ top, left = 100, width = 120, height = TRIGGER_HEIGHT }: {
  top: number
  left?: number
  width?: number
  height?: number
}) {
  return { top, bottom: top + height, left, width }
}

describe('computeDropdownPosition', () => {
  it('opens downward when there is room below the trigger', () => {
    const pos = computeDropdownPosition({
      triggerRect: trigger({ top: 100 }),
      dropdownHeight: 200,
      viewportHeight: 800,
      viewportWidth: 1000,
    })
    // spaceBelow = 800 - 130 = 670 >= 200
    expect(pos.direction).toBe('down')
    expect(pos.top).toBe(130 + 6) // triggerRect.bottom + gap
    expect(pos.left).toBe(100)
  })

  it('flips upward when space below is insufficient and space above is larger', () => {
    // Trigger near the bottom of the viewport (like the chat toolbar).
    const pos = computeDropdownPosition({
      triggerRect: trigger({ top: 700 }),
      dropdownHeight: 200,
      viewportHeight: 800,
      viewportWidth: 1000,
    })
    // spaceBelow = 800 - 730 = 70 (< 200); spaceAbove = 700 -> flip up
    expect(pos.direction).toBe('up')
    expect(pos.top).toBe(700 - 200 - 6) // triggerRect.top - height - gap
    expect(pos.left).toBe(100)
  })

  it('prefers the larger side when neither fully fits', () => {
    // Both sides smaller than the menu; below (120) is larger than above (50).
    const pos = computeDropdownPosition({
      triggerRect: trigger({ top: 50 }),
      dropdownHeight: 300,
      viewportHeight: 200,
      viewportWidth: 1000,
    })
    // spaceBelow = 200 - 80 = 120; spaceAbove = 50. 120 >= 50 -> stay down.
    expect(pos.direction).toBe('down')
    expect(pos.top).toBe(80 + 6)
  })

  it('clamps the upward top so the menu never slides off the top edge', () => {
    // Trigger high up but with even less room below -> upward, but would go negative.
    const pos = computeDropdownPosition({
      triggerRect: trigger({ top: 10 }),
      dropdownHeight: 256,
      viewportHeight: 45, // tiny viewport: spaceBelow = 45 - 40 = 5 < spaceAbove (10)
      viewportWidth: 1000,
    })
    expect(pos.direction).toBe('up')
    // 10 - 256 - 6 = -252, clamped to margin (8)
    expect(pos.top).toBe(8)
  })

  it('clamps horizontally so the menu stays within the viewport', () => {
    const pos = computeDropdownPosition({
      triggerRect: trigger({ top: 100, left: 900, width: 100 }),
      dropdownHeight: 200,
      viewportHeight: 800,
      viewportWidth: 1000,
    })
    // width = max(100, 288) = 288; 900 + 288 = 1188 > 1000 - 8 -> clamp
    expect(pos.width).toBe(288)
    expect(pos.left).toBe(1000 - 288 - 8) // 704
  })

  it('does not clamp horizontally when the menu already fits', () => {
    const pos = computeDropdownPosition({
      triggerRect: trigger({ top: 100, left: 10, width: 100 }),
      dropdownHeight: 200,
      viewportHeight: 800,
      viewportWidth: 1000,
    })
    expect(pos.left).toBe(10)
    expect(pos.width).toBe(288) // minWidth wins over the narrow trigger
  })

  it('uses the trigger width when it exceeds minWidth', () => {
    const pos = computeDropdownPosition({
      triggerRect: trigger({ top: 100, width: 400 }),
      dropdownHeight: 200,
      viewportHeight: 800,
      viewportWidth: 1000,
    })
    expect(pos.width).toBe(400)
  })

  it('respects a custom gap', () => {
    const pos = computeDropdownPosition({
      triggerRect: trigger({ top: 100 }),
      dropdownHeight: 200,
      viewportHeight: 800,
      viewportWidth: 1000,
      gap: 12,
    })
    expect(pos.top).toBe(130 + 12)
  })

  it('applies the gap on upward placement', () => {
    const pos = computeDropdownPosition({
      triggerRect: trigger({ top: 700 }),
      dropdownHeight: 200,
      viewportHeight: 800,
      viewportWidth: 1000,
      gap: 10,
    })
    expect(pos.direction).toBe('up')
    expect(pos.top).toBe(700 - 200 - 10)
  })
})
