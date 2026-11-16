// @vitest-environment jsdom
// SpecsSectionTree — one DECLARED tree section over the payload's flat node
// forest: the section heading and every label come from the framework
// definition, nesting follows the node `children` links, expansion is
// caller-owned, dated documents carry their date column, and the actions the
// framework declared for a node's CONCEPT render on that node and dispatch
// with {path}/{name} substituted (AC4).
import { afterEach, describe, expect, it, vi } from 'vitest'
import { act } from 'react'
import { createRoot, type Root } from 'react-dom/client'

import { SpecsSectionTree } from './SpecsSectionTree'
import { NODE_GATE_REASON, REQUIREMENT_UNMET_REASON } from './specsActions'
import { makeAction, makeDocument, makeNode, makeView } from '@/test/sddFixture'
import type { SDDAction, SDDNode, SDDView } from '@/types/models'

let container: HTMLElement | null = null
let root: Root | null = null
const onOpenMock = vi.fn()
const onDispatchMock = vi.fn()

const SECTION: SDDView = makeView({
  id: 'changes',
  label: 'Changes',
  kind: 'tree',
  nodes: ['openspec/changes/add-auth'],
})

const NODES: SDDNode[] = [
  makeNode({
    path: 'openspec/changes/add-auth',
    kind: 'directory',
    concept: 'change',
    label: 'add-auth',
    children: ['openspec/changes/add-auth/proposal.md', 'openspec/changes/add-auth/design.md'],
    missing: ['tasks.md'],
  }),
  makeNode({
    path: 'openspec/changes/add-auth/proposal.md',
    concept: 'proposal',
    parent: 'openspec/changes/add-auth',
    label: 'Proposal',
    children: [],
  }),
  makeNode({
    path: 'openspec/changes/add-auth/design.md',
    concept: 'design',
    parent: 'openspec/changes/add-auth',
    label: 'Design',
    children: [],
  }),
]

const DOCUMENTS = [
  makeDocument({ path: 'openspec/changes/add-auth/design.md', concept: 'design' }),
  makeDocument({ path: 'openspec/changes/add-auth/proposal.md', concept: 'proposal' }),
]

const APPLY: SDDAction = makeAction({
  kind: 'apply',
  label: 'Apply',
  skill: 'openspec-apply-change',
  prompt: 'Implement {name} in {path}.',
  concept: 'change',
  requires: ['root', 'concept:change'],
})

async function render(
  options: {
    section?: SDDView
    nodes?: SDDNode[]
    documents?: typeof DOCUMENTS
    actions?: SDDAction[]
    expandedNodes?: Record<string, boolean>
    onToggleNode?: (id: string) => void
    memberPaths?: Set<string>
  } = {},
): Promise<void> {
  onOpenMock.mockClear()
  onDispatchMock.mockClear()
  container = document.createElement('div')
  document.body.replaceChildren(container)
  root = createRoot(container)
  await act(async () => {
    root!.render(
      <SpecsSectionTree
        section={options.section ?? SECTION}
        nodes={options.nodes ?? NODES}
        documents={options.documents ?? DOCUMENTS}
        actions={options.actions ?? [APPLY]}
        expandedNodes={options.expandedNodes}
        onToggleNode={options.onToggleNode ?? vi.fn()}
        memberPaths={options.memberPaths}
        onOpenDocument={onOpenMock}
        onDispatch={onDispatchMock}
      />,
    )
  })
}

function all(testId: string): HTMLElement[] {
  return Array.from(container?.querySelectorAll<HTMLElement>(`[data-testid="${testId}"]`) ?? [])
}

afterEach(() => {
  act(() => root?.unmount())
  root = null
  container = null
})

describe('SpecsSectionTree', () => {
  it('renders the declared section heading and its top-level nodes collapsed', async () => {
    await render()
    expect(container?.textContent).toContain('Changes')
    expect(all('specs-tree-group')).toHaveLength(1)
    expect(all('specs-tree-group')[0]?.textContent).toContain('add-auth')
    // Collapsed: the children's rows are not rendered.
    expect(all('specs-node')).toHaveLength(0)
  })

  it('renders the child nodes once the caller expands the node', async () => {
    await render({ expandedNodes: { 'openspec/changes/add-auth': true } })
    const labels = all('specs-node').map((node) => node.textContent)
    expect(labels).toContain('Proposal')
    expect(labels).toContain('Design')
  })

  it('toggles through the caller-owned expansion callback', async () => {
    const onToggle = vi.fn()
    await render({ onToggleNode: onToggle })
    act(() => {
      all('specs-tree-group')[0]?.dispatchEvent(new MouseEvent('click', { bubbles: true }))
    })
    expect(onToggle).toHaveBeenCalledWith('openspec/changes/add-auth')
  })

  it('opens a document node in the viewer by its workspace-relative path', async () => {
    await render({ expandedNodes: { 'openspec/changes/add-auth': true } })
    const design = all('specs-node').find((node) => node.textContent?.includes('Design'))
    act(() => {
      design?.dispatchEvent(new MouseEvent('click', { bubbles: true }))
    })
    expect(onOpenMock).toHaveBeenCalledWith('openspec/changes/add-auth/design.md')
  })

  it('renders the date column of a dated document', async () => {
    await render({
      expandedNodes: { 'openspec/changes/add-auth': true },
      documents: [
        makeDocument({
          path: 'openspec/changes/add-auth/design.md',
          concept: 'design',
          date: '2026-02-03',
        }),
      ],
    })
    const date = container?.querySelector('[data-testid="specs-node-date"]')
    expect(date?.textContent).toBe('2026-02-03')
  })

  it('flags the missing companion files a directory instance declares', async () => {
    await render()
    const missing = container?.querySelector('[data-testid="specs-node-missing"]')
    expect(missing?.getAttribute('title')).toBe('Missing: tasks.md')
  })

  it('renders the node action of the node’s concept and substitutes {path}/{name}', async () => {
    await render()
    const action = container?.querySelector<HTMLElement>('[data-testid="specs-node-action"]')
    expect(action?.textContent).toBe('Apply')
    expect(action?.getAttribute('data-skill')).toBe('openspec-apply-change')

    act(() => {
      action?.dispatchEvent(new MouseEvent('click', { bubbles: true }))
    })

    const [dispatch, newSession] = onDispatchMock.mock.calls[0] as [
      { skill: string; prompt: string; key: string },
      boolean,
    ]
    expect(newSession).toBe(false)
    expect(dispatch.skill).toBe('openspec-apply-change')
    expect(dispatch.key).toBe('apply:openspec/changes/add-auth')
    // AC4: the dispatched prompt carries the node's own path and name.
    expect(dispatch.prompt).toBe('Implement add-auth in openspec/changes/add-auth.')
  })

  it('renders no action on a node whose concept the action does not target', async () => {
    await render({
      expandedNodes: { 'openspec/changes/add-auth': true },
      actions: [{ ...APPLY, concept: 'feature', kind: 'plan', label: 'Plan' }],
    })
    // The change node gets nothing; the proposal/design children get nothing.
    expect(all('specs-node-action')).toHaveLength(0)
  })

  // Review 18, node side: an action whose declared requirements are unmet
  // STAYS in the row — visible, disabled, with the reason — instead of
  // disappearing (hiding it hides the workflow's next step), and a click on
  // it dispatches nothing.
  it('renders an unavailable node action disabled with the reason, not hidden', async () => {
    await render({ actions: [{ ...APPLY, available: false }] })
    const action = container?.querySelector<HTMLElement>('[data-testid="specs-node-action"]')
    expect(action).not.toBeNull()
    expect(action?.hasAttribute('disabled')).toBe(true)
    expect(action?.getAttribute('title')).toBe(REQUIREMENT_UNMET_REASON)

    act(() => {
      action?.dispatchEvent(new MouseEvent('click', { bubbles: true }))
    })
    expect(onDispatchMock).not.toHaveBeenCalled()
  })

  // Review 111: a per-node gate disables the gesture on the holder that already
  // has its artifact — this change folder has design.md, so a design step
  // declaring absent:design is inert on it — and the row names THAT reason
  // rather than the tree-level one.
  it('disables a node action the backend refined as blocked for this node', async () => {
    await render({
      actions: [
        {
          ...APPLY,
          kind: 'design',
          label: 'Design',
          requires: ['root', 'concept:change', 'absent:design'],
          unavailable_at: ['openspec/changes/add-auth'],
        },
      ],
    })
    const action = container?.querySelector<HTMLElement>('[data-testid="specs-node-action"]')
    expect(action).not.toBeNull()
    expect(action?.hasAttribute('disabled')).toBe(true)
    expect(action?.getAttribute('title')).toBe(NODE_GATE_REASON)

    act(() => {
      action?.dispatchEvent(new MouseEvent('click', { bubbles: true }))
    })
    expect(onDispatchMock).not.toHaveBeenCalled()
  })

  it('filters a shared ancestor to the section’s own members', async () => {
    // Two sections over the same forest: `docs` is a shared ancestor whose
    // children belong to different sections' concepts. The backend's
    // per-section membership must stop one section rendering the other's
    // documents through the shared folder.
    const nodes: SDDNode[] = [
      makeNode({
        path: 'docs',
        kind: 'directory',
        concept: '',
        label: 'docs',
        children: ['docs/one.md', 'docs/two.md'],
      }),
      makeNode({ path: 'docs/one.md', concept: 'one', parent: 'docs', label: 'One', children: [] }),
      makeNode({ path: 'docs/two.md', concept: 'two', parent: 'docs', label: 'Two', children: [] }),
    ]
    const section = makeView({ id: 'one', label: 'One', kind: 'tree', nodes: ['docs'] })
    await render({
      section,
      nodes,
      documents: [],
      expandedNodes: { docs: true },
      memberPaths: new Set(['docs', 'docs/one.md']),
    })
    expect(all('specs-tree-group')).toHaveLength(1)
    const labels = all('specs-node').map((node) => node.textContent)
    expect(labels).toContain('One')
    expect(labels).not.toContain('Two')
  })

  it('renders a shared ancestor whose members all filter away as a plain label', async () => {
    const nodes: SDDNode[] = [
      makeNode({
        path: 'docs',
        kind: 'directory',
        concept: '',
        label: 'docs',
        children: ['docs/one.md'],
      }),
      makeNode({ path: 'docs/one.md', concept: 'one', parent: 'docs', label: 'One', children: [] }),
    ]
    const section = makeView({ id: 'empty', label: 'Empty', kind: 'tree', nodes: ['docs'] })
    await render({
      section,
      nodes,
      documents: [],
      expandedNodes: { docs: true },
      memberPaths: new Set(['docs']),
    })
    // Every child filtered away: nothing to collapse (no group row) and no
    // foreign document — the ancestor degrades to a non-openable label row.
    expect(all('specs-tree-group')).toHaveLength(0)
    const rows = all('specs-node').map((node) => node.getAttribute('data-path'))
    expect(rows).toEqual(['docs'])
  })

  // Review 99: the count chip is SECTION-scoped. Every single-root framework
  // anchors its instances at the same top node, so two tree sections list the
  // same folder id; a section-agnostic count printed the whole forest's total
  // beside a correctly filtered child list.
  it('counts only the documents in the section membership', async () => {
    const nodes: SDDNode[] = [
      makeNode({
        path: 'docs',
        kind: 'directory',
        concept: '',
        label: 'docs',
        children: ['docs/one.md', 'docs/two.md'],
      }),
      makeNode({ path: 'docs/one.md', concept: 'one', parent: 'docs', label: 'One', children: [] }),
      makeNode({ path: 'docs/two.md', concept: 'two', parent: 'docs', label: 'Two', children: [] }),
    ]
    const section = makeView({ id: 'one', label: 'One', kind: 'tree', nodes: ['docs'] })
    await render({
      section,
      nodes,
      documents: [],
      expandedNodes: { docs: true },
      memberPaths: new Set(['docs', 'docs/one.md']),
    })
    // The forest holds two documents; THIS section renders one of them.
    expect(all('specs-tree-group')[0]?.textContent).toContain('(1)')
    expect(all('specs-node').map((node) => node.textContent)).toEqual(['One'])
  })

  // Review 61: the membership filter applies at CHILD depth only — a section
  // ROOT is never filtered out even when it is absent from `memberPaths`
  // (the sections' own top-level entries must render, or a membership list
  // that omits a root would blank the whole section).
  it('renders a section root that is absent from memberPaths', async () => {
    const nodes: SDDNode[] = [
      makeNode({
        path: 'docs',
        kind: 'directory',
        concept: '',
        label: 'docs',
        children: ['docs/one.md'],
      }),
      makeNode({ path: 'docs/one.md', concept: 'one', parent: 'docs', label: 'One', children: [] }),
    ]
    const section = makeView({ id: 'one', label: 'One', kind: 'tree', nodes: ['docs'] })
    await render({
      section,
      nodes,
      documents: [],
      expandedNodes: { docs: true },
      // `docs` itself is NOT a member; only its child is.
      memberPaths: new Set(['docs/one.md']),
    })
    // The root renders (as a collapsible group, its child kept), it is only
    // never dropped by the membership filter.
    expect(all('specs-tree-group')).toHaveLength(1)
    expect(all('specs-node').map((node) => node.textContent)).toEqual(['One'])
  })

  it('renders nothing for an empty section', async () => {
    await render({ section: makeView({ id: 'empty', label: 'Empty', nodes: [] }) })
    expect(container?.childElementCount).toBe(0)
  })
})
