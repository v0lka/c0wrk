// @vitest-environment jsdom
//
// LiteratureGraph — the paper workspace's literature DAG. It must reuse the
// shared research canvas (pan/zoom SVG, one clickable <g> per node) and paint a
// colour legend for the four work kinds.

import { describe, it, expect, vi, beforeEach, afterEach } from 'vitest'
import { act } from 'react'
import { createRoot, type Root } from 'react-dom/client'

import { TooltipProvider } from '@/components/ui/tooltip'
import { LiteratureGraph } from './LiteratureGraph'
import { literatureToGraph, parseLiteratureJson } from '@/lib/literatureGraph'

const RAW = JSON.stringify({
  seed: { title: 'Seed', year: 2017, doi: '10.1/seed' },
  predecessors: [
    { title: 'Pred A', year: 2014, doi: '10.1/a' },
    { title: 'Pred B', year: 1997, doi: '10.1/b' },
  ],
  citing: [{ title: 'Citer', year: 2019, doi: '10.1/c', reasons: ['critique'] }],
  contradictions: [{ title: 'Citer', year: 2019, doi: '10.1/c', reasons: ['critique'] }],
})

const model = literatureToGraph(parseLiteratureJson(RAW).record!)

describe('LiteratureGraph', () => {
  let container: HTMLElement
  let root: Root

  beforeEach(() => {
    vi.stubGlobal('ResizeObserver', class {
      observe() {}
      unobserve() {}
      disconnect() {}
    })
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
  })

  const renderGraph = (onSelect = vi.fn()) =>
    act(() => {
      root.render(
        <TooltipProvider>
          <LiteratureGraph model={model} selectedId={null} onSelect={onSelect} />
        </TooltipProvider>,
      )
    })

  it('renders one node per work plus a colour legend for the four kinds', () => {
    renderGraph()
    expect(container.querySelector('[data-testid="literature-graph"]')).not.toBeNull()
    for (const id of ['seed', 'p1', 'p2', 'c1']) {
      expect(container.querySelector(`[data-node-id="${id}"]`)).not.toBeNull()
    }
    const legend = container.querySelector('[data-testid="literature-legend"]')
    expect(legend?.textContent).toContain('Seed')
    expect(legend?.textContent).toContain('Predecessor')
    expect(legend?.textContent).toContain('Citing')
    expect(legend?.textContent).toContain('Contradiction')
  })

  it('reports the clicked node id', () => {
    const onSelect = vi.fn()
    renderGraph(onSelect)
    const seed = container.querySelector('[data-node-id="seed"]') as Element
    act(() => {
      seed.dispatchEvent(new MouseEvent('click', { bubbles: true }))
    })
    expect(onSelect).toHaveBeenCalledWith('seed')
  })
})
