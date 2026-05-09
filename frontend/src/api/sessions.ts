// Session management API wrappers

import { getApp } from './runtime'
import { logger } from '@/lib/logger'
import { isSessionInfo, isArrayOf } from '@/types/guards'
import type { SessionInfo } from '@/types/models'

export async function createSession(): Promise<SessionInfo> {
  try {
    const app = getApp()
    const result = await app.CreateSession()
    if (!isSessionInfo(result)) {
      logger.warn('createSession: unexpected response shape', result)
    }
    return result as SessionInfo
  } catch (err) {
    logger.error('Failed to create session:', err)
    throw err
  }
}

export async function deleteSession(id: string): Promise<void> {
  try {
    const app = getApp()
    await app.DeleteSession(id)
  } catch (err) {
    logger.error('Failed to delete session:', err)
    throw err
  }
}

export async function listSessions(): Promise<SessionInfo[]> {
  try {
    const app = getApp()
    const result = await app.ListSessions()
    if (!isArrayOf(result, isSessionInfo)) {
      logger.warn('listSessions: unexpected response shape', result)
    }
    return result as SessionInfo[]
  } catch (err) {
    logger.error('Failed to list sessions:', err)
    throw err
  }
}

export async function renameSession(id: string, name: string): Promise<void> {
  try {
    const app = getApp()
    await app.RenameSession(id, name)
  } catch (err) {
    logger.error('Failed to rename session:', err)
    throw err
  }
}

export async function archiveSession(id: string): Promise<void> {
  try {
    const app = getApp()
    await app.ArchiveSession(id)
  } catch (err) {
    logger.error('Failed to archive session:', err)
    throw err
  }
}
