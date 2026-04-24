// Session management API wrappers

import { getApp } from './runtime'
import { logger } from '@/lib/logger'
import type { SessionInfo } from '@/types/models'

export async function createSession(): Promise<SessionInfo> {
  try {
    const app = getApp()
    return await app.CreateSession() as SessionInfo
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
    return await app.ListSessions() as SessionInfo[]
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
