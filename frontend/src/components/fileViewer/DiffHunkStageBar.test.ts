// Unit tests for DiffHunkStageBar — hunk→HunkRange mapping (Phase 6)
//
// The component itself is rendered in the app; the testable unit is the pure
// `hunkToRange` mapping that converts a structured diff hunk (HunkDiffInfo)
// into the backend HunkRange (old-file line coordinates).

import { describe, it, expect } from 'vitest'
import { hunkToRange } from './diffHunkRange'
import type { HunkDiffInfo } from '@/types/models'

/** Helper: build a minimal HunkDiffInfo. */
function hunk(
  oldStart: number,
  oldCount: number,
  newStart = 1,
  newCount = 1,
  staged = false,
  oldChangeStart = oldStart,
  newChangeStart = newStart,
): HunkDiffInfo {
  return {
    old_start: oldStart,
    old_count: oldCount,
    new_start: newStart,
    new_count: newCount,
    old_change_start: oldChangeStart,
    new_change_start: newChangeStart,
    staged,
    diff: '',
  }
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

  it('works for staged hunks (staged flag does not affect the range)', () => {
    const range = hunkToRange(hunk(3, 4, 5, 6, true))
    expect(range).toEqual({ start_line: 3, end_line: 6 })
  })

  it('hunkToRange uses old_start (with context), not old_change_start', () => {
    // A hunk with 3 context lines: header says -10,7 but the first changed
    // line is at old line 13. hunkToRange must use the header start (10)
    // for git operations, not the change-start (13).
    const h = hunk(10, 7, 10, 7, false, 13, 13)
    expect(hunkToRange(h)).toEqual({ start_line: 10, end_line: 16 })
  })
})
