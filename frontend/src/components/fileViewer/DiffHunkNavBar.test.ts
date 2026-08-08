// Unit tests for diffHunkNav — index clamping and combobox entry building.
//
// The component (DiffHunkNavBar) is rendered in the app; the testable units
// are the pure helpers: clampHunkIndex (bounds enforcement) and
// buildFileHunkEntries (summary/range derivation via the shared review parser).

import { describe, it, expect } from 'vitest'
import { clampHunkIndex, buildFileHunkEntries } from './diffHunkNav'
import type { HunkDiffInfo } from '@/types/models'

/** Build a HunkDiffInfo with sensible defaults; only set the fields that matter. */
function hunk(overrides: Partial<HunkDiffInfo> = {}): HunkDiffInfo {
  return {
    old_start: 1,
    old_count: 1,
    new_start: 1,
    new_count: 1,
    old_change_start: 1,
    new_change_start: 1,
    staged: false,
    // A minimal raw block: 3 context + 1 add + 1 del.
    diff: ' context\n+added\n-removed\n',
    ...overrides,
  }
}

describe('clampHunkIndex', () => {
  it('returns 0 for an empty hunk set', () => {
    expect(clampHunkIndex(0, 0)).toBe(0)
    expect(clampHunkIndex(5, 0)).toBe(0)
  })
  it('clamps below 0 to 0', () => {
    expect(clampHunkIndex(-1, 3)).toBe(0)
    expect(clampHunkIndex(-100, 3)).toBe(0)
  })
  it('clamps above the last index to total-1', () => {
    expect(clampHunkIndex(5, 3)).toBe(2)
    expect(clampHunkIndex(3, 3)).toBe(2)
  })
  it('returns the index unchanged when in range', () => {
    expect(clampHunkIndex(1, 3)).toBe(1)
    expect(clampHunkIndex(2, 3)).toBe(2)
  })
  it('handles a single hunk (index 0 only)', () => {
    expect(clampHunkIndex(0, 1)).toBe(0)
    expect(clampHunkIndex(1, 1)).toBe(0)
  })
})

describe('buildFileHunkEntries', () => {
  it('returns an empty array for no hunks', () => {
    expect(buildFileHunkEntries([])).toEqual([])
  })

  it('assigns sequential indices in document order', () => {
    const entries = buildFileHunkEntries([hunk(), hunk(), hunk()])
    expect(entries.map((e) => e.index)).toEqual([0, 1, 2])
  })

  it('derives added/removed counts and range label from the raw block', () => {
    // 2 added, 1 removed; new_start=10 is the first hunk line (the context
    // line), so the two added lines land at 11 and 12 → L11-12.
    const entries = buildFileHunkEntries([
      hunk({ new_start: 10, diff: ' a\n+b\n+c\n-d\n' }),
    ])
    expect(entries).toEqual([
      expect.objectContaining({
        index: 0,
        summary: expect.objectContaining({ added: 2, removed: 1 }),
        rangeLabel: 'L11-12',
      }),
    ])
  })

  it('formats a single-line addition as LX (no dash)', () => {
    // Leading context advances newNum from new_start=5 to 6, so the add is L6.
    const entries = buildFileHunkEntries([
      hunk({ new_start: 5, diff: ' a\n+b\n' }),
    ])
    expect(entries).toEqual([
      expect.objectContaining({ rangeLabel: 'L6' }),
    ])
  })

  it('falls back to new_start for a pure-deletion hunk (no additions)', () => {
    // No added lines → firstNewLine/lastNewLine null → fallback to new_start.
    const entries = buildFileHunkEntries([
      hunk({ new_start: 7, diff: ' a\n-b\n' }),
    ])
    expect(entries).toEqual([
      expect.objectContaining({
        rangeLabel: 'L7',
        summary: expect.objectContaining({ added: 0, removed: 1 }),
      }),
    ])
  })

  it('preserves the staged flag for an informational tag', () => {
    const entries = buildFileHunkEntries([
      hunk({ staged: false }),
      hunk({ staged: true }),
    ])
    expect(entries).toEqual([
      expect.objectContaining({ staged: false }),
      expect.objectContaining({ staged: true }),
    ])
  })
})
