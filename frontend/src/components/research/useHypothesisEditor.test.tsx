// @vitest-environment jsdom
// useHypothesisEditor — the workspace card's save round-trip. Covers the
// step-13 hardening: the pre-RPC freshness re-check ([19]a), the
// expected-R-NNN argument and shared applyGraphOrRefresh routing ([18]b),
// and the action-generation guard against overlapping saves ([71]a).
import { afterEach, beforeEach, describe, expect, it, vi } from 'vitest'
import { act } from 'react'
import { createRoot, type Root } from 'react-dom/client'

import { useHypothesisEditor } from './useHypothesisEditor'
import { applyGraphOrRefresh } from './applyGraphOrRefresh'
import { updateHypothesis } from '@/api/research'
import {
  useResearchStore,
  selectActiveProject,
} from '@/stores/researchStore'
import { useProjectStore } from '@/stores/projectStore'
import type {
  HypothesisGraph,
  HypothesisNode,
  HypothesisDraft,
  ResearchStatus,
  ResearchGraphResponse,
} from '@/types/models'

vi.mock('@/api/research', () => ({
  updateHypothesis: vi.fn(),
  getResearchStatus: vi.fn(),
  getResearchNextStep: vi.fn(),
}))

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

const GRAPH: HypothesisGraph = {
  nodes: [{ id: 'H-001', title: 'Root hypothesis', status: 'open' }],
  edges: [],
}

const NODE: HypothesisNode = { id: 'H-001', title: 'Root hypothesis', status: 'open' }

/** A draft that differs from the node in `result` (so a save is dirty). */
const DIRTY_DRAFT: HypothesisDraft = { status: 'open', result: 'finding', timebox: '' }

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
          graph: GRAPH,
          metrics: {
            total: 1,
            by_status: { open: 1 },
            confirmation_rate: 0,
            depth: 1,
            breadth: 1,
            active_front: ['H-001'],
          },
          prior_art_count: 0,
          has_report: false,
          log: [],
        },
      ],
    },
  } as unknown as ResearchStatus
}

function makeGraphResponse(): ResearchGraphResponse {
  return {
    project_id: 'R-001',
    graph: {
      nodes: [
        { id: 'H-001', title: 'Root hypothesis', status: 'open', result: 'finding' },
      ],
      edges: [],
    },
    metrics: {
      total: 1,
      by_status: { open: 1 },
      confirmation_rate: 0,
      depth: 1,
      breadth: 1,
      active_front: ['H-001'],
    },
    has_report: false,
    log: [],
  }
}

/** The same-workspace-project active-R-NNN transition research-init produces. */
function switchActiveProject(): void {
  const switched = makeStatus()
  const r1 = switched.root!.projects[0]!
  switched.root!.projects.push({
    ...r1,
    id: 'R-002',
    brief: { id: 'R-002', title: 'Follow-up research' },
  })
  switched.root!.active_project_id = 'R-002'
  useResearchStore.getState().loadStatus(switched, 'p1')
}

// Drive the hook through a probe component; the latest editor handle is
// exposed so tests can call handleSave directly (the stale-selection race
// the [19]a guard covers is exactly the "handler from an older render"
// case, which a detached DOM button cannot reproduce).
let latestEditor: ReturnType<typeof useHypothesisEditor> | null = null

function Probe(): null {
  latestEditor = useHypothesisEditor(GRAPH, NODE, DIRTY_DRAFT)
  return null
}

let activeRoot: Root | null = null

async function mountProbe(): Promise<void> {
  const container = document.createElement('div')
  document.body.replaceChildren(container)
  const root = createRoot(container)
  activeRoot = root
  await act(async () => {
    root.render(<Probe />)
  })
}

const flush = () => act(async () => { await new Promise((r) => setTimeout(r, 0)) })

beforeEach(() => {
  vi.clearAllMocks()
  useProjectStore.setState({ activeProjectId: 'p1' })
  useResearchStore.getState().reset()
  useResearchStore.getState().loadStatus(makeStatus(), 'p1')
  // A selection made against the CURRENT active project (R-001).
  useResearchStore.getState().selectHypothesis('H-001', { ...DIRTY_DRAFT })
  latestEditor = null
})

afterEach(() => {
  if (activeRoot) {
    act(() => {
      activeRoot!.unmount()
    })
    activeRoot = null
  }
})

describe('useHypothesisEditor — step-13 hardening', () => {
  it('saves with the expected R-NNN and applies through the shared helper ([18]b/[19])', async () => {
    const res = makeGraphResponse()
    vi.mocked(updateHypothesis).mockResolvedValue(res)

    await mountProbe()
    await act(async () => {
      await latestEditor!.handleSave()
    })

    // The research project the selection was resolved against rides along
    // as the expected R ([19]b companion)…
    expect(updateHypothesis).toHaveBeenCalledWith('p1', 'R-001', 'H-001', {
      result: 'finding',
    })
    // …and the response funnels through the shared convergence path.
    expect(applyGraphOrRefresh).toHaveBeenCalledWith(res, 'p1', expect.any(Number))
    expect(
      selectActiveProject(useResearchStore.getState())?.graph.nodes[0]?.result,
    ).toBe('finding')
  })

  it('aborts the save when the selection no longer names the active research project ([19]a)', async () => {
    await mountProbe()
    // The backend switched active R-NNN after the card was resolved: the
    // selection stays keyed to R-001 while R-002 is active. A same-id H-001
    // in R-002 would otherwise be overwritten by the save.
    await act(async () => {
      switchActiveProject()
    })

    await act(async () => {
      await latestEditor!.handleSave()
    })

    expect(updateHypothesis).not.toHaveBeenCalled()
    expect(latestEditor!.saveError).toContain('active research project changed')
    expect(latestEditor!.saving).toBe(false)
  })

  it('aborts the save when the research snapshot belongs to another workspace project (cross-project guard)', async () => {
    await mountProbe()
    // The user switched workspace projects; the research store still holds
    // the OLD project's graph (nothing resets it until the new project's
    // status fetch lands). Every workspace has an R-001, so an
    // unconditional save would overwrite the NEW project's same-id card.
    useProjectStore.setState({ activeProjectId: 'p2' })

    await act(async () => {
      await latestEditor!.handleSave()
    })

    expect(updateHypothesis).not.toHaveBeenCalled()
    expect(applyGraphOrRefresh).not.toHaveBeenCalled()
    expect(latestEditor!.saveError).toContain('different project')
    expect(latestEditor!.saving).toBe(false)
  })

  it("a superseded save's late failure does not clobber the newer save's state ([71]a)", async () => {
    // Overlapping saves (re-entry the disabled Save button cannot fully
    // rule out): the older save's rejection arrives after the newer save
    // completed — it must not surface an error over the newer state.
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

    await mountProbe()
    // Both saves start before either resolves (gen 1 superseded by gen 2).
    await act(async () => {
      void latestEditor!.handleSave()
      void latestEditor!.handleSave()
    })
    expect(updateHypothesis).toHaveBeenCalledTimes(2)

    // The NEWER save (gen 2) completes…
    await act(async () => {
      deferreds[1]!.resolve(makeGraphResponse())
    })
    await flush()
    // …then the OLDER save (gen 1) fails after the fact.
    await act(async () => {
      deferreds[0]!.reject(new Error('stale failure'))
    })
    await flush()

    expect(latestEditor!.saveError).toBeNull()
    expect(latestEditor!.saving).toBe(false)
  })
})
