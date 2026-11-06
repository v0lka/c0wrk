// @vitest-environment jsdom
import { describe, it, expect, vi, beforeEach, afterEach } from 'vitest'
import { act } from 'react'
import { createRoot, type Root } from 'react-dom/client'

// --- Mock @/api/runtime so the transport gate is driven by the tests ---

const { transportSnapshotMock } = vi.hoisted(() => ({
  transportSnapshotMock: vi.fn<() => 'wails' | 'web' | 'none' | 'pending'>(),
}))

vi.mock('@/api/runtime', () => ({ transportSnapshot: transportSnapshotMock }))

import { useCtrlWheelScale } from './useCtrlWheelScale'
import { useUiScaleStore } from '@/stores/uiScaleStore'

let root: Root
let container: HTMLDivElement
// Latest render's hook result — the HUD visibility flag and the card-grab
// controls live in component state, so this is the only way to reach them.
let hud: { visible: boolean; hold: () => void; release: () => void } | null

function Harness() {
  hud = useCtrlWheelScale()
  return null
}

function renderHook(): void {
  container = document.createElement('div')
  document.body.appendChild(container)
  root = createRoot(container)
  act(() => {
    root.render(<Harness />)
  })
}

function unmountHook(): void {
  act(() => {
    root.unmount()
  })
  container.remove()
}

/** Build a wheel event; `preventDefault` is spied so callers can assert it. */
function wheelEvent(deltaY: number, ctrlKey: boolean, metaKey = false) {
  const event = new WheelEvent('wheel', { cancelable: true, deltaY, ctrlKey, metaKey })
  return { event, prevented: vi.spyOn(event, 'preventDefault') }
}

/** Dispatch a wheel event on window (inside act — it can trigger renders). */
function dispatchWheel(deltaY: number, ctrlKey: boolean, metaKey = false) {
  const { event, prevented } = wheelEvent(deltaY, ctrlKey, metaKey)
  act(() => {
    window.dispatchEvent(event)
  })
  return { prevented }
}

/** Dispatch a keyboard event on window. */
function dispatchKey(type: 'keydown' | 'keyup', key: string): void {
  act(() => {
    window.dispatchEvent(new KeyboardEvent(type, { key }))
  })
}

/** Dispatch a window blur (the keyup-swallowing path). */
function dispatchBlur(): void {
  act(() => {
    window.dispatchEvent(new Event('blur'))
  })
}

/** Advance the fake clock inside act (the hide timer calls setState). */
function advance(ms: number): void {
  act(() => {
    vi.advanceTimersByTime(ms)
  })
}

beforeEach(() => {
  vi.useFakeTimers({ toFake: ['setTimeout', 'clearTimeout'] })
  transportSnapshotMock.mockReturnValue('wails')
  useUiScaleStore.setState({ scale: 100 })
})

afterEach(() => {
  unmountHook()
  vi.useRealTimers()
  vi.restoreAllMocks()
})

describe('useCtrlWheelScale — Ctrl+wheel stepping', () => {
  it('steps the scale on ctrl+wheel, shows the HUD and preventDefaults the chord', () => {
    renderHook()
    expect(hud?.visible).toBe(false)

    // Wheel up (deltaY < 0): 100 → +1 wheel step → 110.
    const up = dispatchWheel(-120, true)
    expect(up.prevented).toHaveBeenCalled()
    expect(useUiScaleStore.getState().scale).toBe(110)
    expect(hud?.visible).toBe(true)

    // A second notch with Ctrl still held steps again: 110 → 120.
    dispatchWheel(-1, true)
    expect(useUiScaleStore.getState().scale).toBe(120)
    expect(hud?.visible).toBe(true)
  })

  it('steps down on wheel-down and clamps at the store bounds', () => {
    useUiScaleStore.setState({ scale: 55 })
    renderHook()

    // deltaY > 0 (wheel down) decreases: 55 → snapped to 50-grid already, −10
    // would go below UI_SCALE_MIN — clamps to 50.
    dispatchWheel(50, true)
    expect(useUiScaleStore.getState().scale).toBe(50)
  })

  it('ignores plain wheel: no step, no HUD, no preventDefault', () => {
    renderHook()

    const plain = dispatchWheel(-100, false)
    expect(plain.prevented).not.toHaveBeenCalled()
    expect(useUiScaleStore.getState().scale).toBe(100)
    expect(hud?.visible).toBe(false)
  })
})

describe('useCtrlWheelScale — HUD hide timer', () => {
  it('hides exactly 1000ms after keyup Control', () => {
    renderHook()
    dispatchWheel(-1, true)
    expect(hud?.visible).toBe(true)

    dispatchKey('keyup', 'Control')
    expect(hud?.visible).toBe(true)

    advance(999)
    expect(hud?.visible).toBe(true)
    advance(1)
    expect(hud?.visible).toBe(false)
  })

  it('a repeated ctrl+wheel re-arms the hide 1s after the last chord', () => {
    renderHook()
    dispatchWheel(-1, true)
    dispatchKey('keyup', 'Control')
    advance(600)

    // Fresh wheel while the hide timer is armed: the deadline moves to one
    // delay after THIS chord, not after the original keyup.
    dispatchWheel(-1, true)
    expect(hud?.visible).toBe(true)
    advance(999)
    expect(hud?.visible).toBe(true)
    advance(1)
    expect(hud?.visible).toBe(false)
  })

  it('held-modifier keydown auto-repeat keeps the HUD up past the wheel deadline', () => {
    renderHook()
    dispatchWheel(-1, true)
    advance(500)

    // Holding Ctrl fires repeating keydowns; each cancels the timer armed by
    // the wheel, so the HUD never disappears mid-gesture.
    dispatchKey('keydown', 'Control')
    advance(700)
    expect(hud?.visible).toBe(true)

    // The release re-arms the hide and it completes.
    dispatchKey('keyup', 'Control')
    advance(1000)
    expect(hud?.visible).toBe(false)
  })

  it('keydown Control while visible cancels the pending hide', () => {
    renderHook()
    dispatchWheel(-1, true)
    dispatchKey('keyup', 'Control')

    dispatchKey('keydown', 'Control')
    advance(1000)
    expect(hud?.visible).toBe(true)

    // The next release re-arms the hide and it completes.
    dispatchKey('keyup', 'Control')
    advance(1000)
    expect(hud?.visible).toBe(false)
  })

  it('hides 1000ms after a window blur that swallowed the keyup', () => {
    renderHook()
    dispatchWheel(-1, true)
    expect(hud?.visible).toBe(true)

    dispatchBlur()
    advance(999)
    expect(hud?.visible).toBe(true)
    advance(1)
    expect(hud?.visible).toBe(false)
  })

  it('blur without a visible HUD arms no timer', () => {
    renderHook()
    dispatchBlur()
    expect(vi.getTimerCount()).toBe(0)
    expect(hud?.visible).toBe(false)
  })
})

describe('useCtrlWheelScale — transport gate', () => {
  it.each(['none', 'web', 'pending'] as const)(
    'attaches no listeners when the transport is %s',
    (mode) => {
      transportSnapshotMock.mockReturnValue(mode)
      renderHook()

      dispatchWheel(-100, true)
      expect(useUiScaleStore.getState().scale).toBe(100)
      expect(hud?.visible).toBe(false)

      // The keyboard/blur paths are equally inert — no timers scheduled.
      dispatchKey('keydown', 'Control')
      dispatchKey('keyup', 'Control')
      dispatchBlur()
      expect(vi.getTimerCount()).toBe(0)
    },
  )
})

describe('useCtrlWheelScale — cleanup', () => {
  it('removes the listeners and cancels the pending hide on unmount', () => {
    renderHook()
    dispatchWheel(-1, true)
    dispatchKey('keyup', 'Control')

    unmountHook()
    // No hide timer left behind (StrictMode-safe teardown).
    expect(vi.getTimerCount()).toBe(0)

    // A post-unmount chord reaches nothing: no step, no state write.
    useUiScaleStore.setState({ scale: 100 })
    const late = dispatchWheel(-1, true)
    expect(late.prevented).not.toHaveBeenCalled()
    expect(useUiScaleStore.getState().scale).toBe(100)
  })

  it('remounts cleanly: a fresh mount shows and steps again from scratch', () => {
    renderHook()
    dispatchWheel(-1, true)
    dispatchKey('keyup', 'Control')
    unmountHook()

    renderHook()
    expect(hud?.visible).toBe(false)

    dispatchWheel(-1, true)
    expect(hud?.visible).toBe(true)
    expect(useUiScaleStore.getState().scale).toBe(120)
  })
})

describe('useCtrlWheelScale — macOS: Cmd is the zoom modifier', () => {
  beforeEach(() => {
    // jsdom seeds navigator.platform from the host running the tests; pin
    // macOS explicitly (the outer afterEach restores all mocks).
    vi.spyOn(navigator, 'platform', 'get').mockReturnValue('MacIntel')
  })

  it('steps on Cmd+wheel, preventDefaults it and hides 1s after keyup Meta', () => {
    renderHook()

    const up = dispatchWheel(-1, false, true)
    expect(up.prevented).toHaveBeenCalled()
    expect(useUiScaleStore.getState().scale).toBe(110)
    expect(hud?.visible).toBe(true)

    dispatchKey('keyup', 'Meta')
    advance(999)
    expect(hud?.visible).toBe(true)
    advance(1)
    expect(hud?.visible).toBe(false)
  })

  it('accepts a plain Ctrl+wheel too (WebKit pinch reports synthetic ctrlKey) and hides after the last notch', () => {
    renderHook()

    const pinch = dispatchWheel(-1, true)
    expect(pinch.prevented).toHaveBeenCalled()
    expect(useUiScaleStore.getState().scale).toBe(110)
    expect(hud?.visible).toBe(true)

    // A pinch fires no keyup and no blur: each notch re-arms the hide, so it
    // lands exactly one delay after the LAST wheel event.
    dispatchWheel(-1, true)
    advance(999)
    expect(hud?.visible).toBe(true)
    advance(1)
    expect(hud?.visible).toBe(false)
  })

  it('Meta keydown auto-repeat keeps the HUD up; releasing Cmd hides it', () => {
    renderHook()
    dispatchWheel(-1, false, true)
    advance(500)

    dispatchKey('keydown', 'Meta')
    advance(700)
    expect(hud?.visible).toBe(true)

    dispatchKey('keyup', 'Meta')
    advance(1000)
    expect(hud?.visible).toBe(false)
  })
})

describe('useCtrlWheelScale — non-mac: Cmd keeps its native meaning', () => {
  it('ignores a Cmd-only wheel on Linux/Windows', () => {
    vi.spyOn(navigator, 'platform', 'get').mockReturnValue('Linux x86_64')
    renderHook()

    const cmd = dispatchWheel(-100, false, true)
    expect(cmd.prevented).not.toHaveBeenCalled()
    expect(useUiScaleStore.getState().scale).toBe(100)
    expect(hud?.visible).toBe(false)
    expect(vi.getTimerCount()).toBe(0)
  })
})

describe('useCtrlWheelScale — inert zero-delta wheels', () => {
  it('ignores a deltaY === 0 chord (deltaX-only gesture): untouched, no step, no HUD', () => {
    renderHook()

    // The momentum-tail shape: deltaX carries the input, deltaY is 0 — no
    // zoom direction, so the event must not preventDefault, show the HUD,
    // step the scale or arm a hide timer.
    const inert = dispatchWheel(0, true)
    expect(inert.prevented).not.toHaveBeenCalled()
    expect(useUiScaleStore.getState().scale).toBe(100)
    expect(hud?.visible).toBe(false)
    expect(vi.getTimerCount()).toBe(0)
  })
})

describe('useCtrlWheelScale — chord swallowing over canvas handlers', () => {
  it('stopPropagation keeps a canvas-level wheel handler from seeing the chord', () => {
    renderHook()

    // A canvas-style native listener deeper in the tree (the usePanZoom
    // shape): without the swallow it would zoom the canvas on the same
    // gesture that steps the app scale.
    const canvas = document.createElement('div')
    container.appendChild(canvas)
    let canvasHits = 0
    const onCanvasWheel = (): void => {
      canvasHits++
    }
    canvas.addEventListener('wheel', onCanvasWheel)

    const event = new WheelEvent('wheel', {
      cancelable: true,
      deltaY: -1,
      ctrlKey: true,
      bubbles: true,
    })
    act(() => {
      canvas.dispatchEvent(event)
    })
    canvas.removeEventListener('wheel', onCanvasWheel)

    expect(canvasHits).toBe(0)
    expect(useUiScaleStore.getState().scale).toBe(110)
    expect(hud?.visible).toBe(true)
  })
})

describe('useCtrlWheelScale — HUD hold (card grab pauses the hide)', () => {
  it('hold pauses the hide past its deadline; release re-arms it', () => {
    renderHook()
    dispatchWheel(-1, true)
    dispatchKey('keyup', 'Control')

    act(() => {
      hud?.hold()
    })
    advance(3000)
    expect(hud?.visible).toBe(true)

    act(() => {
      hud?.release()
    })
    advance(999)
    expect(hud?.visible).toBe(true)
    advance(1)
    expect(hud?.visible).toBe(false)
  })

  it('hide re-arm signals (keyup, wheel, blur) are inert while held', () => {
    renderHook()
    dispatchWheel(-1, true)

    act(() => {
      hud?.hold()
    })
    dispatchKey('keyup', 'Control')
    dispatchWheel(-1, true)
    dispatchBlur()
    advance(3000)
    expect(hud?.visible).toBe(true)

    // Releasing after all of that arms a fresh hide from now.
    act(() => {
      hud?.release()
    })
    advance(1000)
    expect(hud?.visible).toBe(false)
  })

  it('hold without a visible HUD and release without hold are inert', () => {
    renderHook()

    act(() => {
      hud?.hold()
    })
    act(() => {
      hud?.release()
    })
    dispatchBlur()
    expect(vi.getTimerCount()).toBe(0)
    expect(hud?.visible).toBe(false)
  })
})
