// Unit tests for api/papers.ts — boundary guards and normalization on the
// library/paper RPC paths (per-entry fail-closed) plus the papers:changed
// payload guard.

import { describe, it, expect, vi, beforeEach } from 'vitest'

const mockApp: Record<string, (...args: unknown[]) => Promise<unknown>> = {}

vi.mock('@/api/runtime', () => ({
  getApp: () => mockApp,
}))
vi.mock('@/lib/logger', () => ({
  logger: { error: vi.fn(), warn: vi.fn() },
}))

import {
  getPaper,
  getPapers,
  isPapersChangedPayload,
  normalizePaperLibrary,
  setPaperPinned,
} from '@/api/papers'

/** A full, well-formed wire PaperDTO. */
function wirePaper(overrides: Record<string, unknown> = {}): Record<string, unknown> {
  return {
    id: 'P-001',
    slug: 'attention',
    title: 'Attention Is All You Need',
    authors: ['Vaswani'],
    year: 2017,
    venue: 'NeurIPS',
    identifiers: [{ scheme: 'doi', value: '10.1/x' }],
    mode: 'deep',
    reading: 'selective',
    verdict: 'accepted',
    confidence: 'high',
    research_ids: ['H-001'],
    anchors: [{ label: 'sec 3', ref: 'p.4' }],
    claims: [{ claim: 'c', evidence: 'e', location: 'p.4', stance: 'support' }],
    red_flags: [{ flag: 'small n', detail: 'n=3', severity: 'low' }],
    uncertainties: [{ item: 'generalization' }],
    dir: '/ws/.research/papers/attention',
    card_path: 'papers/attention/paper.md',
    pinned: true,
    linked_research: [{ hypothesis_id: 'H-001', research_id: 'R-001' }],
    ...overrides,
  }
}

function wireLibrary(papers: unknown[], pinned: string[] = []): Record<string, unknown> {
  return {
    project_id: 'p1',
    research_root: '/ws/.research',
    root: '/ws/.research/papers',
    papers,
    pinned,
  }
}

describe('getPapers boundary validation', () => {
  beforeEach(() => {
    delete mockApp.GetPapers
  })

  it('normalizes a full library and folds the optional wire fields', async () => {
    mockApp.GetPapers = vi.fn(() =>
      Promise.resolve(wireLibrary([wirePaper()], ['papers/attention/paper.md'])),
    )

    const lib = await getPapers('p1')

    expect(lib.project_id).toBe('p1')
    expect(lib.research_root).toBe('/ws/.research')
    expect(lib.root).toBe('/ws/.research/papers')
    expect(lib.pinned).toEqual(['papers/attention/paper.md'])
    expect(lib.papers).toHaveLength(1)
    expect(lib.papers[0]).toMatchObject({
      id: 'P-001',
      slug: 'attention',
      mode: 'deep',
      reading: 'selective',
      verdict: 'accepted',
      confidence: 'high',
      dir: '/ws/.research/papers/attention',
      card_path: 'papers/attention/paper.md',
      pinned: true,
      research_ids: ['H-001'],
    })
    expect(lib.papers[0]!.claims[0]).toEqual({
      claim: 'c',
      evidence: 'e',
      location: 'p.4',
      stance: 'support',
    })
    // Absent optional anchor fields normalize to ''.
    expect(lib.papers[0]!.anchors[0]).toEqual({ label: 'sec 3', ref: 'p.4', note: '' })
    expect(lib.papers[0]!.uncertainties[0]).toEqual({ item: 'generalization', detail: '' })
    expect(lib.papers[0]!.linked_research[0]).toEqual({
      hypothesis_id: 'H-001',
      research_id: 'R-001',
    })
  })

  it('makes null collections non-nil and defaults the absent scalars', async () => {
    mockApp.GetPapers = vi.fn(() =>
      Promise.resolve({
        project_id: 'p1',
        research_root: '/ws/.research',
        root: '/ws/.research/papers',
        papers: [
          {
            id: 'P-002',
            slug: 'x',
            title: 'X',
            authors: null,
            identifiers: null,
            anchors: null,
            claims: null,
            red_flags: null,
            uncertainties: null,
            research_ids: null,
            linked_research: null,
            dir: '/ws/.research/papers/x',
            card_path: 'papers/x/paper.md',
          },
        ],
        pinned: null,
      }),
    )

    const lib = await getPapers('p1')

    expect(lib.pinned).toEqual([])
    expect(lib.papers[0]).toMatchObject({
      authors: [],
      identifiers: [],
      anchors: [],
      claims: [],
      red_flags: [],
      uncertainties: [],
      research_ids: [],
      linked_research: [],
      year: 0,
      venue: '',
      mode: '',
      reading: '',
      verdict: '',
      confidence: '',
      pinned: false,
    })
  })

  it('folds an unknown enum value to the empty string', async () => {
    mockApp.GetPapers = vi.fn(() =>
      Promise.resolve(
        wireLibrary([wirePaper({ mode: 'teleport', reading: 'unless', verdict: 'maybe', confidence: 'certain' })]),
      ),
    )

    const lib = await getPapers('p1')

    expect(lib.papers[0]).toMatchObject({ mode: '', reading: '', verdict: '', confidence: '' })
  })

  it('round-trips a review-mode card and its reading decision', async () => {
    mockApp.GetPapers = vi.fn(() =>
      Promise.resolve(
        wireLibrary([wirePaper({ mode: 'review', reading: 'selective', verdict: 'accepted' })]),
      ),
    )

    const lib = await getPapers('p1')

    // The card's skill mode and its distinct reading decision both survive.
    expect(lib.papers[0]).toMatchObject({ mode: 'review', reading: 'selective', verdict: 'accepted' })
  })

  it('drops malformed paper entries (per-entry fail-closed) but keeps valid ones', async () => {
    mockApp.GetPapers = vi.fn(() =>
      Promise.resolve(
        wireLibrary([
          wirePaper(),
          null,
          { id: 'P-003' }, // missing dir/card_path identity fields
          { ...wirePaper(), slug: 7 }, // non-string slug
          // A malformed nested claim is dropped while the entry itself survives.
          { ...wirePaper({ id: 'P-004' }), claims: [{ evidence: 'no claim field' }] },
        ]),
      ),
    )

    const lib = await getPapers('p1')

    expect(lib.papers.map((p) => p.id)).toEqual(['P-001', 'P-004'])
    expect(lib.papers[1]!.claims).toEqual([])
  })

  it('treats an empty library as a valid empty (non-nil) result', async () => {
    mockApp.GetPapers = vi.fn(() => Promise.resolve(wireLibrary([])))

    const lib = await getPapers('p1')

    expect(lib.papers).toEqual([])
    expect(lib.pinned).toEqual([])
  })

  it('throws on a top-level shape mismatch (schema drift)', async () => {
    mockApp.GetPapers = vi.fn(() => Promise.resolve({ project_id: 'p1' }))

    await expect(getPapers('p1')).rejects.toThrow('Invalid papers response from backend')
  })

  it('throws when papers is not an array', async () => {
    mockApp.GetPapers = vi.fn(() =>
      Promise.resolve({ project_id: 'p1', research_root: '/r', root: '/r/papers', papers: 'nope' }),
    )

    await expect(getPapers('p1')).rejects.toThrow('Invalid papers response from backend')
  })

  it('throws when pinned is not a string array', async () => {
    mockApp.GetPapers = vi.fn(() =>
      Promise.resolve({ ...wireLibrary([wirePaper()]), pinned: [1, 2] }),
    )

    await expect(getPapers('p1')).rejects.toThrow('Invalid papers response from backend')
  })
})

describe('normalizePaperLibrary', () => {
  it('throws on non-object input', () => {
    expect(() => normalizePaperLibrary(null)).toThrow('Invalid papers response from backend')
    expect(() => normalizePaperLibrary('nope')).toThrow('Invalid papers response from backend')
  })
})

describe('getPaper boundary validation', () => {
  beforeEach(() => {
    delete mockApp.GetPaper
  })

  it('returns a normalized single record', async () => {
    mockApp.GetPaper = vi.fn(() => Promise.resolve(wirePaper()))

    const record = await getPaper('p1', 'P-001')

    expect(record.id).toBe('P-001')
    expect(record.anchors[0]).toEqual({ label: 'sec 3', ref: 'p.4', note: '' })
  })

  it('throws when the payload is malformed', async () => {
    mockApp.GetPaper = vi.fn(() => Promise.resolve({ id: 'P-001' }))

    await expect(getPaper('p1', 'P-001')).rejects.toThrow('Invalid paper response from backend')
  })
})

describe('setPaperPinned', () => {
  it('forwards the arguments and resolves', async () => {
    const call = vi.fn(() => Promise.resolve(undefined))
    mockApp.SetPaperPinned = call

    await setPaperPinned('p1', 'P-001', true)

    expect(call).toHaveBeenCalledWith('p1', 'P-001', true)
  })

  it('propagates a backend failure', async () => {
    mockApp.SetPaperPinned = vi.fn(() => Promise.reject(new Error('boom')))

    await expect(setPaperPinned('p1', 'P-001', false)).rejects.toThrow('boom')
  })
})

describe('isPapersChangedPayload', () => {
  it('accepts a well-formed payload', () => {
    expect(isPapersChangedPayload({ project_id: 'p1', paths: '/a,/b' })).toBe(true)
  })

  it('rejects malformed payloads', () => {
    expect(isPapersChangedPayload(null)).toBe(false)
    expect(isPapersChangedPayload({ project_id: 'p1' })).toBe(false)
    expect(isPapersChangedPayload({ project_id: 1, paths: 'x' })).toBe(false)
  })
})
