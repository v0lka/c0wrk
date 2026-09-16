// Tests for lib/spacedRepetition.ts — the pure interval scheduler.

import { describe, it, expect, vi } from 'vitest'
import type { ReviewEntry } from './flashcards'
import {
  addDays,
  advanceIndex,
  INTERVAL_SCHEDULE_DAYS,
  intervalDays,
  MAX_INTERVAL_INDEX,
  scheduleAfter,
  stateForIndex,
  stateFromReviews,
  stageForIndex,
  todayLocalISO,
} from './spacedRepetition'

function review(grade: ReviewEntry['grade'], nextDue = '', date = '2024-01-01'): ReviewEntry {
  return { id: 'X', date, grade, nextDue }
}

describe('stageForIndex', () => {
  it('derives the stage from the rung', () => {
    expect(stageForIndex(-1)).toBe('new')
    expect(stageForIndex(0)).toBe('learning')
    expect(stageForIndex(1)).toBe('review')
    expect(stageForIndex(99)).toBe('review')
  })
})

describe('INTERVAL_SCHEDULE_DAYS / intervalDays', () => {
  it('is the skill default ladder', () => {
    expect(INTERVAL_SCHEDULE_DAYS).toEqual([1, 3, 7, 16, 35])
  })

  it('clamps out-of-range indices', () => {
    expect(intervalDays(-1)).toBe(1)
    expect(intervalDays(0)).toBe(1)
    expect(intervalDays(1)).toBe(3)
    expect(intervalDays(MAX_INTERVAL_INDEX)).toBe(35)
    expect(intervalDays(99)).toBe(35)
  })
})

describe('advanceIndex', () => {
  it('resets to the first rung on again', () => {
    expect(advanceIndex(3, 'again')).toBe(0)
    expect(advanceIndex(-1, 'again')).toBe(0)
  })

  it('holds the rung on hard', () => {
    expect(advanceIndex(2, 'hard')).toBe(2)
    expect(advanceIndex(-1, 'hard')).toBe(0)
  })

  it('promotes one rung on good and easy, capped at the last', () => {
    expect(advanceIndex(0, 'good')).toBe(1)
    expect(advanceIndex(1, 'easy')).toBe(2)
    expect(advanceIndex(MAX_INTERVAL_INDEX, 'good')).toBe(MAX_INTERVAL_INDEX)
    expect(advanceIndex(-1, 'good')).toBe(0)
    expect(advanceIndex(-1, 'easy')).toBe(0)
  })
})

describe('addDays', () => {
  it('does UTC date math across a month boundary', () => {
    expect(addDays('2024-01-31', 1)).toBe('2024-02-01')
    expect(addDays('2024-02-28', 1)).toBe('2024-02-29') // leap year
    expect(addDays('2024-01-01', 35)).toBe('2024-02-05')
  })

  it('returns an unparseable input unchanged', () => {
    expect(addDays('', 3)).toBe('')
    expect(addDays('not-a-date', 3)).toBe('not-a-date')
    expect(addDays('2024-02-31', 1)).toBe('2024-02-31') // overflowed day rejected
  })
})

describe('scheduleAfter', () => {
  it('schedules a new card one day out on its first review', () => {
    const next = scheduleAfter(stateForIndex(-1), 'good', '2024-01-01')
    expect(next).toEqual({ intervalIndex: 0, stage: 'learning', dueDate: '2024-01-02' })
  })

  it('promotes an on-ladder card to the next interval on good', () => {
    const next = scheduleAfter(stateForIndex(0), 'good', '2024-01-02')
    expect(next).toEqual({ intervalIndex: 1, stage: 'review', dueDate: '2024-01-05' })
  })

  it('resets on again and holds on hard', () => {
    expect(scheduleAfter(stateForIndex(2), 'again', '2024-01-02').dueDate).toBe('2024-01-03')
    expect(scheduleAfter(stateForIndex(2), 'hard', '2024-01-02').dueDate).toBe('2024-01-09')
  })
})

describe('stateFromReviews', () => {
  it('replays the log to recover the current rung and stage', () => {
    // new → good (rung 0) → again (rung 0) → good (rung 1) → good (rung 2)
    const state = stateFromReviews([
      review('good'),
      review('again'),
      review('good'),
      review('good'),
    ])
    expect(state).toEqual({ intervalIndex: 2, stage: 'review' })
  })

  it('ignores ungraded entries and starts new', () => {
    expect(stateFromReviews([])).toEqual({ intervalIndex: -1, stage: 'new' })
    expect(stateFromReviews([review('')])).toEqual({ intervalIndex: -1, stage: 'new' })
  })
})

describe('todayLocalISO', () => {
  it('returns the LOCAL calendar date, not the UTC one', () => {
    const realTZ = process.env.TZ
    try {
      // A non-UTC zone so the local and UTC calendar dates can diverge.
      process.env.TZ = 'Asia/Kolkata' // UTC+5:30
      vi.useFakeTimers()
      // 2024-03-05T19:00:00Z is 2024-03-06 00:30 local (UTC+5:30).
      vi.setSystemTime(new Date('2024-03-05T19:00:00Z'))
      expect(todayLocalISO()).toBe('2024-03-06')
      // The UTC spelling of the same instant is the PREVIOUS day — the bug this
      // guards against.
      expect(new Date().toISOString().slice(0, 10)).toBe('2024-03-05')
    } finally {
      vi.useRealTimers()
      process.env.TZ = realTZ
    }
  })
})
