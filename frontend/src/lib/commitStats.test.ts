// Unit tests for commitStats — pure stats bucketing and color mapping.

import { describe, it, expect } from 'vitest'
import { statBucket, countFileStats, statColorClass, totalChanges } from './commitStats'
import type { CommitFile, CommitStats } from '@/types/models'

function file(status: string): CommitFile {
  return { path: `x/${Math.random()}`, status }
}

describe('statBucket', () => {
  it('buckets A as added', () => {
    expect(statBucket('A')).toBe('added')
  })

  it('buckets D as deleted', () => {
    expect(statBucket('D')).toBe('deleted')
  })

  it('buckets M as modified', () => {
    expect(statBucket('M')).toBe('modified')
  })

  it('buckets scored rename/copy/typechange forms by their leading letter (→ modified)', () => {
    expect(statBucket('R100')).toBe('modified')
    expect(statBucket('C75')).toBe('modified')
    expect(statBucket('T')).toBe('modified')
  })

  it('buckets unknown and empty statuses as modified (never dropped)', () => {
    expect(statBucket('X')).toBe('modified')
    expect(statBucket('')).toBe('modified')
  })

  it('uppercases the leading letter', () => {
    expect(statBucket('a')).toBe('added')
    expect(statBucket('d')).toBe('deleted')
  })
})

describe('countFileStats', () => {
  it('returns zeros for an empty file list', () => {
    expect(countFileStats([])).toEqual({ added: 0, modified: 0, deleted: 0 })
  })

  it('buckets a mixed list into the three counters', () => {
    const files = [file('A'), file('A'), file('M'), file('R100'), file('D'), file('C50'), file('T')]
    expect(countFileStats(files)).toEqual({ added: 2, modified: 4, deleted: 1 })
  })

  it('matches the shape of a backend CommitStats triple', () => {
    const stats: CommitStats = countFileStats([file('A'), file('D')])
    expect(stats).toEqual({ added: 1, modified: 0, deleted: 1 })
  })
})

describe('statColorClass', () => {
  it('maps added → success', () => {
    expect(statColorClass('added')).toBe('text-success')
  })

  it('maps modified → warning', () => {
    expect(statColorClass('modified')).toBe('text-warning')
  })

  it('maps deleted → destructive', () => {
    expect(statColorClass('deleted')).toBe('text-destructive')
  })
})

describe('totalChanges', () => {
  it('sums the three counters', () => {
    expect(totalChanges({ added: 2, modified: 3, deleted: 1 })).toBe(6)
  })

  it('is zero for an all-zero triple', () => {
    expect(totalChanges({ added: 0, modified: 0, deleted: 0 })).toBe(0)
  })
})
