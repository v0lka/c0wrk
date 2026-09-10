// @vitest-environment jsdom
// ResearchQuickActions — the research-* skill dispatch row. Covers [22]a:
// send() rethrows when the auto-created session fails (the splash race);
// the rejection must land on the research store's error banner (rendered on
// the research panel), NOT a global toast. Also covers the derived
// enablement: every button is gated on the dashboard's state so no
// workflow-invalid gesture is offered.
import { afterEach, beforeEach, describe, expect, it, vi } from 'vitest'
import { act } from 'react'
import { createRoot, type Root } from 'react-dom/client'

import { ResearchQuickActions } from './ResearchQuickActions'
import { useResearchStore } from '@/stores/researchStore'
import type { ResearchStatus } from '@/types/models'

const sendMock = vi.fn<(text: string, skills?: string[]) => Promise<void>>()
vi.mock('@/hooks/useMessageSender', () => ({
  useMessageSender: () => ({
    send: sendMock,
    cancel: vi.fn(async () => {}),
    isProcessing: false,
  }),
}))

let activeRoot: Root | null = null

async function renderActions(): Promise<HTMLElement> {
  const container = document.createElement('div')
  document.body.replaceChildren(container)
  const root = createRoot(container)
  activeRoot = root
  await act(async () => {
    root.render(<ResearchQuickActions />)
  })
  return container
}

/** R-001 with one in-progress current card (the reconciled front leader) —
 *  the minimal state in which every quick action is legal. */
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
              { id: 'H-001', title: 'Running hypothesis', status: 'in-progress' },
              { id: 'H-002', title: 'Proven hypothesis', status: 'confirmed' },
            ],
            edges: [],
          },
          metrics: {
            total: 2,
            by_status: { 'in-progress': 1, confirmed: 1 },
            confirmation_rate: 1,
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

function actionButton(container: HTMLElement, skill: string): HTMLButtonElement {
  return Array.from(
    container.querySelectorAll<HTMLButtonElement>('[data-testid="research-quick-action"]'),
  ).find((b) => b.getAttribute('data-skill') === skill)!
}

beforeEach(() => {
  sendMock.mockReset()
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

describe('ResearchQuickActions — dispatch failure surfacing ([22]a)', () => {
  it('routes a createSession failure into the research store error', async () => {
    sendMock.mockRejectedValue(new Error('runtime not ready'))

    const container = await renderActions()
    const experiment = actionButton(container, 'research-experiment')
    expect(experiment.disabled).toBe(false)

    await act(async () => {
      experiment.click()
      await new Promise((r) => setTimeout(r, 0))
    })

    const error = useResearchStore.getState().error
    expect(error).toContain('Failed to dispatch')
    expect(error).toContain('runtime not ready')
  })

  it('leaves the store clean when the dispatch succeeds', async () => {
    sendMock.mockResolvedValue(undefined)

    const container = await renderActions()
    const experiment = actionButton(container, 'research-experiment')

    await act(async () => {
      experiment.click()
      await new Promise((r) => setTimeout(r, 0))
    })

    expect(useResearchStore.getState().error).toBeNull()
  })
})

describe('ResearchQuickActions — derived enablement', () => {
  it('enables every action when the current card is in-progress and a terminal card exists', async () => {
    const container = await renderActions()
    for (const skill of [
      'research-experiment',
      'research-hypothesis',
      'research-decision',
      'research-synthesis',
    ]) {
      expect(actionButton(container, skill).disabled).toBe(false)
    }
  })

  it('disables every action when there is no research project at all', async () => {
    useResearchStore.getState().reset()

    const container = await renderActions()
    const buttons = container.querySelectorAll<HTMLButtonElement>(
      '[data-testid="research-quick-action"]',
    )
    expect(buttons.length).toBe(4)
    for (const b of Array.from(buttons)) {
      expect(b.disabled).toBe(true)
      // The disabled tooltip explains WHY (no workflow-dead buttons).
      expect((b.getAttribute('title') ?? '').length).toBeGreaterThan(0)
    }
  })

  it('gates Record result on an in-progress current card (an open card has nothing recorded)', async () => {
    const status = makeStatus()
    status.root!.projects[0]!.graph.nodes[0]!.status = 'open'
    status.root!.projects[0]!.metrics.by_status = { open: 1, confirmed: 1 }
    useResearchStore.getState().reset()
    useResearchStore.getState().loadStatus(status, 'p1')

    const container = await renderActions()
    expect(actionButton(container, 'research-experiment').disabled).toBe(false)
    expect(actionButton(container, 'research-hypothesis').disabled).toBe(true)
  })

  it('gates Decision on at least one terminal hypothesis', async () => {
    const status = makeStatus()
    status.root!.projects[0]!.graph.nodes = [
      { id: 'H-001', title: 'Running hypothesis', status: 'in-progress' },
    ]
    status.root!.projects[0]!.metrics = {
      total: 1,
      by_status: { 'in-progress': 1 },
      confirmation_rate: 0,
      depth: 1,
      breadth: 1,
      active_front: ['H-001'],
    }
    useResearchStore.getState().reset()
    useResearchStore.getState().loadStatus(status, 'p1')

    const container = await renderActions()
    expect(actionButton(container, 'research-decision').disabled).toBe(true)
    expect(actionButton(container, 'research-synthesis').disabled).toBe(false)
  })
})
