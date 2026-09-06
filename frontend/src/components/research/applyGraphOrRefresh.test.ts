// applyGraphOrRefresh — the shared convergence path every research mutation
// site ([18]b) and the watcher's incremental fetch funnel through. Exercises
// the real stores with a mocked RPC layer: the incremental apply, the
// loadGraph-impossible fallback, and the mid-flight project-switch drop.
import { beforeEach, describe, expect, it, vi } from 'vitest'
import { applyGraphOrRefresh, fullResearchRefresh } from './applyGraphOrRefresh'
import { useResearchStore, selectActiveProject } from '@/stores/researchStore'
import { useProjectStore } from '@/stores/projectStore'
import type {
  ResearchStatus,
  ResearchGraphResponse,
  ResearchNextStep,
} from '@/types/models'

const statusMock = vi.fn<(projectId: string) => Promise<ResearchStatus>>()
const nextStepMock = vi.fn<(projectId: string) => Promise<ResearchNextStep>>()
vi.mock('@/api/research', () => ({
  getResearchStatus: (projectId: string) => statusMock(projectId),
  getResearchNextStep: (projectId: string) => nextStepMock(projectId),
}))

vi.mock('@/lib/logger', () => ({
  logger: { error: vi.fn(), warn: vi.fn(), info: vi.fn(), debug: vi.fn() },
}))

function makeStatus(nodeCount: number): ResearchStatus {
  return {
    enabled: true,
    project_id: 'p1',
    research_root: '/ws/.research',
    root: {
      path: '/ws/.research',
      index: [{ id: 'R-001', title: 'T' }],
      active_project_id: 'R-001',
      projects: [
        {
          id: 'R-001',
          brief: {
            id: 'R-001',
            title: 'T',
            status: 'in-progress',
            created: '2026-01-01',
            last_updated: '2026-01-02',
          },
          graph: {
            nodes: Array.from({ length: nodeCount }, (_, i) => ({
              id: `H-00${i + 1}`,
              title: `hypothesis ${i + 1}`,
              status: 'open',
            })),
            edges: [],
          },
          metrics: {
            total: nodeCount,
            by_status: {},
            confirmation_rate: 0,
            depth: 0,
            breadth: 0,
            active_front: [],
          },
          prior_art_count: 0,
          has_report: false,
          log: [],
        },
      ],
    },
  } as unknown as ResearchStatus
}

function makeGraphResponse(projectId: string, nodeCount: number): ResearchGraphResponse {
  const status = makeStatus(nodeCount)
  const p = status.root!.projects[0]!
  return {
    project_id: projectId,
    graph: p.graph,
    metrics: p.metrics,
    has_report: false,
    log: p.log,
  }
}

function seedStore(nodeCount: number): void {
  useProjectStore.setState({ activeProjectId: 'p1' })
  useResearchStore.getState().reset()
  useResearchStore.getState().loadStatus(makeStatus(nodeCount), 'p1')
}

describe('applyGraphOrRefresh ([18]b shared convergence path)', () => {
  beforeEach(() => {
    statusMock.mockReset()
    nextStepMock.mockReset()
  })

  it('applies the graph incrementally while the project is current', async () => {
    seedStore(1)
    const fresh = makeGraphResponse('R-001', 3)

    await applyGraphOrRefresh(fresh, 'p1', useResearchStore.getState().graphSyncSeq)

    expect(selectActiveProject(useResearchStore.getState())?.graph.nodes).toHaveLength(3)
    // No fallback refetch on the happy path.
    expect(statusMock).not.toHaveBeenCalled()
  })

  it('falls back to the full status refetch when loadGraph cannot apply', async () => {
    seedStore(1)
    // A response naming a research project the store has never seen (a
    // brand-new R-NNN created since the last full load) → loadGraph returns
    // false → the helper must converge via GetResearchStatus, not freeze.
    statusMock.mockResolvedValue(makeStatus(5))

    await applyGraphOrRefresh(makeGraphResponse('R-099', 2), 'p1', useResearchStore.getState().graphSyncSeq)

    expect(statusMock).toHaveBeenCalledWith('p1')
    expect(selectActiveProject(useResearchStore.getState())?.graph.nodes).toHaveLength(5)
  })

  it('drops the payload when the active project switched mid-flight ([18]a)', async () => {
    seedStore(1)
    useProjectStore.setState({ activeProjectId: 'p2' })

    await applyGraphOrRefresh(makeGraphResponse('R-001', 3), 'p1', useResearchStore.getState().graphSyncSeq)

    // Nothing applied, nothing fetched: the new project's own load owns it.
    expect(selectActiveProject(useResearchStore.getState())?.graph.nodes).toHaveLength(1)
    expect(statusMock).not.toHaveBeenCalled()
  })

  it('fullResearchRefresh loads the status and the next step while the project is current', async () => {
    seedStore(1)
    statusMock.mockResolvedValue(makeStatus(2))
    const next = {
      project_id: 'R-001',
      action: 'research-decision',
      reason: 'r',
      skill: 'research-decision',
    } as ResearchNextStep
    nextStepMock.mockResolvedValue(next)

    await fullResearchRefresh('p1')

    expect(selectActiveProject(useResearchStore.getState())?.graph.nodes).toHaveLength(2)
    expect(useResearchStore.getState().nextStep).toBe(next)
  })
})
