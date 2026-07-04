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
