import { describe, it, expect } from 'vitest'
import { collectDirsToReload } from '@/components/layout/FileTreePanel'

describe('collectDirsToReload', () => {
  it('returns only the root when no directories are expanded', () => {
    expect(collectDirsToReload('/ws', {})).toEqual(['/ws'])
  })

  it('returns the root plus every expanded directory', () => {
    const expanded: Record<string, true> = {
      '/ws/src': true,
      '/ws/src/components': true,
      '/ws/docs': true,
    }
    expect(collectDirsToReload('/ws', expanded)).toEqual([
      '/ws',
      '/ws/src',
      '/ws/src/components',
      '/ws/docs',
    ])
  })

  it('deduplicates when the root is also present in expandedDirs', () => {
    // The root is never added to expandedDirs in practice (it is rendered
    // directly by FileTreePanel, not as a TreeNode), but the helper must be
    // robust against it to avoid a double listDirectory RPC for the root.
    const expanded: Record<string, true> = { '/ws': true, '/ws/src': true }
    const result = collectDirsToReload('/ws', expanded)
    expect(result).toEqual(['/ws', '/ws/src'])
    expect(result.filter((d) => d === '/ws')).toHaveLength(1)
  })
})
