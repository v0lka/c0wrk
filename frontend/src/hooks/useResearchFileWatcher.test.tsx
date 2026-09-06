// @vitest-environment jsdom
// useResearchFileWatcher — the research panel's incremental auto-update path.
//
// Drives the hook with a captured 'research:file_changed' subscription
// (mirroring the backend payload {project_id, paths}) and asserts that
// researchStore.selectActiveLog reflects the refreshed graph (log entries,
// metrics) — the flow the Research panel relies on for auto-updates.

import { beforeEach, afterEach, describe, expect, it, vi } from 'vitest'
import { act } from 'react'
import { createRoot, type Root } from 'react-dom/client'
import { useResearchFileWatcher } from './useResearchFileWatcher'
import { useResearchStore, selectActiveLog, selectActiveProject } from '@/stores/researchStore'
import { useProjectStore } from '@/stores/projectStore'
import type { ResearchStatus, ResearchGraphResponse, ResearchNextStep } from '@/types/models'

// --- Mock @/api/runtime: capture subscribe handlers by event name ---

const handlers = new Map<string, (...data: unknown[]) => void>()
vi.mock('@/api/runtime', () => ({
  subscribe: (name: string, cb: (...data: unknown[]) => void) => {
    handlers.set(name, cb)
    return () => {
      handlers.delete(name)
    }
  },
}))

// --- Mock @/api/research ---

const statusMock = vi.fn<(projectId: string) => Promise<ResearchStatus>>()
const graphMock = vi.fn<(projectId: string) => Promise<ResearchGraphResponse>>()
const nextStepMock = vi.fn<(projectId: string) => Promise<ResearchNextStep>>()
vi.mock('@/api/research', () => ({
  getResearchStatus: (projectId: string) => statusMock(projectId),
  getResearchGraph: (projectId: string) => graphMock(projectId),
  getResearchNextStep: (projectId: string) => nextStepMock(projectId),
}))

vi.mock('@/lib/logger', () => ({
  logger: { error: vi.fn(), warn: vi.fn(), info: vi.fn(), debug: vi.fn() },
}))

function makeStatus(logCount: number): ResearchStatus {
  return {
    enabled: true,
    project_id: 'p1',
    research_root: '/ws/.research',
    root: {
      path: '/ws/.research',
      index: [{ id: 'R-002', title: 'T' }],
      projects: [
        {
          id: 'R-002',
          brief: {
            id: 'R-002',
            title: 'Flat project',
            status: 'in-progress',
            created: '2026-01-01',
            last_updated: '2026-01-02',
          },
          graph: { nodes: [], edges: [] },
          metrics: {
            total: 0,
            by_status: {},
            confirmation_rate: 0,
            depth: 0,
            breadth: 0,
            active_front: [],
          },
          prior_art_count: 0,
          has_report: false,
          log: Array.from({ length: logCount }, (_, i) => ({
            id: i + 1,
            kind: 'note',
            created_at: `2026-01-0${i + 1}T00:00:00Z`,
            message: `entry ${i + 1}`,
          })),
        },
      ],
      active_project_id: 'R-002',
    },
  } as unknown as ResearchStatus
}

function makeGraphResponse(logCount: number): ResearchGraphResponse {
  const status = makeStatus(logCount)
  const p = status.root!.projects[0]!
  return {
    project_id: p.id,
    graph: p.graph,
    metrics: p.metrics,
    has_report: false,
    log: p.log,
  }
}

function Probe() {
  useResearchFileWatcher()
  return null
}

async function mountProbe(): Promise<() => Promise<void>> {
  const container = document.createElement('div')
  document.body.appendChild(container)
  const root: Root = createRoot(container)
  await act(async () => {
    root.render(<Probe />)
  })
  return async () => {
    await act(async () => {
      root.unmount()
    })
    container.remove()
  }
}

describe('useResearchFileWatcher — event → store update', () => {
  beforeEach(() => {
    vi.useFakeTimers()
    handlers.clear()
    statusMock.mockReset().mockResolvedValue(makeStatus(1))
    graphMock.mockReset()
    nextStepMock.mockReset()
    useProjectStore.setState({ activeProjectId: 'p1' })
    useResearchStore.getState().reset()
    useResearchStore.getState().loadStatus(makeStatus(1), 'p1')
  })

  afterEach(() => {
    vi.useRealTimers()
  })

  it('updates the active log when research:file_changed fires for the active project', async () => {
    graphMock.mockResolvedValue(makeGraphResponse(2))
    nextStepMock.mockResolvedValue({
      project_id: 'R-002',
      action: 'research-hypothesis',
      reason: 'r',
      skill: 'research-hypothesis',
    } as ResearchNextStep)

    const unmount = await mountProbe()

    const emit = handlers.get('research:file_changed')
    expect(emit).toBeDefined()

    await act(async () => {
      emit!({ project_id: 'p1', paths: '/ws/.research/log.md' })
      await vi.advanceTimersByTimeAsync(150)
    })

    expect(graphMock).toHaveBeenCalledWith('p1')
    const log = selectActiveLog(useResearchStore.getState())
    expect(log).toHaveLength(2)

    await unmount()
  })

  it('updates metrics (the whole panel stays in sync), not just the log', async () => {
    const fresh = makeGraphResponse(2)
    fresh.metrics = {
      total: 5,
      by_status: { confirmed: 5 },
      confirmation_rate: 1,
      depth: 2,
      breadth: 3,
      active_front: [],
    }
    graphMock.mockResolvedValue(fresh)
    nextStepMock.mockResolvedValue({
      project_id: 'R-002',
      action: 'research-hypothesis',
      reason: 'r',
      skill: 'research-hypothesis',
    } as ResearchNextStep)

    const unmount = await mountProbe()

    await act(async () => {
      handlers.get('research:file_changed')!({ project_id: 'p1', paths: '/ws/.research/log.md' })
      await vi.advanceTimersByTimeAsync(150)
    })

    expect(selectActiveProject(useResearchStore.getState())?.metrics.total).toBe(5)

    await unmount()
  })

  it('still updates when the payload has an unexpected shape (falls through the guard)', async () => {
    graphMock.mockResolvedValue(makeGraphResponse(3))
    nextStepMock.mockResolvedValue({} as ResearchNextStep)

    const unmount = await mountProbe()

    await act(async () => {
      // Wails sometimes wraps single-arg payloads; the hook must not break.
      handlers.get('research:file_changed')!([{ project_id: 'p1', paths: 'x' }])
      await vi.advanceTimersByTimeAsync(150)
    })

    expect(selectActiveLog(useResearchStore.getState())).toHaveLength(3)

    await unmount()
  })

  it('the fullRefresh fallback also refreshes the recommended next step', async () => {
    // The incremental graph RPC fails → updateGraph falls back to fullRefresh
    // and skips its own next-step fetch (the catch path returns early). The
    // fallback must refresh the recommendation too: without it, a phase
    // change that flipped during those file edits leaves the dashboard
    // rendering a stale next step — within the same project, potentially
    // indefinitely.
    graphMock.mockRejectedValue(new Error('rpc down'))
    statusMock.mockResolvedValue(makeStatus(4))
    const freshNextStep = {
      project_id: 'R-002',
      action: 'research-decision',
      reason: 'phase moved to decision',
      skill: 'research-decision',
    } as ResearchNextStep
    nextStepMock.mockResolvedValue(freshNextStep)

    const unmount = await mountProbe()

    await act(async () => {
      handlers.get('research:file_changed')!({ project_id: 'p1', paths: '/ws/.research/log.md' })
      await vi.advanceTimersByTimeAsync(150)
    })

    // The fallback converged the status…
    expect(statusMock).toHaveBeenCalledWith('p1')
    expect(selectActiveLog(useResearchStore.getState())).toHaveLength(4)
    // …and refreshed the recommendation alongside it.
    expect(nextStepMock).toHaveBeenCalledWith('p1')
    expect(useResearchStore.getState().nextStep).toBe(freshNextStep)

    await unmount()
  })

  it('falls back to a full status refetch when the graph response names an unknown research project', async () => {
    // loadGraph returns false when the response names an R-NNN the store has
    // never seen (a brand-new project created since the last full load) —
    // the incremental path must converge the panel via GetResearchStatus
    // instead of freezing on stale data. The mock factory entry for
    // getResearchStatus exists precisely so this branch is really driven
    // (an undefined mock would throw inside fullRefresh and be swallowed by
    // its own catch, making the test pass vacuously).
    const unknown = makeGraphResponse(5)
    unknown.project_id = 'R-999' // the store only knows R-002
    graphMock.mockResolvedValue(unknown)
    nextStepMock.mockResolvedValue({} as ResearchNextStep)
    statusMock.mockResolvedValue(makeStatus(4))

    const unmount = await mountProbe()

    await act(async () => {
      handlers.get('research:file_changed')!({ project_id: 'p1', paths: '/ws/.research/log.md' })
      await vi.advanceTimersByTimeAsync(150)
    })

    // The incremental fetch ran, could not be applied…
    expect(graphMock).toHaveBeenCalledWith('p1')
    // …so the full status refetch converged the panel on the authoritative
    // payload (log length 4 from the status, not 5 from the rejected graph).
    expect(statusMock).toHaveBeenCalledWith('p1')
    expect(selectActiveLog(useResearchStore.getState())).toHaveLength(4)

    await unmount()
  })

  it('falls back quietly when the incremental graph RPC rejects, leaving no error in the store', async () => {
    // The catch path of updateGraph: an RPC failure must converge the panel
    // via the full status refetch WITHOUT surfacing a user-visible error —
    // the fallback is an implementation detail, not a failure state.
    graphMock.mockRejectedValue(new Error('rpc down'))
    nextStepMock.mockResolvedValue({} as ResearchNextStep)
    statusMock.mockResolvedValue(makeStatus(2))

    const unmount = await mountProbe()

    await act(async () => {
      handlers.get('research:file_changed')!({ project_id: 'p1', paths: '/ws/.research/log.md' })
      await vi.advanceTimersByTimeAsync(150)
    })

    expect(statusMock).toHaveBeenCalledWith('p1')
    expect(selectActiveLog(useResearchStore.getState())).toHaveLength(2)
    expect(useResearchStore.getState().error).toBeNull()

    await unmount()
  })
})
