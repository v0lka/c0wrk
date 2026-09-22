// Unit tests for the two-axis porcelain classification helpers in gitStatus.

import { describe, it, expect } from 'vitest'
import {
  CONFLICT_COMBOS,
  isMergeConflict,
  hasStagedChanges,
  hasUnstagedChanges,
  isUntracked,
  classifyEntries,
  rowStatusChar,
} from '@/lib/gitStatus'
import type { StageSide } from '@/lib/gitStatus'
import type { GitPanelEntry } from '@/stores/gitPanelStore'

// --- Test helpers ---

/**
 * Build a GitPanelEntry from a two-char porcelain `XY` pair (index + worktree).
 * `status` mirrors the backend convention: the change-type letter, or `A` for
 * untracked (`??`) paths.
 */
function entryXY(xy: string, path = 'f.txt'): GitPanelEntry {
  const index = xy.charAt(0)
  const worktree = xy.charAt(1)
  return {
    path,
    status: xy === '??' ? 'A' : index !== ' ' ? index : worktree,
    staged: index !== ' ' && index !== '?',
    diffStat: null,
    indexStatus: index,
    worktreeStatus: worktree,
  }
}

// --- CONFLICT_COMBOS ---

describe('CONFLICT_COMBOS', () => {
  it('holds exactly the seven porcelain conflict combinations', () => {
    expect([...CONFLICT_COMBOS].sort()).toEqual(
      ['AA', 'AU', 'DD', 'DU', 'UA', 'UD', 'UU'].sort(),
    )
    expect(CONFLICT_COMBOS.size).toBe(7)
  })
})

// --- isMergeConflict ---

describe('isMergeConflict', () => {
  it('is true for each of the seven conflict combos', () => {
    for (const combo of ['UU', 'AA', 'DD', 'AU', 'UD', 'UA', 'DU']) {
      expect(isMergeConflict(entryXY(combo))).toBe(true)
    }
  })

  it('is false for non-conflict combos', () => {
    for (const combo of ['M ', ' M', 'MM', '??', 'AM', 'MA', 'AD', 'DA', 'A ', ' D', 'RM']) {
      expect(isMergeConflict(entryXY(combo))).toBe(false)
    }
  })
})

// --- axis predicates ---

describe('hasStagedChanges (index axis)', () => {
  it('is true iff the index code is a real change code', () => {
    expect(hasStagedChanges(entryXY('M '))).toBe(true)
    expect(hasStagedChanges(entryXY('MM'))).toBe(true)
    expect(hasStagedChanges(entryXY('AM'))).toBe(true)
    expect(hasStagedChanges(entryXY('UU'))).toBe(true)

    expect(hasStagedChanges(entryXY(' M'))).toBe(false)
    expect(hasStagedChanges(entryXY('??'))).toBe(false)
  })
})

describe('hasUnstagedChanges (worktree axis)', () => {
  it('is true only for a real worktree change that is not untracked or a conflict', () => {
    expect(hasUnstagedChanges(entryXY(' M'))).toBe(true)
    expect(hasUnstagedChanges(entryXY('MM'))).toBe(true)
    expect(hasUnstagedChanges(entryXY('AM'))).toBe(true)
    expect(hasUnstagedChanges(entryXY('AD'))).toBe(true)
  })

  it('excludes untracked paths', () => {
    expect(hasUnstagedChanges(entryXY('??'))).toBe(false)
  })

  it('excludes every merge-conflict combo', () => {
    for (const combo of ['UU', 'AA', 'DD', 'AU', 'UD', 'UA', 'DU']) {
      expect(hasUnstagedChanges(entryXY(combo))).toBe(false)
    }
  })

  it('is false when the worktree axis is blank', () => {
    expect(hasUnstagedChanges(entryXY('M '))).toBe(false)
    expect(hasUnstagedChanges(entryXY('A '))).toBe(false)
  })
})

describe('isUntracked', () => {
  it('is true only for the ?? combo', () => {
    expect(isUntracked(entryXY('??'))).toBe(true)
    expect(isUntracked(entryXY('M '))).toBe(false)
    expect(isUntracked(entryXY(' M'))).toBe(false)
  })
})

// --- classifyEntries ---

describe('classifyEntries', () => {
  it('places an MM entry in BOTH staged and unstaged', () => {
    const mm = entryXY('MM')
    const { staged, unstaged, untracked } = classifyEntries([mm])
    expect(staged).toContain(mm)
    expect(unstaged).toContain(mm)
    expect(untracked).toEqual([])
  })

  it("'M ' is staged only", () => {
    const e = entryXY('M ')
    const { staged, unstaged, untracked } = classifyEntries([e])
    expect(staged).toEqual([e])
    expect(unstaged).toEqual([])
    expect(untracked).toEqual([])
  })

  it("' M' is unstaged only", () => {
    const e = entryXY(' M')
    const { staged, unstaged, untracked } = classifyEntries([e])
    expect(staged).toEqual([])
    expect(unstaged).toEqual([e])
    expect(untracked).toEqual([])
  })

  it("'??' is untracked only", () => {
    const e = entryXY('??')
    const { staged, unstaged, untracked } = classifyEntries([e])
    expect(staged).toEqual([])
    expect(unstaged).toEqual([])
    expect(untracked).toEqual([e])
  })

  it("'AM' is staged AND unstaged", () => {
    const e = entryXY('AM')
    const { staged, unstaged, untracked } = classifyEntries([e])
    expect(staged).toEqual([e])
    expect(unstaged).toEqual([e])
    expect(untracked).toEqual([])
  })

  it("conflict combos ('UU'/'AA'/'DD') are staged only, never unstaged", () => {
    for (const combo of ['UU', 'AA', 'DD', 'AU', 'UD', 'UA', 'DU']) {
      const e = entryXY(combo)
      const { staged, unstaged, untracked } = classifyEntries([e])
      expect(staged).toEqual([e])
      expect(unstaged).toEqual([])
      expect(untracked).toEqual([])
    }
  })

  it('splits a mixed set and preserves input order within each section', () => {
    const a = entryXY('MM', 'a.txt')
    const b = entryXY('M ', 'b.txt')
    const c = entryXY(' M', 'c.txt')
    const d = entryXY('??', 'd.txt')

    const { staged, unstaged, untracked } = classifyEntries([a, b, c, d])

    expect(staged.map((e) => e.path)).toEqual(['a.txt', 'b.txt'])
    expect(unstaged.map((e) => e.path)).toEqual(['a.txt', 'c.txt'])
    expect(untracked.map((e) => e.path)).toEqual(['d.txt'])
  })

  it('does not mutate the input and returns fresh arrays', () => {
    const entries = [entryXY('MM'), entryXY('??')]
    const snapshot = entries.slice()
    const result = classifyEntries(entries)
    expect(entries).toEqual(snapshot)
    expect(result.staged).not.toBe(entries)
    expect(result.untracked).not.toBe(entries)
  })

  it('handles an empty input', () => {
    expect(classifyEntries([])).toEqual({ staged: [], unstaged: [], untracked: [] })
  })
})

// --- rowStatusChar ---

describe('rowStatusChar', () => {
  it("returns each axis's own porcelain code", () => {
    const e = entryXY('MM')
    expect(rowStatusChar(e, 'index')).toBe('M')
    expect(rowStatusChar(e, 'worktree')).toBe('M')

    const conflict = entryXY('UU')
    expect(rowStatusChar(conflict, 'index')).toBe('U')
    expect(rowStatusChar(conflict, 'worktree')).toBe('U')
  })

  it('returns the untracked marker on the worktree axis', () => {
    const e = entryXY('??')
    expect(rowStatusChar(e, 'worktree')).toBe('?')
  })

  it('falls back to entry.status when the axis code is blank', () => {
    const e: GitPanelEntry = {
      path: 'legacy.txt',
      status: 'M',
      staged: true,
      diffStat: null,
      indexStatus: '',
      worktreeStatus: '',
    }
    const sides: StageSide[] = ['index', 'worktree']
    for (const side of sides) {
      expect(rowStatusChar(e, side)).toBe('M')
    }
  })
})
