// Unit tests for the pure paper ← hypothesis reverse projection
// (lib/papersByHypothesis.ts). No React, no stores — the fold is exercised on
// plain fixtures.
import { describe, it, expect } from 'vitest'
import {
  buildPapersByHypothesis,
  collectHypothesisNodes,
  normalizeHypothesisId,
} from './papersByHypothesis'
import type { PaperRecord } from '@/api/papers'
import type { HypothesisNode, ResearchRoot } from '@/types/models'

function paper(overrides: Partial<PaperRecord> & { id: string }): PaperRecord {
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

function node(id: string, title = id): HypothesisNode {
  return { id, title, status: 'open' }
}

describe('normalizeHypothesisId', () => {
  it('canonicalizes H-NNN spellings (case + zero-pad + whitespace)', () => {
    expect(normalizeHypothesisId('h-3')).toBe('H-003')
    expect(normalizeHypothesisId('H-03')).toBe('H-003')
    expect(normalizeHypothesisId('H-003')).toBe('H-003')
    expect(normalizeHypothesisId('  H-3  ')).toBe('H-003')
    expect(normalizeHypothesisId('H-1000')).toBe('H-1000')
  })

  it('upper-cases a non-H-NNN value and passes "" through', () => {
    expect(normalizeHypothesisId('hc-1')).toBe('HC-1')
    expect(normalizeHypothesisId('')).toBe('')
    expect(normalizeHypothesisId('   ')).toBe('')
  })
})

describe('buildPapersByHypothesis', () => {
  it('returns empty buckets for empty inputs (empty state)', () => {
    const index = buildPapersByHypothesis([], [])
    expect(index.byHypothesis.size).toBe(0)
    expect(index.dangling.size).toBe(0)
    expect(index.informedCount).toBe(0)
    expect(index.danglingCount).toBe(0)
  })

  it('buckets each paper under the id it informs', () => {
    const papers = [
      paper({ id: 'P-001', research_ids: ['H-001'] }),
      paper({ id: 'P-002', research_ids: ['H-001'] }),
      paper({ id: 'P-003', research_ids: ['H-002'] }),
    ]
    const index = buildPapersByHypothesis(papers, [node('H-001'), node('H-002')])

    expect([...index.byHypothesis.keys()].sort()).toEqual(['H-001', 'H-002'])
    expect(index.byHypothesis.get('H-001')!.map((p) => p.id)).toEqual(['P-001', 'P-002'])
    expect(index.byHypothesis.get('H-002')!.map((p) => p.id)).toEqual(['P-003'])
    expect(index.informedCount).toBe(2)
    expect(index.dangling.size).toBe(0)
    expect(index.danglingCount).toBe(0)
  })

  it('joins across H-NNN spelling differences', () => {
    const papers = [paper({ id: 'P-001', research_ids: ['H-3'] })]
    const index = buildPapersByHypothesis(papers, [node('H-003')])

    // The card's `H-3` resolves to the node's canonical `H-003`.
    expect(index.byHypothesis.get('H-003')!.map((p) => p.id)).toEqual(['P-001'])
    expect(index.dangling.size).toBe(0)
  })

  it('surfaces an id that resolves to no node as dangling (not dropped)', () => {
    const papers = [
      paper({ id: 'P-001', research_ids: ['H-001'] }),
      paper({ id: 'P-002', research_ids: ['H-999'] }),
      paper({ id: 'P-003', research_ids: ['H-999'] }),
    ]
    const index = buildPapersByHypothesis(papers, [node('H-001')])

    expect(index.byHypothesis.get('H-001')!.map((p) => p.id)).toEqual(['P-001'])
    expect(index.dangling.get('H-999')!.map((p) => p.id)).toEqual(['P-002', 'P-003'])
    expect(index.danglingCount).toBe(2)
    expect(index.informedCount).toBe(1)
  })

  it('dedupes repeated ids on one paper (same spelling and across spellings)', () => {
    const papers = [
      paper({ id: 'P-001', research_ids: ['H-001', 'h-1', 'H-001'] }),
    ]
    const index = buildPapersByHypothesis(papers, [node('H-001')])

    expect(index.byHypothesis.get('H-001')!.map((p) => p.id)).toEqual(['P-001'])
    expect(index.byHypothesis.get('H-001')!).toHaveLength(1)
  })

  it('ignores blank research_ids entries', () => {
    const papers = [paper({ id: 'P-001', research_ids: ['', '   '] })]
    const index = buildPapersByHypothesis(papers, [node('H-001')])
    expect(index.byHypothesis.size).toBe(0)
    expect(index.dangling.size).toBe(0)
  })
})

describe('collectHypothesisNodes', () => {
  it('returns a stable empty list when the root is undefined', () => {
    expect(collectHypothesisNodes(undefined)).toEqual([])
  })

  it('unions the nodes of every project (the join universe)', () => {
    const root: ResearchRoot = {
      path: '/ws/.research',
      index: [],
      projects: [
        {
          id: 'R-001',
          brief: { id: 'R-001', title: 'A' },
          graph: { nodes: [node('H-001')], edges: [] },
          metrics: { total: 1, by_status: {}, confirmation_rate: 0, depth: 0, breadth: 0 },
          prior_art_count: 0,
          has_report: false,
          log: [],
        },
        {
          id: 'R-002',
          brief: { id: 'R-002', title: 'B' },
          graph: { nodes: [node('H-001'), node('H-005')], edges: [] },
          metrics: { total: 2, by_status: {}, confirmation_rate: 0, depth: 0, breadth: 0 },
          prior_art_count: 0,
          has_report: false,
          log: [],
        },
      ],
    }

    const ids = collectHypothesisNodes(root).map((n) => n.id)
    expect(ids).toEqual(['H-001', 'H-001', 'H-005'])
  })
})
