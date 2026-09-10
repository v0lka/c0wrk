// @vitest-environment jsdom
import { describe, it, expect, vi, beforeEach, afterEach } from 'vitest'
import { act } from 'react'
import { createRoot, type Root } from 'react-dom/client'

import { ResearchDagCanvas } from './ResearchDagCanvas'
import { layoutDag } from './researchDagRender'
import { TooltipProvider, TOOLTIP_DELAY_MS } from '@/components/ui/tooltip'
import type { HypothesisGraph } from '@/types/models'

// Two nodes: H-001 carries long-form card sections (its SVG label truncates
// at LABEL_MAX_CHARS, so the full untruncated title is a tooltip-only probe);
// H-002 is a bare node (header + meta only, undecided / parentless fallbacks).
const GRAPH: HypothesisGraph = {
  nodes: [
    {
      id: 'H-001',
      title: 'Root hypothesis with a long untruncated title',
      status: 'in-progress',
      decision: 'pivot',
      timebox: '2 weeks',
      parents: ['H-000'],
      statement: 'The hover reveals a markdown card.',
      verification_criterion: 'Tooltip shows the statement',
    },
    {
      id: 'H-002',
      title: 'Child',
      status: 'open',
      parents: ['H-001'],
    },
  ],
  edges: [{ from: 'H-001', to: 'H-002' }],
}

/** Tooltip-only probe: H-001's full title (the painted label truncates). */
const H1_CARD_PROBE = 'Root hypothesis with a long untruncated title'
/** Tooltip-only probe: H-001's statement body (only the card renders it). */
const H1_STATEMENT_PROBE = 'The hover reveals a markdown card.'

describe('ResearchDagCanvas — node hover tooltip', () => {
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
    act(() => {
      root.unmount()
    })
    vi.unstubAllGlobals()
    vi.useRealTimers()
  })

  const renderCanvas = () =>
    act(() => {
      root.render(
        <TooltipProvider>
          <ResearchDagCanvas
            layout={layoutDag(GRAPH)}
            nodes={GRAPH.nodes}
            selectedId={null}
            onSelect={vi.fn()}
          />
        </TooltipProvider>,
      )
    })

  const hover = async (id: string) => {
    const trigger = container.querySelector(`[data-node-id="${id}"]`) as Element
    // Async act: flushes microtask-scheduled Radix updates (popper/presence)
    // inside the act scope instead of after it.
    await act(async () => {
      trigger.dispatchEvent(
        new PointerEvent('pointerenter', { bubbles: true, pointerType: 'mouse' }),
      )
      trigger.dispatchEvent(
        new PointerEvent('pointermove', { bubbles: true, pointerType: 'mouse' }),
      )
      vi.advanceTimersByTime(TOOLTIP_DELAY_MS)
    })
  }

  it('mounts the Radix tooltip trigger on each node and drops the native SVG title', () => {
    renderCanvas()
    // The custom tooltip replaces the native <title> (which would double up).
    expect(container.querySelector('svg title')).toBeNull()
    // The Trigger's data-slot is merged onto the node <g> via asChild.
    const node = container.querySelector('[data-node-id="H-001"]')
    expect(node).not.toBeNull()
    expect(node!.getAttribute('data-slot')).toBe('tooltip-trigger')
  })

  it('does not open before the default provider delay elapses', async () => {
    vi.useFakeTimers()
    renderCanvas()
    const trigger = container.querySelector('[data-node-id="H-001"]') as Element
    await act(async () => {
      trigger.dispatchEvent(
        new PointerEvent('pointerenter', { bubbles: true, pointerType: 'mouse' }),
      )
      trigger.dispatchEvent(
        new PointerEvent('pointermove', { bubbles: true, pointerType: 'mouse' }),
      )
      vi.advanceTimersByTime(TOOLTIP_DELAY_MS - 1)
    })
    expect(document.body.textContent).not.toContain(H1_STATEMENT_PROBE)
  })

  it('opens the markdown hypothesis card after the default delay', async () => {
    vi.useFakeTimers()
    renderCanvas()
    await hover('H-001')
    // Radix portals the content to document.body; it must escape the
    // container and carry the full card: heading with the untruncated title,
    // the Status/Decision/Timebox/Parents meta line, then the long-form
    // sections in the methodology card's order.
    const body = document.body.textContent ?? ''
    expect(body).toContain(`H-001: ${H1_CARD_PROBE}`)
    expect(body).toContain('Status:')
    expect(body).toContain('in-progress')
    expect(body).toContain('Decision:')
    expect(body).toContain('pivot')
    expect(body).toContain('Timebox:')
    expect(body).toContain('2 weeks')
    expect(body).toContain('Parents:')
    expect(body).toContain('H-000')
    expect(body).toContain('Statement')
    expect(body).toContain(H1_STATEMENT_PROBE)
    expect(body).toContain('Verification Criterion')
    expect(body).toContain('Tooltip shows the statement')
  })

  it('renders the hovered node\u2019s own card (bare node renders undecided/parentless meta)', async () => {
    vi.useFakeTimers()
    renderCanvas()
    // Fresh canvas, bare node hovered first: its card must show ITS OWN
    // content — header + meta with the undecided / parentless fallbacks —
    // and none of H-001's long-form probes.
    await hover('H-002')
    const body = document.body.textContent ?? ''
    expect(body).toContain('H-002: Child')
    expect(body).toContain('Status:')
    expect(body).toContain('open')
    expect(body).toContain('Decision:')
    expect(body).toContain('undecided')
    expect(body).toContain('Parents:')
    expect(body).toContain('H-001')
    expect(body).not.toContain(H1_CARD_PROBE)
    expect(body).not.toContain(H1_STATEMENT_PROBE)
  })
})
