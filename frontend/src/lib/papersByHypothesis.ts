// Reverse of the paper → research link: which papers inform a given
// hypothesis (H-NNN)?
//
// The forward direction already exists end-to-end: a paper card declares
// `research_ids` (H-NNN), the backend resolves each to the owning R-NNN
// project(s) (`linked_research`), and the paper's Overview renders them as
// "Research links". Nothing answers the reverse question — "which papers
// inform H-003?" — so this module DERIVES it client-side by folding the live
// `paperStore.papers` against the live hypothesis graph nodes.
//
// Both inputs are already on the frontend, so the projection is pure, free,
// and keeps the paper cards as the single source of truth: no new RPC, no new
// persisted file. A backend projection would re-do the same join and ship it
// over a new RPC for no benefit.
//
// Selector discipline (React #185): the fold is a pure function; the hook
// wrappers read only DIRECT store references (the `papers` array, the `root`
// object) and do every allocation inside `useMemo` — never inside a Zustand
// selector, which must return a primitive or a direct reference.
//
// See specs/domains/research.md → "Informing papers (hypothesis ← paper)".

import { useMemo } from 'react'
import { usePaperStore, selectPapers } from '@/stores/paperStore'
import { useResearchStore } from '@/stores/researchStore'
import type { PaperRecord } from '@/api/papers'
import type { HypothesisNode, ResearchRoot } from '@/types/models'

/** The derived reverse index: every hypothesis that has ≥1 informing paper,
 *  plus the ids that resolve to no known hypothesis (surfaced, not dropped). */
export interface PapersByHypothesisIndex {
  /** Normalized H-NNN → the papers informing it, in library order. Each paper
   *  appears at most once per id. */
  byHypothesis: Map<string, PaperRecord[]>
  /** Normalized ids that resolve to NO known hypothesis → the papers claiming
   *  them. A dangling link is surfaced here instead of being silently
   *  dropped, so the UI can flag a card that names a nonexistent hypothesis. */
  dangling: Map<string, PaperRecord[]>
  /** Distinct known hypotheses with ≥1 informing paper. */
  informedCount: number
  /** Total dangling links (paper × unknown id). */
  danglingCount: number
}

/** Stable empty reference so a hook never allocates a fresh array on a miss. */
const EMPTY_PAPERS: PaperRecord[] = []

/**
 * Canonicalize an H-NNN hypothesis id for a spelling-tolerant join: `h-3`,
 * `H-03`, `H-003` and ` H-3 ` all fold to `H-003`. A value that is not an
 * H-NNN shape is returned trimmed and upper-cased, so two identical spellings
 * still match; `` stays ``.
 */
export function normalizeHypothesisId(id: string): string {
  const trimmed = id.trim()
  if (trimmed === '') return ''
  const match = /^h-(\d+)$/i.exec(trimmed)
  if (match !== null) return `H-${String(Number(match[1])).padStart(3, '0')}`
  return trimmed.toUpperCase()
}

/**
 * Fold `papers × nodes` into the reverse index. Pure over its inputs (no
 * store, no React) so it is unit-testable in isolation.
 *
 * A paper contributes one entry per DISTINCT normalized id in its
 * `research_ids` (a card listing the same hypothesis twice — or in two
 * spellings — is counted once). An id present in `nodes` feeds the
 * `byHypothesis` bucket; an id present in no node feeds `dangling`.
 */
export function buildPapersByHypothesis(
  papers: readonly PaperRecord[],
  nodes: readonly HypothesisNode[],
): PapersByHypothesisIndex {
  const known = new Set<string>()
  for (const node of nodes) {
    const id = normalizeHypothesisId(node.id)
    if (id !== '') known.add(id)
  }

  const byHypothesis = new Map<string, PaperRecord[]>()
  const dangling = new Map<string, PaperRecord[]>()

  for (const paper of papers) {
    const seen = new Set<string>()
    for (const raw of paper.research_ids) {
      const id = normalizeHypothesisId(raw)
      if (id === '' || seen.has(id)) continue
      seen.add(id)
      const bucket = known.has(id) ? byHypothesis : dangling
      const list = bucket.get(id)
      if (list === undefined) bucket.set(id, [paper])
      else list.push(paper)
    }
  }

  let danglingCount = 0
  for (const list of dangling.values()) danglingCount += list.length

  return { byHypothesis, dangling, informedCount: byHypothesis.size, danglingCount }
}

/** Stable empty node list for a missing root. */
const EMPTY_NODES: HypothesisNode[] = []

/**
 * Every hypothesis node across every project of a research root — the join
 * universe.
 *
 * The paper library is GLOBAL across the research root while a graph node is
 * scoped to one R-NNN project, so a raw `research_ids` id must match a node in
 * ANY project to count as resolved; otherwise a paper legitimately citing
 * another project's hypothesis would be misreported as dangling. Ids are
 * generic across projects (H-001 exists in every R-NNN), so the reverse index
 * is inherently keyed by id across the root — the same convention the pinned
 * hypotheses use.
 */
export function collectHypothesisNodes(root: ResearchRoot | undefined): HypothesisNode[] {
  if (root === undefined) return EMPTY_NODES
  const nodes: HypothesisNode[] = []
  for (const project of root.projects) {
    for (const node of project.graph.nodes) nodes.push(node)
  }
  return nodes
}

/**
 * The live reverse index over `paperStore.papers` and the active research
 * root's hypothesis nodes. Reads only direct store references; both the node
 * collection and the fold run inside `useMemo`.
 */
export function usePapersByHypothesis(): PapersByHypothesisIndex {
  const papers = usePaperStore(selectPapers)
  const root = useResearchStore((state) => state.status?.root)
  const nodes = useMemo(() => collectHypothesisNodes(root), [root])
  return useMemo(() => buildPapersByHypothesis(papers, nodes), [papers, nodes])
}

/**
 * The papers informing one hypothesis, in library order (a stable empty array
 * when none). The hypothesis id is normalized before lookup, so H-3 / H-003
 * resolve to the same bucket.
 */
export function useInformingPapers(hypothesisId: string): PaperRecord[] {
  const index = usePapersByHypothesis()
  const key = normalizeHypothesisId(hypothesisId)
  return useMemo(() => index.byHypothesis.get(key) ?? EMPTY_PAPERS, [index, key])
}

/**
 * The dangling reverse links: normalized id → the papers claiming it, for ids
 * that resolve to no known hypothesis. The returned Map is the memoized index's
 * own reference (no allocation in a selector).
 */
export function useDanglingPaperLinks(): Map<string, PaperRecord[]> {
  return usePapersByHypothesis().dangling
}
