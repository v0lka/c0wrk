// Agents (Subagent Profiles) API wrappers

import { getApp } from './runtime'
import { logger } from '@/lib/logger'
import type { AgentDescriptor } from '@/types/models'

export async function listAgents(): Promise<AgentDescriptor[]> {
  try {
    const app = getApp()
    const result = await app.ListAgents()
    if (!Array.isArray(result)) {
      logger.warn('listAgents: unexpected response shape', result)
      return []
    }
    return result as AgentDescriptor[]
  } catch (err) {
    logger.error('Failed to list agents:', err)
    throw err
  }
}
