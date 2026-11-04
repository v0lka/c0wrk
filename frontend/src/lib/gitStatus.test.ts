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
  stripRootPrefix,
  entryChangeKind,
  countChangeKinds,
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

// --- entryChangeKind / countChangeKinds ---

describe('entryChangeKind', () => {
  it('classifies untracked and added paths as new', () => {
    // 'UA' (added by them) and 'AU' (added by us) are new-to-HEAD paths too.
    for (const combo of ['??', 'A ', 'AM', 'AD', 'AA', 'AU', 'UA']) {
      expect(entryChangeKind(entryXY(combo))).toBe('new')
    }
  })

  it('classifies deleted paths as deleted, unless the path is new to the repo', () => {
    for (const combo of ['D ', ' D', 'MD', 'DD', 'DU', 'UD']) {
      expect(entryChangeKind(entryXY(combo))).toBe('deleted')
    }
    // 'AD' is a staged-new file deleted from the worktree: no HEAD
    // counterpart exists, so it stays new (A outranks D).
    expect(entryChangeKind(entryXY('AD'))).toBe('new')
  })

  it('classifies edits, renames and remaining conflicts as modified', () => {
    for (const combo of ['M ', ' M', 'MM', 'RM', 'CM', 'UU']) {
      expect(entryChangeKind(entryXY(combo))).toBe('modified')
    }
  })
})

describe('countChangeKinds', () => {
  it('counts each unique PATH once — an MM entry contributes 1, not 2', () => {
    const counts = countChangeKinds([
      entryXY('MM', 'mm.txt'),
      entryXY(' M', 'mod.txt'),
      entryXY('??', 'new.txt'),
      entryXY('A ', 'added.txt'),
      entryXY('D ', 'del.txt'),
      entryXY(' D', 'del2.txt'),
    ])
    expect(counts).toEqual({ modified: 2, deleted: 2, new: 2, total: 6 })
  })

  it('returns all-zero counts for an empty input', () => {
    expect(countChangeKinds([])).toEqual({ modified: 0, deleted: 0, new: 0, total: 0 })
  })
})

// --- stripRootPrefix ---

describe('stripRootPrefix', () => {
  const root = '/repo'

  it('strips the root at a segment boundary', () => {
    expect(stripRootPrefix('/repo/src/main.ts', root)).toBe('src/main.ts')
    expect(stripRootPrefix('/repo/file.txt', root)).toBe('file.txt')
  })

  it('strips a worktree root nested inside the project checkout', () => {
    // The backend keys status onto the focus worktree root; stripping against
    // that root yields the repo-relative display path.
    expect(stripRootPrefix('/repo/.worktrees/s-abc/src/main.ts', '/repo/.worktrees/s-abc')).toBe(
      'src/main.ts',
    )
  })

  it('does not strip a sibling directory sharing the same prefix', () => {
    expect(stripRootPrefix('/repo-extra/src/main.ts', root)).toBe('/repo-extra/src/main.ts')
  })

  it('returns the path unchanged when it lies outside the root', () => {
    expect(stripRootPrefix('/other/pkg/file.ts', root)).toBe('/other/pkg/file.ts')
  })

  it('returns the empty string when the path equals the root', () => {
    expect(stripRootPrefix(root, root)).toBe('')
  })

  it('returns the path unchanged for an empty root', () => {
    expect(stripRootPrefix('/repo/src/main.ts', '')).toBe('/repo/src/main.ts')
    expect(stripRootPrefix('relative/file.ts', '')).toBe('relative/file.ts')
  })

  it('treats only a full-segment match, never a partial segment', () => {
    // "/rep" shares a character prefix with "/repo" but is not a segment match.
    expect(stripRootPrefix('/rep/file.ts', root)).toBe('/rep/file.ts')
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
