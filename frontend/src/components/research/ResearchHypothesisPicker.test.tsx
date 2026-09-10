// @vitest-environment jsdom
// ResearchHypothesisPicker — the dashboard's current-card control. Covers the
// listing/sorting (pinned → active front → rest), the current-card selection
// + scoped next-step refetch, and the status-flip surface inherited from the
// former ResearchQuickMutate block: the shared applyGraphOrRefresh routing
// ([18]b), the expected-R-NNN argument ([19]a), the cross-project guard, the
// disable-while-saving behavior, and the action-generation guard against
// same-path re-entry ([71]a).
import { describe, it, expect, vi, beforeEach, afterEach } from 'vitest'
import { act } from 'react'
import { createRoot, type Root } from 'react-dom/client'

import { ResearchHypothesisPicker } from './ResearchHypothesisPicker'
import { TooltipProvider } from '@/components/ui/tooltip'
import {
  applyGraphOrRefresh,
  refreshNextStep,
} from './applyGraphOrRefresh'
import { updateHypothesis, setHypothesisPinned, getResearchNextStep, getResearchStatus } from '@/api/research'
import { useResearchStore, selectActiveProject } from '@/stores/researchStore'
import { useProjectStore } from '@/stores/projectStore'
import type {
  ResearchStatus,
  ResearchGraphResponse,
  ResearchNextStep as ResearchNextStepDTO,
} from '@/types/models'

const { sendSpy } = vi.hoisted(() => ({ sendSpy: vi.fn() }))

vi.mock('@/hooks/useMessageSender', () => ({
  useMessageSender: () => ({ send: sendSpy, cancel: vi.fn(), isProcessing: false }),
}))

vi.mock('@/api/research', () => ({
  updateHypothesis: vi.fn(),
  setHypothesisPinned: vi.fn(),
  getResearchStatus: vi.fn(),
  getResearchGraph: vi.fn(),
  getResearchNextStep: vi.fn(),
  setActiveResearch: vi.fn(),
  deleteResearch: vi.fn(),
  setResearchPinned: vi.fn(),
  createHypothesis: vi.fn(),
  enableResearch: vi.fn(),
  disableResearch: vi.fn(),
}))

// Spy on the shared convergence paths while delegating to the real
// implementations, so the tests assert BOTH the routing ([18]b / scoped
// refetch) and the real store effects.
vi.mock('./applyGraphOrRefresh', async (importOriginal) => {
  const actual = await importOriginal<typeof import('./applyGraphOrRefresh')>()
  return {
    ...actual,
    applyGraphOrRefresh: vi.fn(actual.applyGraphOrRefresh),
    fullResearchRefresh: vi.fn(actual.fullResearchRefresh),
    refreshNextStep: vi.fn(actual.refreshNextStep),
  }
})

vi.mock('@/lib/logger', () => ({
  logger: { error: vi.fn(), warn: vi.fn(), info: vi.fn(), debug: vi.fn() },
}))

// Radix dropdown positioning observes the trigger with ResizeObserver, which
// jsdom does not provide (same stub as the dashboard tests).
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
    // ItemActions rows embed a Tooltip, which requires a provider ancestor
    // (the app mounts one at the root — tests mirror that here).
    root.render(<TooltipProvider>{el}</TooltipProvider>)
  })
  return container
}

/** R-001 with a mixed graph: H-001/H-002 form the active front, H-003/H-004
 *  are terminal. The reconciled current card is the front leader (H-001). */
function makeStatus(pinnedHypotheses: Record<string, string[]> = {}): ResearchStatus {
  return {
    enabled: true,
    project_id: 'p1',
    research_root: '/root/.research',
    pinned_hypotheses: pinnedHypotheses,
    root: {
      path: '/root/.research',
      index: [{ id: 'R-001', path: 'R-001-test/brief.md' }],
      active_project_id: 'R-001',
      projects: [
        {
          id: 'R-001',
          brief: { id: 'R-001', title: 'Test Research' },
          graph: {
            nodes: [
              { id: 'H-001', title: 'Leading hypothesis', status: 'open' },
              { id: 'H-002', title: 'Running hypothesis', status: 'in-progress' },
              { id: 'H-003', title: 'Proven hypothesis', status: 'confirmed' },
              { id: 'H-004', title: 'Dead hypothesis', status: 'refuted' },
            ],
            edges: [],
          },
          metrics: {
            total: 4,
            by_status: { open: 1, 'in-progress': 1, confirmed: 1, refuted: 1 },
            confirmation_rate: 0.5,
            depth: 1,
            breadth: 2,
            active_front: ['H-001', 'H-002'],
          },
          prior_art_count: 0,
          has_report: false,
          log: [],
        },
      ],
    },
  }
}

function makeGraphResponse(status: string): ResearchGraphResponse {
  const base = makeStatus()
  const p = base.root!.projects[0]!
  return {
    project_id: 'R-001',
    graph: {
      nodes: p.graph.nodes.map((n) =>
        n.id === 'H-001' ? { ...n, status } : n,
      ),
      edges: [],
    },
    metrics: p.metrics,
    has_report: false,
    log: [],
  }
}

function makeNextStep(overrides: Partial<ResearchNextStepDTO> = {}): ResearchNextStepDTO {
  return {
    project_id: 'p1',
    action: 'research-experiment',
    target: 'H-001',
    reason: 'run an experiment',
    skill: 'research-experiment',
    ...overrides,
  }
}

function trigger(container: HTMLElement): HTMLButtonElement {
  return container.querySelector<HTMLButtonElement>(
    '[data-testid="research-hypothesis-picker-trigger"]',
  )!
}

function statusSelect(container: HTMLElement, id: string): HTMLSelectElement {
  return container.querySelector<HTMLSelectElement>(
    `select[aria-label="Status for ${id}"]`,
  )!
}

function changeSelect(el: HTMLSelectElement, value: string): void {
  el.value = value
  el.dispatchEvent(new Event('change', { bubbles: true }))
}

async function openMenu(container: HTMLElement): Promise<HTMLElement> {
  const btn = trigger(container)
  await act(async () => {
    btn.dispatchEvent(new MouseEvent('pointerdown', { bubbles: true }))
    await new Promise((r) => setTimeout(r, 10))
  })
  const menu = document.body.querySelector('[role="menu"]')
  expect(menu).not.toBeNull()
  return menu as HTMLElement
}

function menuItems(menu: HTMLElement): HTMLElement[] {
  return Array.from(menu.querySelectorAll<HTMLElement>('[role="menuitem"]'))
}

const flush = () => act(async () => { await new Promise((r) => setTimeout(r, 0)) })

beforeEach(() => {
  vi.clearAllMocks()
  vi.mocked(getResearchNextStep).mockResolvedValue(makeNextStep())
  sendSpy.mockReset()
  useProjectStore.setState({ projects: null, activeProjectId: 'p1', lastRealProjectId: 'p1' })
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

describe('ResearchHypothesisPicker — trigger + listing', () => {
  it('shows the current card (id + title) and its status in the select', async () => {
    const container = await render(<ResearchHypothesisPicker />)
    expect(trigger(container).textContent).toContain('H-001')
    expect(trigger(container).textContent).toContain('Leading hypothesis')
    expect(statusSelect(container, 'H-001').value).toBe('open')
  })

  it('lists every hypothesis: active front first, then the rest, each by H-NNN', async () => {
    const container = await render(<ResearchHypothesisPicker />)
    const menu = await openMenu(container)
    const ids = menuItems(menu).map((i) => i.textContent)
    expect(ids[0]).toContain('H-001')
    expect(ids[1]).toContain('H-002')
    expect(ids[2]).toContain('H-003')
    expect(ids[3]).toContain('H-004')
    // The current card carries the check.
    expect(menuItems(menu)[0]!.querySelector('svg.lucide-check')).not.toBeNull()
  })

  it('floats pinned cards above the active front (pinned → front → rest)', async () => {
    useResearchStore.getState().loadStatus(
      makeStatus({ 'H-003': ['R-001-test/hypotheses/H-003.md'] }),
      'p1',
    )

    const container = await render(<ResearchHypothesisPicker />)
    const menu = await openMenu(container)
    const ids = menuItems(menu).map((i) => i.textContent)
    expect(ids[0]).toContain('H-003')
    expect(ids[1]).toContain('H-001')
    expect(ids[2]).toContain('H-002')
    expect(ids[3]).toContain('H-004')
  })

  it('a pin keyed by the same H-NNN under ANOTHER project does not pin this card', async () => {
    useResearchStore.getState().loadStatus(
      makeStatus({ 'H-003': ['R-009-other/hypotheses/H-003.md'] }),
      'p1',
    )

    const container = await render(<ResearchHypothesisPicker />)
    const menu = await openMenu(container)
    // The pin belongs to R-009's H-003, not R-001's → normal ordering.
    const ids = menuItems(menu).map((i) => i.textContent)
    expect(ids[0]).toContain('H-001')
    expect(ids[2]).toContain('H-003')
  })

  it('renders a status dot for every row', async () => {
    const container = await render(<ResearchHypothesisPicker />)
    const menu = await openMenu(container)
    const rows = menuItems(menu)
    // One inline-styled dot span per row (the status color token).
    for (const row of rows) {
      expect(row.querySelector<HTMLElement>('span[style]')).not.toBeNull()
    }
    expect(rows.length).toBe(4)
  })

  it('disables the trigger and hides the status select when the graph is empty', async () => {
    const status = makeStatus()
    status.root!.projects[0]!.graph = { nodes: [], edges: [] }
    status.root!.projects[0]!.metrics = {
      total: 0,
      by_status: {},
      confirmation_rate: 0,
      depth: 0,
      breadth: 0,
      active_front: [],
    }
    useResearchStore.getState().loadStatus(status, 'p1')

    const container = await render(<ResearchHypothesisPicker />)
    expect(trigger(container).disabled).toBe(true)
    expect(container.querySelector('select')).toBeNull()
    // The Create gesture stays available for the very first card.
    expect(
      container.querySelector<HTMLButtonElement>('[data-testid="research-create-hypothesis"]'),
    ).not.toBeNull()
  })
})

describe('ResearchHypothesisPicker — selection', () => {
  it('picking a card stamps it as current and refetches the scoped next step', async () => {
    vi.mocked(getResearchNextStep).mockResolvedValue(
      makeNextStep({ target: 'H-003', reason: 'decide the proven card' }),
    )

    const container = await render(<ResearchHypothesisPicker />)
    const menu = await openMenu(container)
    const h003 = menuItems(menu).find((i) => i.textContent?.includes('H-003'))!

    await act(async () => {
      h003.dispatchEvent(new MouseEvent('mousedown', { bubbles: true }))
      h003.dispatchEvent(new MouseEvent('click', { bubbles: true }))
    })
    await flush()

    const s = useResearchStore.getState()
    expect(s.activeHypothesisId).toBe('H-003')
    expect(s.activeHypothesisResearchId).toBe('R-001')
    expect(getResearchNextStep).toHaveBeenCalledWith('p1', 'H-003')
    expect(refreshNextStep).toHaveBeenCalledWith('p1')
    expect(s.nextStep?.target).toBe('H-003')
    // The trigger + status select now follow the picked card.
    expect(trigger(container).textContent).toContain('H-003')
    expect(statusSelect(container, 'H-003').value).toBe('confirmed')
  })

  it('re-picking the current card is a no-op', async () => {
    const container = await render(<ResearchHypothesisPicker />)
    const menu = await openMenu(container)
    const h001 = menuItems(menu).find((i) => i.textContent?.includes('H-001'))!

    await act(async () => {
      h001.dispatchEvent(new MouseEvent('mousedown', { bubbles: true }))
      h001.dispatchEvent(new MouseEvent('click', { bubbles: true }))
    })
    await flush()

    expect(getResearchNextStep).not.toHaveBeenCalled()
  })
})

describe('ResearchHypothesisPicker — status flip (former QuickMutate)', () => {
  it('persists a status change through updateHypothesis (t4)', async () => {
    const res = makeGraphResponse('in-progress')
    vi.mocked(updateHypothesis).mockResolvedValue(res)

    const container = await render(<ResearchHypothesisPicker />)
    await act(async () => {
      changeSelect(statusSelect(container, 'H-001'), 'in-progress')
    })
    await flush()

    expect(updateHypothesis).toHaveBeenCalledWith('p1', 'R-001', 'H-001', {
      status: 'in-progress',
    })
    expect(applyGraphOrRefresh).toHaveBeenCalledWith(res, 'p1', expect.any(Number))
    // The helper really applied it (the store carries the response's graph).
    const node = selectActiveProject(useResearchStore.getState())?.graph.nodes.find(
      (n) => n.id === 'H-001',
    )
    expect(node?.status).toBe('in-progress')
  })

  it('does not offer illegal status transitions', async () => {
    const container = await render(<ResearchHypothesisPicker />)
    const options = Array.from(statusSelect(container, 'H-001').options).map((o) => o.value)
    // open → in-progress | cancelled are the only legal transitions; the
    // current value is kept so the controlled select always has a match.
    expect(options).toEqual(['open', 'in-progress', 'cancelled'])
  })

  it('does not call updateHypothesis when the status is unchanged', async () => {
    const container = await render(<ResearchHypothesisPicker />)
    await act(async () => {
      changeSelect(statusSelect(container, 'H-001'), 'open')
    })
    await flush()

    expect(updateHypothesis).not.toHaveBeenCalled()
  })

  it('bails without an RPC when the research snapshot belongs to another workspace project', async () => {
    const container = await render(<ResearchHypothesisPicker />)
    // Project switch after render: the research store still holds p1's
    // graph while the project store moved to p2. H-001-style ids collide
    // across projects, so the flip must never be sent.
    useProjectStore.setState({ activeProjectId: 'p2' })

    await act(async () => {
      changeSelect(statusSelect(container, 'H-001'), 'in-progress')
    })
    await flush()

    expect(updateHypothesis).not.toHaveBeenCalled()
    expect(applyGraphOrRefresh).not.toHaveBeenCalled()
    // The mismatch surfaces on the block's error line instead…
    expect(container.querySelector('[role="alert"]')?.textContent).toContain(
      'different project',
    )
    // …and no disabled state is left stuck.
    expect(statusSelect(container, 'H-001').disabled).toBe(false)
  })

  it('disables the select while a save is in flight', async () => {
    let resolveRpc: (v: ResearchGraphResponse) => void = () => {}
    vi.mocked(updateHypothesis).mockImplementation(
      () => new Promise<ResearchGraphResponse>((resolve) => { resolveRpc = resolve }),
    )

    const container = await render(<ResearchHypothesisPicker />)
    await act(async () => {
      changeSelect(statusSelect(container, 'H-001'), 'in-progress')
    })
    await flush()

    expect(statusSelect(container, 'H-001').disabled).toBe(true)

    await act(async () => {
      resolveRpc(makeGraphResponse('in-progress'))
    })
    await flush()

    expect(statusSelect(container, 'H-001').disabled).toBe(false)
  })

  it("a superseded flip's late failure does not annotate the newer flip's state ([71]a)", async () => {
    // Same-path re-entry: two flips overlap (the disabled select cannot
    // rule out programmatic re-entry before React commits the re-render).
    // The OLDER flip's generation is superseded; its late rejection must
    // neither surface an error nor clobber the newer flip's cleared state.
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

    const container = await render(<ResearchHypothesisPicker />)
    // Both change events dispatch inside one act — React has not committed
    // the disabled re-render yet, so both flips start (gen 1, then gen 2).
    await act(async () => {
      changeSelect(statusSelect(container, 'H-001'), 'in-progress')
      changeSelect(statusSelect(container, 'H-001'), 'cancelled')
    })

    expect(updateHypothesis).toHaveBeenCalledTimes(2)

    // The NEWER flip (gen 2) completes successfully…
    await act(async () => {
      deferreds[1]!.resolve(makeGraphResponse('cancelled'))
    })
    await flush()
    expect(statusSelect(container, 'H-001').disabled).toBe(false)

    // …and the OLDER flip's late rejection is swallowed: no error, no stuck
    // saving state.
    await act(async () => {
      deferreds[0]!.reject(new Error('older flip failed'))
    })
    await flush()

    expect(container.querySelector('[role="alert"]')).toBeNull()
    expect(statusSelect(container, 'H-001').disabled).toBe(false)
  })
})

describe('ResearchHypothesisPicker — pins', () => {
  it('pins through the RPC and mirrors the refreshed pins into the store', async () => {
    const refreshed = makeStatus({ 'H-001': ['R-001-test/hypotheses/H-001.md'] })
    vi.mocked(getResearchStatus).mockResolvedValue(refreshed)

    const container = await render(<ResearchHypothesisPicker />)
    const menu = await openMenu(container)
    const h001 = menuItems(menu).find((i) => i.textContent?.includes('H-001'))!
    const pin = h001.querySelector('button')!

    await act(async () => {
      pin.dispatchEvent(new MouseEvent('click', { bubbles: true }))
    })
    await flush()

    expect(setHypothesisPinned).toHaveBeenCalledWith('p1', 'R-001', 'H-001', true)
    // The pin RPC emits no event — the full status refetch is the refresh
    // signal, and the store mirrors the new pins.
    expect(getResearchStatus).toHaveBeenCalledWith('p1')
    expect(useResearchStore.getState().pinnedHypotheses).toEqual({
      'H-001': ['R-001-test/hypotheses/H-001.md'],
    })
  })
})

describe('ResearchHypothesisPicker — Create hypothesis', () => {
  it('dispatches the research-hypothesis skill with the create prompt', async () => {
    const container = await render(<ResearchHypothesisPicker />)
    const plus = container.querySelector<HTMLButtonElement>(
      '[data-testid="research-create-hypothesis"]',
    )!

    await act(async () => {
      plus.click()
    })

    expect(sendSpy).toHaveBeenCalledTimes(1)
    expect(sendSpy).toHaveBeenCalledWith(
      'Create a new hypothesis.',
      ['research-hypothesis'],
      undefined,
      undefined,
      { newSession: false },
    )
  })

  it('dispatches into a fresh session when Shift is held', async () => {
    const container = await render(<ResearchHypothesisPicker />)
    const plus = container.querySelector<HTMLButtonElement>(
      '[data-testid="research-create-hypothesis"]',
    )!

    await act(async () => {
      plus.dispatchEvent(new MouseEvent('click', { bubbles: true, shiftKey: true }))
    })

    const [, , , , options] = sendSpy.mock.calls[0]!
    expect(options).toEqual({ newSession: true })
  })
})
