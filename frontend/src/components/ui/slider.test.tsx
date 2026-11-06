// @vitest-environment jsdom
//
// Slider primitive — Radix visual wrapper tests.
//
// The a11y/interaction core (role=slider, aria-valuemin/max/now, Arrow
// stepping, pointer drag) is Radix's own contract; these tests pin what
// c0wrk adds on top: the wrapper renders the primitive with the design-token
// classes, the keyboard steps honor the `step` prop, and a controlled value
// round-trips through onValueChange. Rendering needs `requestAnimationFrame`
// (Radix updates geometry on rAF), so the suite uses the shared fake-timer
// harness the other Radix component suites use.

import { describe, it, expect, vi, beforeEach, afterEach } from 'vitest'
import * as React from 'react'
import { act } from 'react'
import { createRoot, type Root } from 'react-dom/client'

vi.mock('@/lib/platform', () => ({ isMacHost: () => false }))

// jsdom lacks ResizeObserver, which Radix's use-size hook (slider geometry)
// requires on mount. A record-only stub is enough: the tests never resize.
class ResizeObserverStub {
  observe(): void {}
  unobserve(): void {}
  disconnect(): void {}
}
beforeEach(() => {
  window.ResizeObserver = window.ResizeObserver ?? (ResizeObserverStub as unknown as typeof ResizeObserver)
  if (!('fonts' in document)) {
    Object.defineProperty(document, 'fonts', { value: { ready: Promise.resolve() } })
  }
})

import { Slider } from './slider'

let container: HTMLDivElement
let root: Root

/**
 * Radix slider's internal context always carries `values: number[]` — a
 * scalar `value` prop is NOT normalized upstream, and `Range` crashes on
 * `values.map`. The tests (and the Scale HUD, whose store value is a plain
 * number) must therefore pass `[number]` arrays.
 */
const toValues = (v: number | number[]): number[] => (Array.isArray(v) ? v : [v])

beforeEach(() => {
  container = document.createElement('div')
  document.body.appendChild(container)
  root = createRoot(container)
  vi.useFakeTimers()
})

afterEach(() => {
  act(() => {
    root.unmount()
  })
  container.remove()
  document.body.innerHTML = ''
  vi.useRealTimers()
})

function sliderEl(): HTMLElement {
  const el = container.querySelector<HTMLElement>('[role="slider"]')
  expect(el).not.toBeNull()
  return el!
}

function track(): HTMLElement {
  const el = container.querySelector<HTMLElement>('[data-slot="slider-track"]')
  expect(el).not.toBeNull()
  return el!
}

function range(): HTMLElement {
  const el = container.querySelector<HTMLElement>('[data-slot="slider-range"]')
  expect(el).not.toBeNull()
  return el!
}

function thumb(): HTMLElement {
  const el = container.querySelector<HTMLElement>('[data-slot="slider-thumb"]')
  expect(el).not.toBeNull()
  return el!
}

describe('Slider', () => {
  it('exposes the Radix slider ARIA contract', () => {
    act(() => {
      root.render(<Slider min={50} max={200} step={5} value={[100]} aria-label="UI scale" />)
    })
    const el = sliderEl()
    expect(el.getAttribute('aria-valuemin')).toBe('50')
    expect(el.getAttribute('aria-valuemax')).toBe('200')
    expect(el.getAttribute('aria-valuenow')).toBe('100')
    // The role=slider element is the THUMB (carrying the value attributes);
    // the root span keeps the free-form aria-label.
    expect(container.querySelector<HTMLElement>('[data-slot="slider"]')!.getAttribute('aria-label')).toBe('UI scale')
  })

  it('carries the design-token classes on its parts', () => {
    act(() => {
      root.render(<Slider min={50} max={200} step={5} value={[100]} />)
    })
    expect(track().className).toContain('bg-secondary')
    expect(range().className).toContain('bg-primary')
    expect(thumb().className).toContain('rounded-full')
  })

  it('steps by the step prop on ArrowRight and clamps at max', () => {
    const onValueChange = vi.fn()
    act(() => {
      root.render(
        <Slider min={50} max={200} step={5} value={[195]} onValueChange={onValueChange} />,
      )
    })
    const el = sliderEl()
    act(() => {
      el.focus()
    })
    act(() => {
      el.dispatchEvent(new KeyboardEvent('keydown', { key: 'ArrowRight', bubbles: true }))
    })
    expect(onValueChange).toHaveBeenCalledWith([200])
  })

  it('updates aria-valuenow from a controlled value change', () => {
    function Harness(): React.ReactElement {
      const [value, setValue] = React.useState(100)
      return (
        <Slider min={50} max={200} step={5} value={toValues(value)} onValueChange={(v) => setValue(v[0] ?? value)} />
      )
    }
    act(() => {
      root.render(<Harness />)
    })
    expect(sliderEl().getAttribute('aria-valuenow')).toBe('100')
    // ArrowDown is a decrease step: 100 - 5 = 95 (jsdom keyboard events are
    // handled by Radix's own keydown handling).
    act(() => {
      sliderEl().dispatchEvent(new KeyboardEvent('keydown', { key: 'ArrowDown', bubbles: true }))
    })
    expect(sliderEl().getAttribute('aria-valuenow')).toBe('95')
  })

  it('supports two thumbs when the value array has two entries', () => {
    act(() => {
      root.render(<Slider min={0} max={100} step={1} value={[20, 80]} />)
    })
    expect(container.querySelectorAll('[data-slot="slider-thumb"]')).toHaveLength(2)
  })
})
