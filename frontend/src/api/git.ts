// Git API wrappers — all Wails RPC calls for git operations go through here

import { getApp } from './runtime'
import { logger } from '@/lib/logger'
import { isArrayOf } from '@/types/guards'
import type { Branch, BranchInfo, CommitInfo, CommitFile, DiffStat, StashEntry, GraphCommit, HunkRange, MergeRebaseState } from '@/types/models'

// --- Type guards ---

function isBranch(v: unknown): v is Branch {
  if (typeof v !== 'object' || v === null) return false
  return typeof (v as Record<string, unknown>).name === 'string' && typeof (v as Record<string, unknown>).is_current === 'boolean'
}

function isBranchInfo(v: unknown): v is BranchInfo {
  if (typeof v !== 'object' || v === null) return false
  const o = v as Record<string, unknown>
  return (
    typeof o.name === 'string' &&
    typeof o.upstream === 'string' &&
    typeof o.ahead === 'number' &&
    typeof o.behind === 'number'
  )
}

function isCommitInfo(v: unknown): v is CommitInfo {
  if (typeof v !== 'object' || v === null) return false
  const o = v as Record<string, unknown>
  return (
    typeof o.sha === 'string' &&
    typeof o.author === 'string' &&
    typeof o.email === 'string' &&
    typeof o.date === 'string' &&
    typeof o.message === 'string'
  )
}

function isCommitFile(v: unknown): v is CommitFile {
  if (typeof v !== 'object' || v === null) return false
  return (
    typeof (v as Record<string, unknown>).path === 'string' &&
    typeof (v as Record<string, unknown>).status === 'string'
  )
}

function isStashEntry(v: unknown): v is StashEntry {
  if (typeof v !== 'object' || v === null) return false
  return (
    typeof (v as Record<string, unknown>).index === 'number' &&
    typeof (v as Record<string, unknown>).message === 'string'
  )
}

function isDiffStat(v: unknown): v is DiffStat {
  if (typeof v !== 'object' || v === null) return false
  return typeof (v as Record<string, unknown>).added === 'number' && typeof (v as Record<string, unknown>).deleted === 'number'
}

function isGraphCommit(v: unknown): v is GraphCommit {
  if (typeof v !== 'object' || v === null) return false
  const o = v as Record<string, unknown>
  return (
    typeof o.sha === 'string' &&
    Array.isArray(o.parents) &&
    o.parents.every((p) => typeof p === 'string') &&
    typeof o.message === 'string' &&
    Array.isArray(o.refs) &&
    o.refs.every((r) => typeof r === 'string')
  )
}

function isHunkRange(v: unknown): v is HunkRange {
  if (typeof v !== 'object' || v === null) return false
  const o = v as Record<string, unknown>
  return typeof o.start_line === 'number' && typeof o.end_line === 'number'
}

function isMergeRebaseState(v: unknown): v is MergeRebaseState {
  if (typeof v !== 'object' || v === null) return false
  const o = v as Record<string, unknown>
  return typeof o.is_merging === 'boolean' && typeof o.is_rebasing === 'boolean'
}

// --- Staging operations ---

export async function stageFile(path: string): Promise<void> {
  try {
    const app = getApp()
    await app.StageFile(path)
  } catch (err) {
    logger.error('stageFile failed:', err)
    throw err
  }
}

export async function unstageFile(path: string): Promise<void> {
  try {
    const app = getApp()
    await app.UnstageFile(path)
  } catch (err) {
    logger.error('unstageFile failed:', err)
    throw err
  }
}

export async function stageAll(): Promise<void> {
  try {
    const app = getApp()
    await app.StageAll()
  } catch (err) {
    logger.error('stageAll failed:', err)
    throw err
  }
}

export async function unstageAll(): Promise<void> {
  try {
    const app = getApp()
    await app.UnstageAll()
  } catch (err) {
    logger.error('unstageAll failed:', err)
    throw err
  }
}

// --- Commit ---

/** Create a git commit with the given message and return the new commit's SHA. */
export async function commit(message: string): Promise<string> {
  try {
    const app = getApp()
    const result = await app.Commit(message)
    if (typeof result !== 'string' || result.length === 0) {
      throw new Error('commit: backend returned an invalid commit SHA')
    }
    return result
  } catch (err) {
    logger.error('commit failed:', err)
    throw err
  }
}

// --- Branches ---

export async function getBranches(): Promise<Branch[]> {
  try {
    const app = getApp()
    const result = await app.GetBranches()
    if (!isArrayOf(result, isBranch)) {
      logger.error('getBranches: unexpected response shape, returning []', result)
      return []
    }
    return result
  } catch (err) {
    logger.error('getBranches failed:', err)
    throw err
  }
}

export async function getCurrentBranch(): Promise<BranchInfo> {
  try {
    const app = getApp()
    const result = await app.GetCurrentBranch()
    if (!isBranchInfo(result)) {
      throw new Error('getCurrentBranch: backend returned invalid BranchInfo data')
    }
    return result
  } catch (err) {
    logger.error('getCurrentBranch failed:', err)
    throw err
  }
}

// --- Remote operations (Phase 5) ---
// An empty `remote` argument lets git use the configured upstream.

export async function pull(remote: string): Promise<string> {
  try {
    const app = getApp()
    const result = await app.Pull(remote)
    if (typeof result !== 'string') {
      throw new Error('pull: backend returned non-string output')
    }
    return result
  } catch (err) {
    logger.error('pull failed:', err)
    throw err
  }
}

export async function push(remote: string): Promise<string> {
  try {
    const app = getApp()
    const result = await app.Push(remote)
    if (typeof result !== 'string') {
      throw new Error('push: backend returned non-string output')
    }
    return result
  } catch (err) {
    logger.error('push failed:', err)
    throw err
  }
}

export async function fetch(remote: string): Promise<string> {
  try {
    const app = getApp()
    const result = await app.Fetch(remote)
    if (typeof result !== 'string') {
      throw new Error('fetch: backend returned non-string output')
    }
    return result
  } catch (err) {
    logger.error('fetch failed:', err)
    throw err
  }
}

// --- Commit history (Phase 5) ---

export async function getCommitLog(limit: number, skip: number): Promise<CommitInfo[]> {
  try {
    const app = getApp()
    const result = await app.GetCommitLog(limit, skip)
    if (!isArrayOf(result, isCommitInfo)) {
      logger.error('getCommitLog: unexpected response shape, returning []', result)
      return []
    }
    return result
  } catch (err) {
    logger.error('getCommitLog failed:', err)
    throw err
  }
}

export async function getCommitFiles(sha: string): Promise<CommitFile[]> {
  try {
    const app = getApp()
    const result = await app.GetCommitFiles(sha)
    if (!isArrayOf(result, isCommitFile)) {
      logger.error('getCommitFiles: unexpected response shape, returning []', result)
      return []
    }
    return result
  } catch (err) {
    logger.error('getCommitFiles failed:', err)
    throw err
  }
}

// --- Stash (Phase 5) ---

export async function stashCreate(message: string): Promise<void> {
  try {
    const app = getApp()
    await app.StashCreate(message)
  } catch (err) {
    logger.error('stashCreate failed:', err)
    throw err
  }
}

export async function stashPop(index: number): Promise<void> {
  try {
    const app = getApp()
    await app.StashPop(index)
  } catch (err) {
    logger.error('stashPop failed:', err)
    throw err
  }
}

/** Drop a stash entry by index (`git stash drop stash@{index}`). */
export async function stashDrop(index: number): Promise<void> {
  try {
    const app = getApp()
    await app.StashDrop(index)
  } catch (err) {
    logger.error('stashDrop failed:', err)
    throw err
  }
}

export async function stashList(): Promise<StashEntry[]> {
  try {
    const app = getApp()
    const result = await app.StashList()
    if (!isArrayOf(result, isStashEntry)) {
      logger.error('stashList: unexpected response shape, returning []', result)
      return []
    }
    return result
  } catch (err) {
    logger.error('stashList failed:', err)
    throw err
  }
}

export async function checkoutBranch(name: string): Promise<void> {
  try {
    const app = getApp()
    await app.CheckoutBranch(name)
  } catch (err) {
    logger.error('checkoutBranch failed:', err)
    throw err
  }
}

export async function createBranch(name: string): Promise<void> {
  try {
    const app = getApp()
    await app.CreateBranch(name)
  } catch (err) {
    logger.error('createBranch failed:', err)
    throw err
  }
}

// --- Diff statistics ---

export async function getDiffStat(path: string): Promise<DiffStat> {
  try {
    const app = getApp()
    const result = await app.GetDiffStat(path)
    if (!isDiffStat(result)) {
      throw new Error('getDiffStat: backend returned invalid data')
    }
    return result
  } catch (err) {
    logger.error('getDiffStat failed:', err)
    throw err
  }
}

/**
 * Batch-fetch added/deleted line counts for ALL uncommitted changes in a
 * single `git diff --numstat HEAD` call. Keys are absolute file paths,
 * matching the GitStatus key convention so entries can be enriched by a
 * direct path comparison. Returns an empty map when the tree is clean.
 */
export async function getDiffStats(): Promise<Record<string, DiffStat>> {
  try {
    const app = getApp()
    const result = await app.GetDiffStats()
    if (result === null || typeof result !== 'object') {
      logger.error('getDiffStats: unexpected response shape, returning {}', result)
      return {}
    }
    const map = result as Record<string, unknown>
    for (const value of Object.values(map)) {
      if (!isDiffStat(value)) {
        logger.error('getDiffStats: invalid DiffStat entry, returning {}', result)
        return {}
      }
    }
    return map as Record<string, DiffStat>
  } catch (err) {
    logger.error('getDiffStats failed:', err)
    throw err
  }
}

// --- AI commit message generation ---

export async function generateCommitMessage(diff: string): Promise<string> {
  try {
    const app = getApp()
    const result = await app.GenerateCommitMessage(diff)
    if (typeof result !== 'string') {
      throw new Error('generateCommitMessage: backend returned non-string data')
    }
    return result
  } catch (err) {
    logger.error('generateCommitMessage failed:', err)
    throw err
  }
}

// --- File context-menu operations (Phase 6) ---

/** Discard all local changes to a file (staged + unstaged). Requires user confirmation. */
export async function discardChanges(path: string): Promise<void> {
  try {
    const app = getApp()
    await app.DiscardChanges(path)
  } catch (err) {
    logger.error('discardChanges failed:', err)
    throw err
  }
}

/** Append a pattern to the repository-root `.gitignore` (creates it if missing). */
export async function appendToGitignore(pattern: string): Promise<void> {
  try {
    const app = getApp()
    await app.AppendToGitignore(pattern)
  } catch (err) {
    logger.error('appendToGitignore failed:', err)
    throw err
  }
}

/** Stage a subset of hunks for a single file (partial staging). */
export async function stageHunks(path: string, hunks: HunkRange[]): Promise<void> {
  if (!isArrayOf(hunks, isHunkRange)) {
    throw new Error('stageHunks: invalid hunk ranges')
  }
  try {
    const app = getApp()
    await app.StageHunks(path, hunks)
  } catch (err) {
    logger.error('stageHunks failed:', err)
    throw err
  }
}

// --- Merge / rebase workflow (Phase 6) ---

/** Merge `branch` into the current branch. Conflicts surface as an error. */
export async function merge(branch: string): Promise<void> {
  try {
    const app = getApp()
    await app.Merge(branch)
  } catch (err) {
    logger.error('merge failed:', err)
    throw err
  }
}

/** Rebase the current branch onto `branch`. Conflicts surface as an error. */
export async function rebase(branch: string): Promise<void> {
  try {
    const app = getApp()
    await app.Rebase(branch)
  } catch (err) {
    logger.error('rebase failed:', err)
    throw err
  }
}

/** Abort an in-progress merge. */
export async function abortMerge(): Promise<void> {
  try {
    const app = getApp()
    await app.AbortMerge()
  } catch (err) {
    logger.error('abortMerge failed:', err)
    throw err
  }
}

/** Abort an in-progress rebase. */
export async function abortRebase(): Promise<void> {
  try {
    const app = getApp()
    await app.AbortRebase()
  } catch (err) {
    logger.error('abortRebase failed:', err)
    throw err
  }
}

/** Detect whether a merge or rebase is currently in progress. */
export async function getRebaseMergeState(): Promise<MergeRebaseState> {
  try {
    const app = getApp()
    const result = await app.GetRebaseMergeState()
    if (!isMergeRebaseState(result)) {
      return { is_merging: false, is_rebasing: false }
    }
    return result
  } catch (err) {
    logger.error('getRebaseMergeState failed:', err)
    throw err
  }
}

// --- Commit graph (Phase 6) ---

/**
 * Fetch one page of the commit graph (SHAs, parents, messages, decorations)
 * for visualization. `limit` caps the page size and `skip` offsets from the
 * newest commit, enabling server-side lazy loading. A non-positive `limit`
 * defaults to 100 on the backend.
 */
export async function getGitGraph(limit: number, skip: number): Promise<GraphCommit[]> {
  try {
    const app = getApp()
    const result = await app.GetGitGraph(limit, skip)
    if (!isArrayOf(result, isGraphCommit)) {
      logger.error('getGitGraph: unexpected response shape, returning []', result)
      return []
    }
    return result
  } catch (err) {
    logger.error('getGitGraph failed:', err)
    throw err
  }
}
