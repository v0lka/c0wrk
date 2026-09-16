// @vitest-environment jsdom
//
// PaperWorkspace — the paper viewer tab: section routing, the critical-layer
// widgets, and the E1 anchor navigation (a resolved anchor switches to the
// Source section and scrolls; an unresolved one shows "не найдено" and never
// moves).

import { describe, it, expect, vi, beforeEach, afterEach } from 'vitest'
import { act } from 'react'
import { createRoot, type Root } from 'react-dom/client'

const { sendMock } = vi.hoisted(() => ({ sendMock: vi.fn() }))
vi.mock('@/api/papers', () => ({
  getPapers: vi.fn(),
  getPaper: vi.fn(),
  setPaperPinned: vi.fn(() => Promise.resolve()),
}))
vi.mock('@/hooks/useMessageSender', () => ({
  useMessageSender: () => ({ send: sendMock, cancel: vi.fn(), isProcessing: false }),
}))

// The artifact loader is mocked so each test pins the on-disk state it needs.
const { artifactsHolder } = vi.hoisted(() => ({
  artifactsHolder: { current: null as unknown },
}))
vi.mock('./usePaperArtifacts', () => ({
  usePaperArtifacts: () => artifactsHolder.current,
}))

// The research-root comparisons loader is mocked the same way, so each test
// pins which library comparisons (if any) the paper takes part in.
const { comparisonsHolder } = vi.hoisted(() => ({
  comparisonsHolder: { current: null as unknown },
}))
vi.mock('./useComparisons', () => ({
  useComparisons: () => comparisonsHolder.current,
}))

// The Markdown renderer pulls in the full react-markdown stack; a stub keeps
// the test focused on the workspace's own logic.
vi.mock('@/lib/markdownConfig', () => ({
  Markdown: ({ content }: { content: string }) => (
    <div data-testid="paper-markdown">{content}</div>
  ),
}))

import { PaperWorkspace } from './PaperWorkspace'
import type { PaperArtifacts, PaperArtifact, PaperSectionId } from './usePaperArtifacts'
import { usePaperStore } from '@/stores/paperStore'
import { setPaperPinned, type PaperRecord } from '@/api/papers'
import {
  STUDY_PAPER_SKILL,
  RESEARCH_HYPOTHESIS_SKILL,
  buildDeepenPrompt,
  buildProposeHypothesisPrompt,
} from './paperActions'

const SECTION_IDS: PaperSectionId[] = [
  'note',
  'appraisal',
  'compare',
  'flashcards',
  'source',
  'literature',
]

function absent(): PaperArtifact {
  return { fileName: '', content: '', loading: false, missing: true, error: null }
}

function artifactsOf(
  overrides: Partial<Record<PaperSectionId, Partial<PaperArtifact>>> = {},
): PaperArtifacts {
  const out = {} as PaperArtifacts
  for (const id of SECTION_IDS) out[id] = absent()
  for (const [key, value] of Object.entries(overrides)) {
    const id = key as PaperSectionId
    out[id] = { ...out[id], ...value }
  }
  return out
}

const NOTE = `## 5. Contribution → evidence matrix

| Contribution | Claim the evidence is meant to support | Evidence offered (experiment / result / proof) | Anchor | Evidence strength (present / weak / absent) |
| - | - | - | - | - |
| C1 | Sparse attention matches dense quality | WMT14 | Table 2 | present |
| C2 | Trains 2x faster | curves | Fig 3 | weak |
| C3 | Generalizes | - | - | absent |

| Flag | Detail | Severity |
| - | - | - |
| No error bars | single seed | high |
| Weak baseline | old model | medium |
| Tiny dev set | 500 ex | low |
| Extra | cosmetic | low |

| Item | Detail |
| - | - |
| Number of seeds | not reported |
`

const SOURCE = `# Paper Title

## 3 Method

Body line.

## 4 Results

More body.
`

function paperRecord(overrides: Partial<PaperRecord> = {}): PaperRecord {
  return {
    id: 'P-001',
    slug: 'demo',
    title: 'Demo Paper',
    authors: ['A. Author'],
    year: 2024,
    venue: 'Journal',
    identifiers: [],
    mode: 'deep',
    reading: '',
    verdict: 'accepted',
    confidence: 'high',
    research_ids: [],
    anchors: [
      { label: 'sec3', ref: '§3', note: '' },
      { label: 'sec9', ref: '§9', note: '' },
    ],
    claims: [],
    red_flags: [],
    uncertainties: [],
    dir: '/ws/.research/papers/demo',
    card_path: 'papers/demo/paper.md',
    pinned: false,
    linked_research: [],
    ...overrides,
  }
}

function seed(paper: PaperRecord | null): void {
  if (paper === null) {
    usePaperStore.getState().reset()
    return
  }
  usePaperStore.getState().loadLibrary({
    project_id: 'p1',
    research_root: '/ws/.research',
    root: '/ws/.research/papers',
    papers: [paper],
    pinned: [],
  })
}

let root: Root | null = null
let container: HTMLDivElement | null = null

function render(slug = 'demo'): void {
  container = document.createElement('div')
  document.body.appendChild(container)
  root = createRoot(container)
  act(() => {
    root!.render(<PaperWorkspace slug={slug} />)
  })
}

function click(selector: string): void {
  const el = document.querySelector<HTMLElement>(selector)
  expect(el).not.toBeNull()
  act(() => {
    el!.dispatchEvent(new MouseEvent('click', { bubbles: true }))
  })
}

/** Click and let the dispatched async work (send / auto-pin) settle. */
async function clickAsync(selector: string): Promise<void> {
  const el = document.querySelector<HTMLElement>(selector)
  expect(el).not.toBeNull()
  await act(async () => {
    el!.dispatchEvent(new MouseEvent('click', { bubbles: true }))
    await new Promise((r) => setTimeout(r, 0))
  })
}

const activeSection = (): string | null | undefined =>
  document.querySelector('[data-testid="paper-sections"] [data-active="true"]')?.getAttribute(
    'data-testid',
  )

describe('PaperWorkspace', () => {
  beforeEach(() => {
    // jsdom does not implement scrollIntoView; stub it so the scroll path runs.
    Element.prototype.scrollIntoView = vi.fn()
    sendMock.mockReset()
    sendMock.mockResolvedValue(undefined)
    vi.mocked(setPaperPinned).mockClear()
    seed(paperRecord())
    artifactsHolder.current = artifactsOf()
    comparisonsHolder.current = { dir: '', loading: false, items: [] }
  })

  afterEach(() => {
    act(() => {
      root?.unmount()
    })
    container?.remove()
    root = null
    container = null
    usePaperStore.getState().reset()
  })

  it('renders the header, every section tab, and the overview by default', () => {
    render()
    expect(document.querySelector('[data-testid="paper-workspace"]')).not.toBeNull()
    for (const id of ['overview', 'note', 'appraisal', 'compare', 'flashcards', 'source', 'literature']) {
      expect(document.querySelector(`[data-testid="paper-section-${id}"]`)).not.toBeNull()
    }
    expect(activeSection()).toBe('paper-section-overview')
    expect(document.querySelector('[data-testid="paper-identity"]')).not.toBeNull()
  })

  it('renders the reading badge alongside mode/verdict/confidence in the overview', () => {
    // The reading decision is a separate axis from the soundness verdict, so the
    // overview shows both badges (acceptance: mode: review + reading: selective
    // + verdict: accepted renders every badge).
    seed(paperRecord({ mode: 'review', reading: 'selective', verdict: 'accepted', confidence: 'high' }))
    render()
    expect(document.querySelector('[data-testid="paper-reading"]')?.textContent).toBe('selective')
    expect(document.querySelector('[data-testid="paper-mode"]')?.textContent).toBe('review')
    expect(document.querySelector('[data-testid="paper-verdict"]')?.textContent).toBe('accepted')
    expect(document.querySelector('[data-testid="paper-confidence"]')?.textContent).toBe('high')
  })

  it('renders an honest empty state when the slug is not in the library', () => {
    seed(null)
    render('missing')
    expect(document.querySelector('[data-testid="paper-not-found"]')).not.toBeNull()
  })

  it('renders the critical layer as widgets (matrix strengths, ≤3 red flags, uncertainty)', () => {
    artifactsHolder.current = artifactsOf({
      note: { fileName: 'note.md', content: NOTE, loading: false, missing: false, error: null },
    })
    render()
    const rows = document.querySelectorAll('[data-testid="paper-evidence-row"]')
    expect(rows).toHaveLength(3)
    const strengths = Array.from(
      document.querySelectorAll('[data-testid="paper-evidence-strength"]'),
    ).map((el) => el.getAttribute('data-strength'))
    expect(strengths).toEqual(['present', 'weak', 'absent'])
    // The four parsed red flags are capped at three.
    expect(document.querySelectorAll('[data-testid="paper-red-flag-chip"]')).toHaveLength(3)
    expect(document.querySelectorAll('[data-testid="paper-uncertainty-chip"]')).toHaveLength(1)
  })

  it('falls back to the record when no artifact carries a critical layer', () => {
    seed(
      paperRecord({
        red_flags: [{ flag: 'Record flag', detail: 'd', severity: 'high' }],
        uncertainties: [{ item: 'Record uncertainty', detail: '' }],
      }),
    )
    render()
    expect(document.querySelector('[data-testid="paper-red-flag-chip"]')?.textContent).toContain(
      'Record flag',
    )
    expect(
      document.querySelector('[data-testid="paper-uncertainty-chip"]')?.textContent,
    ).toContain('Record uncertainty')
  })

  it('switches sections and renders the artifact markdown', () => {
    artifactsHolder.current = artifactsOf({
      appraisal: {
        fileName: 'appraisal.md',
        content: '# Appraisal body',
        loading: false,
        missing: false,
        error: null,
      },
    })
    render()
    click('[data-testid="paper-section-appraisal"]')
    expect(activeSection()).toBe('paper-section-appraisal')
    expect(document.querySelector('[data-testid="paper-appraisal-markdown"]')?.textContent).toContain(
      'Appraisal body',
    )
  })

  it('shows the empty state for a section with no artifact', () => {
    render()
    click('[data-testid="paper-section-note"]')
    expect(document.querySelector('[data-testid="paper-note-empty"]')).not.toBeNull()
  })

  it('renders the source lines in the Источник section', () => {
    artifactsHolder.current = artifactsOf({
      source: { fileName: 'source.md', content: SOURCE, loading: false, missing: false, error: null },
    })
    render()
    click('[data-testid="paper-section-source"]')
    expect(document.querySelectorAll('[data-paper-line]')).toHaveLength(SOURCE.split('\n').length)
  })

  it('navigates to a resolved anchor: switches to Источник and scrolls', () => {
    artifactsHolder.current = artifactsOf({
      source: { fileName: 'source.md', content: SOURCE, loading: false, missing: false, error: null },
    })
    render()
    click('[data-testid="paper-anchor"][data-anchor-index="0"]')
    expect(activeSection()).toBe('paper-section-source')
    expect(Element.prototype.scrollIntoView).toHaveBeenCalled()
  })

  it('degrades honestly: an unresolvable anchor shows "не найдено" and never moves', () => {
    artifactsHolder.current = artifactsOf({
      source: { fileName: 'source.md', content: SOURCE, loading: false, missing: false, error: null },
    })
    render()
    // Anchor index 1 is "§9" — the source has no section 9.
    click('[data-testid="paper-anchor"][data-anchor-index="1"]')
    expect(activeSection()).toBe('paper-section-overview')
    expect(document.querySelector('[data-testid="paper-anchor-miss"]')?.textContent).toBe('не найдено')
    expect(Element.prototype.scrollIntoView).not.toHaveBeenCalled()
  })

  it('degrades honestly when the source text is missing', () => {
    render()
    click('[data-testid="paper-anchor"][data-anchor-index="0"]')
    expect(activeSection()).toBe('paper-section-overview')
    expect(document.querySelector('[data-testid="paper-anchor-miss"]')).not.toBeNull()
  })
})

describe('PaperWorkspace — deepening & hypothesis bridge (E4/E5)', () => {
  beforeEach(() => {
    // Mirror the outer suite's fixture: stub scrollIntoView, seed one paper and
    // reset the dispatch / pin spies.
    Element.prototype.scrollIntoView = vi.fn()
    sendMock.mockReset()
    sendMock.mockResolvedValue(undefined)
    vi.mocked(setPaperPinned).mockClear()
    seed(paperRecord())
    artifactsHolder.current = artifactsOf()
  })

  afterEach(() => {
    act(() => {
      root?.unmount()
    })
    container?.remove()
    root = null
    container = null
    usePaperStore.getState().reset()
  })

  it('renders the "Go deeper" and "Suggest hypotheses from gaps" actions', () => {
    render()
    expect(document.querySelector('[data-testid="paper-go-deeper"]')).not.toBeNull()
    expect(document.querySelector('[data-testid="paper-suggest-hypotheses"]')).not.toBeNull()
  })

  it('Go deeper dispatches study-paper one mode deeper on the same card', async () => {
    render()
    await clickAsync('[data-testid="paper-go-deeper"]')
    expect(sendMock).toHaveBeenCalledWith(buildDeepenPrompt(paperRecord()), [STUDY_PAPER_SKILL])
  })

  it('Suggest hypotheses from gaps dispatches research-hypothesis AND auto-pins the paper', async () => {
    render()
    await clickAsync('[data-testid="paper-suggest-hypotheses"]')
    expect(sendMock).toHaveBeenCalledWith(buildProposeHypothesisPrompt(paperRecord()), [
      RESEARCH_HYPOTHESIS_SKILL,
    ])
    expect(setPaperPinned).toHaveBeenCalledWith('p1', 'P-001', true)
  })

  it('keeps every previous section when a deepen appends a new one (round-trip)', () => {
    artifactsHolder.current = artifactsOf({
      note: { fileName: 'note.md', content: 'Note v1', loading: false, missing: false, error: null },
      appraisal: {
        fileName: 'appraisal.md',
        content: 'Appraisal v1',
        loading: false,
        missing: false,
        error: null,
      },
      literature: {
        fileName: 'literature.md',
        content: 'Literature appended',
        loading: false,
        missing: false,
        error: null,
      },
    })
    render()
    // The sections written before the deepen are still present…
    click('[data-testid="paper-section-note"]')
    expect(document.querySelector('[data-testid="paper-note-markdown"]')?.textContent).toContain(
      'Note v1',
    )
    click('[data-testid="paper-section-appraisal"]')
    expect(
      document.querySelector('[data-testid="paper-appraisal-markdown"]')?.textContent,
    ).toContain('Appraisal v1')
    // …and the newly appended section is built out on top of them.
    click('[data-testid="paper-section-literature"]')
    expect(
      document.querySelector('[data-testid="paper-literature-markdown"]')?.textContent,
    ).toContain('Literature appended')
  })
})

describe('PaperWorkspace — Compare section (multi-paper comparisons)', () => {
  // A comparison artifact the study-paper skill writes to the research root;
  // it names P-001 (this paper "demo") in its papers table.
  const COMPARISON = `# Paper Comparison Matrix — sparse vs dense attention

## 1. Papers under comparison

| ID | Short name | Citation / identifier | One-line summary [Paper] |
| --- | --- | --- | --- |
| P-001 | Demo | arxiv:1706.03762 | dense baseline [Paper] |
| P-002 | Sparse | arxiv:2004.05150 | sparse variant [Paper] |

## 2. Comparison matrix

| Dimension | P-001 | P-002 |
| --- | --- | --- |
| Problem framing | dense attention | sparse attention |
| Evidence strength (present / weak / absent) | present | weak |
`

  const OTHER = `# Paper Comparison Matrix — unrelated

## 1. Papers under comparison

| ID | Short name | Citation / identifier | One-line summary [Paper] |
| --- | --- | --- | --- |
| P-900 | Zzz | arxiv:1 | unrelated [Paper] |
`

  beforeEach(() => {
    Element.prototype.scrollIntoView = vi.fn()
    seed(paperRecord())
    artifactsHolder.current = artifactsOf()
    comparisonsHolder.current = { dir: '', loading: false, items: [] }
  })

  afterEach(() => {
    act(() => {
      root?.unmount()
    })
    container?.remove()
    root = null
    container = null
    usePaperStore.getState().reset()
  })

  it('renders the comparison matrix for a library comparison that names the paper', () => {
    comparisonsHolder.current = {
      dir: '/ws/.research/comparisons',
      loading: false,
      items: [{ slug: 'demo-vs-sparse', fileName: 'demo-vs-sparse.md', content: COMPARISON, error: null }],
    }
    render()
    click('[data-testid="paper-section-compare"]')
    expect(document.querySelector('[data-testid="paper-compare-comparisons"]')).not.toBeNull()
    const matrix = document.querySelector('[data-testid="comparison-matrix"]')
    expect(matrix).not.toBeNull()
    expect(matrix!.getAttribute('data-slug')).toBe('demo-vs-sparse')
    expect(document.querySelector('[data-testid="comparison-topic"]')?.textContent).toContain(
      'sparse vs dense attention',
    )
    expect(document.querySelectorAll('[data-testid="comparison-grid-row"]')).toHaveLength(2)
  })

  it('ignores a comparison that does not name the paper (falls back to the empty state)', () => {
    comparisonsHolder.current = {
      dir: '/ws/.research/comparisons',
      loading: false,
      items: [{ slug: 'other', fileName: 'other.md', content: OTHER, error: null }],
    }
    render()
    click('[data-testid="paper-section-compare"]')
    expect(document.querySelector('[data-testid="comparison-matrix"]')).toBeNull()
    expect(document.querySelector('[data-testid="paper-compare-empty"]')).not.toBeNull()
  })

  it('falls back to the per-paper comparison artifact when no library comparison applies', () => {
    comparisonsHolder.current = { dir: '/ws/.research/comparisons', loading: false, items: [] }
    artifactsHolder.current = artifactsOf({
      compare: {
        fileName: 'comparison.md',
        content: '# Per-paper comparison',
        loading: false,
        missing: false,
        error: null,
      },
    })
    render()
    click('[data-testid="paper-section-compare"]')
    expect(document.querySelector('[data-testid="paper-compare-markdown"]')?.textContent).toContain(
      'Per-paper comparison',
    )
  })

  it('shows the loading state while the comparisons directory is read', () => {
    comparisonsHolder.current = { dir: '/ws/.research/comparisons', loading: true, items: [] }
    render()
    click('[data-testid="paper-section-compare"]')
    expect(document.querySelector('[data-testid="paper-compare-loading"]')).not.toBeNull()
  })
})
