// @vitest-environment jsdom
// SpecsPanel — the panel orchestrator over specsStore: the mount fetch, the
// in-panel workspace:tree_changed subscription, the DECLARED render model
// (view sections, labels, findings, quick actions), the framework selector
// (>1 detected only, persisted through SetActiveSDD while the previous parse
// stays rendered), the declaration-gated findings gesture, and the read path
// through the file viewer.
import { afterEach, beforeEach, describe, expect, it, vi } from 'vitest'
import { act } from 'react'
import { createRoot, type Root } from 'react-dom/client'

import { SpecsPanel } from './SpecsPanel'
import { findingLine } from './specsActions'
import { useSpecsStore } from '@/stores/specsStore'
import { useSddStore } from '@/stores/sddStore'
import { useProjectStore } from '@/stores/projectStore'
import { useSessionStore } from '@/stores/sessionStore'
import { useFileViewerStore } from '@/stores/fileViewerStore'
import {
  makeAction,
  makeDocument,
  makeFinding,
  makeFramework,
  makeNode,
  makeSpecsStatus,
  makeView,
} from '@/test/sddFixture'
import type { SpecsStatus } from '@/types/models'

const getSpecsStatusMock = vi.hoisted(() => vi.fn())
const setActiveSDDMock = vi.hoisted(() => vi.fn())
vi.mock('@/api/specs', () => ({
  getSpecsStatus: (...args: unknown[]) => getSpecsStatusMock(...args),
  setActiveSDD: (...args: unknown[]) => setActiveSDDMock(...args),
}))

// Event subscription capture: tests fire the captured handlers directly — the
// refresh TRIGGER is under test, not the transport.
const eventHandlers = new Map<string, Array<(...data: unknown[]) => void>>()
vi.mock('@/api/runtime', () => ({
  subscribe: (name: string, cb: (...data: unknown[]) => void) => {
    const list = eventHandlers.get(name) ?? []
    list.push(cb)
    eventHandlers.set(name, list)
    return () => {
      eventHandlers.set(name, (eventHandlers.get(name) ?? []).filter((f) => f !== cb))
    }
  },
  clipboardSetText: vi.fn().mockResolvedValue(true),
}))

const sendMock = vi.hoisted(() => vi.fn(async (..._args: unknown[]) => {}))
vi.mock('@/hooks/useMessageSender', () => ({
  useMessageSender: () => ({ send: sendMock, cancel: vi.fn(async () => {}), isProcessing: false }),
}))

let container: HTMLElement | null = null
let activeRoot: Root | null = null

async function flush(): Promise<void> {
  await act(async () => {
    for (let i = 0; i < 10; i++) await Promise.resolve()
  })
}

async function renderPanel(): Promise<void> {
  container = document.createElement('div')
  document.body.replaceChildren(container)
  activeRoot = createRoot(container)
  await act(async () => {
    activeRoot!.render(<SpecsPanel />)
  })
  await flush()
}

function query(testId: string): HTMLElement | null {
  return container?.querySelector(`[data-testid="${testId}"]`) ?? null
}

function all(testId: string): HTMLElement[] {
  return Array.from(container?.querySelectorAll<HTMLElement>(`[data-testid="${testId}"]`) ?? [])
}

function text(): string {
  return container?.textContent ?? ''
}

// The dispatch preview dialog portals to document.body (Radix), so its nodes
// are looked up at the document level, not inside the render container.
function dialogEl(testId: string): HTMLElement | null {
  return document.body.querySelector(`[data-testid="${testId}"]`)
}

function fireTreeChanged(...data: unknown[]): void {
  for (const handler of eventHandlers.get('workspace:tree_changed') ?? []) handler(...data)
}

function seedProject(id: string): void {
  useProjectStore.setState({
    projects: [
      {
        id,
        name: id,
        workspace_path: `/ws/${id}`,
        is_external: false,
        is_no_project: false,
        created_at: '2026-01-01T00:00:00Z',
        last_active_at: '2026-01-01T00:00:00Z',
      },
    ],
    activeProjectId: id,
  })
}

/** A status with two declared tree sections and a findings section. */
function statusWithSections(overrides: Partial<SpecsStatus> = {}): SpecsStatus {
  return makeSpecsStatus({
    nodes: [
      makeNode({ path: 'specs/INDEX.md', label: 'Specs Index', concept: 'index' }),
      makeNode({
        path: 'specs/decisions/001-one.md',
        label: 'One',
        concept: 'decision',
      }),
    ],
    documents: [
      makeDocument({ path: 'specs/INDEX.md' }),
      makeDocument({ path: 'specs/decisions/001-one.md', concept: 'decision', date: '2026-01-05' }),
    ],
    views: [
      makeView({ id: 'tree', label: 'Specification tree', kind: 'tree', nodes: ['specs/INDEX.md'] }),
      makeView({
        id: 'decisions',
        label: 'Decisions',
        kind: 'tree',
        nodes: ['specs/decisions/001-one.md'],
      }),
      makeView({ id: 'findings', label: 'Findings', kind: 'findings', nodes: [] }),
    ],
    ...overrides,
  })
}

beforeEach(() => {
  eventHandlers.clear()
  getSpecsStatusMock.mockReset()
  setActiveSDDMock.mockReset()
  sendMock.mockClear()
  useSpecsStore.getState().reset()
  useSddStore.getState().reset()
  useFileViewerStore.setState({ files: {}, openTabs: [], activeFile: null, collapsed: true })
  useSessionStore.setState({ sessions: null, activeSessionId: null })
  seedProject('p1')
})

afterEach(() => {
  act(() => activeRoot?.unmount())
  activeRoot = null
  container = null
})

describe('SpecsPanel — declared render model', () => {
  it('renders every declared view section with its own label', async () => {
    getSpecsStatusMock.mockResolvedValue(statusWithSections())

    await renderPanel()

    const views = all('specs-view').map((view) => view.getAttribute('data-view'))
    expect(views).toEqual(['tree', 'decisions'])
    expect(text()).toContain('Specification tree')
    expect(text()).toContain('Decisions')
  })

  it('renders the declared quick actions and the declared findings', async () => {
    getSpecsStatusMock.mockResolvedValue(
      statusWithSections({
        actions: [
          makeAction({ kind: 'init', label: 'Init', skill: 'spec-init', requires: [] }),
          makeAction({ kind: 'check', label: 'Check drift', skill: 'spec-check' }),
        ],
        findings: [makeFinding({ message: 'a spec file must be listed in the INDEX' })],
        counts: {
          documents: 2,
          findings: 1,
          by_concept: { index: 1, decision: 1 },
          findings_by_kind: { INDEX_STALE: 1 },
        },
      }),
    )

    await renderPanel()

    expect(all('specs-quick-action').map((b) => b.textContent)).toEqual(['Init', 'Check drift'])
    expect(text()).toContain('a spec file must be listed in the INDEX')
    expect(query('specs-integrity-badge')?.textContent).toContain('1 finding')
  })

  it('renders the empty state when a detected framework has no tree', async () => {
    getSpecsStatusMock.mockResolvedValue(
      makeSpecsStatus({
        tree_present: false,
        root: '',
        documents: [],
        nodes: [],
        views: [],
        actions: [makeAction({ kind: 'init', label: 'Init', skill: 'spec-init', requires: [] })],
      }),
    )

    await renderPanel()

    expect(query('specs-empty-state')).not.toBeNull()
    expect(query('specs-initialize-cta')?.getAttribute('data-skill')).toBe('spec-init')
  })

  it('renders the empty-documents state when the tree is present but holds none', async () => {
    // A `specs/` directory that exists before the first document is routine
    // (a fresh branch, a framework recognized by skill only): `tree_present`
    // is a filesystem predicate, so the sections would render nothing at all
    // under a header claiming a clean result. The operator must get the cause
    // and the bootstrap gesture instead.
    getSpecsStatusMock.mockResolvedValue(
      makeSpecsStatus({
        tree_present: true,
        root: '/ws/specs',
        documents: [],
        nodes: [],
        views: [],
        counts: { documents: 0, findings: 0, by_concept: {}, findings_by_kind: {} },
        actions: [makeAction({ kind: 'init', label: 'Init', skill: 'spec-init', requires: [] })],
      }),
    )

    await renderPanel()

    const empty = query('specs-empty-state')
    expect(empty).not.toBeNull()
    expect(empty?.getAttribute('data-reason')).toBe('empty-tree')
    expect(query('specs-initialize-cta')?.getAttribute('data-skill')).toBe('spec-init')
    const badge = query('specs-integrity-badge')
    expect(badge?.textContent).toContain('Nothing to check')
    expect(badge?.textContent).not.toContain('Integrity OK')
  })

  it('keeps Integrity OK for a tree that has documents and no findings', async () => {
    getSpecsStatusMock.mockResolvedValue(statusWithSections())

    await renderPanel()

    const badge = query('specs-integrity-badge')
    expect(badge?.textContent).toContain('Integrity OK')
    expect(query('specs-empty-state')).toBeNull()
  })

  it('dispatches a node action with the node path substituted (AC4)', async () => {
    getSpecsStatusMock.mockResolvedValue(
      statusWithSections({
        nodes: [
          makeNode({
            path: 'specs/decisions/001-one.md',
            label: 'One',
            concept: 'decision',
            kind: 'document',
          }),
        ],
        views: [
          makeView({ id: 'decisions', label: 'Decisions', kind: 'tree', nodes: ['specs/decisions/001-one.md'] }),
        ],
        actions: [
          makeAction({
            kind: 'update',
            label: 'Update',
            skill: 'spec-update',
            prompt: 'Update {path} ({name}).',
            concept: 'decision',
            requires: ['root', 'concept:decision'],
          }),
        ],
      }),
    )

    await renderPanel()

    act(() => {
      all('specs-node-action')[0]?.dispatchEvent(new MouseEvent('click', { bubbles: true }))
    })

    // The dispatch GATE: the framework-declared prompt is previewed verbatim
    // and NOTHING is sent until the dialog confirms — the prompt is repo
    // content (specs.yaml), not UI copy, and must not enter the chat unseen.
    expect(sendMock).not.toHaveBeenCalled()
    expect(dialogEl('specs-dispatch-prompt')?.textContent).toBe(
      'Update specs/decisions/001-one.md (001-one.md).',
    )
    act(() => {
      dialogEl('specs-dispatch-confirm')?.dispatchEvent(new MouseEvent('click', { bubbles: true }))
    })

    const args = sendMock.mock.calls[0] as unknown[]
    expect(args[0]).toBe('Update specs/decisions/001-one.md (001-one.md).')
    expect(args[1]).toEqual(['spec-update'])
  })

  it('keeps a cancelled gesture out of the chat entirely', async () => {
    getSpecsStatusMock.mockResolvedValue(
      statusWithSections({
        actions: [
          makeAction({ kind: 'update', label: 'Update', skill: 'spec-update', prompt: 'Update {path}.', concept: 'decision', requires: ['root', 'concept:decision'] }),
        ],
      }),
    )

    await renderPanel()

    act(() => {
      all('specs-node-action')[0]?.dispatchEvent(new MouseEvent('click', { bubbles: true }))
    })
    expect(dialogEl('specs-dispatch-prompt')).not.toBeNull()

    act(() => {
      dialogEl('specs-dispatch-cancel')?.dispatchEvent(new MouseEvent('click', { bubbles: true }))
    })

    expect(sendMock).not.toHaveBeenCalled()
    expect(dialogEl('specs-dispatch-prompt')).toBeNull()
  })

  it('reports a failed dispatch on the specs panel error banner with the skill named', async () => {
    // A rejecting sender (e.g. the splash-race session auto-create failure)
    // must degrade to the store's error banner — naming the skill — after the
    // preview was confirmed, and never crash the click.
    sendMock.mockRejectedValueOnce(new Error('session gone'))
    getSpecsStatusMock.mockResolvedValue(
      statusWithSections({
        actions: [
          makeAction({ kind: 'update', label: 'Update', skill: 'spec-update', prompt: 'Update {path}.', concept: 'decision', requires: ['root', 'concept:decision'] }),
        ],
      }),
    )

    await renderPanel()

    act(() => {
      all('specs-node-action')[0]?.dispatchEvent(new MouseEvent('click', { bubbles: true }))
    })
    act(() => {
      dialogEl('specs-dispatch-confirm')?.dispatchEvent(new MouseEvent('click', { bubbles: true }))
    })
    await flush()

    const error = useSpecsStore.getState().error
    expect(error).toContain('Failed to dispatch spec-update')
    expect(error).toContain('session gone')
  })
})

describe('SpecsPanel — framework selector', () => {
  const twoFrameworks = () => [
    makeFramework('sdd', 'Specification System'),
    makeFramework('openspec', 'OpenSpec'),
  ]

  it('is absent while a single framework is detected (AC2)', async () => {
    getSpecsStatusMock.mockResolvedValue(makeSpecsStatus())

    await renderPanel()

    expect(query('specs-framework-selector')).toBeNull()
  })

  it('is present with one option per detected framework when >1 (AC1)', async () => {
    getSpecsStatusMock.mockResolvedValue(
      makeSpecsStatus({ detected: twoFrameworks(), selector_needed: true }),
    )

    await renderPanel()

    const select = query('specs-framework-select') as HTMLSelectElement | null
    expect(select).not.toBeNull()
    expect(Array.from(select!.options).map((o) => o.value)).toEqual(['sdd', 'openspec'])
    expect(select!.value).toBe('sdd')
  })

  it('persists a selection through SetActiveSDD and keeps the previous tree meanwhile (AC1)', async () => {
    const first = makeSpecsStatus({ detected: twoFrameworks(), selector_needed: true })
    getSpecsStatusMock.mockResolvedValue(first)
    await renderPanel()
    const firstParse = useSpecsStore.getState().status

    // The persist hangs: the panel must keep the previous parse while the
    // selector already reports the operator's choice.
    let releaseSwitch: (value: undefined) => void = () => {}
    setActiveSDDMock.mockReturnValue(new Promise<undefined>((resolve) => {
      releaseSwitch = resolve
    }))
    let releaseReparse: (value: SpecsStatus) => void = () => {}
    getSpecsStatusMock.mockReturnValue(new Promise<SpecsStatus>((resolve) => {
      releaseReparse = resolve
    }))

    act(() => {
      const select = query('specs-framework-select') as HTMLSelectElement
      select.value = 'openspec'
      select.dispatchEvent(new Event('change', { bubbles: true }))
    })
    await flush()

    expect(setActiveSDDMock).toHaveBeenCalledWith('p1', 'openspec')
    expect(useSddStore.getState().activeId).toBe('openspec')
    expect((query('specs-framework-select') as HTMLSelectElement).value).toBe('openspec')
    // LWW: the previous parse is still what the panel renders.
    expect(useSpecsStore.getState().status).toBe(firstParse)

    // The re-parse lands with the new active framework.
    await act(async () => {
      releaseSwitch(undefined)
      await Promise.resolve()
    })
    await act(async () => {
      releaseReparse(
        makeSpecsStatus({
          detected: twoFrameworks(),
          active_id: 'openspec',
          views: [makeView({ id: 'specs', label: 'Specifications', nodes: ['specs/INDEX.md'] })],
        }),
      )
      await Promise.resolve()
    })
    await flush()

    expect(useSddStore.getState().switching).toBe(false)
    expect((query('specs-framework-select') as HTMLSelectElement).value).toBe('openspec')
    expect(all('specs-view').map((v) => v.getAttribute('data-view'))).toEqual(['specs'])
  })

  it('reverts the selector and surfaces the error when the persist fails', async () => {
    getSpecsStatusMock.mockResolvedValue(
      makeSpecsStatus({ detected: twoFrameworks(), selector_needed: true }),
    )
    await renderPanel()
    setActiveSDDMock.mockRejectedValue(new Error('SDD framework "openspec" is not detected'))

    act(() => {
      const select = query('specs-framework-select') as HTMLSelectElement
      select.value = 'openspec'
      select.dispatchEvent(new Event('change', { bubbles: true }))
    })
    await flush()

    expect((query('specs-framework-select') as HTMLSelectElement).value).toBe('sdd')
    expect(query('specs-error')?.textContent).toContain('not detected')
  })
})

describe('SpecsPanel — findings gesture gating (AC5)', () => {
  it('renders an inert badge when the framework declares no fix-findings action', async () => {
    getSpecsStatusMock.mockResolvedValue(
      statusWithSections({
        actions: [makeAction({ kind: 'check', label: 'Check' })],
        findings: [makeFinding()],
        counts: {
          documents: 2,
          findings: 1,
          by_concept: { index: 1, decision: 1 },
          findings_by_kind: { INDEX_STALE: 1 },
        },
      }),
    )

    await renderPanel()

    const badge = query('specs-integrity-badge')
    expect(badge?.tagName).toBe('SPAN')
    expect(badge?.getAttribute('data-inert')).toBe('true')
    act(() => {
      badge?.dispatchEvent(new MouseEvent('click', { bubbles: true }))
    })
    expect(sendMock).not.toHaveBeenCalled()
  })

  it('dispatches the declared findings-fix action with every finding verbatim', async () => {
    const findings = [makeFinding({ file: 'specs/a.md' }), makeFinding({ kind: 'PATH_MISSING', file: 'specs/b.md' })]
    getSpecsStatusMock.mockResolvedValue(
      statusWithSections({
        actions: [
          makeAction({
            kind: 'fix-findings',
            label: 'Fix findings',
            skill: 'spec-update',
            prompt: 'Repair every finding, then re-check the tree.',
            requires: ['root', 'findings'],
          }),
        ],
        findings,
        counts: {
          documents: 2,
          findings: 2,
          by_concept: { index: 1, decision: 1 },
          findings_by_kind: { INDEX_STALE: 1, PATH_MISSING: 1 },
        },
      }),
    )

    await renderPanel()

    const badge = query('specs-integrity-badge')
    expect(badge?.tagName).toBe('BUTTON')
    act(() => {
      badge?.dispatchEvent(new MouseEvent('click', { bubbles: true }))
    })

    // Same preview gate: nothing sends until the dialog confirms.
    expect(sendMock).not.toHaveBeenCalled()
    act(() => {
      dialogEl('specs-dispatch-confirm')?.dispatchEvent(new MouseEvent('click', { bubbles: true }))
    })

    const args = sendMock.mock.calls[0] as unknown[]
    expect(args[1]).toEqual(['spec-update'])
    expect(args[0]).toContain('Repair every finding')
    expect(args[0]).toContain(findingLine(findings[0]!))
    expect(args[0]).toContain(findingLine(findings[1]!))
  })
})

describe('SpecsPanel — fetch, errors and refresh', () => {
  it('fetches on mount and renders the declared document labels', async () => {
    getSpecsStatusMock.mockResolvedValue(statusWithSections())

    await renderPanel()

    expect(getSpecsStatusMock).toHaveBeenCalledWith('p1')
    expect(text()).toContain('Specs Index')
    expect(text()).toContain('2 documents')
  })

  it('surfaces a failed fetch in the panel banner', async () => {
    getSpecsStatusMock.mockRejectedValue(new Error('parse exploded'))

    await renderPanel()

    expect(query('specs-error')?.textContent).toContain('parse exploded')
  })

  it('refetches on a specs-scoped tree_changed event and ignores foreign paths', async () => {
    getSpecsStatusMock.mockResolvedValue(statusWithSections())
    await renderPanel()
    getSpecsStatusMock.mockClear()

    act(() => fireTreeChanged({ paths: ['/ws/p1/src/main.go'] }))
    await flush()
    expect(getSpecsStatusMock).not.toHaveBeenCalled()

    act(() => fireTreeChanged({ paths: ['/ws/p1/specs/INDEX.md'] }))
    await flush()
    expect(getSpecsStatusMock).toHaveBeenCalledWith('p1')
  })

  it('refetches from the header refresh button', async () => {
    getSpecsStatusMock.mockResolvedValue(statusWithSections())
    await renderPanel()
    getSpecsStatusMock.mockClear()

    act(() => {
      query('specs-refresh')?.dispatchEvent(new MouseEvent('click', { bubbles: true }))
    })
    await flush()

    expect(getSpecsStatusMock).toHaveBeenCalledWith('p1')
  })

  it('opens a document in the file viewer against the parse’s workspace', async () => {
    getSpecsStatusMock.mockResolvedValue(statusWithSections())
    await renderPanel()

    act(() => {
      all('specs-node')[0]?.dispatchEvent(new MouseEvent('click', { bubbles: true }))
    })

    const viewer = useFileViewerStore.getState()
    expect(viewer.activeFile).toBe('/ws/p1/specs/INDEX.md')
  })

  it('renders non-fatal backend warnings without becoming an error', async () => {
    getSpecsStatusMock.mockResolvedValue(
      statusWithSections({ warnings: ['sdd: operator framework ignored'] }),
    )

    await renderPanel()

    expect(query('specs-warnings')?.textContent).toContain('operator framework ignored')
    expect(query('specs-error')).toBeNull()
  })
})

describe('SpecsPanel — production tree wiring and declared sections', () => {
  // Review 17: the PRODUCTION panel must wire the caller-owned expansion into
  // the tree calls — without `expandedNodes`/`onToggleNode` every node renders
  // always-expanded, the persisted per-project expansion is ignored, and a
  // folder/directory click reaches the viewer as a file open.
  it('wires expansion: folders start collapsed, toggle, persist per project, and never open as files', async () => {
    getSpecsStatusMock.mockResolvedValue(
      makeSpecsStatus({
        nodes: [
          makeNode({
            path: 'specs/changes/add-auth',
            label: 'add-auth',
            kind: 'directory',
            concept: 'change',
            children: ['specs/changes/add-auth/proposal.md', 'specs/changes/add-auth/archive'],
          }),
          makeNode({
            path: 'specs/changes/add-auth/proposal.md',
            label: 'Proposal',
            concept: 'proposal',
            parent: 'specs/changes/add-auth',
          }),
          // A childless directory instance: a LABEL, not a file link.
          makeNode({
            path: 'specs/changes/add-auth/archive',
            label: 'archive',
            kind: 'directory',
            concept: 'change',
            parent: 'specs/changes/add-auth',
            children: [],
          }),
        ],
        documents: [
          makeDocument({ path: 'specs/changes/add-auth/proposal.md', concept: 'proposal' }),
        ],
        views: [makeView({ id: 'tree', nodes: ['specs/changes/add-auth'] })],
      }),
    )

    await renderPanel()

    // Collapsed by default: the persisted-expansion default, and the child
    // rows are not on screen.
    const group = container?.querySelector<HTMLElement>(
      '[data-testid="specs-tree-group"][data-node="specs/changes/add-auth"]',
    )
    expect(group?.getAttribute('aria-expanded')).toBe('false')
    expect(all('specs-node')).toHaveLength(0)

    // Expand the directory through the caller-owned toggle.
    act(() => {
      group?.dispatchEvent(new MouseEvent('click', { bubbles: true }))
    })
    expect(group?.getAttribute('aria-expanded')).toBe('true')

    const paths = all('specs-node').map((n) => n.getAttribute('data-path'))
    expect(paths).toEqual(['specs/changes/add-auth/proposal.md', 'specs/changes/add-auth/archive'])

    // The document is a BUTTON that opens in the viewer; the childless
    // directory is a SPAN whose click never reaches the viewer.
    const proposal = all('specs-node')[0]!
    const archive = all('specs-node')[1]!
    expect(proposal.tagName).toBe('BUTTON')
    expect(archive.tagName).toBe('SPAN')
    act(() => {
      archive.dispatchEvent(new MouseEvent('click', { bubbles: true }))
    })
    expect(useFileViewerStore.getState().activeFile).toBeNull()
    act(() => {
      proposal.dispatchEvent(new MouseEvent('click', { bubbles: true }))
    })
    expect(useFileViewerStore.getState().activeFile).toBe('/ws/p1/specs/changes/add-auth/proposal.md')

    // The toggle went through the store's per-project expansion slice.
    expect(useSpecsStore.getState().treeExpandedNodes['p1']?.['specs/changes/add-auth']).toBe(true)
    act(() => {
      group?.dispatchEvent(new MouseEvent('click', { bubbles: true }))
    })
    expect(useSpecsStore.getState().treeExpandedNodes['p1']?.['specs/changes/add-auth']).toBe(false)
    expect(all('specs-node')).toHaveLength(0)
  })

  // Review 19: every document of one concept must read as its own row — two
  // decisions render two DISTINCT labels (the backend's per-instance label),
  // not one generic concept name eight times.
  it('renders distinct rows for two documents of the same concept', async () => {
    getSpecsStatusMock.mockResolvedValue(
      makeSpecsStatus({
        nodes: [
          makeNode({ path: 'specs/decisions/001-one.md', label: 'One: indexing', concept: 'decision' }),
          makeNode({ path: 'specs/decisions/002-two.md', label: 'Two: the viewer', concept: 'decision' }),
        ],
        documents: [
          makeDocument({ path: 'specs/decisions/001-one.md', concept: 'decision' }),
          makeDocument({ path: 'specs/decisions/002-two.md', concept: 'decision' }),
        ],
        views: [
          makeView({
            id: 'decisions',
            label: 'Decisions',
            kind: 'tree',
            nodes: ['specs/decisions/001-one.md', 'specs/decisions/002-two.md'],
          }),
        ],
      }),
    )

    await renderPanel()

    const rows = all('specs-node')
    expect(rows).toHaveLength(2)
    expect(rows.map((r) => r.textContent)).toEqual(['One: indexing', 'Two: the viewer'])
    expect(rows.map((r) => r.getAttribute('data-path'))).toEqual([
      'specs/decisions/001-one.md',
      'specs/decisions/002-two.md',
    ])
  })

  // Review 36: the parse model carries no finding→section membership, so two
  // declared non-tree sections must not render the same list twice — the
  // findings render ONCE, under the first declared section, and the duplicate
  // section (its second heading, its second set of fix chips and copy
  // buttons) never appears.
  it('renders a declared checks section without duplicating the findings list', async () => {
    getSpecsStatusMock.mockResolvedValue(
      statusWithSections({
        findings: [makeFinding()],
        counts: {
          documents: 2,
          findings: 1,
          by_concept: { index: 1, decision: 1 },
          findings_by_kind: { INDEX_STALE: 1 },
        },
        views: [
          makeView({ id: 'tree', label: 'Specification tree', kind: 'tree', nodes: ['specs/INDEX.md'] }),
          makeView({ id: 'findings', label: 'Findings', kind: 'findings', nodes: [] }),
          makeView({ id: 'checks', label: 'Checks', kind: 'checks', nodes: [] }),
        ],
      }),
    )

    await renderPanel()

    // One section carries the list — the FIRST declared non-tree view.
    const sections = all('specs-findings')
    expect(sections).toHaveLength(1)
    expect(sections[0]?.getAttribute('data-view')).toBe('findings')
    // One card, one fix chip, one copy button — no duplicated second set.
    expect(all('specs-finding')).toHaveLength(1)
    expect(all('specs-finding-fix')).toHaveLength(1)
    expect(all('specs-finding-copy')).toHaveLength(1)
    // The duplicate section never renders (no "Checks" heading over the same
    // cards).
    expect(text()).not.toContain('Checks')
  })

  // Review 54: a fix-findings action declared WITHOUT a skill is not a
  // dispatchable gesture — the header badge must be INERT with the real
  // reason (the same predicate the per-finding chips use), not a live button
  // whose click silently does nothing.
  it('renders the header badge inert for a skillless declared fix-findings action', async () => {
    getSpecsStatusMock.mockResolvedValue(
      statusWithSections({
        actions: [
          makeAction({
            kind: 'fix-findings',
            label: 'Fix findings',
            skill: '',
            requires: ['root', 'findings'],
            available: true,
          }),
        ],
        findings: [makeFinding()],
        counts: {
          documents: 2,
          findings: 1,
          by_concept: { index: 1, decision: 1 },
          findings_by_kind: { INDEX_STALE: 1 },
        },
      }),
    )

    await renderPanel()

    const badge = query('specs-integrity-badge')
    expect(badge?.tagName).toBe('SPAN')
    expect(badge?.getAttribute('data-inert')).toBe('true')
    expect(badge?.getAttribute('title')).toBe(
      'The framework declares this action with no skill to run',
    )
    // The per-finding chip agrees with the header: inert, same reason.
    const chip = query('specs-finding-fix')
    expect(chip?.getAttribute('data-inert')).toBe('true')
    expect(chip?.getAttribute('title')).toBe(
      'The framework declares this action with no skill to run',
    )
    act(() => {
      badge?.dispatchEvent(new MouseEvent('click', { bubbles: true }))
    })
    expect(sendMock).not.toHaveBeenCalled()
  })
})
