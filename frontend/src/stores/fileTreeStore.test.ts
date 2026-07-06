import { describe, it, expect, beforeEach } from 'vitest'
import { useFileTreeStore } from '@/stores/fileTreeStore'
import type { FileEntry } from '@/types/models'

function makeEntry(path: string, isDir: boolean): FileEntry {
  return {
    name: path.split('/').pop() ?? path,
    path,
    is_dir: isDir,
    hidden: false,
    gitignored: false,
    icon: '',
    icon_color: '',
  }
}

describe('fileTreeStore — toggleDir cache invalidation', () => {
  beforeEach(() => {
    useFileTreeStore.getState().clearTree()
  })

  it('clears cached children when collapsing a directory', () => {
    const store = useFileTreeStore.getState()
    store.setEntries('/ws/src', [makeEntry('/ws/src/a.ts', false)])
    store.toggleDir('/ws/src') // expand

    expect(useFileTreeStore.getState().tree['/ws/src']).toBeDefined()
    expect(useFileTreeStore.getState().expandedDirs['/ws/src']).toBe(true)

    store.toggleDir('/ws/src') // collapse

    expect(useFileTreeStore.getState().tree['/ws/src']).toBeUndefined()
    expect(useFileTreeStore.getState().expandedDirs['/ws/src']).toBeUndefined()
  })

  it('clears descendant caches and expanded state when collapsing a parent', () => {
    const store = useFileTreeStore.getState()
    // Set up: /ws/src expanded with child /ws/src/sub also expanded
    store.setEntries('/ws/src', [
      makeEntry('/ws/src/sub', true),
      makeEntry('/ws/src/a.ts', false),
    ])
    store.setEntries('/ws/src/sub', [makeEntry('/ws/src/sub/b.ts', false)])
    store.toggleDir('/ws/src') // expand parent
    store.toggleDir('/ws/src/sub') // expand child

    expect(useFileTreeStore.getState().tree['/ws/src']).toBeDefined()
    expect(useFileTreeStore.getState().tree['/ws/src/sub']).toBeDefined()
    expect(useFileTreeStore.getState().expandedDirs['/ws/src/sub']).toBe(true)

    // Collapse the parent — must cascade to all descendants
    store.toggleDir('/ws/src') // collapse parent

    expect(useFileTreeStore.getState().tree['/ws/src']).toBeUndefined()
    expect(useFileTreeStore.getState().tree['/ws/src/sub']).toBeUndefined()
    expect(useFileTreeStore.getState().expandedDirs['/ws/src']).toBeUndefined()
    expect(useFileTreeStore.getState().expandedDirs['/ws/src/sub']).toBeUndefined()
  })

  it('does not clear sibling caches when collapsing an unrelated directory', () => {
    const store = useFileTreeStore.getState()
    store.setEntries('/ws/src', [makeEntry('/ws/src/a.ts', false)])
    store.setEntries('/ws/docs', [makeEntry('/ws/docs/b.ts', false)])
    store.toggleDir('/ws/src')
    store.toggleDir('/ws/docs')

    // Collapse /ws/src — /ws/docs must be unaffected
    store.toggleDir('/ws/src')

    expect(useFileTreeStore.getState().tree['/ws/src']).toBeUndefined()
    expect(useFileTreeStore.getState().tree['/ws/docs']).toBeDefined()
    expect(useFileTreeStore.getState().expandedDirs['/ws/docs']).toBe(true)
  })

  it('does not match directories with similar prefixes (boundary check)', () => {
    const store = useFileTreeStore.getState()
    // /ws/src and /ws/src-other share the prefix "/ws/src" but are siblings
    store.setEntries('/ws/src', [makeEntry('/ws/src/a.ts', false)])
    store.setEntries('/ws/src-other', [makeEntry('/ws/src-other/b.ts', false)])
    store.toggleDir('/ws/src')
    store.toggleDir('/ws/src-other')

    store.toggleDir('/ws/src') // collapse /ws/src

    expect(useFileTreeStore.getState().tree['/ws/src']).toBeUndefined()
    expect(useFileTreeStore.getState().tree['/ws/src-other']).toBeDefined()
    expect(useFileTreeStore.getState().expandedDirs['/ws/src-other']).toBe(true)
  })

  it('clears loadingDirs for the collapsed directory and descendants', () => {
    const store = useFileTreeStore.getState()
    store.setLoading('/ws/src', true)
    store.setLoading('/ws/src/sub', true)
    store.toggleDir('/ws/src') // expand
    store.toggleDir('/ws/src/sub') // expand child

    store.toggleDir('/ws/src') // collapse parent

    expect(useFileTreeStore.getState().loadingDirs['/ws/src']).toBeUndefined()
    expect(useFileTreeStore.getState().loadingDirs['/ws/src/sub']).toBeUndefined()
  })
})
