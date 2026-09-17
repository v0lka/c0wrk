// Paper library API wrappers.
//
// The literature ("papers") library is a global subdirectory of a project's
// EFFECTIVE research root — `<research-root>/papers/<slug>/{paper.md, note.md,
// appraisal.md}` — where `<research-root>` is the persisted research root when
// RESEARCH is enabled, else the default `<workspace>/.research`. The library
// lives and is watched INDEPENDENTLY of the RESEARCH toggle. See
// specs/contracts/desktop-frontend.md → Papers.
//
// Every response is validated and normalized at this boundary: Go serializes a
// nil slice as `null`, so collections are made non-nil, and a malformed paper
// ENTRY is dropped (per-entry fail-closed) instead of surfacing as an uncaught
// render failure downstream.

import { getApp } from './runtime'
import { logger } from '@/lib/logger'

// --- View model (hand-written mirror of backend.PapersDTO / backend.PaperDTO) ---

/** A paper → research link: the card's `research_ids` entry (H-NNN) resolved
 *  to the R-NNN project that owns it. An id that resolves to no project yields
 *  a dangling link (`research_id === ''`). */
export interface PaperResearchLink {
  hypothesis_id: string
  research_id: string
}

/** One bibliographic identifier (DOI, arXiv, …). */
export interface PaperIdentifier {
  scheme: string
  value: string
}

/** A located quotation/section anchor from the source document. */
export interface PaperAnchor {
  label: string
  ref: string
  note: string
}

/** A claim extracted from the paper, with its supporting evidence. */
export interface PaperClaim {
  claim: string
  evidence: string
  location: string
  stance: string
}

/** A methodological/quality red flag noted during appraisal. */
export interface PaperRedFlag {
  flag: string
  detail: string
  severity: string
}

/** An open uncertainty the reader recorded. */
export interface PaperUncertainty {
  item: string
  detail: string
}

/** Reading depth of the paper card (mirrors core/papers.Mode). '' when absent.
 *  The card vocabulary (skim/deep/survey) and the study-paper skill's own modes
 *  (review/implement/teach) share one shallow → deep axis. */
export type PaperMode = 'skim' | 'deep' | 'survey' | 'review' | 'implement' | 'teach' | ''
/** Reading DECISION of the paper card (mirrors core/papers.Reading) — how much
 *  of the paper to read. Distinct from `verdict` (the soundness judgement).
 *  '' when absent. */
export type PaperReading = 'full' | 'selective' | 'skip' | ''
/** Appraisal verdict (mirrors core/papers.Verdict). '' when absent. */
export type PaperVerdict = 'accepted' | 'rejected' | 'uncertain' | ''
/** Appraisal confidence (mirrors core/papers.Confidence). '' when absent. */
export type PaperConfidence = 'low' | 'medium' | 'high' | ''

/** The normalized wire shape of a single paper (backend.PaperDTO). */
export interface PaperRecord {
  /** Normalized paper id (`P-NNN`, zero-padded). */
  id: string
  slug: string
  title: string
  authors: string[]
  year: number
  venue: string
  identifiers: PaperIdentifier[]
  mode: PaperMode
  reading: PaperReading
  verdict: PaperVerdict
  confidence: PaperConfidence
  /** Raw `research_ids` (H-NNN) declared on the card. */
  research_ids: string[]
  anchors: PaperAnchor[]
  claims: PaperClaim[]
  red_flags: PaperRedFlag[]
  uncertainties: PaperUncertainty[]
  /** Absolute paper directory the record was parsed from (for the file viewer). */
  dir: string
  /** The card's document path relative to the research root, forward slashes —
   *  the pin-path form `papers/<slug>/paper.md`. */
  card_path: string
  /** Whether this paper's card is currently pinned. */
  pinned: boolean
  /** `research_ids` resolved to the owning R-NNN research project(s). */
  linked_research: PaperResearchLink[]
}

/** Response for GetPapers: the project's normalized paper library. */
export interface PaperLibrary {
  project_id: string
  /** The project's effective research root (the library is a subdirectory). */
  research_root: string
  /** Absolute library root (`<research-root>/papers`). */
  root: string
  papers: PaperRecord[]
  /** Pinned paper card paths (research-root-relative, forward slashes). */
  pinned: string[]
}

/** Payload of the global `papers:changed` event. */
export interface PapersChangedPayload {
  project_id: string
  /** Comma-separated list of changed absolute paths. */
  paths: string
}

// --- Boundary helpers ---

function isRecord(v: unknown): v is Record<string, unknown> {
  return typeof v === 'object' && v !== null
}

/** `[]`, `null`, and missing are all accepted (Go serializes nil slices as
 *  `null`); anything else — or a non-string entry — fails the check. */
function isStringArrayOrMissing(v: unknown): boolean {
  if (v === undefined || v === null) return true
  if (!Array.isArray(v)) return false
  return v.every((entry) => typeof entry === 'string')
}

/** Coerce an optional wire string to a plain string ('' when absent). */
function asString(v: unknown): string {
  return typeof v === 'string' ? v : ''
}

/** Coerce an optional wire string list to a non-nil string list. */
function asStringArray(v: unknown): string[] {
  if (!Array.isArray(v)) return []
  return v.filter((entry): entry is string => typeof entry === 'string')
}

const PAPER_MODES: ReadonlySet<string> = new Set([
  'skim',
  'deep',
  'survey',
  'review',
  'implement',
  'teach',
])
const PAPER_READINGS: ReadonlySet<string> = new Set(['full', 'selective', 'skip'])
const PAPER_VERDICTS: ReadonlySet<string> = new Set(['accepted', 'rejected', 'uncertain'])
const PAPER_CONFIDENCES: ReadonlySet<string> = new Set(['low', 'medium', 'high'])

/** Fold a wire enum string to its declared union (unknown → '', so the UI can
 *  switch exhaustively without the boundary rejecting a future enum value). */
function asEnum<T extends string>(v: unknown, allowed: ReadonlySet<string>): T | '' {
  return typeof v === 'string' && allowed.has(v) ? (v as T) : ''
}

function identifiersOf(v: unknown): PaperIdentifier[] {
  if (!Array.isArray(v)) return []
  const out: PaperIdentifier[] = []
  for (const entry of v) {
    if (!isRecord(entry)) continue
    if (typeof entry['scheme'] !== 'string' || typeof entry['value'] !== 'string') continue
    out.push({ scheme: entry['scheme'], value: entry['value'] })
  }
  return out
}

function anchorsOf(v: unknown): PaperAnchor[] {
  if (!Array.isArray(v)) return []
  const out: PaperAnchor[] = []
  for (const entry of v) {
    if (!isRecord(entry)) continue
    out.push({ label: asString(entry['label']), ref: asString(entry['ref']), note: asString(entry['note']) })
  }
  return out
}

function claimsOf(v: unknown): PaperClaim[] {
  if (!Array.isArray(v)) return []
  const out: PaperClaim[] = []
  for (const entry of v) {
    if (!isRecord(entry) || typeof entry['claim'] !== 'string') continue
    out.push({
      claim: entry['claim'],
      evidence: asString(entry['evidence']),
      location: asString(entry['location']),
      stance: asString(entry['stance']),
    })
  }
  return out
}

function redFlagsOf(v: unknown): PaperRedFlag[] {
  if (!Array.isArray(v)) return []
  const out: PaperRedFlag[] = []
  for (const entry of v) {
    if (!isRecord(entry) || typeof entry['flag'] !== 'string') continue
    out.push({
      flag: entry['flag'],
      detail: asString(entry['detail']),
      severity: asString(entry['severity']),
    })
  }
  return out
}

function uncertaintiesOf(v: unknown): PaperUncertainty[] {
  if (!Array.isArray(v)) return []
  const out: PaperUncertainty[] = []
  for (const entry of v) {
    if (!isRecord(entry) || typeof entry['item'] !== 'string') continue
    out.push({ item: entry['item'], detail: asString(entry['detail']) })
  }
  return out
}

function linksOf(v: unknown): PaperResearchLink[] {
  if (!Array.isArray(v)) return []
  const out: PaperResearchLink[] = []
  for (const entry of v) {
    if (!isRecord(entry) || typeof entry['hypothesis_id'] !== 'string') continue
    out.push({ hypothesis_id: entry['hypothesis_id'], research_id: asString(entry['research_id']) })
  }
  return out
}

/**
 * Normalize a single wire PaperDTO. Returns null when a required identity
 * field (id/slug/title/dir/card_path) is missing — the caller drops the entry
 * (per-entry fail-closed) instead of rejecting the whole library.
 */
function normalizePaper(v: unknown): PaperRecord | null {
  if (!isRecord(v)) return null
  const { id, slug, title, dir, card_path: cardPath } = v
  if (typeof id !== 'string' || typeof slug !== 'string' || typeof title !== 'string') return null
  if (typeof dir !== 'string' || typeof cardPath !== 'string') return null
  const year = v['year']
  return {
    id,
    slug,
    title,
    authors: asStringArray(v['authors']),
    year: typeof year === 'number' && Number.isFinite(year) ? year : 0,
    venue: asString(v['venue']),
    identifiers: identifiersOf(v['identifiers']),
    mode: asEnum<Exclude<PaperMode, ''>>(v['mode'], PAPER_MODES),
    reading: asEnum<Exclude<PaperReading, ''>>(v['reading'], PAPER_READINGS),
    verdict: asEnum<Exclude<PaperVerdict, ''>>(v['verdict'], PAPER_VERDICTS),
    confidence: asEnum<Exclude<PaperConfidence, ''>>(v['confidence'], PAPER_CONFIDENCES),
    research_ids: asStringArray(v['research_ids']),
    anchors: anchorsOf(v['anchors']),
    claims: claimsOf(v['claims']),
    red_flags: redFlagsOf(v['red_flags']),
    uncertainties: uncertaintiesOf(v['uncertainties']),
    dir,
    card_path: cardPath,
    pinned: v['pinned'] === true,
    linked_research: linksOf(v['linked_research']),
  }
}

/** Normalize the library's paper list, dropping malformed entries (logged). */
function papersOf(v: unknown): PaperRecord[] {
  if (!Array.isArray(v)) return []
  const out: PaperRecord[] = []
  for (const entry of v) {
    const record = normalizePaper(entry)
    if (record !== null) {
      out.push(record)
      continue
    }
    logger.warn('[papers] dropped a malformed paper entry from GetPapers')
  }
  return out
}

/**
 * Validate + normalize a GetPapers response. Throws when the top-level shape is
 * not a PapersDTO (backend/schema drift); a malformed paper ENTRY is dropped
 * instead (see papersOf).
 */
export function normalizePaperLibrary(v: unknown): PaperLibrary {
  if (!isRecord(v)) throw new Error('Invalid papers response from backend')
  const { project_id: projectId, research_root: researchRoot, root } = v
  if (typeof projectId !== 'string' || typeof researchRoot !== 'string' || typeof root !== 'string') {
    throw new Error('Invalid papers response from backend')
  }
  if (!Array.isArray(v['papers'])) {
    throw new Error('Invalid papers response from backend')
  }
  if (!isStringArrayOrMissing(v['pinned'])) {
    throw new Error('Invalid papers response from backend')
  }
  return {
    project_id: projectId,
    research_root: researchRoot,
    root,
    papers: papersOf(v['papers']),
    pinned: asStringArray(v['pinned']),
  }
}

/** Type guard for the `papers:changed` event payload (event boundary
 *  validation, mirroring the RPC guards). */
export function isPapersChangedPayload(v: unknown): v is PapersChangedPayload {
  if (!isRecord(v)) return false
  return typeof v['project_id'] === 'string' && typeof v['paths'] === 'string'
}

// --- RPC wrappers ---

/**
 * Get the active project's paper library (every parsed paper + the pinned card
 * paths). A missing/empty library is NOT an error — it yields an empty,
 * non-nil `papers` array so the panel renders its empty state.
 */
export async function getPapers(projectId: string): Promise<PaperLibrary> {
  try {
    const app = getApp()
    return normalizePaperLibrary(await app.GetPapers(projectId))
  } catch (err) {
    logger.error('Failed to get paper library:', err)
    throw err
  }
}

/**
 * Pin or unpin a paper (persisted in the project record; survives restarts).
 * Pinning requires the card to exist; unpinning tolerates a deleted card.
 * Idempotent both ways and emits no event — the resolved promise is the
 * refresh signal for the caller.
 */
export async function setPaperPinned(
  projectId: string,
  paperId: string,
  pinned: boolean,
): Promise<void> {
  try {
    const app = getApp()
    await app.SetPaperPinned(projectId, paperId, pinned)
  } catch (err) {
    logger.error('Failed to set paper pin:', err)
    throw err
  }
}

/**
 * Open the native single-select file picker for studying a local document
 * (the markitdown-supported formats, PDF among them). Returns the chosen
 * absolute path, or null when the user cancelled the dialog (the backend
 * returns an empty string with no error on cancel).
 */
export async function pickStudyDocument(): Promise<string | null> {
  try {
    const app = getApp()
    const result: unknown = await app.PickStudyDocument()
    if (typeof result !== 'string') {
      throw new Error('pickStudyDocument: backend returned a non-string path')
    }
    return result === '' ? null : result
  } catch (err) {
    logger.error('Failed to pick a study document:', err)
    throw err
  }
}

/** A self-graded flashcard review outcome (mirrors core/papers.Grade). */
export type FlashcardGrade = 'again' | 'hard' | 'good' | 'easy'

/**
 * Record a self-grade for one flashcard of a paper. The backend appends the
 * grade to the paper's flashcards.md review log, advances the card's Stage, and
 * writes the whole deck atomically (containment-checked). No event is emitted
 * synchronously: the write lands inside the watched library, so the file
 * watcher emits `papers:changed` and the resolved promise is the caller's
 * refresh signal.
 */
export async function recordFlashcardReview(
  projectId: string,
  paperId: string,
  cardId: string,
  grade: FlashcardGrade,
): Promise<void> {
  try {
    const app = getApp()
    await app.RecordFlashcardReview(projectId, paperId, cardId, grade)
  } catch (err) {
    logger.error('Failed to record flashcard review:', err)
    throw err
  }
}

// --- Literature-graph run (study-paper literature.py) ---

/**
 * Outcome of running the literature helper for a paper. `ok` carries the written
 * JSON; every other value is an EXPLICIT degradation the UI must surface (the
 * helper needs the network, a managed Python, and a resolvable seed):
 *   - `offline`      the helper could not reach OpenAlex/Crossref/arXiv (exit 2)
 *   - `unresolved`   the seed could not be resolved (exit 1)
 *   - `rate_limited` a source kept returning HTTP 429 (exit 3)
 *   - `no_python`    the managed Python interpreter is not installed
 *   - `no_script`    the seeded literature.py helper is missing
 *   - `no_seed`      the paper carries no identifier/title to seed with
 *   - `error`        any other failure (carries the helper's stderr tail)
 */
export type PaperLiteratureStatus =
  | 'ok'
  | 'offline'
  | 'unresolved'
  | 'rate_limited'
  | 'no_python'
  | 'no_script'
  | 'no_seed'
  | 'error'

/** The normalized wire shape of RunPaperLiterature (backend.PaperLiteratureDTO). */
export interface PaperLiteratureResult {
  status: PaperLiteratureStatus
  /** Human-readable detail (helper stderr tail / notes); '' when clean. */
  message: string
  /** Absolute path of the written literature.json ('' when nothing was written). */
  path: string
  /** The literature.json content ('' when nothing was written). */
  content: string
}

const LITERATURE_STATUSES: ReadonlySet<string> = new Set([
  'ok',
  'offline',
  'unresolved',
  'rate_limited',
  'no_python',
  'no_script',
  'no_seed',
  'error',
])

/** Validate + normalize a RunPaperLiterature response; an unknown status folds
 *  to `error` so the UI never silently treats it as success. */
export function normalizePaperLiteratureResult(v: unknown): PaperLiteratureResult {
  if (!isRecord(v)) throw new Error('Invalid literature response from backend')
  const raw = typeof v['status'] === 'string' ? v['status'] : 'error'
  return {
    status: (LITERATURE_STATUSES.has(raw) ? raw : 'error') as PaperLiteratureStatus,
    message: asString(v['message']),
    path: asString(v['path']),
    content: asString(v['content']),
  }
}

/**
 * Run the study-paper literature helper for a paper: resolve its seed, query
 * the open sources through the MANAGED Python interpreter, and write
 * `<paper-dir>/literature.json`. The RPC always resolves with an explicit
 * status (an offline/unresolved/failed run is data, not a thrown error); only a
 * transport failure (unknown paper, backend down) rejects.
 */
export async function runPaperLiterature(
  projectId: string,
  paperId: string,
): Promise<PaperLiteratureResult> {
  try {
    const app = getApp()
    return normalizePaperLiteratureResult(await app.RunPaperLiterature(projectId, paperId))
  } catch (err) {
    logger.error('Failed to run literature lookup:', err)
    throw err
  }
}

// --- Original-source fetch (paper.html) ---

/**
 * Outcome of fetching a paper's original arXiv HTML rendition into
 * `<paper-dir>/paper.html` (+ `assets/`). `ok` means the localized rendition
 * is on disk; every other value is an EXPLICIT degradation the UI must
 * surface (never an empty silent failure):
 *   - `offline`    no rendition endpoint was reachable at the network level
 *   - `no_arxiv`   the card carries no usable arXiv identifier
 *   - `not_found`  no HTML rendition exists (404/410 everywhere reachable)
 *   - `error`      any other failure (unexpected status, oversized document,
 *                  containment violation, filesystem failure)
 */
export type PaperOriginalStatus = 'ok' | 'offline' | 'no_arxiv' | 'not_found' | 'error'

/**
 * The normalized wire shape of FetchPaperOriginal
 * (backend.PaperOriginalDTO).
 */
export interface PaperOriginalResult {
  status: PaperOriginalStatus
  /** The URL that actually produced the document (after redirects); ''
   *  unless the fetch succeeded. */
  url: string
}

const ORIGINAL_STATUSES: ReadonlySet<string> = new Set([
  'ok',
  'offline',
  'no_arxiv',
  'not_found',
  'error',
])

/** Validate + normalize a FetchPaperOriginal response; an unknown status folds
 *  to `error` so the UI never silently treats it as success. */
export function normalizePaperOriginalResult(v: unknown): PaperOriginalResult {
  if (!isRecord(v)) throw new Error('Invalid paper original response from backend')
  const raw = typeof v['status'] === 'string' ? v['status'] : 'error'
  return {
    status: (ORIGINAL_STATUSES.has(raw) ? raw : 'error') as PaperOriginalStatus,
    url: asString(v['url']),
  }
}

/**
 * Fetch a paper's original arXiv HTML rendition (native arxiv.org/html with
 * the ar5iv mirror as fallback), sanitized and localized (images rewritten to
 * a local `assets/` dir), into `<paper-dir>/paper.html`. The RPC always
 * resolves with an explicit status (offline / no arXiv id / no rendition is
 * data, not a thrown error); only a transport failure (unknown paper, backend
 * down) rejects. No event is emitted synchronously: the write lands inside
 * the watched library, so the file watcher emits `papers:changed` and the
 * resolved promise is the caller's refresh signal.
 */
export async function fetchPaperOriginal(
  projectId: string,
  paperId: string,
): Promise<PaperOriginalResult> {
  try {
    const app = getApp()
    return normalizePaperOriginalResult(await app.FetchPaperOriginal(projectId, paperId))
  } catch (err) {
    logger.error('Failed to fetch paper original:', err)
    throw err
  }
}
