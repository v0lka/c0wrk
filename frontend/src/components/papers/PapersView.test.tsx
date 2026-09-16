// @vitest-environment jsdom
// PapersView — the literature showcase + invocation surface.
//
// A pure view over paperStore (seeded directly — the library sync lives in the
// App-root usePapersEvents) plus the dispatch surface: the Study-paper field
// and the mode selector send the `study-paper` skill through the message
// sender, and each row's actions open the reader tab / deepen / compare /
// pin / propose a hypothesis.
import { afterEach, beforeEach, describe, expect, it, vi } from 'vitest'
import { act } from 'react'
import { createRoot, type Root } from 'react-dom/client'

import { PapersView } from './PapersView'
import {
  STUDY_PAPER_SKILL,
  RESEARCH_HYPOTHESIS_SKILL,
  buildStudyPrompt,
  buildDeepenPrompt,
  buildComparePrompt,
  buildCompareSelectedPrompt,
  buildProposeHypothesisPrompt,
} from './paperActions'
import type { PaperLibrary, PaperRecord } from '@/api/papers'
import { setPaperPinned } from '@/api/papers'
import { usePaperStore } from '@/stores/paperStore'
import { useProjectStore } from '@/stores/projectStore'
import { useFileViewerStore } from '@/stores/fileViewerStore'
import { PAPER_TAB_PREFIX } from '@/stores/paperStore'

const { sendMock } = vi.hoisted(() => ({ sendMock: vi.fn() }))
vi.mock('@/hooks/useMessageSender', () => ({
  useMessageSender: () => ({ send: sendMock, cancel: vi.fn(), isProcessing: false }),
}))
vi.mock('@/api/papers', () => ({
  getPapers: vi.fn(),
  getPaper: vi.fn(),
  setPaperPinned: vi.fn(() => Promise.resolve()),
}))

let activeRoot: Root | null = null

async function render(): Promise<HTMLElement> {
  const container = document.createElement('div')
  document.body.replaceChildren(container)
  const root = createRoot(container)
  activeRoot = root
  await act(async () => {
    root.render(<PapersView />)
  })
  return container
}

function makePaper(overrides: Partial<PaperRecord> = {}): PaperRecord {
  return {
    id: 'P-001',
    slug: 'vaswani-2017-attention',
    title: 'Attention Is All You Need',
    authors: ['Vaswani'],
    year: 2017,
    venue: 'NeurIPS',
    identifiers: [{ scheme: 'arxiv', value: '1706.03762' }],
    mode: 'skim',
    reading: '',
    verdict: 'accepted',
    confidence: 'high',
    research_ids: [],
    anchors: [],
    claims: [],
    red_flags: [],
    uncertainties: [],
    dir: '/root/.research/papers/vaswani-2017-attention',
    card_path: 'papers/vaswani-2017-attention/paper.md',
    pinned: false,
    linked_research: [],
    ...overrides,
  }
}

function makeLibrary(papers: PaperRecord[], projectId = 'p1'): PaperLibrary {
  return {
    project_id: projectId,
    research_root: '/root/.research',
    root: '/root/.research/papers',
    papers,
    pinned: [],
  }
}

function row(container: HTMLElement, paperId: string): HTMLElement {
  const el = container.querySelector<HTMLElement>(`[data-testid="paper-row"][data-paper-id="${paperId}"]`)
  expect(el).not.toBeNull()
  return el!
}

function action(container: HTMLElement, paperId: string, name: string): HTMLButtonElement {
  const el = row(container, paperId).querySelector<HTMLButtonElement>(`[data-testid="paper-action-${name}"]`)
  expect(el).not.toBeNull()
  return el!
}

/** Set a controlled <input> by aria-label via the native value setter so
 *  React's value tracker observes the change. */
function setInputValue(container: HTMLElement, label: string, value: string) {
  const field = container.querySelector<HTMLInputElement>(`input[aria-label="${label}"]`)!
  const proto = Object.getPrototypeOf(field) as {
    value: PropertyDescriptor & { set?: (v: string) => void }
  }
  Object.getOwnPropertyDescriptor(proto, 'value')?.set?.call(field, value)
  field.dispatchEvent(new Event('input', { bubbles: true }))
}

/** Choose a controlled <select> option by aria-label via the native value
 *  setter so React's value tracker observes the change. */
function setSelectValue(container: HTMLElement, label: string, value: string) {
  const field = container.querySelector<HTMLSelectElement>(`select[aria-label="${label}"]`)!
  const proto = Object.getPrototypeOf(field) as {
    value: PropertyDescriptor & { set?: (v: string) => void }
  }
  Object.getOwnPropertyDescriptor(proto, 'value')?.set?.call(field, value)
  field.dispatchEvent(new Event('change', { bubbles: true }))
}

beforeEach(() => {
  sendMock.mockReset()
  sendMock.mockResolvedValue(undefined)
  vi.mocked(setPaperPinned).mockClear()
  usePaperStore.getState().reset()
  useFileViewerStore.setState({ openTabs: [], activeFile: null, files: {} })
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

describe('PapersView — invocation surface', () => {
  it('dispatches the study-paper skill with the paste reference and clears the field', async () => {
    const container = await render()
    const study = container.querySelector<HTMLButtonElement>('[data-testid="papers-invoke-study"]')!

    // Empty field → the Study button is disabled.
    expect(study.disabled).toBe(true)

    await act(async () => {
      setInputValue(container, 'Study paper', '1706.03762')
    })
    expect(study.disabled).toBe(false)

    await act(async () => {
      study.click()
      await new Promise((r) => setTimeout(r, 0))
    })

    expect(sendMock).toHaveBeenCalledTimes(1)
    expect(sendMock).toHaveBeenCalledWith(
      buildStudyPrompt('1706.03762', 'auto'),
      [STUDY_PAPER_SKILL],
      undefined,
      undefined,
      { newSession: false },
    )
    const input = container.querySelector<HTMLInputElement>('[data-testid="papers-invoke-input"]')!
    expect(input.value).toBe('')
  })

  it('threads the selected mode into the dispatched prompt', async () => {
    const container = await render()
    const mode = container.querySelector<HTMLSelectElement>('[data-testid="papers-mode-select"]')!

    expect(mode.value).toBe('auto')
    await act(async () => {
      setSelectValue(container, 'Study mode', 'implement')
      setInputValue(container, 'Study paper', '10.1145/xyz')
    })

    await act(async () => {
      container.querySelector<HTMLButtonElement>('[data-testid="papers-invoke-study"]')!.click()
      await new Promise((r) => setTimeout(r, 0))
    })

    expect(sendMock).toHaveBeenCalledWith(
      buildStudyPrompt('10.1145/xyz', 'implement'),
      [STUDY_PAPER_SKILL],
      undefined,
      undefined,
      { newSession: false },
    )
  })

  it('routes a dispatch failure into the paper store error', async () => {
    sendMock.mockRejectedValue(new Error('runtime not ready'))
    const container = await render()

    await act(async () => {
      setInputValue(container, 'Study paper', '1706.03762')
    })
    await act(async () => {
      container.querySelector<HTMLButtonElement>('[data-testid="papers-invoke-study"]')!.click()
      await new Promise((r) => setTimeout(r, 0))
    })

    expect(usePaperStore.getState().error).toContain('Failed to dispatch')
    expect(usePaperStore.getState().error).toContain('runtime not ready')
  })
})

describe('PapersView — the paper list', () => {
  it('renders an empty state until a paper is studied', async () => {
    usePaperStore.getState().loadLibrary(makeLibrary([]))
    const container = await render()
    expect(container.querySelector('[data-testid="papers-empty"]')).not.toBeNull()
    expect(container.querySelectorAll('[data-testid="paper-row"]').length).toBe(0)
  })

  it('renders ALL of a card’s badges: mode, reading, verdict, confidence', async () => {
    // Acceptance path: a card written as `mode: review` + `reading: read
    // selectively` + `verdict: weak accept` arrives normalized (review /
    // selective / accepted) and must render every badge — the reading axis is
    // distinct from the soundness verdict.
    usePaperStore
      .getState()
      .loadLibrary(
        makeLibrary([
          makePaper({ mode: 'review', reading: 'selective', verdict: 'accepted', confidence: 'high' }),
          makePaper({ id: 'P-002', slug: 'bare', title: 'Bare Card', mode: '', reading: '', verdict: '', confidence: '' }),
        ]),
      )

    const container = await render()
    expect(container.querySelectorAll('[data-testid="paper-row"]').length).toBe(2)

    const first = row(container, 'P-001')
    expect(first.querySelector('[data-testid="paper-badge-mode"]')!.textContent).toBe('review')
    expect(first.querySelector('[data-testid="paper-badge-reading"]')!.textContent).toBe('selective')
    expect(first.querySelector('[data-testid="paper-badge-verdict"]')!.textContent).toBe('accepted')
    expect(first.querySelector('[data-testid="paper-badge-confidence"]')!.textContent).toBe('high')
    expect(first.textContent).toContain('Attention Is All You Need')
    expect(first.textContent).toContain('P-001')

    // A card with no appraisal yet renders no badges at all.
    const second = row(container, 'P-002')
    expect(second.querySelector('[data-testid="paper-badge-mode"]')).toBeNull()
    expect(second.querySelector('[data-testid="paper-badge-reading"]')).toBeNull()
    expect(second.querySelector('[data-testid="paper-badge-verdict"]')).toBeNull()
    expect(second.querySelector('[data-testid="paper-badge-confidence"]')).toBeNull()
  })

  it('shows a no-project hint when no project is active', async () => {
    useProjectStore.setState({ projects: null, activeProjectId: null, lastRealProjectId: null })
    const container = await render()
    expect(container.querySelector('[data-testid="papers-no-project"]')).not.toBeNull()
  })
})

describe('PapersView — row actions', () => {
  beforeEach(() => {
    usePaperStore.getState().loadLibrary(makeLibrary([makePaper()]))
  })

  it('clicking a paper opens its reader tab', async () => {
    const container = await render()
    await act(async () => {
      row(container, 'P-001').querySelector<HTMLButtonElement>('[data-testid="paper-open"]')!.click()
      await new Promise((r) => setTimeout(r, 0))
    })

    const viewer = useFileViewerStore.getState()
    expect(viewer.openTabs).toContain(`${PAPER_TAB_PREFIX}vaswani-2017-attention`)
    expect(viewer.activeFile).toBe(`${PAPER_TAB_PREFIX}vaswani-2017-attention`)
  })

  it('the Open action opens the same reader tab', async () => {
    const container = await render()
    await act(async () => {
      action(container, 'P-001', 'open').click()
      await new Promise((r) => setTimeout(r, 0))
    })
    expect(useFileViewerStore.getState().openTabs).toContain(
      `${PAPER_TAB_PREFIX}vaswani-2017-attention`,
    )
  })

  it('Deepen dispatches the study-paper skill scoped to the card', async () => {
    const container = await render()
    await act(async () => {
      action(container, 'P-001', 'deepen').click()
      await new Promise((r) => setTimeout(r, 0))
    })
    expect(sendMock).toHaveBeenCalledWith(
      buildDeepenPrompt(makePaper()),
      [STUDY_PAPER_SKILL],
      undefined,
      undefined,
      { newSession: false },
    )
  })

  it('Shift+Deepen dispatches into a new session', async () => {
    const container = await render()
    await act(async () => {
      action(container, 'P-001', 'deepen').dispatchEvent(
        new MouseEvent('click', { bubbles: true, shiftKey: true }),
      )
      await new Promise((r) => setTimeout(r, 0))
    })
    expect(sendMock).toHaveBeenCalledWith(
      buildDeepenPrompt(makePaper()),
      [STUDY_PAPER_SKILL],
      undefined,
      undefined,
      { newSession: true },
    )
  })

  it('Compare dispatches the study-paper skill', async () => {
    const container = await render()
    await act(async () => {
      action(container, 'P-001', 'compare').click()
      await new Promise((r) => setTimeout(r, 0))
    })
    expect(sendMock).toHaveBeenCalledWith(
      buildComparePrompt(makePaper()),
      [STUDY_PAPER_SKILL],
      undefined,
      undefined,
      { newSession: false },
    )
  })

  it('Propose hypothesis dispatches the research-hypothesis skill', async () => {
    const container = await render()
    await act(async () => {
      action(container, 'P-001', 'propose').click()
      await new Promise((r) => setTimeout(r, 0))
    })
    expect(sendMock).toHaveBeenCalledWith(
      buildProposeHypothesisPrompt(makePaper()),
      [RESEARCH_HYPOTHESIS_SKILL],
      undefined,
      undefined,
      { newSession: false },
    )
  })

  it('threads the paper gaps into the dispatched proposal prompt', async () => {
    usePaperStore
      .getState()
      .loadPaper(makePaper({ uncertainties: [{ item: 'Number of seeds', detail: 'not reported' }] }))
    const container = await render()
    await act(async () => {
      action(container, 'P-001', 'propose').click()
      await new Promise((r) => setTimeout(r, 0))
    })
    const [prompt] = sendMock.mock.calls[0]!
    expect(String(prompt)).toContain('- Number of seeds — not reported')
  })

  it('Go deeper dispatches one mode deeper (skim → review)', async () => {
    const container = await render()
    await act(async () => {
      action(container, 'P-001', 'deepen').click()
      await new Promise((r) => setTimeout(r, 0))
    })
    const [prompt] = sendMock.mock.calls[0]!
    expect(String(prompt)).toContain('use review mode')
    expect(String(prompt)).toContain(makePaper().card_path)
  })

  it('Suggest hypotheses from gaps auto-pins the paper as prior art', async () => {
    const container = await render()
    await act(async () => {
      action(container, 'P-001', 'propose').click()
      await new Promise((r) => setTimeout(r, 0))
    })
    expect(setPaperPinned).toHaveBeenCalledWith('p1', 'P-001', true)
    expect(usePaperStore.getState().pinned).toContain('papers/vaswani-2017-attention/paper.md')
  })

  it('never re-pins an already-pinned paper (idempotent)', async () => {
    usePaperStore.getState().loadPaper(makePaper({ pinned: true }))
    const container = await render()
    await act(async () => {
      action(container, 'P-001', 'propose').click()
      await new Promise((r) => setTimeout(r, 0))
    })
    // No redundant RPC, and the pinned list still carries the card exactly once.
    expect(setPaperPinned).not.toHaveBeenCalled()
    const cardPath = 'papers/vaswani-2017-attention/paper.md'
    expect(usePaperStore.getState().pinned.filter((p) => p === cardPath)).toHaveLength(1)
  })

  it('Pin pins the paper and folds the pin into the store', async () => {
    const container = await render()
    await act(async () => {
      action(container, 'P-001', 'pin').click()
      await new Promise((r) => setTimeout(r, 0))
    })

    expect(setPaperPinned).toHaveBeenCalledWith('p1', 'P-001', true)
    const state = usePaperStore.getState()
    expect(state.pinned).toContain('papers/vaswani-2017-attention/paper.md')
    expect(state.records['P-001']!.pinned).toBe(true)
  })
})

describe('PapersView — multi-select comparison', () => {
  const p1 = makePaper()
  const p2 = makePaper({
    id: 'P-002',
    slug: 'sutskever-2014-sequence',
    title: 'Sequence to Sequence Learning',
    identifiers: [{ scheme: 'arxiv', value: '1409.3215' }],
  })

  beforeEach(() => {
    usePaperStore.getState().loadLibrary(makeLibrary([p1, p2]))
  })

  function checkbox(container: HTMLElement, paperId: string): HTMLInputElement {
    const box = row(container, paperId).querySelector<HTMLInputElement>('[data-testid="paper-select"]')
    expect(box).not.toBeNull()
    return box!
  }

  function compareButton(container: HTMLElement): HTMLButtonElement {
    return container.querySelector<HTMLButtonElement>('[data-testid="papers-compare-selected"]')!
  }

  it('renders a selection checkbox per row and disables Compare below two', async () => {
    const container = await render()
    expect(container.querySelectorAll('[data-testid="paper-select"]')).toHaveLength(2)
    expect(container.querySelector('[data-testid="papers-selection-count"]')!.textContent).toBe(
      '0 selected',
    )
    expect(compareButton(container).disabled).toBe(true)
    expect(container.querySelector('[data-testid="papers-selection-hint"]')).not.toBeNull()

    // One paper is still not enough.
    await act(async () => {
      checkbox(container, 'P-001').click()
    })
    expect(container.querySelector('[data-testid="papers-selection-count"]')!.textContent).toBe(
      '1 selected',
    )
    expect(compareButton(container).disabled).toBe(true)
  })

  it('enables Compare at two and dispatches the selected papers’ identifiers', async () => {
    const container = await render()
    await act(async () => {
      checkbox(container, 'P-001').click()
      checkbox(container, 'P-002').click()
    })
    expect(compareButton(container).disabled).toBe(false)
    expect(container.querySelector('[data-testid="papers-selection-hint"]')).toBeNull()

    await act(async () => {
      compareButton(container).click()
      await new Promise((r) => setTimeout(r, 0))
    })

    expect(sendMock).toHaveBeenCalledTimes(1)
    expect(sendMock).toHaveBeenCalledWith(
      buildCompareSelectedPrompt([p1, p2], '/root/.research'),
      [STUDY_PAPER_SKILL],
      undefined,
      undefined,
      { newSession: false },
    )
    // The dispatch clears the selection so the gesture cannot repeat by accident.
    expect(container.querySelector('[data-testid="papers-selection-count"]')!.textContent).toBe(
      '0 selected',
    )
    expect(compareButton(container).disabled).toBe(true)
  })

  it('Shift+Compare selected dispatches into a new session', async () => {
    const container = await render()
    await act(async () => {
      checkbox(container, 'P-001').click()
      checkbox(container, 'P-002').click()
    })
    await act(async () => {
      compareButton(container).dispatchEvent(new MouseEvent('click', { bubbles: true, shiftKey: true }))
      await new Promise((r) => setTimeout(r, 0))
    })
    expect(sendMock).toHaveBeenCalledWith(
      buildCompareSelectedPrompt([p1, p2], '/root/.research'),
      [STUDY_PAPER_SKILL],
      undefined,
      undefined,
      { newSession: true },
    )
  })

  it('Clear empties the selection', async () => {
    const container = await render()
    await act(async () => {
      checkbox(container, 'P-001').click()
      checkbox(container, 'P-002').click()
    })
    await act(async () => {
      container.querySelector<HTMLButtonElement>('[data-testid="papers-selection-clear"]')!.click()
    })
    expect(container.querySelector('[data-testid="papers-selection-count"]')!.textContent).toBe(
      '0 selected',
    )
    expect(compareButton(container).disabled).toBe(true)
  })

  it('drops a selected id when the paper leaves the library', async () => {
    const container = await render()
    await act(async () => {
      checkbox(container, 'P-001').click()
      checkbox(container, 'P-002').click()
    })
    expect(container.querySelector('[data-testid="papers-selection-count"]')!.textContent).toBe(
      '2 selected',
    )
    // P-002 is deleted from the library (e.g. a card removed on disk).
    await act(async () => {
      usePaperStore.getState().loadLibrary(makeLibrary([p1]))
    })
    expect(container.querySelector('[data-testid="papers-selection-count"]')!.textContent).toBe(
      '1 selected',
    )
    expect(compareButton(container).disabled).toBe(true)
  })
})
