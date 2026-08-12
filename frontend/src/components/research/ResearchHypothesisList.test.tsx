// @vitest-environment jsdom
import { describe, it, expect, vi } from 'vitest'
import { act } from 'react'
import { createRoot, type Root } from 'react-dom/client'

vi.hoisted(() => {
  ;(globalThis as Record<string, unknown>).IS_REACT_ACT_ENVIRONMENT = true
})

import { ResearchHypothesisList } from './ResearchHypothesisList'
import type { HypothesisGraph } from '@/types/models'

function graphOf(
  nodes: { id: string; title?: string; status?: string; parents?: string[]; result?: string }[],
  edges?: { from: string; to: string }[],
): HypothesisGraph {
  return {
    nodes: nodes.map((n) => ({
      id: n.id,
      title: n.title ?? n.id,
      status: (n.status ?? 'open') as HypothesisGraph['nodes'][number]['status'],
      parents: n.parents,
      result: n.result,
    })),
    edges: edges ?? [],
  }
}

function render(ui: React.ReactElement): { container: HTMLElement; root: Root } {
  const container = document.createElement('div')
  document.body.replaceChildren(container)
  const root = createRoot(container)
  act(() => {
    root.render(ui)
  })
  return { container, root }
}

describe('ResearchHypothesisList', () => {
  it('shows the empty state when the graph has no nodes', () => {
    const { container } = render(<ResearchHypothesisList graph={graphOf([])} />)
    expect(container.textContent).toContain('No hypotheses yet')
  })

  it('renders each hypothesis as a row', () => {
    const g = graphOf([{ id: 'H-001' }, { id: 'H-002', parents: ['H-001'] }])
    const { container } = render(<ResearchHypothesisList graph={g} />)
    const rows = container.querySelectorAll('[role="treeitem"]')
    expect(rows).toHaveLength(2)
    expect(container.textContent).toContain('H-001')
    expect(container.textContent).toContain('H-002')
  })

  it('calls onSelectNode with the id when a row is clicked', () => {
    const g = graphOf([{ id: 'H-001', title: 'Root hypothesis' }])
    const onSelect = vi.fn()
    const { container } = render(
      <ResearchHypothesisList graph={g} onSelectNode={onSelect} />,
    )
    // The onClick handler lives on the inner div (the tree row), not the li.
    const row = container.querySelector('[role="treeitem"] div')!
    act(() => {
      row.dispatchEvent(new MouseEvent('click', { bubbles: true }))
    })
    expect(onSelect).toHaveBeenCalledWith('H-001')
  })

  it('expands the inline detail for the selected node only', () => {
    const g = graphOf([
      { id: 'H-001', title: 'Root', result: 'It worked' },
      { id: 'H-002', title: 'Child', parents: ['H-001'] },
    ])
    const { container } = render(
      <ResearchHypothesisList graph={g} selectedId="H-001" />,
    )
    // The result text only appears inside the detail of the selected node.
    expect(container.textContent).toContain('It worked')
    // H-002 is not selected, so it has no detail with "No details recorded".
    expect(container.textContent).not.toContain('No details recorded')
  })

  it('indents children deeper than roots', () => {
    const g = graphOf(
      [{ id: 'H-001' }, { id: 'H-002', parents: ['H-001'] }],
    )
    const { container } = render(<ResearchHypothesisList graph={g} />)
    const rows = Array.from(container.querySelectorAll<HTMLElement>('[role="treeitem"] > div'))
    expect(rows.length).toBeGreaterThanOrEqual(2)
    // The child's inner row should be indented further than the root's.
    const rootPad = parseFloat(getComputedStyle(rows[0]!).paddingLeft)
    const childPad = parseFloat(getComputedStyle(rows[1]!).paddingLeft)
    expect(childPad).toBeGreaterThan(rootPad)
  })
})
