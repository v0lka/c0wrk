// @vitest-environment jsdom
// ResearchLog — the dashboard's log list. Covers [20]b: long append-only
// logs render capped with a "show all" expansion instead of one DOM node
// per entry.
import { afterEach, beforeEach, describe, expect, it } from 'vitest'
import { act } from 'react'
import { createRoot, type Root } from 'react-dom/client'

import { ResearchLog } from './ResearchLog'
import { RESEARCH_LOG_RENDER_CAP } from './researchLogUtils'
import { useResearchStore } from '@/stores/researchStore'
import type { ResearchStatus } from '@/types/models'

let activeRoot: Root | null = null

async function renderLog(): Promise<HTMLElement> {
  const container = document.createElement('div')
  document.body.replaceChildren(container)
  const root = createRoot(container)
  activeRoot = root
  await act(async () => {
    root.render(<ResearchLog />)
  })
  return container
}

/** Seed the active project's log with `count` append-only entries. */
function seedLog(count: number): void {
  const status = {
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
          brief: { id: 'R-001', title: 'T' },
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
          log: Array.from({ length: count }, (_, i) => ({
            id: i + 1,
            kind: 'note',
            created_at: '2026-01-01T00:00:00Z',
            message: `entry ${i + 1}`,
          })),
        },
      ],
    },
  } as unknown as ResearchStatus
  useResearchStore.getState().reset()
  useResearchStore.getState().loadStatus(status, 'p1')
}

afterEach(() => {
  if (activeRoot) {
    act(() => {
      activeRoot!.unmount()
    })
    activeRoot = null
  }
})

describe('ResearchLog — render cap + expansion ([20]b)', () => {
  beforeEach(() => {
    useResearchStore.getState().reset()
  })

  it('renders a short log in full (no expansion button)', async () => {
    seedLog(3)
    const container = await renderLog()

    expect(container.querySelectorAll('[data-testid="research-log-entry"]')).toHaveLength(3)
    expect(container.querySelector('[data-testid="research-log-show-all"]')).toBeNull()
  })

  it(`caps at ${RESEARCH_LOG_RENDER_CAP} entries and expands on demand`, async () => {
    seedLog(RESEARCH_LOG_RENDER_CAP + 50)
    const container = await renderLog()

    // Capped: only the newest 100 entries render…
    expect(container.querySelectorAll('[data-testid="research-log-entry"]')).toHaveLength(
      RESEARCH_LOG_RENDER_CAP,
    )
    // …with the newest first.
    const first = container.querySelector('[data-testid="research-log-entry"]')
    expect(first?.textContent).toContain(`entry ${RESEARCH_LOG_RENDER_CAP + 50}`)

    // The expansion names the full count…
    const showAll = container.querySelector<HTMLButtonElement>(
      '[data-testid="research-log-show-all"]',
    )!
    expect(showAll.textContent).toContain(`Show all ${RESEARCH_LOG_RENDER_CAP + 50} entries`)

    // …and expands to the whole log on click.
    await act(async () => {
      showAll.click()
    })
    expect(container.querySelectorAll('[data-testid="research-log-entry"]')).toHaveLength(
      RESEARCH_LOG_RENDER_CAP + 50,
    )
    expect(container.querySelector('[data-testid="research-log-show-all"]')).toBeNull()
  })
})
