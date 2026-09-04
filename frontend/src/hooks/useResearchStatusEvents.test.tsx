// @vitest-environment jsdom
// useResearchStatusEvents — full-refresh subscriptions + the research-scoped
// watchdog. The watchdog is the convergence safety net for the Research
// panel's auto-update: when workspace:tree_changed is research-scoped, the
// immediate refetch is skipped (the incremental research:file_changed path
// owns it), but a delayed check runs a full refresh UNLESS a successful
// incremental sync (researchStore.lastGraphSyncAt) landed after scheduling.

import { beforeEach, afterEach, describe, expect, it, vi } from 'vitest'
import { act } from 'react'
import { createRoot, type Root } from 'react-dom/client'
import { useResearchStatusEvents } from './useResearchStatusEvents'
import { useResearchStore } from '@/stores/researchStore'
import { useProjectStore } from '@/stores/projectStore'
import type { ResearchStatus, ResearchNextStep } from '@/types/models'

const handlers = new Map<string, (...data: unknown[]) => void>()
vi.mock('@/api/runtime', () => ({
  subscribe: (name: string, cb: (...data: unknown[]) => void) => {
    handlers.set(name, cb)
    return () => {
      handlers.delete(name)
    }
  },
}))

const statusMock = vi.fn<(projectId: string) => Promise<ResearchStatus>>()
const nextStepMock = vi.fn<(projectId: string) => Promise<ResearchNextStep>>()
vi.mock('@/api/research', () => ({
  getResearchStatus: (projectId: string) => statusMock(projectId),
  getResearchNextStep: (projectId: string) => nextStepMock(projectId),
}))

vi.mock('@/lib/logger', () => ({
  logger: { error: vi.fn(), warn: vi.fn(), info: vi.fn(), debug: vi.fn() },
}))

function makeStatus(): ResearchStatus {
  return {
    enabled: true,
    project_id: 'p1',
    research_root: '/ws/.research',
    root: {
      path: '/ws/.research',
      index: [],
      projects: [
        {
          id: 'R-001',
          brief: { id: 'R-001', title: 'T' },
          graph: { nodes: [], edges: [] },
          metrics: {
            total: 0,
            by_status: {},
            confirmation_rate: 0,
            depth: 0,
            breadth: 0,
          },
          prior_art_count: 0,
          has_report: false,
          log: [],
        },
      ],
      active_project_id: 'R-001',
    },
  } as unknown as ResearchStatus
}

const NEXT_STEP = {
  project_id: 'R-001',
  action: 'research-hypothesis',
  reason: 'r',
  skill: 'research-hypothesis',
} as ResearchNextStep

function Probe() {
  useResearchStatusEvents()
  return null
}

describe('useResearchStatusEvents — refresh + research watchdog', () => {
  let container: HTMLDivElement
  let root: Root

  beforeEach(() => {
    vi.useFakeTimers()
    handlers.clear()
    statusMock.mockReset().mockResolvedValue(makeStatus())
    nextStepMock.mockReset().mockResolvedValue(NEXT_STEP)
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

  it('refetches on research:changed (toggle) after the 50ms debounce', async () => {
    await act(async () => {
      root.render(<Probe />)
    })
    expect(statusMock).toHaveBeenCalledTimes(1)

    await act(async () => {
      handlers.get('research:changed')!()
      await vi.advanceTimersByTimeAsync(100)
    })
    expect(statusMock).toHaveBeenCalledTimes(2)
  })

  it('research-scoped tree change: no immediate refetch, but the watchdog refetches when the incremental path never lands', async () => {
    await act(async () => {
      root.render(<Probe />)
    })
    expect(statusMock).toHaveBeenCalledTimes(1)

    // The incremental research:file_changed event is LOST (never delivered).
    await act(async () => {
      // Advance past the initial load's sync stamp so the watchdog's
      // scheduledAt is strictly newer (in production the initial load lands
      // long before any file change).
      await vi.advanceTimersByTimeAsync(50)
      handlers.get('workspace:tree_changed')!({ research_scoped: true })
      await vi.advanceTimersByTimeAsync(300)
    })
    // Not the immediate path, not yet the watchdog.
    expect(statusMock).toHaveBeenCalledTimes(1)

    await act(async () => {
      await vi.advanceTimersByTimeAsync(1500)
    })
    // Watchdog fired: the panel converges via the full refetch.
    expect(statusMock).toHaveBeenCalledTimes(2)
  })

  it('research-scoped tree change: the watchdog stays quiet when a successful incremental sync landed', async () => {
    await act(async () => {
      root.render(<Probe />)
    })
    await act(async () => {
      await vi.advanceTimersByTimeAsync(50)
      handlers.get('workspace:tree_changed')!({ research_scoped: true })
      await vi.advanceTimersByTimeAsync(100)
    })

    // The incremental path applies its update (stamps lastGraphSyncAt).
    act(() => {
      useResearchStore.getState().loadStatus(makeStatus(), 'p1')
    })

    await act(async () => {
      await vi.advanceTimersByTimeAsync(2000)
    })
    // Only the initial fetch — no redundant full refetch.
    expect(statusMock).toHaveBeenCalledTimes(1)
  })

  it('non-research tree change refetches through the normal debounce', async () => {
    await act(async () => {
      root.render(<Probe />)
    })
    await act(async () => {
      handlers.get('workspace:tree_changed')!({ research_scoped: false })
      await vi.advanceTimersByTimeAsync(100)
    })
    expect(statusMock).toHaveBeenCalledTimes(2)
  })
})
