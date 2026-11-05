// @vitest-environment jsdom
// ResearchLegacyMigration — the blocking migration notice for pre-rename
// `.research` roots, plus its ResearchPanel integration: while the status
// reports legacy roots, the notice replaces the Dashboard/Papers content.
import { afterEach, beforeEach, describe, expect, it, vi } from 'vitest'
import { act } from 'react'
import { createRoot, type Root } from 'react-dom/client'

import { ResearchPanel } from './index'
import { ResearchLegacyMigration } from './ResearchLegacyMigration'
import { migrateResearchRoots } from '@/api/research'
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
  migrateResearchRoots: vi.fn(),
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

function makeStatus(legacyRoots?: string[]): ResearchStatus {
  return {
    enabled: true,
    project_id: 'p1',
    research_root: '/root/research',
    legacy_research_roots: legacyRoots,
    root: {
      path: '/root/research',
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

function migrateButton(container: HTMLElement): HTMLButtonElement {
  const el = container.querySelector<HTMLButtonElement>(
    '[data-testid="research-legacy-migration-migrate"]',
  )
  expect(el).not.toBeNull()
  return el!
}

beforeEach(() => {
  localStorage.clear()
  useResearchStore.getState().reset()
  useUIStore.setState({ researchSegmentByProject: {} })
  useProjectStore.setState({ projects: null, activeProjectId: 'p1', lastRealProjectId: 'p1' })
  vi.mocked(migrateResearchRoots).mockReset()
})

afterEach(() => {
  if (activeRoot) {
    act(() => {
      activeRoot!.unmount()
    })
    activeRoot = null
  }
})

describe('ResearchPanel — legacy-root migration gate', () => {
  it('replaces the Dashboard/Papers content with the notice while legacy roots exist', async () => {
    useResearchStore.getState().loadStatus(makeStatus(['/root/.research']), 'p1')
    const container = await render(<ResearchPanel />)

    expect(container.querySelector('[data-testid="research-legacy-migration"]')).not.toBeNull()
    expect(container.querySelector('[data-testid="research-legacy-migration-roots"]')?.textContent).toContain(
      '/root/.research',
    )
    // The segment control and both segments' content are gone.
    expect(container.querySelector('[data-testid="research-segment"]')).toBeNull()
    expect(container.querySelector('[data-testid="research-quick-actions"]')).toBeNull()
  })

  it('migrates on click and restores the panel content with the fresh status', async () => {
    useResearchStore.getState().loadStatus(makeStatus(['/root/.research']), 'p1')
    const fresh = makeStatus()
    vi.mocked(migrateResearchRoots).mockResolvedValue(fresh)
    const container = await render(<ResearchPanel />)

    await act(async () => {
      migrateButton(container).click()
    })

    expect(migrateResearchRoots).toHaveBeenCalledWith('p1')
    // The fresh status landed in the store; its empty legacy list unmounts
    // the notice and the panel content returns.
    expect(useResearchStore.getState().status).toBe(fresh)
    expect(container.querySelector('[data-testid="research-legacy-migration"]')).toBeNull()
    expect(container.querySelector('[data-testid="research-segment"]')).not.toBeNull()
    expect(container.querySelector('[data-testid="research-quick-actions"]')).not.toBeNull()
  })

  it('renders the panel content when no legacy roots are reported', async () => {
    useResearchStore.getState().loadStatus(makeStatus([]), 'p1')
    const container = await render(<ResearchPanel />)

    expect(container.querySelector('[data-testid="research-legacy-migration"]')).toBeNull()
    expect(container.querySelector('[data-testid="research-segment"]')).not.toBeNull()
  })
})

describe('ResearchLegacyMigration — busy and error states', () => {
  it('keeps the button disabled while the RPC is in flight', async () => {
    useResearchStore.getState().loadStatus(makeStatus(['/root/.research']), 'p1')
    let resolveMigration!: (status: ResearchStatus) => void
    vi.mocked(migrateResearchRoots).mockImplementation(
      () =>
        new Promise<ResearchStatus>((resolve) => {
          resolveMigration = resolve
        }),
    )
    const container = await render(
      <ResearchLegacyMigration legacyRoots={['/root/.research']} projectId="p1" />,
    )

    await act(async () => {
      migrateButton(container).click()
    })

    const busyButton = migrateButton(container)
    expect(busyButton.disabled).toBe(true)
    expect(busyButton.textContent).toContain('Migrating')

    await act(async () => {
      resolveMigration(makeStatus())
    })

    expect(migrateButton(container).disabled).toBe(false)
  })

  it('renders the RPC error inline and allows a retry', async () => {
    useResearchStore.getState().loadStatus(makeStatus(['/root/.research']), 'p1')
    vi.mocked(migrateResearchRoots)
      .mockRejectedValueOnce(new Error('research exists and is not empty'))
      .mockResolvedValueOnce(makeStatus())
    const container = await render(
      <ResearchLegacyMigration legacyRoots={['/root/.research']} projectId="p1" />,
    )

    await act(async () => {
      migrateButton(container).click()
    })

    const error = container.querySelector('[data-testid="research-legacy-migration-error"]')
    expect(error?.textContent).toContain('research exists and is not empty')
    // The notice stays (nothing was migrated) and the button re-enables.
    expect(container.querySelector('[data-testid="research-legacy-migration"]')).not.toBeNull()
    expect(migrateButton(container).disabled).toBe(false)

    await act(async () => {
      migrateButton(container).click()
    })

    expect(migrateResearchRoots).toHaveBeenCalledTimes(2)
    expect(container.querySelector('[data-testid="research-legacy-migration-error"]')).toBeNull()
  })
})
