// @vitest-environment jsdom
// SpecsPanel — the panel orchestrator over specsStore (ADR-106): the
// mount fetch, the in-panel workspace:tree_changed subscription with the
// specs/ path filter, the manual refresh button (the header's icon-only
// refetch trigger), the read path through the file viewer, and the four
// render states (loading, error-alongside-previous-status, empty, full
// body). The api boundary itself is covered by api/specs.test.ts; the
// quick-actions/empty-state dispatch wiring by their own suites.
import { afterEach, beforeEach, describe, expect, it, vi } from 'vitest'
import { act } from 'react'
import { createRoot, type Root } from 'react-dom/client'

import { SpecsPanel } from './SpecsPanel'
import { useSpecsStore } from '@/stores/specsStore'
import { useProjectStore } from '@/stores/projectStore'
import { useSessionStore } from '@/stores/sessionStore'
import { useFileViewerStore } from '@/stores/fileViewerStore'
import type { SpecsStatus } from '@/types/models'

// The RPC is mocked at the api boundary: the panel only owns orchestration.
const getSpecsStatusMock = vi.fn(async (_projectId: string) => makeStatus(true))
vi.mock('@/api/specs', () => ({
  getSpecsStatus: (...args: unknown[]) => getSpecsStatusMock(...(args as [string])),
}))

// Event subscription capture: tests fire the captured handlers directly —
// the refresh TRIGGER is under test, not the transport.
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
}))

// The panel renders the quick actions / empty state (which use the sender);
// their dispatch behavior is covered by their own suites. The captured send
// mock also serves the fix-findings badge tests below.
const sendMock = vi.hoisted(() => vi.fn(async (..._args: unknown[]) => {}))
vi.mock('@/hooks/useMessageSender', () => ({
  useMessageSender: () => ({
    send: sendMock,
    cancel: vi.fn(async () => {}),
    isProcessing: false,
  }),
}))

let container: HTMLElement | null = null
let activeRoot: Root | null = null

/** The macrotask-flush idiom of the WorkspacePanel suite. */
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

function fireTreeChanged(...data: unknown[]): void {
  for (const handler of eventHandlers.get('workspace:tree_changed') ?? []) {
    handler(...data)
  }
}

function text(): string {
  return container?.textContent ?? ''
}

function query(testId: string): HTMLElement | null {
  return container?.querySelector(`[data-testid="${testId}"]`) ?? null
}

/** A full parsed tree: every section has something to assert on. */
function makeStatus(present: boolean): SpecsStatus {
  if (!present) {
    return {
      present: false,
      root: '/ws/p1/specs',
      files: [],
      index_tasks: [],
      adrs: [],
      findings: [],
      counts: { files: 0, adrs: 0, findings: 0, findings_by_kind: {} },
    }
  }
  return {
    present: true,
    root: '/ws/p1/specs',
    files: [
      { path: 'INDEX.md', title: 'Specs Index' },
      { path: 'domains/testing.md', section: 'domains', title: 'Testing', key_files: ['core/x.go'] },
      { path: 'decisions/106-specs-sidebar-panel.md', section: 'decisions', title: 'Specs sidebar panel' },
      { path: 'decisions/038-old-decision.md', section: 'decisions', title: 'Old decision' },
    ],
    index_tasks: [{ task: 'Writing tests', specs: ['./domains/testing.md'] }],
    adrs: [
      {
        number: '105',
        slug: 'specs-sidebar-panel',
        file: 'decisions/106-specs-sidebar-panel.md',
        status: 'accepted',
        title: 'Specs sidebar panel',
        date: '2026-01-15',
      },
      {
        number: '038',
        slug: 'old-decision',
        file: 'decisions/038-old-decision.md',
        status: 'superseded',
        superseded_by: '105',
        title: 'Old decision',
      },
    ],
    findings: [
      { kind: 'PATH_MISSING', file: 'domains/testing.md', detail: 'core/x.go does not exist' },
    ],
    counts: { files: 4, adrs: 2, findings: 1, findings_by_kind: { PATH_MISSING: 1 } },
  }
}

beforeEach(() => {
  getSpecsStatusMock.mockReset()
  getSpecsStatusMock.mockResolvedValue(makeStatus(true))
  sendMock.mockReset()
  sendMock.mockResolvedValue(undefined)
  eventHandlers.clear()
  useSpecsStore.getState().reset()
  useProjectStore.setState({ activeProjectId: 'p1' })
  useSessionStore.setState({ sessions: null, activeSessionId: null })
  useFileViewerStore.setState({ openTabs: [], activeFile: null, collapsed: true })
})

afterEach(() => {
  if (activeRoot) {
    act(() => {
      activeRoot!.unmount()
    })
    activeRoot = null
    container = null
  }
})

describe('SpecsPanel — render states', () => {
  it('fetches on mount and renders the collapsed tree plus findings', async () => {
    await renderPanel()

    expect(getSpecsStatusMock).toHaveBeenCalledWith('p1')
    // Header: integrity badge (1 finding) + counts.
    expect(query('specs-integrity-badge')!.textContent).toContain('1 finding')
    expect(text()).toContain('4 spec files')
    expect(text()).toContain('2 ADRs')
    // The INDEX navigation and the ADR timeline live on the overview page
    // now — the panel renders NEITHER, even though the parse carries both.
    expect(query('specs-index')).toBeNull()
    expect(query('specs-adrs')).toBeNull()
    expect(text()).not.toContain('Writing tests')
    // The tree renders collapsed: group headers visible, no file rows yet.
    expect(query('specs-tree')).not.toBeNull()
    expect(query('specs-file')).toBeNull()
    // Expanding the domains group reveals its leaf (title + key-files chip).
    act(() => {
      container!
        .querySelector<HTMLElement>('[data-testid="specs-tree-group"][data-node="domains"]')!
        .click()
    })
    expect(query('specs-file')!.textContent).toContain('Testing')
    // The DECISIONS date column (ADR-109): the dated 105 leaf carries its
    // blue-tinted date; the undated 038 leaf renders none.
    act(() => {
      container!
        .querySelector<HTMLElement>('[data-testid="specs-tree-group"][data-node="decisions"]')!
        .click()
    })
    const datedLeaf = container!.querySelector('[data-path="decisions/106-specs-sidebar-panel.md"]')!
    expect(datedLeaf.querySelector('[data-testid="specs-file-date"]')!.textContent).toBe('2026-01-15')
    const undatedLeaf = container!.querySelector('[data-path="decisions/038-old-decision.md"]')!
    expect(undatedLeaf.querySelector('[data-testid="specs-file-date"]')).toBeNull()
    // The findings list renders the detail (the cascade card's own row).
    expect(text()).toContain('core/x.go does not exist')
  })

  it('renders the loading state before the first status lands', async () => {
    getSpecsStatusMock.mockReturnValue(new Promise(() => {}))
    await renderPanel()

    expect(text()).toContain('Loading specs…')
  })

  it('renders the empty state with the Initialize CTA when present=false', async () => {
    getSpecsStatusMock.mockResolvedValue(makeStatus(false))
    await renderPanel()

    expect(query('specs-empty-state')).not.toBeNull()
    expect(query('specs-initialize-cta')).not.toBeNull()
    expect(text()).toContain('/ws/p1/specs')
    // The full body does not render alongside the empty state.
    expect(query('specs-tree')).toBeNull()
    // The header STAYS with the clean badge (the opener is gone — ADR-109
    // removed the overview page).
    expect(query('specs-header')).not.toBeNull()
    expect(query('specs-open-overview')).toBeNull()
    expect(query('specs-integrity-badge')!.textContent).toContain('Integrity OK')
  })

  it('keeps the previous status rendered alongside an error banner', async () => {
    await renderPanel()
    getSpecsStatusMock.mockRejectedValue(new Error('boom'))
    act(() => {
      fireTreeChanged()
    })
    await flush()

    expect(query('specs-error')!.textContent).toContain('boom')
    // The previous tree stays rendered (error ≠ data loss).
    expect(query('specs-tree')).not.toBeNull()
  })

  it('renders nothing when there is no active project (defensive neutral)', async () => {
    useProjectStore.setState({ activeProjectId: null })
    await renderPanel()

    expect(getSpecsStatusMock).not.toHaveBeenCalled()
    expect(query('specs-panel')).toBeNull()
  })

  it("renders the loading state — never the previous project's tree — across a project switch", async () => {
    await renderPanel()
    expect(query('specs-tree')).not.toBeNull()

    // Switch to p2 whose fetch hangs: p1's tree must vanish for the whole
    // flight (the store clears it at fetch start; the render guard pairs
    // the store's projectId with the active project either way).
    getSpecsStatusMock.mockReturnValue(new Promise(() => {}))
    await act(async () => {
      useProjectStore.setState({ activeProjectId: 'p2' })
    })
    await flush()

    expect(getSpecsStatusMock).toHaveBeenCalledWith('p2')
    expect(text()).toContain('Loading specs…')
    expect(query('specs-tree')).toBeNull()
  })
})

describe('SpecsPanel — refresh trigger (in-panel subscription)', () => {
  it('refetches on a tree_changed whose paths touch specs/', async () => {
    await renderPanel()
    expect(getSpecsStatusMock).toHaveBeenCalledTimes(1)

    act(() => {
      fireTreeChanged({ paths: ['/ws/p1/specs/INDEX.md'] })
    })
    await flush()
    expect(getSpecsStatusMock).toHaveBeenCalledTimes(2)
  })

  it('refetches on a payload without paths (the production shapes)', async () => {
    await renderPanel()

    act(() => {
      fireTreeChanged({ research_scoped: true })
    })
    await flush()
    expect(getSpecsStatusMock).toHaveBeenCalledTimes(2)

    act(() => {
      fireTreeChanged()
    })
    await flush()
    expect(getSpecsStatusMock).toHaveBeenCalledTimes(3)
  })

  it('skips the refetch for paths outside the specs tree', async () => {
    await renderPanel()

    act(() => {
      fireTreeChanged({ paths: ['/ws/p1/src/main.go'] })
    })
    await flush()
    expect(getSpecsStatusMock).toHaveBeenCalledTimes(1)
  })

  it('unsubscribes on unmount', async () => {
    await renderPanel()
    expect(eventHandlers.get('workspace:tree_changed')).toHaveLength(1)

    act(() => {
      activeRoot!.unmount()
    })
    activeRoot = null
    expect(eventHandlers.get('workspace:tree_changed')).toHaveLength(0)
  })
})

describe('SpecsPanel — session switch (the parse follows the active session)', () => {
  // The backend scans the ACTIVE session's workspace (like research/papers),
  // so a session switch is a refetch trigger exactly like a project switch —
  // same fetch call (the server resolves the session), new parse.
  it('refetches the active project when the active session changes', async () => {
    await renderPanel()
    expect(getSpecsStatusMock).toHaveBeenCalledTimes(1)

    await act(async () => {
      useSessionStore.setState({ sessions: null, activeSessionId: 's2' })
    })
    await flush()

    // EXACTLY one more fetch: the mount/project effect refires; the
    // tree_changed effect re-keying on the session only resubscribes.
    expect(getSpecsStatusMock).toHaveBeenCalledTimes(2)
    expect(getSpecsStatusMock).toHaveBeenLastCalledWith('p1')

    // The resubscribed tree_changed subscription still refetches (a broken
    // resubscription would leave a dead or duplicated listener).
    act(() => {
      fireTreeChanged({ paths: ['/ws/p1/specs/INDEX.md'] })
    })
    await flush()
    expect(getSpecsStatusMock).toHaveBeenCalledTimes(3)
  })

  it('renders the loading state — never the previous session\'s tree — across a session switch', async () => {
    await renderPanel()
    expect(query('specs-tree')).not.toBeNull()

    // s2's fetch hangs: s1's tree must vanish for the whole flight (the
    // store's session-stamped clean slate clears it at fetch start).
    getSpecsStatusMock.mockReturnValue(new Promise(() => {}))
    await act(async () => {
      useSessionStore.setState({ sessions: null, activeSessionId: 's2' })
    })
    await flush()

    expect(text()).toContain('Loading specs…')
    expect(query('specs-tree')).toBeNull()
  })
})

describe('SpecsPanel — manual refresh button (specs-refresh)', () => {
  it('re-fetches the ACTIVE project on click', async () => {
    await renderPanel()
    expect(getSpecsStatusMock).toHaveBeenCalledTimes(1)

    act(() => {
      query('specs-refresh')!.click()
    })
    await flush()

    expect(getSpecsStatusMock).toHaveBeenCalledTimes(2)
    expect(getSpecsStatusMock).toHaveBeenLastCalledWith('p1')
    // Enabled again once the flight lands.
    expect(query('specs-refresh')!.hasAttribute('disabled')).toBe(false)
  })

  it('is icon-only: no text node — svg + attributes only — with the exact tooltip', async () => {
    await renderPanel()

    const refresh = query('specs-refresh')!
    expect(refresh.textContent).toBe('')
    expect(refresh.querySelector('svg')).not.toBeNull()
    expect(refresh.getAttribute('title')).toBe(
      'Re-parse the specs/ tree and re-check integrity',
    )
    expect(refresh.getAttribute('aria-label')).toBe(
      'Re-parse the specs/ tree and re-check integrity',
    )
  })

  it('is disabled while the refetch is in flight and the previous status stays rendered', async () => {
    await renderPanel()
    expect(query('specs-refresh')!.hasAttribute('disabled')).toBe(false)

    let resolve!: (s: SpecsStatus) => void
    getSpecsStatusMock.mockReturnValue(
      new Promise<SpecsStatus>((res) => {
        resolve = res
      }),
    )
    act(() => {
      query('specs-refresh')!.click()
    })
    await flush()

    expect(getSpecsStatusMock).toHaveBeenCalledTimes(2)
    // In flight → inert (a double click must not stack a duplicate fetch).
    expect(query('specs-refresh')!.hasAttribute('disabled')).toBe(true)
    // Same-project refetch: the previous parse stays on screen — no
    // "Loading specs…" body swap, no tree loss (the store's retention +
    // LWW ticket contract, pinned in specsStore.test.ts too).
    expect(query('specs-tree')).not.toBeNull()
    expect(text()).not.toContain('Loading specs…')

    await act(async () => {
      resolve(makeStatus(true))
    })
    await flush()
    expect(query('specs-refresh')!.hasAttribute('disabled')).toBe(false)
  })

  it('renders and works when present=false (a tree-less project is re-parseable)', async () => {
    getSpecsStatusMock.mockResolvedValue(makeStatus(false))
    await renderPanel()

    expect(query('specs-empty-state')).not.toBeNull()
    const refresh = query('specs-refresh')!
    expect(refresh.hasAttribute('disabled')).toBe(false)

    act(() => {
      refresh.click()
    })
    await flush()

    expect(getSpecsStatusMock).toHaveBeenCalledTimes(2)
    expect(getSpecsStatusMock).toHaveBeenLastCalledWith('p1')
  })
})

describe('SpecsPanel — read path (file viewer)', () => {
  // Coverage boundary note: openSpec's cross-project no-op (store stamp ≠
  // active project) is defense in depth under the render guard — rows for
  // another project cannot render, so the guard has no reachable UI click;
  // the cross-project behavior itself is pinned in specsStore.test.ts and
  // the project-switch render test above.
  it('opens a tree leaf and a finding file through openFile', async () => {
    await renderPanel()

    // The tree starts collapsed: expand the domains group, then click the
    // leaf → the absolute path under the specs root.
    act(() => {
      container!
        .querySelector<HTMLElement>('[data-testid="specs-tree-group"][data-node="domains"]')!
        .click()
    })
    act(() => {
      query('specs-file')!.click()
    })
    // A finding's file link opens too.
    act(() => {
      query('specs-finding-file')!.click()
    })

    const openTabs = useFileViewerStore.getState().openTabs
    expect(openTabs).toContain('/ws/p1/specs/domains/testing.md')
    // Opening uncollapses the viewer.
    expect(useFileViewerStore.getState().collapsed).toBe(false)
  })

  it('expansion survives a tab switch (remount) via the per-project store state', async () => {
    await renderPanel()

    act(() => {
      container!
        .querySelector<HTMLElement>('[data-testid="specs-tree-group"][data-node="domains"]')!
        .click()
    })
    expect(query('specs-file')).not.toBeNull()
    expect(useSpecsStore.getState().treeExpandedNodes.p1).toEqual({ domains: true })

    // The Specs tab switching away unmounts the panel; switching back
    // remounts it — the expanded set must come from the store, not from
    // component state.
    act(() => {
      activeRoot!.unmount()
    })
    await renderPanel()

    const domains = container!.querySelector<HTMLElement>(
      '[data-testid="specs-tree-group"][data-node="domains"]',
    )!
    expect(domains.getAttribute('aria-expanded')).toBe('true')
    expect(query('specs-file')).not.toBeNull()
  })

  it("a foreign project never inherits another project's expanded set", async () => {
    await renderPanel()

    act(() => {
      container!
        .querySelector<HTMLElement>('[data-testid="specs-tree-group"][data-node="domains"]')!
        .click()
    })

    // Switch to p2: its own (empty) expansion set applies — the domains
    // group starts collapsed even though p1 had it expanded.
    getSpecsStatusMock.mockResolvedValue(makeStatus(true))
    await act(async () => {
      useProjectStore.setState({ activeProjectId: 'p2' })
    })
    await flush()

    const domains = container!.querySelector<HTMLElement>(
      '[data-testid="specs-tree-group"][data-node="domains"]',
    )!
    expect(domains.getAttribute('aria-expanded')).toBe('false')
    expect(useSpecsStore.getState().treeExpandedNodes.p2).toBeUndefined()
    // p1's set is untouched for the switch back.
    expect(useSpecsStore.getState().treeExpandedNodes.p1).toEqual({ domains: true })
  })

  it('clicking the findings badge dispatches the fix-findings task into the chat', async () => {
    await renderPanel()

    const badge = query('specs-integrity-badge')!
    expect(badge.textContent).toContain('1 finding')
    act(() => {
      badge.click()
    })

    expect(sendMock).toHaveBeenCalledTimes(1)
    const [prompt, skills, , , , options] = sendMock.mock.calls[0]!
    expect(String(prompt)).toContain('Fix every tier-1 integrity finding')
    // The prompt enumerates the CURRENT parse's findings verbatim.
    expect(String(prompt)).toContain('[PATH_MISSING] domains/testing.md: core/x.go does not exist')
    expect(skills).toEqual(['spec-update'])
    expect(options).toMatchObject({ newSession: false })
  })

  it('shift-clicking the findings badge targets a NEW session', async () => {
    await renderPanel()

    const badge = query('specs-integrity-badge')!
    await act(async () => {
      badge.dispatchEvent(new MouseEvent('click', { bubbles: true, shiftKey: true }))
    })

    expect(sendMock).toHaveBeenCalledTimes(1)
    expect(sendMock.mock.calls[0]![5]).toMatchObject({ newSession: true })
  })

  it('the badge is inert without findings (the OK chip dispatches nothing)', async () => {
    const clean = makeStatus(true)
    clean.findings = []
    clean.counts = { ...clean.counts, findings: 0, findings_by_kind: {} }
    getSpecsStatusMock.mockResolvedValue(clean)
    await renderPanel()

    const badge = query('specs-integrity-badge')!
    expect(badge.textContent).toContain('Integrity OK')
    act(() => {
      badge.click()
    })
    expect(sendMock).not.toHaveBeenCalled()
  })
})

describe('SpecsPanel — per-finding fix badge (specs-finding-fix)', () => {
  /** The parse with TWO findings: a per-finding dispatch must quote only
   *  the clicked one — never the sibling backlog. */
  function statusWithTwoFindings(): SpecsStatus {
    const status = makeStatus(true)
    status.findings = [
      { kind: 'PATH_MISSING', file: 'domains/testing.md', detail: 'core/x.go does not exist' },
      { kind: 'INDEX_STALE', file: 'domains/orphan.md', detail: 'not listed in INDEX.md' },
    ]
    status.counts = {
      ...status.counts,
      findings: 2,
      findings_by_kind: { PATH_MISSING: 1, INDEX_STALE: 1 },
    }
    return status
  }

  it('clicking a finding badge dispatches exactly ONE task quoting only that finding', async () => {
    getSpecsStatusMock.mockResolvedValue(statusWithTwoFindings())
    await renderPanel()

    const fixes = document.querySelectorAll<HTMLElement>('[data-testid="specs-finding-fix"]')
    expect(fixes).toHaveLength(2)
    act(() => {
      fixes[1]!.click()
    })

    // Exactly one dispatch — the aggregate badge is a different affordance.
    expect(sendMock).toHaveBeenCalledTimes(1)
    const [prompt, skills, , , , options] = sendMock.mock.calls[0]!
    expect(skills).toEqual(['spec-update'])
    expect(options).toMatchObject({ newSession: false })
    // The per-finding opener + the clicked finding verbatim...
    expect(String(prompt)).toContain('Fix the following tier-1 integrity finding')
    expect(String(prompt)).toContain('[INDEX_STALE] domains/orphan.md: not listed in INDEX.md')
    // ...and NOTHING of the sibling finding (exactly one bullet line total).
    expect(String(prompt)).not.toContain('[PATH_MISSING]')
    expect(String(prompt)).not.toContain('core/x.go does not exist')
    const lines = String(prompt)
      .split('\n')
      .filter((l) => l.startsWith('- ['))
    expect(lines).toEqual(['- [INDEX_STALE] domains/orphan.md: not listed in INDEX.md'])
    // A fix dispatch is a chat gesture — the viewer stays untouched.
    expect(useFileViewerStore.getState().openTabs).toEqual([])
  })

  it('shift-clicking a finding badge targets a NEW session', async () => {
    await renderPanel()

    const fix = query('specs-finding-fix')!
    await act(async () => {
      fix.dispatchEvent(new MouseEvent('click', { bubbles: true, shiftKey: true }))
    })

    expect(sendMock).toHaveBeenCalledTimes(1)
    expect(sendMock.mock.calls[0]![5]).toMatchObject({ newSession: true })
  })

  it('the aggregate badge keeps its own fix-all dispatch alongside the card badges', async () => {
    getSpecsStatusMock.mockResolvedValue(statusWithTwoFindings())
    await renderPanel()

    act(() => {
      query('specs-integrity-badge')!.click()
    })

    expect(sendMock).toHaveBeenCalledTimes(1)
    const [prompt] = sendMock.mock.calls[0]!
    expect(String(prompt)).toContain('Fix every tier-1 integrity finding')
    // The aggregate still enumerates BOTH findings of the current parse...
    expect(String(prompt)).toContain('[PATH_MISSING] domains/testing.md: core/x.go does not exist')
    expect(String(prompt)).toContain('[INDEX_STALE] domains/orphan.md: not listed in INDEX.md')
    // ...while the per-finding badges render alongside it unchanged.
    expect(document.querySelectorAll('[data-testid="specs-finding-fix"]')).toHaveLength(2)
  })
})
