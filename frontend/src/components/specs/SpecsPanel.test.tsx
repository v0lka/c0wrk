// @vitest-environment jsdom
// SpecsPanel — the panel orchestrator over specsStore (ADR-105): the
// mount fetch, the in-panel workspace:tree_changed subscription with the
// specs/ path filter, the read path through the file viewer, and the four
// render states (loading, error-alongside-previous-status, empty, full
// body). The api boundary itself is covered by api/specs.test.ts; the
// quick-actions/empty-state dispatch wiring by their own suites.
import { afterEach, beforeEach, describe, expect, it, vi } from 'vitest'
import { act } from 'react'
import { createRoot, type Root } from 'react-dom/client'

import { SpecsPanel } from './SpecsPanel'
import { useSpecsStore } from '@/stores/specsStore'
import { useProjectStore } from '@/stores/projectStore'
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
// their dispatch behavior is covered by their own suites.
vi.mock('@/hooks/useMessageSender', () => ({
  useMessageSender: () => ({
    send: vi.fn(async () => {}),
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
    ],
    index_tasks: [{ task: 'Writing tests', specs: ['./domains/testing.md'] }],
    adrs: [
      {
        number: '105',
        slug: 'specs-sidebar-panel',
        file: 'decisions/105-specs-sidebar-panel.md',
        status: 'accepted',
        title: 'Specs sidebar panel',
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
    counts: { files: 2, adrs: 2, findings: 1, findings_by_kind: { PATH_MISSING: 1 } },
  }
}

beforeEach(() => {
  getSpecsStatusMock.mockReset()
  getSpecsStatusMock.mockResolvedValue(makeStatus(true))
  eventHandlers.clear()
  useSpecsStore.getState().reset()
  useProjectStore.setState({ activeProjectId: 'p1' })
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
  it('fetches on mount and renders the full parsed body', async () => {
    await renderPanel()

    expect(getSpecsStatusMock).toHaveBeenCalledWith('p1')
    // Header: integrity badge (1 finding) + counts.
    expect(query('specs-integrity-badge')!.textContent).toContain('1 finding')
    expect(text()).toContain('2 spec files')
    expect(text()).toContain('2 ADRs')
    // INDEX task table, section tree, ADR timeline, findings list.
    expect(text()).toContain('Writing tests')
    expect(text()).toContain('Testing')
    expect(query('specs-adr')!.textContent).toContain('Specs sidebar panel')
    expect(text()).toContain('core/x.go does not exist')
    // Superseded record: status chip + supersession to the successor.
    const superseded = container!.querySelector<HTMLElement>('[data-number="038"]')!
    expect(superseded.getAttribute('data-status')).toBe('superseded')
    expect(query('specs-adr-supersession')!.textContent).toContain('105')
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
    expect(query('specs-header')).toBeNull()
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
    expect(text()).toContain('Writing tests')
  })

  it('renders nothing when there is no active project (defensive neutral)', async () => {
    useProjectStore.setState({ activeProjectId: null })
    await renderPanel()

    expect(getSpecsStatusMock).not.toHaveBeenCalled()
    expect(query('specs-panel')).toBeNull()
  })

  it("renders the loading state — never the previous project's tree — across a project switch", async () => {
    await renderPanel()
    expect(text()).toContain('Writing tests')

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
    expect(text()).not.toContain('Writing tests')
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

describe('SpecsPanel — read path (file viewer)', () => {
  // Coverage boundary note: openSpec's cross-project no-op (store stamp ≠
  // active project) is defense in depth under the render guard — rows for
  // another project cannot render, so the guard has no reachable UI click;
  // the cross-project behavior itself is pinned in specsStore.test.ts and
  // the project-switch render test above.
  it('opens a spec file, an INDEX target, and an ADR through openFile', async () => {
    await renderPanel()

    // Section tree row → absolute path under the specs root.
    act(() => {
      query('specs-file')!.click()
    })
    // INDEX link: the verbatim './domains/testing.md' target resolves.
    act(() => {
      container!.querySelector<HTMLButtonElement>('[data-spec="./domains/testing.md"]')!.click()
    })
    // ADR rows → their decisions/ files (the timeline is newest-first, so
    // the first row is ADR-105 and the superseded 038 is second).
    act(() => {
      query('specs-adr')!.click()
      container!.querySelector<HTMLButtonElement>('[data-testid="specs-adr"][data-number="038"]')!.click()
    })
    // The supersession chip opens the SUCCESSOR's file (deduped here — the
    // 105 row above already opened it; openFile re-activates the tab).
    act(() => {
      query('specs-adr-supersession')!.click()
    })
    // A finding's file link opens too.
    act(() => {
      query('specs-finding-file')!.click()
    })

    const openTabs = useFileViewerStore.getState().openTabs
    expect(openTabs).toContain('/ws/p1/specs/domains/testing.md')
    expect(openTabs).toContain('/ws/p1/specs/decisions/105-specs-sidebar-panel.md')
    expect(openTabs).toContain('/ws/p1/specs/decisions/038-old-decision.md')
    // Opening uncollapses the viewer.
    expect(useFileViewerStore.getState().collapsed).toBe(false)
  })
})
