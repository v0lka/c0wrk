// Dispatch constants and pure helpers for the paper-study invocation surface —
// the literature ("papers") showcase in the Research panel AND every
// invoke-from-anywhere entry point that dispatches the `study-paper` skill
// (the file-tree PDF menu, the chat-attachment Study gesture, the research
// prior-art Deep-read row).
//
// Mirrors `researchActions.ts`: every prompt dispatched by a paper gesture
// lives here as a constant or a small builder so the wiring stays auditable
// and the components render only. The actual `send()` call (and its error
// routing) lives in each component.

import type { PaperRecord } from '@/api/papers'

/** The skill that owns studying, appraising, and comparing papers. */
export const STUDY_PAPER_SKILL = 'study-paper'

/** The skill that owns formulating a research hypothesis. */
export const RESEARCH_HYPOTHESIS_SKILL = 'research-hypothesis'

/** Reading depth offered by the mode selector. `auto` lets the study-paper
 *  skill infer the depth from the reference (its "choose the mode" step);
 *  the rest force one of the skill's four modes. */
export type StudyMode = 'auto' | 'skim' | 'review' | 'implement' | 'teach'

/** The mode selector's options, in display order. */
export const STUDY_MODE_OPTIONS: ReadonlyArray<{ value: StudyMode; label: string }> = [
  { value: 'auto', label: 'Auto' },
  { value: 'skim', label: 'Skim' },
  { value: 'review', label: 'Review' },
  { value: 'implement', label: 'Implement' },
  { value: 'teach', label: 'Teach' },
]

/** The reading depth used when a gesture does not force one — the skill then
 *  infers it from the reference (its "choose the mode" step). */
export const DEFAULT_STUDY_MODE: StudyMode = 'auto'

/** The reading depth the prior-art Deep-read gesture forces: the
 *  full-appraisal pass the skill's `review` mode implements. */
export const PRIOR_ART_DEEP_READ_MODE: StudyMode = 'review'

/** Append the forced-depth clause to a study prompt. `auto` adds nothing so
 *  the skill infers the depth; any other mode names it explicitly. */
function withStudyMode(base: string, mode: StudyMode): string {
  return mode === 'auto' ? base : `${base} Use ${mode} mode.`
}

/**
 * A human-readable reference for a paper: its title plus the first identifier
 * when one exists — e.g. `Attention Is All You Need (arxiv:1706.03762)`.
 */
export function paperDisplayRef(paper: PaperRecord): string {
  const ident = paper.identifiers[0]
  return ident ? `${paper.title} (${ident.scheme}:${ident.value})` : paper.title
}

/**
 * The most precise reference for a paper that already has a card in the
 * library: its research-root-relative card path (`papers/<slug>/paper.md`)
 * when present, else the display reference.
 */
export function paperCardRef(paper: PaperRecord): string {
  return paper.card_path !== '' ? paper.card_path : paperDisplayRef(paper)
}

/**
 * The prompt dispatched by the "Study paper" field: the raw reference the user
 * pasted (an arXiv id/URL, a DOI, a citation, or a local PDF path), plus the
 * chosen study mode. `auto` adds no mode clause — the skill infers the depth.
 */
export function buildStudyPrompt(reference: string, mode: StudyMode): string {
  const base = `Study this paper: ${reference.trim()}.`
  return mode === 'auto' ? base : `${base} Use ${mode} mode.`
}

/** The study-paper skill's reading-depth ladder, shallow → deep. A "Go deeper"
 *  gesture escalates exactly one rung (per the skill: a skim becomes a review,
 *  a review becomes an implement pass, and so on); the deepest rung is a fixed
 *  point that saturates instead of overflowing. */
export const STUDY_MODE_LADDER: ReadonlyArray<Exclude<StudyMode, 'auto'>> = [
  'skim',
  'review',
  'implement',
  'teach',
]

/**
 * How deep a paper's recorded card mode sits on the ladder. The card mode
 * (`core/papers.Mode`) now carries the skill's own vocabulary
 * (skim / review / implement / teach) alongside the legacy engagement depths
 * (skim / deep / survey); both map onto the same shallow → deep axis, with
 * `deep` (read in full, claims verified) folded onto the `review` rung.
 * Anything shallower — or an unmapped/absent value — sits at the bottom, so an
 * unknown mode degrades to the shallowest rung rather than being rejected.
 */
function cardDepthRank(mode: string): number {
  switch (mode) {
    case 'implement':
      return 2
    case 'teach':
      return 3
    case 'deep':
    case 'review':
      return 1
    default:
      return 0
  }
}

/**
 * The concrete (never `auto`) mode a "Go deeper" gesture dispatches for a
 * paper: exactly one rung above the paper's recorded depth, saturating at the
 * deepest rung. A paper only skimmed deepens into a review; one already read in
 * full escalates into an implement pass.
 */
export function nextStudyMode(paper: PaperRecord): Exclude<StudyMode, 'auto'> {
  const next = Math.min(cardDepthRank(paper.mode) + 1, STUDY_MODE_LADDER.length - 1)
  return STUDY_MODE_LADDER[next]!
}

/** The prompt dispatched by the "Go deeper" gesture: re-enters the SAME card
 *  (same slug) one mode deeper and states the append-only invariant explicitly,
 *  so the skill extends the existing note/appraisal sections instead of
 *  rewriting or dropping them (see study-paper's "Append, never rewrite"). */
export function buildDeepenPrompt(paper: PaperRecord): string {
  const mode = nextStudyMode(paper)
  const target =
    paper.slug !== '' ? `${paperCardRef(paper)} (slug "${paper.slug}")` : paperCardRef(paper)
  return (
    `Deepen the reading of ${target} — go one level deeper and use ${mode} mode. ` +
    `Stay on the same card and APPEND only: keep every section already written and add the ` +
    `new findings — never rewrite or delete existing content.`
  )
}

/** The prompt dispatched by a row's Compare gesture. */
export function buildComparePrompt(paper: PaperRecord): string {
  return `Compare ${paperCardRef(paper)} with the other papers in the library.`
}

/** The research-root subdirectory holding multi-paper comparison artifacts, as
 *  a research-root-relative reference. Mirrors backend `config.ComparisonDirName`
 *  (`<research-root>/comparisons`). */
export const COMPARISONS_SUBDIR = 'comparisons'

/**
 * The most precise reference for one paper in a comparison entry: its first
 * identifier as `scheme:value` (e.g. `arxiv:1706.03762`), else its card path,
 * else its title. A comparison artifact's "Papers under comparison" table
 * records exactly these, which is how the reader later matches a comparison
 * back to the paper that takes part in it.
 */
export function paperIdentifierRef(paper: PaperRecord): string {
  const identifier = paper.identifiers[0]
  if (identifier !== undefined && identifier.value !== '') {
    return `${identifier.scheme}:${identifier.value}`
  }
  if (paper.card_path !== '') return paper.card_path
  return paper.title
}

/**
 * The comparisons directory reference for a research root. With a known
 * absolute root this is `<research-root>/comparisons`; without one it degrades
 * to the bare `comparisons` subdirectory (the skill falls back to a
 * workspace-relative location).
 */
export function comparisonsRef(researchRoot: string): string {
  const root = researchRoot.replace(/[\\/]+$/, '')
  return root === '' ? COMPARISONS_SUBDIR : `${root}/${COMPARISONS_SUBDIR}`
}

/**
 * A deterministic slug for a comparison set, derived from the member papers'
 * slugs (falling back to their lowercased ids): `a-vs-b-vs-c`. Used to name the
 * `<slug>.md` artifact so re-running the same set overwrites its own file
 * instead of accumulating near-duplicates.
 */
export function comparisonSlug(papers: PaperRecord[]): string {
  return papers
    .map((paper) => (paper.slug !== '' ? paper.slug : paper.id.toLowerCase()))
    .join('-vs-')
}

/**
 * The prompt dispatched by the "Compare selected" gesture: the study-paper
 * Compare intent over two or more papers, listing each paper's identifier and
 * naming the artifact path under the research root's `comparisons/` directory.
 * The Fairness / Agreement / Gaps sections come from the skill's
 * comparison-matrix template.
 */
export function buildCompareSelectedPrompt(papers: PaperRecord[], researchRoot: string): string {
  const refs = papers.map((paper) => `- ${paper.id} — ${paperIdentifierRef(paper)}`).join('\n')
  const target = `${comparisonsRef(researchRoot)}/${comparisonSlug(papers)}.md`
  return (
    `Compare these ${papers.length} papers using the study-paper Compare intent (the ` +
    `comparison-matrix template) and write the matrix — with the Fairness & comparability, ` +
    `Agreement/Disagreement, and Gaps sections — to ${target}:\n${refs}`
  )
}

/**
 * The paper's recorded open questions / gaps, formatted as prompt bullet lines
 * (empty entries dropped). An uncertainty carrying a detail renders as
 * ``item — detail``; the list is empty when the card recorded none.
 */
export function paperGaps(paper: PaperRecord): string[] {
  const out: string[] = []
  for (const gap of paper.uncertainties) {
    const item = gap.item.trim()
    const detail = gap.detail.trim()
    const line = detail === '' ? item : `${item} — ${detail}`
    if (line !== '') out.push(line)
  }
  return out
}

/** The prompt dispatched by the "Suggest hypotheses from gaps" gesture: asks
 *  the research-hypothesis skill to formulate hypotheses that close the paper's
 *  recorded uncertainties / open questions. With no recorded gaps it falls back
 *  to a generic proposal against the paper. The paper is auto-pinned as prior
 *  art by the caller (a side effect — not part of the prompt). */
export function buildProposeHypothesisPrompt(paper: PaperRecord): string {
  const base = `Propose research hypotheses informed by ${paperDisplayRef(paper)}.`
  const gaps = paperGaps(paper)
  if (gaps.length === 0) return base
  const bullets = gaps.map((gap) => `- ${gap}`).join('\n')
  return `${base} Ground them in the paper's open questions and gaps:\n${bullets}`
}

// --- Invoke-from-anywhere entry points -----------------------------------
// These prompts drive the study-paper skill from surfaces OUTSIDE the Papers
// panel: the file-tree PDF menu (a path reference, via buildStudyPrompt), the
// chat-attachment Study gesture, and the research prior-art Deep-read row.

/**
 * The prompt dispatched by the chat-attachment "Study" gesture: a staged PDF is
 * referenced by its file name (the backend exposes no on-disk path for
 * documents), at the caller's chosen reading depth.
 */
export function buildStudyAttachmentPrompt(fileName: string, mode: StudyMode): string {
  return withStudyMode(`Study this attached paper: ${fileName.trim()}.`, mode)
}

/**
 * The prompt dispatched by the research panel's prior-art Deep-read gesture:
 * the catalog's research-root-relative path, read at `mode` (defaulting to the
 * forced deep pass) so the referenced works are appraised, not skimmed.
 */
export function buildDeepReadPriorArtPrompt(
  priorArtPath: string,
  mode: StudyMode = PRIOR_ART_DEEP_READ_MODE,
): string {
  return withStudyMode(
    `Deep read the prior-art catalog at ${priorArtPath.trim()} and study the referenced works in depth.`,
    mode,
  )
}
