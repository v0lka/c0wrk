// specsPaths — the pure helpers: absolute-path resolution for the file
// viewer, the workspace:tree_changed relevance filter (ADR-105 D3), and the
// tree/timeline ordering.
import { describe, expect, it } from 'vitest'
import type { SpecADRMeta, SpecFile } from '@/types/models'
import {
  findingKindLabel,
  groupFilesBySection,
  isSpecsRelevantChange,
  joinSpecPath,
  sortADRsNewestFirst,
} from './specsPaths'

describe('joinSpecPath', () => {
  it('joins the absolute root with a plain relative path', () => {
    expect(joinSpecPath('/ws/p1/specs', 'domains/testing.md')).toBe(
      '/ws/p1/specs/domains/testing.md',
    )
  })

  it('strips ./ prefixes, #anchors, and duplicate slashes', () => {
    expect(joinSpecPath('/ws/specs/', './domains/testing.md#status')).toBe(
      '/ws/specs/domains/testing.md',
    )
    expect(joinSpecPath('/ws/specs', 'INDEX.md#navigation')).toBe('/ws/specs/INDEX.md')
  })

  it('resolves ../ escapes lexically (links may leave the specs tree)', () => {
    expect(joinSpecPath('/ws/p1/specs', '../CODING_STANDARDS.md')).toBe(
      '/ws/p1/CODING_STANDARDS.md',
    )
  })

  it('normalizes OS separators (watcher paths may carry backslashes)', () => {
    expect(joinSpecPath('/ws\\p1\\specs', 'domains\\testing.md')).toBe(
      '/ws/p1/specs/domains/testing.md',
    )
  })

  it('preserves a UNC root anchor (//server/share — ADR-095 dialect)', () => {
    expect(joinSpecPath('//server/share/ws/specs', 'INDEX.md')).toBe(
      '//server/share/ws/specs/INDEX.md',
    )
    expect(joinSpecPath('\\\\server\\share\\ws\\specs', 'domains\\testing.md')).toBe(
      '//server/share/ws/specs/domains/testing.md',
    )
  })

  it('preserves a Windows drive root in canonical form (C:/…)', () => {
    expect(joinSpecPath('C:\\ws\\specs', 'domains/testing.md')).toBe(
      'C:/ws/specs/domains/testing.md',
    )
  })

  it('clamps ../ escapes at the root anchor, never above it', () => {
    expect(joinSpecPath('//server/share/ws/specs', '../../../../../etc/passwd')).toBe(
      '//server/share/etc/passwd',
    )
    expect(joinSpecPath('/ws/specs', '../..')).toBe('/')
  })
})

describe('isSpecsRelevantChange (workspace:tree_changed filter)', () => {
  const root = '/ws/p1/specs'

  it('refreshes when the payload carries no paths (both production shapes)', () => {
    expect(isSpecsRelevantChange(undefined, root)).toBe(true)
    expect(isSpecsRelevantChange(null, root)).toBe(true)
    expect(isSpecsRelevantChange({ research_scoped: true }, root)).toBe(true)
  })

  it('scopes to specs/-touching paths when the payload lists them', () => {
    expect(
      isSpecsRelevantChange({ paths: ['/ws/p1/specs/INDEX.md'] }, root),
    ).toBe(true)
    expect(
      isSpecsRelevantChange({ paths: ['/ws/p1/src/main.go', '/ws/p1/specs/a.md'] }, root),
    ).toBe(true)
    expect(isSpecsRelevantChange({ paths: ['/ws/p1/src/main.go'] }, root)).toBe(false)
  })

  it('accepts the comma-separated paths shape (the sibling events contract)', () => {
    expect(isSpecsRelevantChange({ paths: '/other/x.md, /ws/p1/specs/a.md' }, root)).toBe(
      true,
    )
    expect(isSpecsRelevantChange({ paths: '/other/x.md,/other/y.md' }, root)).toBe(false)
  })

  it('falls back to a specs path segment before the root is known', () => {
    expect(isSpecsRelevantChange({ paths: ['/ws/p1/specs/INDEX.md'] }, '')).toBe(true)
    expect(isSpecsRelevantChange({ paths: ['/ws/p1/specs'] }, '')).toBe(true)
    expect(isSpecsRelevantChange({ paths: ['/ws/p1/specz/a.md'] }, '')).toBe(false)
  })

  it('treats a malformed paths value as absent (fail-open)', () => {
    expect(isSpecsRelevantChange({ paths: [42, '/nope.md'] }, root)).toBe(true)
    expect(isSpecsRelevantChange({ paths: {} }, root)).toBe(true)
  })

  it('matches with OS separators and the bare root itself', () => {
    expect(isSpecsRelevantChange({ paths: ['C:\\ws\\specs\\a.md'] }, 'C:\\ws\\specs')).toBe(
      true,
    )
    expect(isSpecsRelevantChange({ paths: ['/ws/p1/specs'] }, root)).toBe(true)
  })
})

describe('groupFilesBySection', () => {
  const files: SpecFile[] = [
    { path: 'INDEX.md' },
    { path: 'architecture/layers.md', section: 'architecture' },
    { path: 'architecture/overview.md', section: 'architecture' },
    { path: 'domains/testing.md', section: 'domains' },
  ]

  it('groups by section in first-appearance order, preserving file order', () => {
    const groups = groupFilesBySection(files)
    expect(groups.map((g) => g.section)).toEqual(['', 'architecture', 'domains'])
    expect(groups[1]!.files.map((f) => f.path)).toEqual([
      'architecture/layers.md',
      'architecture/overview.md',
    ])
  })

  it('handles an empty list', () => {
    expect(groupFilesBySection([])).toEqual([])
  })
})

describe('sortADRsNewestFirst', () => {
  const adr = (number: string, slug = number): SpecADRMeta => ({
    number,
    slug,
    file: `decisions/${number}-${slug}.md`,
    status: 'accepted',
  })

  it('sorts numerically descending', () => {
    const sorted = sortADRsNewestFirst([adr('9'), adr('105'), adr('38')])
    expect(sorted.map((a) => a.number)).toEqual(['105', '38', '9'])
  })

  it('sorts non-numeric numbers last, keeping the rest stable', () => {
    const sorted = sortADRsNewestFirst([adr('draft'), adr('7'), adr('x')])
    expect(sorted.map((a) => a.number)).toEqual(['7', 'draft', 'x'])
  })

  it('does not mutate the input', () => {
    const input = [adr('2'), adr('1')]
    sortADRsNewestFirst(input)
    expect(input.map((a) => a.number)).toEqual(['2', '1'])
  })
})

describe('findingKindLabel', () => {
  it('labels the four tier-1 categories (ADR-105 D5)', () => {
    expect(findingKindLabel('PATH_MISSING')).toBe('Missing path')
    expect(findingKindLabel('CROSS_REF_BROKEN')).toBe('Broken cross-ref')
    expect(findingKindLabel('DUPLICATE_ADR_NUMBER')).toBe('Duplicate ADR number')
    expect(findingKindLabel('INDEX_STALE')).toBe('INDEX stale')
  })

  it('passes a future tier kind through with a generic label', () => {
    expect(findingKindLabel('BEHAVIOR_DRIFT')).toBe('behavior drift')
  })
})
