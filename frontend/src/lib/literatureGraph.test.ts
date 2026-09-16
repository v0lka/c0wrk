import { describe, it, expect } from 'vitest'

import {
  literatureGeneratedAtLabel,
  literatureToGraph,
  parseLiteratureJson,
  workDisplayTitle,
  workIdentity,
  LITERATURE_SEED_ID,
  type LiteratureRecord,
  type LiteratureWork,
} from './literatureGraph'

function work(overrides: Partial<LiteratureWork> = {}): LiteratureWork {
  return {
    title: '',
    year: null,
    doi: '',
    openalex: '',
    citedBy: null,
    authors: [],
    origin: '',
    abstract: '',
    reasons: [],
    ...overrides,
  }
}

function record(overrides: Partial<LiteratureRecord> = {}): LiteratureRecord {
  return {
    seed: work({ title: 'Seed Paper', doi: '10.1/seed', year: 2017 }),
    seedInput: '10.1/seed',
    seedMatch: 'identifier',
    direction: 'both',
    predecessors: [],
    citing: [],
    contradictions: [],
    sourcesUsed: [],
    rateLimitEvents: 0,
    notes: [],
    generatedAt: '',
    ...overrides,
  }
}

/** A representative literature.py payload (snake_case, as the script writes it). */
const RAW = JSON.stringify({
  generated_at: '2026-09-16T12:34:56Z',
  seed: { title: 'Attention Is All You Need', year: 2017, doi: '10.5555/attn', cited_by: 90000, authors: ['Vaswani'] },
  seed_input: '1706.03762',
  seed_match: 'title-search',
  direction: 'both',
  predecessors: [
    { title: 'Neural Machine Translation', year: 2014, doi: '10.1/nmt', authors: ['Sutskever'] },
    { title: 'Long Short-Term Memory', year: 1997, doi: '10.1/lstm' },
  ],
  citing: [
    { title: 'BERT', year: 2019, doi: '10.1/bert' },
    { title: 'A critique of attention', year: 2020, doi: '10.1/crit', reasons: ['critique'] },
  ],
  contradictions: [
    { title: 'A critique of attention', year: 2020, doi: '10.1/crit', reasons: ['critique'] },
  ],
  sources_used: ['OpenAlex', 'Crossref references'],
  rate_limit_events: 2,
  notes: ['OpenAlex: seed matched by title search'],
})

describe('parseLiteratureJson', () => {
  it('rejects an empty payload with an explicit error', () => {
    expect(parseLiteratureJson('   ')).toEqual({ record: null, error: 'empty payload' })
  })

  it('rejects invalid JSON with an explicit error', () => {
    const result = parseLiteratureJson('{ not json')
    expect(result.record).toBeNull()
    expect(result.error).toContain('invalid JSON')
  })

  it('rejects a non-object payload', () => {
    expect(parseLiteratureJson('[]').error).toBe('payload is not a JSON object')
  })

  it('rejects an object without a seed', () => {
    expect(parseLiteratureJson('{"predecessors":[]}').error).toBe('payload has no "seed" object')
  })

  it('normalizes snake_case fields and preserves reasons/notes', () => {
    const { record: parsed, error } = parseLiteratureJson(RAW)
    expect(error).toBeNull()
    expect(parsed).not.toBeNull()
    expect(parsed!.seed.title).toBe('Attention Is All You Need')
    expect(parsed!.seed.citedBy).toBe(90000)
    expect(parsed!.seedInput).toBe('1706.03762')
    expect(parsed!.seedMatch).toBe('title-search')
    expect(parsed!.predecessors).toHaveLength(2)
    expect(parsed!.citing).toHaveLength(2)
    expect(parsed!.contradictions[0]!.reasons).toEqual(['critique'])
    expect(parsed!.sourcesUsed).toEqual(['OpenAlex', 'Crossref references'])
    expect(parsed!.rateLimitEvents).toBe(2)
    expect(parsed!.notes).toEqual(['OpenAlex: seed matched by title search'])
    expect(parsed!.generatedAt).toBe('2026-09-16T12:34:56Z')
  })

  it('defaults generated_at to an empty string when absent (legacy payload)', () => {
    const { record: parsed } = parseLiteratureJson('{"seed":{"title":"X"}}')
    expect(parsed!.generatedAt).toBe('')
  })

  it('tolerates a minimal payload (seed only) and defaults the rest', () => {
    const { record: parsed, error } = parseLiteratureJson('{"seed":{"title":"X"}}')
    expect(error).toBeNull()
    expect(parsed!.predecessors).toEqual([])
    expect(parsed!.citing).toEqual([])
    expect(parsed!.contradictions).toEqual([])
    expect(parsed!.rateLimitEvents).toBe(0)
  })
})

describe('literatureToGraph — node/edge mapping', () => {
  const { record: parsed } = parseLiteratureJson(RAW)
  const model = literatureToGraph(parsed!)
  const nodeById = new Map(model.graph.nodes.map((n) => [n.id, n]))

  it('makes the seed the hub with the amber (in-progress) status', () => {
    const seed = nodeById.get(LITERATURE_SEED_ID)
    expect(seed).toBeDefined()
    expect(seed!.title).toBe('Attention Is All You Need')
    expect(seed!.status).toBe('in-progress')
    expect(model.kinds[LITERATURE_SEED_ID]).toBe('seed')
  })

  it('maps predecessors as parents of the seed with the muted status', () => {
    const p1 = nodeById.get('p1')
    expect(p1!.title).toBe('Neural Machine Translation')
    expect(p1!.status).toBe('cancelled')
    expect(p1!.parents).toEqual([])
    expect(nodeById.get(LITERATURE_SEED_ID)!.parents).toEqual(['p1', 'p2'])
    expect(model.kinds['p2']).toBe('predecessor')
  })

  it('maps citing works as children of the seed with the success status', () => {
    expect(nodeById.get('c1')!.title).toBe('BERT')
    expect(nodeById.get('c1')!.status).toBe('confirmed')
    expect(nodeById.get('c1')!.parents).toEqual([LITERATURE_SEED_ID])
    expect(model.kinds['c1']).toBe('citing')
  })

  it('emits edges predecessor→seed and seed→citing', () => {
    const edges = model.graph.edges.map((e) => `${e.from}->${e.to}`).sort()
    expect(edges).toEqual(['p1->seed', 'p2->seed', 'seed->c1', 'seed->c2'])
  })

  it('re-tags a contradiction in place (destructive colour) without adding a node', () => {
    // c2 ("A critique of attention") is the citing work flagged as a contradiction.
    expect(model.kinds['c2']).toBe('contradiction')
    expect(nodeById.get('c2')!.status).toBe('refuted')
    expect(model.works['c2']!.reasons).toEqual(['critique'])
    // 1 seed + 2 predecessors + 2 citing = 5 nodes (contradiction re-used c2).
    expect(model.graph.nodes).toHaveLength(5)
  })

  it('is deterministic: ids follow input order', () => {
    expect(model.graph.nodes.map((n) => n.id)).toEqual(['seed', 'p1', 'p2', 'c1', 'c2'])
  })

  it('renders a lone seed when the record has no related works', () => {
    const lonely = literatureToGraph(record())
    expect(lonely.graph.nodes).toHaveLength(1)
    expect(lonely.graph.edges).toHaveLength(0)
  })

  it('dedupes a work that appears in both predecessors and citing (by DOI)', () => {
    const shared = work({ title: 'Shared Work', doi: '10.9/shared' })
    const model2 = literatureToGraph(
      record({ predecessors: [shared], citing: [{ ...shared }] }),
    )
    expect(model2.graph.nodes).toHaveLength(2) // seed + one shared node
    const sharedNode = model2.graph.nodes.find((n) => n.id !== LITERATURE_SEED_ID)!
    // It is a predecessor first, so it keeps the predecessor kind; the citing
    // edge still lands on the same node.
    expect(model2.kinds[sharedNode.id]).toBe('predecessor')
    expect(model2.graph.edges.map((e) => `${e.from}->${e.to}`)).toContain('seed->' + sharedNode.id)
  })

  it('adds an orphan contradiction (not present in either set) as its own node', () => {
    const orphan = work({ title: 'Orphan refutation', doi: '10.7/orphan', reasons: ['retraction'] })
    const model2 = literatureToGraph(record({ contradictions: [orphan] }))
    expect(model2.graph.nodes).toHaveLength(2)
    const orphanNode = model2.graph.nodes.find((n) => n.id === 'x1')
    expect(orphanNode).toBeDefined()
    expect(orphanNode!.status).toBe('refuted')
    expect(model2.kinds['x1']).toBe('contradiction')
  })

  it('puts a work into the tooltip statement (year, DOI, contradiction markers)', () => {
    const statement = nodeById.get('c2')!.statement ?? ''
    expect(statement).toContain('2020')
    expect(statement).toContain('DOI: 10.1/crit')
    expect(statement).toContain('**Contradiction markers:** critique')
  })
})

describe('identity helpers', () => {
  it('prefers DOI and falls back to the title for the dedupe identity', () => {
    expect(workIdentity(work({ doi: '10.1/x', title: 'T' }))).toBe('10.1/x')
    expect(workIdentity(work({ title: 'The Title' }))).toBe('the title')
  })

  it('falls back to the DOI for a missing title', () => {
    expect(workDisplayTitle(work({ doi: '10.1/y' }))).toBe('10.1/y')
    expect(workDisplayTitle(work())).toBe('(untitled)')
  })
})

describe('literatureGeneratedAtLabel', () => {
  it('renders a compact "generated <date>" staleness label (UTC)', () => {
    expect(literatureGeneratedAtLabel('2026-09-16T12:34:56Z')).toBe('generated 2026-09-16')
  })

  it('omits the label for an absent or unparseable timestamp', () => {
    expect(literatureGeneratedAtLabel('')).toBe('')
    expect(literatureGeneratedAtLabel('   ')).toBe('')
    expect(literatureGeneratedAtLabel('not-a-date')).toBe('')
  })
})
