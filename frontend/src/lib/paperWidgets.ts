// Paper critical-layer widget parsers.
//
// The `study-paper` skill writes a paper's evidence note (note.md) and
// appraisal sheet (appraisal.md) as Markdown documents whose core is the
// "critical layer": a claim→evidence matrix, a red-flags list (≤3), and an
// uncertainty list. The plain Markdown tables carry an evidence-strength
// verdict (present / weak / absent) that the backend's structured PaperRecord
// does NOT capture (its Claim has no strength field), so the reader re-parses
// the raw artifact here to render the layer as widgets.
//
// Everything in this module is PURE over strings — no React, no DOM, no I/O —
// so the widget extraction is unit-testable on fixtures.

export type EvidenceStrength = 'present' | 'weak' | 'absent' | 'unknown'

/** One row of a claim→evidence matrix. */
export interface EvidenceMatrixRow {
  /** The load-bearing claim / contribution the row is about. */
  claim: string
  /** The evidence offered (experiment, result, proof). */
  evidence: string
  /** The source anchor (section / figure / table / page). */
  anchor: string
  /** Parsed evidence strength. */
  strength: EvidenceStrength
}

/** A red-flag chip (the skill caps these at 3, ranked by severity). */
export interface RedFlagChip {
  flag: string
  detail: string
  severity: string
}

/** An open-uncertainty chip. */
export interface UncertaintyChip {
  item: string
  detail: string
}

/** The three parts of the critical layer. */
export interface CriticalLayer {
  evidence: EvidenceMatrixRow[]
  redFlags: RedFlagChip[]
  uncertainties: UncertaintyChip[]
}

/** The skill caps red flags at three; the widget respects that cap. */
export const MAX_RED_FLAG_CHIPS = 3

// --- Markdown table parsing (mirrors core/papers/parser.go) ---

export interface MarkdownTable {
  header: string[]
  rows: string[][]
}

function splitUnescapedPipes(s: string): string[] {
  const parts: string[] = []
  let start = 0
  let prev = ''
  for (let i = 0; i < s.length; i++) {
    const ch = s.charAt(i)
    if (ch === '|' && prev !== '\\') {
      parts.push(s.slice(start, i))
      start = i + 1
    }
    prev = ch
  }
  parts.push(s.slice(start))
  return parts
}

/** Reverse of the writer's cell escaping: `\|` → `|`, `<br>` → newline. */
function unescapeCell(v: string): string {
  return v.replace(/\\\|/g, '|').replace(/<br>/g, '\n')
}

function splitRow(raw: string): string[] {
  let row = raw.trim()
  if (row.startsWith('|')) row = row.slice(1)
  if (row.endsWith('|') && !row.endsWith('\\|')) row = row.slice(0, -1)
  return splitUnescapedPipes(row).map((p) => unescapeCell(p.trim()))
}

function isTableRow(line: string): boolean {
  return line.trimStart().startsWith('|')
}

function isSeparatorRow(cells: string[]): boolean {
  if (cells.length === 0) return false
  return cells.every((c) => c !== '' && /^[-: ]+$/.test(c))
}

function buildTable(block: string[]): MarkdownTable | null {
  const cells = block.map(splitRow)
  let sepIdx = -1
  for (let k = 0; k < cells.length; k++) {
    if (sepIdx < 0 && isSeparatorRow(cells[k]!)) sepIdx = k
  }
  // A table needs a header row immediately before its separator.
  if (sepIdx < 1) return null
  const header = cells[sepIdx - 1]!
  const rows: string[][] = []
  for (const row of cells.slice(sepIdx + 1)) {
    if (row.length === 0 || isSeparatorRow(row)) continue
    rows.push(row)
  }
  return { header, rows }
}

/** Extract every Markdown table from a document (header + data rows). */
export function parseMarkdownTables(content: string): MarkdownTable[] {
  const lines = content.split('\n')
  const tables: MarkdownTable[] = []
  let i = 0
  while (i < lines.length) {
    if (!isTableRow(lines[i]!)) {
      i++
      continue
    }
    const start = i
    while (i < lines.length && isTableRow(lines[i]!)) i++
    const table = buildTable(lines.slice(start, i))
    if (table) tables.push(table)
  }
  return tables
}

// --- Column resolution ---

function norm(s: string): string {
  return s
    .toLowerCase()
    .replace(/[*_`]/g, '')
    .trim()
}

function headerIndex(header: string[], pattern: RegExp): number {
  for (let i = 0; i < header.length; i++) {
    if (pattern.test(norm(header[i]!))) return i
  }
  return -1
}

function headerIndexExcluding(header: string[], pattern: RegExp, exclude: number[]): number {
  for (let i = 0; i < header.length; i++) {
    if (exclude.includes(i)) continue
    if (pattern.test(norm(header[i]!))) return i
  }
  return -1
}

function cell(row: string[], i: number): string {
  return i >= 0 && i < row.length ? (row[i] ?? '').trim() : ''
}

/** A negator that PRECEDES a positive verdict (within a few words) — "not
 *  present", "no evidence present" — inverts that verdict. A negator that
 *  follows the verdict ("present, no caveats") does not. */
const NEGATED_POSITIVE = /\b(?:not|no|none|without|never)\b(?:\s+\S+){0,3}\s+(?:present|weak)\b|n['’]t\s+(?:present|weak)\b/

/** Fold a raw evidence-strength cell to its verdict. A cell naming more than
 *  one verdict (the template's "present / weak / absent" placeholder) is
 *  ambiguous and folds to 'unknown' rather than guessing. A NEGATED verdict
 *  ("not present" / "no evidence present") is inverted — a negated phrase
 *  contains the positive token, so it must not classify as that verdict. */
export function normalizeStrength(raw: string): EvidenceStrength {
  const s = raw.toLowerCase()
  if (s.trim() === '') return 'unknown'
  // Whole-word tokens: "unrepresented" / "presence" must not collide with the
  // verdict words they merely contain.
  const absent = /\babsent\b/.test(s)
  const weak = /\bweak\b/.test(s)
  const present = /\bpresent\b/.test(s)
  if ([absent, weak, present].filter(Boolean).length !== 1) return 'unknown'
  if (NEGATED_POSITIVE.test(s)) {
    // "not present" / "no evidence present" → the evidence is absent.
    return present ? 'absent' : 'unknown'
  }
  if (absent) return 'absent'
  if (weak) return 'weak'
  return 'present'
}

// --- Evidence matrix ---

const STRENGTH_COL = /strength|present|weak|absent/
const CLAIM_COL = /claim|assertion|statement|finding/
const CONTR_COL = /contribution/
const ANCHOR_COL = /anchor|location|source|where|page|\bref\b/

/** Prefer a column literally named "claim"/"assertion"/… over the vaguer
 *  "Contribution" row label the note's matrix uses. */
function pickClaimCol(header: string[], exclude: number[]): number {
  const preferred = headerIndexExcluding(header, CLAIM_COL, exclude)
  if (preferred >= 0) return preferred
  return headerIndexExcluding(header, CONTR_COL, exclude)
}

function pickEvidenceCol(header: string[], strengthIdx: number): number {
  // Prefer an explicitly evidence-ish column. Note: a bare "support" token is
  // NOT used — the note matrix's claim column says "…meant to support" and its
  // verdict column says "…does it support the claim", so it would collide.
  const specific = /evidence offered|experiment|result|proof/
  for (let i = 0; i < header.length; i++) {
    if (i === strengthIdx) continue
    if (specific.test(norm(header[i]!))) return i
  }
  for (let i = 0; i < header.length; i++) {
    if (i === strengthIdx) continue
    const h = norm(header[i]!)
    if (h.includes('evidence') && !h.includes('claim')) return i
  }
  return -1
}

/**
 * Parse a claim→evidence matrix from a document: the first Markdown table that
 * carries both an evidence column and an evidence-strength column (the note's
 * "Contribution → evidence matrix" / the appraisal's "Claim → evidence").
 * Returns [] when no such table exists.
 */
export function parseEvidenceMatrix(content: string): EvidenceMatrixRow[] {
  for (const t of parseMarkdownTables(content)) {
    const strengthIdx = headerIndex(t.header, STRENGTH_COL)
    if (strengthIdx < 0) continue
    const evidenceIdx = pickEvidenceCol(t.header, strengthIdx)
    if (evidenceIdx < 0) continue
    const claimIdx = pickClaimCol(t.header, [strengthIdx, evidenceIdx])
    if (claimIdx < 0) continue
    const anchorIdx = headerIndexExcluding(t.header, ANCHOR_COL, [strengthIdx, evidenceIdx, claimIdx])
    const rows: EvidenceMatrixRow[] = []
    for (const row of t.rows) {
      const rawStrength = cell(row, strengthIdx)
      const claim = cell(row, claimIdx)
      const evidence = cell(row, evidenceIdx)
      const anchor = cell(row, anchorIdx)
      if (claim === '' && evidence === '' && rawStrength === '') continue
      rows.push({ claim, evidence, anchor, strength: normalizeStrength(rawStrength) })
    }
    if (rows.length > 0) return rows
  }
  return []
}

// --- Red flags & uncertainty (table form, then list fallback) ---

function stripInlineMd(s: string): string {
  return s.replace(/\*\*/g, '').replace(/[*_`]/g, '').replace(/\[Analyst\]/gi, '').trim()
}

/** Find the first table whose header carries a red-flag column and read it. */
export function parseRedFlags(content: string): RedFlagChip[] {
  const t = parseMarkdownTables(content).find(
    (tbl) => headerIndex(tbl.header, /flag|concern|warning|issue|risk/) >= 0,
  )
  if (t) {
    const flagIdx = headerIndex(t.header, /flag|concern|warning|issue|risk/)
    const detailIdx = headerIndexExcluding(t.header, /detail|why|note|explanation|description/, [flagIdx])
    const sevIdx = headerIndexExcluding(t.header, /severity|impact|level/, [flagIdx, detailIdx])
    const out: RedFlagChip[] = []
    for (const row of t.rows) {
      const flag = stripInlineMd(cell(row, flagIdx))
      const detail = stripInlineMd(cell(row, detailIdx))
      const severity = cell(row, sevIdx)
      if (flag === '' && detail === '' && severity === '') continue
      out.push({ flag, detail, severity })
    }
    if (out.length > 0) return out
  }
  return parseRedFlagList(content)
}

/** Find the first table whose header carries an uncertainty column and read it. */
export function parseUncertainties(content: string): UncertaintyChip[] {
  const t = parseMarkdownTables(content).find(
    (tbl) => headerIndex(tbl.header, /uncertain|unknown|open question|limitation|unresolved|gap|item/) >= 0,
  )
  if (t) {
    const itemIdx = headerIndex(t.header, /item|question|uncertainty|issue|unknown|limitation|gap/)
    const detailIdx = headerIndexExcluding(t.header, /detail|why|note|explanation|reason/, [itemIdx])
    const out: UncertaintyChip[] = []
    for (const row of t.rows) {
      const item = stripInlineMd(cell(row, itemIdx))
      const detail = stripInlineMd(cell(row, detailIdx))
      if (item === '' && detail === '') continue
      out.push({ item, detail })
    }
    if (out.length > 0) return out
  }
  return parseUncertaintyList(content)
}

/** The critical-layer labels the templates use for the red-flag / uncertainty
 *  sections. A bold lead-in naming one of these is a section LABEL, not a
 *  list item. */
const SECTION_LABEL = /red flag|concern|weakness|uncertain|unknown|open question|limitation/

/** Whether a trimmed line is a *pure* bold label — its bold text spans the
 *  whole line (an optional trailing colon aside), e.g. `- **Red flags:**`.
 *  A bold lead-in followed by prose (`- **Data leakage** is not ruled out.`)
 *  is a list ITEM, not a label. */
const PURE_BOLD_LABEL = /^[-*+]?\s*\*\*(.+?)\*\*\s*:?\s*$/

/** Collect the list items of the section whose heading OR bold marker line
 *  matches `marker`. Covers both the canonical note.md tables' list-shaped
 *  cousins (the templates write the critical layer as bullets under a heading
 *  or a bold label, e.g. `- **Red flags (≤3, ranked by severity):**`).
 *
 *  A section only starts on a sub-heading (level ≥ 2) or a bold label — never
 *  on the document H1 (`# Reading Note — <paper title>`), whose model-authored
 *  title may itself contain a marker word. A bold-led list ITEM inside the
 *  section is kept as an item rather than mistaken for a new label. */
function listItemsOfSection(content: string, marker: RegExp): string[] {
  const lines = content.split('\n')
  const items: string[] = []
  let inSection = false
  let stopLevel = 6
  for (const raw of lines) {
    const trimmed = raw.trim()
    const heading = /^(#{1,6})\s+(.*)$/.exec(trimmed)
    if (heading) {
      const level = heading[1]!.length
      const text = heading[2]!.replace(/[*_`]/g, '').toLowerCase()
      // The H1 carries the paper title; a marker in it must not open a section.
      if (level >= 2 && marker.test(text)) {
        inSection = true
        stopLevel = level
        continue
      }
      if (inSection && level <= stopLevel) inSection = false
      continue
    }
    const bold = /^[-*+]?\s*\*\*(.+?)\*\*/.exec(trimmed)
    if (bold) {
      const text = bold[1]!.replace(/[*_`]/g, '').toLowerCase()
      if (marker.test(text)) {
        inSection = true
        stopLevel = 6
        continue
      }
      // A bold LABEL — a pure `**…**` line, or a bold lead-in naming another
      // critical-layer label — ends the previous list. A bold lead-in that
      // merely starts a list ITEM does not: fall through and keep it as one.
      if (inSection && (PURE_BOLD_LABEL.test(trimmed) || SECTION_LABEL.test(text))) {
        inSection = false
        continue
      }
    }
    if (!inSection) continue
    const item = /^\s*(?:[-*+]|\d+[.)])\s+(.*)$/.exec(raw)
    if (item && item[1]!.trim() !== '') items.push(item[1]!.trim())
  }
  return items
}

function parseRedFlagList(content: string): RedFlagChip[] {
  return listItemsOfSection(content, /red flag|concern|weakness/).map((item) => {
    const cleaned = stripInlineMd(item)
    // "… — why it matters" → keep the why as the detail.
    const m = /^(.*?)\s+[—–]\s+(.*)$/.exec(cleaned)
    if (m) return { flag: m[1]!.trim(), detail: m[2]!.trim(), severity: '' }
    return { flag: cleaned, detail: '', severity: '' }
  })
}

function parseUncertaintyList(content: string): UncertaintyChip[] {
  return listItemsOfSection(content, /uncertain|unknown|open question|limitation/).map((item) => ({
    item: stripInlineMd(item).replace(/^\[unknown\]\s*/i, ''),
    detail: '',
  }))
}

// --- The whole critical layer ---

/**
 * Merge the critical layer from a paper's note and appraisal documents. The
 * note is authoritative; the appraisal fills whatever the note lacks (its §4
 * carries a claim→evidence matrix and its §7 the red flags/uncertainty).
 * Red flags are capped at `MAX_RED_FLAG_CHIPS`.
 */
export function parseCriticalLayer(note: string, appraisal: string): CriticalLayer {
  const evidence = parseEvidenceMatrix(note).length > 0 ? parseEvidenceMatrix(note) : parseEvidenceMatrix(appraisal)
  const redFlags = parseRedFlags(note).length > 0 ? parseRedFlags(note) : parseRedFlags(appraisal)
  const uncertainties =
    parseUncertainties(note).length > 0 ? parseUncertainties(note) : parseUncertainties(appraisal)
  return { evidence, redFlags: redFlags.slice(0, MAX_RED_FLAG_CHIPS), uncertainties }
}
