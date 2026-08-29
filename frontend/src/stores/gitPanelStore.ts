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

/**
 * Per-project state for the commit box: the draft message, the AI-generation
 * flag, the commit-in-flight flag, the last error, and the SHA of the most
 * recent commit (success banner). Keyed by project id so a draft survives
 * project switches and CHAT↔CODE mode switches (GitPanel unmount), and so a
 * mid-generation project switch can never land text in the wrong project's
 * box. Transient — NOT persisted.
 */
export interface CommitDraftState {
  /** Draft commit message shown in the textarea. */
  message: string
  /** True while an AI commit-message generation is in flight. */
  isGenerating: boolean
  /** True while the commit RPC itself is in flight (disables the buttons). */
  isCommitting: boolean
  /** Last generation/commit error surfaced under the textarea. */
  error: string | null
  /** SHA of the most recently created commit (FE-1). Drives the success banner. */
  lastCommitSha: string | null
}

/**
 * Default per-project commit slice, used when a project has no entry yet.
 * Referentially stable (module constant) so components can derive defaults
 * without allocating inside Zustand selectors.
 */
export const EMPTY_COMMIT_DRAFT: CommitDraftState = {
  message: '',
  isGenerating: false,
  isCommitting: false,
  error: null,
  lastCommitSha: null,
}

// --- State types ---

interface GitPanelState {
  viewMode: 'flat' | 'tree'
  entries: GitPanelEntry[]
  /** Transient per-project commit-box state, keyed by project id. Not persisted. */
  commitByProject: Record<string, CommitDraftState>
  branch: BranchInfo
  branches: Branch[]
  isBranchPickerOpen: boolean
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
  /** Sort criterion for the Changes list, persisted across sessions (D8). */
  sortBy: SortBy
  /** Grouping criterion for the Changes list, persisted across sessions (D8). */
  groupBy: GroupBy
  /** Transient: set by "View History" file-tree action, consumed by GitHistoryTab, cleared after use. */
  pendingHistoryFilter: string | null
  /**
   * Transient: a ref (SHA/branch/tag) to pre-select as the base for the next
   * branch created in the BranchPicker. Set by the commit context menu's
   * "Create › Branch" action so the Switch Branch dialog opens with that
   * commit already chosen as the start-point. Consumed and cleared by
   * NewBranchSection on open.
   */
  pendingBranchBase: string | null
}

interface GitPanelActions {
  setViewMode: (mode: 'flat' | 'tree') => void
  /** Set the commit-message draft for a project. */
  setCommitMessage: (projectId: string, message: string) => void
  loadEntries: (entries: GitPanelEntry[]) => void
  toggleStage: (path: string) => void
  setBranch: (branch: BranchInfo) => void
  setBranches: (branches: Branch[]) => void
  openBranchPicker: () => void
  closeBranchPicker: () => void
  /** Toggle the AI-generation flag for a project. */
  setGeneratingCommit: (projectId: string, generating: boolean) => void
  /** Toggle the commit-in-flight flag for a project. */
  setCommitting: (projectId: string, committing: boolean) => void
  /** Set or clear the commit-box error for a project. */
  setCommitError: (projectId: string, error: string | null) => void
  /**
   * Record a successful commit for a project: store the new SHA (drives the
   * success banner) and clear that project's draft message. Passing `null`
   * clears the banner only (banner auto-dismiss timer) and keeps the draft.
   */
  setCommitSuccess: (projectId: string, sha: string | null) => void
  /** Drop a project's commit-box state entirely (project deleted). */
  dropProjectCommitState: (projectId: string) => void
  setGitRepo: (isRepo: boolean) => void
  setLoading: (loading: boolean) => void
  setError: (error: string | null) => void
  toggleExpandedDir: (dir: string) => void
  /** Replace the entire expanded-dirs set (used by expand-all / collapse-all). */
  setExpandedDirs: (dirs: Set<string>) => void
  setRemoteOperationInProgress: (inProgress: boolean) => void
  setActiveTab: (tab: 'changes' | 'history') => void
  setMergeRebaseState: (state: MergeRebaseState) => void
  setSortBy: (mode: SortBy) => void
  setGroupBy: (mode: GroupBy) => void
  /** Queue a file-path filter for the git history tab. */
  setPendingHistoryFilter: (filter: string) => void
  /** Clear the pending history filter after it has been consumed. */
  clearPendingHistoryFilter: () => void
  /** Queue a preselected base ref for the next BranchPicker open. */
  setPendingBranchBase: (base: string) => void
  /** Clear the pending branch base after it has been consumed. */
  clearPendingBranchBase: () => void
  reset: () => void
}

// --- Initial state (used by both create and reset) ---

const initialState: GitPanelState = {
  viewMode: 'flat',
  entries: [],
  commitByProject: {},
  branch: EMPTY_BRANCH_INFO,
  branches: [],
  expandedDirs: new Set<string>(),
  isLoading: false,
  isGitRepo: false,
  isBranchPickerOpen: false,
  error: null,
  remoteOperationInProgress: false,
  activeTab: 'changes',
  mergeRebaseState: EMPTY_MERGE_REBASE_STATE,
  sortBy: 'path',
  groupBy: 'none',
  pendingHistoryFilter: null,
  pendingBranchBase: null,
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

/**
 * Spread-update one project's commit slice, creating it with defaults first
 * if the project has no entry yet. Other projects' slices are untouched.
 * A patch that changes nothing returns the state itself (reference-equal no-
 * op, the same contract the per-key stores use) so subscribers don't churn.
 */
function withCommitDraft(
  s: GitPanelState,
  projectId: string,
  patch: Partial<CommitDraftState>,
): GitPanelState | Pick<GitPanelState, 'commitByProject'> {
  const current = s.commitByProject[projectId] ?? EMPTY_COMMIT_DRAFT
  if ((Object.keys(patch) as (keyof CommitDraftState)[]).every((k) => current[k] === patch[k])) {
    return s
  }
  return {
    commitByProject: {
      ...s.commitByProject,
      [projectId]: {
        ...current,
        ...patch,
      },
    },
  }
}

export const useGitPanelStore = create<GitPanelState & GitPanelActions>()(
  persist(
    (set) => ({
      ...initialState,

      setViewMode: (mode) => set({ viewMode: mode }),

      setCommitMessage: (projectId, message) =>
        set((s) => withCommitDraft(s, projectId, { message })),

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

      setGeneratingCommit: (projectId, generating) =>
        set((s) => withCommitDraft(s, projectId, { isGenerating: generating })),

      setCommitting: (projectId, committing) =>
        set((s) => withCommitDraft(s, projectId, { isCommitting: committing })),

      setCommitError: (projectId, error) =>
        set((s) => withCommitDraft(s, projectId, { error })),

      setCommitSuccess: (projectId, sha) =>
        set((s) =>
          withCommitDraft(s, projectId, {
            lastCommitSha: sha,
            // A real SHA marks a completed commit: consume the draft.
            // `null` only dismisses the banner (auto-dismiss timer) and
            // must not wipe a draft the user may have started typing.
            ...(sha !== null ? { message: '' } : {}),
          }),
        ),

      dropProjectCommitState: (projectId) =>
        set((s) => {
          if (s.commitByProject[projectId] === undefined) return {}
          const next = { ...s.commitByProject }
          delete next[projectId]
          return { commitByProject: next }
        }),

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

      setSortBy: (mode) => set({ sortBy: mode }),

      setGroupBy: (mode) => set({ groupBy: mode }),

      setPendingHistoryFilter: (filter) => set({ pendingHistoryFilter: filter }),

      clearPendingHistoryFilter: () => set({ pendingHistoryFilter: null }),

      setPendingBranchBase: (base) => set({ pendingBranchBase: base }),

      clearPendingBranchBase: () => set({ pendingBranchBase: null }),

      reset: () =>
        set({
          ...initialState,
          expandedDirs: new Set<string>(),
          // Fresh empty map — never share the initial-state object across resets.
          commitByProject: {},
        }),
    }),
    {
      name: 'git-panel-settings',
      partialize: partializeGitPanel,
      merge: mergeGitPanel,
    },
  ),
)
