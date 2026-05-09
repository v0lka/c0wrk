// Chat / task API wrappers

import { getApp } from './runtime'
import { logger } from '@/lib/logger'
import { isChatMessage, isTokenInfo, isArrayOf } from '@/types/guards'
import type { ChatMessage, TokenInfo } from '@/types/models'

export async function sendMessage(sessionId: string, text: string, mode: string): Promise<void> {
  try {
    const app = getApp()
    await app.SendMessage(sessionId, text, mode)
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
      logger.warn('getSessionHistory: unexpected response shape', result)
    }
    return result as ChatMessage[]
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
      logger.warn('getSessionTokens: unexpected response shape', result)
    }
    return result as TokenInfo
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
