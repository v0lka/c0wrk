// @vitest-environment jsdom
import { describe, it, expect, vi, beforeEach, afterEach } from 'vitest'
import { act } from 'react'
import { createRoot, type Root } from 'react-dom/client'

import { ResearchNextStep } from './ResearchNextStep'
import { ResearchQuickActions } from './ResearchQuickActions'
import { ResearchLog } from './ResearchLog'
import { latestLogEntries, formatLogTime, DEFAULT_LOG_LIMIT } from './researchLogUtils'
import { ResearchQuickMutate } from './ResearchQuickMutate'
import { useResearchStore } from '@/stores/researchStore'
import { useProjectStore } from '@/stores/projectStore'
import { updateHypothesis } from '@/api/research'
import { buildNextStepPrompt, QUICK_ACTIONS } from './researchActions'
import type {
  ResearchNextStep as ResearchNextStepDTO,
  ResearchStatus,
  ResearchLogEntry,
  ResearchGraphResponse,
} from '@/types/models'

const { sendSpy } = vi.hoisted(() => ({ sendSpy: vi.fn() }))

vi.mock('@/hooks/useMessageSender', () => ({
  useMessageSender: () => ({ send: sendSpy, cancel: vi.fn(), isProcessing: false }),
}))

vi.mock('@/api/research', () => ({
  getResearchStatus: vi.fn(),
  getResearchGraph: vi.fn(),
  getResearchNextStep: vi.fn(),
  updateHypothesis: vi.fn(),
  createHypothesis: vi.fn(),
}))

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

function makeNextStep(overrides: Partial<ResearchNextStepDTO> = {}): ResearchNextStepDTO {
  return {
    project_id: 'p1',
    action: 'research-experiment',
    target: 'H-001',
    reason: 'an active front exists — run an experiment',
    skill: 'research-experiment',
    ...overrides,
  }
}

function makeLog(entries: ResearchLogEntry[]): ResearchLogEntry[] {
  return entries
}

function makeStatus(log: ResearchLogEntry[] = []): ResearchStatus {
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
              { id: 'H-001', title: 'Leading hypothesis', status: 'open' },
              { id: 'H-002', title: 'Done hypothesis', status: 'confirmed', parents: ['H-001'] },
            ],
            edges: [{ from: 'H-001', to: 'H-002' }],
          },
          metrics: {
            total: 2,
            by_status: { open: 1, confirmed: 1 },
            confirmation_rate: 0.5,
            depth: 2,
            breadth: 1,
            active_front: ['H-001'],
          },
          prior_art_count: 0,
          has_report: false,
          log,
        },
      ],
    },
  }
}

function makeGraphResponse(): ResearchGraphResponse {
  return {
    project_id: 'p1',
    graph: {
      nodes: [
        { id: 'H-001', title: 'Leading hypothesis', status: 'confirmed' },
        { id: 'H-002', title: 'Done hypothesis', status: 'confirmed', parents: ['H-001'] },
      ],
      edges: [{ from: 'H-001', to: 'H-002' }],
    },
    metrics: {
      total: 2,
      by_status: { confirmed: 2 },
      confirmation_rate: 1,
      depth: 2,
      breadth: 1,
    },
    has_report: false,
    log: [],
  }
}

beforeEach(() => {
  vi.clearAllMocks()
  vi.mocked(updateHypothesis).mockResolvedValue(makeGraphResponse())
  useProjectStore.setState({ projects: null, activeProjectId: 'p1', lastRealProjectId: 'p1' })
  useResearchStore.getState().reset()
})

afterEach(() => {
  if (activeRoot) {
    act(() => {
      activeRoot!.unmount()
    })
    activeRoot = null
  }
})

describe('ResearchNextStep — dispatch', () => {
  it('renders the recommendation and Execute dispatches the correct skill message', async () => {
    const nextStep = makeNextStep()
    useResearchStore.getState().loadNextStep(nextStep)

    const container = await render(<ResearchNextStep />)
    expect(container.textContent).toContain(nextStep.reason)
    expect(container.textContent).toContain('research-experiment')
    expect(container.textContent).toContain('H-001')

    const execute = Array.from(container.querySelectorAll('button')).find((b) =>
      b.textContent?.includes('Execute'),
    )!
    await act(async () => {
      execute.click()
    })

    expect(sendSpy).toHaveBeenCalledTimes(1)
    expect(sendSpy).toHaveBeenCalledWith(buildNextStepPrompt(nextStep), ['research-experiment'])
  })

  it('shows a muted empty card when no recommendation is loaded', async () => {
    const container = await render(<ResearchNextStep />)
    expect(container.textContent).toContain('No next step available yet')
    expect(sendSpy).not.toHaveBeenCalled()
  })

  it('dispatches the init skill when the recommendation is research-init', async () => {
    const nextStep = makeNextStep({
      action: 'research-init',
      target: undefined,
      reason: 'no research project exists yet',
      skill: 'research-init',
    })
    useResearchStore.getState().loadNextStep(nextStep)

    const container = await render(<ResearchNextStep />)
    const execute = Array.from(container.querySelectorAll('button')).find((b) =>
      b.textContent?.includes('Execute'),
    )!
    await act(async () => {
      execute.click()
    })

    expect(sendSpy).toHaveBeenCalledWith(buildNextStepPrompt(nextStep), ['research-init'])
  })
})

describe('ResearchQuickActions — dispatch', () => {
  it('renders one button per action, each carrying its skill', async () => {
    const container = await render(<ResearchQuickActions />)
    const buttons = container.querySelectorAll('[data-testid="research-quick-action"]')
    expect(buttons.length).toBe(QUICK_ACTIONS.length)
    QUICK_ACTIONS.forEach((action, i) => {
      expect(buttons[i]!.getAttribute('data-skill')).toBe(action.skill)
    })
  })

  it('dispatches the matching skill for a clicked action', async () => {
    const container = await render(<ResearchQuickActions />)
    const synthesize = Array.from(
      container.querySelectorAll<HTMLButtonElement>('[data-testid="research-quick-action"]'),
    ).find((b) => b.getAttribute('data-skill') === 'research-synthesis')!

    await act(async () => {
      synthesize.click()
    })

    expect(sendSpy).toHaveBeenCalledTimes(1)
    const [prompt, skills] = sendSpy.mock.calls[0]!
    expect(skills).toEqual(['research-synthesis'])
    expect(typeof prompt).toBe('string')
    expect(prompt.length).toBeGreaterThan(0)
  })
})

describe('ResearchLog — rendering + helpers', () => {
  it('renders the latest entries, newest first', async () => {
    const entries = makeLog([
      { id: '1', kind: 'note', message: 'seeded project', created_at: '2026-01-01T10:00:00Z' },
      { id: '2', kind: 'experiment', hypothesis_id: 'H-001', message: 'ran experiment', created_at: '2026-01-02T10:00:00Z' },
      { id: '3', kind: 'decision', message: 'decided to pivot', created_at: '2026-01-03T10:00:00Z' },
    ])
    useResearchStore.getState().loadStatus(makeStatus(entries), 'p1')

    const container = await render(<ResearchLog />)
    const rendered = container.querySelectorAll('[data-testid="research-log-entry"]')
    expect(rendered.length).toBe(3)
    // Newest first: "decided to pivot" (id 3) is the top row.
    expect(rendered[0]!.textContent).toContain('decided to pivot')
    expect(rendered[0]!.textContent).toContain('2026-01-03 10:00:00')
    // The hypothesis id is surfaced.
    expect(rendered[1]!.textContent).toContain('H-001')
  })

  it('renders an empty state when there are no entries', async () => {
    useResearchStore.getState().loadStatus(makeStatus([]), 'p1')
    const container = await render(<ResearchLog />)
    expect(container.textContent).toContain('No entries yet')
  })

  it('latestLogEntries caps to the limit and reverses to newest-first', () => {
    const entries = Array.from({ length: 15 }, (_, i) => ({
      id: String(i + 1),
      kind: 'note' as const,
      message: `entry ${i + 1}`,
      created_at: `2026-01-${String(i + 1).padStart(2, '0')}T10:00:00Z`,
    }))
    const latest = latestLogEntries(entries)
    expect(latest.length).toBe(DEFAULT_LOG_LIMIT)
    expect(latest[0]!.id).toBe('15')
    expect(latest[latest.length - 1]!.id).toBe('6')
  })

  it('formatLogTime trims the ISO string deterministically', () => {
    expect(formatLogTime('2026-01-03T10:05:07.123Z')).toBe('2026-01-03 10:05:07')
    expect(formatLogTime('2026-01-03 10:05:07')).toBe('2026-01-03 10:05:07')
  })
})

describe('ResearchQuickMutate — status change', () => {
  it('persists a status change through updateHypothesis (t4)', async () => {
    useResearchStore.getState().loadStatus(makeStatus(), 'p1')

    const container = await render(<ResearchQuickMutate />)
    const select = container.querySelector<HTMLSelectElement>(
      'select[aria-label="Status for H-001"]',
    )!
    expect(select.value).toBe('open')

    await act(async () => {
      select.value = 'in-progress'
      select.dispatchEvent(new Event('change', { bubbles: true }))
      await new Promise((r) => setTimeout(r, 0))
    })

    expect(updateHypothesis).toHaveBeenCalledWith('p1', 'H-001', { status: 'in-progress' })
  })

  it('does not offer illegal status transitions', async () => {
    useResearchStore.getState().loadStatus(makeStatus(), 'p1')

    const container = await render(<ResearchQuickMutate />)
    const select = container.querySelector<HTMLSelectElement>(
      'select[aria-label="Status for H-001"]',
    )!

    const options = Array.from(select.options).map((o) => o.value)
    // open → in-progress | cancelled are the only legal transitions; the
    // current value is kept so the controlled select always has a match.
    expect(options).toContain('open')
    expect(options).toContain('in-progress')
    expect(options).toContain('cancelled')
    expect(options).not.toContain('confirmed')
    expect(options).not.toContain('refuted')
  })

  it('does not call updateHypothesis when the status is unchanged', async () => {
    useResearchStore.getState().loadStatus(makeStatus(), 'p1')

    const container = await render(<ResearchQuickMutate />)
    const select = container.querySelector<HTMLSelectElement>(
      'select[aria-label="Status for H-001"]',
    )!

    await act(async () => {
      // Re-selecting the current value must be a no-op (guarded in changeStatus).
      select.value = 'open'
      select.dispatchEvent(new Event('change', { bubbles: true }))
      await new Promise((r) => setTimeout(r, 0))
    })

    expect(updateHypothesis).not.toHaveBeenCalled()
  })

  it('renders nothing when there is no active front', async () => {
    const status = makeStatus()
    status.root!.projects[0]!.metrics.active_front = []
    useResearchStore.getState().loadStatus(status, 'p1')

    const container = await render(<ResearchQuickMutate />)
    expect(container.querySelector('[data-testid="research-quick-mutate"]')).toBeNull()
  })
})
