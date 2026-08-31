// @vitest-environment jsdom
import { describe, it, expect, afterEach } from 'vitest'
import { act } from 'react'
import { createRoot, type Root } from 'react-dom/client'
import { useShiftHeld } from './useShiftHeld'

// Mutable handle the probe writes its hook state into on every render, so the
// test can read the Shift state without DOM instrumentation (mirrors
// useDropdown.test.tsx).
interface ProbeHandle {
  held: boolean
}

function Probe({ handle }: { handle: ProbeHandle }) {
  handle.held = useShiftHeld()
  return <div data-testid="probe" />
}

const pressShift = () =>
  act(() => {
    window.dispatchEvent(new KeyboardEvent('keydown', { key: 'Shift' }))
  })

const releaseShift = () =>
  act(() => {
    window.dispatchEvent(new KeyboardEvent('keyup', { key: 'Shift' }))
  })

describe('useShiftHeld', () => {
  let container: HTMLDivElement
  let root: Root
  const handle: ProbeHandle = { held: false }

  function mount() {
    container = document.createElement('div')
    document.body.appendChild(container)
    root = createRoot(container)
    act(() => root.render(<Probe handle={handle} />))
  }

  afterEach(() => {
    // Reset the shared module state so tests stay isolated. This must happen
    // BEFORE unmount: unmounting detaches the store's window listeners, so a
    // keyup/blur dispatched afterwards never reaches it and a held Shift
    // would leak into the next test.
    releaseShift()
    act(() => {
      window.dispatchEvent(new Event('blur'))
    })
    act(() => root.unmount())
    container.remove()
    handle.held = false
  })

  it('starts not held', () => {
    mount()
    expect(handle.held).toBe(false)
  })

  it('flips to true on Shift keydown and back on keyup', () => {
    mount()
    pressShift()
    expect(handle.held).toBe(true)
    releaseShift()
    expect(handle.held).toBe(false)
  })

  it('ignores non-Shift keys', () => {
    mount()
    act(() => {
      window.dispatchEvent(new KeyboardEvent('keydown', { key: 'Control' }))
    })
    expect(handle.held).toBe(false)
  })

  it('resets when the window blurs while Shift is held (swallowed keyup)', () => {
    mount()
    pressShift()
    expect(handle.held).toBe(true)
    act(() => {
      window.dispatchEvent(new Event('blur'))
    })
    expect(handle.held).toBe(false)
  })

  it('keeps all subscribers in sync through a single listener set', () => {
    const second: ProbeHandle = { held: false }
    mount()
    act(() => root.render(
      <>
        <Probe handle={handle} />
        <Probe handle={second} />
      </>,
    ))
    pressShift()
    expect(handle.held).toBe(true)
    expect(second.held).toBe(true)
    releaseShift()
    expect(handle.held).toBe(false)
    expect(second.held).toBe(false)
  })
})
