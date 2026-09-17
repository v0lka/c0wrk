// Flashcards parser for the study-paper skill's `flashcards.md` artifact.
//
// The skill writes a deck as a Markdown document (see
// core/papers/skills/study-paper/assets/flashcards.md) with two data tables:
//
//   ## 2. Cards        | ID | Front (question) | Back (answer) | Anchor | Tag | Stage |
//   ## 3. Review log   | ID | Date | Grade | Next due |
//
// The plain Markdown tables carry the review state (the append-only grade
// history, which the reader replays to derive each card's stage) that the
// backend's structured PaperRecord does NOT capture — PaperRecord models the
// card, not the deck — so the reader re-parses the raw artifact here to drive
// interactive review. The cards table's Stage column is write-only from this
// module's perspective: the stage is DERIVED from the review log (see
// lib/spacedRepetition.ts) and never read from the parsed card.
//
// Everything in this module is PURE over strings — no React, no DOM, no I/O —
// so the deck extraction is unit-testable on fixtures. Table parsing is shared
// with the critical-layer widgets (paperWidgets.parseMarkdownTables) so both
// mirror core/papers/parser.go identically.

import { parseMarkdownTables, type MarkdownTable } from './paperWidgets'

/** A card's lifecycle stage (mirrors the skill's `new` / `learning` / `review`). */
export type FlashcardStage = 'new' | 'learning' | 'review'

/** A self-graded review outcome. */
export type FlashcardGrade = 'again' | 'hard' | 'good' | 'easy'

/** One atomic question/answer card. */
export interface Flashcard {
  /** Stable deck-local label (e.g. `P1-01`). */
  id: string
  /** The question / prompt. */
  front: string
  /** The answer / fact. */
  back: string
  /** Where in the paper the fact comes from (`[Analyst]` for the reader's own synthesis). */
  anchor: string
  /** Topic / concept tag, for filtering. */
  tag: string
}

/** One row of the review log. */
export interface ReviewEntry {
  /** The card the grade belongs to. */
  id: string
  /** The review date (verbatim, typically `YYYY-MM-DD`). */
  date: string
  /** The self-grade; '' when the row carries an unrecognized/blank grade. */
  grade: FlashcardGrade | ''
  /** The next due date the grade produced (verbatim). */
  nextDue: string
}

/** A parsed deck: the ordered cards plus the append-only review log. */
export interface FlashcardDeck {
  cards: Flashcard[]
  reviews: ReviewEntry[]
}

// --- Column resolution (case-insensitive, markdown-emphasis tolerant) ---

function norm(s: string): string {
  return s
    .toLowerCase()
    .replace(/[*_`]/g, '')
    .trim()
}

/**
 * Normalize a cell VALUE (as opposed to a header): Markdown emphasis and
 * surrounding punctuation are stripped, then lower-cased and trimmed. Mirrors
 * the Go parser's shared value-normalizer, so a `**review**` or "`good`." cell
 * folds the same on both sides of the boundary.
 */
function normValue(s: string): string {
  return norm(s).replace(/^\.+|\.+$/g, '')
}

/** First header index matching any pattern, skipping already-used indices. */
function pickColumn(header: string[], patterns: RegExp, exclude: number[] = []): number {
  for (let i = 0; i < header.length; i++) {
    if (exclude.includes(i)) continue
    if (patterns.test(norm(header[i]!))) return i
  }
  return -1
}

function cell(row: string[], i: number): string {
  return i >= 0 && i < row.length ? (row[i] ?? '').trim() : ''
}

const ID_COL = /^id\b/
const FRONT_COL = /front|question|prompt/
const BACK_COL = /back|answer/
const ANCHOR_COL = /anchor|source|location|\bref\b|where|page/
const TAG_COL = /\btag\b|topic|concept|\blabel\b/
const DATE_COL = /date|when/
const GRADE_COL = /grade|rating|result/
const NEXT_DUE_COL = /next|\bdue\b/

/** Fold a raw stage token to its canonical value (unknown/blank → `new`).
 *  Emphasis/punctuation are stripped so a `**review**` cell folds like
 *  `review` — matching the Go parser's NormalizeStage. Kept as the documented
 *  cross-boundary mirror even though the deck's Stage column is derived from the
 *  review log and is no longer surfaced on the parsed card (see `Flashcard`). */
export function normalizeStage(raw: string): FlashcardStage {
  const s = normValue(raw)
  switch (s) {
    case 'learning':
    case 'learn':
      return 'learning'
    case 'review':
    case 'reviewing':
    case 'graduated':
      return 'review'
    case 'new':
      return 'new'
    default:
      return 'new'
  }
}

/** Fold a raw grade token to its canonical value (unknown/blank → '').
 *  Emphasis/punctuation are stripped so a "`good`" cell folds like `good` —
 *  matching the Go parser's NormalizeGrade. */
export function normalizeGrade(raw: string): FlashcardGrade | '' {
  const s = normValue(raw)
  switch (s) {
    case 'again':
    case 'hard':
    case 'good':
    case 'easy':
      return s
    default:
      return ''
  }
}

/** A table qualifies as the cards table when it carries a Front and a Back
 *  column (the deck's card table; its ID column is resolved separately so a
 *  header-only variant still yields cards). */
function isCardsTable(t: MarkdownTable): boolean {
  return pickColumn(t.header, FRONT_COL) >= 0 && pickColumn(t.header, BACK_COL) >= 0
}

/** A table qualifies as the review log when it carries a Grade column. */
function isReviewTable(t: MarkdownTable): boolean {
  return pickColumn(t.header, GRADE_COL) >= 0 || pickColumn(t.header, NEXT_DUE_COL) >= 0
}

function parseCardTable(t: MarkdownTable): Flashcard[] {
  const idIdx = pickColumn(t.header, ID_COL)
  const frontIdx = pickColumn(t.header, FRONT_COL)
  const backIdx = pickColumn(t.header, BACK_COL)
  const anchorIdx = pickColumn(t.header, ANCHOR_COL, [idIdx, frontIdx, backIdx])
  const tagIdx = pickColumn(t.header, TAG_COL, [idIdx, frontIdx, backIdx, anchorIdx])
  const cards: Flashcard[] = []
  for (const row of t.rows) {
    const id = cell(row, idIdx)
    // Card-row identity (shared with the Go parser's isCardRow): a data row is a
    // card unless the deck carries an id column and the row's id cell is empty.
    if (idIdx >= 0 && id === '') continue
    cards.push({
      id,
      front: cell(row, frontIdx),
      back: cell(row, backIdx),
      anchor: cell(row, anchorIdx),
      tag: cell(row, tagIdx),
    })
  }
  return cards
}

function parseReviewTable(t: MarkdownTable): ReviewEntry[] {
  const idIdx = pickColumn(t.header, ID_COL)
  const dateIdx = pickColumn(t.header, DATE_COL, [idIdx])
  const gradeIdx = pickColumn(t.header, GRADE_COL, [idIdx, dateIdx])
  const dueIdx = pickColumn(t.header, NEXT_DUE_COL, [idIdx, dateIdx, gradeIdx])
  const reviews: ReviewEntry[] = []
  for (const row of t.rows) {
    const id = cell(row, idIdx)
    const date = cell(row, dateIdx)
    const grade = cell(row, gradeIdx)
    const nextDue = cell(row, dueIdx)
    if (id === '' && date === '' && grade === '' && nextDue === '') continue
    reviews.push({ id, date, grade: normalizeGrade(grade), nextDue })
  }
  return reviews
}

/**
 * Parse a `flashcards.md` document into its deck. Best-effort and total: a
 * document with no recognizable tables yields an empty deck (the caller renders
 * the raw Markdown fallback), and rows that carry no card/grade are skipped.
 */
export function parseFlashcards(content: string): FlashcardDeck {
  const tables = parseMarkdownTables(content)
  const cardsTable = tables.find(isCardsTable)
  const reviewTable = tables.find((t) => t !== cardsTable && isReviewTable(t))
  return {
    cards: cardsTable ? parseCardTable(cardsTable) : [],
    reviews: reviewTable ? parseReviewTable(reviewTable) : [],
  }
}

/** The review entries belonging to one card, in log order. */
export function reviewsForCard(deck: FlashcardDeck, cardId: string): ReviewEntry[] {
  return deck.reviews.filter((entry) => entry.id === cardId)
}
