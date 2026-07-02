// Chat / task API wrappers

import { getApp } from './runtime'
import { logger } from '@/lib/logger'
import { isChatMessage, isTokenInfo, isArrayOf } from '@/types/guards'
import type { ChatMessage, TokenInfo } from '@/types/models'

export async function sendMessage(sessionId: string, text: string, mode: string, activeSkills: string[] = [], modelOverride: string = '', reasoningOverride: string = '', planReview: boolean = false): Promise<void> {
  try {
    const app = getApp()
    await app.SendMessage(sessionId, text, mode, activeSkills, modelOverride, reasoningOverride, planReview)
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
