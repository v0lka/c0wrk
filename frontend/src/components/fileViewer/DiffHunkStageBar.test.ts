// Unit tests for DiffHunkStageBar — hunk→HunkRange mapping (Phase 6)
//
// The component itself is rendered in the app; the testable unit is the pure
// `hunkToRange` mapping that converts a parsed diff hunk into the backend
// HunkRange (old-file line coordinates).

import { describe, it, expect } from 'vitest'
import { hunkToRange } from './diffHunkRange'
import type { DiffHunk } from '@/lib/diffParser'

/** Helper: build a minimal DiffHunk. */
function hunk(oldStart: number, oldCount: number, newStart = 1, newCount = 1): DiffHunk {
  return { id: `${oldStart}`, oldStart, oldCount, newStart, newCount, lines: [] }
}

describe('hunkToRange', () => {
  it('maps a standard modification hunk to old-file coordinates', () => {
    expect(hunkToRange(hunk(1, 3))).toEqual({ start_line: 1, end_line: 3 })
  })

  it('computes end_line = oldStart + oldCount - 1', () => {
    expect(hunkToRange(hunk(10, 5))).toEqual({ start_line: 10, end_line: 14 })
  })

  it('handles a single-line hunk (count 1)', () => {
    expect(hunkToRange(hunk(7, 1))).toEqual({ start_line: 7, end_line: 7 })
  })

  it('maps a pure-addition hunk (oldCount 0) to end_line = start_line - 1', () => {
    // oldCount 0 means no old-file lines existed. The backend hunkInRange
    // derives end = start + count - 1, so for zero old lines end_line is
    // start_line - 1 — exactly what the backend HunkRange expects (no clamp).
    expect(hunkToRange(hunk(5, 0))).toEqual({ start_line: 5, end_line: 4 })
  })

  it('ignores new-file coordinates (only old-file matters for the range)', () => {
    const range = hunkToRange(hunk(3, 4, 99, 99))
    expect(range).toEqual({ start_line: 3, end_line: 6 })
  })
})
