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
import { useGitPanelStore, EMPTY_MERGE_REBASE_STATE, EMPTY_COMMIT_DRAFT, COMMIT_BANNER_DISMISS_MS, partializeGitPanel, mergeGitPanel, selectGitPanelTab, selectSkipCommitSuppress, type GitPanelEntry } from '@/stores/gitPanelStore'

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
    expect(s.gitRepoProjectId).toBeNull()
    expect(s.isBranchPickerOpen).toBe(false)
    expect(s.remoteOperationInProgress).toBe(false)
    expect(s.activeTabByProject).toEqual({})
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
      lastCommitOutput: null,
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

  it('setGitRepo toggles isGitRepo and records the project id it belongs to', () => {
    const { setGitRepo } = useGitPanelStore.getState()
    setGitRepo(true, 'proj-1')
    expect(useGitPanelStore.getState().isGitRepo).toBe(true)
    expect(useGitPanelStore.getState().gitRepoProjectId).toBe('proj-1')
    setGitRepo(false, 'proj-2')
    expect(useGitPanelStore.getState().isGitRepo).toBe(false)
    expect(useGitPanelStore.getState().gitRepoProjectId).toBe('proj-2')
    setGitRepo(false, null)
    expect(useGitPanelStore.getState().isGitRepo).toBe(false)
    expect(useGitPanelStore.getState().gitRepoProjectId).toBeNull()
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

  // ── setActiveTab (per-project) ──

  it('setActiveTab switches a single project between files, changes and history', () => {
    const { setActiveTab } = useGitPanelStore.getState()
    expect(useGitPanelStore.getState().activeTabByProject['p1']).toBeUndefined()
    setActiveTab('p1', 'history')
    expect(useGitPanelStore.getState().activeTabByProject['p1']).toBe('history')
    setActiveTab('p1', 'changes')
    expect(useGitPanelStore.getState().activeTabByProject['p1']).toBe('changes')
    setActiveTab('p1', 'files')
    expect(useGitPanelStore.getState().activeTabByProject['p1']).toBe('files')
  })

  it("setActiveTab('p1','history') touches only p1 and leaves p2 untouched", () => {
    const { setActiveTab } = useGitPanelStore.getState()
    setActiveTab('p2', 'changes')
    setActiveTab('p1', 'history')
    const map = useGitPanelStore.getState().activeTabByProject
    expect(map['p1']).toBe('history')
    expect(map['p2']).toBe('changes')
  })

  it('setActiveTab is a reference no-op when the tab is unchanged', () => {
    const { setActiveTab } = useGitPanelStore.getState()
    setActiveTab('p1', 'history')
    const before = useGitPanelStore.getState()
    setActiveTab('p1', 'history')
    // Reference-stable: subscribers must not churn on a same-value set.
    expect(useGitPanelStore.getState()).toBe(before)
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
    store.setGitRepo(true, 'proj-1')
    store.setLoading(true)
    store.setError('some error')
    store.toggleExpandedDir('src')
    store.openBranchPicker()
    store.setActiveTab('p1', 'history')

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
    expect(s.gitRepoProjectId).toBeNull()
    expect(s.isBranchPickerOpen).toBe(false)
    expect(s.remoteOperationInProgress).toBe(false)
    expect(s.activeTabByProject).toEqual({})
    expect(s.error).toBeNull()
  })

  // ── Integration: typical flow ──

  it('handles a full staging → commit flow', () => {
    const store = useGitPanelStore.getState()

    // Initial load
    store.setGitRepo(true, 'proj-1')
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
    store.setGitRepo(false, 'proj-1')
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
    expect(s.activeTabByProject).toEqual({})
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
      lastCommitOutput: null,
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
      lastCommitOutput: null,
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

  it('setCommitSuccess stores the commit output; dismissal (null) keeps it', () => {
    const { setCommitSuccess } = useGitPanelStore.getState()
    setCommitSuccess('proj-1', 'abc123', '[main abc123d] feat: run hooks\nhook said hi')
    let slice = useGitPanelStore.getState().commitByProject['proj-1']!
    expect(slice.lastCommitOutput).toBe('[main abc123d] feat: run hooks\nhook said hi')
    // The banner auto-dismissal clears only the SHA…
    setCommitSuccess('proj-1', null)
    slice = useGitPanelStore.getState().commitByProject['proj-1']!
    expect(slice.lastCommitSha).toBeNull()
    // …the output survives until the next commit replaces it.
    expect(slice.lastCommitOutput).toBe('[main abc123d] feat: run hooks\nhook said hi')
    // A new commit without output clears the stale log.
    setCommitSuccess('proj-1', 'def456')
    slice = useGitPanelStore.getState().commitByProject['proj-1']!
    expect(slice.lastCommitOutput).toBeNull()
  })

  it('the banner auto-dismiss timer does not clear the stored commit output', () => {
    vi.useFakeTimers()
    try {
      const { setCommitSuccess } = useGitPanelStore.getState()
      setCommitSuccess('proj-1', 'abc123', 'hook: prettier ran')
      vi.advanceTimersByTime(COMMIT_BANNER_DISMISS_MS)
      const slice = useGitPanelStore.getState().commitByProject['proj-1']!
      expect(slice.lastCommitSha).toBeNull()
      expect(slice.lastCommitOutput).toBe('hook: prettier ran')
    } finally {
      vi.useRealTimers()
    }
  })
})

// --- Per-project suppression-dialog skip flag (skipCommitSuppressByProject) ---

describe('gitPanelStore — commit suppression skip flag', () => {
  beforeEach(() => {
    resetStore()
  })

  it('selectSkipCommitSuppress defaults to false (dialog shows)', () => {
    const s = useGitPanelStore.getState()
    expect(selectSkipCommitSuppress(s, 'never-seen')).toBe(false)
    expect(selectSkipCommitSuppress(s, null)).toBe(false)
    expect(selectSkipCommitSuppress(s, undefined)).toBe(false)
  })

  it('setSkipCommitSuppress sets and clears the flag per project', () => {
    const { setSkipCommitSuppress } = useGitPanelStore.getState()
    setSkipCommitSuppress('proj-a', true)
    setSkipCommitSuppress('proj-b', false)
    expect(selectSkipCommitSuppress(useGitPanelStore.getState(), 'proj-a')).toBe(true)
    expect(selectSkipCommitSuppress(useGitPanelStore.getState(), 'proj-b')).toBe(false)
    // Explicitly re-enabling the dialog restores it.
    setSkipCommitSuppress('proj-a', false)
    expect(selectSkipCommitSuppress(useGitPanelStore.getState(), 'proj-a')).toBe(false)
  })

  it('setSkipCommitSuppress is a reference-stable no-op for an unchanged value', () => {
    const { setSkipCommitSuppress } = useGitPanelStore.getState()
    setSkipCommitSuppress('proj-a', true)
    // A second write with the SAME value is a no-op; a write that flips to
    // the implicit default (false) records an explicit entry — the same
    // contract setActiveTab uses.
    const before = useGitPanelStore.getState()
    setSkipCommitSuppress('proj-a', true)
    expect(useGitPanelStore.getState()).toBe(before)
  })

  it('the flag survives a persist round-trip (partialize → merge)', () => {
    const { setSkipCommitSuppress } = useGitPanelStore.getState()
    setSkipCommitSuppress('proj-a', true)
    const partial = partializeGitPanel(useGitPanelStore.getState())
    expect(partial.skipCommitSuppressByProject).toEqual({ 'proj-a': true })

    const rehydrated = JSON.parse(JSON.stringify(partial)) as unknown
    const merged = mergeGitPanel(rehydrated, useGitPanelStore.getState())
    expect(merged.skipCommitSuppressByProject).toEqual({ 'proj-a': true })
  })

  it('merge defaults the flag map to {} for legacy state without it', () => {
    const current = useGitPanelStore.getState()
    const merged = mergeGitPanel({ viewMode: 'tree', expandedDirs: ['src'] }, current)
    expect(merged.skipCommitSuppressByProject).toEqual({})
  })

  it('merge rejects non-boolean persisted flags but keeps valid ones', () => {
    const current = useGitPanelStore.getState()
    const merged = mergeGitPanel(
      { skipCommitSuppressByProject: { p1: true, p2: 'yes', p3: 0, p4: false } },
      current,
    )
    expect(merged.skipCommitSuppressByProject).toEqual({ p1: true, p4: false })
  })

  it('merge tolerates a non-object persisted flag map', () => {
    const current = useGitPanelStore.getState()
    const merged = mergeGitPanel({ skipCommitSuppressByProject: null }, current)
    expect(merged.skipCommitSuppressByProject).toEqual({})
  })

  it('rehydrates the flag map from a real localStorage payload', async () => {
    localStorage.setItem(
      'git-panel-settings',
      JSON.stringify({
        state: {
          viewMode: 'flat',
          expandedDirs: [],
          skipCommitSuppressByProject: { p1: true, p2: 'bogus' },
        },
        version: 0,
      }),
    )

    await useGitPanelStore.persist.rehydrate()

    expect(useGitPanelStore.getState().skipCommitSuppressByProject).toEqual({ p1: true })
  })

  it('dropProjectCommitState removes the flag along with the draft', () => {
    const { setSkipCommitSuppress, setCommitMessage, dropProjectCommitState } = useGitPanelStore.getState()
    setSkipCommitSuppress('proj-a', true)
    setSkipCommitSuppress('proj-b', true)
    setCommitMessage('proj-a', 'draft a')
    dropProjectCommitState('proj-a')
    const s = useGitPanelStore.getState()
    expect(s.skipCommitSuppressByProject['proj-a']).toBeUndefined()
    expect(s.skipCommitSuppressByProject['proj-b']).toBe(true)
    expect(s.commitByProject['proj-a']).toBeUndefined()
  })

  it('reset clears the flag map', () => {
    const { setSkipCommitSuppress, reset } = useGitPanelStore.getState()
    setSkipCommitSuppress('proj-a', true)
    reset()
    expect(useGitPanelStore.getState().skipCommitSuppressByProject).toEqual({})
  })

  it('selectors return primitives (no per-call allocation)', () => {
    setSkipCommitSuppressFlagForTest()
    // selectSkipCommitSuppress returns a boolean primitive.
    expect(typeof selectSkipCommitSuppress(useGitPanelStore.getState(), 'proj-a')).toBe('boolean')
  })
})

/** Helper used by the selector-stability test above. */
function setSkipCommitSuppressFlagForTest() {
  useGitPanelStore.getState().setSkipCommitSuppress('proj-a', true)
}

// --- Per-project active tab (activeTabByProject) & persistence ---

describe('gitPanelStore — per-project active tab', () => {
  beforeEach(() => {
    resetStore()
  })

  it('selectGitPanelTab returns "files" for an unknown project', () => {
    const s = useGitPanelStore.getState()
    expect(selectGitPanelTab(s, 'never-seen')).toBe('files')
  })

  it('selectGitPanelTab returns "files" when no project is active', () => {
    const s = useGitPanelStore.getState()
    expect(selectGitPanelTab(s, null)).toBe('files')
    expect(selectGitPanelTab(s, undefined)).toBe('files')
  })

  it('selectGitPanelTab returns the recorded tab for a known project', () => {
    const { setActiveTab } = useGitPanelStore.getState()
    setActiveTab('p1', 'history')
    expect(selectGitPanelTab(useGitPanelStore.getState(), 'p1')).toBe('history')
  })

  it('dropProjectTabs removes just the given project', () => {
    const { setActiveTab, dropProjectTabs } = useGitPanelStore.getState()
    setActiveTab('p1', 'history')
    setActiveTab('p2', 'changes')
    dropProjectTabs('p1')
    const map = useGitPanelStore.getState().activeTabByProject
    expect(map['p1']).toBeUndefined()
    expect(map['p2']).toBe('changes')
  })

  it('dropProjectTabs is a no-op for an unknown project', () => {
    const { setActiveTab, dropProjectTabs } = useGitPanelStore.getState()
    setActiveTab('p1', 'history')
    const before = useGitPanelStore.getState()
    expect(() => dropProjectTabs('never-seen')).not.toThrow()
    expect(useGitPanelStore.getState()).toBe(before)
  })

  it('activeTabByProject survives a persist round-trip (partialize → merge)', () => {
    const { setActiveTab } = useGitPanelStore.getState()
    setActiveTab('p1', 'history')
    setActiveTab('p2', 'changes')

    const partial = partializeGitPanel(useGitPanelStore.getState())
    expect(partial.activeTabByProject).toEqual({ p1: 'history', p2: 'changes' })

    // Simulate the JSON round-trip localStorage performs.
    const rehydrated = JSON.parse(JSON.stringify(partial)) as unknown
    const merged = mergeGitPanel(rehydrated, useGitPanelStore.getState())
    expect(merged.activeTabByProject).toEqual({ p1: 'history', p2: 'changes' })
  })

  it('merge defaults activeTabByProject to {} for legacy state without the map', () => {
    const current = useGitPanelStore.getState()
    // Simulate an older localStorage entry written before per-project tabs.
    const merged = mergeGitPanel({ viewMode: 'tree', expandedDirs: ['src'] }, current)
    expect(merged.activeTabByProject).toEqual({})
  })

  it('merge rejects unknown persisted tab values but keeps valid ones', () => {
    const current = useGitPanelStore.getState()
    const merged = mergeGitPanel(
      {
        activeTabByProject: { p1: 'history', p2: 'bogus', p3: 42, p4: 'changes' },
      },
      current,
    )
    expect(merged.activeTabByProject).toEqual({ p1: 'history', p4: 'changes' })
  })

  it('merge tolerates a non-object persisted activeTabByProject', () => {
    const current = useGitPanelStore.getState()
    const merged = mergeGitPanel({ activeTabByProject: null }, current)
    expect(merged.activeTabByProject).toEqual({})
  })

  it('reset clears activeTabByProject', () => {
    const { setActiveTab, reset } = useGitPanelStore.getState()
    setActiveTab('p1', 'history')
    reset()
    expect(useGitPanelStore.getState().activeTabByProject).toEqual({})
  })

  it('rehydrates activeTabByProject from a real localStorage payload', async () => {
    // Exercise the actual persist middleware (name 'git-panel-settings'),
    // not just the partialize/merge helpers: seed localStorage, rehydrate.
    localStorage.setItem(
      'git-panel-settings',
      JSON.stringify({
        state: {
          viewMode: 'tree',
          expandedDirs: [],
          activeTabByProject: { p1: 'history', p2: 'changes' },
        },
        version: 0,
      }),
    )

    await useGitPanelStore.persist.rehydrate()

    expect(useGitPanelStore.getState().activeTabByProject).toEqual({
      p1: 'history',
      p2: 'changes',
    })
  })

  it('rehydrates a legacy localStorage entry (no tab map) to an empty map', async () => {
    localStorage.setItem(
      'git-panel-settings',
      JSON.stringify({ state: { viewMode: 'tree', expandedDirs: [] }, version: 0 }),
    )

    await useGitPanelStore.persist.rehydrate()

    expect(useGitPanelStore.getState().activeTabByProject).toEqual({})
  })

  it('rehydrate rejects a bogus persisted tab value but keeps valid siblings', async () => {
    localStorage.setItem(
      'git-panel-settings',
      JSON.stringify({
        state: {
          viewMode: 'tree',
          expandedDirs: [],
          activeTabByProject: { p1: 'history', p2: 'bogus' },
        },
        version: 0,
      }),
    )

    await useGitPanelStore.persist.rehydrate()

    expect(useGitPanelStore.getState().activeTabByProject).toEqual({ p1: 'history' })
  })
})
