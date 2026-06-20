import { getApp } from './runtime'
import { logger } from '@/lib/logger'

export async function writeFile(sessionId: string, path: string, content: string): Promise<void> {
  try {
    const app = getApp()
    await app.WriteFile(sessionId, path, content)
  } catch (err) {
    logger.error('Failed to write file:', err)
    throw err
  }
}

export async function approvePlan(sessionId: string, planPath: string): Promise<void> {
  try {
    const app = getApp()
    await app.ApprovePlan(sessionId, planPath)
  } catch (err) {
    logger.error('Failed to approve plan:', err)
    throw err
  }
}

export async function rejectPlan(sessionId: string, feedback: string): Promise<void> {
  try {
    const app = getApp()
    await app.RejectPlan(sessionId, feedback)
  } catch (err) {
    logger.error('Failed to reject plan:', err)
    throw err
  }
}
