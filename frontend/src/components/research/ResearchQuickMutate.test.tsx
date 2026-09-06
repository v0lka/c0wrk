// @vitest-environment jsdom
// ResearchQuickMutate — the dashboard's quick status-flip surface. Covers the
// step-13 hardening: the shared applyGraphOrRefresh routing ([18]b), the
// expected-R-NNN argument ([19]a), the block-wide disable while saving
// ([21]a), and the action-generation guard against same-path re-entry
// ([71]a).
import { afterEach, beforeEach, describe, expect, it, vi } from 'vitest'
import { act } from 'react'
import { createRoot, type Root } from 'react-dom/client'

import { ResearchQuickMutate } from './ResearchQuickMutate'
import { applyGraphOrRefresh } from './applyGraphOrRefresh'
import { updateHypothesis } from '@/api/research'
import { useResearchStore, selectActiveProject } from '@/stores/researchStore'
import { useProjectStore } from '@/stores/projectStore'
import type {
  ResearchStatus,
  ResearchGraphResponse,
} from '@/types/models'

vi.mock('@/api/research', () => ({
  updateHypothesis: vi.fn(),
  getResearchStatus: vi.fn(),
  getResearchNextStep: vi.fn(),
}))

// Spy on the shared convergence path while delegating to the real
// implementation, so the tests assert BOTH the routing ([18]b) and the real
// store effects.
vi.mock('./applyGraphOrRefresh', async (importOriginal) => {
  const actual = await importOriginal<typeof import('./applyGraphOrRefresh')>()
  return {
    ...actual,
    applyGraphOrRefresh: vi.fn(actual.applyGraphOrRefresh),
  }
})

vi.mock('@/lib/logger', () => ({
  logger: { error: vi.fn(), warn: vi.fn(), info: vi.fn(), debug: vi.fn() },
}))

function makeStatus(): ResearchStatus {
  return {
    enabled: true,
    project_id: 'p1',
    research_root: '/root/.research',
    root: {
      path: '/root/.research',
      index: [{ id: 'R-001', path: 'R-001/brief.md' }],
      active_project_id: 'R-001',
      projects: [
        {
          id: 'R-001',
          brief: { id: 'R-001', title: 'Test Research' },
          graph: {
            nodes: [
              { id: 'H-001', title: 'First', status: 'open' },
              { id: 'H-002', title: 'Second', status: 'in-progress' },
            ],
            edges: [],
          },
          metrics: {
            total: 2,
            by_status: { open: 1, 'in-progress': 1 },
            confirmation_rate: 0,
            depth: 1,
            breadth: 1,
            active_front: ['H-001', 'H-002'],
          },
          prior_art_count: 0,
          has_report: false,
          log: [],
        },
      ],
    },
  } as unknown as ResearchStatus
}

function makeGraphResponse(nodeCount: number): ResearchGraphResponse {
  const status = makeStatus()
  const p = status.root!.projects[0]!
  return {
    project_id: 'R-001',
    graph: {
      nodes: Array.from({ length: nodeCount }, (_, i) => ({
        id: `H-00${i + 1}`,
        title: `h${i + 1}`,
        status: 'in-progress',
      })),
      edges: [],
    },
    metrics: p.metrics,
    has_report: false,
    log: p.log,
  }
}

let activeRoot: Root | null = null

async function renderQuickMutate(): Promise<HTMLElement> {
  const container = document.createElement('div')
  document.body.replaceChildren(container)
  const root = createRoot(container)
  activeRoot = root
  await act(async () => {
    root.render(<ResearchQuickMutate />)
  })
  return container
}

function selectEl(container: HTMLElement, id: string): HTMLSelectElement {
  return container.querySelector<HTMLSelectElement>(
    `select[aria-label="Status for ${id}"]`,
  )!
}

function changeSelect(el: HTMLSelectElement, value: string): void {
  el.value = value
  el.dispatchEvent(new Event('change', { bubbles: true }))
}

const flush = () => act(async () => { await new Promise((r) => setTimeout(r, 0)) })

beforeEach(() => {
  vi.clearAllMocks()
  useProjectStore.setState({ activeProjectId: 'p1' })
  useResearchStore.getState().reset()
  useResearchStore.getState().loadStatus(makeStatus(), 'p1')
})

afterEach(() => {
  if (activeRoot) {
    act(() => {
      activeRoot!.unmount()
    })
    activeRoot = null
  }
})

describe('ResearchQuickMutate — step-13 hardening', () => {
  it('routes the mutated graph through the shared applyGraphOrRefresh helper ([18]b)', async () => {
    const res = makeGraphResponse(2)
    vi.mocked(updateHypothesis).mockResolvedValue(res)

    const container = await renderQuickMutate()
    await act(async () => {
      changeSelect(selectEl(container, 'H-001'), 'in-progress')
    })
    await flush()

    expect(updateHypothesis).toHaveBeenCalledWith('p1', 'R-001', 'H-001', {
      status: 'in-progress',
    })
    expect(applyGraphOrRefresh).toHaveBeenCalledWith(
      res,
      'p1',
      expect.any(Number),
    )
    // The helper really applied it (the store carries the response's graph).
    expect(selectActiveProject(useResearchStore.getState())?.graph.nodes).toHaveLength(2)
  })

  it('bails without an RPC when the research snapshot belongs to another workspace project (cross-project guard)', async () => {
    const container = await renderQuickMutate()
    // Project switch after render: the research store still holds p1's
    // graph while the project store moved to p2. H-001-style ids collide
    // across projects, so the flip must never be sent.
    useProjectStore.setState({ activeProjectId: 'p2' })

    await act(async () => {
      changeSelect(selectEl(container, 'H-001'), 'in-progress')
    })
    await flush()

    expect(updateHypothesis).not.toHaveBeenCalled()
    expect(applyGraphOrRefresh).not.toHaveBeenCalled()
    // The mismatch surfaces on the block's shared error banner instead…
    expect(container.querySelector('[role="alert"]')?.textContent).toContain(
      'different project',
    )
    // …and no spinner state is left stuck.
    expect(selectEl(container, 'H-001').disabled).toBe(false)
  })

  it('disables every row while a save is in flight ([21]a)', async () => {
    let resolveRpc: (v: ResearchGraphResponse) => void = () => {}
    vi.mocked(updateHypothesis).mockImplementation(
      () =>
        new Promise<ResearchGraphResponse>((resolve) => {
          resolveRpc = resolve
        }),
    )

    const container = await renderQuickMutate()
    await act(async () => {
      changeSelect(selectEl(container, 'H-001'), 'in-progress')
    })
    await flush()

    // BOTH rows are disabled — the block mutates one hypothesis at a time.
    expect(selectEl(container, 'H-001').disabled).toBe(true)
    expect(selectEl(container, 'H-002').disabled).toBe(true)

    await act(async () => {
      resolveRpc(makeGraphResponse(2))
    })
    await flush()

    expect(selectEl(container, 'H-001').disabled).toBe(false)
    expect(selectEl(container, 'H-002').disabled).toBe(false)
  })

  it("a superseded flip's late failure does not annotate the newer flip's state ([71]a)", async () => {
    // Same-path re-entry: two flips overlap (the disabled selects cannot
    // rule out programmatic re-entry before React commits the re-render).
    // The OLDER flip's generation is superseded; its late rejection must
    // neither surface an error nor clobber the newer flip's cleared state.
    const deferreds: {
      resolve: (v: ResearchGraphResponse) => void
      reject: (e: Error) => void
    }[] = []
    vi.mocked(updateHypothesis).mockImplementation(
      () =>
        new Promise<ResearchGraphResponse>((resolve, reject) => {
          deferreds.push({ resolve, reject })
        }),
    )

    const container = await renderQuickMutate()
    // Both change events dispatch inside one act — React has not committed
    // the disabled re-render yet, so both flips start (gen 1, then gen 2).
    await act(async () => {
      changeSelect(selectEl(container, 'H-001'), 'in-progress')
      changeSelect(selectEl(container, 'H-002'), 'cancelled')
    })

    expect(updateHypothesis).toHaveBeenCalledTimes(2)

    // The NEWER flip (gen 2) completes successfully…
    await act(async () => {
      deferreds[1]!.resolve(makeGraphResponse(2))
    })
    await flush()
    // …then the OLDER flip (gen 1) fails after the fact.
    await act(async () => {
      deferreds[0]!.reject(new Error('stale failure'))
    })
    await flush()

    // The stale failure is dropped: no error banner, no stuck spinner state.
    expect(container.querySelector('[role="alert"]')).toBeNull()
    expect(selectEl(container, 'H-001').disabled).toBe(false)
    expect(selectEl(container, 'H-002').disabled).toBe(false)
  })
})
