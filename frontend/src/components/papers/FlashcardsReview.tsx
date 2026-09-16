// Flashcards section (E3) — interactive active-recall review over a paper's
// `flashcards.md` deck.
//
// The deck (cards + review log) is parsed from the raw artifact (lib/flashcards)
// and driven by the pure interval scheduler (lib/spacedRepetition). Review is
// READ-ONLY here: the session's grades live in component state and the computed
// next-due date is shown per card — nothing is written back to disk. An artifact
// that carries no recognizable card table falls back to the plain Markdown
// render (via PaperMarkdownSection), so an unparsed deck is never hidden.

import { useCallback, useEffect, useMemo, useState } from 'react'
import { parseFlashcards, reviewsForCard, type FlashcardGrade } from '@/lib/flashcards'
import {
  advanceIndex,
  intervalDays,
  scheduleAfter,
  stateFromReviews,
  type ReviewState,
} from '@/lib/spacedRepetition'
import { cn } from '@/lib/utils'
import { commitFlashcardReview } from '@/stores/paperStore'
import { PaperMarkdownSection } from './PaperMarkdownSection'
import type { PaperArtifact } from './usePaperArtifacts'

interface FlashcardsReviewProps {
  artifact: PaperArtifact
  /** Shown when the section's candidate file does not exist. */
  emptyText: string
  /** Absolute path of the rendered document (relative images resolve against it). */
  baseFilePath?: string | null
  testId: string
  /** Review date (`YYYY-MM-DD`); defaults to today. Injectable for tests. */
  today?: string
  /** When set, each grade is written back to the deck through the store (the
   *  read-only review becomes a persisted one). Omitted → a pure local review. */
  paperId?: string
}

const GRADES: ReadonlyArray<{ grade: FlashcardGrade; label: string; className: string }> = [
  { grade: 'again', label: 'Again', className: 'text-destructive border-destructive/40 hover:bg-destructive/10' },
  { grade: 'hard', label: 'Hard', className: 'text-warning border-warning/40 hover:bg-warning/10' },
  { grade: 'good', label: 'Good', className: 'text-success border-success/40 hover:bg-success/10' },
  { grade: 'easy', label: 'Easy', className: 'text-info border-info/40 hover:bg-info/10' },
]

const STAGE_BADGE: Record<ReviewState['stage'], string> = {
  new: 'bg-muted/50 text-muted-foreground',
  learning: 'bg-warning/15 text-warning',
  review: 'bg-success/15 text-success',
}

function todayISO(): string {
  return new Date().toISOString().slice(0, 10)
}

export function FlashcardsReview({
  artifact,
  emptyText,
  baseFilePath,
  testId,
  today: todayProp,
  paperId,
}: FlashcardsReviewProps) {
  const fallback = (
    <PaperMarkdownSection
      artifact={artifact}
      emptyText={emptyText}
      baseFilePath={baseFilePath}
      testId={testId}
    />
  )

  const deck = useMemo(() => parseFlashcards(artifact.content), [artifact.content])
  const today = todayProp ?? todayISO()

  const [index, setIndex] = useState(0)
  const [flipped, setFlipped] = useState(false)
  const [results, setResults] = useState<Record<number, { grade: FlashcardGrade; dueDate: string }>>({})
  const [finished, setFinished] = useState(false)

  // A different artifact in the same tab starts a clean session.
  useEffect(() => {
    setIndex(0)
    setFlipped(false)
    setResults({})
    setFinished(false)
  }, [artifact.content])

  const rate = useCallback(
    (grade: FlashcardGrade, state: ReviewState, cardIndex: number) => {
      const next = scheduleAfter(state, grade, today)
      setResults((prev) => ({ ...prev, [cardIndex]: { grade, dueDate: next.dueDate } }))
      // Write-back (only when the owner paper is known): fire-and-forget so the
      // review stays responsive; the watcher's refetch is authoritative.
      const cardId = deck.cards[cardIndex]?.id ?? ''
      if (paperId !== undefined && cardId !== '') {
        void commitFlashcardReview(paperId, cardId, grade)
      }
      if (cardIndex + 1 < deck.cards.length) {
        setIndex(cardIndex + 1)
        setFlipped(false)
      } else {
        setFinished(true)
      }
    },
    [deck.cards, paperId, today],
  )

  if (artifact.loading || artifact.error !== null || artifact.content === '') return fallback
  if (deck.cards.length === 0) return fallback

  const cards = deck.cards
  const card = cards[Math.min(index, cards.length - 1)]!
  const cardReviews = reviewsForCard(deck, card.id)
  const state = stateFromReviews(cardReviews)

  if (finished) {
    return (
      <div
        data-testid={`${testId}-done`}
        className="flex min-h-0 flex-1 flex-col gap-2 overflow-auto custom-scrollbar px-3 py-3"
      >
        <p className="text-[12px] font-medium text-foreground">
          Review complete — {cards.length} {cards.length === 1 ? 'card' : 'cards'}.
        </p>
        <ul className="flex flex-col gap-1">
          {cards.map((c, i) => {
            const result = results[i]
            return (
              <li
                key={i}
                data-testid={`${testId}-result`}
                className="flex items-center gap-2 text-[11px] text-muted-foreground"
              >
                <span className="shrink-0 font-mono text-[10px] text-foreground/70">{c.id || `#${i + 1}`}</span>
                <span className="truncate">{c.front}</span>
                {result && (
                  <span className="ml-auto shrink-0">
                    {result.grade} → {result.dueDate}
                  </span>
                )}
              </li>
            )
          })}
        </ul>
        <button
          type="button"
          data-testid={`${testId}-restart`}
          onClick={() => {
            setIndex(0)
            setFlipped(false)
            setResults({})
            setFinished(false)
          }}
          className="self-start rounded border border-border px-2 py-0.5 text-[11px] text-muted-foreground hover:bg-muted/40 hover:text-foreground"
        >
          Restart
        </button>
      </div>
    )
  }

  return (
    <div data-testid={testId} className="flex min-h-0 flex-1 flex-col gap-2 overflow-auto custom-scrollbar px-3 py-3">
      <div className="flex items-center gap-2 text-[10px] text-muted-foreground">
        <span data-testid={`${testId}-progress`}>
          Card {index + 1} / {cards.length}
        </span>
        <span data-testid={`${testId}-stage`} data-stage={state.stage} className={cn('rounded px-1 py-0.5 uppercase tracking-wide', STAGE_BADGE[state.stage])}>
          {state.stage}
        </span>
        {card.tag !== '' && (
          <span data-testid={`${testId}-tag`} className="truncate rounded bg-muted/40 px-1 py-0.5">
            {card.tag}
          </span>
        )}
      </div>

      <div className="rounded border border-border bg-secondary/20 px-3 py-2">
        <p data-testid={`${testId}-front`} className="whitespace-pre-wrap break-words text-[12px] text-foreground">
          {card.front}
        </p>
      </div>

      {!flipped ? (
        <button
          type="button"
          data-testid={`${testId}-reveal`}
          onClick={() => setFlipped(true)}
          className="self-start rounded border border-border px-2 py-1 text-[11px] text-foreground hover:bg-muted/40"
        >
          Show answer
        </button>
      ) : (
        <>
          <div className="rounded border border-border bg-background px-3 py-2">
            <p data-testid={`${testId}-back`} className="whitespace-pre-wrap break-words text-[12px] text-foreground">
              {card.back}
            </p>
            {card.anchor !== '' && (
              <p data-testid={`${testId}-anchor`} className="mt-1 text-[10px] text-muted-foreground">
                {card.anchor}
              </p>
            )}
          </div>
          <div data-testid={`${testId}-grades`} className="flex flex-wrap gap-1">
            {GRADES.map(({ grade, label, className }) => {
              const days = intervalDays(advanceIndex(state.intervalIndex, grade))
              return (
                <button
                  key={grade}
                  type="button"
                  data-testid={`${testId}-rate-${grade}`}
                  onClick={() => rate(grade, state, index)}
                  className={cn('rounded border px-2 py-1 text-[11px] transition-colors', className)}
                >
                  {label} · {days}d
                </button>
              )
            })}
          </div>
        </>
      )}
    </div>
  )
}
