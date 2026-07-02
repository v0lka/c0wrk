// Git API wrappers — all Wails RPC calls for git operations go through here

import { getApp } from './runtime'
import { logger } from '@/lib/logger'
import { isArrayOf } from '@/types/guards'
import type { Branch, DiffStat } from '@/types/models'

// --- Type guards ---

function isBranch(v: unknown): v is Branch {
  if (typeof v !== 'object' || v === null) return false
  return typeof (v as Record<string, unknown>).name === 'string' && typeof (v as Record<string, unknown>).is_current === 'boolean'
}

function isDiffStat(v: unknown): v is DiffStat {
  if (typeof v !== 'object' || v === null) return false
  return typeof (v as Record<string, unknown>).added === 'number' && typeof (v as Record<string, unknown>).deleted === 'number'
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

export async function commit(message: string): Promise<void> {
  try {
    const app = getApp()
    await app.Commit(message)
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

export async function getCurrentBranch(): Promise<string> {
  try {
    const app = getApp()
    const result = await app.GetCurrentBranch()
    if (typeof result !== 'string') {
      throw new Error('getCurrentBranch: backend returned non-string data')
    }
    return result
  } catch (err) {
    logger.error('getCurrentBranch failed:', err)
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
