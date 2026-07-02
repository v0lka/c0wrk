// Unit tests for gitPanelStore — Zustand store actions and state transitions

import { describe, it, expect, beforeEach } from 'vitest'
import { useGitPanelStore, type GitPanelEntry } from '@/stores/gitPanelStore'

/** Reset the store to initial state before each test */
function resetStore() {
  useGitPanelStore.getState().reset()
}

// --- Test helpers ---

function makeEntry(overrides: Partial<GitPanelEntry> & { path: string }): GitPanelEntry {
  return {
    status: 'M',
    staged: false,
    diffStat: null,
    ...overrides,
  }
}

describe('gitPanelStore', () => {
  beforeEach(() => {
    resetStore()
  })

  // ── Initial state ──

  it('has correct initial state', () => {
    const s = useGitPanelStore.getState()
    expect(s.viewMode).toBe('flat')
    expect(s.entries).toEqual([])
    expect(s.commitMessage).toBe('')
    expect(s.branch).toBe('')
    expect(s.expandedDirs).toEqual(new Set())
    expect(s.isLoading).toBe(false)
    expect(s.isGitRepo).toBe(false)
    expect(s.error).toBeNull()
  })

  // ── setViewMode ──

  it('setViewMode changes viewMode', () => {
    const { setViewMode } = useGitPanelStore.getState()
    setViewMode('tree')
    expect(useGitPanelStore.getState().viewMode).toBe('tree')
    setViewMode('flat')
    expect(useGitPanelStore.getState().viewMode).toBe('flat')
  })

  // ── setCommitMessage ──

  it('setCommitMessage updates the message', () => {
    const { setCommitMessage } = useGitPanelStore.getState()
    setCommitMessage('fix: update config')
    expect(useGitPanelStore.getState().commitMessage).toBe('fix: update config')
  })

  it('setCommitMessage allows empty string', () => {
    const { setCommitMessage } = useGitPanelStore.getState()
    setCommitMessage('initial')
    setCommitMessage('')
    expect(useGitPanelStore.getState().commitMessage).toBe('')
  })

  // ── setLoading ──

  it('setLoading toggles isLoading', () => {
    const { setLoading } = useGitPanelStore.getState()
    expect(useGitPanelStore.getState().isLoading).toBe(false)
    setLoading(true)
    expect(useGitPanelStore.getState().isLoading).toBe(true)
    setLoading(false)
    expect(useGitPanelStore.getState().isLoading).toBe(false)
  })

  // ── setError ──

  it('setError sets error message', () => {
    const { setError } = useGitPanelStore.getState()
    setError('Something went wrong')
    expect(useGitPanelStore.getState().error).toBe('Something went wrong')
  })

  it('setError(null) clears error', () => {
    const { setError } = useGitPanelStore.getState()
    setError('Something went wrong')
    setError(null)
    expect(useGitPanelStore.getState().error).toBeNull()
  })

  // ── loadEntries ──

  it('loadEntries sets entries and clears loading/error', () => {
    const { loadEntries, setLoading, setError } = useGitPanelStore.getState()
    setLoading(true)
    setError('previous error')

    const entries: GitPanelEntry[] = [
      makeEntry({ path: 'src/main.ts', status: 'M', staged: false }),
      makeEntry({ path: 'src/utils.ts', status: 'A', staged: true }),
    ]
    loadEntries(entries)

    const s = useGitPanelStore.getState()
    expect(s.entries).toEqual(entries)
    expect(s.isLoading).toBe(false)
    expect(s.error).toBeNull()
  })

  it('loadEntries with empty array works', () => {
    const { loadEntries } = useGitPanelStore.getState()
    loadEntries([])
    expect(useGitPanelStore.getState().entries).toEqual([])
  })

  // ── toggleStage ──

  it('toggleStage toggles staged flag from false to true', () => {
    const { loadEntries, toggleStage } = useGitPanelStore.getState()
    loadEntries([
      makeEntry({ path: 'a.ts', staged: false }),
      makeEntry({ path: 'b.ts', staged: true }),
    ])

    toggleStage('a.ts')
    const entries = useGitPanelStore.getState().entries
    expect(entries.find(e => e.path === 'a.ts')!.staged).toBe(true)
    expect(entries.find(e => e.path === 'b.ts')!.staged).toBe(true) // unchanged
  })

  it('toggleStage toggles staged flag from true to false', () => {
    const { loadEntries, toggleStage } = useGitPanelStore.getState()
    loadEntries([
      makeEntry({ path: 'a.ts', staged: true }),
    ])

    toggleStage('a.ts')
    expect(useGitPanelStore.getState().entries[0]!.staged).toBe(false)
  })

  it('toggleStage does nothing for nonexistent path', () => {
    const { loadEntries, toggleStage } = useGitPanelStore.getState()
    loadEntries([makeEntry({ path: 'a.ts', staged: false })])
    toggleStage('nonexistent.ts')
    expect(useGitPanelStore.getState().entries).toHaveLength(1)
    expect(useGitPanelStore.getState().entries[0]!.staged).toBe(false)
  })

  it('toggleStage preserves other entry properties', () => {
    const { loadEntries, toggleStage } = useGitPanelStore.getState()
    loadEntries([
      makeEntry({ path: 'a.ts', status: 'M', diffStat: { added: 3, deleted: 1 } }),
    ])

    toggleStage('a.ts')
    const entry = useGitPanelStore.getState().entries[0]!
    expect(entry.staged).toBe(true)
    expect(entry.status).toBe('M')
    expect(entry.diffStat).toEqual({ added: 3, deleted: 1 })
  })

  // ── setBranch ──

  it('setBranch updates branch name', () => {
    const { setBranch } = useGitPanelStore.getState()
    setBranch('feature/login')
    expect(useGitPanelStore.getState().branch).toBe('feature/login')
  })

  it('setBranch allows empty string', () => {
    const { setBranch } = useGitPanelStore.getState()
    setBranch('main')
    setBranch('')
    expect(useGitPanelStore.getState().branch).toBe('')
  })

  // ── setGitRepo ──

  it('setGitRepo toggles isGitRepo', () => {
    const { setGitRepo } = useGitPanelStore.getState()
    setGitRepo(true)
    expect(useGitPanelStore.getState().isGitRepo).toBe(true)
    setGitRepo(false)
    expect(useGitPanelStore.getState().isGitRepo).toBe(false)
  })

  // ── toggleExpandedDir ──

  it('toggleExpandedDir adds a directory', () => {
    const { toggleExpandedDir } = useGitPanelStore.getState()
    toggleExpandedDir('src')
    expect(useGitPanelStore.getState().expandedDirs).toEqual(new Set(['src']))
  })

  it('toggleExpandedDir removes an existing directory', () => {
    const { toggleExpandedDir } = useGitPanelStore.getState()
    toggleExpandedDir('src')
    toggleExpandedDir('src')
    expect(useGitPanelStore.getState().expandedDirs).toEqual(new Set())
  })

  it('toggleExpandedDir handles multiple directories', () => {
    const { toggleExpandedDir } = useGitPanelStore.getState()
    toggleExpandedDir('src')
    toggleExpandedDir('src/components')
    toggleExpandedDir('src/hooks')

    const dirs = useGitPanelStore.getState().expandedDirs
    expect(dirs.has('src')).toBe(true)
    expect(dirs.has('src/components')).toBe(true)
    expect(dirs.has('src/hooks')).toBe(true)

    toggleExpandedDir('src')
    expect(useGitPanelStore.getState().expandedDirs.has('src')).toBe(false)
    expect(useGitPanelStore.getState().expandedDirs.has('src/components')).toBe(true)
  })

  // ── reset ──

  it('reset clears all state to initial values', () => {
    const store = useGitPanelStore.getState()
    store.setViewMode('tree')
    store.loadEntries([makeEntry({ path: 'a.ts' })])
    store.setCommitMessage('fix: bug')
    store.setBranch('feature/x')
    store.setGitRepo(true)
    store.setLoading(true)
    store.setError('some error')
    store.toggleExpandedDir('src')

    store.reset()

    const s = useGitPanelStore.getState()
    expect(s.viewMode).toBe('flat')
    expect(s.entries).toEqual([])
    expect(s.commitMessage).toBe('')
    expect(s.branch).toBe('')
    expect(s.expandedDirs).toEqual(new Set())
    expect(s.isLoading).toBe(false)
    expect(s.isGitRepo).toBe(false)
    expect(s.error).toBeNull()
  })

  // ── Integration: typical flow ──

  it('handles a full staging → commit flow', () => {
    const store = useGitPanelStore.getState()

    // Initial load
    store.setGitRepo(true)
    store.setBranch('main')
    store.loadEntries([
      makeEntry({ path: 'src/app.ts', status: 'M', staged: false }),
      makeEntry({ path: 'README.md', status: 'M', staged: false }),
    ])

    // Stage one file
    store.toggleStage('src/app.ts')
    expect(useGitPanelStore.getState().entries.find(e => e.path === 'src/app.ts')!.staged).toBe(true)

    // Set commit message
    store.setCommitMessage('feat: add app module')

    // Simulate loading during commit
    store.setLoading(true)
    expect(useGitPanelStore.getState().isLoading).toBe(true)

    // Commit succeeds — reload
    store.loadEntries([])
    store.setCommitMessage('')
    expect(useGitPanelStore.getState().entries).toEqual([])
    expect(useGitPanelStore.getState().commitMessage).toBe('')
  })

  it('handles error during refresh', () => {
    const store = useGitPanelStore.getState()
    store.setLoading(true)
    store.setError('Failed to load git status')
    store.setGitRepo(false)
    store.loadEntries([])

    const s = useGitPanelStore.getState()
    expect(s.isLoading).toBe(false) // loadEntries clears loading
    expect(s.error).toBeNull() // loadEntries clears error
    expect(s.isGitRepo).toBe(false)
    expect(s.entries).toEqual([])
  })
})
