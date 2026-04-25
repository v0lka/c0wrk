// Blackboard state API wrapper

import { getApp } from './runtime'
import { logger } from '@/lib/logger'
import type { BlackboardState } from '@/types/models'

export async function getBlackboardState(sessionId: string): Promise<BlackboardState | null> {
  try {
    const app = getApp()
    const result = await app.GetBlackboardState(sessionId)
    return result as BlackboardState | null
  } catch (err) {
    logger.error('Failed to get blackboard state:', err)
    throw err
  }
}
