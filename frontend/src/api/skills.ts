// Skills API wrappers

import { getApp } from './runtime'
import { logger } from '@/lib/logger'
import type { SkillDescriptor } from '@/types/models'

export async function listSkills(): Promise<SkillDescriptor[]> {
  try {
    const app = getApp()
    const result = await app.ListSkills()
    if (!Array.isArray(result)) {
      logger.warn('listSkills: unexpected response shape', result)
      return []
    }
    return result as SkillDescriptor[]
  } catch (err) {
    logger.error('Failed to list skills:', err)
    throw err
  }
}
