// Unit tests for gitSortGroup — pure sort/group utilities (D8)

import { describe, it, expect } from 'vitest'
import {
  sortEntries,
  groupEntries,
  compareEntries,
  getExtension,
  parentDir,
  statusLabel,
} from '@/lib/gitSortGroup'
import type { GitPanelEntry } from '@/stores/gitPanelStore'

// --- Test helpers ---

function makeEntry(
  overrides: Partial<GitPanelEntry> & { path: string },
): GitPanelEntry {
  return {
    status: 'M',
    staged: false,
    diffStat: null,
    indexStatus: '',
    worktreeStatus: '',
    ...overrides,
  }
}

// --- Helpers ---

describe('getExtension', () => {
  it('extracts the lowercased extension', () => {
    expect(getExtension('src/a.ts')).toBe('ts')
    expect(getExtension('README.md')).toBe('md')
    expect(getExtension('archive.tar.gz')).toBe('gz')
  })
  it('returns empty string for no extension or dotfiles', () => {
    expect(getExtension('Makefile')).toBe('')
    expect(getExtension('.gitignore')).toBe('')
    expect(getExtension('src/.eslintrc')).toBe('')
  })
  it('is case-insensitive', () => {
    expect(getExtension('a.TS')).toBe('ts')
    expect(getExtension('b.Jsx')).toBe('jsx')
  })
})

describe('parentDir', () => {
  it('returns the parent directory without trailing slash', () => {
    expect(parentDir('src/a.ts')).toBe('src')
    expect(parentDir('src/sub/b.ts')).toBe('src/sub')
  })
  it('returns (root) for root-level files', () => {
    expect(parentDir('README.md')).toBe('(root)')
  })
})

describe('statusLabel', () => {
  it('maps known status codes to human-readable labels', () => {
    expect(statusLabel('M')).toBe('Modified')
    expect(statusLabel('A')).toBe('Added')
    expect(statusLabel('D')).toBe('Deleted')
    expect(statusLabel('R')).toBe('Renamed')
    expect(statusLabel('C')).toBe('Copied')
    expect(statusLabel('U')).toBe('Unmerged')
    expect(statusLabel('?')).toBe('Untracked')
  })
  it('falls back to the raw status for unknown codes', () => {
    expect(statusLabel('X')).toBe('X')
    expect(statusLabel('')).toBe('Unknown')
  })
})

// --- Sorting ---

describe('sortEntries', () => {
  it('sorts by path alphabetically', () => {
    const entries = [
      makeEntry({ path: 'src/z.ts' }),
      makeEntry({ path: 'src/a.ts' }),
      makeEntry({ path: 'README.md' }),
    ]
    expect(sortEntries(entries, 'path').map((e) => e.path)).toEqual([
      'README.md',
      'src/a.ts',
      'src/z.ts',
    ])
  })

  it('sorts by status, then path as tiebreak', () => {
    const entries = [
      makeEntry({ path: 'b.ts', status: 'M' }),
      makeEntry({ path: 'a.ts', status: 'A' }),
      makeEntry({ path: 'c.ts', status: 'A' }),
    ]
    expect(
      sortEntries(entries, 'status').map((e) => `${e.status}:${e.path}`),
    ).toEqual(['A:a.ts', 'A:c.ts', 'M:b.ts'])
  })

  it('sorts by extension, then path as tiebreak', () => {
    const entries = [
      makeEntry({ path: 'a.ts' }),
      makeEntry({ path: 'b.md' }),
      makeEntry({ path: 'c.ts' }),
    ]
    // .md before .ts; within .ts: a.ts before c.ts
    expect(sortEntries(entries, 'extension').map((e) => e.path)).toEqual([
      'b.md',
      'a.ts',
      'c.ts',
    ])
  })

  it('places files without an extension first in extension sort', () => {
    const entries = [
      makeEntry({ path: 'z.ts' }),
      makeEntry({ path: 'Makefile' }),
      makeEntry({ path: 'a.ts' }),
    ]
    expect(sortEntries(entries, 'extension').map((e) => e.path)).toEqual([
      'Makefile',
      'a.ts',
      'z.ts',
    ])
  })

  it('extension comparison is case-insensitive', () => {
    const entries = [makeEntry({ path: 'a.TS' }), makeEntry({ path: 'b.ts' })]
    // both 'ts' → tiebreak by path
    expect(sortEntries(entries, 'extension').map((e) => e.path)).toEqual([
      'a.TS',
      'b.ts',
    ])
  })

  it('does not mutate the input array', () => {
    const entries = [makeEntry({ path: 'b.ts' }), makeEntry({ path: 'a.ts' })]
    const snapshot = entries.map((e) => e.path)
    sortEntries(entries, 'path')
    expect(entries.map((e) => e.path)).toEqual(snapshot)
  })

  it('returns a new array (not the same reference)', () => {
    const entries = [makeEntry({ path: 'a.ts' })]
    expect(sortEntries(entries, 'path')).not.toBe(entries)
  })

  it('returns an empty array for empty input', () => {
    expect(sortEntries([], 'path')).toEqual([])
    expect(sortEntries([], 'status')).toEqual([])
  })

  it('handles a single entry', () => {
    const entries = [makeEntry({ path: 'a.ts', status: 'A' })]
    expect(sortEntries(entries, 'status')).toEqual(entries)
    expect(sortEntries(entries, 'extension')).toEqual(entries)
  })
})

// --- Grouping ---

describe('groupEntries', () => {
  it('none returns a single group with all entries (order preserved)', () => {
    const entries = [makeEntry({ path: 'b.ts' }), makeEntry({ path: 'a.ts' })]
    const groups = groupEntries(entries, 'none')
    expect(groups.size).toBe(1)
    const first = [...groups][0]!
    expect(first[0]).toBe('')
    expect(first[1].map((e) => e.path)).toEqual(['b.ts', 'a.ts'])
  })

  it('none returns a copy — does not share the input array', () => {
    const entries = [makeEntry({ path: 'a.ts' })]
    const groups = groupEntries(entries, 'none')
    const items = [...groups.values()][0]!
    expect(items).not.toBe(entries)
  })

  it('status groups by human-readable label (keys sorted)', () => {
    const entries = [
      makeEntry({ path: 'a.ts', status: 'M' }),
      makeEntry({ path: 'b.ts', status: 'A' }),
      makeEntry({ path: 'c.ts', status: 'M' }),
      makeEntry({ path: 'd.ts', status: 'D' }),
    ]
    const groups = groupEntries(entries, 'status')
    expect([...groups.keys()]).toEqual(['Added', 'Deleted', 'Modified'])
    expect(groups.get('Modified')!.map((e) => e.path)).toEqual(['a.ts', 'c.ts'])
    expect(groups.get('Added')!.map((e) => e.path)).toEqual(['b.ts'])
  })

  it('directory groups by parent directory (keys sorted)', () => {
    const entries = [
      makeEntry({ path: 'src/a.ts' }),
      makeEntry({ path: 'lib/b.ts' }),
      makeEntry({ path: 'src/sub/c.ts' }),
      makeEntry({ path: 'README.md' }),
    ]
    const groups = groupEntries(entries, 'directory')
    expect([...groups.keys()]).toEqual(['(root)', 'lib', 'src', 'src/sub'])
    expect(groups.get('src')!.map((e) => e.path)).toEqual(['src/a.ts'])
    expect(groups.get('src/sub')!.map((e) => e.path)).toEqual(['src/sub/c.ts'])
    expect(groups.get('(root)')!.map((e) => e.path)).toEqual(['README.md'])
  })

  it('preserves input order within each group', () => {
    const entries = [
      makeEntry({ path: 'src/z.ts' }),
      makeEntry({ path: 'src/a.ts' }),
    ]
    const groups = groupEntries(entries, 'directory')
    expect(groups.get('src')!.map((e) => e.path)).toEqual(['src/z.ts', 'src/a.ts'])
  })

  it('does not mutate the input array', () => {
    const entries = [makeEntry({ path: 'a.ts' }), makeEntry({ path: 'b.ts' })]
    const snapshot = entries.map((e) => e.path)
    groupEntries(entries, 'status')
    expect(entries.map((e) => e.path)).toEqual(snapshot)
  })

  it('returns an empty single group for empty input (none)', () => {
    const groups = groupEntries([], 'none')
    expect(groups.size).toBe(1)
    expect([...groups.values()][0]).toEqual([])
  })

  it('returns an empty map for empty input (status/directory)', () => {
    expect(groupEntries([], 'status').size).toBe(0)
    expect(groupEntries([], 'directory').size).toBe(0)
  })

  it('handles a single entry for each group mode', () => {
    const entries = [makeEntry({ path: 'src/a.ts', status: 'M' })]
    expect(groupEntries(entries, 'status').get('Modified')).toHaveLength(1)
    expect(groupEntries(entries, 'directory').get('src')).toHaveLength(1)
  })
})

// --- Comparator ---

describe('compareEntries', () => {
  it('status orders by status letter then path', () => {
    const a = makeEntry({ path: 'z.ts', status: 'A' })
    const b = makeEntry({ path: 'a.ts', status: 'M' })
    expect(compareEntries(a, b, 'status')).toBeLessThan(0) // A < M
  })

  it('path orders alphabetically', () => {
    const a = makeEntry({ path: 'a.ts' })
    const b = makeEntry({ path: 'b.ts' })
    expect(compareEntries(a, b, 'path')).toBeLessThan(0)
  })

  it('extension orders by extension then path', () => {
    const a = makeEntry({ path: 'a.md' })
    const b = makeEntry({ path: 'b.ts' })
    expect(compareEntries(a, b, 'extension')).toBeLessThan(0) // md < ts
  })
})
