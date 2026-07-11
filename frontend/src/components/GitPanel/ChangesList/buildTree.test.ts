import { describe, it, expect } from 'vitest'
import { collectAllDirPaths } from './buildTree'
import type { GitPanelEntry } from '@/stores/gitPanelStore'

// --- Test helpers ---

function makeEntry(path: string): GitPanelEntry {
  return {
    path,
    status: 'M',
    staged: false,
    diffStat: null,
    indexStatus: '',
    worktreeStatus: '',
  }
}

describe('collectAllDirPaths', () => {
  it('returns empty array for no entries', () => {
    expect(collectAllDirPaths([], '/repo')).toEqual([])
  })

  it('returns empty array for root-level files only', () => {
    const entries = [makeEntry('/repo/a.ts'), makeEntry('/repo/b.ts')]
    expect(collectAllDirPaths(entries, '/repo')).toEqual([])
  })

  it('collects all intermediate directory paths', () => {
    const entries = [makeEntry('/repo/src/components/Button.tsx')]
    expect(collectAllDirPaths(entries, '/repo')).toEqual([
      'src',
      'src/components',
    ])
  })

  it('deduplicates shared directory paths', () => {
    const entries = [
      makeEntry('/repo/src/a.ts'),
      makeEntry('/repo/src/b.ts'),
      makeEntry('/repo/src/components/X.tsx'),
      makeEntry('/repo/src/components/Y.tsx'),
    ]
    expect(collectAllDirPaths(entries, '/repo')).toEqual([
      'src',
      'src/components',
    ])
  })

  it('uses display-relative paths when entries are under workspace root', () => {
    const entries = [makeEntry('/repo/lib/utils/helpers.ts')]
    expect(collectAllDirPaths(entries, '/repo')).toEqual([
      'lib',
      'lib/utils',
    ])
  })

  it('uses raw paths when entries are outside workspace root', () => {
    // Absolute paths outside the workspace root are used as-is; the leading
    // slash produces an empty first segment (same behaviour as buildTree).
    const entries = [makeEntry('/other/pkg/file.ts')]
    expect(collectAllDirPaths(entries, '/repo')).toEqual([
      '',
      'other',
      'other/pkg',
    ])
  })

  it('handles mixed root-level and nested files', () => {
    const entries = [
      makeEntry('/repo/README.md'),
      makeEntry('/repo/src/index.ts'),
      makeEntry('/repo/src/deep/nested/file.ts'),
    ]
    expect(collectAllDirPaths(entries, '/repo')).toEqual([
      'src',
      'src/deep',
      'src/deep/nested',
    ])
  })

  it('does not match sibling directories sharing a prefix', () => {
    // "/repo-extra" shares the prefix "/repo" but is a different directory.
    // The raw absolute path is used, so the leading slash produces an empty
    // first segment (same behaviour as buildTree).
    const entries = [makeEntry('/repo-extra/src/file.ts')]
    expect(collectAllDirPaths(entries, '/repo')).toEqual([
      '',
      'repo-extra',
      'repo-extra/src',
    ])
  })

  it('handles empty workspace root', () => {
    const entries = [makeEntry('src/components/Button.tsx')]
    expect(collectAllDirPaths(entries, '')).toEqual([
      'src',
      'src/components',
    ])
  })
})
