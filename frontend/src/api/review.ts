// Code review API wrappers — backend RPC calls for the review buffer and diff

import { getApp } from './runtime'
import { logger } from '@/lib/logger'
import { isArrayOf, isObj } from '@/types/guards'
function isStr(v: unknown): v is string {
  return typeof v === 'string'
}

export interface ReviewHunkComment {
  id: string
  session_id: string
  file_path: string
  hunk_id: string
  body: string
  created_at: string
}

export interface ReviewFileComment {
  id: string
  session_id: string
  file_path: string
  body: string
  created_at: string
}

export interface ReviewData {
  session_id: string
  status: string
  general_comment: string
  hunk_comments: ReviewHunkComment[]
  file_comments: ReviewFileComment[]
  updated_at: string
}

export interface ReviewHunk {
  raw: string
  old_start: number
  old_count: number
  new_start: number
  new_count: number
}

export interface ReviewFileDiff {
  path: string
  old_path?: string
  hunks: ReviewHunk[]
}

function isReviewComment(v: unknown): v is (ReviewHunkComment | ReviewFileComment) {
  return isObj(v)
    && isStr(v.id)
    && isStr(v.session_id)
    && isStr(v.file_path)
    && isStr(v.body)
    && isStr(v.created_at)
}

/** Element guard for one diff hunk: parseHunkRaw(hunk.raw, hunk.old_start,
 *  hunk.new_start) and the diff-equality pass read every field per element,
 *  so a malformed hunk must fail the file guard instead of throwing during
 *  the review render. */
function isReviewHunk(v: unknown): v is ReviewHunk {
  return isObj(v)
    && isStr(v.raw)
    && typeof v.old_start === 'number'
    && typeof v.old_count === 'number'
    && typeof v.new_start === 'number'
    && typeof v.new_count === 'number'
}

function isReviewData(v: unknown): v is ReviewData {
  return (
    isObj(v) &&
    isStr(v.session_id) &&
    isStr(v.status) &&
    isStr(v.general_comment) &&
    isArrayOf(v.hunk_comments, isReviewComment) &&
    isArrayOf(v.file_comments, isReviewComment)
  )
}

function isReviewFileDiff(v: unknown): v is ReviewFileDiff {
  return isObj(v) && isStr(v.path) && isArrayOf(v.hunks, isReviewHunk)
}

export async function getReview(sessionId: string): Promise<ReviewData> {
  try {
    const app = getApp()
    const result = await app.GetReview(sessionId)
    if (!isReviewData(result)) {
      throw new Error(`getReview: backend returned invalid shape for session ${sessionId}`)
    }
    return result
  } catch (err) {
    logger.error('getReview failed:', err)
    throw err
  }
}

export async function saveReviewGeneralComment(sessionId: string, body: string): Promise<void> {
  try {
    const app = getApp()
    await app.SaveReviewGeneralComment(sessionId, body)
  } catch (err) {
    logger.error('saveReviewGeneralComment failed:', err)
    throw err
  }
}

export async function saveReviewFileComment(sessionId: string, filePath: string, body: string): Promise<string> {
  try {
    const app = getApp()
    const result = await app.SaveReviewFileComment(sessionId, filePath, body)
    return typeof result === 'string' ? result : ''
  } catch (err) {
    logger.error('saveReviewFileComment failed:', err)
    throw err
  }
}

export async function saveReviewHunkComment(sessionId: string, filePath: string, hunkId: string, body: string): Promise<string> {
  try {
    const app = getApp()
    const result = await app.SaveReviewHunkComment(sessionId, filePath, hunkId, body)
    return typeof result === 'string' ? result : ''
  } catch (err) {
    logger.error('saveReviewHunkComment failed:', err)
    throw err
  }
}

export async function deleteReviewComment(id: string): Promise<void> {
  try {
    const app = getApp()
    await app.DeleteReviewComment(id)
  } catch (err) {
    logger.error('deleteReviewComment failed:', err)
    throw err
  }
}

export async function setReviewStatus(sessionId: string, status: string): Promise<void> {
  try {
    const app = getApp()
    await app.SetReviewStatus(sessionId, status)
  } catch (err) {
    logger.error('setReviewStatus failed:', err)
    throw err
  }
}

export async function clearReviewComments(sessionId: string): Promise<void> {
  try {
    const app = getApp()
    await app.ClearReviewComments(sessionId)
  } catch (err) {
    logger.error('clearReviewComments failed:', err)
    throw err
  }
}

export async function clearReview(sessionId: string): Promise<void> {
  try {
    const app = getApp()
    await app.ClearReview(sessionId)
  } catch (err) {
    logger.error('clearReview failed:', err)
    throw err
  }
}

export async function getReviewDiff(): Promise<ReviewFileDiff[]> {
  try {
    const app = getApp()
    const result = await app.GetReviewDiff()
    if (!isArrayOf(result, isReviewFileDiff)) return []
    return result
  } catch (err) {
    logger.error('getReviewDiff failed:', err)
    throw err
  }
}

export async function getCommitDiff(sha: string): Promise<ReviewFileDiff[]> {
  try {
    const app = getApp()
    const result = await app.GetCommitDiff(sha)
    if (!isArrayOf(result, isReviewFileDiff)) return []
    return result
  } catch (err) {
    logger.error('getCommitDiff failed:', err)
    throw err
  }
}
