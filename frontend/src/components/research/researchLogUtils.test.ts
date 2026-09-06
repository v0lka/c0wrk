// Pure tests for the research-log rendering helpers, including the [20]b
// render cap.
import { describe, expect, it } from 'vitest'
import {
  latestLogEntries,
  formatLogTime,
  RESEARCH_LOG_RENDER_CAP,
} from './researchLogUtils'
import type { ResearchLogEntry } from '@/types/models'

function entry(id: number): ResearchLogEntry {
  return {
    id: String(id),
    kind: 'note',
    created_at: `2026-01-0${(id % 9) + 1}T00:00:00Z`,
    message: `entry ${id}`,
  }
}

describe('latestLogEntries', () => {
  it('returns every entry newest-first when uncapped', () => {
    const log = [entry(1), entry(2), entry(3)]
    expect(latestLogEntries(log).map((e) => e.id)).toEqual(['3', '2', '1'])
  })

  it('caps to the newest N entries ([20]b)', () => {
    const log = Array.from({ length: 250 }, (_, i) => entry(i + 1))
    const capped = latestLogEntries(log, RESEARCH_LOG_RENDER_CAP)
    expect(capped).toHaveLength(100)
    // The NEWEST 100 — the tail of the append-only array, reversed.
    expect(capped[0]!.id).toBe('250')
    expect(capped[99]!.id).toBe('151')
  })

  it('never grows a short log past its length', () => {
    const log = [entry(1), entry(2)]
    expect(latestLogEntries(log, RESEARCH_LOG_RENDER_CAP)).toHaveLength(2)
  })

  it('does not mutate the input array', () => {
    const log = [entry(1), entry(2), entry(3)]
    latestLogEntries(log, 2)
    expect(log.map((e) => e.id)).toEqual(['1', '2', '3'])
  })
})

describe('formatLogTime', () => {
  it('trims ISO timestamps to a fixed width', () => {
    expect(formatLogTime('2026-01-03T10:05:07Z')).toBe('2026-01-03 10:05:07')
  })
})
