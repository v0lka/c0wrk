// @vitest-environment jsdom
// ResearchPanel — the [Dashboard | Papers] segmented control.
//
// ResearchPanel is a pure view over researchStore / paperStore / uiStore, so
// the segment tests seed the stores directly. The chosen segment is remembered
// PER PROJECT in uiStore, and the Papers library stays reachable even when
// RESEARCH is disabled (the library lives independently of the toggle).
import { afterEach, beforeEach, describe, expect, it, vi } from 'vitest'
import { act } from 'react'
import { createRoot, type Root } from 'react-dom/client'

import { ResearchPanel } from './index'
import { useResearchStore } from '@/stores/researchStore'
import { useProjectStore } from '@/stores/projectStore'
import { useUIStore } from '@/stores/uiStore'
import type { ResearchStatus } from '@/types/models'

vi.mock('@/hooks/useMessageSender', () => ({
  useMessageSender: () => ({ send: vi.fn(), cancel: vi.fn(), isProcessing: false }),
}))

vi.mock('@/api/research', () => ({
  getResearchStatus: vi.fn(),
  getResearchGraph: vi.fn(),
  getResearchNextStep: vi.fn(),
  updateHypothesis: vi.fn(),
  createHypothesis: vi.fn(),
  setActiveResearch: vi.fn(),
  deleteResearch: vi.fn(),
  setResearchPinned: vi.fn(),
  setHypothesisPinned: vi.fn(),
  enableResearch: vi.fn(),
  disableResearch: vi.fn(),
}))

vi.stubGlobal(
  'ResizeObserver',
  class {
    observe(): void {}
    unobserve(): void {}
    disconnect(): void {}
  },
)

let activeRoot: Root | null = null

async function render(el: React.ReactNode): Promise<HTMLElement> {
  const container = document.createElement('div')
  document.body.replaceChildren(container)
  const root = createRoot(container)
  activeRoot = root
  await act(async () => {
    root.render(el)
  })
  return container
}

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
            nodes: [{ id: 'H-001', title: 'Running hypothesis', status: 'in-progress' }],
            edges: [],
          },
          metrics: {
            total: 1,
            by_status: { 'in-progress': 1 },
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
  }
}

function segmentButton(container: HTMLElement, value: string): HTMLButtonElement {
  const el = container.querySelector<HTMLButtonElement>(`[data-testid="research-segment-${value}"]`)
  expect(el).not.toBeNull()
  return el!
}

beforeEach(() => {
  localStorage.clear()
  useResearchStore.getState().reset()
  useUIStore.setState({ researchSegmentByProject: {} })
  useProjectStore.setState({ projects: null, activeProjectId: 'p1', lastRealProjectId: 'p1' })
})

afterEach(() => {
  if (activeRoot) {
    act(() => {
      activeRoot!.unmount()
    })
    activeRoot = null
  }
})

describe('ResearchPanel — segmented control', () => {
  it('defaults to the Dashboard segment', async () => {
    useResearchStore.getState().loadStatus(makeStatus(), 'p1')
    const container = await render(<ResearchPanel />)

    expect(segmentButton(container, 'dashboard').getAttribute('aria-selected')).toBe('true')
    expect(segmentButton(container, 'papers').getAttribute('aria-selected')).toBe('false')
    expect(container.querySelector('[data-testid="papers-view"]')).toBeNull()
    expect(container.querySelector('[data-testid="research-quick-actions"]')).not.toBeNull()
  })

  it('switches to Papers and remembers the segment per project', async () => {
    useResearchStore.getState().loadStatus(makeStatus(), 'p1')
    const container = await render(<ResearchPanel />)

    await act(async () => {
      segmentButton(container, 'papers').click()
    })

    expect(container.querySelector('[data-testid="papers-view"]')).not.toBeNull()
    expect(container.querySelector('[data-testid="research-quick-actions"]')).toBeNull()
    expect(segmentButton(container, 'papers').getAttribute('aria-selected')).toBe('true')
    expect(useUIStore.getState().researchSegmentByProject.p1).toBe('papers')
  })

  it('restores each project’s remembered segment on a project switch', async () => {
    useUIStore.setState({ researchSegmentByProject: { p1: 'papers' } })
    const container = await render(<ResearchPanel />)
    expect(container.querySelector('[data-testid="papers-view"]')).not.toBeNull()

    // Project 2 has no remembered segment → Dashboard.
    await act(async () => {
      useProjectStore.setState({ activeProjectId: 'p2' })
    })
    expect(container.querySelector('[data-testid="papers-view"]')).toBeNull()

    // Back to project 1 → its remembered Papers segment.
    await act(async () => {
      useProjectStore.setState({ activeProjectId: 'p1' })
    })
    expect(container.querySelector('[data-testid="papers-view"]')).not.toBeNull()
  })

  it('keeps the Papers library reachable while RESEARCH is disabled', async () => {
    // No status → the panel's disabled branch (the toggle card).
    const container = await render(<ResearchPanel />)
    expect(container.querySelector('[data-testid="papers-view"]')).toBeNull()

    await act(async () => {
      segmentButton(container, 'papers').click()
    })
    expect(container.querySelector('[data-testid="papers-view"]')).not.toBeNull()
  })

  it('falls back to the Dashboard when a corrupt persisted segment is dropped on rehydrate', async () => {
    localStorage.setItem(
      'c0wrk-sidebar-collapsed',
      JSON.stringify({
        state: { researchSegmentByProject: { p1: 'nonsense' } },
        version: 6,
      }),
    )
    await act(async () => {
      await useUIStore.persist.rehydrate()
    })

    const container = await render(<ResearchPanel />)
    expect(segmentButton(container, 'dashboard').getAttribute('aria-selected')).toBe('true')
    expect(container.querySelector('[data-testid="papers-view"]')).toBeNull()
  })
})
