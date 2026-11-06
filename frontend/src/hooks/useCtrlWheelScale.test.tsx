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
// Latest render's hook result — the HUD visibility flag lives in component
// state, so this is the only way to observe it.
let hud: { visible: boolean } | null

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
function wheelEvent(deltaY: number, ctrlKey: boolean) {
  const event = new WheelEvent('wheel', { cancelable: true, deltaY, ctrlKey })
  return { event, prevented: vi.spyOn(event, 'preventDefault') }
}

/** Dispatch a wheel event on window (inside act — it can trigger renders). */
function dispatchWheel(deltaY: number, ctrlKey: boolean) {
  const { event, prevented } = wheelEvent(deltaY, ctrlKey)
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

  it('a repeated ctrl+wheel cancels the pending hide', () => {
    renderHook()
    dispatchWheel(-1, true)
    dispatchKey('keyup', 'Control')
    advance(600)

    // Fresh wheel while the hide timer is armed: the HUD must stay up well
    // past the original 1000ms deadline.
    dispatchWheel(-1, true)
    expect(hud?.visible).toBe(true)
    advance(2000)
    expect(hud?.visible).toBe(true)
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
