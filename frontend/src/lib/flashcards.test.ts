// Tests for lib/flashcards.ts — the flashcards.md deck parser, exercised on a
// fixture shaped like the study-paper skill's assets/flashcards.md.

import { describe, it, expect } from 'vitest'
import { normalizeGrade, normalizeStage, parseFlashcards, reviewsForCard } from './flashcards'

// A realistic deck: deck-header table (must be ignored), the card table, the
// review log, and the schedule section.
const DECK = `# Flashcards — Attention Is All You Need

## Deck header

| Field | Value |
| --- | --- |
| Source paper(s) / identifier | arXiv:1706.03762 |
| Deck scope | Transformer architecture |
| Date created | 2024-01-01 |
| Interval schedule (see §4) | 1 → 3 → 7 → 16 → 35 |

## 1. Card format

- **ID** — stable label (e.g. \`P1-01\`).

## 2. Cards

| ID | Front (question) | Back (answer) | Anchor | Tag | Stage |
| --- | --- | --- | --- | --- | --- |
| P1-01 | What replaces recurrence? | Self-attention | §3 | architecture | new |
| P1-02 | How many attention heads? | 8 | §3.2 | hyperparams | learning |
| P1-03 | What is scaled by 1/sqrt(d_k)? | The dot-product logits | Eq 1 | math | Review |
| P1-04 | | | | | new |

## 3. Review log

| ID | Date | Grade | Next due |
| --- | --- | --- | --- |
| P1-01 | 2024-01-02 | good | 2024-01-05 |
| P1-02 | 2024-01-02 | Again | 2024-01-03 |
| P1-02 | 2024-01-03 | good | 2024-01-06 |
| | | | |

## 4. Spaced-repetition schedule

- **Intervals**: 1 → 3 → 7 → 16 → 35 days.
`

describe('parseFlashcards', () => {
  it('parses the card table with every field, treating id-carrying rows as cards', () => {
    const deck = parseFlashcards(DECK)
    expect(deck.cards).toHaveLength(4)
    expect(deck.cards[0]).toEqual({
      id: 'P1-01',
      front: 'What replaces recurrence?',
      back: 'Self-attention',
      anchor: '§3',
      tag: 'architecture',
    })
    expect(deck.cards[1]!.front).toBe('How many attention heads?')
    // The not-yet-filled row still carries an id, so it is a card (the identity
    // is id-based and shared with the Go writer) — it just has no content yet.
    expect(deck.cards[3]).toEqual({ id: 'P1-04', front: '', back: '', anchor: '', tag: '' })
  })

  it('parses the review log and normalizes the grade case', () => {
    const deck = parseFlashcards(DECK)
    expect(deck.reviews).toHaveLength(3)
    expect(deck.reviews[0]).toEqual({
      id: 'P1-01',
      date: '2024-01-02',
      grade: 'good',
      nextDue: '2024-01-05',
    })
    expect(deck.reviews[1]!.grade).toBe('again')
  })

  it('filters the review log by card id', () => {
    const deck = parseFlashcards(DECK)
    const p1_02 = reviewsForCard(deck, 'P1-02')
    expect(p1_02.map((r) => r.grade)).toEqual(['again', 'good'])
    expect(reviewsForCard(deck, 'missing')).toEqual([])
  })

  it('is tolerant: a document with no tables yields an empty deck', () => {
    expect(parseFlashcards('# Nothing\n\nJust prose.')).toEqual({ cards: [], reviews: [] })
  })

  it('is tolerant of a deck with cards but no review log', () => {
    const deck = parseFlashcards(
      '| ID | Front | Back |\n| - | - | - |\n| A1 | q | a |',
    )
    expect(deck.cards).toHaveLength(1)
    expect(deck.cards[0]!.id).toBe('A1')
    expect(deck.reviews).toEqual([])
  })

  it('unescapes | and <br> inside a cell (shared table parser)', () => {
    const deck = parseFlashcards('| ID | Front | Back |\n| - | - | - |\n| C1 | left\\|right | a<br>b |')
    expect(deck.cards[0]!.front).toBe('left|right')
    expect(deck.cards[0]!.back).toBe('a\nb')
  })

  it('resolves columns by name even when extra columns are present', () => {
    const deck = parseFlashcards(
      '| Stage | Back (answer) | Front (question) | ID | Note |\n| - | - | - | - | - |\n| review | A | Q | X9 | extra |',
    )
    expect(deck.cards[0]).toEqual({ id: 'X9', front: 'Q', back: 'A', anchor: '', tag: '' })
  })

  it('gives an id-less deck empty card ids (no positional fallback)', () => {
    const deck = parseFlashcards(
      '| Front | Back |\n| - | - |\n| a question | an answer |',
    )
    expect(deck.cards).toHaveLength(1)
    expect(deck.cards[0]).toEqual({
      id: '',
      front: 'a question',
      back: 'an answer',
      anchor: '',
      tag: '',
    })
  })

  it('does not claim Card ID / Reference / Tags as id/anchor/tag (anchored, word-boundary)', () => {
    const deck = parseFlashcards(
      '| Card ID | Front | Back | Reference | Tags |\n| - | - | - | - | - |\n| X9 | q | a | §3 | arch |',
    )
    expect(deck.cards[0]).toEqual({ id: '', front: 'q', back: 'a', anchor: '', tag: '' })
  })
})

describe('normalizeStage / normalizeGrade', () => {
  it('folds known stage tokens and defaults unknown to new', () => {
    expect(normalizeStage('learning')).toBe('learning')
    expect(normalizeStage('Review')).toBe('review')
    expect(normalizeStage('  NEW ')).toBe('new')
    expect(normalizeStage('bogus')).toBe('new')
    expect(normalizeStage('')).toBe('new')
  })

  it('keeps the four grades and drops anything else', () => {
    expect(normalizeGrade('Again')).toBe('again')
    expect(normalizeGrade('EASY')).toBe('easy')
    expect(normalizeGrade('perfect')).toBe('')
    expect(normalizeGrade('')).toBe('')
  })

  it('strips Markdown emphasis/punctuation (mirrors the Go normalizers)', () => {
    expect(normalizeStage('**review**')).toBe('review')
    expect(normalizeStage('`learning`.')).toBe('learning')
    expect(normalizeGrade('`good`')).toBe('good')
    expect(normalizeGrade('**easy**.')).toBe('easy')
  })
})
