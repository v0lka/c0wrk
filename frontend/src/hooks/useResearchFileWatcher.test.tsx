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

const graphMock = vi.fn<(projectId: string) => Promise<ResearchGraphResponse>>()
const nextStepMock = vi.fn<(projectId: string) => Promise<ResearchNextStep>>()
vi.mock('@/api/research', () => ({
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
})
