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
      throw new Error('createSession: backend returned invalid data')
    }
    return result
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
      logger.error('listSessions: unexpected response shape, returning []', result)
      return []
    }
    return result
  } catch (err) {
    logger.error('Failed to list sessions:', err)
    throw err
  }
}

/** Sessions across ALL projects (global switcher), sorted pinned-first then by
 *  effective activity. Errors are handled exactly like listSessions(). */
export async function listAllSessions(): Promise<SessionInfo[]> {
  try {
    const app = getApp()
    const result = await app.ListAllSessions()
    if (!isArrayOf(result, isSessionInfo)) {
      logger.error('listAllSessions: unexpected response shape, returning []', result)
      return []
    }
    return result
  } catch (err) {
    logger.error('Failed to list all sessions:', err)
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

export async function pinSession(id: string): Promise<void> {
  try {
    const app = getApp()
    await app.PinSession(id)
  } catch (err) {
    logger.error('Failed to pin session:', err)
    throw err
  }
}

export async function forkSession(id: string): Promise<SessionInfo> {
  try {
    const app = getApp()
    const result = await app.ForkSession(id)
    if (!isSessionInfo(result)) {
      throw new Error('forkSession: backend returned invalid data')
    }
    return result
  } catch (err) {
    logger.error('Failed to fork session:', err)
    throw err
  }
}
