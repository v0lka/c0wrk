// @vitest-environment jsdom
import { describe, it, expect, beforeEach, afterEach } from 'vitest'
import { act } from 'react'
import { createRoot, type Root } from 'react-dom/client'
import { useDropdown } from './useDropdown'

// Mutable handle the probe writes its hook state into on every render, so the
// test can read isOpen and drive setIsOpen without DOM instrumentation.
interface ProbeHandle {
  isOpen: boolean
  setIsOpen: (open: boolean | ((prev: boolean) => boolean)) => void
}

function Probe({ disabled, handle }: { disabled: boolean; handle: ProbeHandle }) {
  const { isOpen, setIsOpen } = useDropdown(disabled)
  handle.isOpen = isOpen
  handle.setIsOpen = setIsOpen
  return <div data-testid="probe" />
}

describe('useDropdown close-on-disabled', () => {
  let container: HTMLDivElement
  let root: Root

  beforeEach(() => {
    container = document.createElement('div')
    document.body.appendChild(container)
    root = createRoot(container)
  })

  afterEach(() => {
    act(() => root.unmount())
    container.remove()
  })

  it('closes an open menu when disabled flips to true', () => {
    const handle: ProbeHandle = { isOpen: false, setIsOpen: () => {} }
    act(() => root.render(<Probe disabled={false} handle={handle} />))

    act(() => handle.setIsOpen(true))
    expect(handle.isOpen).toBe(true)

    act(() => root.render(<Probe disabled handle={handle} />))
    expect(handle.isOpen).toBe(false)
  })

  it('keeps a closed menu closed when disabled flips to true', () => {
    const handle: ProbeHandle = { isOpen: false, setIsOpen: () => {} }
    act(() => root.render(<Probe disabled={false} handle={handle} />))

    act(() => root.render(<Probe disabled handle={handle} />))
    expect(handle.isOpen).toBe(false)
  })

  it('does not reopen the menu when disabled flips back to false', () => {
    const handle: ProbeHandle = { isOpen: false, setIsOpen: () => {} }
    act(() => root.render(<Probe disabled={false} handle={handle} />))
    act(() => handle.setIsOpen(true))
    act(() => root.render(<Probe disabled handle={handle} />))
    expect(handle.isOpen).toBe(false)

    act(() => root.render(<Probe disabled={false} handle={handle} />))
    expect(handle.isOpen).toBe(false)
  })
})
