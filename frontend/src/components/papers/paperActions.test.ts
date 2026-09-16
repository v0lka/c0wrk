// Pure prompt-builder tests for the Papers view dispatch constants.
import { describe, expect, it } from 'vitest'

import {
  STUDY_MODE_OPTIONS,
  STUDY_MODE_LADDER,
  STUDY_PAPER_SKILL,
  RESEARCH_HYPOTHESIS_SKILL,
  DEFAULT_STUDY_MODE,
  PRIOR_ART_DEEP_READ_MODE,
  buildStudyPrompt,
  buildStudyAttachmentPrompt,
  buildDeepReadPriorArtPrompt,
  buildDeepenPrompt,
  buildComparePrompt,
  buildCompareSelectedPrompt,
  buildProposeHypothesisPrompt,
  nextStudyMode,
  paperGaps,
  paperCardRef,
  paperDisplayRef,
  paperIdentifierRef,
  comparisonsRef,
  comparisonSlug,
  COMPARISONS_SUBDIR,
} from './paperActions'
import type { PaperRecord } from '@/api/papers'

function makePaper(overrides: Partial<PaperRecord> = {}): PaperRecord {
  return {
    id: 'P-001',
    slug: 'vaswani-2017-attention',
    title: 'Attention Is All You Need',
    authors: ['Vaswani'],
    year: 2017,
    venue: 'NeurIPS',
    identifiers: [],
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

describe('paperActions — skill constants', () => {
  it('names the skills that own study/appraise/compare and hypotheses', () => {
    expect(STUDY_PAPER_SKILL).toBe('study-paper')
    expect(RESEARCH_HYPOTHESIS_SKILL).toBe('research-hypothesis')
  })

  it('offers the mode selector in Auto-first order', () => {
    expect(STUDY_MODE_OPTIONS.map((o) => o.label)).toEqual([
      'Auto',
      'Skim',
      'Review',
      'Implement',
      'Teach',
    ])
    expect(STUDY_MODE_OPTIONS[0]!.value).toBe('auto')
  })
})

describe('paperActions — study prompt', () => {
  it('adds no mode clause for Auto', () => {
    expect(buildStudyPrompt('1706.03762', 'auto')).toBe('Study this paper: 1706.03762.')
  })

  it('names the chosen mode when one is forced', () => {
    expect(buildStudyPrompt('10.1145/xyz', 'review')).toBe(
      'Study this paper: 10.1145/xyz. Use review mode.',
    )
  })

  it('trims the pasted reference', () => {
    expect(buildStudyPrompt('  https://arxiv.org/abs/1706.03762  ', 'skim')).toBe(
      'Study this paper: https://arxiv.org/abs/1706.03762. Use skim mode.',
    )
  })
})

describe('paperActions — references', () => {
  it('builds a display reference with the first identifier', () => {
    const paper = makePaper({ identifiers: [{ scheme: 'arxiv', value: '1706.03762' }] })
    expect(paperDisplayRef(paper)).toBe('Attention Is All You Need (arxiv:1706.03762)')
  })

  it('falls back to the bare title without an identifier', () => {
    expect(paperDisplayRef(makePaper())).toBe('Attention Is All You Need')
  })

  it('prefers the card path for a library reference, else the display reference', () => {
    expect(paperCardRef(makePaper())).toBe('papers/vaswani-2017-attention/paper.md')
    expect(paperCardRef(makePaper({ card_path: '' }))).toBe('Attention Is All You Need')
  })
})

describe('paperActions — row gestures', () => {
  it('scopes Deepen to the paper card', () => {
    expect(buildDeepenPrompt(makePaper())).toContain('papers/vaswani-2017-attention/paper.md')
  })

  it('scopes Compare to the paper card', () => {
    expect(buildComparePrompt(makePaper())).toContain('papers/vaswani-2017-attention/paper.md')
  })

  it('scopes Propose-hypothesis to the paper title', () => {
    expect(buildProposeHypothesisPrompt(makePaper())).toContain('Attention Is All You Need')
  })

  it('uses the display reference (with identifier) when proposing a hypothesis', () => {
    const paper = makePaper({ identifiers: [{ scheme: 'doi', value: '10.1/x' }] })
    expect(buildProposeHypothesisPrompt(paper)).toContain('Attention Is All You Need (doi:10.1/x)')
  })
})

describe('paperActions — depth escalation (E4)', () => {
  it('exposes the study ladder shallow → deep', () => {
    expect([...STUDY_MODE_LADDER]).toEqual(['skim', 'review', 'implement', 'teach'])
  })

  it('escalates exactly one rung from the recorded card mode', () => {
    // A skim (or an absent/unknown mode) deepens into a review.
    expect(nextStudyMode(makePaper({ mode: 'skim' }))).toBe('review')
    expect(nextStudyMode(makePaper({ mode: '' }))).toBe('review')
    expect(nextStudyMode(makePaper({ mode: 'survey' }))).toBe('review')
    // A full read escalates into an implement pass.
    expect(nextStudyMode(makePaper({ mode: 'deep' }))).toBe('implement')
  })

  it('advances one rung per card mode (skim → review → implement → teach, saturating)', () => {
    // The card now records the skill's own tokens verbatim, so each dispatches
    // to the next rung by name.
    expect(nextStudyMode(makePaper({ mode: 'skim' }))).toBe('review')
    expect(nextStudyMode(makePaper({ mode: 'review' }))).toBe('implement')
    expect(nextStudyMode(makePaper({ mode: 'implement' }))).toBe('teach')
    expect(nextStudyMode(makePaper({ mode: 'teach' }))).toBe('teach')
  })

  it('saturates at the deepest rung instead of overflowing', () => {
    // The skill vocabulary (which the card carries verbatim) ranks deeper.
    expect(nextStudyMode(makePaper({ mode: 'implement' }))).toBe('teach')
    expect(nextStudyMode(makePaper({ mode: 'teach' }))).toBe('teach')
  })
})

describe('paperActions — Go deeper (E4)', () => {
  it('round-trips to the SAME card, one mode deeper, and mandates append-only', () => {
    const paper = makePaper({ mode: 'skim' })
    const prompt = buildDeepenPrompt(paper)
    // Same card (round-trip): the deepen re-enters the exact document the record
    // carries — no new card is created, so the existing sections stay in place.
    expect(prompt).toContain(paper.card_path)
    expect(prompt).toContain(`slug "${paper.slug}"`)
    // A higher mode than the recorded skim.
    expect(prompt).toContain('use review mode')
    // The append invariant that keeps the previous sections (study-paper's
    // "Append, never rewrite").
    expect(prompt).toMatch(/APPEND only/)
    expect(prompt).toContain('never rewrite or delete existing content')
  })

  it('escalates by exactly one rung for a paper already read in full', () => {
    expect(buildDeepenPrompt(makePaper({ mode: 'deep' }))).toContain('use implement mode')
  })
})

describe('paperActions — Suggest hypotheses from gaps (E5)', () => {
  it('falls back to a generic proposal when no gaps were recorded', () => {
    expect(paperGaps(makePaper())).toEqual([])
    expect(buildProposeHypothesisPrompt(makePaper())).toBe(
      'Propose research hypotheses informed by Attention Is All You Need.',
    )
  })

  it('formats each uncertainty as a gap bullet (dropping empty entries)', () => {
    const paper = makePaper({
      uncertainties: [
        { item: 'Number of seeds', detail: 'not reported' },
        { item: 'Error bars', detail: '' },
        { item: '   ', detail: '' },
      ],
    })
    expect(paperGaps(paper)).toEqual(['Number of seeds — not reported', 'Error bars'])
  })

  it('grounds the dispatched prompt in the recorded gaps', () => {
    const paper = makePaper({
      uncertainties: [{ item: 'Number of seeds', detail: 'not reported' }],
    })
    const prompt = buildProposeHypothesisPrompt(paper)
    expect(prompt).toContain('Attention Is All You Need')
    expect(prompt).toContain("Ground them in the paper's open questions and gaps:")
    expect(prompt).toContain('- Number of seeds — not reported')
  })
})

describe('paperActions — invoke-from-anywhere prompts', () => {
  it('defaults a study gesture to Auto (the skill infers the depth)', () => {
    expect(DEFAULT_STUDY_MODE).toBe('auto')
    expect(buildStudyAttachmentPrompt('report.pdf', DEFAULT_STUDY_MODE)).toBe(
      'Study this attached paper: report.pdf.',
    )
  })

  it('names the chosen mode on a chat-attachment study', () => {
    expect(buildStudyAttachmentPrompt('  report.pdf  ', 'skim')).toBe(
      'Study this attached paper: report.pdf. Use skim mode.',
    )
  })

  it('forces the deep (review) pass for prior art and references the catalog path', () => {
    expect(PRIOR_ART_DEEP_READ_MODE).toBe('review')
    const prompt = buildDeepReadPriorArtPrompt('papers/R-001-flaw/prior-art.md')
    expect(prompt).toContain('papers/R-001-flaw/prior-art.md')
    expect(prompt).toContain('Use review mode.')
  })

  it('honours an explicit mode override on the prior-art prompt', () => {
    expect(buildDeepReadPriorArtPrompt('p.md', 'skim')).toBe(
      'Deep read the prior-art catalog at p.md and study the referenced works in depth. Use skim mode.',
    )
  })
})

describe('paperActions — compare selected (multi-paper)', () => {
  const p1 = makePaper({ identifiers: [{ scheme: 'arxiv', value: '1706.03762' }] })
  const p2 = makePaper({
    id: 'P-002',
    slug: 'sutskever-2014-sequence',
    title: 'Sequence to Sequence Learning',
    identifiers: [{ scheme: 'arxiv', value: '1409.3215' }],
  })

  it('prefers the first identifier, then the card path, then the title', () => {
    expect(paperIdentifierRef(p1)).toBe('arxiv:1706.03762')
    expect(paperIdentifierRef(makePaper({ identifiers: [] }))).toBe(
      'papers/vaswani-2017-attention/paper.md',
    )
    expect(paperIdentifierRef(makePaper({ identifiers: [], card_path: '' }))).toBe(
      'Attention Is All You Need',
    )
  })

  it('resolves the comparisons directory against a research root', () => {
    expect(COMPARISONS_SUBDIR).toBe('comparisons')
    expect(comparisonsRef('/ws/.research')).toBe('/ws/.research/comparisons')
    expect(comparisonsRef('/ws/.research/')).toBe('/ws/.research/comparisons')
    expect(comparisonsRef('')).toBe('comparisons')
  })

  it('derives a deterministic <slug> from the member papers', () => {
    expect(comparisonSlug([p1, p2])).toBe('vaswani-2017-attention-vs-sutskever-2014-sequence')
    // Falls back to the lowercased id when a paper has no slug.
    expect(comparisonSlug([makePaper({ slug: '' })])).toBe('p-001')
  })

  it('dispatches the Compare intent with every selected identifier and the artifact path', () => {
    const prompt = buildCompareSelectedPrompt([p1, p2], '/ws/.research')
    expect(prompt).toContain('Compare these 2 papers using the study-paper Compare intent')
    // The three template sections the reader renders come from the skill.
    expect(prompt).toContain('Fairness & comparability')
    expect(prompt).toContain('Agreement/Disagreement')
    expect(prompt).toContain('Gaps')
    // The artifact path under the research root's comparisons/ directory.
    expect(prompt).toContain(
      '/ws/.research/comparisons/vaswani-2017-attention-vs-sutskever-2014-sequence.md',
    )
    // Each selected paper's id + identifier is listed.
    expect(prompt).toContain('- P-001 — arxiv:1706.03762')
    expect(prompt).toContain('- P-002 — arxiv:1409.3215')
  })

  it('degrades to the bare comparisons subdir without a known research root', () => {
    expect(buildCompareSelectedPrompt([p1, p2], '')).toContain(
      'comparisons/vaswani-2017-attention-vs-sutskever-2014-sequence.md',
    )
  })
})
