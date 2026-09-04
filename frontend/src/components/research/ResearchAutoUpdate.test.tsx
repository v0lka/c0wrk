// @vitest-environment jsdom
// Full-panel integration: the ResearchPanel (left sidebar tab) must update
// its visible Research log + metrics when the backend emits
// research:file_changed for the active project — the auto-update flow.

import { beforeEach, afterEach, describe, expect, it, vi } from 'vitest'
import { act } from 'react'
import { createRoot, type Root } from 'react-dom/client'
import { ResearchPanel } from './index'
import { ResearchEventBridge } from './ResearchEventBridge'
import { useResearchStore } from '@/stores/researchStore'
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
            total: 1,
            by_status: { open: 1 },
            confirmation_rate: 0,
            depth: 1,
            breadth: 1,
            active_front: ['H-001'],
          },
          prior_art_count: 0,
          has_report: false,
          log: Array.from({ length: logCount }, (_, i) => ({
            id: String(i + 1),
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

const NEXT_STEP = {
  project_id: 'R-002',
  action: 'research-hypothesis',
  reason: 'r',
  skill: 'research-hypothesis',
} as ResearchNextStep

describe('ResearchPanel — auto-update on research:file_changed', () => {
  let container: HTMLDivElement
  let root: Root

  beforeEach(() => {
    vi.useFakeTimers()
    handlers.clear()
    statusMock.mockReset()
    graphMock.mockReset()
    nextStepMock.mockReset()
    statusMock.mockResolvedValue(makeStatus(2))
    nextStepMock.mockResolvedValue(NEXT_STEP)
    useProjectStore.setState({ activeProjectId: 'p1' })
    useResearchStore.getState().reset()

    container = document.createElement('div')
    document.body.appendChild(container)
    root = createRoot(container)
  })

  afterEach(async () => {
    await act(async () => {
      root.unmount()
    })
    container.remove()
    vi.useRealTimers()
  })

  it('appends new log entries to the visible panel without remount', async () => {
    // Initial mount: full status fetch (2 entries visible). The bridge is
    // mounted alongside the panel exactly as App.tsx mounts it (gated on
    // the experimental feature) — it owns the data sync.
    await act(async () => {
      root.render(
        <>
          <ResearchEventBridge />
          <ResearchPanel />
        </>,
      )
    })
    await act(async () => {
      await vi.advanceTimersByTimeAsync(100)
    })
    expect(container.querySelectorAll('[data-testid="research-log-entry"]').length).toBe(2)

    // The agent appends one more entry; the backend emits research:file_changed.
    graphMock.mockResolvedValue(makeGraphResponse(3))
    await act(async () => {
      handlers.get('research:file_changed')!({ project_id: 'p1', paths: '/ws/.research/log.md' })
      await vi.advanceTimersByTimeAsync(400)
    })

    expect(graphMock).toHaveBeenCalledWith('p1')
    expect(container.querySelectorAll('[data-testid="research-log-entry"]').length).toBe(3)
  })

  it('keeps the panel fresh when the active R-NNN changed since the last full load', async () => {
    // Store holds R-002; the agent started R-003 (research-init) — the fresh
    // graph response now reports R-003 as the active research project.
    await act(async () => {
      root.render(
        <>
          <ResearchEventBridge />
          <ResearchPanel />
        </>,
      )
    })
    await act(async () => {
      await vi.advanceTimersByTimeAsync(100)
    })
    expect(container.querySelectorAll('[data-testid="research-log-entry"]').length).toBe(2)

    const fresh = makeGraphResponse(5)
    fresh.project_id = 'R-003'
    graphMock.mockResolvedValue(fresh)
    const freshStatus = makeStatus(5)
    freshStatus.root!.projects[0]!.id = 'R-003'
    freshStatus.root!.active_project_id = 'R-003'
    freshStatus.root!.index = [{ id: 'R-003', title: 'T' }]
    statusMock.mockResolvedValue(freshStatus)

    await act(async () => {
      handlers.get('research:file_changed')!({ project_id: 'p1', paths: '/ws/.research/R-003/log.md' })
      await vi.advanceTimersByTimeAsync(400)
    })

    // The panel must converge on the new active project's data — either via
    // the incremental path or a full refetch fallback. Stale = bug.
    expect(container.querySelectorAll('[data-testid="research-log-entry"]').length).toBe(5)
  })
})
