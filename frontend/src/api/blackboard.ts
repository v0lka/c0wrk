// Blackboard state API wrapper

import { getApp } from './runtime'
import { logger } from '@/lib/logger'
import { isBlackboardState } from '@/types/guards'
import type { BlackboardState } from '@/types/models'

export async function getBlackboardState(sessionId: string): Promise<BlackboardState | null> {
  try {
    const app = getApp()
    const result = await app.GetBlackboardState(sessionId)
    if (result === null || result === undefined) {
      return null
    }
    if (!isBlackboardState(result)) {
      throw new Error('getBlackboardState: backend returned invalid data')
    }
    return result
  } catch (err) {
    logger.error('Failed to get blackboard state:', err)
    throw err
  }
}
