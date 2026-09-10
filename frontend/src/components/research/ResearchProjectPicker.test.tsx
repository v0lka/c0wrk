// @vitest-environment jsdom
import { describe, it, expect, vi, beforeEach, afterEach } from 'vitest'
import { act } from 'react'
import { createRoot, type Root } from 'react-dom/client'

import { ResearchProjectPicker } from './ResearchProjectPicker'
import { useResearchStore, selectActiveProject } from '@/stores/researchStore'
import { useProjectStore } from '@/stores/projectStore'
import {
  setActiveResearch,
  deleteResearch,
  setResearchPinned,
  getResearchStatus,
  getResearchNextStep,
} from '@/api/research'
import type {
  ResearchStatus,
  ResearchNextStep as ResearchNextStepDTO,
  ResearchProject,
} from '@/types/models'

const { sendSpy } = vi.hoisted(() => ({ sendSpy: vi.fn() }))

vi.mock('@/hooks/useMessageSender', () => ({
  useMessageSender: () => ({ send: sendSpy, cancel: vi.fn(), isProcessing: false }),
}))

// ItemAction renders Radix Tooltips, which require the app-root
// TooltipProvider; render plain buttons instead (the same stopPropagation +
// onClick contract the real overlay provides inside a menu row).
vi.mock('@/components/layout/ItemAction', () => ({
  ItemAction: ({
    label,
    onClick,
    children,
  }: {
    label: string
    onClick: () => void
    children: React.ReactNode
  }) => (
    <button
      type="button"
      aria-label={label}
      onClick={(e) => {
        e.stopPropagation()
        onClick()
      }}
    >
      {children}
    </button>
  ),
  ItemActions: ({ children }: { children: React.ReactNode }) => (
    <span>{children}</span>
  ),
}))

vi.mock('@/api/research', () => ({
  setActiveResearch: vi.fn(),
  deleteResearch: vi.fn(),
  setResearchPinned: vi.fn(),
  getResearchStatus: vi.fn(),
  getResearchGraph: vi.fn(),
  getResearchNextStep: vi.fn(),
  updateHypothesis: vi.fn(),
  createHypothesis: vi.fn(),
  enableResearch: vi.fn(),
  disableResearch: vi.fn(),
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
    root.render(el)
  })
  return container
}

function makeProject(id: string, title: string): ResearchProject {
  return {
    id,
    brief: { id, title },
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
  }
}

/** Two research projects; R-002 is the active one. `pinnedResearch` seeds the
 *  persisted pins mirrored into the store by loadStatus. */
function makeStatus(pinnedResearch: string[] = []): ResearchStatus {
  return {
    enabled: true,
    project_id: 'p1',
    research_root: '/root/.research',
    pinned_research: pinnedResearch,
    root: {
      path: '/root/.research',
      index: [
        { id: 'R-001', path: 'R-001-first/brief.md' },
        { id: 'R-002', path: 'R-002-second/brief.md' },
      ],
      active_project_id: 'R-002',
      projects: [makeProject('R-001', 'First research'), makeProject('R-002', 'Second research')],
    },
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

function pickerTrigger(container: HTMLElement): HTMLButtonElement {
  return container.querySelector<HTMLButtonElement>(
    '[data-testid="research-project-picker"]',
  )!
}

async function openMenu(container: HTMLElement): Promise<HTMLElement> {
  const trigger = pickerTrigger(container)
  await act(async () => {
    trigger.dispatchEvent(new MouseEvent('pointerdown', { bubbles: true }))
    await new Promise((r) => setTimeout(r, 10))
  })
  const menu = document.body.querySelector('[role="menu"]')
  expect(menu).not.toBeNull()
  return menu as HTMLElement
}

/** The hover ItemActions buttons rendered inside a menu item row (pin first,
 *  delete second — the row itself is not a button). */
function rowActions(item: HTMLElement): HTMLButtonElement[] {
  return Array.from(item.querySelectorAll('button'))
}

const flush = () => act(async () => { await new Promise((r) => setTimeout(r, 0)) })

beforeEach(() => {
  vi.clearAllMocks()
  vi.mocked(getResearchStatus).mockResolvedValue(makeStatus())
  vi.mocked(getResearchNextStep).mockResolvedValue(makeNextStep())
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

describe('ResearchProjectPicker — trigger + listing', () => {
  it('shows the active project title in the trigger', async () => {
    const container = await render(<ResearchProjectPicker />)
    expect(pickerTrigger(container).textContent).toContain('Second research')
  })

  it('disables the trigger when the root has no projects', async () => {
    const status = makeStatus()
    status.root!.projects = []
    status.root!.active_project_id = ''
    useResearchStore.getState().loadStatus(status, 'p1')

    const container = await render(<ResearchProjectPicker />)
    expect(pickerTrigger(container).disabled).toBe(true)
  })

  it('lists projects ascending by R-NNN and marks the active one', async () => {
    const container = await render(<ResearchProjectPicker />)
    const menu = await openMenu(container)
    const items = Array.from(menu.querySelectorAll<HTMLElement>('[role="menuitem"]'))
    expect(items.map((i) => i.textContent)).toEqual([
      expect.stringContaining('R-001'),
      expect.stringContaining('R-002'),
    ])
    // The active project (R-002) carries the check + emphasis.
    expect(items[1]!.textContent).toContain('Second research')
    expect(items[1]!.querySelector('svg.lucide-check')).not.toBeNull()
    expect(items[0]!.querySelector('svg.lucide-check')).toBeNull()
  })

  it('floats pinned projects to the top (pinned → R-NNN)', async () => {
    useResearchStore.getState().loadStatus(makeStatus(['R-001-first/brief.md']), 'p1')

    const container = await render(<ResearchProjectPicker />)
    const menu = await openMenu(container)
    const items = Array.from(menu.querySelectorAll<HTMLElement>('[role="menuitem"]'))
    // R-002 is active but unpinned; R-001 is pinned → R-001 first.
    expect(items[0]!.textContent).toContain('R-001')
    expect(items[1]!.textContent).toContain('R-002')
  })
})

describe('ResearchProjectPicker — selection', () => {
  it('switches the active research and refetches the next step', async () => {
    const switched = makeStatus()
    switched.root!.active_project_id = 'R-001'
    vi.mocked(setActiveResearch).mockResolvedValue(switched)

    const container = await render(<ResearchProjectPicker />)
    const menu = await openMenu(container)
    const r001 = Array.from(menu.querySelectorAll<HTMLElement>('[role="menuitem"]'))[0]!

    await act(async () => {
      r001.dispatchEvent(new MouseEvent('mousedown', { bubbles: true }))
      r001.dispatchEvent(new MouseEvent('click', { bubbles: true }))
    })
    await flush()

    expect(setActiveResearch).toHaveBeenCalledWith('p1', 'R-001')
    // The refreshed status was applied to the store.
    expect(selectActiveProject(useResearchStore.getState())?.id).toBe('R-001')
    // The next step was refetched for the (reconciled) current card.
    expect(getResearchNextStep).toHaveBeenCalledWith('p1', expect.any(String))
    expect(useResearchStore.getState().nextStep).toEqual(makeNextStep())
  })

  it('drops the payload when the workspace project switched mid-flight', async () => {
    let resolveRpc!: (v: ResearchStatus) => void
    vi.mocked(setActiveResearch).mockImplementation(
      () => new Promise<ResearchStatus>((resolve) => { resolveRpc = resolve }),
    )

    const container = await render(<ResearchProjectPicker />)
    const menu = await openMenu(container)
    const r001 = Array.from(menu.querySelectorAll<HTMLElement>('[role="menuitem"]'))[0]!

    await act(async () => {
      r001.dispatchEvent(new MouseEvent('mousedown', { bubbles: true }))
      r001.dispatchEvent(new MouseEvent('click', { bubbles: true }))
    })
    // Project switch while the RPC is in flight.
    useProjectStore.setState({ activeProjectId: 'p2' })
    await act(async () => {
      resolveRpc(makeStatus())
    })
    await flush()

    expect(setActiveResearch).toHaveBeenCalledTimes(1)
    // loadStatus was skipped: the store keeps the original active project.
    expect(selectActiveProject(useResearchStore.getState())?.id).toBe('R-002')
  })

  it('surfaces a failed switch on the research store error banner', async () => {
    vi.mocked(setActiveResearch).mockRejectedValue(new Error('boom'))

    // The component mirrors the failure into the store error banner and also
    // logs it via logger.error → console.error; that logging is expected
    // noise for this exact scenario — keep the test output clean.
    const errorSpy = vi.spyOn(console, 'error').mockImplementation(() => {})
    try {
      const container = await render(<ResearchProjectPicker />)
      const menu = await openMenu(container)
      const r001 = Array.from(menu.querySelectorAll<HTMLElement>('[role="menuitem"]'))[0]!

      await act(async () => {
        r001.dispatchEvent(new MouseEvent('mousedown', { bubbles: true }))
        r001.dispatchEvent(new MouseEvent('click', { bubbles: true }))
      })
      await flush()

      expect(useResearchStore.getState().error).toContain('boom')
    } finally {
      errorSpy.mockRestore()
    }
  })
})

describe('ResearchProjectPicker — pins', () => {
  it('pins through the RPC and mirrors the refreshed pins into the store', async () => {
    const pinned = makeStatus(['R-001-first/brief.md'])
    vi.mocked(getResearchStatus).mockResolvedValue(pinned)

    const container = await render(<ResearchProjectPicker />)
    const menu = await openMenu(container)
    const r001 = Array.from(menu.querySelectorAll<HTMLElement>('[role="menuitem"]'))[0]!
    const pin = rowActions(r001)[0]!

    await act(async () => {
      pin.dispatchEvent(new MouseEvent('click', { bubbles: true }))
    })
    await flush()

    expect(setResearchPinned).toHaveBeenCalledWith('p1', 'R-001', true)
    // The pin RPC emits no event — the full status refetch is the refresh
    // signal, and the store mirrors the new pins.
    expect(getResearchStatus).toHaveBeenCalledWith('p1')
    expect(useResearchStore.getState().pinnedResearch).toEqual(['R-001-first/brief.md'])
  })
})

describe('ResearchProjectPicker — delete', () => {
  async function openDeleteDialog(container: HTMLElement): Promise<HTMLElement> {
    const menu = await openMenu(container)
    const r001 = Array.from(menu.querySelectorAll<HTMLElement>('[role="menuitem"]'))[0]!
    const del = rowActions(r001)[1]!
    await act(async () => {
      del.dispatchEvent(new MouseEvent('click', { bubbles: true }))
    })
    const dialog = document.body.querySelector('[role="dialog"]')
    expect(dialog).not.toBeNull()
    return dialog as HTMLElement
  }

  function dialogButton(dialog: HTMLElement, label: string): HTMLButtonElement {
    return Array.from(dialog.querySelectorAll('button')).find(
      (b) => b.textContent === label,
    )!
  }

  it('asks for confirmation, then deletes and applies the refreshed status', async () => {
    const refreshed = makeStatus()
    refreshed.root!.projects = [refreshed.root!.projects[1]!]
    refreshed.root!.index = [refreshed.root!.index[1]!]
    vi.mocked(deleteResearch).mockResolvedValue(refreshed)

    const container = await render(<ResearchProjectPicker />)
    const dialog = await openDeleteDialog(container)
    expect(dialog.textContent).toContain('R-001')
    expect(dialog.textContent).toContain('First research')

    await act(async () => {
      dialogButton(dialog, 'Delete').click()
    })
    await flush()

    expect(deleteResearch).toHaveBeenCalledWith('p1', 'R-001')
    expect(useResearchStore.getState().status?.root?.projects).toHaveLength(1)
    // The recommendation is refreshed for whatever is active after the
    // deletion.
    expect(getResearchNextStep).toHaveBeenCalledWith('p1', expect.any(String))
  })

  it('cancel closes the dialog without an RPC', async () => {
    const container = await render(<ResearchProjectPicker />)
    const dialog = await openDeleteDialog(container)

    await act(async () => {
      dialogButton(dialog, 'Cancel').click()
    })
    await flush()

    expect(deleteResearch).not.toHaveBeenCalled()
  })

  it('keeps the dialog open with an inline error when the RPC fails', async () => {
    vi.mocked(deleteResearch).mockRejectedValue(new Error('denied'))

    // The failure is rendered inline in the dialog and also logged via
    // logger.error → console.error; expected noise for this exact scenario —
    // keep the test output clean.
    const errorSpy = vi.spyOn(console, 'error').mockImplementation(() => {})
    try {
      const container = await render(<ResearchProjectPicker />)
      const dialog = await openDeleteDialog(container)

      await act(async () => {
        dialogButton(dialog, 'Delete').click()
      })
      await flush()

      expect(deleteResearch).toHaveBeenCalledTimes(1)
      const stillOpen = document.body.querySelector('[role="dialog"]')
      expect(stillOpen).not.toBeNull()
      expect(stillOpen!.textContent).toContain('denied')
    } finally {
      errorSpy.mockRestore()
    }
  })
})

describe('ResearchProjectPicker — research-init plus button', () => {
  it('dispatches research-init into a brand-new session', async () => {
    const container = await render(<ResearchProjectPicker />)
    const plus = container.querySelector<HTMLButtonElement>(
      '[data-testid="research-project-init"]',
    )!

    await act(async () => {
      plus.click()
    })

    expect(sendSpy).toHaveBeenCalledTimes(1)
    expect(sendSpy).toHaveBeenCalledWith(
      'Initialize a new research project.',
      ['research-init'],
      undefined,
      undefined,
      { newSession: true },
    )
  })

  it('surfaces a dispatch failure on the research store error', async () => {
    sendSpy.mockRejectedValue(new Error('runtime not ready'))
    const container = await render(<ResearchProjectPicker />)
    const plus = container.querySelector<HTMLButtonElement>(
      '[data-testid="research-project-init"]',
    )!

    await act(async () => {
      plus.click()
      await new Promise((r) => setTimeout(r, 0))
    })

    expect(useResearchStore.getState().error).toContain('research-init')
  })
})
