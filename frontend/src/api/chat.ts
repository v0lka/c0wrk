// Chat / task API wrappers

import { getApp } from './runtime'
import { logger } from '@/lib/logger'
import { isChatMessage, isTokenInfo, isArrayOf } from '@/types/guards'
import type { ChatMessage, TokenInfo } from '@/types/models'

export async function sendMessage(sessionId: string, text: string, activeSkills: string[] = [], modelOverride: string = '', reasoningOverride: string = ''): Promise<void> {
  try {
    const app = getApp()
    await app.SendMessage(sessionId, text, activeSkills, modelOverride, reasoningOverride)
  } catch (err) {
    logger.error('Failed to send message:', err)
    throw err
  }
}

export async function cancelTask(sessionId: string): Promise<void> {
  try {
    const app = getApp()
    await app.CancelTask(sessionId)
  } catch (err) {
    logger.error('Failed to cancel task:', err)
    throw err
  }
}

export async function getSessionHistory(sessionId: string): Promise<ChatMessage[]> {
  try {
    const app = getApp()
    const result = await app.GetSessionHistory(sessionId)
    if (!isArrayOf(result, isChatMessage)) {
      logger.error('getSessionHistory: unexpected response shape, returning []', result)
      return []
    }
    return result
  } catch (err) {
    logger.error('Failed to get session history:', err)
    throw err
  }
}

export async function getSessionTokens(sessionId: string): Promise<TokenInfo> {
  try {
    const app = getApp()
    const result = await app.GetSessionTokens(sessionId)
    if (!isTokenInfo(result)) {
      throw new Error('getSessionTokens: backend returned invalid data')
    }
    return result
  } catch (err) {
    logger.error('Failed to get session tokens:', err)
    throw err
  }
}

export async function resumeTask(sessionId: string): Promise<void> {
  try {
    const app = getApp()
    await app.ResumeTask(sessionId)
  } catch (err) {
    logger.error('Failed to resume task:', err)
    throw err
  }
}

export async function cancelUnfinishedTask(sessionId: string): Promise<void> {
  try {
    const app = getApp()
    await app.CancelUnfinishedTask(sessionId)
  } catch (err) {
    logger.error('Failed to cancel unfinished task:', err)
    throw err
  }
}

/** Live/persisted execution state of a session (see backend GetSessionRuntimeStatus). */
export interface SessionRuntimeStatus {
  active: boolean
  has_unfinished_task: boolean
  unfinished_task_id?: string
}

function isSessionRuntimeStatus(d: unknown): d is SessionRuntimeStatus {
  return typeof d === 'object' && d !== null
    && typeof (d as Record<string, unknown>).active === 'boolean'
    && typeof (d as Record<string, unknown>).has_unfinished_task === 'boolean'
}

/**
 * Query whether a task is running in the session and whether an unfinished
 * (resumable) task is persisted. Returns null on failure — callers must treat
 * null as "unknown", not as "idle".
 */
export async function getSessionRuntimeStatus(sessionId: string): Promise<SessionRuntimeStatus | null> {
  try {
    const app = getApp()
    const result = await app.GetSessionRuntimeStatus(sessionId)
    if (!isSessionRuntimeStatus(result)) {
      logger.error('getSessionRuntimeStatus: unexpected response shape', result)
      return null
    }
    return result
  } catch (err) {
    logger.error('Failed to get session runtime status:', err)
    return null
  }
}

// --- Pending HITL actions ---

export interface PendingToolConfirm {
  confirm_id: string
  tool: string
  args: string
  reasoning?: string
}

export interface PendingStepLimit {
  request_id: string
  current_step: number
  max_steps: number
  reason?: string
}

export interface PendingPlanApproval {
  request_id: string
  plan_path: string
  plan_content: string
}

export interface PendingAskUser {
  request_id: string
  questions: Array<{ id: string; question: string; options: Array<{ label: string; value: string }>; multi_select?: boolean; recommended?: string[] }>
}

export interface PendingActionsResponse {
  tool_confirms: PendingToolConfirm[]
  step_limits: PendingStepLimit[]
  plan_approvals: PendingPlanApproval[]
  ask_user: PendingAskUser[]
}

function isPendingActionsResponse(d: unknown): d is PendingActionsResponse {
  if (typeof d !== 'object' || d === null) return false
  const o = d as Record<string, unknown>
  return Array.isArray(o.tool_confirms) && Array.isArray(o.step_limits)
    && Array.isArray(o.plan_approvals) && Array.isArray(o.ask_user)
}

/**
 * Fetch all pending HITL prompts (tool_confirm, step_limit, plan_review,
 * ask_user) currently blocking a session's agent goroutine. Called on
 * session switch to resurface prompts whose events were missed while the
 * session was in the background, and to reconcile stale persisted prompts
 * (a persisted HITL message NOT in this response has already been resolved).
 */
export async function getPendingActions(sessionId: string): Promise<PendingActionsResponse | null> {
  try {
    const app = getApp()
    const result = await app.GetPendingActions(sessionId)
    if (!isPendingActionsResponse(result)) {
      logger.error('getPendingActions: unexpected response shape', result)
      return null
    }
    return result
  } catch (err) {
    logger.error('Failed to get pending actions:', err)
    return null
  }
}
