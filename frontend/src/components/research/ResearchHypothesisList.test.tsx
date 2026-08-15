// @vitest-environment jsdom
import { describe, it, expect, vi } from 'vitest'
import { act } from 'react'
import { createRoot, type Root } from 'react-dom/client'

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

  it('renders each hypothesis as a row within a path group', () => {
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

  it('indents children deeper than roots within a path', () => {
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

  it('renders multiple paths with shared root (no separator)', () => {
    // Binary branching: root → left, root → right
    // After merge: root has children left and right = 3 treeitems, no hr
    const g = graphOf(
      [{ id: 'root' }, { id: 'left' }, { id: 'right' }],
      [
        { from: 'root', to: 'left' },
        { from: 'root', to: 'right' },
      ],
    )
    const { container } = render(<ResearchHypothesisList graph={g} />)
    // 3 treeitems: root + left + right (merged under shared root)
    const rows = container.querySelectorAll('[role="treeitem"]')
    expect(rows).toHaveLength(3)
    // No separator since there is only 1 root
    const hr = container.querySelector('hr')
    expect(hr).toBeNull()
  })

  it('renders a diamond DAG with shared nodes collapsed', () => {
    // a → b → d, a → c → d
    // After merge: a has children b and c; b has child d; c has child d
    // = 5 treeitems total (a, b, c, d, d) — d appears twice (once per branch)
    const g = graphOf(
      [{ id: 'a' }, { id: 'b' }, { id: 'c' }, { id: 'd' }],
      [
        { from: 'a', to: 'b' },
        { from: 'a', to: 'c' },
        { from: 'b', to: 'd' },
        { from: 'c', to: 'd' },
      ],
    )
    const { container } = render(<ResearchHypothesisList graph={g} />)
    // 5 treeitems: a + b + c + d(under b) + d(under c)
    const rows = container.querySelectorAll('[role="treeitem"]')
    expect(rows).toHaveLength(5)
    // 'd' should appear twice (once in each branch: b→d and c→d).
    const dRows = Array.from(container.querySelectorAll('[role="treeitem"]')).filter(
      (row) => row.textContent?.includes('d'),
    )
    expect(dRows.length).toBe(2)
  })

  it('marks root nodes with a left border accent', () => {
    const g = graphOf([{ id: 'H-001', title: 'Root' }])
    const { container } = render(<ResearchHypothesisList graph={g} />)
    const rootDiv = container.querySelector('[role="treeitem"] div')!
    // border-l-2 should be present on root nodes.
    expect(rootDiv.className).toContain('border-l-2')
  })

  it('makes root titles bold (font-semibold)', () => {
    const g = graphOf([{ id: 'H-001', title: 'Root hypothesis' }])
    const { container } = render(<ResearchHypothesisList graph={g} />)
    const titleSpan = container.querySelector('[role="treeitem"] .truncate')!
    // font-semibold should be applied to root node titles.
    expect(titleSpan.className).toContain('font-semibold')
  })
})
