// @vitest-environment jsdom
//
// FlashcardsReview — the interactive active-recall review: deck parsing, the
// flip (reveal answer), the again/hard/good/easy ratings, the computed interval
// hints, and the completion summary.

import { describe, it, expect, vi, beforeEach, afterEach } from 'vitest'
import { act } from 'react'
import { createRoot, type Root } from 'react-dom/client'

vi.mock('@/lib/markdownConfig', () => ({
  Markdown: ({ content }: { content: string }) => <div data-testid="md">{content}</div>,
}))

vi.mock('@/api/papers', () => ({
  getPapers: vi.fn(),
  getPaper: vi.fn(),
  setPaperPinned: vi.fn(),
  recordFlashcardReview: vi.fn(),
}))

import { recordFlashcardReview, type PaperRecord } from '@/api/papers'
import { FlashcardsReview } from './FlashcardsReview'
import { usePaperStore } from '@/stores/paperStore'
import type { PaperArtifact } from './usePaperArtifacts'

const mockedRecordFlashcardReview = vi.mocked(recordFlashcardReview)

function paperRecordOf(id: string): PaperRecord {
  return {
    id,
    slug: id.toLowerCase(),
    title: `Paper ${id}`,
    authors: [],
    year: 2024,
    venue: '',
    identifiers: [],
    mode: 'deep',
    reading: '',
    verdict: 'accepted',
    confidence: 'high',
    research_ids: [],
    anchors: [],
    claims: [],
    red_flags: [],
    uncertainties: [],
    dir: `/ws/.research/papers/${id.toLowerCase()}`,
    card_path: `papers/${id.toLowerCase()}/paper.md`,
    pinned: false,
    linked_research: [],
  }
}

const DECK = `## 2. Cards

| ID | Front (question) | Back (answer) | Anchor | Tag | Stage |
| --- | --- | --- | --- | --- | --- |
| P1-01 | What replaces recurrence? | Self-attention | §3 | architecture | new |
| P1-02 | How many attention heads? | 8 heads | §3.2 | hyperparams | learning |
| P1-03 | What is scaled by 1/sqrt(d_k)? | The logits | Eq 1 | math | review |

## 3. Review log

| ID | Date | Grade | Next due |
| --- | --- | --- | --- |
| P1-01 | 2024-01-02 | good | 2024-01-05 |
| P1-02 | 2024-01-02 | again | 2024-01-03 |
| P1-02 | 2024-01-03 | good | 2024-01-06 |
`

function artifactOf(content: string, overrides: Partial<PaperArtifact> = {}): PaperArtifact {
  return { fileName: 'flashcards.md', content, loading: false, missing: false, error: null, ...overrides }
}

let root: Root | null = null
let container: HTMLDivElement | null = null

function render(artifact: PaperArtifact, paperId?: string): void {
  if (root !== null) {
    act(() => {
      root!.unmount()
    })
    container?.remove()
    root = null
    container = null
  }
  container = document.createElement('div')
  document.body.appendChild(container)
  root = createRoot(container)
  act(() => {
    root!.render(
      <FlashcardsReview
        artifact={artifact}
        testId="paper-flashcards"
        emptyText="No flashcards recorded for this paper yet."
        today="2024-01-10"
        paperId={paperId}
      />,
    )
  })
}

function click(selector: string): void {
  const el = document.querySelector<HTMLElement>(selector)
  expect(el, `expected ${selector}`).not.toBeNull()
  act(() => {
    el!.dispatchEvent(new MouseEvent('click', { bubbles: true }))
  })
}

const q = (selector: string): HTMLElement | null => document.querySelector(selector)

describe('FlashcardsReview', () => {
  beforeEach(() => {
    document.body.innerHTML = ''
    usePaperStore.getState().reset()
    mockedRecordFlashcardReview.mockReset()
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

  it('renders loading / error / empty states through the markdown fallback', () => {
    render(artifactOf('', { loading: true }))
    expect(q('[data-testid="paper-flashcards-loading"]')).not.toBeNull()

    render(artifactOf('', { error: 'boom' }))
    expect(q('[data-testid="paper-flashcards-error"]')).not.toBeNull()

    render(artifactOf(''))
    expect(q('[data-testid="paper-flashcards-empty"]')).not.toBeNull()
  })

  it('falls back to raw markdown when the artifact has no recognizable cards', () => {
    render(artifactOf('# Flashcards\n\nNo tables here.'))
    expect(q('[data-testid="paper-flashcards-markdown"]')).not.toBeNull()
    expect(q('[data-testid="paper-flashcards-front"]')).toBeNull()
  })

  it('shows the first card with its prompt hidden answer and progress', () => {
    render(artifactOf(DECK))
    expect(q('[data-testid="paper-flashcards-progress"]')?.textContent).toBe('Card 1 / 3')
    expect(q('[data-testid="paper-flashcards-front"]')?.textContent).toContain('What replaces recurrence?')
    expect(q('[data-testid="paper-flashcards-back"]')).toBeNull()
    expect(q('[data-testid="paper-flashcards-rate-good"]')).toBeNull()
    expect(q('[data-testid="paper-flashcards-reveal"]')).not.toBeNull()
  })

  it('reveals the answer, anchor and the four rating buttons with interval hints', () => {
    render(artifactOf(DECK))
    click('[data-testid="paper-flashcards-reveal"]')
    expect(q('[data-testid="paper-flashcards-back"]')?.textContent).toContain('Self-attention')
    expect(q('[data-testid="paper-flashcards-anchor"]')?.textContent).toBe('§3')
    expect(q('[data-testid="paper-flashcards-grades"]')).not.toBeNull()
    // Card 1 is at rung 0 (its log has one `good`): `good` promotes to 3 days.
    expect(q('[data-testid="paper-flashcards-rate-good"]')?.textContent).toContain('3d')
    expect(q('[data-testid="paper-flashcards-rate-again"]')?.textContent).toContain('1d')
  })

  it('advances to the next card and resets the flip after a rating', () => {
    render(artifactOf(DECK))
    click('[data-testid="paper-flashcards-reveal"]')
    click('[data-testid="paper-flashcards-rate-good"]')
    expect(q('[data-testid="paper-flashcards-progress"]')?.textContent).toBe('Card 2 / 3')
    expect(q('[data-testid="paper-flashcards-front"]')?.textContent).toContain('How many attention heads?')
    expect(q('[data-testid="paper-flashcards-back"]')).toBeNull()
    expect(q('[data-testid="paper-flashcards-reveal"]')).not.toBeNull()
  })

  it('completes after the last card and lists each grade with its next due date', () => {
    render(artifactOf(DECK))
    for (let i = 0; i < 3; i++) {
      click('[data-testid="paper-flashcards-reveal"]')
      click('[data-testid="paper-flashcards-rate-good"]')
    }
    expect(q('[data-testid="paper-flashcards-done"]')).not.toBeNull()
    const rows = document.querySelectorAll('[data-testid="paper-flashcards-result"]')
    expect(rows).toHaveLength(3)
    // Card 1 at rung 0 → good → +3 days from 2024-01-10.
    expect(rows[0]!.textContent).toContain('good')
    expect(rows[0]!.textContent).toContain('2024-01-13')
  })

  it('restarts the session from the completion summary', () => {
    render(artifactOf(DECK))
    for (let i = 0; i < 3; i++) {
      click('[data-testid="paper-flashcards-reveal"]')
      click('[data-testid="paper-flashcards-rate-good"]')
    }
    click('[data-testid="paper-flashcards-restart"]')
    expect(q('[data-testid="paper-flashcards-progress"]')?.textContent).toBe('Card 1 / 3')
    expect(q('[data-testid="paper-flashcards-reveal"]')).not.toBeNull()
  })

  it('writes a review back through the store when the owner paper is provided', async () => {
    usePaperStore.getState().loadLibrary({
      project_id: 'p1',
      research_root: '/ws/.research',
      root: '/ws/.research/papers',
      papers: [paperRecordOf('P-001')],
      pinned: [],
    })
    mockedRecordFlashcardReview.mockResolvedValue(undefined)

    render(artifactOf(DECK), 'P-001')
    click('[data-testid="paper-flashcards-reveal"]')
    click('[data-testid="paper-flashcards-rate-good"]')
    await Promise.resolve()

    expect(mockedRecordFlashcardReview).toHaveBeenCalledWith('p1', 'P-001', 'P1-01', 'good')
  })

  it('stays read-only (no write-back) when no owner paper is provided', () => {
    render(artifactOf(DECK))
    click('[data-testid="paper-flashcards-reveal"]')
    click('[data-testid="paper-flashcards-rate-good"]')
    expect(mockedRecordFlashcardReview).not.toHaveBeenCalled()
  })
})
