// @vitest-environment jsdom
import { describe, it, expect, vi, beforeEach, afterEach } from 'vitest'
import { act } from 'react'
import { createRoot, type Root } from 'react-dom/client'

import { EllipsisHint } from './EllipsisHint'
import { TooltipProvider, TOOLTIP_DELAY_MS } from '@/components/ui/tooltip'

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
    // vi.useFakeTimers() does not fake requestAnimationFrame; Radix schedules
    // post-open updates (popper position, presence) via rAF, which would fire
    // after the act() block and warn. Run frames synchronously instead.
    vi.stubGlobal('requestAnimationFrame', (cb: FrameRequestCallback): number => {
      cb(0)
      return 0
    })
    vi.stubGlobal('cancelAnimationFrame', () => {})
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

  it('applies the truncating classes (min-w-0 + className)', () => {
    render(
      <EllipsisHint fullText="full" className="text-sm truncate">
        short
      </EllipsisHint>,
    )
    const span = container.querySelector('span')
    expect(span).not.toBeNull()
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

  it('reveals the full value in a wrapping tooltip on hover when alwaysShow', async () => {
    vi.useFakeTimers()
    const full = 'very long untruncated value here that should wrap'
    render(
      <EllipsisHint fullText={full} alwaysShow className="truncate">
        short
      </EllipsisHint>,
    )
    const trigger = container.querySelector('[data-slot="tooltip-trigger"]') as HTMLElement
    // Async act: flushes microtask-scheduled Radix updates (popper/presence)
    // inside the act scope instead of after it.
    await act(async () => {
      trigger.dispatchEvent(new PointerEvent('pointerenter', { bubbles: true, pointerType: 'mouse' }))
      trigger.dispatchEvent(new PointerEvent('pointermove', { bubbles: true, pointerType: 'mouse' }))
      vi.advanceTimersByTime(TOOLTIP_DELAY_MS)
    })
    // Radix portals the content to document.body; it must escape the container
    // and contain the full untruncated value.
    expect(document.body.textContent).toContain(full)
  })
})
