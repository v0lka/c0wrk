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

export interface ReviewData {
  session_id: string
  status: string
  general_comment: string
  hunk_comments: ReviewHunkComment[]
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

function isReviewData(v: unknown): v is ReviewData {
  return isObj(v) && isStr(v.session_id) && isStr(v.status) && isStr(v.general_comment) && Array.isArray(v.hunk_comments)
}

function isReviewFileDiff(v: unknown): v is ReviewFileDiff {
  return isObj(v) && isStr(v.path) && Array.isArray(v.hunks)
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
