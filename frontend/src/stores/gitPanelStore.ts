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

/**
 * Active GitPanel tab. 'graph' was merged into 'history' (unified view);
 * 'files' hosts the workspace file explorer (FilterBar + tree) as the first
 * section, shown only for git-repository projects. Persisted per project.
 */
export type GitPanelTab = 'files' | 'changes' | 'history'

/** Valid SortBy values — used by persist `merge` to validate localStorage. */
const SORT_BY_VALUES = new Set<SortBy>(['path', 'status', 'extension'])

/** Valid GroupBy values — used by persist `merge` to validate localStorage. */
const GROUP_BY_VALUES = new Set<GroupBy>(['none', 'status', 'directory'])

/** Valid GitPanelTab values — used by persist `merge` to validate localStorage. */
const GIT_PANEL_TAB_VALUES = new Set<GitPanelTab>(['files', 'changes', 'history'])

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
  /**
   * Bounded combined stdout+stderr of the most recent commit spawn (hook /
   * signing output for trusted repositories). Non-empty only when the commit
   * produced output; drives the collapsed-by-default "hook output" section
   * under the success banner. Unlike the banner it OUTLIVES the banner's
   * auto-dismissal (a collapsed log the user may expand later must not
   * vanish after 4s mid-read) — it is replaced by the next commit instead.
   */
  lastCommitOutput: string | null
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
  lastCommitOutput: null,
}

// --- State types ---

/**
 * Auto-dismissal delay for the per-project commit success banner (FE-1).
 */
export const COMMIT_BANNER_DISMISS_MS = 4000

/**
 * Pending per-project banner-dismissal timers. Module-level (not store
 * state): timers are imperative runtime objects, not serializable state.
 * Keyed by project id so banners in different projects dismiss
 * independently — a single component-owned timer cleared on every new
 * commit (or killed by a CHAT-mode GitPanel unmount) left earlier projects'
 * banners stuck until their next commit. Owned by the store so dismissal
 * survives unmounts and project switches alike.
 */
const commitBannerTimers = new Map<string, ReturnType<typeof setTimeout>>()

/** Cancel a project's pending banner-dismissal timer, if any. */
function clearCommitBannerTimer(projectId: string): void {
  const timer = commitBannerTimers.get(projectId)
  if (timer !== undefined) {
    clearTimeout(timer)
    commitBannerTimers.delete(projectId)
  }
}

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
  /**
   * The project id the current `isGitRepo` value was checked against. Null
   * when no check has completed for any project. Consumers must pair the two
   * fields (`isGitRepo && gitRepoProjectId === activeProjectId`) so a stale
   * result from a previously active project never leaks into the layout
   * decision during rapid project switches.
   */
  gitRepoProjectId: string | null
  error: string | null
  /** True while a pull/push/fetch is running — blocks parallel remote ops (Phase 5). */
  remoteOperationInProgress: boolean
  /**
   * Active GitPanel tab per project, keyed by project id. Persisted so each
   * project's tab survives switch-away and CHAT↔CODE mode switches. Absent
   * keys default to 'files' (see selectGitPanelTab). 'graph' was merged into
   * 'history' (unified view); 'files' hosts the workspace file explorer
   * (FilterBar + tree) as the first section, shown only for git repositories.
   */
  activeTabByProject: Record<string, GitPanelTab>
  /**
   * Per-project "Don't ask again for this project" flag for the commit
   * suppression dialog: true means an armed-but-untrusted repository commits
   * hardened (force) without showing the Trust/continue dialog first.
   * Persisted like activeTabByProject so the decision survives restarts.
   * Keyed by project id, NOT by repository root: the active project's
   * workspace IS the repository the decision applies to.
   */
  skipCommitSuppressByProject: Record<string, boolean>
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
   * success banner), clear that project's draft message, remember the
   * bounded commit output (drives the collapsed "hook output" section), and
   * arm a per-project auto-dismissal timer (COMMIT_BANNER_DISMISS_MS) that
   * clears the banner again — unless a newer commit replaced it first.
   * Passing `null` clears the banner immediately (manual dismissal) and
   * keeps the draft.
   */
  setCommitSuccess: (projectId: string, sha: string | null, output?: string) => void
  /** Drop a project's commit-box state entirely (project deleted). */
  dropProjectCommitState: (projectId: string) => void
  setGitRepo: (isRepo: boolean, projectId: string | null) => void
  setLoading: (loading: boolean) => void
  setError: (error: string | null) => void
  toggleExpandedDir: (dir: string) => void
  /** Replace the entire expanded-dirs set (used by expand-all / collapse-all). */
  setExpandedDirs: (dirs: Set<string>) => void
  setRemoteOperationInProgress: (inProgress: boolean) => void
  /**
   * Set a project's active GitPanel tab. Spread-updates only that project's
   * entry; a no-op (reference-stable) when the value is unchanged.
   */
  setActiveTab: (projectId: string, tab: GitPanelTab) => void
  /** Drop a project's active GitPanel tab entirely (project deleted). */
  dropProjectTabs: (projectId: string) => void
  /**
   * Set the per-project "commit without asking about suppressed
   * hooks/signing" flag. Setting true makes the next armed-but-untrusted
   * commit go straight to the hardened force commit (no dialog); false
   * restores the dialog. A no-op (reference-stable) when unchanged.
   */
  setSkipCommitSuppress: (projectId: string, skip: boolean) => void
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
  gitRepoProjectId: null,
  isBranchPickerOpen: false,
  error: null,
  remoteOperationInProgress: false,
  activeTabByProject: {},
  skipCommitSuppressByProject: {},
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
  activeTabByProject: Record<string, GitPanelTab>
  skipCommitSuppressByProject: Record<string, boolean>
} {
  return {
    viewMode: state.viewMode,
    expandedDirs: Array.from(state.expandedDirs),
    sortBy: state.sortBy,
    groupBy: state.groupBy,
    activeTabByProject: state.activeTabByProject,
    skipCommitSuppressByProject: state.skipCommitSuppressByProject,
  }
}

/**
 * Rehydrate persisted state into the current state. Older localStorage entries
 * (written before D8) lack `sortBy`/`groupBy`; corrupt or unknown values are
 * rejected — both fall back to the current (default) values.
 *
 * `activeTabByProject` is validated entry-by-entry: an absent map (legacy
 * state written before it existed) rehydrates to `{}`, and any entry whose
 * tab value is not a known GitPanelTab is dropped rather than trusted.
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
    activeTabByProject?: Record<string, unknown>
    skipCommitSuppressByProject?: Record<string, unknown>
  }
  const sortBy: SortBy =
    p.sortBy !== undefined && SORT_BY_VALUES.has(p.sortBy)
      ? p.sortBy
      : current.sortBy
  const groupBy: GroupBy =
    p.groupBy !== undefined && GROUP_BY_VALUES.has(p.groupBy)
      ? p.groupBy
      : current.groupBy
  const activeTabByProject: Record<string, GitPanelTab> = {}
  if (
    p.activeTabByProject !== null &&
    typeof p.activeTabByProject === 'object'
  ) {
    for (const [projectId, tab] of Object.entries(p.activeTabByProject)) {
      if (GIT_PANEL_TAB_VALUES.has(tab as GitPanelTab)) {
        activeTabByProject[projectId] = tab as GitPanelTab
      }
    }
  }
  // Same entry-by-entry validation as the tab map: an absent map (legacy
  // localStorage) rehydrates to {}, and any non-boolean value is dropped
  // rather than trusted (fail-closed: the dialog re-appears).
  const skipCommitSuppressByProject: Record<string, boolean> = {}
  if (
    p.skipCommitSuppressByProject !== null &&
    typeof p.skipCommitSuppressByProject === 'object'
  ) {
    for (const [projectId, skip] of Object.entries(p.skipCommitSuppressByProject)) {
      if (typeof skip === 'boolean') {
        skipCommitSuppressByProject[projectId] = skip
      }
    }
  }
  return {
    ...current,
    viewMode: p.viewMode ?? current.viewMode,
    expandedDirs: new Set(p.expandedDirs ?? []),
    sortBy,
    groupBy,
    activeTabByProject,
    skipCommitSuppressByProject,
  }
}

/**
 * Pure selector for a project's active GitPanel tab. Returns 'files' when the
 * project has no recorded tab (the default) or when no project is active —
 * safe to call with a null/undefined project id during CHAT mode.
 */
export function selectGitPanelTab(
  state: Pick<GitPanelState, 'activeTabByProject'>,
  projectId: string | null | undefined,
): GitPanelTab {
  if (projectId === null || projectId === undefined) return 'files'
  return state.activeTabByProject[projectId] ?? 'files'
}

/**
 * Pure selector for a project's "commit hardened without the suppression
 * dialog" flag. Returns false when the project has no recorded decision or
 * no project is active — the fail-closed default that keeps the Trust
 * dialog appearing. Returns a primitive, so it is safe as a selector return
 * value (no allocation).
 */
export function selectSkipCommitSuppress(
  state: Pick<GitPanelState, 'skipCommitSuppressByProject'>,
  projectId: string | null | undefined,
): boolean {
  if (projectId === null || projectId === undefined) return false
  return state.skipCommitSuppressByProject[projectId] === true
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

      setCommitSuccess: (projectId, sha, output) => {
        // A manual dismiss (null) or a newer commit replaces any pending
        // auto-dismissal for this project first.
        clearCommitBannerTimer(projectId)
        if (sha === null) {
          set((s) => withCommitDraft(s, projectId, { lastCommitSha: null }))
          return
        }
        set((s) =>
          withCommitDraft(s, projectId, {
            lastCommitSha: sha,
            // A real SHA marks a completed commit: consume the draft.
            // `null` only dismisses the banner and must not wipe a draft
            // the user may have started typing.
            message: '',
            // Bounded combined commit output (hook/signing lines). Replaced
            // by every new commit; deliberately NOT cleared by the banner's
            // auto-dismissal (see CommitDraftState.lastCommitOutput).
            lastCommitOutput: output ?? null,
          }),
        )
        const timer = setTimeout(() => {
          commitBannerTimers.delete(projectId)
          set((s) => {
            // Dismiss only the banner this timer was armed for: a newer
            // commit in the same project must not be clobbered.
            if (s.commitByProject[projectId]?.lastCommitSha !== sha) return s
            return withCommitDraft(s, projectId, { lastCommitSha: null })
          })
        }, COMMIT_BANNER_DISMISS_MS)
        commitBannerTimers.set(projectId, timer)
      },

      dropProjectCommitState: (projectId) => {
        clearCommitBannerTimer(projectId)
        set((s) => {
          const hasDraft = s.commitByProject[projectId] !== undefined
          const hasSkip =
            s.skipCommitSuppressByProject[projectId] !== undefined
          if (!hasDraft && !hasSkip) return s
          const patch: Partial<GitPanelState> = {}
          if (hasDraft) {
            const nextCommit = { ...s.commitByProject }
            delete nextCommit[projectId]
            patch.commitByProject = nextCommit
          }
          if (hasSkip) {
            const nextSkip = { ...s.skipCommitSuppressByProject }
            delete nextSkip[projectId]
            patch.skipCommitSuppressByProject = nextSkip
          }
          return patch
        })
      },

      setGitRepo: (isRepo, projectId) => set({ isGitRepo: isRepo, gitRepoProjectId: projectId }),

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

      setActiveTab: (projectId, tab) =>
        set((s) => {
          if (s.activeTabByProject[projectId] === tab) return s
          return {
            activeTabByProject: {
              ...s.activeTabByProject,
              [projectId]: tab,
            },
          }
        }),

      dropProjectTabs: (projectId) =>
        set((s) => {
          if (s.activeTabByProject[projectId] === undefined) return s
          const next = { ...s.activeTabByProject }
          delete next[projectId]
          return { activeTabByProject: next }
        }),

      setSkipCommitSuppress: (projectId, skip) =>
        set((s) => {
          if (s.skipCommitSuppressByProject[projectId] === skip) return s
          return {
            skipCommitSuppressByProject: {
              ...s.skipCommitSuppressByProject,
              [projectId]: skip,
            },
          }
        }),

      setMergeRebaseState: (state) => set({ mergeRebaseState: state }),

      setSortBy: (mode) => set({ sortBy: mode }),

      setGroupBy: (mode) => set({ groupBy: mode }),

      setPendingHistoryFilter: (filter) => set({ pendingHistoryFilter: filter }),

      clearPendingHistoryFilter: () => set({ pendingHistoryFilter: null }),

      setPendingBranchBase: (base) => set({ pendingBranchBase: base }),

      clearPendingBranchBase: () => set({ pendingBranchBase: null }),

      reset: () => {
        for (const projectId of commitBannerTimers.keys()) {
          clearCommitBannerTimer(projectId)
        }
        set({
          ...initialState,
          expandedDirs: new Set<string>(),
          // Fresh empty maps — never share the initial-state objects across resets.
          commitByProject: {},
          activeTabByProject: {},
          skipCommitSuppressByProject: {},
        })
      },
    }),
    {
      name: 'git-panel-settings',
      partialize: partializeGitPanel,
      merge: mergeGitPanel,
    },
  ),
)
