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
  /** Active GitPanel tab (Phase 5/6). */
  activeTab: 'changes' | 'history' | 'graph'
  /** Transient: whether a merge or rebase is currently in progress (Phase 6). Not persisted. */
  mergeRebaseState: MergeRebaseState
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
  setRemoteOperationInProgress: (inProgress: boolean) => void
  setActiveTab: (tab: 'changes' | 'history' | 'graph') => void
  setMergeRebaseState: (state: MergeRebaseState) => void
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

      setRemoteOperationInProgress: (inProgress) =>
        set({ remoteOperationInProgress: inProgress }),

      setActiveTab: (tab) => set({ activeTab: tab }),

      setMergeRebaseState: (state) => set({ mergeRebaseState: state }),

      reset: () => set({ ...initialState, expandedDirs: new Set<string>() }),
    }),
    {
      name: 'git-panel-settings',
      partialize: (state) => ({
        viewMode: state.viewMode,
        expandedDirs: Array.from(state.expandedDirs),
      }),
      merge: (persisted, current) => {
        const p = persisted as {
          viewMode?: 'flat' | 'tree'
          expandedDirs?: string[]
        }
        return {
          ...current,
          viewMode: p.viewMode ?? current.viewMode,
          expandedDirs: new Set(p.expandedDirs ?? []),
        }
      },
    },
  ),
)
