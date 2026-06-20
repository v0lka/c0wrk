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
