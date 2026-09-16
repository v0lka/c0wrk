// Literature-graph mapping — turns the study-paper `literature.py` JSON output
// into a hypothesis-shaped DAG so the paper workspace can render it with the
// EXISTING research DAG components (`researchDagRender.layoutDag` +
// `ResearchDagCanvas`) instead of a bespoke canvas.
//
// The helper groups one seed paper with the works it references (predecessors),
// the works that cite it (citing), and a heuristic shortlist of contradiction
// candidates (a deduped subset of the two sets carrying refutation markers). We
// project that onto a left-to-right DAG:
//
//      predecessor ─┐
//      predecessor ─┼─▶  seed  ─┬─▶  citing
//                   │           └─▶  citing
//      contradiction ── re-tags the matching node (either side)
//
// i.e. a predecessor is a PARENT of the seed, a citing work a CHILD of it, and a
// contradiction is re-tagged so it paints in the destructive colour while
// keeping its place in the graph. The node's `status` field carries the kind,
// because the shared DAG canvas colours nodes through
// `researchDagRender.statusColorVar(status)`.
//
// Pure — no DOM, no React — and unit-tested in isolation. The JSON boundary is
// validated here too: a malformed payload yields an explicit error rather than a
// half-built graph.

import type { HypothesisEdge, HypothesisGraph, HypothesisNode } from '@/types/models'

export type LiteratureWorkKind = 'seed' | 'predecessor' | 'citing' | 'contradiction'

/** Stable id of the seed node in the projected graph. */
export const LITERATURE_SEED_ID = 'seed'

/** Display order of the kinds (legend). */
export const LITERATURE_KIND_ORDER: readonly LiteratureWorkKind[] = [
  'seed',
  'predecessor',
  'citing',
  'contradiction',
]

/** A single work in the literature record (mirrors literature.py's `_work_summary`). */
export interface LiteratureWork {
  title: string
  year: number | null
  doi: string
  openalex: string
  citedBy: number | null
  authors: string[]
  origin: string
  abstract: string
  /** Contradiction markers that fired for this work (empty unless flagged). */
  reasons: string[]
}

/** The normalized literature.json payload. */
export interface LiteratureRecord {
  seed: LiteratureWork
  seedInput: string
  seedMatch: string
  direction: string
  predecessors: LiteratureWork[]
  citing: LiteratureWork[]
  contradictions: LiteratureWork[]
  sourcesUsed: string[]
  rateLimitEvents: number
  notes: string[]
  /**
   * ISO-8601 UTC instant the helper stamped into the file (`generated_at`), or
   * '' for a legacy payload written before the field existed. Surfaced through
   * `parseLiteratureJson(...).record.generatedAt` so the UI can flag staleness.
   */
  generatedAt: string
}

/** Outcome of parsing a raw literature.json string. */
export interface LiteratureParseResult {
  record: LiteratureRecord | null
  error: string | null
}

/** The graph projection consumed by the paper workspace. */
export interface LiteratureGraphModel {
  graph: HypothesisGraph
  /** Node id → work kind (colour + legend source). */
  kinds: Record<string, LiteratureWorkKind>
  /** Node id → the underlying work (detail-panel source). */
  works: Record<string, LiteratureWork>
}

// The DAG canvas colours nodes via researchDagRender.statusColorVar; every kind
// maps to an existing hypothesis status so we reuse that palette unchanged.
const KIND_STATUS: Record<LiteratureWorkKind, string> = {
  seed: 'in-progress',
  predecessor: 'cancelled',
  citing: 'confirmed',
  contradiction: 'refuted',
}

const KIND_LABEL: Record<LiteratureWorkKind, string> = {
  seed: 'Seed',
  predecessor: 'Predecessor',
  citing: 'Citing',
  contradiction: 'Contradiction',
}

/** Human label for a work kind (legend + tooltip). */
export function literatureKindLabel(kind: LiteratureWorkKind): string {
  return KIND_LABEL[kind]
}

/** The hypothesis status a work kind paints as (via statusColorVar). */
export function literatureKindStatus(kind: LiteratureWorkKind): string {
  return KIND_STATUS[kind]
}

/**
 * A one-line staleness label for a record's `generated_at`, e.g.
 * "generated 2026-09-16". Returns '' when the timestamp is absent or
 * unparseable, so the UI omits the hint rather than rendering junk. The date is
 * rendered in UTC (the helper stamps UTC), which keeps the label deterministic
 * across locales.
 */
export function literatureGeneratedAtLabel(generatedAt: string): string {
  const iso = generatedAt.trim()
  if (iso === '') return ''
  const at = new Date(iso)
  if (Number.isNaN(at.getTime())) return ''
  const year = at.getUTCFullYear()
  const month = String(at.getUTCMonth() + 1).padStart(2, '0')
  const day = String(at.getUTCDate()).padStart(2, '0')
  return `generated ${year}-${month}-${day}`
}

// ── Boundary validation ────────────────────────────────────────────────

function isRecord(v: unknown): v is Record<string, unknown> {
  return typeof v === 'object' && v !== null && !Array.isArray(v)
}

function asString(v: unknown): string {
  return typeof v === 'string' ? v : ''
}

function asStringArray(v: unknown): string[] {
  if (!Array.isArray(v)) return []
  return v.filter((entry): entry is string => typeof entry === 'string')
}

function asNumberOrNull(v: unknown): number | null {
  return typeof v === 'number' && Number.isFinite(v) ? v : null
}

/** Coerce one work entry, tolerating missing/extra fields. */
function asWork(v: unknown): LiteratureWork {
  const r = isRecord(v) ? v : {}
  return {
    title: asString(r['title']),
    year: asNumberOrNull(r['year']),
    doi: asString(r['doi']),
    openalex: asString(r['openalex']),
    citedBy: asNumberOrNull(r['cited_by']),
    authors: asStringArray(r['authors']),
    origin: asString(r['origin']),
    abstract: asString(r['abstract']),
    reasons: asStringArray(r['reasons']),
  }
}

function asWorkList(v: unknown): LiteratureWork[] {
  if (!Array.isArray(v)) return []
  return v.map(asWork)
}

/**
 * Parse + validate a raw literature.json string. Returns an explicit error for
 * empty/invalid/undocumented payloads (never a partial record), so the UI can
 * degrade honestly instead of rendering an empty graph.
 */
export function parseLiteratureJson(raw: string): LiteratureParseResult {
  if (raw.trim() === '') return { record: null, error: 'empty payload' }
  let parsed: unknown
  try {
    parsed = JSON.parse(raw)
  } catch (err) {
    return { record: null, error: `invalid JSON: ${err instanceof Error ? err.message : String(err)}` }
  }
  if (!isRecord(parsed)) return { record: null, error: 'payload is not a JSON object' }
  if (!isRecord(parsed['seed'])) {
    return { record: null, error: 'payload has no "seed" object' }
  }
  const record: LiteratureRecord = {
    seed: asWork(parsed['seed']),
    seedInput: asString(parsed['seed_input']),
    seedMatch: asString(parsed['seed_match']),
    direction: asString(parsed['direction']),
    predecessors: asWorkList(parsed['predecessors']),
    citing: asWorkList(parsed['citing']),
    contradictions: asWorkList(parsed['contradictions']),
    sourcesUsed: asStringArray(parsed['sources_used']),
    rateLimitEvents: asNumberOrNull(parsed['rate_limit_events']) ?? 0,
    notes: asStringArray(parsed['notes']),
    generatedAt: asString(parsed['generated_at']),
  }
  return { record, error: null }
}

/** The identity used to dedupe a work across the sets (DOI, else title). */
export function workIdentity(work: LiteratureWork): string {
  return (work.doi || work.title).trim().toLowerCase()
}

/** A compact one-line reference for a work (title, else DOI, else '(untitled)'). */
export function workDisplayTitle(work: LiteratureWork): string {
  const title = work.title.trim()
  if (title !== '') return title
  return work.doi.trim() !== '' ? work.doi : '(untitled)'
}

/** Markdown detail block for a node's hover tooltip card. */
function workStatement(work: LiteratureWork): string {
  const meta: string[] = []
  if (work.year !== null) meta.push(String(work.year))
  if (work.citedBy !== null) meta.push(`${work.citedBy} citations`)
  const lines: string[] = []
  if (meta.length > 0) lines.push(`**${meta.join(' · ')}**`)
  if (work.authors.length > 0) lines.push(`Authors: ${work.authors.join(', ')}`)
  if (work.doi.trim() !== '') lines.push(`DOI: ${work.doi}`)
  if (work.openalex.trim() !== '') lines.push(`OpenAlex: ${work.openalex}`)
  if (work.origin.trim() !== '') lines.push(`Source: ${work.origin}`)
  if (work.reasons.length > 0) lines.push(`**Contradiction markers:** ${work.reasons.join(', ')}`)
  return lines.join('\n\n')
}

/**
 * Project a literature record onto a hypothesis-shaped DAG. The seed is the hub;
 * predecessors become its parents (left column) and citing works its children
 * (right column). A contradiction — a deduped work drawn from either set — keeps
 * its position but is re-tagged so it paints destructively. Deterministic: node
 * ids follow input order (`seed`, `p1…`, `c1…`), and a work appearing in both
 * sets resolves to ONE node via its identity (DOI, else title).
 */
export function literatureToGraph(record: LiteratureRecord): LiteratureGraphModel {
  const order: string[] = []
  const drafts = new Map<string, LiteratureWork>()
  const kinds = new Map<string, LiteratureWorkKind>()
  const parentsOf = new Map<string, string[]>()
  const byKey = new Map<string, string>()

  const parentsFor = (id: string): string[] => {
    const existing = parentsOf.get(id)
    if (existing !== undefined) return existing
    const created: string[] = []
    parentsOf.set(id, created)
    return created
  }

  const addNode = (id: string, work: LiteratureWork, kind: LiteratureWorkKind): void => {
    order.push(id)
    drafts.set(id, work)
    kinds.set(id, kind)
    parentsFor(id)
  }

  // Resolve a work to an existing node id (by identity) or create a new one.
  const resolve = (
    work: LiteratureWork,
    fallbackId: string,
    kind: LiteratureWorkKind,
  ): string => {
    const key = workIdentity(work)
    if (key !== '') {
      const known = byKey.get(key)
      if (known !== undefined) return known
    }
    addNode(fallbackId, work, kind)
    if (key !== '') byKey.set(key, fallbackId)
    return fallbackId
  }

  // Seed hub.
  addNode(LITERATURE_SEED_ID, record.seed, 'seed')
  const seedKey = workIdentity(record.seed)
  if (seedKey !== '') byKey.set(seedKey, LITERATURE_SEED_ID)

  // Predecessors are the seed's parents.
  const seedParents = parentsFor(LITERATURE_SEED_ID)
  record.predecessors.forEach((work, index) => {
    const id = resolve(work, `p${index + 1}`, 'predecessor')
    if (!seedParents.includes(id)) seedParents.push(id)
  })

  // Citing works are the seed's children (their parent is the seed).
  record.citing.forEach((work, index) => {
    const id = resolve(work, `c${index + 1}`, 'citing')
    const parents = parentsFor(id)
    if (!parents.includes(LITERATURE_SEED_ID)) parents.push(LITERATURE_SEED_ID)
  })

  // Contradictions re-tag the matching node (from either set); an orphan not in
  // the pool is added standalone so the flag is never silently dropped.
  record.contradictions.forEach((work, index) => {
    const key = workIdentity(work)
    const known = key !== '' ? byKey.get(key) : undefined
    if (known !== undefined) {
      kinds.set(known, 'contradiction')
      drafts.set(known, work) // keeps the work's `reasons`
      return
    }
    resolve(work, `x${index + 1}`, 'contradiction')
  })

  const nodes: HypothesisNode[] = []
  const edges: HypothesisEdge[] = []
  for (const id of order) {
    const work = drafts.get(id)
    const kind = kinds.get(id)
    if (work === undefined || kind === undefined) continue
    const parents = parentsFor(id)
    nodes.push({
      id,
      title: workDisplayTitle(work),
      status: KIND_STATUS[kind],
      parents: [...parents],
      statement: workStatement(work),
    })
    for (const parent of parents) edges.push({ from: parent, to: id })
  }

  return { graph: { nodes, edges }, kinds: Object.fromEntries(kinds), works: Object.fromEntries(drafts) }
}
