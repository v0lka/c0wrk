// Spaced-repetition scheduling for the flashcards review (E3).
//
// The study-paper skill defines a fixed interval ladder — by default
// 1 → 3 → 7 → 16 → 35 days — and one rule per self-grade (see
// `## 4. Spaced-repetition schedule` in assets/flashcards.md):
//
//   * again — reset to the first interval (and the card is back to `learning`)
//   * hard  — repeat at the same interval
//   * good  — promote to the next interval
//   * easy  — promote to the next interval
//
// (The skill deliberately folds `good` and `easy` onto the same promotion; the
// distinction is a learner-confidence signal, not a different interval.)
//
// A card's progress is encoded as an `intervalIndex`: -1 for a never-reviewed
// (`new`) card, 0 for the first interval (1 day), up to the last rung. The
// stage is DERIVED from the index rather than stored, so the two never drift:
// `new` (index -1) → `learning` (index 0) → `review` (index ≥ 1).
//
// Everything here is PURE — no React, no DOM, no I/O — and date math is done in
// UTC on `YYYY-MM-DD` strings, so the schedule is deterministic and testable.

import type { FlashcardGrade, FlashcardStage, ReviewEntry } from './flashcards'

/** The default interval ladder, in days (skill `## 4`). */
export const INTERVAL_SCHEDULE_DAYS: readonly number[] = [1, 3, 7, 16, 35]

/** The interval index of a never-reviewed card (not yet scheduled). */
export const NEW_INTERVAL_INDEX = -1

/** The last rung of the interval ladder. */
export const MAX_INTERVAL_INDEX = INTERVAL_SCHEDULE_DAYS.length - 1

/** A card's scheduling state: its progress rung plus the derived stage. */
export interface ReviewState {
  /** -1 for a new card, else the current interval-ladder index. */
  intervalIndex: number
  /** Derived from `intervalIndex` (never stored independently). */
  stage: FlashcardStage
}

/** A scheduled review: the resulting state plus the computed next due date. */
export interface ScheduledReview extends ReviewState {
  /** The next due date as `YYYY-MM-DD`. */
  dueDate: string
}

/** Clamp an interval index into the valid `[-1, MAX]` range. */
function clampIndex(index: number): number {
  if (!Number.isFinite(index)) return NEW_INTERVAL_INDEX
  if (index < NEW_INTERVAL_INDEX) return NEW_INTERVAL_INDEX
  if (index > MAX_INTERVAL_INDEX) return MAX_INTERVAL_INDEX
  return Math.floor(index)
}

/** The stage implied by an interval index. */
export function stageForIndex(index: number): FlashcardStage {
  const i = clampIndex(index)
  if (i < 0) return 'new'
  if (i === 0) return 'learning'
  return 'review'
}

/** Build a review state from an interval index (stage derived). */
export function stateForIndex(index: number): ReviewState {
  const intervalIndex = clampIndex(index)
  return { intervalIndex, stage: stageForIndex(intervalIndex) }
}

/** The interval length (days) for an index, clamped to the ladder (a
 *  never-reviewed card maps to the first rung). */
export function intervalDays(index: number): number {
  const i = clampIndex(index)
  return INTERVAL_SCHEDULE_DAYS[i < 0 ? 0 : i]!
}

/**
 * The interval index produced by grading a card that is currently at
 * `currentIndex`. `again` resets to the first rung, `hard` holds the rung,
 * `good`/`easy` advance one (capped at the last rung). A never-reviewed card
 * (index -1) lands on the first rung for every grade — its first review sets
 * the initial interval.
 */
export function advanceIndex(currentIndex: number, grade: FlashcardGrade): number {
  const current = clampIndex(currentIndex)
  switch (grade) {
    case 'again':
      return 0
    case 'hard':
      return current < 0 ? 0 : current
    case 'good':
    case 'easy':
      return current < 0 ? 0 : Math.min(current + 1, MAX_INTERVAL_INDEX)
  }
}

/** Parse a `YYYY-MM-DD` string to a UTC midnight Date (or null when invalid). */
function parseISODate(iso: string): Date | null {
  const m = /^(\d{4})-(\d{2})-(\d{2})$/.exec(iso.trim())
  if (!m) return null
  const year = Number(m[1])
  const month = Number(m[2])
  const day = Number(m[3])
  const date = new Date(Date.UTC(year, month - 1, day))
  if (Number.isNaN(date.getTime())) return null
  // Reject an overflowed day (2024-02-31 → 2024-03-02).
  if (date.getUTCMonth() !== month - 1 || date.getUTCDate() !== day) return null
  return date
}

/** Format a Date (UTC) as `YYYY-MM-DD`. */
function toISODate(date: Date): string {
  return date.toISOString().slice(0, 10)
}

/**
 * The LOCAL calendar date as `YYYY-MM-DD`, built from the local getters — NOT
 * `toISOString()`, which is UTC. The backend records a flashcard review under
 * `time.Now().Format("2006-01-02")` (the LOCAL date), so the frontend's default
 * 'today' seed must be local too or a review near midnight lands on the wrong
 * calendar day. (Pure date ARITHMETIC stays in UTC via `addDays`/`toISODate`.)
 */
export function todayLocalISO(): string {
  const now = new Date()
  const year = now.getFullYear()
  const month = String(now.getMonth() + 1).padStart(2, '0')
  const day = String(now.getDate()).padStart(2, '0')
  return `${year}-${month}-${day}`
}

/** Add `days` to a `YYYY-MM-DD` date, returning `YYYY-MM-DD`. An unparseable
 *  input is returned unchanged (the caller shows it verbatim). */
export function addDays(iso: string, days: number): string {
  const date = parseISODate(iso)
  if (date === null) return iso
  date.setUTCDate(date.getUTCDate() + days)
  return toISODate(date)
}

/** Schedule a review of a card at `state` graded `grade` on `today`. */
export function scheduleAfter(state: ReviewState, grade: FlashcardGrade, today: string): ScheduledReview {
  const intervalIndex = advanceIndex(state.intervalIndex, grade)
  return {
    ...stateForIndex(intervalIndex),
    dueDate: addDays(today, intervalDays(intervalIndex)),
  }
}

/**
 * Replay a card's review log to recover its current state: every graded entry
 * advances the index in log order, so the card's stage and interval are exactly
 * what the recorded grades produced. A log with no graded entries yields the
 * `new` state.
 */
export function stateFromReviews(reviews: ReviewEntry[]): ReviewState {
  let index = NEW_INTERVAL_INDEX
  for (const entry of reviews) {
    if (entry.grade === '') continue
    index = advanceIndex(index, entry.grade)
  }
  return stateForIndex(index)
}
