import { create } from 'zustand'
import { persist } from 'zustand/middleware'
import type { Branch, BranchInfo, MergeRebaseState } from '@/types/models'

// --- Types ---

/** Empty BranchInfo used as the initial branch state. */
export const EMPTY_BRANCH_INFO: BranchInfo = {
  name: '',
  upstream: '',
  ahead: 0,
  behind: 0,
}

/** Empty MergeRebaseState — no merge or rebase in progress. */
export const EMPTY_MERGE_REBASE_STATE: MergeRebaseState = {
  is_merging: false,
  is_rebasing: false,
}

export interface GitPanelEntry {
  path: string
  /** Git status code: M=modified, A=added, R=renamed, C=copied, D=deleted, U=unmerged */
  status: string
  staged: boolean
  diffStat: { added: number; deleted: number } | null
  /** Raw index (staged) status code from `git status --porcelain` (Phase 5). */
  indexStatus: string
  /** Raw worktree (unstaged) status code from `git status --porcelain` (Phase 5). */
  worktreeStatus: string
}

/** Sort criterion for the Changes list (D8). Persisted across sessions. */
export type SortBy = 'path' | 'status' | 'extension'

/** Grouping criterion for the Changes list (D8). Persisted across sessions. */
export type GroupBy = 'none' | 'status' | 'directory'

/** Valid SortBy values — used by persist `merge` to validate localStorage. */
const SORT_BY_VALUES = new Set<SortBy>(['path', 'status', 'extension'])

/** Valid GroupBy values — used by persist `merge` to validate localStorage. */
const GROUP_BY_VALUES = new Set<GroupBy>(['none', 'status', 'directory'])

// --- State types ---

interface GitPanelState {
  viewMode: 'flat' | 'tree'
  entries: GitPanelEntry[]
  commitMessage: string
  branch: BranchInfo
  branches: Branch[]
  isBranchPickerOpen: boolean
  isGeneratingCommit: boolean
  expandedDirs: Set<string>
  isLoading: boolean
  isGitRepo: boolean
  error: string | null
  /** True while a pull/push/fetch is running — blocks parallel remote ops (Phase 5). */
  remoteOperationInProgress: boolean
  /** Active GitPanel tab. 'graph' was merged into 'history' (unified view). */
  activeTab: 'changes' | 'history'
  /** Transient: whether a merge or rebase is currently in progress (Phase 6). Not persisted. */
  mergeRebaseState: MergeRebaseState
  /** Transient: SHA of the most recently created commit (FE-1). Not persisted. */
  lastCommitSha: string | null
  /** Sort criterion for the Changes list, persisted across sessions (D8). */
  sortBy: SortBy
  /** Grouping criterion for the Changes list, persisted across sessions (D8). */
  groupBy: GroupBy
}

interface GitPanelActions {
  setViewMode: (mode: 'flat' | 'tree') => void
  setCommitMessage: (message: string) => void
  loadEntries: (entries: GitPanelEntry[]) => void
  toggleStage: (path: string) => void
  setBranch: (branch: BranchInfo) => void
  setBranches: (branches: Branch[]) => void
  openBranchPicker: () => void
  closeBranchPicker: () => void
  setGeneratingCommit: (generating: boolean) => void
  setGitRepo: (isRepo: boolean) => void
  setLoading: (loading: boolean) => void
  setError: (error: string | null) => void
  toggleExpandedDir: (dir: string) => void
  /** Replace the entire expanded-dirs set (used by expand-all / collapse-all). */
  setExpandedDirs: (dirs: Set<string>) => void
  setRemoteOperationInProgress: (inProgress: boolean) => void
  setActiveTab: (tab: 'changes' | 'history') => void
  setMergeRebaseState: (state: MergeRebaseState) => void
  setLastCommitSha: (sha: string | null) => void
  setSortBy: (mode: SortBy) => void
  setGroupBy: (mode: GroupBy) => void
  reset: () => void
}

// --- Initial state (used by both create and reset) ---

const initialState: GitPanelState = {
  viewMode: 'flat',
  entries: [],
  commitMessage: '',
  branch: EMPTY_BRANCH_INFO,
  branches: [],
  expandedDirs: new Set<string>(),
  isLoading: false,
  isGitRepo: false,
  isBranchPickerOpen: false,
  isGeneratingCommit: false,
  error: null,
  remoteOperationInProgress: false,
  activeTab: 'changes',
  mergeRebaseState: EMPTY_MERGE_REBASE_STATE,
  lastCommitSha: null,
  sortBy: 'path',
  groupBy: 'none',
}

// --- Persist helpers (exported for direct unit testing) ---

/**
 * Select the slice of state persisted to localStorage. `expandedDirs` (a Set)
 * is serialized to an array; `sortBy`/`groupBy` are persisted directly (D8).
 */
export function partializeGitPanel(
  state: GitPanelState & GitPanelActions,
): {
  viewMode: 'flat' | 'tree'
  expandedDirs: string[]
  sortBy: SortBy
  groupBy: GroupBy
} {
  return {
    viewMode: state.viewMode,
    expandedDirs: Array.from(state.expandedDirs),
    sortBy: state.sortBy,
    groupBy: state.groupBy,
  }
}

/**
 * Rehydrate persisted state into the current state. Older localStorage entries
 * (written before D8) lack `sortBy`/`groupBy`; corrupt or unknown values are
 * rejected — both fall back to the current (default) values.
 */
export function mergeGitPanel(
  persisted: unknown,
  current: GitPanelState & GitPanelActions,
): GitPanelState & GitPanelActions {
  const p = persisted as {
    viewMode?: 'flat' | 'tree'
    expandedDirs?: string[]
    sortBy?: SortBy
    groupBy?: GroupBy
  }
  const sortBy: SortBy =
    p.sortBy !== undefined && SORT_BY_VALUES.has(p.sortBy)
      ? p.sortBy
      : current.sortBy
  const groupBy: GroupBy =
    p.groupBy !== undefined && GROUP_BY_VALUES.has(p.groupBy)
      ? p.groupBy
      : current.groupBy
  return {
    ...current,
    viewMode: p.viewMode ?? current.viewMode,
    expandedDirs: new Set(p.expandedDirs ?? []),
    sortBy,
    groupBy,
  }
}

// --- Store ---

export const useGitPanelStore = create<GitPanelState & GitPanelActions>()(
  persist(
    (set) => ({
      ...initialState,

      setViewMode: (mode) => set({ viewMode: mode }),

      setCommitMessage: (message) => set({ commitMessage: message }),

      setLoading: (loading) => set({ isLoading: loading }),

      setError: (error) => set({ error }),

      loadEntries: (entries) => set({ entries, isLoading: false, error: null }),

      toggleStage: (path) =>
        set((s) => ({
          entries: s.entries.map((entry) =>
            entry.path === path
              ? { ...entry, staged: !entry.staged }
              : entry,
          ),
        })),

      setBranch: (branch) => set({ branch }),

      setBranches: (branches) => set({ branches }),

      openBranchPicker: () => set({ isBranchPickerOpen: true }),

      closeBranchPicker: () => set({ isBranchPickerOpen: false }),

      setGeneratingCommit: (generating) => set({ isGeneratingCommit: generating }),

      setGitRepo: (isRepo) => set({ isGitRepo: isRepo }),

      toggleExpandedDir: (dir) =>
        set((s) => {
          const next = new Set(s.expandedDirs)
          if (next.has(dir)) {
            next.delete(dir)
          } else {
            next.add(dir)
          }
          return { expandedDirs: next }
        }),

      setExpandedDirs: (dirs) => set({ expandedDirs: dirs }),

      setRemoteOperationInProgress: (inProgress) =>
        set({ remoteOperationInProgress: inProgress }),

      setActiveTab: (tab) => set({ activeTab: tab }),

      setMergeRebaseState: (state) => set({ mergeRebaseState: state }),

      setLastCommitSha: (sha) => set({ lastCommitSha: sha }),

      setSortBy: (mode) => set({ sortBy: mode }),

      setGroupBy: (mode) => set({ groupBy: mode }),

      reset: () => set({ ...initialState, expandedDirs: new Set<string>() }),
    }),
    {
      name: 'git-panel-settings',
      partialize: partializeGitPanel,
      merge: mergeGitPanel,
    },
  ),
)
