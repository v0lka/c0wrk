// specsPaths — the pure helpers: workspace-relative model paths resolved for
// the file viewer, the workspace:tree_changed relevance filter, the date
// lookup the tree reads, and the payload-driven finding label.
import { describe, expect, it } from 'vitest'
import type { SDDDocument, SDDFinding } from '@/types/models'
import {
  documentDatesByPath,
  findingLabel,
  humanizeToken,
  isSpecsRelevantChange,
  joinWorkspacePath,
} from './specsPaths'

describe('joinWorkspacePath', () => {
  it('joins the absolute workspace with a workspace-relative model path', () => {
    expect(joinWorkspacePath('/ws/p1', 'specs/domains/testing.md')).toBe(
      '/ws/p1/specs/domains/testing.md',
    )
  })

  it('strips leading slashes, ./ prefixes and #anchors', () => {
    expect(joinWorkspacePath('/ws/', './specs/domains/testing.md#status')).toBe(
      '/ws/specs/domains/testing.md',
    )
    expect(joinWorkspacePath('/ws', '/specs/INDEX.md#navigation')).toBe('/ws/specs/INDEX.md')
  })

  it('resolves ../ escapes lexically (a declared root may sit outside)', () => {
    expect(joinWorkspacePath('/ws/p1/sub', '../CODING_STANDARDS.md')).toBe(
      '/ws/p1/CODING_STANDARDS.md',
    )
  })

  it('normalizes OS separators (watcher paths may carry backslashes)', () => {
    expect(joinWorkspacePath('/ws\\p1', 'specs\\domains\\testing.md')).toBe(
      '/ws/p1/specs/domains/testing.md',
    )
  })

  it('preserves a UNC root anchor (//server/share — ADR-095 dialect)', () => {
    expect(joinWorkspacePath('//server/share/ws', 'specs/INDEX.md')).toBe(
      '//server/share/ws/specs/INDEX.md',
    )
  })

  it('preserves a Windows drive root in canonical form (C:/…)', () => {
    expect(joinWorkspacePath('C:\\ws', 'specs/domains/testing.md')).toBe(
      'C:/ws/specs/domains/testing.md',
    )
  })

  it('clamps ../ escapes at the root anchor, never above it', () => {
    expect(joinWorkspacePath('//server/share/ws', '../../../../../etc/passwd')).toBe(
      '//server/share/etc/passwd',
    )
  })

  it('returns the relative path when the workspace is unknown', () => {
    expect(joinWorkspacePath('', 'specs/INDEX.md')).toBe('specs/INDEX.md')
  })
})

describe('isSpecsRelevantChange (workspace:tree_changed filter)', () => {
  const root = '/ws/p1/specs'

  it('refreshes when the payload carries no paths (both production shapes)', () => {
    expect(isSpecsRelevantChange(undefined, root)).toBe(true)
    expect(isSpecsRelevantChange(null, root)).toBe(true)
    expect(isSpecsRelevantChange({ research_scoped: true }, root)).toBe(true)
  })

  it('scopes to the active root when the payload lists paths', () => {
    expect(isSpecsRelevantChange({ paths: ['/ws/p1/specs/INDEX.md'] }, root)).toBe(true)
    expect(
      isSpecsRelevantChange({ paths: ['/ws/p1/src/main.go', '/ws/p1/specs/a.md'] }, root),
    ).toBe(true)
    expect(isSpecsRelevantChange({ paths: ['/ws/p1/src/main.go'] }, root)).toBe(false)
  })

  it('accepts the comma-separated paths shape (the sibling events contract)', () => {
    expect(isSpecsRelevantChange({ paths: '/other/x.md, /ws/p1/specs/a.md' }, root)).toBe(true)
    expect(isSpecsRelevantChange({ paths: '/other/x.md,/other/y.md' }, root)).toBe(false)
  })

  it('fails open before the active root is known (detection may come from anywhere)', () => {
    expect(isSpecsRelevantChange({ paths: ['/ws/p1/specs/INDEX.md'] }, '')).toBe(true)
    expect(isSpecsRelevantChange({ paths: ['/ws/p1/.claude/skills/x/SKILL.md'] }, '')).toBe(true)
  })

  it('treats a malformed paths value as absent (fail-open)', () => {
    expect(isSpecsRelevantChange({ paths: [42, '/nope.md'] }, root)).toBe(true)
    expect(isSpecsRelevantChange({ paths: {} }, root)).toBe(true)
  })

  it('matches with OS separators and the bare root itself', () => {
    expect(isSpecsRelevantChange({ paths: ['C:\\ws\\specs\\a.md'] }, 'C:\\ws\\specs')).toBe(true)
    expect(isSpecsRelevantChange({ paths: ['/ws/p1/specs'] }, root)).toBe(true)
  })
})

describe('documentDatesByPath', () => {
  function doc(path: string, date?: string): SDDDocument {
    return {
      path,
      root: 'specs',
      concept: 'decision',
      name: path.split('/').pop() ?? path,
      slug: 'x',
      date,
      headings: [],
      key_files: [],
      cross_refs: [],
      requirements: [],
      scenarios: [],
      deltas: [],
      principles: [],
      user_stories: [],
      task_items: [],
      tasks: [],
      missing: [],
    }
  }

  it('keys the git add-date by the workspace-relative path', () => {
    expect(
      documentDatesByPath([
        doc('specs/decisions/001-a.md', '2026-01-02'),
        doc('specs/decisions/002-b.md'),
        doc('specs/INDEX.md', ''),
      ]),
    ).toEqual({ 'specs/decisions/001-a.md': '2026-01-02' })
  })
})

describe('finding labels (framework vocabulary, never a frontend map)', () => {
  function finding(kind: string, message?: string): SDDFinding {
    return { kind, file: 'specs/x.md', detail: 'detail', message, severity: 'error' }
  }

  it('prefers the check’s declared message', () => {
    expect(findingLabel(finding('WHATEVER_KIND', 'every change needs a proposal'))).toBe(
      'every change needs a proposal',
    )
  })

  it('falls back to the readable token when the framework declares no message', () => {
    expect(findingLabel(finding('CHANGE_PROPOSAL_MISSING'))).toBe('change proposal missing')
    expect(findingLabel(finding('WHATEVER_KIND', ''))).toBe('whatever kind')
  })

  it('humanizes both separators', () => {
    expect(humanizeToken('A-B_C')).toBe('a b c')
  })
})
