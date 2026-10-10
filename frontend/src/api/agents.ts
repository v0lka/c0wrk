// Agents (Subagent Profiles) API wrappers

import { getApp } from './runtime'
import { logger } from '@/lib/logger'
import { isAgentDescriptor, isArrayOf } from '@/types/guards'
import type { AgentDescriptor } from '@/types/models'

export async function listAgents(): Promise<AgentDescriptor[]> {
  try {
    const app = getApp()
    const result = await app.ListAgents()
    if (!isArrayOf(result, isAgentDescriptor)) {
      logger.warn('listAgents: unexpected response shape', result)
      return []
    }
    return result
  } catch (err) {
    logger.error('Failed to list agents:', err)
    throw err
  }
}
