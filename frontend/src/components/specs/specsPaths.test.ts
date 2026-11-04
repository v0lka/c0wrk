// specsPaths — the pure helpers: absolute-path resolution for the file
// viewer, the workspace:tree_changed relevance filter (ADR-105 D3), and the
// nested tree + DECISIONS date lookup.
import { describe, expect, it } from 'vitest'
import type { SpecADRMeta, SpecFile } from '@/types/models'
import {
  adrDatesByFile,
  buildSpecsTree,
  findingKindLabel,
  isSpecsRelevantChange,
  joinSpecPath,
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

describe('buildSpecsTree (nested tree builder)', () => {
  const files: SpecFile[] = [
    { path: 'INDEX.md', title: 'Specs Index' },
    { path: 'META.md', title: 'META' },
    { path: 'architecture/layers.md', section: 'architecture', title: 'Layers' },
    { path: 'architecture/overview.md', section: 'architecture' },
    { path: 'domains/frontend/ui-scale.md', section: 'domains', title: 'UI Scale' },
    { path: 'domains/testing.md', section: 'domains', title: 'Testing' },
    { path: 'contracts/events.md', section: 'contracts' },
  ]

  it('builds the root files group (INDEX/META) first, then sections in appearance order', () => {
    const tree = buildSpecsTree(files)
    expect(tree.map((n) => n.id)).toEqual(['', 'architecture', 'domains', 'contracts'])
    const rootGroup = tree[0]!
    expect(rootGroup.label).toBe('root')
    expect(rootGroup.files.map((f) => f.path)).toEqual(['INDEX.md', 'META.md'])
    expect(rootGroup.children).toEqual([])
  })

  it('nests subdirectory nodes from the path segments', () => {
    const tree = buildSpecsTree(files)
    const domains = tree.find((n) => n.id === 'domains')!
    expect(domains.children.map((c) => c.id)).toEqual(['domains/frontend'])
    const frontend = domains.children[0]!
    expect(frontend.label).toBe('frontend')
    expect(frontend.files.map((f) => f.path)).toEqual(['domains/frontend/ui-scale.md'])
    // The direct file stays on the section node itself.
    expect(domains.files.map((f) => f.path)).toEqual(['domains/testing.md'])
  })

  it('counts files over the whole subtree', () => {
    const tree = buildSpecsTree(files)
    const byId = new Map(tree.map((n) => [n.id, n]))
    expect(byId.get('')!.count).toBe(2)
    expect(byId.get('architecture')!.count).toBe(2)
    expect(byId.get('contracts')!.count).toBe(1)
    expect(byId.get('domains')!.count).toBe(2)
    expect(byId.get('domains')!.children[0]!.count).toBe(1)
  })

  it('excludes "_"-prefixed files (the template prefix the parse skips)', () => {
    const tree = buildSpecsTree([
      ...files,
      { path: 'domains/_template.md', section: 'domains' },
      { path: 'decisions/_draft.md', section: 'decisions' },
    ])
    const paths: string[] = []
    const walk = (node: { files: SpecFile[]; children: unknown[] }): void => {
      for (const f of node.files) paths.push(f.path)
      for (const c of node.children ?? []) walk(c as typeof node)
    }
    for (const top of tree) walk(top)
    expect(paths).not.toContain('domains/_template.md')
    expect(paths).not.toContain('decisions/_draft.md')
    // The template-only section does not become an empty node either.
    expect(tree.map((n) => n.id)).not.toContain('decisions')
  })

  it('sorts child directories by label', () => {
    const tree = buildSpecsTree([
      { path: 'domains/zeta/a.md' },
      { path: 'domains/alpha/b.md' },
      { path: 'domains/mid/c.md' },
    ])
    expect(tree[0]!.children.map((c) => c.label)).toEqual(['alpha', 'mid', 'zeta'])
  })

  it('handles an empty list and a root-only list', () => {
    expect(buildSpecsTree([])).toEqual([])
    const rootOnly = buildSpecsTree([{ path: 'INDEX.md' }])
    expect(rootOnly).toHaveLength(1)
    expect(rootOnly[0]!.id).toBe('')
    expect(rootOnly[0]!.files).toHaveLength(1)
  })

  it('does not mutate the input file list', () => {
    const input = [...files]
    buildSpecsTree(input)
    expect(input).toEqual(files)
  })
})

describe('findingKindLabel', () => {
  it('labels the tier-1 categories (ADR-105 D5 as completed by ADR-108)', () => {
    expect(findingKindLabel('PATH_MISSING')).toBe('Missing path')
    expect(findingKindLabel('CROSS_REF_BROKEN')).toBe('Broken cross-ref')
    expect(findingKindLabel('DUPLICATE_ADR_NUMBER')).toBe('Duplicate ADR number')
    expect(findingKindLabel('INDEX_STALE')).toBe('INDEX stale')
    expect(findingKindLabel('ROOT_FILE_MISSING')).toBe('Missing root file')
    expect(findingKindLabel('ADR_NAME_MALFORMED')).toBe('Malformed ADR name')
    expect(findingKindLabel('ADR_NUMBER_MISMATCH')).toBe('ADR number mismatch')
    expect(findingKindLabel('SUPERSEDED_TARGET_MISSING')).toBe('Missing successor')
    expect(findingKindLabel('SUPERSEDED_CYCLE')).toBe('Supersession cycle')
  })

  it('passes a future tier kind through with a generic label', () => {
    expect(findingKindLabel('BEHAVIOR_DRIFT')).toBe('behavior drift')
  })
})

describe('adrDatesByFile (DECISIONS date-column lookup)', () => {
  const adr = (number: string, date?: string): SpecADRMeta => ({
    number,
    slug: number,
    file: `decisions/${number}-x.md`,
    status: 'accepted',
    ...(date !== undefined ? { date } : {}),
  })

  it('keys each dated record by its specs-root-relative file path', () => {
    expect(adrDatesByFile([adr('104', '2025-11-01'), adr('107', '2026-01-15')])).toEqual({
      'decisions/104-x.md': '2025-11-01',
      'decisions/107-x.md': '2026-01-15',
    })
  })

  it('omits undated records (non-git workspace, uncommitted file)', () => {
    expect(adrDatesByFile([adr('104'), adr('105', ''), adr('106', '2026-02-02')])).toEqual({
      'decisions/106-x.md': '2026-02-02',
    })
  })

  it('returns an empty map for an empty list', () => {
    expect(adrDatesByFile([])).toEqual({})
  })
})

