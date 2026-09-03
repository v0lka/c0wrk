// @vitest-environment jsdom
import { describe, it, expect, vi, beforeEach, afterEach } from 'vitest'
import { act } from 'react'
import { createRoot, type Root } from 'react-dom/client'

import { ResearchNextStep } from './ResearchNextStep'
import { ResearchQuickActions } from './ResearchQuickActions'
import { ResearchLog } from './ResearchLog'
import { latestLogEntries, formatLogTime, DEFAULT_LOG_LIMIT } from './researchLogUtils'
import { ResearchQuickMutate } from './ResearchQuickMutate'
import { ResearchPanel } from './index'
import { useResearchStore } from '@/stores/researchStore'
import { useProjectStore } from '@/stores/projectStore'
import { useFileViewerStore } from '@/stores/fileViewerStore'
import { RESEARCH_TAB_PATH } from '@/stores/researchStore'
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

// The data-sync hooks are side-effect only; stub them so panel tests exercise
// rendering + dispatch against a seeded store without backend fetches.
vi.mock('@/hooks/useResearchStatusEvents', () => ({
  useResearchStatusEvents: () => {},
}))
vi.mock('@/hooks/useResearchFileWatcher', () => ({
  useResearchFileWatcher: () => {},
}))

vi.mock('@/api/research', () => ({
  getResearchStatus: vi.fn(),
  getResearchGraph: vi.fn(),
  getResearchNextStep: vi.fn(),
  updateHypothesis: vi.fn(),
  createHypothesis: vi.fn(),
}))

// Radix dropdown positioning observes the trigger with ResizeObserver, which
// jsdom does not provide (same stub as combobox.test.tsx).
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
  useFileViewerStore.setState({ openTabs: [], activeFile: null })
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
    expect(sendSpy).toHaveBeenCalledWith(
      buildNextStepPrompt(nextStep),
      ['research-experiment'],
      undefined,
      undefined,
      { newSession: false },
    )
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

    expect(sendSpy).toHaveBeenCalledWith(
      buildNextStepPrompt(nextStep),
      ['research-init'],
      undefined,
      undefined,
      { newSession: false },
    )
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
    // The Shift modifier is visible without hovering, not only in tooltips.
    const hint = container.querySelector('[data-testid="research-shift-hint"]')
    expect(hint?.textContent).toContain('Shift')
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
    const [prompt, skills, , , options] = sendSpy.mock.calls[0]!
    expect(skills).toEqual(['research-synthesis'])
    expect(typeof prompt).toBe('string')
    expect(prompt.length).toBeGreaterThan(0)
    expect(options).toEqual({ newSession: false })
  })

  it('dispatches into a fresh session when Shift is held (quick action)', async () => {
    const container = await render(<ResearchQuickActions />)
    const decision = Array.from(
      container.querySelectorAll<HTMLButtonElement>('[data-testid="research-quick-action"]'),
    ).find((b) => b.getAttribute('data-skill') === 'research-decision')!

    await act(async () => {
      decision.dispatchEvent(new MouseEvent('click', { bubbles: true, shiftKey: true }))
    })

    expect(sendSpy).toHaveBeenCalledTimes(1)
    const [prompt, skills, , , options] = sendSpy.mock.calls[0]!
    expect(skills).toEqual(['research-decision'])
    expect(prompt.length).toBeGreaterThan(0)
    expect(options).toEqual({ newSession: true })
  })

  it('flips Synthesize to "Update report" (label + prompt) once a report exists', async () => {
    const status = makeStatus()
    status.root!.projects[0]!.has_report = true
    useResearchStore.getState().loadStatus(status, 'p1')

    const container = await render(<ResearchQuickActions />)
    const synthesize = Array.from(
      container.querySelectorAll<HTMLButtonElement>('[data-testid="research-quick-action"]'),
    ).find((b) => b.getAttribute('data-skill') === 'research-synthesis')!
    expect(synthesize.textContent).toContain('Update report')

    await act(async () => {
      synthesize.click()
    })

    const [prompt, skills] = sendSpy.mock.calls[0]!
    expect(skills).toEqual(['research-synthesis'])
    expect(prompt).toBe('Update the existing research report with the latest results.')
  })
})

describe('ResearchQuickActions — Run experiment dropdown', () => {
  function experimentTrigger(container: HTMLElement): HTMLButtonElement {
    return Array.from(
      container.querySelectorAll<HTMLButtonElement>('[data-testid="research-quick-action"]'),
    ).find((b) => b.getAttribute('data-skill') === 'research-experiment')!
  }

  async function openExperimentMenu(container: HTMLElement): Promise<HTMLElement> {
    const trigger = experimentTrigger(container)
    await act(async () => {
      trigger.dispatchEvent(new MouseEvent('pointerdown', { bubbles: true }))
      await new Promise((r) => setTimeout(r, 10))
    })
    const menu = document.body.querySelector('[role="menu"]')
    expect(menu).not.toBeNull()
    return menu as HTMLElement
  }

  it('lists only the active-front hypotheses and dispatches the picked target', async () => {
    useResearchStore.getState().loadStatus(makeStatus(), 'p1')

    const container = await render(<ResearchQuickActions />)
    const menu = await openExperimentMenu(container)
    const items = Array.from(menu.querySelectorAll<HTMLElement>('[role="menuitem"]'))
    // H-001 (open) is on the active front; H-002 (confirmed) is not.
    expect(items.length).toBe(1)
    expect(items[0]!.textContent).toContain('H-001')
    expect(items[0]!.textContent).toContain('Leading hypothesis')

    await act(async () => {
      items[0]!.dispatchEvent(new MouseEvent('mousedown', { bubbles: true }))
      items[0]!.dispatchEvent(new MouseEvent('click', { bubbles: true }))
    })

    expect(sendSpy).toHaveBeenCalledTimes(1)
    const [prompt, skills, , , options] = sendSpy.mock.calls[0]!
    expect(skills).toEqual(['research-experiment'])
    expect(prompt).toContain('H-001')
    expect(prompt).toContain('Leading hypothesis')
    expect(options).toEqual({ newSession: false })
  })

  it('dispatches into a fresh session when Shift is held on the picked item', async () => {
    useResearchStore.getState().loadStatus(makeStatus(), 'p1')

    const container = await render(<ResearchQuickActions />)
    const menu = await openExperimentMenu(container)
    const item = menu.querySelector<HTMLElement>('[role="menuitem"]')!

    await act(async () => {
      item.dispatchEvent(new MouseEvent('mousedown', { bubbles: true, shiftKey: true }))
      item.dispatchEvent(new MouseEvent('click', { bubbles: true }))
    })

    const [, , , , options] = sendSpy.mock.calls[0]!
    expect(options).toEqual({ newSession: true })
  })

  it('disables the Run experiment trigger when the active front is empty', async () => {
    const status = makeStatus()
    status.root!.projects[0]!.metrics.active_front = []
    useResearchStore.getState().loadStatus(status, 'p1')

    const container = await render(<ResearchQuickActions />)
    expect(experimentTrigger(container).disabled).toBe(true)
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

describe('ResearchPanel — header + View Artifacts', () => {
  function viewArtifactsTrigger(container: HTMLElement): HTMLButtonElement {
    const el = container.querySelector<HTMLButtonElement>(
      '[data-testid="research-view-artifacts"]',
    )
    expect(el).not.toBeNull()
    return el!
  }

  async function openArtifactsMenu(container: HTMLElement): Promise<HTMLElement> {
    const trigger = viewArtifactsTrigger(container)
    await act(async () => {
      trigger.dispatchEvent(new MouseEvent('pointerdown', { bubbles: true }))
      await new Promise((r) => setTimeout(r, 10))
    })
    const menu = document.body.querySelector('[role="menu"]')
    expect(menu).not.toBeNull()
    return menu as HTMLElement
  }

  it('carries the full project title as a tooltip on the truncated header', async () => {
    useResearchStore.getState().loadStatus(makeStatus(), 'p1')

    const container = await render(<ResearchPanel />)
    const header = container.querySelector<HTMLElement>('span[title="Test Research"]')
    expect(header).not.toBeNull()
    expect(header!.textContent).toBe('Test Research')
  })

  it('drops the old bottom quick links (no New hypothesis button)', async () => {
    useResearchStore.getState().loadStatus(makeStatus(), 'p1')

    const container = await render(<ResearchPanel />)
    const bottomBar = container.lastElementChild!
    expect(bottomBar.textContent).not.toContain('New hypothesis')
    expect(bottomBar.textContent).toContain('View artifacts')
  })

  it('lists only existing artifacts and opens the picked one', async () => {
    // total: 2 hypotheses (graph exists); no prior art, no report.
    useResearchStore.getState().loadStatus(makeStatus(), 'p1')

    const container = await render(<ResearchPanel />)
    const menu = await openArtifactsMenu(container)
    const items = Array.from(menu.querySelectorAll<HTMLElement>('[role="menuitem"]'))
    expect(items.map((i) => i.textContent)).toEqual(['Brief', 'Graph'])

    await act(async () => {
      items[0]!.dispatchEvent(new MouseEvent('click', { bubbles: true }))
    })
    expect(useFileViewerStore.getState().openTabs).toContain('/root/.research/R-001/brief.md')
  })

  it('lists all four artifacts when everything exists; Graph opens the workspace tab', async () => {
    const status = makeStatus()
    status.root!.projects[0]!.prior_art_count = 3
    status.root!.projects[0]!.has_report = true
    useResearchStore.getState().loadStatus(status, 'p1')

    const container = await render(<ResearchPanel />)
    const menu = await openArtifactsMenu(container)
    const items = Array.from(menu.querySelectorAll<HTMLElement>('[role="menuitem"]'))
    expect(items.map((i) => i.textContent)).toEqual(['Brief', 'Prior art', 'Graph', 'Report'])

    await act(async () => {
      items[2]!.dispatchEvent(new MouseEvent('click', { bubbles: true }))
    })
    expect(useFileViewerStore.getState().activeFile).toBe(RESEARCH_TAB_PATH)
  })

  it('disables View Artifacts when no artifacts exist at all', async () => {
    const status = makeStatus()
    status.root!.projects = []
    status.root!.active_project_id = ''
    useResearchStore.getState().loadStatus(status, 'p1')

    const container = await render(<ResearchPanel />)
    expect(viewArtifactsTrigger(container).disabled).toBe(true)
  })
})
