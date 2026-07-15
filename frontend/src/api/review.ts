// Code review API wrappers — backend RPC calls for the review buffer and diff

import { getApp } from './runtime'
import { logger } from '@/lib/logger'
import { isArrayOf, isObj } from '@/types/guards'

function isStr(v: unknown): v is string {
  return typeof v === 'string'
}

function isReviewPromptResult(v: unknown): v is ReviewPromptResult {
  const r = v as Record<string, unknown> | null
  return isObj(r) && isStr(r.prompt_id) && isStr(r.content)
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

// Persisted review_prompt chat message. The prompt is injected client-side on
// a successful task_complete with uncommitted changes; SaveReviewPrompt stores
// it as a real session message (with a prompt_id in metadata) so it survives a
// session switch / restart instead of vanishing like the old frontend-only
// message. resolveReviewPrompt records the user's enter/decline decision on
// that same persisted message via ResolvePendingMessage keyed on prompt_id.
export type ReviewPromptDecision = 'enter' | 'decline'

// Descriptor of the persisted review_prompt message returned by SaveReviewPrompt.
// The content is the single source of truth (owned by the backend): the
// frontend renders the live card from this value rather than duplicating the
// string, so the in-memory and persisted wording always match.
export interface ReviewPromptResult {
  prompt_id: string
  content: string
}

export async function saveReviewPrompt(sessionId: string): Promise<ReviewPromptResult> {
  try {
    const app = getApp()
    const result = await app.SaveReviewPrompt(sessionId)
    if (!isReviewPromptResult(result)) {
      throw new Error(`saveReviewPrompt: backend returned invalid shape for session ${sessionId}`)
    }
    return result
  } catch (err) {
    logger.error('saveReviewPrompt failed:', err)
    throw err
  }
}

export async function resolveReviewPrompt(sessionId: string, promptId: string, decision: ReviewPromptDecision): Promise<void> {
  try {
    const app = getApp()
    await app.ResolvePendingMessage(sessionId, 'review_prompt', 'prompt_id', promptId, { resolved: true, decision })
  } catch (err) {
    // Best-effort: the optimistic UI update already happened, and a missed
    // persist is self-healing — the unresolved prompt just reappears on reload.
    logger.error('resolveReviewPrompt failed:', err)
  }
}
