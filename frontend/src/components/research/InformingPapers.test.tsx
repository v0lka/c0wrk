// @vitest-environment jsdom
// Render tests for the reverse paper ← hypothesis projection's two faces:
// `InformingPapers` (the hypothesis card section) and `DanglingPaperLinks`
// (the research dashboard warning). Both are pure views over paperStore ×
// researchStore, so the tests seed the stores directly and assert rendering +
// the open-paper tab gesture.
import { describe, it, expect, beforeEach, afterEach } from 'vitest'
import { act } from 'react'
import { createRoot, type Root } from 'react-dom/client'

import { InformingPapers, DanglingPaperLinks } from './InformingPapers'
import { usePaperStore, paperTabPath } from '@/stores/paperStore'
import { useResearchStore } from '@/stores/researchStore'
import { useFileViewerStore } from '@/stores/fileViewerStore'
import type { PaperRecord } from '@/api/papers'
import type { ResearchStatus } from '@/types/models'

function makePaper(overrides: Partial<PaperRecord> & { id: string }): PaperRecord {
  return {
    slug: `slug-${overrides.id}`,
    title: `Title ${overrides.id}`,
    authors: [],
    year: 0,
    venue: '',
    identifiers: [],
    mode: '',
    reading: '',
    verdict: '',
    confidence: '',
    research_ids: [],
    anchors: [],
    claims: [],
    red_flags: [],
    uncertainties: [],
    dir: `/ws/.research/papers/${overrides.id}`,
    card_path: `papers/${overrides.id}/paper.md`,
    pinned: false,
    linked_research: [],
    ...overrides,
  }
}

function makeStatus(): ResearchStatus {
  return {
    enabled: true,
    project_id: 'p1',
    research_root: '/ws/.research',
    root: {
      path: '/ws/.research',
      index: [{ id: 'R-001', path: 'R-001/brief.md' }],
      active_project_id: 'R-001',
      projects: [
        {
          id: 'R-001',
          brief: { id: 'R-001', title: 'Test Research' },
          graph: {
            nodes: [
              { id: 'H-001', title: 'Root hypothesis', status: 'open' },
              { id: 'H-003', title: 'Third hypothesis', status: 'open' },
            ],
            edges: [],
          },
          metrics: {
            total: 2,
            by_status: { open: 2 },
            confirmation_rate: 0,
            depth: 1,
            breadth: 1,
          },
          prior_art_count: 0,
          has_report: false,
          log: [],
        },
      ],
    },
  }
}

function seedPapers(papers: PaperRecord[]): void {
  usePaperStore.getState().loadLibrary({
    project_id: 'p1',
    research_root: '/ws/.research',
    root: '/ws/.research/papers',
    papers,
    pinned: [],
  })
}

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

beforeEach(() => {
  usePaperStore.getState().reset()
  useResearchStore.getState().reset()
  useResearchStore.getState().loadStatus(makeStatus(), 'p1')
  useFileViewerStore.setState({ openTabs: [], activeFile: null, files: {}, collapsed: true })
})

afterEach(() => {
  if (activeRoot) {
    act(() => {
      activeRoot!.unmount()
    })
    activeRoot = null
  }
})

describe('InformingPapers', () => {
  it('lists the informing papers with a count and opens the paper tab on click', async () => {
    seedPapers([
      makePaper({ id: 'P-001', slug: 'vaswani-2017', title: 'Attention', research_ids: ['H-001'] }),
      makePaper({ id: 'P-002', slug: 'devlin-2019', title: 'BERT', research_ids: ['H-001'] }),
      makePaper({ id: 'P-003', slug: 'other', title: 'Unrelated', research_ids: ['H-003'] }),
    ])

    const container = await render(<InformingPapers hypothesisId="H-001" />)

    const section = container.querySelector('[data-testid="informing-papers"]')!
    expect(section.textContent).toContain('Informing papers (2)')
    expect(section.textContent).toContain('Attention')
    expect(section.textContent).toContain('BERT')
    // The unrelated paper (informs H-003) is not listed.
    expect(section.textContent).not.toContain('Unrelated')

    await act(async () => {
      container.querySelector<HTMLButtonElement>('[data-testid="informing-paper-P-002"]')!.click()
    })

    const viewer = useFileViewerStore.getState()
    expect(viewer.openTabs).toContain(paperTabPath('devlin-2019'))
    expect(viewer.activeFile).toBe(paperTabPath('devlin-2019'))
    expect(viewer.collapsed).toBe(false)
  })

  it('renders nothing when no paper informs the hypothesis', async () => {
    seedPapers([makePaper({ id: 'P-001', research_ids: ['H-003'] })])
    const container = await render(<InformingPapers hypothesisId="H-001" />)
    expect(container.querySelector('[data-testid="informing-papers"]')).toBeNull()
  })

  it('resolves an H-NNN spelling difference against the graph node', async () => {
    // The node is canonical H-003; the card wrote `H-3`.
    seedPapers([makePaper({ id: 'P-001', title: 'Spelt short', research_ids: ['H-3'] })])
    const container = await render(<InformingPapers hypothesisId="H-003" />)
    const section = container.querySelector('[data-testid="informing-papers"]')!
    expect(section.textContent).toContain('Informing papers (1)')
    expect(section.textContent).toContain('Spelt short')
  })
})

describe('DanglingPaperLinks', () => {
  it('surfaces a paper whose research_ids point to a nonexistent H-NNN', async () => {
    seedPapers([
      makePaper({ id: 'P-001', slug: 'known', title: 'Known', research_ids: ['H-001'] }),
      makePaper({ id: 'P-002', slug: 'orphan', title: 'Orphan', research_ids: ['H-999'] }),
    ])

    const container = await render(<DanglingPaperLinks />)

    const warning = container.querySelector('[data-testid="dangling-paper-links"]')!
    expect(warning.textContent).toContain('1 paper link')
    expect(warning.textContent).toContain('H-999')
    expect(warning.textContent).toContain('Orphan')

    await act(async () => {
      container
        .querySelector<HTMLButtonElement>('[data-testid="dangling-paper-P-002-H-999"]')!
        .click()
    })

    expect(useFileViewerStore.getState().openTabs).toContain(paperTabPath('orphan'))
  })

  it('renders nothing when every research_id resolves', async () => {
    seedPapers([makePaper({ id: 'P-001', research_ids: ['H-001'] })])
    const container = await render(<DanglingPaperLinks />)
    expect(container.querySelector('[data-testid="dangling-paper-links"]')).toBeNull()
  })
})
