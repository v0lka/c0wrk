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

/** Fetch the full output (body) of a single step lazily, on tooltip hover. */
export async function getStepOutput(sessionId: string, stepId: string): Promise<string> {
  try {
    const app = getApp()
    const result = await app.GetStepOutput(sessionId, stepId)
    return typeof result === 'string' ? result : ''
  } catch (err) {
    logger.error('Failed to get step output:', err)
    throw err
  }
}

/** Return the IDs of steps whose full output matches the query (server-side). */
export async function searchStepOutputs(sessionId: string, query: string): Promise<string[]> {
  try {
    const app = getApp()
    const result = await app.SearchBlackboardStepOutputs(sessionId, query)
    if (!Array.isArray(result)) return []
    return result.filter((id: unknown): id is string => typeof id === 'string')
  } catch (err) {
    logger.error('Failed to search step outputs:', err)
    throw err
  }
}
