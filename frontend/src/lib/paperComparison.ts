// Multi-paper comparison parsing for the paper Compare section.
//
// The `study-paper` skill's Compare intent writes ONE artifact per comparison
// set to `<research-root>/comparisons/<slug>.md`, built from its
// assets/comparison-matrix.md template: a papers-under-comparison table, the
// dimension × paper matrix, fairness notes, an agreement/disagreement table,
// gaps none of the papers address, and a synthesis verdict. The plain Markdown
// carries structure the backend's PaperRecord does not model (a comparison
// spans several papers, not one), so the reader re-parses the raw artifact here
// and renders it as a first-class matrix.
//
// Everything in this module is PURE over strings — no React, no DOM, no I/O —
// so the extraction is unit-testable on fixtures.

import { parseMarkdownTables, type MarkdownTable } from './paperWidgets'

/** One paper row of the "Papers under comparison" table. */
export interface ComparisonPaper {
  id: string
  shortName: string
  citation: string
  summary: string
}

/** One dimension row of the comparison matrix (cells align with `columns`). */
export interface ComparisonMatrixRow {
  dimension: string
  cells: string[]
}

/** One row of the agreement/disagreement table. */
export interface ComparisonAgreement {
  point: string
  verdict: string
  papers: string
}

/** One row of the synthesis-verdict field/value table. */
export interface ComparisonVerdictRow {
  field: string
  value: string
}

/** The parsed shape of a comparison artifact. */
export interface ParsedComparison {
  /** The topic from the `# Paper Comparison Matrix — <topic>` heading ('' when absent). */
  topic: string
  papers: ComparisonPaper[]
  /** The matrix's paper columns (the header cells after "Dimension"). */
  columns: string[]
  matrix: ComparisonMatrixRow[]
  /** Fairness & comparability bullets (from the template's §3). */
  fairness: string[]
  agreements: ComparisonAgreement[]
  /** Gaps none of the papers address, as bullets (the template's §5). */
  gaps: string[]
  verdict: ComparisonVerdictRow[]
  /** True when a comparison matrix (≥1 dimension row) was parsed. */
  hasMatrix: boolean
}

// --- Markdown structure helpers (pure) ---

interface MdSection {
  level: number
  title: string
  body: string
}

/** Split a document into heading-anchored sections (the body is the text up to
 *  the next heading of any level). Content before the first heading is dropped
 *  — the template always opens with an H1. */
function splitSections(content: string): MdSection[] {
  const sections: MdSection[] = []
  let current: MdSection | null = null
  for (const line of content.split('\n')) {
    const heading = /^(#{1,6})\s+(.*)$/.exec(line.trim())
    if (heading) {
      current = { level: heading[1]!.length, title: heading[2]!.trim(), body: '' }
      sections.push(current)
      continue
    }
    if (current !== null) current.body += `${line}\n`
  }
  return sections
}

function findSection(sections: MdSection[], pattern: RegExp): MdSection | null {
  return sections.find((section) => pattern.test(section.title)) ?? null
}

/** Lowercase + strip Markdown emphasis for header matching. */
function norm(s: string): string {
  return s
    .toLowerCase()
    .replace(/[*_`]/g, '')
    .trim()
}

/** Strip the inline emphasis the template uses so cells read cleanly. Bracket
 *  labels ([Analyst] / [Unknown]) are preserved — they carry meaning. */
function cleanCell(s: string): string {
  return s.replace(/\*\*/g, '').replace(/[`_]/g, '').trim()
}

function colIndex(header: string[], pattern: RegExp): number {
  for (let i = 0; i < header.length; i++) {
    if (pattern.test(norm(header[i]!))) return i
  }
  return -1
}

function cell(row: string[], i: number): string {
  return i >= 0 && i < row.length ? cleanCell(row[i] ?? '') : ''
}

function firstTable(body: string): MarkdownTable | null {
  return parseMarkdownTables(body)[0] ?? null
}

function isTableLine(line: string): boolean {
  return line.trimStart().startsWith('|')
}

/** The bullet items of a body (list markers only; table rows are skipped). */
function bulletItems(body: string): string[] {
  const items: string[] = []
  for (const raw of body.split('\n')) {
    const trimmed = raw.trim()
    if (trimmed === '' || isTableLine(trimmed)) continue
    const item = /^(?:[-*+]|\d+[.)])\s+(.*)$/.exec(trimmed)
    const text = item?.[1]?.trim() ?? ''
    if (text !== '') items.push(cleanCell(text))
  }
  return items
}

// --- Section parsers ---

function parseTopic(sections: MdSection[]): string {
  const h1 = sections.find((section) => section.level === 1)
  if (h1 === undefined) return ''
  const m = /paper comparison matrix\s*[—–:-]\s*(.+)$/i.exec(h1.title)
  if (m) return cleanCell(m[1]!)
  // A bare template title (no filled topic) yields no topic.
  return /paper comparison matrix/i.test(h1.title) ? '' : cleanCell(h1.title)
}

function parsePapers(section: MdSection | null): ComparisonPaper[] {
  if (section === null) return []
  const table = firstTable(section.body)
  if (table === null) return []
  const header = table.header
  const idIdx = colIndex(header, /^id$|\bid\b/)
  const shortIdx = colIndex(header, /short|name|label/)
  const citationIdx = colIndex(header, /citation|identifier|reference/)
  const summaryIdx = colIndex(header, /summary|one-line|tldr|tl;dr/)
  const papers: ComparisonPaper[] = []
  for (const row of table.rows) {
    const paper: ComparisonPaper = {
      id: cell(row, idIdx),
      shortName: cell(row, shortIdx),
      citation: cell(row, citationIdx),
      summary: cell(row, summaryIdx >= 0 ? summaryIdx : 3),
    }
    if (paper.id === '' && paper.shortName === '' && paper.citation === '' && paper.summary === '') {
      continue
    }
    papers.push(paper)
  }
  return papers
}

function parseMatrix(section: MdSection | null): { columns: string[]; rows: ComparisonMatrixRow[] } {
  if (section === null) return { columns: [], rows: [] }
  const table = firstTable(section.body)
  if (table === null || table.header.length < 2) return { columns: [], rows: [] }
  const columns = table.header.slice(1).map((h) => cleanCell(h))
  const rows: ComparisonMatrixRow[] = []
  for (const row of table.rows) {
    const dimension = cleanCell(row[0] ?? '')
    const cells = row.slice(1).map((c) => cleanCell(c))
    if (dimension === '' && cells.every((c) => c === '')) continue
    rows.push({ dimension, cells })
  }
  return { columns, rows }
}

function parseAgreements(section: MdSection | null): ComparisonAgreement[] {
  if (section === null) return []
  const table = firstTable(section.body)
  if (table === null) return []
  const header = table.header
  const pointIdx = colIndex(header, /point|dimension|aspect|topic/)
  const verdictIdx = colIndex(header, /agree|disagree|verdict|stance/)
  const papersIdx = colIndex(header, /paper|anchor|where|source/)
  const out: ComparisonAgreement[] = []
  for (const row of table.rows) {
    const item: ComparisonAgreement = {
      point: cell(row, pointIdx >= 0 ? pointIdx : 0),
      verdict: cell(row, verdictIdx >= 0 ? verdictIdx : 1),
      papers: cell(row, papersIdx >= 0 ? papersIdx : 2),
    }
    if (item.point === '' && item.verdict === '' && item.papers === '') continue
    out.push(item)
  }
  return out
}

function parseVerdict(section: MdSection | null): ComparisonVerdictRow[] {
  if (section === null) return []
  const table = firstTable(section.body)
  if (table === null) return []
  const header = table.header
  const fieldIdx = colIndex(header, /field|item|aspect|question/)
  const valueIdx = colIndex(header, /value|answer|verdict|detail/)
  const out: ComparisonVerdictRow[] = []
  for (const row of table.rows) {
    const item: ComparisonVerdictRow = {
      field: cell(row, fieldIdx >= 0 ? fieldIdx : 0),
      value: cell(row, valueIdx >= 0 ? valueIdx : 1),
    }
    if (item.field === '' && item.value === '') continue
    out.push(item)
  }
  return out
}

/**
 * Parse a comparison artifact (the study-paper skill's
 * `assets/comparison-matrix.md` output) into its structured shape. Tolerant by
 * design: an unrecognized or partial document yields empty collections rather
 * than throwing, so the reader shows an honest empty state.
 */
export function parseComparison(content: string): ParsedComparison {
  const sections = splitSections(content)
  const papersSection =
    findSection(sections, /papers under comparison|papers being compared/i) ??
    findSection(sections, /^papers$/i)
  // The H1 title is "Paper Comparison Matrix — <topic>", so match the numbered
  // section heading ("2. Comparison matrix") rather than any "comparison
  // matrix" occurrence, or the H1 would win on document order.
  const matrixSection =
    findSection(sections, /^(?:\d+[.)]\s*)?comparison matrix\b/i) ??
    findSection(sections, /^matrix$/i)
  const fairnessSection = findSection(sections, /fairness|comparability/i)
  const agreementsSection = findSection(sections, /agree|disagree/i)
  const gapsSection = findSection(sections, /gaps/i)
  const verdictSection = findSection(sections, /synthesis verdict|verdict/i)

  const matrix = parseMatrix(matrixSection)

  return {
    topic: parseTopic(sections),
    papers: parsePapers(papersSection),
    columns: matrix.columns,
    matrix: matrix.rows,
    fairness: fairnessSection === null ? [] : bulletItems(fairnessSection.body),
    agreements: parseAgreements(agreementsSection),
    gaps: gapsSection === null ? [] : bulletItems(gapsSection.body),
    verdict: parseVerdict(verdictSection),
    hasMatrix: matrix.rows.length > 0,
  }
}

/** The minimal paper shape the mention check needs (a PaperRecord satisfies it). */
export interface ComparisonMentionPaper {
  id: string
  slug: string
  title: string
  card_path: string
  identifiers: ReadonlyArray<{ scheme: string; value: string }>
}

/**
 * Whether a comparison artifact refers to a paper. A comparison names the
 * papers it covers in its "Papers under comparison" table — by internal id,
 * slug, card path, a bibliographic identifier (scheme:value) or the title — so
 * a match on any of these (≥3 chars, case-insensitive) counts. Used by the
 * paper workspace's Compare section to show only the comparisons this paper
 * takes part in.
 */
export function comparisonMentionsPaper(
  content: string,
  paper: ComparisonMentionPaper,
): boolean {
  const haystack = content.toLowerCase()
  const needles: string[] = [paper.slug, paper.id, paper.card_path]
  for (const identifier of paper.identifiers) {
    if (identifier.value !== '') needles.push(`${identifier.scheme}:${identifier.value}`)
  }
  if (needles.some((needle) => needle.length >= 3 && haystack.includes(needle.toLowerCase()))) {
    return true
  }
  const title = paper.title.trim().toLowerCase()
  return title.length >= 4 && haystack.includes(title)
}
