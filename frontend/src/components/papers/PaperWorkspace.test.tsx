// @vitest-environment jsdom
//
// PaperWorkspace — the paper viewer tab: section routing, the critical-layer
// widgets, and the E1 anchor navigation (a resolved anchor switches to the
// Source section and scrolls; an unresolved one shows "not found" and never
// moves).

import { describe, it, expect, vi, beforeEach, afterEach } from 'vitest'
import { act } from 'react'
import { createRoot, type Root } from 'react-dom/client'

vi.mock('@/api/papers', () => ({
  getPapers: vi.fn(),
  getPaper: vi.fn(),
  setPaperPinned: vi.fn(() => Promise.resolve()),
}))
// The artifact loader: most tests pin the on-disk state through `artifactsHolder`
// (set before render). The "round-trip" test clears it to drive the REAL loader
// (against the mocked workspace RPCs below), so a refresh-key bump re-runs the
// load — the regression it guards.
const { artifactsHolder } = vi.hoisted(() => ({
  artifactsHolder: { current: null as unknown },
}))
vi.mock('@/api/workspace', () => ({
  // Pending by default: with the holder override in place the real loader's
  // probe never resolves, so it schedules no state update outside act.
  listDirectory: vi.fn(() => new Promise(() => {})),
  readFile: vi.fn(() => new Promise(() => {})),
}))
vi.mock('./usePaperArtifacts', async () => {
  const actual = await vi.importActual<typeof import('./usePaperArtifacts')>('./usePaperArtifacts')
  return {
    ...actual,
    // Invoke the real loader unconditionally (hooks rules) and prefer a test's
    // pinned artifacts when it supplied them.
    usePaperArtifacts: (dir: string, refreshKey?: number): PaperArtifacts => {
      const loaded = actual.usePaperArtifacts(dir, refreshKey)
      return (artifactsHolder.current ?? loaded) as PaperArtifacts
    },
  }
})

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
import { listDirectory, readFile } from '@/api/workspace'
import { usePaperStore } from '@/stores/paperStore'
import type { PaperRecord } from '@/api/papers'

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

/** Let the (real) artifact loader's mocked workspace RPCs settle. */
async function flushArtifacts(): Promise<void> {
  await act(async () => {
    await new Promise((r) => setTimeout(r, 0))
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

  it('exposes the active section to assistive tech via aria-current', () => {
    render()
    const current = (): string | null | undefined =>
      document
        .querySelector('[data-testid="paper-sections"] [aria-current="true"]')
        ?.getAttribute('data-testid')
    expect(document.querySelector('[data-testid="paper-section-note"]')?.getAttribute('aria-current')).toBeNull()
    expect(current()).toBe('paper-section-overview')

    click('[data-testid="paper-section-note"]')
    expect(current()).toBe('paper-section-note')
    expect(
      document.querySelector('[data-testid="paper-section-overview"]')?.getAttribute('aria-current'),
    ).toBeNull()
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

  it('renders the source lines in the Source section', () => {
    artifactsHolder.current = artifactsOf({
      source: { fileName: 'source.md', content: SOURCE, loading: false, missing: false, error: null },
    })
    render()
    click('[data-testid="paper-section-source"]')
    expect(document.querySelectorAll('[data-paper-line]')).toHaveLength(SOURCE.split('\n').length)
  })

  it('navigates to a resolved anchor: switches to Source and scrolls', () => {
    artifactsHolder.current = artifactsOf({
      source: { fileName: 'source.md', content: SOURCE, loading: false, missing: false, error: null },
    })
    render()
    click('[data-testid="paper-anchor"][data-anchor-index="0"]')
    expect(activeSection()).toBe('paper-section-source')
    expect(Element.prototype.scrollIntoView).toHaveBeenCalled()
  })

  it('degrades honestly: an unresolvable anchor shows "not found" and never moves', () => {
    artifactsHolder.current = artifactsOf({
      source: { fileName: 'source.md', content: SOURCE, loading: false, missing: false, error: null },
    })
    render()
    // Anchor index 1 is "§9" — the source has no section 9.
    click('[data-testid="paper-anchor"][data-anchor-index="1"]')
    expect(activeSection()).toBe('paper-section-overview')
    expect(document.querySelector('[data-testid="paper-anchor-miss"]')?.textContent).toBe('not found')
    expect(Element.prototype.scrollIntoView).not.toHaveBeenCalled()
  })

  it('degrades honestly when the source text is missing', () => {
    render()
    click('[data-testid="paper-anchor"][data-anchor-index="0"]')
    expect(activeSection()).toBe('paper-section-overview')
    expect(document.querySelector('[data-testid="paper-anchor-miss"]')).not.toBeNull()
  })
})

describe('PaperWorkspace — library-sync refresh & inline errors', () => {
  beforeEach(() => {
    // Mirror the outer suite's fixture: stub scrollIntoView and seed one paper.
    Element.prototype.scrollIntoView = vi.fn()
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

  it('keeps every previous section when a library sync appends a new one (round-trip)', async () => {
    // Drive the REAL loader (mocked workspace RPCs, not the hook) so the
    // refresh-key bump genuinely re-runs the load — the regression this guards
    // is a rebuild that drops the previously parsed sections (the stub could
    // never fail on it).
    artifactsHolder.current = null
    const dir = paperRecord().dir
    const fileEntry = (name: string) => ({ name, path: `${dir}/${name}`, is_dir: false })
    vi.mocked(listDirectory).mockResolvedValue([fileEntry('note.md'), fileEntry('appraisal.md')])
    vi.mocked(readFile).mockImplementation(async (path: string) => {
      if (path.endsWith('note.md')) return 'Note v1'
      if (path.endsWith('appraisal.md')) return 'Appraisal v1'
      // A card-less deck falls back to the plain Markdown render.
      if (path.endsWith('flashcards.md')) return '# Flashcards\n\nNo tables here.'
      return ''
    })

    render()
    await flushArtifacts()

    // The sections written before the deepen are present…
    click('[data-testid="paper-section-note"]')
    expect(document.querySelector('[data-testid="paper-note-markdown"]')?.textContent).toContain(
      'Note v1',
    )
    click('[data-testid="paper-section-appraisal"]')
    expect(
      document.querySelector('[data-testid="paper-appraisal-markdown"]')?.textContent,
    ).toContain('Appraisal v1')

    // The deepen appends flashcards.md; the library sync bumps the refresh key,
    // which re-runs the loader against the now-larger directory.
    vi.mocked(listDirectory).mockResolvedValue([
      fileEntry('note.md'),
      fileEntry('appraisal.md'),
      fileEntry('flashcards.md'),
    ])
    await act(async () => {
      usePaperStore.setState({ lastSyncAt: usePaperStore.getState().lastSyncAt + 1 })
    })
    await flushArtifacts()

    // …the prior sections survive the rebuild…
    click('[data-testid="paper-section-note"]')
    expect(document.querySelector('[data-testid="paper-note-markdown"]')?.textContent).toContain(
      'Note v1',
    )
    click('[data-testid="paper-section-appraisal"]')
    expect(
      document.querySelector('[data-testid="paper-appraisal-markdown"]')?.textContent,
    ).toContain('Appraisal v1')
    // …and the appended section is built out on top of them (a card-less deck
    // renders through the Markdown fallback).
    click('[data-testid="paper-section-flashcards"]')
    expect(
      document.querySelector('[data-testid="paper-flashcards-markdown"]')?.textContent,
    ).toContain('No tables here.')

    // Restore the default (pending) RPCs so the later suites' render of the real
    // loader schedules no state update outside act.
    vi.mocked(listDirectory).mockReturnValue(new Promise(() => {}))
    vi.mocked(readFile).mockReturnValue(new Promise(() => {}))
  })

  it('renders a paper-store failure inline', () => {
    // The failure lands on paperStore.error, whose only other renderer is the
    // Research panel's Papers segment — a different surface. This tab must show
    // it itself so the failure is never invisible.
    render()
    act(() => {
      usePaperStore.getState().setError('Failed to record the flashcard review: boom')
    })
    const alert = document.querySelector('[data-testid="paper-workspace-error"]')
    expect(alert).not.toBeNull()
    expect(alert?.textContent).toContain('Failed to record the flashcard review: boom')
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

  it('surfaces a comparison whose file could not be read (Issue 23)', () => {
    // An unreadable comparison has empty content, so it never matches the paper;
    // without the notice the section would silently look like "no comparisons".
    comparisonsHolder.current = {
      dir: '/ws/.research/comparisons',
      loading: false,
      items: [{ slug: 'broken', content: '', error: 'EACCES: permission denied' }],
    }
    render()
    click('[data-testid="paper-section-compare"]')
    const notice = document.querySelector('[data-testid="paper-compare-error"]')
    expect(notice).not.toBeNull()
    expect(notice?.textContent).toContain('could not read broken.md')
  })
})
