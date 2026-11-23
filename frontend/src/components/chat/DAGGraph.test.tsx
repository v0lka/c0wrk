// @vitest-environment jsdom
//
// Render tests for the plan-DAG component: every rendered `<path>` must be
// exactly `buildEdgePath` over the computed layout (path shapes themselves —
// fork/merge/vertical/broken incl. dy offsets — are covered in
// dagEdgePath.test.ts, whose topologies reused here are verified in
// dagLayout.test.ts). Rendering must stay free of console diagnostics.
//
// Golden strings encode the renderer geometry: laneX(l) = l*14 + 6 + 7 and
// rowY(r) = r*24 + 12.

import { describe, it, expect, beforeEach, afterEach, vi } from 'vitest'
import { act } from 'react'
import { createRoot, type Root } from 'react-dom/client'
import type { ReactElement } from 'react'

import { DAGGraph } from './DAGGraph'
import { buildEdgePath } from './dagEdgePath'
import { computeDAGLayout } from '@/lib/dagLayout'
import type { PlanItem } from '@/types/models'

const laneX = (lane: number) => lane * 14 + 6 + 14 / 2
const rowY = (row: number) => row * 24 + 12

function item(id: string, dependsOn: string[]): PlanItem {
  return { id, title: id, status: 'completed', dependsOn }
}

/**
 * Wide fan (the dagLayout "side-aware fork fan" topology, verified there):
 * lanes [0,0,1,2,3] — A→B stays a vertical while A→C/D/E fork with exitDy
 * 0/3/6. No merges, no broken continuations.
 */
const fanItems: PlanItem[] = [
  item('A', []),
  item('B', ['A']),
  item('C', ['A']),
  item('D', ['A']),
  item('E', ['A']),
]

/**
 * Merge stack (the dagLayout entryDy topology, verified there): lanes
 * [0,0,1,2] — B→K and C→K merge into K's row with entryDy 0/3, while A→C and
 * A→K fork with exitDy 0/3.
 */
const mergeStackItems: PlanItem[] = [
  item('A', []),
  item('B', ['A']),
  item('C', ['A']),
  item('K', ['B', 'C', 'A']),
]

/**
 * Broken lane reuse (the dagLayout BUG4 topology, verified there): B (root,
 * lane 1, row 1) and F (lane 1, row 5) share a lane with no dependency edge
 * between them — the continuation must render as two stubs.
 */
const brokenReuseItems: PlanItem[] = [
  item('A', []),
  item('B', []),
  item('C', ['A', 'B']),
  item('D', ['A']),
  item('E', ['D']),
  item('F', ['D', 'E']),
]

let root: Root | null = null

function renderEl(el: ReactElement): HTMLElement {
  const container = document.createElement('div')
  document.body.appendChild(container)
  root = createRoot(container)
  act(() => {
    root!.render(el)
  })
  return container
}

describe('DAGGraph render', () => {
  let errorSpy: ReturnType<typeof vi.spyOn>
  let warnSpy: ReturnType<typeof vi.spyOn>

  beforeEach(() => {
    document.body.innerHTML = ''
    root = null
    // Record-only spies: any React warning/error still prints (visible in the
    // run output) AND fails the test — expected diagnostics have none here.
    errorSpy = vi.spyOn(console, 'error')
    warnSpy = vi.spyOn(console, 'warn')
  })

  afterEach(() => {
    act(() => {
      root?.unmount()
    })
    root = null
    vi.restoreAllMocks()
  })

  function expectCleanConsole(): void {
    expect(errorSpy).not.toHaveBeenCalled()
    expect(warnSpy).not.toHaveBeenCalled()
  }

  /** Every rendered path is exactly the pure helper over the computed layout. */
  function expectPathsMatchLayout(container: HTMLElement, items: PlanItem[]): void {
    const layout = computeDAGLayout(items)
    const paths = [...container.querySelectorAll('path')]
    expect(paths).toHaveLength(layout.connectors.length)
    paths.forEach((p, i) => {
      expect(p.getAttribute('d')).toBe(buildEdgePath(layout.connectors[i]!, laneX, rowY))
    })
  }

  it('renders the wide fan: fork runs stack exitDy 0/3/6, nodes at r=3', () => {
    const container = renderEl(<DAGGraph items={fanItems} />)
    const svg = container.querySelector('svg')
    expect(svg).not.toBeNull()
    // width = (maxLane+1)*14 + 6*2 = 4 lanes; height = 5 rows * 24
    expect(svg!.getAttribute('width')).toBe('68')
    expect(svg!.getAttribute('height')).toBe('120')

    expectPathsMatchLayout(container, fanItems)
    const paths = [...container.querySelectorAll('path')]

    // A→B: plain vertical down lane 0
    expect(paths.some(p => p.getAttribute('d') === 'M 13 12 L 13 36')).toBe(true)
    // A→C fork, exitDy 0: legacy shape (run straight out of the node)
    expect(paths.some(p => p.getAttribute('d') === 'M 13 12 L 20 12 Q 27 12 27 19 L 27 60')).toBe(true)
    // A→D fork, exitDy 3: drop stub onto the run 3px below the parent row
    expect(paths.some(p => p.getAttribute('d') === 'M 13 12 L 13 15 L 34 15 Q 41 15 41 22 L 41 84')).toBe(true)
    // A→E fork, exitDy 6: third stacked run
    expect(paths.some(p => p.getAttribute('d') === 'M 13 12 L 13 18 L 48 18 Q 55 18 55 25 L 55 108')).toBe(true)

    // no unrelated occupants in this topology — every connector is a single subpath
    const brokenDs = paths.map(p => p.getAttribute('d')!).filter(d => d.split('M').length === 3)
    expect(brokenDs).toEqual([])

    // node dots: one per item, r=3, first node at lane0/row0 center
    const circles = [...container.querySelectorAll('circle')]
    expect(circles).toHaveLength(fanItems.length)
    expect(circles[0]!.getAttribute('r')).toBe('3')
    expect(circles[0]!.getAttribute('cx')).toBe('13')
    expect(circles[0]!.getAttribute('cy')).toBe('12')

    expectCleanConsole()
  })

  it('stacks overlapping merge entry runs (entryDy 0 then 3) in the child row', () => {
    const container = renderEl(<DAGGraph items={mergeStackItems} />)
    const svg = container.querySelector('svg')
    expect(svg!.getAttribute('width')).toBe('54')
    expect(svg!.getAttribute('height')).toBe('96')

    expectPathsMatchLayout(container, mergeStackItems)
    const paths = [...container.querySelectorAll('path')]

    // A→C fork, exitDy 0; A→K fork, exitDy 3 (stacked under A→C's run)
    expect(paths.some(p => p.getAttribute('d') === 'M 13 12 L 20 12 Q 27 12 27 19 L 27 60')).toBe(true)
    expect(paths.some(p => p.getAttribute('d') === 'M 13 12 L 13 15 L 34 15 Q 41 15 41 22 L 41 84')).toBe(true)
    // B→K merge, entryDy 0: entry run on the child row itself
    expect(paths.some(p => p.getAttribute('d') === 'M 13 36 L 13 77 Q 13 84 20 84 L 41 84')).toBe(true)
    // C→K merge, entryDy 3: run 3px below the child row, tail rises back to the node
    expect(paths.some(p => p.getAttribute('d') === 'M 27 60 L 27 80 Q 27 87 34 87 L 41 87 L 41 84')).toBe(true)

    expectCleanConsole()
  })

  it('draws broken lane continuations as two stubs with a visible gap', () => {
    const container = renderEl(<DAGGraph items={brokenReuseItems} />)
    const svg = container.querySelector('svg')
    expect(svg!.getAttribute('width')).toBe('54')
    expect(svg!.getAttribute('height')).toBe('144')

    expectPathsMatchLayout(container, brokenReuseItems)
    const paths = [...container.querySelectorAll('path')]

    // exactly one broken connector — B→F on lane 1 (rows 1→5): stubs split at
    // gapPx 72, the layout-placed gap in the bare stretch (53..91) between
    // the merge tail C←B ink and the fork drop F←D ink — 6px gap 69..75
    const brokenDs = paths.map(p => p.getAttribute('d')!).filter(d => d.split('M').length === 3)
    expect(brokenDs).toEqual(['M 27 36 L 27 69 M 27 75 L 27 132'])

    // edge-backed verticals stay solid single-subpath paths
    expect(paths.some(p => p.getAttribute('d') === 'M 13 12 L 13 60')).toBe(true) // A→C
    expect(paths.some(p => p.getAttribute('d') === 'M 41 84 L 41 108')).toBe(true) // D→E

    expectCleanConsole()
  })

  it('renders nothing for an empty plan', () => {
    const container = renderEl(<DAGGraph items={[]} />)
    expect(container.innerHTML).toBe('')
    expectCleanConsole()
  })
})
