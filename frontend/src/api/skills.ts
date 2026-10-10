// Skills API wrappers

import { getApp } from './runtime'
import { logger } from '@/lib/logger'
import { isSkillDescriptor, isArrayOf } from '@/types/guards'
import type { SkillDescriptor } from '@/types/models'

export async function listSkills(): Promise<SkillDescriptor[]> {
  try {
    const app = getApp()
    const result = await app.ListSkills()
    if (!isArrayOf(result, isSkillDescriptor)) {
      logger.warn('listSkills: unexpected response shape', result)
      return []
    }
    return result
  } catch (err) {
    logger.error('Failed to list skills:', err)
    throw err
  }
}
