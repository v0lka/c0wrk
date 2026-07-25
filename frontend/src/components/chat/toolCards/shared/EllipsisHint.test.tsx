// @vitest-environment jsdom
import { describe, it, expect, vi, beforeEach, afterEach } from 'vitest'
import { act } from 'react'
import { createRoot, type Root } from 'react-dom/client'

vi.hoisted(() => {
  ;(globalThis as Record<string, unknown>).IS_REACT_ACT_ENVIRONMENT = true
})

import { EllipsisHint } from './EllipsisHint'
import { TooltipProvider } from '@/components/ui/tooltip'

describe('EllipsisHint', () => {
  let container: HTMLElement
  let root: Root

  beforeEach(() => {
    // Radix tooltip positioning uses ResizeObserver, which jsdom lacks.
    vi.stubGlobal('ResizeObserver', class {
      observe() {}
      unobserve() {}
      disconnect() {}
    })
    container = document.createElement('div')
    document.body.replaceChildren(container)
    root = createRoot(container)
  })

  afterEach(() => {
    vi.unstubAllGlobals()
    vi.useRealTimers()
  })

  const render = (el: React.ReactElement) =>
    act(() => {
      root.render(<TooltipProvider>{el}</TooltipProvider>)
    })

  it('renders the visible children', () => {
    render(
      <EllipsisHint fullText="the full long value" className="truncate">
        short
      </EllipsisHint>,
    )
    expect(container.textContent).toContain('short')
  })

  it('applies the truncating classes (block + min-w-0 + className)', () => {
    render(
      <EllipsisHint fullText="full" className="text-sm truncate">
        short
      </EllipsisHint>,
    )
    const span = container.querySelector('span')
    expect(span).not.toBeNull()
    expect(span!.className).toContain('block')
    expect(span!.className).toContain('min-w-0')
    expect(span!.className).toContain('truncate')
    expect(span!.className).toContain('text-sm')
  })

  it('renders a plain span without a tooltip when fullText is empty', () => {
    render(
      <EllipsisHint fullText="" className="truncate">
        short
      </EllipsisHint>,
    )
    // No Radix tooltip trigger mounted — plain span only.
    expect(container.querySelector('[data-slot="tooltip-trigger"]')).toBeNull()
    expect(container.textContent).toContain('short')
  })

  it('mounts the Radix tooltip trigger when fullText is provided', () => {
    render(
      <EllipsisHint fullText="the full long value" className="truncate">
        short
      </EllipsisHint>,
    )
    // The Trigger's data-slot is merged onto the span via asChild.
    expect(container.querySelector('[data-slot="tooltip-trigger"]')).not.toBeNull()
  })

  it('reveals the full value in a wrapping tooltip on hover when alwaysShow', () => {
    vi.useFakeTimers()
    const full = 'very long untruncated value here that should wrap'
    render(
      <EllipsisHint fullText={full} alwaysShow className="truncate">
        short
      </EllipsisHint>,
    )
    const trigger = container.querySelector('[data-slot="tooltip-trigger"]') as HTMLElement
    act(() => {
      trigger.dispatchEvent(new PointerEvent('pointerenter', { bubbles: true, pointerType: 'mouse' }))
      trigger.dispatchEvent(new PointerEvent('pointermove', { bubbles: true, pointerType: 'mouse' }))
      vi.advanceTimersByTime(400)
    })
    // Radix portals the content to document.body; it must escape the container
    // and contain the full untruncated value.
    expect(document.body.textContent).toContain(full)
  })
})
