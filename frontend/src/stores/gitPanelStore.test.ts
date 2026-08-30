// Unit tests for gitPanelStore — Zustand store actions and state transitions
//
// jsdom environment: the store uses zustand's persist middleware without an
// explicit `storage` option, so zustand resolves its default
// `createJSONStorage(() => window.localStorage)` at import time. In the plain
// node environment `window` is undefined, the getter throws, the middleware
// degrades to "storage unavailable" and warns on every `set` (matching
// panelPersistence.test.ts, which opts into jsdom for the same reason).
// @vitest-environment jsdom
import { describe, it, expect, beforeEach, vi } from 'vitest'
import { useGitPanelStore, EMPTY_MERGE_REBASE_STATE, EMPTY_COMMIT_DRAFT, COMMIT_BANNER_DISMISS_MS, partializeGitPanel, mergeGitPanel, type GitPanelEntry } from '@/stores/gitPanelStore'

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
    indexStatus: '',
    worktreeStatus: '',
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
    expect(s.sortBy).toBe('path')
    expect(s.groupBy).toBe('none')
    expect(s.entries).toEqual([])
    expect(s.commitByProject).toEqual({})
    expect(s.branch).toEqual({ name: '', upstream: '', ahead: 0, behind: 0 })
    expect(s.branches).toEqual([])
    expect(s.expandedDirs).toEqual(new Set())
    expect(s.isLoading).toBe(false)
    expect(s.isGitRepo).toBe(false)
    expect(s.isBranchPickerOpen).toBe(false)
    expect(s.remoteOperationInProgress).toBe(false)
    expect(s.activeTab).toBe('changes')
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

  // ── setCommitMessage (per-project) ──

  it('setCommitMessage updates the message of the given project', () => {
    const { setCommitMessage } = useGitPanelStore.getState()
    setCommitMessage('proj-1', 'fix: update config')
    expect(useGitPanelStore.getState().commitByProject['proj-1']).toEqual({
      message: 'fix: update config',
      isGenerating: false,
      isCommitting: false,
      error: null,
      lastCommitSha: null,
    })
  })

  it('setCommitMessage allows empty string', () => {
    const { setCommitMessage } = useGitPanelStore.getState()
    setCommitMessage('proj-1', 'initial')
    setCommitMessage('proj-1', '')
    expect(useGitPanelStore.getState().commitByProject['proj-1']!.message).toBe('')
  })

  it('setCommitMessage is scoped per project', () => {
    const { setCommitMessage } = useGitPanelStore.getState()
    setCommitMessage('proj-a', 'feat: a')
    setCommitMessage('proj-b', 'fix: b')
    const s = useGitPanelStore.getState().commitByProject
    expect(s['proj-a']!.message).toBe('feat: a')
    expect(s['proj-b']!.message).toBe('fix: b')
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

  it('setBranch updates branch info', () => {
    const { setBranch } = useGitPanelStore.getState()
    setBranch({ name: 'feature/login', upstream: 'origin/feature/login', ahead: 2, behind: 0 })
    expect(useGitPanelStore.getState().branch).toEqual({
      name: 'feature/login',
      upstream: 'origin/feature/login',
      ahead: 2,
      behind: 0,
    })
  })

  it('setBranch allows empty branch info', () => {
    const { setBranch } = useGitPanelStore.getState()
    setBranch({ name: 'main', upstream: '', ahead: 0, behind: 0 })
    setBranch({ name: '', upstream: '', ahead: 0, behind: 0 })
    expect(useGitPanelStore.getState().branch).toEqual({
      name: '',
      upstream: '',
      ahead: 0,
      behind: 0,
    })
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

  // ── setExpandedDirs (expand-all / collapse-all) ──

  it('setExpandedDirs replaces the entire set', () => {
    const { toggleExpandedDir, setExpandedDirs } = useGitPanelStore.getState()
    toggleExpandedDir('old-dir')

    setExpandedDirs(new Set(['src', 'src/components', 'lib']))
    expect(useGitPanelStore.getState().expandedDirs).toEqual(
      new Set(['src', 'src/components', 'lib']),
    )
  })

  it('setExpandedDirs with empty set collapses all directories', () => {
    const { toggleExpandedDir, setExpandedDirs } = useGitPanelStore.getState()
    toggleExpandedDir('src')
    toggleExpandedDir('lib')

    setExpandedDirs(new Set())
    expect(useGitPanelStore.getState().expandedDirs).toEqual(new Set())
  })

  it('setExpandedDirs does not merge with previous state', () => {
    const { toggleExpandedDir, setExpandedDirs } = useGitPanelStore.getState()
    toggleExpandedDir('previous')

    setExpandedDirs(new Set(['new']))
    const dirs = useGitPanelStore.getState().expandedDirs
    expect(dirs).toEqual(new Set(['new']))
    expect(dirs.has('previous')).toBe(false)
  })

  // ── setBranches ──

  it('setBranches updates the branch list', () => {
    const { setBranches } = useGitPanelStore.getState()
    setBranches([
      { name: 'main', is_current: true, kind: 'local', upstream: 'origin/main' },
      { name: 'feature/x', is_current: false, kind: 'local', upstream: '' },
    ])
    expect(useGitPanelStore.getState().branches).toEqual([
      { name: 'main', is_current: true, kind: 'local', upstream: 'origin/main' },
      { name: 'feature/x', is_current: false, kind: 'local', upstream: '' },
    ])
  })

  it('setBranches allows empty array', () => {
    const { setBranches } = useGitPanelStore.getState()
    setBranches([{ name: 'main', is_current: true, kind: 'local', upstream: 'origin/main' }])
    setBranches([])
    expect(useGitPanelStore.getState().branches).toEqual([])
  })

  // ── openBranchPicker / closeBranchPicker ──

  it('openBranchPicker sets isBranchPickerOpen to true', () => {
    const { openBranchPicker } = useGitPanelStore.getState()
    expect(useGitPanelStore.getState().isBranchPickerOpen).toBe(false)
    openBranchPicker()
    expect(useGitPanelStore.getState().isBranchPickerOpen).toBe(true)
  })

  it('closeBranchPicker sets isBranchPickerOpen to false', () => {
    const { openBranchPicker, closeBranchPicker } = useGitPanelStore.getState()
    openBranchPicker()
    expect(useGitPanelStore.getState().isBranchPickerOpen).toBe(true)
    closeBranchPicker()
    expect(useGitPanelStore.getState().isBranchPickerOpen).toBe(false)
  })

  // ── setGeneratingCommit (per-project) ──

  it('setGeneratingCommit toggles the flag of the given project', () => {
    const { setGeneratingCommit } = useGitPanelStore.getState()
    expect(useGitPanelStore.getState().commitByProject['proj-1']).toBeUndefined()
    setGeneratingCommit('proj-1', true)
    expect(useGitPanelStore.getState().commitByProject['proj-1']!.isGenerating).toBe(true)
    setGeneratingCommit('proj-1', false)
    expect(useGitPanelStore.getState().commitByProject['proj-1']!.isGenerating).toBe(false)
  })

  it('setGeneratingCommit preserves other fields of the slice', () => {
    const { setCommitMessage, setGeneratingCommit } = useGitPanelStore.getState()
    setCommitMessage('proj-1', 'draft')
    setGeneratingCommit('proj-1', true)
    const slice = useGitPanelStore.getState().commitByProject['proj-1']!
    expect(slice.message).toBe('draft')
    expect(slice.isGenerating).toBe(true)
  })

  // ── setRemoteOperationInProgress ──

  it('setRemoteOperationInProgress toggles the flag', () => {
    const { setRemoteOperationInProgress } = useGitPanelStore.getState()
    expect(useGitPanelStore.getState().remoteOperationInProgress).toBe(false)
    setRemoteOperationInProgress(true)
    expect(useGitPanelStore.getState().remoteOperationInProgress).toBe(true)
    setRemoteOperationInProgress(false)
    expect(useGitPanelStore.getState().remoteOperationInProgress).toBe(false)
  })

  // ── setActiveTab ──

  it('setActiveTab switches between changes and history', () => {
    const { setActiveTab } = useGitPanelStore.getState()
    expect(useGitPanelStore.getState().activeTab).toBe('changes')
    setActiveTab('history')
    expect(useGitPanelStore.getState().activeTab).toBe('history')
    setActiveTab('changes')
    expect(useGitPanelStore.getState().activeTab).toBe('changes')
  })

  // ── GitPanelEntry carries index/worktree status ──

  it('loadEntries preserves indexStatus and worktreeStatus', () => {
    const { loadEntries } = useGitPanelStore.getState()
    loadEntries([
      makeEntry({ path: 'conflict.ts', indexStatus: 'U', worktreeStatus: 'U' }),
      makeEntry({ path: 'deleted.ts', indexStatus: 'D', worktreeStatus: ' ' }),
    ])
    const entries = useGitPanelStore.getState().entries
    expect(entries[0]!.indexStatus).toBe('U')
    expect(entries[0]!.worktreeStatus).toBe('U')
    expect(entries[1]!.indexStatus).toBe('D')
  })

  // ── reset ──

  it('reset clears all state to initial values', () => {
    const store = useGitPanelStore.getState()
    store.setViewMode('tree')
    store.loadEntries([makeEntry({ path: 'a.ts' })])
    store.setCommitMessage('proj-1', 'fix: bug')
    store.setGeneratingCommit('proj-1', true)
    store.setCommitError('proj-1', 'boom')
    store.setCommitSuccess('proj-2', 'abc123def456')
    store.setBranch({ name: 'feature/x', upstream: '', ahead: 0, behind: 0 })
    store.setBranches([{ name: 'main', is_current: true, kind: 'local', upstream: 'origin/main' }])
    store.setGitRepo(true)
    store.setLoading(true)
    store.setError('some error')
    store.toggleExpandedDir('src')
    store.openBranchPicker()

    store.reset()

    const s = useGitPanelStore.getState()
    expect(s.viewMode).toBe('flat')
    expect(s.entries).toEqual([])
    expect(s.commitByProject).toEqual({})
    expect(s.branch).toEqual({ name: '', upstream: '', ahead: 0, behind: 0 })
    expect(s.branches).toEqual([])
    expect(s.expandedDirs).toEqual(new Set())
    expect(s.isLoading).toBe(false)
    expect(s.isGitRepo).toBe(false)
    expect(s.isBranchPickerOpen).toBe(false)
    expect(s.remoteOperationInProgress).toBe(false)
    expect(s.activeTab).toBe('changes')
    expect(s.error).toBeNull()
  })

  // ── Integration: typical flow ──

  it('handles a full staging → commit flow', () => {
    const store = useGitPanelStore.getState()

    // Initial load
    store.setGitRepo(true)
    store.setBranch({ name: 'main', upstream: '', ahead: 0, behind: 0 })
    store.loadEntries([
      makeEntry({ path: 'src/app.ts', status: 'M', staged: false }),
      makeEntry({ path: 'README.md', status: 'M', staged: false }),
    ])

    // Stage one file
    store.toggleStage('src/app.ts')
    expect(useGitPanelStore.getState().entries.find(e => e.path === 'src/app.ts')!.staged).toBe(true)

    // Set commit message
    store.setCommitMessage('proj-1', 'feat: add app module')

    // Simulate loading during commit
    store.setLoading(true)
    expect(useGitPanelStore.getState().isLoading).toBe(true)

    // Commit succeeds — reload
    store.loadEntries([])
    store.setCommitMessage('proj-1', '')
    expect(useGitPanelStore.getState().entries).toEqual([])
    expect(useGitPanelStore.getState().commitByProject['proj-1']!.message).toBe('')
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

// --- Phase 6: merge/rebase state & history tab ---

describe('gitPanelStore — Phase 6 (merge/rebase state & history tab)', () => {
  beforeEach(() => {
    resetStore()
  })

  it('initializes mergeRebaseState to the empty state', () => {
    expect(useGitPanelStore.getState().mergeRebaseState).toEqual({ is_merging: false, is_rebasing: false })
  })

  it('exposes EMPTY_MERGE_REBASE_STATE matching the empty state', () => {
    expect(EMPTY_MERGE_REBASE_STATE).toEqual({ is_merging: false, is_rebasing: false })
  })

  it('setMergeRebaseState updates the merge/rebase state', () => {
    const { setMergeRebaseState } = useGitPanelStore.getState()
    setMergeRebaseState({ is_merging: true, is_rebasing: false })
    expect(useGitPanelStore.getState().mergeRebaseState).toEqual({ is_merging: true, is_rebasing: false })
    setMergeRebaseState({ is_merging: false, is_rebasing: true })
    expect(useGitPanelStore.getState().mergeRebaseState).toEqual({ is_merging: false, is_rebasing: true })
  })

  it('reset() restores mergeRebaseState to the empty state', () => {
    const { setMergeRebaseState, reset } = useGitPanelStore.getState()
    setMergeRebaseState({ is_merging: true, is_rebasing: true })
    reset()
    expect(useGitPanelStore.getState().mergeRebaseState).toEqual({ is_merging: false, is_rebasing: false })
  })

  it('mergeRebaseState is transient: reset reverts it independent of viewMode', () => {
    // viewMode is persisted; mergeRebaseState is not. After reset both return
    // to their defaults, confirming mergeRebaseState is never carried over.
    const { setViewMode, setMergeRebaseState, reset } = useGitPanelStore.getState()
    setViewMode('tree')
    setMergeRebaseState({ is_merging: true, is_rebasing: false })

    reset()

    const s = useGitPanelStore.getState()
    expect(s.mergeRebaseState).toEqual(EMPTY_MERGE_REBASE_STATE)
    expect(s.activeTab).toBe('changes')
  })

  it('setActiveTab switches between changes and history', () => {
    const { setActiveTab } = useGitPanelStore.getState()
    setActiveTab('history')
    expect(useGitPanelStore.getState().activeTab).toBe('history')
    setActiveTab('changes')
    expect(useGitPanelStore.getState().activeTab).toBe('changes')
  })
})

// --- D8: sortBy / groupBy state & persistence ---

describe('gitPanelStore — D8 (sortBy / groupBy)', () => {
  beforeEach(() => {
    resetStore()
  })

  it('initializes sortBy to "path" and groupBy to "none"', () => {
    const s = useGitPanelStore.getState()
    expect(s.sortBy).toBe('path')
    expect(s.groupBy).toBe('none')
  })

  it('setSortBy updates the sort criterion', () => {
    const { setSortBy } = useGitPanelStore.getState()
    setSortBy('status')
    expect(useGitPanelStore.getState().sortBy).toBe('status')
    setSortBy('extension')
    expect(useGitPanelStore.getState().sortBy).toBe('extension')
    setSortBy('path')
    expect(useGitPanelStore.getState().sortBy).toBe('path')
  })

  it('setGroupBy updates the group criterion', () => {
    const { setGroupBy } = useGitPanelStore.getState()
    setGroupBy('status')
    expect(useGitPanelStore.getState().groupBy).toBe('status')
    setGroupBy('directory')
    expect(useGitPanelStore.getState().groupBy).toBe('directory')
    setGroupBy('none')
    expect(useGitPanelStore.getState().groupBy).toBe('none')
  })

  it('setSortBy and setGroupBy are independent', () => {
    const { setSortBy, setGroupBy } = useGitPanelStore.getState()
    setSortBy('extension')
    setGroupBy('status')
    const s = useGitPanelStore.getState()
    expect(s.sortBy).toBe('extension')
    expect(s.groupBy).toBe('status')
  })

  it('reset restores sortBy and groupBy to defaults', () => {
    const { setSortBy, setGroupBy, reset } = useGitPanelStore.getState()
    setSortBy('status')
    setGroupBy('directory')
    reset()
    const s = useGitPanelStore.getState()
    expect(s.sortBy).toBe('path')
    expect(s.groupBy).toBe('none')
  })

  it('partialize persists sortBy and groupBy alongside viewMode and expandedDirs', () => {
    const { setSortBy, setGroupBy, toggleExpandedDir, setViewMode } =
      useGitPanelStore.getState()
    setViewMode('tree')
    setSortBy('extension')
    setGroupBy('directory')
    toggleExpandedDir('src')

    const partial = partializeGitPanel(useGitPanelStore.getState())
    expect(partial.viewMode).toBe('tree')
    expect(partial.sortBy).toBe('extension')
    expect(partial.groupBy).toBe('directory')
    expect(partial.expandedDirs).toEqual(['src'])
  })

  it('merge applies defaults when persisted state lacks sortBy/groupBy', () => {
    const current = useGitPanelStore.getState()
    // Simulate an older localStorage entry written before D8 existed.
    const merged = mergeGitPanel({ viewMode: 'tree', expandedDirs: ['src'] }, current)
    expect(merged.sortBy).toBe('path')
    expect(merged.groupBy).toBe('none')
    expect(merged.viewMode).toBe('tree')
    expect(merged.expandedDirs).toEqual(new Set(['src']))
  })

  it('merge restores valid persisted sortBy/groupBy', () => {
    const current = useGitPanelStore.getState()
    const merged = mergeGitPanel(
      {
        viewMode: 'flat',
        expandedDirs: [],
        sortBy: 'status',
        groupBy: 'directory',
      },
      current,
    )
    expect(merged.sortBy).toBe('status')
    expect(merged.groupBy).toBe('directory')
  })

  it('merge falls back to defaults for invalid persisted sortBy/groupBy', () => {
    const current = useGitPanelStore.getState()
    const merged = mergeGitPanel({ sortBy: 'bogus', groupBy: 'nope' }, current)
    expect(merged.sortBy).toBe('path')
    expect(merged.groupBy).toBe('none')
  })
})

// --- Per-project commit-box state (commitByProject) ---

describe('gitPanelStore — per-project commit state', () => {
  beforeEach(() => {
    resetStore()
  })

  it('exposes EMPTY_COMMIT_DRAFT matching the default slice', () => {
    expect(EMPTY_COMMIT_DRAFT).toEqual({
      message: '',
      isGenerating: false,
      isCommitting: false,
      error: null,
      lastCommitSha: null,
    })
  })

  it('setCommitting toggles the flag on the given project only', () => {
    const { setCommitting } = useGitPanelStore.getState()
    setCommitting('proj-1', true)
    expect(useGitPanelStore.getState().commitByProject['proj-1']!.isCommitting).toBe(true)
    expect(useGitPanelStore.getState().commitByProject['proj-2']).toBeUndefined()
    setCommitting('proj-1', false)
    expect(useGitPanelStore.getState().commitByProject['proj-1']!.isCommitting).toBe(false)
  })

  it('a no-op commit-slice patch keeps the state reference stable', () => {
    const { setCommitMessage, setCommitting } = useGitPanelStore.getState()
    setCommitMessage('proj-1', 'draft')
    const before = useGitPanelStore.getState()
    // Same value on an existing slice, and false on an absent slice.
    setCommitting('proj-1', false)
    setCommitting('proj-never', false)
    expect(useGitPanelStore.getState()).toBe(before)
  })

  it('setCommitError sets and clears the error of the given project', () => {
    const { setCommitError } = useGitPanelStore.getState()
    setCommitError('proj-1', 'generation failed')
    expect(useGitPanelStore.getState().commitByProject['proj-1']!.error).toBe('generation failed')
    setCommitError('proj-1', null)
    expect(useGitPanelStore.getState().commitByProject['proj-1']!.error).toBeNull()
  })

  it('setCommitSuccess stores the SHA and clears the draft message', () => {
    const { setCommitMessage, setCommitSuccess } = useGitPanelStore.getState()
    setCommitMessage('proj-1', 'feat: thing')
    setCommitSuccess('proj-1', 'abc123def456789')
    const slice = useGitPanelStore.getState().commitByProject['proj-1']!
    expect(slice.lastCommitSha).toBe('abc123def456789')
    expect(slice.message).toBe('')
  })

  it('setCommitSuccess(null) dismisses the banner without wiping a new draft', () => {
    // The banner auto-dismiss timer fires seconds after the commit; by then
    // the user may have started typing a new message that must survive.
    const { setCommitSuccess, setCommitMessage } = useGitPanelStore.getState()
    setCommitSuccess('proj-1', 'abc123def456789')
    setCommitMessage('proj-1', 'next draft')
    setCommitSuccess('proj-1', null)
    const slice = useGitPanelStore.getState().commitByProject['proj-1']!
    expect(slice.lastCommitSha).toBeNull()
    expect(slice.message).toBe('next draft')
  })

  it('commit banners auto-dismiss per project and independently', () => {
    // Regression: a single shared timer left an earlier project's banner
    // stranded forever when a later commit in another project replaced it.
    vi.useFakeTimers()
    try {
      const { setCommitSuccess } = useGitPanelStore.getState()
      setCommitSuccess('proj-a', 'sha-a')
      setCommitSuccess('proj-b', 'sha-b')
      // Both banners still visible before the dismissal window elapses.
      expect(useGitPanelStore.getState().commitByProject['proj-a']!.lastCommitSha).toBe('sha-a')
      expect(useGitPanelStore.getState().commitByProject['proj-b']!.lastCommitSha).toBe('sha-b')
      vi.advanceTimersByTime(COMMIT_BANNER_DISMISS_MS)
      expect(useGitPanelStore.getState().commitByProject['proj-a']!.lastCommitSha).toBeNull()
      expect(useGitPanelStore.getState().commitByProject['proj-b']!.lastCommitSha).toBeNull()
    } finally {
      vi.useRealTimers()
    }
  })

  it('a newer commit banner is not clobbered by an older auto-dismiss timer', () => {
    vi.useFakeTimers()
    try {
      const { setCommitSuccess } = useGitPanelStore.getState()
      setCommitSuccess('proj-1', 'sha-old')
      // A second commit in the same project re-arms the dismissal timer.
      vi.advanceTimersByTime(COMMIT_BANNER_DISMISS_MS - 100)
      setCommitSuccess('proj-1', 'sha-new')
      vi.advanceTimersByTime(COMMIT_BANNER_DISMISS_MS - 100)
      // The old timer was cancelled; the new banner is still within its own
      // full window.
      expect(useGitPanelStore.getState().commitByProject['proj-1']!.lastCommitSha).toBe('sha-new')
      vi.advanceTimersByTime(100)
      expect(useGitPanelStore.getState().commitByProject['proj-1']!.lastCommitSha).toBeNull()
    } finally {
      vi.useRealTimers()
    }
  })

  it('dropProjectCommitState removes only the given project', () => {
    const { setCommitMessage, dropProjectCommitState } = useGitPanelStore.getState()
    setCommitMessage('proj-a', 'draft a')
    setCommitMessage('proj-b', 'draft b')
    dropProjectCommitState('proj-a')
    const s = useGitPanelStore.getState().commitByProject
    expect(s['proj-a']).toBeUndefined()
    expect(s['proj-b']!.message).toBe('draft b')
  })

  it('dropProjectCommitState is a no-op for an unknown project', () => {
    const { setCommitMessage, dropProjectCommitState } = useGitPanelStore.getState()
    setCommitMessage('proj-a', 'draft a')
    expect(() => dropProjectCommitState('never-seen')).not.toThrow()
    expect(useGitPanelStore.getState().commitByProject['proj-a']!.message).toBe('draft a')
  })

  it('commit state survives an A→B→A project switch (in-memory)', () => {
    const { setCommitMessage, setCommitError, setGeneratingCommit } = useGitPanelStore.getState()
    setCommitMessage('proj-a', 'feat: a')
    setGeneratingCommit('proj-a', true)
    setCommitError('proj-a', 'err')
    setCommitMessage('proj-b', 'fix: b')

    // Switch away to B and back to A — A's values are restored untouched.
    const s = useGitPanelStore.getState().commitByProject
    expect(s['proj-a']).toEqual({
      message: 'feat: a',
      isGenerating: true,
      isCommitting: false,
      error: 'err',
      lastCommitSha: null,
    })
    expect(s['proj-b']!.message).toBe('fix: b')
    expect(s['proj-b']!.isGenerating).toBe(false)
  })

  it('commitByProject is transient: excluded from the persisted partial', () => {
    const { setCommitMessage, setCommitSuccess } = useGitPanelStore.getState()
    setCommitMessage('proj-1', 'draft')
    setCommitSuccess('proj-1', 'abc123')
    const partial = partializeGitPanel(useGitPanelStore.getState())
    expect(partial).not.toHaveProperty('commitByProject')
  })
})
