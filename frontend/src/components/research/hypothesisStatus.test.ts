// Unit tests for the shared hypothesis-status module: the transition map
// must mirror the backend state machine (core/research/writer.go
// `transitions`), and statusOptions must expose only legal targets while
// always keeping the current status selectable (controlled <select>s need a
// matching option). Pure module — no DOM needed.
import { describe, it, expect } from 'vitest'
import { STATUS_TRANSITIONS, statusOptions } from './hypothesisStatus'

describe('STATUS_TRANSITIONS', () => {
  it('mirrors the backend state machine: forward-only, terminal sinks', () => {
    expect(STATUS_TRANSITIONS).toEqual({
      open: ['in-progress', 'cancelled'],
      'in-progress': ['confirmed', 'refuted', 'cancelled'],
      confirmed: [],
      refuted: [],
      cancelled: [],
    })
  })

  it('never allows skipping in-progress (open → confirmed/refuted) or going backward', () => {
    expect(STATUS_TRANSITIONS.open).not.toContain('confirmed')
    expect(STATUS_TRANSITIONS.open).not.toContain('refuted')
    expect(STATUS_TRANSITIONS['in-progress']).not.toContain('open')
    for (const terminal of ['confirmed', 'refuted', 'cancelled'] as const) {
      expect(STATUS_TRANSITIONS[terminal]).toHaveLength(0)
    }
  })
})

describe('statusOptions', () => {
  it('lists the current status first, then its legal transition targets', () => {
    expect(statusOptions('open')).toEqual(['open', 'in-progress', 'cancelled'])
    expect(statusOptions('in-progress')).toEqual([
      'in-progress',
      'confirmed',
      'refuted',
      'cancelled',
    ])
  })

  it('offers only the current status for terminal statuses', () => {
    expect(statusOptions('confirmed')).toEqual(['confirmed'])
    expect(statusOptions('refuted')).toEqual(['refuted'])
    expect(statusOptions('cancelled')).toEqual(['cancelled'])
  })

  it('falls back to the current status alone for unknown values', () => {
    // A node/draft from an older backend may carry a non-canonical status;
    // the select must still render a matching option and offer no targets.
    expect(statusOptions('done')).toEqual(['done'])
    expect(statusOptions('')).toEqual([''])
  })
})
